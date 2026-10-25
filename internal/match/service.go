package match

import (
	"sort"
	"strings"

	"zanbuddy/internal/model"
)

// Error 业务错误。与 demand/trade 同构：状态码、机器码、人话、提示在这一层定死，
// handler 只做搬运。三个域各有一个同名结构是刻意的——跨域共用会逼着所有域
// 依赖同一个包，而它们的错误码本来就不该互相知道。
type Error struct {
	Status int
	Code   string
	Msg    string
	Hint   string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// 召回与裁剪阶段的错误。分开报的原因和交易域一致：
// "这单不存在"、"这单已经有人接了"，两种情况下用户要做的事完全不同。
var (
	ErrNoTask    = &Error{404, "TASK_NOT_FOUND", "这条需求找不到了", ""}
	ErrTaskTaken = &Error{409, "TASK_NOT_OPEN", "这单已经不在等人接了",
		"已经有人接走，或者你把它撤回了。去「我发的单」看看它现在在哪一步。"}
)

// CardLimit 一次返回几张卡。PRD 是"不多不少三个"：
// 给十个让人挑，选择成本比没有推荐还高；给一个又变成平台替他决定。
const CardLimit = 3

// Store 召回需要的最小存储能力。
//
// 只声明用到的这几个方法，不直接依赖 store.Store：
// 这里没有"我要一个数据库"的需求，只有"我要这几样东西"。
// 好处是这个包的单测可以塞一个假实现进来，不需要起库。
type Store interface {
	NextID(prefix string) string
	GetTask(id string) (*model.Task, bool)
	ListProviders() []*model.User
	GetProfile(userID string) (*model.ProviderProfile, bool)
	ListOrdersByUser(userID string) []*model.Order
	ListDeliveriesByOrder(orderID string) []*model.Delivery
	ListReviewsByReviewee(userID string) []*model.Review
	// ListRevealedReviewsByReviewee 只返回已揭盲(或单方提交满 7 天)的评价。
	// 历史因子吃这个而不是全量: 未揭盲的评价连算分都不该参与,
	// 否则"先写的一方"能靠猜对方打几分来调整措辞。
	ListRevealedReviewsByReviewee(userID string, now int64) []*model.Review
	ListMatchesByTask(taskID string) []*model.MatchRecord
	PutMatch(m *model.MatchRecord)
	ReplaceMatchReasons(matchID string, rs []*model.MatchReason)
	// ListEndorsementsByTarget 收到的、可见且计权重的背书, 新的在前。
	// 过滤条件在存储层: 匹配要的是"能拿去说服需求方的凭据",
	// 被标记过的和零权重的都不该变成推荐理由。
	ListEndorsementsByTarget(targetID string) []*model.Endorsement
	// GetUser 理由里要写"谁给他背的书"。不具名的背书说服力减半——
	// 具名才有代价, 有代价的话才值得信。
	GetUser(id string) (*model.User, bool)
}

// Service 召回服务。
type Service struct {
	Store    Store
	Embedder Embedder
}

// NewService 装配。Embedder 为空时退回词袋保底（与 v1 的 embed 回退一致），
// 绝不因为没配 LLM 就让推荐位空白。
func NewService(st Store, emb Embedder) *Service {
	if emb == nil {
		emb = BagOfWordsEmbedder{}
	}
	return &Service{Store: st, Embedder: emb}
}

// Candidate 一个候选人。理由只在这里的内存结构里出现一次，
// 落库之后从 match_reason 读回来的是同一批（多了 id 与 rank_no）。
type Candidate struct {
	User     *model.User
	Profile  *model.ProviderProfile
	MatchID  string
	Score    float64
	Reasons  []Reason
	Stats    CandidateStats
	NowScore Score
	// Facts 生成理由用的凭据。必须一起带出去：
	// 卡上除了三条理由，还要摆一句客户的背书原话（primary_endorsement）——
	// 理由负责"为什么是他"，背书负责"有人愿意具名说这句话"。两者不是一回事。
	Facts Facts
}

// CandidateStats 卡片上的数字。算不出来的给 0，前端不显示——
// 编一个"平均 7 天交付"比不显示更糟。
type CandidateStats struct {
	Orders        int // 已完成订单数
	RepeatClients int // 回头客数（同一需求方在他这里下过 >=2 单）
	AvgDays       int // 平均交付天数（完成时间 - 下单时间）
}

// Candidates 给定需求单，返回最多 3 个候选人。
//
// 只有 open 的单能匹配：已经有人接了还推荐谁去接，点了也是 409，
// 那就成了"页面在骗人"。这不是权限问题，是状态问题，所以错误要分开报。
func (s *Service) Candidates(taskID string) ([]Candidate, error) {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return nil, ErrNoTask
	}
	if t.Status != model.TaskOpen {
		return nil, ErrTaskTaken
	}

	providers := s.Store.ListProviders()

	// 向量化批量做：需求侧一次，供应侧一次。
	// 逐人调用 Embed 的话，LLM 模式下就是 N 次 HTTP 往返，接口会慢到不能用。
	taskVec := s.embedOne(s.taskDoc(t))
	docs := make([]string, len(providers))
	for i, p := range providers {
		docs[i] = s.providerDoc(p)
	}
	vecs := s.embed(docs)

	cands := make([]Candidate, 0, len(providers))
	for i, p := range providers {
		// 自己发的单不推荐自己接。这条在 v1 也有，但那里是在推荐任务侧过滤，
		// 这里必须在推荐人侧再挡一次：需求方自己开了接单方账号，卡片上会出现他自己。
		if p.ID == t.RequesterID {
			continue
		}
		prof, _ := s.Store.GetProfile(p.ID)
		// 没有档案的人不进召回池。理由的锚点全部来自档案与历史订单，
		// 没档案的人连一条理由都产不出，推上去只能写废话。
		if prof == nil || len(prof.Skills) == 0 {
			continue
		}
		if prof.Availability == 0 {
			continue // 明确挂起接单的人不推，推了需求方点了没人应
		}

		skills := prof.Skills
		if len(skills) == 0 {
			skills = p.Skills
		}
		sc := Compute(s.taskDoc(t), Profile{
			Skills:      skills,
			CreditScore: p.CreditScore,
			TaxBound:    p.TaxBound,
			AvgRating:   s.avgRating(p.ID),
		}, taskVec.Cosine(vecs[i]))

		if !sc.Recalled() || sc.Total <= 0 {
			continue
		}

		stats, facts := s.collectFacts(p.ID)
		reasons := BuildReasons(sc, facts)
		// 0 条理由 = 说不出为什么是他。这种人按 PRD 不该被推荐：
		// 卡片上没有一句可指认的话, 需求方只能凭头像选人。
		if len(reasons) == 0 {
			continue
		}

		cands = append(cands, Candidate{
			User:     p,
			Profile:  prof,
			Score:    sc.Total,
			Reasons:  reasons,
			Stats:    stats,
			NowScore: sc,
			Facts:    facts,
		})
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].User.ID < cands[j].User.ID // 同分按 ID, 保证两次刷新顺序一致
	})
	if len(cands) > CardLimit {
		cands = cands[:CardLimit]
	}

	s.persist(t.ID, cands)
	return cands, nil
}

// persist 把这批推荐写进 match_record 与 match_reason。
//
// 幂等靠两件事：match 记录按 (task, provider) 复用原 ID 而不是每次新建；
// 理由按 match_id 整体替换。否则需求方每刷新一次页面，库里就多三个人三条理由，
// 事后回溯"当时推荐了谁"会看到一堆重复快照，分不清哪次是用户真正看到的。
func (s *Service) persist(taskID string, cands []Candidate) {
	existing := map[string]*model.MatchRecord{}
	for _, m := range s.Store.ListMatchesByTask(taskID) {
		existing[m.ProviderID] = m
	}
	for i := range cands {
		rec, ok := existing[cands[i].User.ID]
		if !ok {
			rec = &model.MatchRecord{
				ID:         s.Store.NextID("M"),
				TaskID:     taskID,
				ProviderID: cands[i].User.ID,
				Status:     model.MatchRecommended,
				CreatedAt:  model.Now(),
			}
		}
		rec.Score = cands[i].Score
		rec.Reason = Summary(cands[i].Reasons)
		if rec.Status == "" {
			rec.Status = model.MatchRecommended
		}
		s.Store.PutMatch(rec)
		cands[i].MatchID = rec.ID

		rows := make([]*model.MatchReason, 0, len(cands[i].Reasons))
		for k, r := range cands[i].Reasons {
			rows = append(rows, &model.MatchReason{
				ID:          s.Store.NextID("MR"),
				MatchID:     rec.ID,
				RankNo:      k + 1,
				Text:        r.Text,
				AnchorType:  r.AnchorType,
				AnchorRef:   r.AnchorRef,
				AnchorLabel: r.AnchorLabel,
				Factor:      r.Factor,
				FactorScore: r.FactorScore,
				CreatedAt:   model.Now(),
			})
		}
		s.Store.ReplaceMatchReasons(rec.ID, rows)
	}
}

// collectFacts 汇总一个人的凭据与统计。
//
// avg_days 只取最近 5 单算：要按单查交付记录，全量算等于每次推荐发 N 次查询，
// 而这个数字是卡片上的一个辅助信息，不值得把接口拖慢。
func (s *Service) collectFacts(providerID string) (CandidateStats, Facts) {
	var stats CandidateStats
	var facts Facts

	orders := s.Store.ListOrdersByUser(providerID)
	byRequester := map[string]int{}
	done := make([]*model.Order, 0, len(orders))
	for _, o := range orders {
		if o.ProviderID != providerID {
			continue // ListOrdersByUser 会连他作为需求方的单一起返回
		}
		if o.Status != model.OrderCompleted {
			continue
		}
		done = append(done, o)
		byRequester[o.RequesterID]++
	}
	stats.Orders = len(done)
	for _, n := range byRequester {
		if n >= 2 {
			stats.RepeatClients++
		}
	}

	if len(done) > 0 {
		// 最近一单：按创建时间取最大（订单号是自增前缀+序号，不可靠）
		latest := done[0]
		for _, o := range done[1:] {
			if o.CreatedAt > latest.CreatedAt {
				latest = o
			}
		}
		facts.CompletedOrders = len(done)
		facts.LatestOrderID = latest.ID
		if t, ok := s.Store.GetTask(latest.TaskID); ok {
			facts.LatestOrderTitle = t.Title
		}

		limit := len(done)
		if limit > 5 {
			limit = 5
		}
		var totalDays, counted int
		for _, o := range done[:limit] {
			at := s.completedAt(o.ID)
			if at <= 0 || o.CreatedAt <= 0 || at <= o.CreatedAt {
				continue
			}
			totalDays += int((at - o.CreatedAt) / 86400000)
			counted++
		}
		if counted > 0 {
			stats.AvgDays = totalDays / counted
		}
	}

	// 背书：只取最新一条当理由锚点，条数另算。
	//
	// 为什么不把几条都列进理由：一张卡最多 3 条理由，背书要占的位置是
	// "有人愿意具名为他说一句话"。列十条反而稀释——需求方记住的是一句话，不是十条标题。
	//
	// 存储层给的是"可见且计权重"（零权重与 void 已过滤），但 flagged 仍然在里面：
	// flagged 的语义是"还在等人工复核、页面照常展示并带标记"，不是"已经判定有问题"。
	// 展示与推荐是两件事——一条可能刷出来的话可以让人自己判断，不能拿去当平台推荐。
	// 所以这里再收一道 status == active。
	if es := rankableEndorsements(s.Store.ListEndorsementsByTarget(providerID)); len(es) > 0 {
		top := es[0]
		facts.EndorsementCount = len(es)
		facts.EndorsementID = top.ID
		facts.EndorsementQuote = top.Body
		facts.EndorsementRelation = top.Relation
		if u, ok := s.Store.GetUser(top.AuthorID); ok {
			facts.EndorsementAuthor = u.Name
			facts.EndorsementLabel = u.Name + " 的背书"
		} else {
			facts.EndorsementLabel = "一条背书"
		}
	}

	return stats, facts
}

// rankableEndorsements 从"可见且计权重"里再挑出能当推荐理由的那些。
// 只认 active：flagged 是待复核，void 已由存储层滤掉，这里再判一次 status 是防止
// 以后有人放宽存储层的过滤条件时，一条待复核的背书悄悄变成平台背书。
func rankableEndorsements(es []*model.Endorsement) []*model.Endorsement {
	out := make([]*model.Endorsement, 0, len(es))
	for _, e := range es {
		if e != nil && e.Status == model.EndorseActive && e.Weight > 0 {
			out = append(out, e)
		}
	}
	return out
}

// completedAt 订单被验收通过的时间，取最后一轮交付的 reviewed_at。
func (s *Service) completedAt(orderID string) int64 {
	ds := s.Store.ListDeliveriesByOrder(orderID)
	if len(ds) == 0 {
		return 0
	}
	return ds[len(ds)-1].ReviewedAt
}

// avgRating 历史互评均值, 只算已揭盲的评价。
//
// 为什么不算未揭盲那部分: 揭盲的意义就是"对方看不到我之前写了什么"。
// 如果未揭盲的评价偷偷进了分数, 这个分数就会通过推荐位和理由反推出去,
// 同时揭盲就成了摆设。
func (s *Service) avgRating(providerID string) float64 {
	revs := s.Store.ListRevealedReviewsByReviewee(providerID, model.Now())
	if len(revs) == 0 {
		return 0
	}
	sum := 0
	for _, r := range revs {
		sum += r.Rating
	}
	return float64(sum) / float64(len(revs))
}

// taskDoc 需求语义文档。字段顺序不影响余弦（词袋），但保持与 v1 一致便于对账。
func (s *Service) taskDoc(t *model.Task) string {
	return strings.ToLower(t.Category + " " + t.Title + " " + t.Spec + " " +
		t.SpecObj.Deliverables + " " + t.SpecObj.RequiredFormat + " " +
		t.SpecObj.StyleRef + " " + t.SpecObj.Samples)
}

// providerDoc 供应方语义文档：技能 + 名称 + 定位。
// 加上 headline 是因为 v2 的档案里它是"这个人做什么"的第一句人话，比技能裸词更有信息量。
func (s *Service) providerDoc(p *model.User) string {
	doc := strings.Join(p.Skills, " ") + " " + p.Name
	if prof, ok := s.Store.GetProfile(p.ID); ok && prof.Headline != "" {
		doc += " " + prof.Headline
	}
	return strings.ToLower(doc)
}

func (s *Service) embed(texts []string) []Embedding {
	vecs, err := s.Embedder.Embed(texts)
	if err != nil || len(vecs) != len(texts) {
		vecs, _ = BagOfWordsEmbedder{}.Embed(texts)
	}
	return vecs
}

func (s *Service) embedOne(text string) Embedding {
	return s.embed([]string{text})[0]
}
