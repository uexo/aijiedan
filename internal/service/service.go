// Package service 实现HiveGet M0 主链路业务逻辑:
// 发单 → AI撮合(规则召回+打分) → 接单 → 托管(闸门) → 交付 → 验收 → 结算(闸门)。
// 资金与IM均为 stub, 合规闸门通过 compliance.Service 在关键节点拦截。
package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"hiveget/internal/auth"
	"hiveget/internal/clarify"
	"hiveget/internal/compliance"
	"hiveget/internal/im"
	"hiveget/internal/kyc"
	"hiveget/internal/match"
	"hiveget/internal/middleware"
	"hiveget/internal/model"
	"hiveget/internal/notify"
	"hiveget/internal/payment"
	"hiveget/internal/quote"
	"hiveget/internal/store"
)

// Service 聚合各子系统, 供 handler 调用。
type Service struct {
	Store      store.Store
	Compliance *compliance.Service
	Payment    *payment.Service
	KYC        *kyc.Service
	IM         *im.Service
	Clarify    *clarify.Manager // P1-2 AI 澄清会话管理
	Notifier   *notify.Service  // P2-3 通知中心
	Embedder   match.Embedder   // M2 可插拔语义向量(词袋保底/可选LLM)
}

func New(st store.Store, comp *compliance.Service, pay *payment.Service, kycSvc *kyc.Service, imSvc *im.Service, cl *clarify.Manager, notifier *notify.Service) *Service {
	return &Service{Store: st, Compliance: comp, Payment: pay, KYC: kycSvc, IM: imSvc, Clarify: cl, Notifier: notifier, Embedder: match.NewEmbedder()}
}

// PublishResult 发单结果
type PublishResult struct {
	Task           *model.Task
	Clarifications []string          // 规则澄清问题(缺必填结构化字段时生成)
	Block          *compliance.Block // 合规拦截(非nil表示被拦)
}

// PublishTask 需求方发单。受 ActPublishTask 合规闸门约束(默认仅需KYC)。
// specObj 为结构化规格; 缺必填字段时生成 Clarifications(规则澄清兜底), 不阻塞发单。
func (s *Service) PublishTask(requesterID, title, category string, budgetCents int64, spec string, specObj model.TaskSpec) *PublishResult {
	u, ok := s.Store.GetUser(requesterID)
	if !ok {
		return &PublishResult{Block: &compliance.Block{Reason: "用户不存在"}}
	}
	if ok, b := s.Compliance.Check(compliance.ActPublishTask, u); !ok {
		return &PublishResult{Block: b}
	}
	// 类目白名单校验(空类目默认归为 other, 非法类目拦截)。
	if category == "" {
		category = "other"
	} else if !model.IsValidCategory(category) {
		return &PublishResult{Block: &compliance.Block{Reason: "类目非法，请从类目法列表中选择（GET /meta/categories）"}}
	}
	t := &model.Task{
		ID:          s.Store.NextID("T"),
		RequesterID: requesterID,
		Title:       title,
		Category:    category,
		BudgetCents: budgetCents,
		Spec:        spec,
		SpecObj:     specObj,
		Clarifications: model.ClarifySpec(specObj),
		Status:      model.TaskOpen,
		CreatedAt:   model.Now(),
	}
	s.Store.PutTask(t)
	return &PublishResult{Task: t, Clarifications: t.Clarifications}
}

// MatchTask AI撮合: 语义召回 → 多因子精排 → 排序 → TopN推荐。
// 召回层: 技能字面命中 或 语义余弦≥阈值 均可进入候选(解决无字面命中误淘汰)。
// 精排层: 技能命中 + 语义相似 + 信用分 + 税源 + 历史评分 多因子加权(0-100)。
func (s *Service) MatchTask(taskID string) []model.MatchRecord {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return nil
	}
	providers := s.Store.ListProviders()
	// M2: 语义层走 Embedder(词袋保底/可选LLM), 批量向量化需求侧 + 供应侧, 再 per-pair 精排。
	taskVec := s.embed([]string{s.taskDoc(t)})[0]
	provDocs := make([]string, len(providers))
	for i, p := range providers {
		provDocs[i] = s.providerDoc(p)
	}
	provVecs := s.embed(provDocs)
	recs := make([]model.MatchRecord, 0, len(providers))
	for i, p := range providers {
		score, reason := s.score(p, t, taskVec, provVecs[i])
		if score <= 0 {
			continue
		}
		recs = append(recs, model.MatchRecord{
			ID:         s.Store.NextID("M"),
			TaskID:     t.ID,
			ProviderID: p.ID,
			Score:      score,
			Reason:     reason,
			Status:     model.MatchRecommended,
			CreatedAt:  model.Now(),
		})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	if len(recs) > 5 {
		recs = recs[:5]
	}
	for i := range recs {
		s.Store.PutMatch(&recs[i])
	}
	return recs
}

// TaskRecommendation 推荐给接单方的任务(带匹配分与可解释理由)。
type TaskRecommendation struct {
	Task   *model.Task `json:"task"`
	Score  float64     `json:"score"`
	Reason string      `json:"reason"`
}

// RecommendTasks 推荐位(接单方首页 feed): 对全量 open 任务用与 MatchTask 相同的多因子精排打分,
// 返回 TopN(默认 10)高分任务。复用 score(p, t) 保证与撮合口径一致, 形成「好服务 → 高曝光」闭环。
// 自动排除接单方自己发布的任务(不能接自己的单)。
func (s *Service) RecommendTasks(providerID string) []TaskRecommendation {
	p, ok := s.Store.GetUser(providerID)
	if !ok {
		return nil
	}
	open := s.Store.ListOpenTasks()
	// 过滤掉自己发布的任务, 再批量向量化(需求侧), 供应侧向量只算一次。
	feeds := make([]*model.Task, 0, len(open))
	for _, t := range open {
		if t.RequesterID != providerID {
			feeds = append(feeds, t)
		}
	}
	taskDocs := make([]string, len(feeds))
	for i, t := range feeds {
		taskDocs[i] = s.taskDoc(t)
	}
	taskVecs := s.embed(taskDocs)
	provVec := s.embed([]string{s.providerDoc(p)})[0]
	recs := make([]TaskRecommendation, 0, len(feeds))
	for i, t := range feeds {
		score, reason := s.score(p, t, taskVecs[i], provVec)
		if score <= 0 {
			continue
		}
		recs = append(recs, TaskRecommendation{Task: t, Score: score, Reason: reason})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	if len(recs) > 10 {
		recs = recs[:10]
	}
	return recs
}

// ProviderRecommendation 推荐给需求方的接单方(带匹配分与可解释理由) —— 需求方首页 feed。
type ProviderRecommendation struct {
	Provider *PublicProfile `json:"provider"`
	Score    float64        `json:"score"`
	Reason   string         `json:"reason"`
}

// RecommendProviders 推荐位(需求方首页): 给定任务, 用与 AI 撮合相同的多因子精排(score)对全量接单方排序,
// 返回 TopN(默认 10)高匹配接单方。复用 score(p, t) 保证供需两侧撮合口径一致。
func (s *Service) RecommendProviders(taskID string) []ProviderRecommendation {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return nil
	}
	providers := s.Store.ListProviders()
	// M2: 需求侧向量一次, 供应侧批量向量化, 再 per-pair 精排。
	taskVec := s.embed([]string{s.taskDoc(t)})[0]
	provDocs := make([]string, len(providers))
	for i, p := range providers {
		provDocs[i] = s.providerDoc(p)
	}
	provVecs := s.embed(provDocs)
	recs := make([]ProviderRecommendation, 0, len(providers))
	for i, p := range providers {
		score, reason := s.score(p, t, taskVec, provVecs[i])
		if score <= 0 {
			continue
		}
		prof, err := s.GetPublicProfile(p.ID)
		if err != nil {
			continue
		}
		recs = append(recs, ProviderRecommendation{Provider: prof, Score: score, Reason: reason})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	if len(recs) > 10 {
		recs = recs[:10]
	}
	return recs
}

// taskDoc 需求语义文档: 类目/标题/描述/结构化规格拼接, 作为语义召回的需求侧向量源。
func (s *Service) taskDoc(t *model.Task) string {
	return strings.ToLower(t.Category + " " + t.Title + " " + t.Spec + " " +
		t.SpecObj.Deliverables + " " + t.SpecObj.RequiredFormat + " " +
		t.SpecObj.StyleRef + " " + t.SpecObj.Samples)
}

// providerDoc 接单方语义文档: 技能标签 + 名称 + 角色, 作为语义召回的供应侧向量源。
func (s *Service) providerDoc(p *model.User) string {
	return strings.ToLower(strings.Join(p.Skills, " ") + " " + p.Name + " " + string(p.Role))
}

// embed 批量向量化; LLM 模式调用失败自动回退词袋保底(BagOfWordsEmbedder 零依赖),
// 保证语义召回永不挂死 —— 与项目「Stub 默认 + env 切换真实 Provider」范式一致。
func (s *Service) embed(texts []string) []match.Embedding {
	vecs, err := s.Embedder.Embed(texts)
	if err != nil || len(vecs) != len(texts) {
		vecs, _ = match.BagOfWordsEmbedder{}.Embed(texts)
	}
	return vecs
}

// avgRating 接单方历史平均评分(0-5)。基于 review 表中其作为被评价方(接单方)的记录实时计算。
// P0-4 的评价数据由此反哺匹配权重, 形成「好服务 → 高评价 → 高曝光」反馈闭环。
func (s *Service) avgRating(providerID string) float64 {
	revs := s.Store.ListReviewsByReviewee(providerID)
	if len(revs) == 0 {
		return 0
	}
	sum := 0
	for _, r := range revs {
		sum += r.Rating
	}
	return float64(sum) / float64(len(revs))
}

// score 多因子精排(0-100)：
//   - 技能精确命中: 每个命中技能 +12, 封顶 30
//   - 语义余弦: cosine*25 (0-25)
//   - 信用分: credit/100*20 (0-20)
//   - 税源加成: 5
//   - 历史评分反馈: 平均评分/5*20 (0-20)
//
// 召回门槛: 技能未命中 且 语义<0.1 时返回 0(不推荐); 否则进入候选并精排。
// 这样即便技能标签未精确字面命中, 只要需求描述与技能画像语义接近(余弦≥0.1)仍可被召回。
func (s *Service) score(p *model.User, t *model.Task, taskVec, provVec match.Embedding) (float64, string) {
	hay := s.taskDoc(t)
	hits := make([]string, 0)
	for _, sk := range p.Skills {
		if strings.Contains(hay, strings.ToLower(sk)) {
			hits = append(hits, sk)
		}
	}
	skillScore := float64(len(hits)) * 12
	if skillScore > 30 {
		skillScore = 30
	}

	sem := taskVec.Cosine(provVec) // 0-1, 走 Embedder(词袋/LLM 统一)
	semScore := sem * 25

	creditScore := float64(p.CreditScore) / 100 * 20

	taxScore := 0.0
	if p.TaxBound {
		taxScore = 5
	}

	ar := s.avgRating(p.ID)
	ratingScore := ar / 5 * 20

	total := skillScore + semScore + creditScore + taxScore + ratingScore
	if total > 100 {
		total = 100
	}

	// 召回门槛: 无字面命中且语义极低 → 不推荐
	if len(hits) == 0 && sem < 0.1 {
		return 0, ""
	}

	reason := fmt.Sprintf("技能命中[%s]", strings.Join(hits, ","))
	reason += fmt.Sprintf(" 语义%.2f", sem)
	reason += fmt.Sprintf(" 信用%d", p.CreditScore)
	if p.TaxBound {
		reason += " 已绑税源"
	}
	reason += fmt.Sprintf(" 历史%.1f★", ar)
	return total, reason
}

// AcceptResult 接单结果
type AcceptResult struct {
	Order *model.Order
	Block *compliance.Block
}

// AcceptMatch 接单方应征推荐并生成订单(待托管)。受 ActAcceptTask 闸门(需KYC+税源)。
// milestones 可选: 传入则进入里程碑分阶段结算模式(金额之和须等于订单预算), 否则一次性结算。
func (s *Service) AcceptMatch(matchID, providerID string, milestones []model.Milestone, providerQuoteCents int64) *AcceptResult {
	m, ok := s.Store.GetMatch(matchID)
	if !ok {
		return &AcceptResult{Block: &compliance.Block{Reason: "匹配记录不存在"}}
	}
	if m.ProviderID != providerID {
		return &AcceptResult{Block: &compliance.Block{Reason: "非该接单方推荐"}}
	}
	u, ok := s.Store.GetUser(providerID)
	if !ok {
		return &AcceptResult{Block: &compliance.Block{Reason: "用户不存在"}}
	}
	if ok, b := s.Compliance.Check(compliance.ActAcceptTask, u); !ok {
		return &AcceptResult{Block: b}
	}
	m.Status = model.MatchAccepted
	s.Store.PutMatch(m)
	t, _ := s.Store.GetTask(m.TaskID)
	t.Status = model.TaskMatched
	s.Store.PutTask(t)

	mode := model.SettleLump
	var ms []model.Milestone
	if len(milestones) > 0 {
		var sum int64
		for _, m0 := range milestones {
			sum += m0.AmountCents
		}
		if sum != t.BudgetCents {
			return &AcceptResult{Block: &compliance.Block{Reason: fmt.Sprintf("里程碑金额之和(%d)必须等于订单预算(%d)", sum, t.BudgetCents)}}
		}
		mode = model.SettleMilestone
		for i := range milestones {
			milestones[i].ID = s.Store.NextID("MS")
			milestones[i].Status = model.MilestonePending
			ms = append(ms, milestones[i])
		}
	}

	o := &model.Order{
		ID:            s.Store.NextID("O"),
		TaskID:        t.ID,
		RequesterID:   t.RequesterID,
		ProviderID:    providerID,
		OrgID:         t.OrgID,
		MatchID:       m.ID,
		AmountCents:   t.BudgetCents,
		Status:        model.OrderPendingEscrow,
		MaxRevisions:  model.DefaultMaxRevisions,
		Milestones:    ms,
		SettlementMode: mode,
		CreatedAt:     model.Now(),
		ProviderQuoteCents: providerQuoteCents,
	}
	s.Store.PutOrder(o)
	// P2-2: 订单建立后自动建会话并推送系统通知, 双方可在 IM 沟通需求细节与交付标准。
	s.IM.SendSystem(o.RequesterID, o.ProviderID, o.ID, "【系统】订单已建立，可在会话中沟通需求细节与交付标准。")
	// P2-3: 通知双方接单成功(站内信)。
	s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "接单方已接单", fmt.Sprintf("接单方 %s 已应征订单 %s，请尽快完成资金托管。", providerID, o.ID))
	s.Notifier.Notify(o.ProviderID, model.ChannelInApp, "接单成功", fmt.Sprintf("你已成功接单 %s，等待需求方完成资金托管。", o.ID))
	return &AcceptResult{Order: o}
}

// SuggestQuote B2 自动报价：依据任务特征 + 接单方画像给出建议报价区间。
// providerID 用于取历史成交均价与互评星级；返回 nil 表示任务/用户不存在。
func (s *Service) SuggestQuote(taskID, providerID string) (*quote.Quote, error) {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return nil, fmt.Errorf("需求不存在")
	}
	if _, ok := s.Store.GetUser(providerID); !ok {
		return nil, fmt.Errorf("用户不存在")
	}
	avg := s.providerAvgNet(providerID)
	rating := s.avgRating(providerID)
	inp := quote.Input{
		Category:             t.Category,
		Spec:                 t.Spec + " " + t.SpecObj.Deliverables + " " + t.SpecObj.RequiredFormat,
		ItemsCount:           len(t.SpecObj.Items),
		DeadlineAt:           t.SpecObj.DeadlineAt,
		RequesterBudgetCents: t.BudgetCents,
		ProviderRating:       rating,
		ProviderAvgDealCents: avg,
	}
	return quote.Suggest(inp), nil
}

// providerAvgNet 接单方已完成且已结算订单的「净收入」均值(分)。
// 平台佣金 10%，接单方实收 = 订单金额 × 0.9。
func (s *Service) providerAvgNet(providerID string) int64 {
	orders := s.Store.ListOrdersByUser(providerID)
	var sum, n int64
	for _, o := range orders {
		if o.ProviderID == providerID && o.Status == model.OrderCompleted && o.Settled {
			sum += o.AmountCents * 9 / 10
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// TakeTask 接单方从需求大厅主动认领 open 任务并直接生成订单(待托管)。
// 与 AcceptMatch 的区别: 不经过 AI 推荐(match), 适合 provider 浏览大厅主动抢单。
// 受 ActAcceptTask 闸门(需 KYC+税源); 任务须为 open 且非本人发布; 里程碑金额和须等于预算; 先到先得(接单后任务转 matched)。
func (s *Service) TakeTask(taskID, providerID string, milestones []model.Milestone, providerQuoteCents int64) *AcceptResult {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return &AcceptResult{Block: &compliance.Block{Reason: "需求不存在"}}
	}
	if t.Status != model.TaskOpen {
		return &AcceptResult{Block: &compliance.Block{Reason: "该需求已被接单或不可接"}}
	}
	if t.RequesterID == providerID {
		return &AcceptResult{Block: &compliance.Block{Reason: "不能接自己发布的需求"}}
	}
	u, ok := s.Store.GetUser(providerID)
	if !ok {
		return &AcceptResult{Block: &compliance.Block{Reason: "用户不存在"}}
	}
	if ok, b := s.Compliance.Check(compliance.ActAcceptTask, u); !ok {
		return &AcceptResult{Block: b}
	}
	mode := model.SettleLump
	var ms []model.Milestone
	if len(milestones) > 0 {
		var sum int64
		for _, m0 := range milestones {
			sum += m0.AmountCents
		}
		if sum != t.BudgetCents {
			return &AcceptResult{Block: &compliance.Block{Reason: fmt.Sprintf("里程碑金额之和(%d)必须等于订单预算(%d)", sum, t.BudgetCents)}}
		}
		mode = model.SettleMilestone
		for i := range milestones {
			milestones[i].ID = s.Store.NextID("MS")
			milestones[i].Status = model.MilestonePending
			ms = append(ms, milestones[i])
		}
	}
	t.Status = model.TaskMatched
	s.Store.PutTask(t)
	o := &model.Order{
		ID:             s.Store.NextID("O"),
		TaskID:         t.ID,
		RequesterID:    t.RequesterID,
		ProviderID:     providerID,
		OrgID:          t.OrgID,
		MatchID:        "",
		AmountCents:    t.BudgetCents,
		Status:         model.OrderPendingEscrow,
		MaxRevisions:   model.DefaultMaxRevisions,
		Milestones:     ms,
		SettlementMode: mode,
		CreatedAt:      model.Now(),
		ProviderQuoteCents: providerQuoteCents,
	}
	s.Store.PutOrder(o)
	// P2-2: 订单建立后自动建会话并推送系统通知。
	s.IM.SendSystem(o.RequesterID, o.ProviderID, o.ID, "【系统】订单已建立，可在会话中沟通需求细节与交付标准。")
	// P2-3: 通知双方接单成功(站内信)。
	s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "接单方已接单", fmt.Sprintf("接单方 %s 已认领订单 %s，请尽快完成资金托管。", providerID, o.ID))
	s.Notifier.Notify(o.ProviderID, model.ChannelInApp, "接单成功", fmt.Sprintf("你已成功接单 %s，等待需求方完成资金托管。", o.ID))
	return &AcceptResult{Order: o}
}

// CreateEscrow 创建资金托管。受 ActCreateEscrow 闸门(需KYC+税源+支付托管开通)。
// 通过则订单进入进行中(active); 不通过则停在 pending_escrow。
func (s *Service) CreateEscrow(orderID string) *AcceptResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &AcceptResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	// 组织级发包: 合规在 org 层(组织存在性已在 CreateOrgTask 校验), 不依赖个人 KYC。
	if o.OrgID == "" {
		u, _ := s.Store.GetUser(o.RequesterID)
		if ok, b := s.Compliance.Check(compliance.ActCreateEscrow, u); !ok {
			return &AcceptResult{Block: b}
		}
	}
	// 组织级发包: 托管从组织钱包扣减(企业版统一结算主体)。
	if o.OrgID != "" {
		ow := s.Store.GetOrCreateOrgWallet(o.OrgID)
		if ow.BalanceCents < o.AmountCents {
			return &AcceptResult{Block: &compliance.Block{Reason: "组织钱包余额不足, 无法托管"}}
		}
		ow.BalanceCents -= o.AmountCents
		ow.FrozenCents += o.AmountCents
		s.Store.PutOrgWallet(ow)
		s.recordLedger(o.OrgID, o.ID, model.TxEscrow, -o.AmountCents, ow.BalanceCents, "组织托管冻结")
		o.EscrowID = "ESC-" + o.ID
		o.Status = model.OrderActive
		s.Store.PutOrder(o)
		return &AcceptResult{Order: o}
	}
	// P2-1 钱包记账: 个人需求方扣款并冻结托管资金
	reqW := s.Store.GetOrCreateWallet(o.RequesterID)
	if reqW.BalanceCents < o.AmountCents {
		return &AcceptResult{Block: &compliance.Block{Reason: "需求方钱包余额不足, 无法托管"}}
	}
	reqW.BalanceCents -= o.AmountCents
	reqW.FrozenCents += o.AmountCents
	s.Store.PutWallet(reqW)
	s.recordLedger(o.RequesterID, o.ID, model.TxEscrow, -o.AmountCents, reqW.BalanceCents, "需求方托管冻结")
	escrowID, err := s.Payment.Escrow(o.ID, o.AmountCents)
	if err != nil {
		// 回滚钱包扣款
		reqW.BalanceCents += o.AmountCents
		reqW.FrozenCents -= o.AmountCents
		s.Store.PutWallet(reqW)
		return &AcceptResult{Block: &compliance.Block{Reason: "托管创建失败: " + err.Error()}}
	}
	o.EscrowID = escrowID
	// 真实支付通道: 托管下单后订单停在 pending_escrow, 等待支付回调(OnPaymentNotify)再转 active;
	// 模拟通道(stub/dev): 同步记为已托管, 订单直接 active, 保证演示与冒烟连贯。
	if s.Payment.IsLive() {
		o.Status = model.OrderPendingEscrow
	} else {
		o.Status = model.OrderActive
	}
	s.Store.PutOrder(o)
	return &AcceptResult{Order: o}
}

// OnPaymentNotify 接收支付通道的异步结果回调(微信支付结果通知等)。
// 解析出订单号与支付成败; 若对应订单处于 pending_escrow 则推进为 active(托管生效)。
// 幂等: 订单已是 active 则视为成功并已处理, 直接返回 ok。
func (s *Service) OnPaymentNotify(body []byte) (orderID string, ok bool) {
	oid, paid := s.Payment.HandleNotify(body)
	if oid == "" {
		return "", false
	}
	o, exists := s.Store.GetOrder(oid)
	if !exists {
		return oid, false
	}
	if o.Status == model.OrderPendingEscrow && paid {
		o.Status = model.OrderActive
		s.Store.PutOrder(o)
	}
	return oid, true
}

// SubmitDelivery 接单方提交交付物(支持多轮, 受改稿轮次上限约束)。订单进入待验收(delivered)。
func (s *Service) SubmitDelivery(orderID, providerID, content string, items []model.DeliveryItem) (*model.Delivery, *compliance.Block) {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return nil, &compliance.Block{Reason: "订单不存在"}
	}
	if o.ProviderID != providerID {
		return nil, &compliance.Block{Reason: "非该订单接单方"}
	}
	if o.Status != model.OrderActive && o.Status != model.OrderDelivering {
		return nil, &compliance.Block{Reason: "订单状态不可交付: " + string(o.Status)}
	}
	round := len(s.Store.ListDeliveriesByOrder(orderID)) + 1
	if round > o.MaxRevisions {
		return nil, &compliance.Block{Reason: fmt.Sprintf("已达最大改稿轮次(%d), 请与需求方协商或发起纠纷", o.MaxRevisions)}
	}
	d := &model.Delivery{
		ID:        s.Store.NextID("D"),
		OrderID:   orderID,
		Round:     round,
		Content:   content,
		Items:     items,
		Status:    "submitted",
		CreatedAt: model.Now(),
	}
	s.Store.PutDelivery(d)
	o.Status = model.OrderDelivered
	s.Store.PutOrder(o)
	// P2-3: 通知需求方待验收。
	s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "待验收提醒", fmt.Sprintf("接单方已提交第 %d 轮交付，请验收。", round))
	return d, nil
}

// RequestRevision 需求方打回最新交付, 要求重做。订单回到 delivering, 接单方可再次提交(占用一轮改稿)。
func (s *Service) RequestRevision(orderID, requesterID, note string) *compliance.Block {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &compliance.Block{Reason: "订单不存在"}
	}
	if o.RequesterID != requesterID {
		return &compliance.Block{Reason: "非该订单需求方"}
	}
	if o.Status != model.OrderDelivered {
		return &compliance.Block{Reason: "仅待验收(delivered)状态可打回重做"}
	}
	ds := s.Store.ListDeliveriesByOrder(orderID)
	if len(ds) > 0 {
		ds[len(ds)-1].Status = "rejected"
		s.Store.PutDelivery(ds[len(ds)-1])
	}
	o.Status = model.OrderDelivering
	s.Store.PutOrder(o)
	return nil
}

// SettleResult 验收结算结果
type SettleResult struct {
	Order             *model.Order
	Released          bool
	Block             *compliance.Block // 合规拦截(非nil表示结算被拦, 订单转 pending_settlement)
	RevisionRequested bool              // 存在未达标项: 已打回接单方修正, 未结算
}

// AcceptDelivery 需求方验收交付。验收通过后尝试结算放款。
// items: 需求方对各需求项(Deliverables 清单)的逐项判定(留痕); severeMismatch: 是否判定需求与完成清单严重不符。
// 结算受 ActSettle 闸门(需KYC+税源+ICP备案+支付托管); 不通过则订单转 pending_settlement 等待合规落地。
func (s *Service) AcceptDelivery(orderID, requesterID string, items []model.AcceptanceItem, severeMismatch bool) *SettleResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &SettleResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	if o.SettlementMode == model.SettleMilestone {
		return &SettleResult{Block: &compliance.Block{Reason: "里程碑结算订单, 请使用阶段确认(confirm-milestone)而非一次性验收"}}
	}
	if o.RequesterID != requesterID {
		return &SettleResult{Block: &compliance.Block{Reason: "非该订单需求方"}}
	}
	if o.Status != model.OrderDelivered {
		return &SettleResult{Block: &compliance.Block{Reason: "订单未处于待验收状态"}}
	}
	// 记录验收逐项结果(留痕), 标记验收时间与严重不符判定。
	o.Acceptance = items
	o.SevereMismatch = severeMismatch
	o.AcceptedAt = model.Now()
	// 存在未达标项: 不结算、不打完成标签, 订单打回接单方修正后重新提交。
	var failed []model.AcceptanceItem
	if len(items) > 0 {
		for _, it := range items {
			if !it.Pass {
				failed = append(failed, it)
			}
		}
	}
	if len(failed) > 0 {
		ds := s.Store.ListDeliveriesByOrder(orderID)
		if len(ds) > 0 {
			ds[len(ds)-1].Status = "rejected"
			s.Store.PutDelivery(ds[len(ds)-1])
		}
		o.Status = model.OrderDelivering
		s.Store.PutOrder(o)
		var names []string
		for _, f := range failed {
			names = append(names, f.ItemID)
		}
		s.Notifier.Notify(o.ProviderID, model.ChannelInApp, "验收未达标",
			fmt.Sprintf("订单 %s 存在 %d 项未达标(%s)，请修正后重新提交交付。", o.ID, len(failed), strings.Join(names, "、")))
		return &SettleResult{Order: o, RevisionRequested: true}
	}
	// 全部达标: 标记最新交付为已接受
	ds := s.Store.ListDeliveriesByOrder(orderID)
	if len(ds) > 0 {
		ds[len(ds)-1].Status = "accepted"
		s.Store.PutDelivery(ds[len(ds)-1])
	}
	return s.Settle(o)
}

// Settle 结算放款(单独暴露, 供合规落地后补结算)。
func (s *Service) Settle(o *model.Order) *SettleResult {
	if o.Settled {
		return &SettleResult{Order: o, Released: true}
	}
	u, _ := s.Store.GetUser(o.RequesterID)
	if ok, b := s.Compliance.Check(compliance.ActSettle, u); !ok {
		o.Status = model.OrderPendingSettlement
		s.Store.PutOrder(o)
		// P2-3: 验收通过但结算被合规拦截, 通知需求方。
		s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "结算待合规", fmt.Sprintf("订单 %s 验收通过，但结算被合规拦截，待合规项落地后由运营补结算。", o.ID))
		return &SettleResult{Order: o, Block: b}
	}
	commission := payment.CommissionCents(o.AmountCents)
	toProvider := o.AmountCents - commission
	if err := s.Payment.Release(o.EscrowID, toProvider, commission); err != nil {
		return &SettleResult{Order: o, Block: &compliance.Block{Reason: "放款失败: " + err.Error()}}
	}
	// P2-1 钱包记账: 释放需求方冻结, 接单方+平台佣金入账
	s.releaseToWallets(o, toProvider, commission)
	o.Settled = true
	o.Status = model.OrderCompleted
	s.Store.PutOrder(o)
	// P2-3: 结算完成, 通知双方。
	s.Notifier.Notify(o.ProviderID, model.ChannelInApp, "结算完成", fmt.Sprintf("订单 %s 验收完成，已结算放款。", o.ID))
	s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "验收完成", fmt.Sprintf("订单 %s 已验收并结算。", o.ID))
	return &SettleResult{Order: o, Released: true}
}

// SendMessage 发送IM消息(经内容审核; 拦截时返回 (nil, reason))。
func (s *Service) SendMessage(from, to, body string) (*model.ChatMessage, string) {
	return s.IM.Send(from, to, body)
}

// ListConversations 当前用户的会话列表(含未读计数)。
func (s *Service) ListConversations(uid string) []*model.Conversation {
	return s.IM.ListConversations(uid)
}

// GetMessages 取与某 peer 的会话历史。
func (s *Service) GetMessages(uid, peer string) []*model.ChatMessage {
	return s.IM.GetMessages(uid, peer)
}

// MarkRead 将 uid 与 peer 会话中发给 uid 的未读清零, 返回清零条数。
func (s *Service) MarkRead(uid, peer string) int {
	return s.IM.MarkRead(uid, peer)
}

// ---- P2-3 通知中心 ----

// ListNotifications 当前用户的通知列表(按时间倒序, limit<=0 表示默认 50)。
func (s *Service) ListNotifications(uid string, limit int) []*model.Notification {
	if limit <= 0 {
		limit = 50
	}
	return s.Notifier.List(uid, limit)
}

// UnreadCount 当前用户未读通知数。
func (s *Service) UnreadCount(uid string) int {
	return s.Notifier.UnreadCount(uid)
}

// MarkNotificationRead 标记某条通知已读(须属于该用户), 返回更新后的通知; 不存在/越权返回 (nil,false)。
func (s *Service) MarkNotificationRead(uid, id string) (*model.Notification, bool) {
	return s.Notifier.MarkRead(uid, id)
}

// RegisterResult 注册结果
type RegisterResult struct {
	User *model.User
	Err  string
}

// Register 真实用户注册: 账号+密码(至少6位), 角色限定 requester/provider, 注册即视为已实名(M0简化)。
// 账号唯一; 注册后通过 /api/v1/auth/login 的账号密码方式登录获取 token。
func (s *Service) Register(account, password, roleStr, realName, phone string) *RegisterResult {
	if account == "" || password == "" {
		return &RegisterResult{Err: "账号和密码必填"}
	}
	if len(password) < 6 {
		return &RegisterResult{Err: "密码至少 6 位"}
	}
	if _, ok := s.Store.GetUserByAccount(account); ok {
		return &RegisterResult{Err: "账号已存在"}
	}
	var role model.Role
	switch roleStr {
	case string(model.RoleProvider):
		role = model.RoleProvider
	case string(model.RoleRequester):
		role = model.RoleRequester
	default:
		return &RegisterResult{Err: "角色仅支持 requester 或 provider"}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return &RegisterResult{Err: "密码加密失败"}
	}
	u := &model.User{
		ID:           s.Store.NextID("U"),
		Account:      account,
		PasswordHash: hash,
		RealName:     realName,
		Phone:        phone,
		Name:         realName,
		Role:         role,
		KYCVerified:  true, // M0: 注册即视为已实名(生产接真实KYC)
		CreditScore:  50,
		CreatedAt:    model.Now(),
	}
	s.Store.PutUser(u)
	return &RegisterResult{User: u}
}

// ReviewResult 评价提交结果
type ReviewResult struct {
	Review   *model.Review
	Reviewee *model.User // 被评价方更新后的信用分
	Err      string
}

// SubmitReview 验收完成后双向评价之一: 评价方(订单参与方)对被评价方打分。
// 规则:
//   - 订单必须已完成(completed)
//   - 评价方必须是订单参与方(需求方或接单方)
//   - 同一方对同订单只能评一次
//   - 评分 1-5
//   - 写入后重算被评价方 credit_score = round(mean(ratings)/5*100), 并持久化(可携带信用凭证)
func (s *Service) SubmitReview(orderID, reviewerID string, rating int, comment string) *ReviewResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &ReviewResult{Err: "订单不存在"}
	}
	if o.Status != model.OrderCompleted {
		return &ReviewResult{Err: "订单未完成, 不可评价(需 completed)"}
	}
	if reviewerID != o.RequesterID && reviewerID != o.ProviderID {
		return &ReviewResult{Err: "仅订单参与方可评价"}
	}
	for _, r := range s.Store.ListReviewsByOrder(orderID) {
		if r.ReviewerID == reviewerID {
			return &ReviewResult{Err: "您已评价, 不可重复"}
		}
	}
	if rating < 1 || rating > 5 {
		return &ReviewResult{Err: "评分需在 1-5 之间"}
	}
	revieweeID := o.ProviderID
	reviewerRole := model.RoleRequester
	if reviewerID == o.ProviderID {
		revieweeID = o.RequesterID
		reviewerRole = model.RoleProvider
	}
	r := &model.Review{
		ID:           s.Store.NextID("RV"),
		OrderID:      orderID,
		TaskID:       o.TaskID,
		ReviewerID:   reviewerID,
		RevieweeID:   revieweeID,
		ReviewerRole: reviewerRole,
		Rating:       rating,
		Comment:      comment,
		CreatedAt:    model.Now(),
	}
	s.Store.PutReview(r)

	// 重算被评价方信用分: 其全部被评价记录均分 → 0-100
	if reviewee, ok := s.Store.GetUser(revieweeID); ok {
		reviews := s.Store.ListReviewsByReviewee(revieweeID)
		if len(reviews) > 0 {
			sum := 0
			for _, rv := range reviews {
				sum += rv.Rating
			}
			reviewee.CreditScore = int(float64(sum) / float64(len(reviews)) / 5.0 * 100)
			s.Store.PutUser(reviewee)
		}
		return &ReviewResult{Review: r, Reviewee: reviewee}
	}
	return &ReviewResult{Review: r}
}

// ---- P1-4 订单完整化: 取消/退款、纠纷、里程碑确认 ----

// CancelResult 取消结果
type CancelResult struct {
	Order *model.Order
	Block *compliance.Block
}

// DisputeResult 纠纷/里程碑确认结果
type DisputeResult struct {
	Order *model.Order
	Block *compliance.Block
}

// CancelOrder 订单取消 + 按比例退款(stub)。
// 可取消状态: pending_escrow / active / delivering / delivered。
// 退款比例: 未托管(待托管)全额退; 已托管按 50% 退(已发生工作量)。
func (s *Service) CancelOrder(orderID, byUserID, reason string) *CancelResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &CancelResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	if o.RequesterID != byUserID && o.ProviderID != byUserID {
		return &CancelResult{Block: &compliance.Block{Reason: "仅订单参与方可取消"}}
	}
	switch o.Status {
	case model.OrderPendingEscrow, model.OrderActive, model.OrderDelivering, model.OrderDelivered:
		// 允许取消
	default:
		return &CancelResult{Block: &compliance.Block{Reason: "当前状态不可取消: " + string(o.Status)}}
	}
	pct := 100
	if o.Status != model.OrderPendingEscrow {
		pct = 50
	}
	refund := o.AmountCents * int64(pct) / 100
	if o.EscrowID != "" {
		if o.OrgID != "" {
			// 组织钱包: 释放冻结, 回补退款部分到组织余额
			ow := s.Store.GetOrCreateOrgWallet(o.OrgID)
			ow.FrozenCents -= o.AmountCents
			ow.BalanceCents += refund
			s.Store.PutOrgWallet(ow)
			s.recordLedger(o.OrgID, o.ID, model.TxRefund, refund, ow.BalanceCents, fmt.Sprintf("组织取消退款 %d%%", pct))
		} else {
			// 个人需求方: 释放冻结, 回补退款部分
			reqW := s.Store.GetOrCreateWallet(o.RequesterID)
			reqW.FrozenCents -= o.AmountCents
			reqW.BalanceCents += refund
			s.Store.PutWallet(reqW)
			s.recordLedger(o.RequesterID, o.ID, model.TxRefund, refund, reqW.BalanceCents, fmt.Sprintf("取消退款 %d%%", pct))
		}
		_ = s.Payment.Refund(o.EscrowID, refund)
	}
	o.RefundPct = pct
	o.RefundCents = refund
	o.CancelledBy = byUserID
	o.CancelReason = reason
	o.CancelledAt = model.Now()
	o.Status = model.OrderCancelled
	s.Store.PutOrder(o)
	return &CancelResult{Order: o}
}

// OpenDispute 发起纠纷。可发起状态: active / delivering / delivered / pending_settlement。
func (s *Service) OpenDispute(orderID, byUserID, reason string) *DisputeResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &DisputeResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	if o.RequesterID != byUserID && o.ProviderID != byUserID {
		return &DisputeResult{Block: &compliance.Block{Reason: "仅订单参与方可发起纠纷"}}
	}
	switch o.Status {
	case model.OrderActive, model.OrderDelivering, model.OrderDelivered, model.OrderPendingSettlement:
		// 允许发起
	default:
		return &DisputeResult{Block: &compliance.Block{Reason: "当前状态不可发起纠纷: " + string(o.Status)}}
	}
	o.DisputeReason = reason
	o.DisputedBy = byUserID
	o.DisputedAt = model.Now()
	o.Status = model.OrderDisputed
	s.Store.PutOrder(o)
	// P2-3: 通知对方与运营(仲裁)。
	other := o.RequesterID
	if byUserID == o.RequesterID {
		other = o.ProviderID
	}
	s.Notifier.Notify(other, model.ChannelInApp, "纠纷提醒", fmt.Sprintf("订单 %s 被 %s 发起纠纷：%s", o.ID, byUserID, reason))
	s.Notifier.Notify("A001", model.ChannelInApp, "纠纷待仲裁", fmt.Sprintf("订单 %s 发生纠纷，请尽快仲裁。", o.ID))
	return &DisputeResult{Order: o}
}

// ResolveDispute 运营仲裁结案。按 refundPct 退款需求方, 剩余(扣佣金)放接单方, 订单转 completed 并标记已结算。
func (s *Service) ResolveDispute(orderID, adminID string, refundPct int, decision string) *DisputeResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &DisputeResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	u, ok := s.Store.GetUser(adminID)
	if !ok {
		return &DisputeResult{Block: &compliance.Block{Reason: "处理人不存在"}}
	}
	if u.Role != model.RoleAdmin {
		return &DisputeResult{Block: &compliance.Block{Reason: "仅运营可处理纠纷"}}
	}
	if o.Status != model.OrderDisputed {
		return &DisputeResult{Block: &compliance.Block{Reason: "订单未处于纠纷状态"}}
	}
	if refundPct < 0 || refundPct > 100 {
		return &DisputeResult{Block: &compliance.Block{Reason: "退款比例需在 0-100"}}
	}
	refund := o.AmountCents * int64(refundPct) / 100
	commission := payment.CommissionCents(o.AmountCents)
	toProvider := o.AmountCents - refund - commission
	if o.EscrowID != "" {
		// P2-1 钱包记账: 释放需求方冻结, 退款退需求方, 剩余放接单方, 佣金入平台
		reqW := s.Store.GetOrCreateWallet(o.RequesterID)
		reqW.FrozenCents -= o.AmountCents
		reqW.BalanceCents += refund
		s.Store.PutWallet(reqW)
		s.recordLedger(o.RequesterID, o.ID, model.TxRefund, refund, reqW.BalanceCents, fmt.Sprintf("纠纷退款 %d%%", refundPct))
		if toProvider > 0 {
			provW := s.Store.GetOrCreateWallet(o.ProviderID)
			provW.BalanceCents += toProvider
			s.Store.PutWallet(provW)
			s.recordLedger(o.ProviderID, o.ID, model.TxSettle, toProvider, provW.BalanceCents, "纠纷结算放款")
			_ = s.Payment.Release(o.EscrowID, toProvider, commission)
		}
		platW := s.Store.GetOrCreateWallet("A001")
		platW.BalanceCents += commission
		s.Store.PutWallet(platW)
		s.recordLedger("A001", o.ID, model.TxCommission, commission, platW.BalanceCents, "平台佣金")
		if refund > 0 {
			_ = s.Payment.Refund(o.EscrowID, refund)
		}
	}
	o.RefundPct = refundPct
	o.RefundCents = refund
	o.ResolvedBy = adminID
	o.ResolvedAt = model.Now()
	o.ResolvedNote = decision
	o.Settled = true
	o.Status = model.OrderCompleted
	s.Store.PutOrder(o)
	// P2-3: 通知双方纠纷结案。
	s.Notifier.Notify(o.RequesterID, model.ChannelInApp, "纠纷结案", fmt.Sprintf("订单 %s 纠纷已结案，退款比例 %d%%。", o.ID, refundPct))
	s.Notifier.Notify(o.ProviderID, model.ChannelInApp, "纠纷结案", fmt.Sprintf("订单 %s 纠纷已结案，退款比例 %d%%。", o.ID, refundPct))
	return &DisputeResult{Order: o}
}

// ConfirmMilestone 需求方确认某里程碑完成, 释放该阶段款项(扣佣金)。全部里程碑完成则订单 completed。
func (s *Service) ConfirmMilestone(orderID, requesterID, milestoneID string) *DisputeResult {
	o, ok := s.Store.GetOrder(orderID)
	if !ok {
		return &DisputeResult{Block: &compliance.Block{Reason: "订单不存在"}}
	}
	if o.RequesterID != requesterID {
		return &DisputeResult{Block: &compliance.Block{Reason: "仅需求方可确认里程碑"}}
	}
	if o.SettlementMode != model.SettleMilestone {
		return &DisputeResult{Block: &compliance.Block{Reason: "该订单非里程碑结算模式"}}
	}
	if o.Status != model.OrderActive && o.Status != model.OrderDelivering && o.Status != model.OrderDelivered {
		return &DisputeResult{Block: &compliance.Block{Reason: "当前状态不可确认里程碑: " + string(o.Status)}}
	}
	var target *model.Milestone
	for i := range o.Milestones {
		if o.Milestones[i].ID == milestoneID {
			target = &o.Milestones[i]
			break
		}
	}
	if target == nil {
		return &DisputeResult{Block: &compliance.Block{Reason: "里程碑不存在"}}
	}
	if target.Status == model.MilestoneDone {
		return &DisputeResult{Block: &compliance.Block{Reason: "该里程碑已完成"}}
	}
	commission := payment.CommissionCents(target.AmountCents)
	toProvider := target.AmountCents - commission
	if o.EscrowID != "" && toProvider > 0 {
		// P2-1 钱包记账: 释放该阶段需求方冻结, 接单方+平台佣金入账
		reqW := s.Store.GetOrCreateWallet(o.RequesterID)
		reqW.FrozenCents -= target.AmountCents
		s.Store.PutWallet(reqW)
		provW := s.Store.GetOrCreateWallet(o.ProviderID)
		provW.BalanceCents += toProvider
		s.Store.PutWallet(provW)
		s.recordLedger(o.ProviderID, o.ID, model.TxSettle, toProvider, provW.BalanceCents, "里程碑放款 "+target.Title)
		platW := s.Store.GetOrCreateWallet("A001")
		platW.BalanceCents += commission
		s.Store.PutWallet(platW)
		s.recordLedger("A001", o.ID, model.TxCommission, commission, platW.BalanceCents, "平台佣金(里程碑)")
		_ = s.Payment.Release(o.EscrowID, toProvider, commission)
	}
	target.Status = model.MilestoneDone
	target.DoneAt = model.Now()
	allDone := true
	for _, m := range o.Milestones {
		if m.Status != model.MilestoneDone {
			allDone = false
			break
		}
	}
	o.Settled = allDone
	if allDone {
		o.Status = model.OrderCompleted
	} else {
		o.Status = model.OrderDelivering
	}
	s.Store.PutOrder(o)
	return &DisputeResult{Order: o}
}

// ---- P2-1 钱包/流水/提现 ----

// WalletView 钱包视图: 余额/冻结 + 最近流水
type WalletView struct {
	Wallet *model.Wallet   `json:"wallet"`
	Ledger []*model.Ledger `json:"ledger"`
}

// WithdrawResult 提现结果
type WithdrawResult struct {
	Withdrawal *model.Withdrawal
	Block      *compliance.Block
}

// recordLedger 记一笔资金流水(内部辅助)
func (s *Service) recordLedger(uid, orderID, txType string, amount, balanceAfter int64, note string) {
	l := &model.Ledger{
		ID:           s.Store.NextID("L"),
		UID:          uid,
		OrderID:      orderID,
		Type:         txType,
		AmountCents:  amount,
		BalanceAfter: balanceAfter,
		Note:         note,
		CreatedAt:    model.Now(),
	}
	s.Store.PutLedger(l)
}

// releaseToWallets 整单结算: 释放需求方冻结, 接单方+平台佣金入账(供 Settle 复用)
func (s *Service) releaseToWallets(o *model.Order, toProvider, commission int64) {
	if o.EscrowID == "" {
		return
	}
	if o.OrgID != "" {
		// 组织订单: 解冻组织钱包托管资金
		ow := s.Store.GetOrCreateOrgWallet(o.OrgID)
		ow.FrozenCents -= o.AmountCents
		s.Store.PutOrgWallet(ow)
	} else {
		reqW := s.Store.GetOrCreateWallet(o.RequesterID)
		reqW.FrozenCents -= o.AmountCents
		s.Store.PutWallet(reqW)
	}
	provW := s.Store.GetOrCreateWallet(o.ProviderID)
	provW.BalanceCents += toProvider
	s.Store.PutWallet(provW)
	s.recordLedger(o.ProviderID, o.ID, model.TxSettle, toProvider, provW.BalanceCents, "结算放款")
	platW := s.Store.GetOrCreateWallet("A001")
	platW.BalanceCents += commission
	s.Store.PutWallet(platW)
	s.recordLedger("A001", o.ID, model.TxCommission, commission, platW.BalanceCents, "平台佣金")
	if o.OrgID != "" {
		s.DispatchWebhook(o.OrgID, "order_settled", map[string]interface{}{"order_id": o.ID, "amount_cents": toProvider})
	}
}

// GetWallet 查询当前用户钱包与最近流水
func (s *Service) GetWallet(uid string) *WalletView {
	w, ok := s.Store.GetWallet(uid)
	if !ok {
		w = s.Store.GetOrCreateWallet(uid)
	}
	ledger := s.Store.ListLedgerByUser(uid)
	if len(ledger) > 20 {
		ledger = ledger[:20]
	}
	return &WalletView{Wallet: w, Ledger: ledger}
}

// RequestWithdraw 接单方提交提现申请(预占冻结, 待 admin 审批)
func (s *Service) RequestWithdraw(uid string, amountCents int64) *WithdrawResult {
	u, ok := s.Store.GetUser(uid)
	if !ok {
		return &WithdrawResult{Block: &compliance.Block{Reason: "用户不存在"}}
	}
	if u.Role != model.RoleProvider {
		return &WithdrawResult{Block: &compliance.Block{Reason: "仅接单方可申请提现"}}
	}
	if amountCents <= 0 {
		return &WithdrawResult{Block: &compliance.Block{Reason: "提现金额需为正"}}
	}
	w := s.Store.GetOrCreateWallet(uid)
	if w.BalanceCents < amountCents {
		return &WithdrawResult{Block: &compliance.Block{Reason: "可提现余额不足"}}
	}
	w.FrozenCents += amountCents // 冻结预占(未真正出账)
	s.Store.PutWallet(w)
	wd := &model.Withdrawal{
		ID:          s.Store.NextID("WD"),
		UID:         uid,
		AmountCents: amountCents,
		Status:      model.WithdrawPending,
		CreatedAt:   model.Now(),
	}
	s.Store.PutWithdrawal(wd)
	return &WithdrawResult{Withdrawal: wd}
}

// ApproveWithdraw admin 审批提现: 通过则真实出账(余额扣减+解冻), 驳回则解冻回可用。
// TODO(C3): 真实实现需调用微信支付「企业付款到银行卡/零钱」, 并回写 ExternalID。
func (s *Service) ApproveWithdraw(id, adminID string, approve bool, note string) *WithdrawResult {
	u, ok := s.Store.GetUser(adminID)
	if !ok || u.Role != model.RoleAdmin {
		return &WithdrawResult{Block: &compliance.Block{Reason: "仅运营可审批提现"}}
	}
	wd, ok := s.Store.GetWithdrawal(id)
	if !ok {
		return &WithdrawResult{Block: &compliance.Block{Reason: "提现单不存在"}}
	}
	if wd.Status != model.WithdrawPending {
		return &WithdrawResult{Block: &compliance.Block{Reason: "该提现已处理"}}
	}
	w := s.Store.GetOrCreateWallet(wd.UID)
	if approve {
		w.BalanceCents -= wd.AmountCents
		w.FrozenCents -= wd.AmountCents
		s.Store.PutWallet(w)
		s.recordLedger(wd.UID, "", model.TxWithdraw, -wd.AmountCents, w.BalanceCents, "提现到账")
		wd.Status = model.WithdrawDone
	} else {
		w.FrozenCents -= wd.AmountCents // 驳回解冻回可用
		s.Store.PutWallet(w)
		wd.Status = model.WithdrawRejected
	}
	wd.ResolvedAt = model.Now()
	wd.Note = note
	s.Store.PutWithdrawal(wd)
	// P2-3: 通知提现申请人审批结果。
	if approve {
		s.Notifier.Notify(wd.UID, model.ChannelInApp, "提现已通过", fmt.Sprintf("提现 ¥%.2f 已通过并出账。", float64(wd.AmountCents)/100))
	} else {
		s.Notifier.Notify(wd.UID, model.ChannelInApp, "提现已驳回", "提现申请被运营驳回，金额已解冻回可用余额。")
	}
	return &WithdrawResult{Withdrawal: wd}
}

// ListWithdrawals admin 查看提现列表(pending 优先)
func (s *Service) ListWithdrawals(adminID string) ([]*model.Withdrawal, *compliance.Block) {
	u, ok := s.Store.GetUser(adminID)
	if !ok || u.Role != model.RoleAdmin {
		return nil, &compliance.Block{Reason: "仅运营可查看提现列表"}
	}
	all := s.Store.ListWithdrawals()
	var pending, others []*model.Withdrawal
	for _, w := range all {
		if w.Status == model.WithdrawPending {
			pending = append(pending, w)
		} else {
			others = append(others, w)
		}
	}
	return append(pending, others...), nil
}

// ---- P3 企业版治理(设计阶段最小骨架: 组织/成员/角色 RBAC) ----
// 说明: 仅落地组织创建/邀请/列表示意接口, 验证多租户数据模型与 RBAC 字段可用。
// 组织级发包/钱包/审批流等由 P3 专项落地, 不在 M0 强耦合。

// CreateOrg 由 uid 创建组织(成为 owner)。
func (s *Service) CreateOrg(ownerUID, name string) (*model.Org, error) {
	if _, ok := s.Store.GetUser(ownerUID); !ok {
		return nil, fmt.Errorf("用户不存在")
	}
	o := &model.Org{
		ID:        s.Store.NextID("ORG"),
		Name:      name,
		OwnerID:   ownerUID,
		Plan:      "free",
		Status:    "active",
		CreatedAt: model.Now(),
	}
	s.Store.SaveOrg(o)
	// 创建者默认成为 owner 成员。
	s.Store.SaveOrgMember(&model.OrgMember{OrgID: o.ID, UserID: ownerUID, Role: model.OrgRoleOwner, JoinedAt: o.CreatedAt})
	return o, nil
}

// InviteOrgMember 组织主/管理员邀请用户加入组织并赋予角色。
func (s *Service) InviteOrgMember(orgID, operatorUID, userID, role string) error {
	op, ok := s.Store.GetOrgMember(orgID, operatorUID)
	if !ok || (op.Role != model.OrgRoleOwner && op.Role != model.OrgRoleAdmin) {
		return fmt.Errorf("无权限邀请成员(需 owner/admin)")
	}
	if _, ok := s.Store.GetUser(userID); !ok {
		return fmt.Errorf("被邀请用户不存在")
	}
	switch role {
	case model.OrgRoleAdmin, model.OrgRoleMember, model.OrgRoleViewer:
	default:
		return fmt.Errorf("非法角色")
	}
	s.Store.SaveOrgMember(&model.OrgMember{OrgID: orgID, UserID: userID, Role: role, JoinedAt: model.Now()})
	return nil
}

// ListOrgs 列出某用户作为 owner 的组织。
func (s *Service) ListOrgs(ownerUID string) []*model.Org {
	return s.Store.ListOrgsByOwner(ownerUID)
}

// ListOrgMembers 列出组织成员。
func (s *Service) ListOrgMembers(orgID string) []*model.OrgMember {
	return s.Store.ListOrgMembers(orgID)
}

// ---- P3 专项: 组织钱包 / 组织级发包 / 审批流 / 开放 API + Webhook ----

// OrgWalletView 组织钱包视图(对外不含敏感字段)。
type OrgWalletView struct {
	OrgID        string `json:"org_id"`
	BalanceCents int64  `json:"balance_cents"`
	FrozenCents  int64  `json:"frozen_cents"`
}

// GetOrgWallet 查询组织钱包(成员可见)。
func (s *Service) GetOrgWallet(orgID string) *OrgWalletView {
	w, ok := s.Store.GetOrgWallet(orgID)
	if !ok {
		return &OrgWalletView{OrgID: orgID}
	}
	return &OrgWalletView{OrgID: w.OrgID, BalanceCents: w.BalanceCents, FrozenCents: w.FrozenCents}
}

// DepositOrgWallet 组织钱包充值(owner/admin; 演示资金入口)。
func (s *Service) DepositOrgWallet(orgID, operatorUID string, amountCents int64) error {
	m, ok := s.Store.GetOrgMember(orgID, operatorUID)
	if !ok || (m.Role != model.OrgRoleOwner && m.Role != model.OrgRoleAdmin) {
		return fmt.Errorf("无权限操作组织钱包(需 owner/admin)")
	}
	if amountCents <= 0 {
		return fmt.Errorf("充值金额必须大于 0")
	}
	w := s.Store.GetOrCreateOrgWallet(orgID)
	w.BalanceCents += amountCents
	s.Store.PutOrgWallet(w)
	s.recordLedger(orgID, "", model.TxDeposit, amountCents, w.BalanceCents, "组织钱包充值")
	return nil
}

// CreateOrgTask 组织级发包: 仅发布 open 任务(带 OrgID), 不预扣款。
// 组织任务走与个人任务完全相同的「接单→托管→结算」管线: provider 接单生成订单后,
// 由 CreateEscrow 按 OrgID 从组织钱包扣托管, 结算时 releaseToWallets 释放组织钱包。
func (s *Service) CreateOrgTask(orgID, operatorUID, title, category string, budgetCents int64, spec string) (*model.Task, error) {
	if _, ok := s.Store.GetOrg(orgID); !ok {
		return nil, fmt.Errorf("组织不存在")
	}
	if _, ok := s.Store.GetOrgMember(orgID, operatorUID); !ok {
		return nil, fmt.Errorf("非组织成员, 无法以组织名义发包")
	}
	if budgetCents <= 0 {
		return nil, fmt.Errorf("预算必须大于 0")
	}
	t := &model.Task{
		ID:          s.Store.NextID("T"),
		RequesterID: operatorUID,
		OrgID:       orgID,
		Title:       title,
		Category:    category,
		BudgetCents: budgetCents,
		Spec:        spec,
		Status:      model.TaskOpen,
		CreatedAt:   model.Now(),
	}
	s.Store.PutTask(t)
	s.Notifier.Notify(operatorUID, model.ChannelInApp, "组织任务已发布", fmt.Sprintf("组织 %s 已发布任务 %s(预算 %d 分), 等待接单。", orgID, t.ID, budgetCents))
	s.DispatchWebhook(orgID, "task_created", map[string]interface{}{"task_id": t.ID, "budget_cents": budgetCents})
	return t, nil
}

// GetOrgOrders 列出组织的订单(企业版统一结算视图)。
func (s *Service) GetOrgOrders(orgID string) []*model.Order {
	return s.Store.ListOrdersByOrg(orgID)
}

// GetOrgTasks 列出组织发布的任务。
func (s *Service) GetOrgTasks(orgID string) []*model.Task {
	var out []*model.Task
	for _, t := range s.Store.ListTasks() {
		if t.OrgID == orgID {
			out = append(out, t)
		}
	}
	return out
}

// RequestOrgPayout 组织钱包提现: 成员发起 → 冻结金额并生成 pending 审批单, 待 owner/admin 审批。
func (s *Service) RequestOrgPayout(orgID, operatorUID string, amountCents int64) (*model.Approval, error) {
	if _, ok := s.Store.GetOrg(orgID); !ok {
		return nil, fmt.Errorf("组织不存在")
	}
	if _, ok := s.Store.GetOrgMember(orgID, operatorUID); !ok {
		return nil, fmt.Errorf("非组织成员, 无法发起提现")
	}
	if amountCents <= 0 {
		return nil, fmt.Errorf("提现金额必须大于 0")
	}
	ow := s.Store.GetOrCreateOrgWallet(orgID)
	avail := ow.BalanceCents - ow.FrozenCents
	if avail < amountCents {
		return nil, fmt.Errorf("组织可用余额不足(可用 %d < 申请 %d)", avail, amountCents)
	}
	ow.FrozenCents += amountCents
	s.Store.PutOrgWallet(ow)
	a := &model.Approval{
		ID:          s.Store.NextID("AP"),
		OrgID:       orgID,
		Type:        model.ApprovalOrgPayout,
		AmountCents: amountCents,
		Status:      model.ApprovalPending,
		CreatedBy:   operatorUID,
		CreatedAt:   model.Now(),
	}
	s.Store.SaveApproval(a)
	if org, ok := s.Store.GetOrg(orgID); ok {
		s.Notifier.Notify(org.OwnerID, model.ChannelInApp, "组织提现待审批", fmt.Sprintf("成员 %s 申请提现 %d 分, 待审批。", operatorUID, amountCents))
	}
	return a, nil
}

// ApproveOrgPayout 审批组织提现: owner/admin 审批通过后真实出账(解冻+减余额, 生成提现记录+流水)。
func (s *Service) ApproveOrgPayout(orgID, approverUID, approvalID string, approve bool) (*model.Approval, error) {
	m, ok := s.Store.GetOrgMember(orgID, approverUID)
	if !ok || (m.Role != model.OrgRoleOwner && m.Role != model.OrgRoleAdmin) {
		return nil, fmt.Errorf("无审批权限(需 owner/admin)")
	}
	a, ok := s.Store.GetApproval(approvalID)
	if !ok || a.OrgID != orgID {
		return nil, fmt.Errorf("审批单不存在")
	}
	if a.Status != model.ApprovalPending {
		return nil, fmt.Errorf("审批单已处理")
	}
	a.ApprovedBy = approverUID
	a.ResolvedAt = model.Now()
	ow := s.Store.GetOrCreateOrgWallet(orgID)
	if approve {
		a.Status = model.ApprovalApproved
		ow.FrozenCents -= a.AmountCents
		ow.BalanceCents -= a.AmountCents
		s.Store.PutOrgWallet(ow)
		w := &model.Withdrawal{
			ID:          s.Store.NextID("WD"),
			UID:         orgID,
			AmountCents: a.AmountCents,
			Status:      model.WithdrawDone,
			CreatedAt:   a.CreatedAt,
			ResolvedAt:  a.ResolvedAt,
			Note:        "组织钱包提现(审批通过)",
		}
		s.Store.PutWithdrawal(w)
		s.recordLedger(orgID, w.ID, model.TxWithdraw, -a.AmountCents, ow.BalanceCents, "组织钱包提现出账")
		a.RefID = w.ID
		s.Store.SaveApproval(a)
		s.Notifier.Notify(a.CreatedBy, model.ChannelInApp, "提现已批准", fmt.Sprintf("您的组织提现 %d 分已审批通过并出账。", a.AmountCents))
		s.DispatchWebhook(orgID, "payout_approved", map[string]interface{}{"approval_id": a.ID, "amount_cents": a.AmountCents})
	} else {
		a.Status = model.ApprovalRejected
		ow.FrozenCents -= a.AmountCents
		s.Store.PutOrgWallet(ow)
		s.Store.SaveApproval(a)
		s.Notifier.Notify(a.CreatedBy, model.ChannelInApp, "提现被驳回", fmt.Sprintf("您的组织提现 %d 分申请被驳回。", a.AmountCents))
	}
	return a, nil
}

// CreateAPIKey 生成组织开放 API 凭证。明文 key 仅返回一次(存储 SHA256 hash)。
func (s *Service) CreateAPIKey(orgID, operatorUID, label, scopes string) (string, *model.APIKey, error) {
	m, ok := s.Store.GetOrgMember(orgID, operatorUID)
	if !ok || (m.Role != model.OrgRoleOwner && m.Role != model.OrgRoleAdmin) {
		return "", nil, fmt.Errorf("无权限创建 API Key(需 owner/admin)")
	}
	if scopes == "" {
		scopes = "tasks,wallet,webhooks"
	}
	raw := "hgk_" + randHex(16)
	k := &model.APIKey{
		ID:        s.Store.NextID("AK"),
		OrgID:     orgID,
		KeyHash:   sha256Hex(raw),
		Label:     label,
		Scopes:    scopes,
		Status:    "active",
		CreatedAt: model.Now(),
	}
	s.Store.SaveAPIKey(k)
	return raw, k, nil
}

// ListAPIKeys 列出组织 API Key(不含明文与 hash)。
func (s *Service) ListAPIKeys(orgID string) []*model.APIKey {
	return s.Store.ListAPIKeysByOrg(orgID)
}

// ResolveAPIKey 将 API Key 解析为组织凭证 Claims(供 handler 鉴权: Bearer hgk_xxx)。
func (s *Service) ResolveAPIKey(raw string) (*middleware.Claims, bool) {
	if !strings.HasPrefix(raw, "hgk_") {
		return nil, false
	}
	k, ok := s.Store.GetAPIKeyByHash(sha256Hex(raw))
	if !ok || k.Status != "active" {
		return nil, false
	}
	org, ok := s.Store.GetOrg(k.OrgID)
	if !ok {
		return nil, false
	}
	return &middleware.Claims{UserID: org.OwnerID, Role: "org_api", OrgID: org.ID}, true
}

// CreateWebhook 订阅组织事件(任务创建/订单结算/提现审批)。
func (s *Service) CreateWebhook(orgID, operatorUID, url string, events []string) (*model.Webhook, error) {
	if _, ok := s.Store.GetOrgMember(orgID, operatorUID); !ok {
		return nil, fmt.Errorf("非组织成员, 无法订阅 Webhook")
	}
	if url == "" {
		return nil, fmt.Errorf("webhook url 不能为空")
	}
	w := &model.Webhook{
		ID:        s.Store.NextID("WH"),
		OrgID:     orgID,
		URL:       url,
		Events:    events,
		Secret:    randHex(16),
		Status:    "active",
		CreatedAt: model.Now(),
	}
	s.Store.SaveWebhook(w)
	return w, nil
}

// ListWebhooks 列出组织 Webhook 订阅。
func (s *Service) ListWebhooks(orgID string) []*model.Webhook {
	return s.Store.ListWebhooksByOrg(orgID)
}

// DispatchWebhook 向组织订阅的 webhook 投递事件(后台 goroutine, 落库投递记录)。
func (s *Service) DispatchWebhook(orgID, event string, payload map[string]interface{}) {
	for _, wh := range s.Store.ListWebhooksByOrg(orgID) {
		if wh.Status != "active" {
			continue
		}
		matched := false
		for _, e := range wh.Events {
			if e == event {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		body, _ := json.Marshal(map[string]interface{}{"event": event, "org_id": orgID, "data": payload, "ts": model.Now()})
		d := &model.WebhookDelivery{
			ID:        s.Store.NextID("WDV"),
			OrgID:     orgID,
			WebhookID: wh.ID,
			Event:     event,
			Status:    "pending",
			Attempts:  0,
			CreatedAt: model.Now(),
		}
		s.Store.SaveWebhookDelivery(d)
		go s.postWebhook(wh, d, body)
	}
}

// postWebhook 真实发起 HTTP POST(演示; 失败标记 failed, 不重试)。
func (s *Service) postWebhook(wh *model.Webhook, d *model.WebhookDelivery, body []byte) {
	req, err := http.NewRequest("POST", wh.URL, bytes.NewReader(body))
	if err != nil {
		d.Status = "failed"
		s.Store.SaveWebhookDelivery(d)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HiveGet-Event", d.Event)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		d.Status = "failed"
		s.Store.SaveWebhookDelivery(d)
		return
	}
	defer resp.Body.Close()
	d.Attempts++
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.Status = "success"
	} else {
		d.Status = "failed"
	}
	s.Store.SaveWebhookDelivery(d)
}

// sha256Hex 计算字符串 SHA256 十六进制。
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// randHex 生成 n 字节随机十六进制串。
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- 需求大厅检索 + 用户声誉画像 (Marketplace 成熟度) ---

// SearchTasks 透传 store 检索(过滤/排序/截断)。
// SearchTasks 需求大厅检索(分面过滤由 Store 完成); 若 Sort==relevance 且传入 providerID,
// 则在 Go 层按该接单方与任务的匹配分(score)降序重排(复用 AI 撮合口径), 形成「最懂你的需求排前面」。
// 真实落库(MySQL)模式同样适用: Store 返回候选后在此统一用 score 排序, 两种存储口径一致。
func (s *Service) SearchTasks(providerID string, f store.TaskFilter) []*model.Task {
	out := s.Store.SearchTasks(f)
	if f.Sort == "relevance" && providerID != "" {
		p, ok := s.Store.GetUser(providerID)
		if ok && p.Role == model.RoleProvider {
			// M2: 批量向量化候选任务(需求侧) + 接单方(供应侧), 再按匹配分排序, 口径与撮合一致。
			docs := make([]string, len(out))
			for i, t := range out {
				docs[i] = s.taskDoc(t)
			}
			taskVecs := s.embed(docs)
			provVec := s.embed([]string{s.providerDoc(p)})[0]
			sort.SliceStable(out, func(i, j int) bool {
				si, _ := s.score(p, out[i], taskVecs[i], provVec)
				sj, _ := s.score(p, out[j], taskVecs[j], provVec)
				return si > sj
			})
		}
	}
	return out
}

// PublicProfile 对外可见的用户声誉画像(不含敏感字段)。
type PublicProfile struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	Skills         []string `json:"skills"`
	CreditScore    int     `json:"credit_score"`    // 0-100 信用分(由历史评分重算)
	AvgRating      float64 `json:"avg_rating"`      // 历史互评均值 1-5
	ReviewCount    int     `json:"review_count"`    // 被评价次数
	CompletedCount int     `json:"completed_count"` // 已完成(已结算)订单数
	TrustScore     int     `json:"trust_score"`     // 综合可信分 0-100(需求方一眼排序用)
	KYCVerified    bool    `json:"kyc_verified"`
	TaxBound       bool    `json:"tax_bound"`
	RecentOrders   []ProfileOrder  `json:"recent_orders,omitempty"`  // 近期订单(需求方/接单方双视角)
	RecentReviews  []ProfileReview `json:"recent_reviews,omitempty"` // 近期收到的评价
}

// ProfileOrder 画像页展示的近期订单(角色区分需求方/接单方)。
type ProfileOrder struct {
	ID          string `json:"id"`
	Role        string `json:"role"` // requester | provider
	TaskTitle   string `json:"task_title"`
	AmountCents int64  `json:"amount_cents"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
}

// ProfileReview 画像页展示的近期评价(收到的互评)。
type ProfileReview struct {
	OrderID       string `json:"order_id"`
	Counterparty  string `json:"counterparty"` // 评价方(对方)名称
	Rating        int    `json:"rating"`
	Comment       string `json:"comment"`
	CreatedAt     int64  `json:"created_at"`
}

// ListCategories 返回平台开放类目法(需求大厅筛选/发单下拉的唯一事实来源)。
func (s *Service) ListCategories() []model.CategoryDef {
	return model.TaskCategories
}

// computeTrustScore 透明可解释的综合可信分(0-100), 与匹配分(MatchTask 五因子)相互独立:
//   - 服务质量(avg_rating/5)        占 40%
//   - 交付经验(min(completed,20)/20) 占 30%
//   - 信用(credit_score/100)        占 30%
func computeTrustScore(avg float64, completed, credit int) int {
	ratingComp := (avg / 5.0) * 40
	expComp := (float64(minInt(completed, 20)) / 20.0) * 30
	creditComp := (float64(credit) / 100.0) * 30
	score := int(ratingComp + expComp + creditComp)
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetPublicProfile 聚合用户公开声誉(纯读, 不触碰合规闸门)。
func (s *Service) GetPublicProfile(uid string) (*PublicProfile, error) {
	u, ok := s.Store.GetUser(uid)
	if !ok {
		return nil, fmt.Errorf("用户不存在")
	}
	// 历史互评均值。
	reviews := s.Store.ListReviewsByReviewee(uid)
	var sum int
	for _, r := range reviews {
		sum += r.Rating
	}
	avg := 0.0
	if len(reviews) > 0 {
		avg = float64(sum) / float64(len(reviews))
	}
	// 已完成(已结算)订单数。
	orders := s.Store.ListOrdersByUser(uid)
	done := 0
	recentOrders := make([]ProfileOrder, 0, len(orders))
	for _, o := range orders {
		if o.Status == model.OrderCompleted {
			done++
		}
		role := "provider"
		if o.RequesterID == uid {
			role = "requester"
		}
		title := ""
		if t, ok := s.Store.GetTask(o.TaskID); ok {
			title = t.Title
		}
		recentOrders = append(recentOrders, ProfileOrder{
			ID: o.ID, Role: role, TaskTitle: title,
			AmountCents: o.AmountCents, Status: string(o.Status), CreatedAt: o.CreatedAt,
		})
	}
	// 近期收到的评价(被评价方视角)。
	recentReviews := make([]ProfileReview, 0, len(reviews))
	for _, r := range reviews {
		cpty := ""
		if ru, ok := s.Store.GetUser(r.ReviewerID); ok {
			cpty = ru.Name
		}
		recentReviews = append(recentReviews, ProfileReview{
			OrderID: r.OrderID, Counterparty: cpty, Rating: r.Rating,
			Comment: r.Comment, CreatedAt: r.CreatedAt,
		})
	}
	if len(recentOrders) > 5 {
		recentOrders = recentOrders[:5]
	}
	if len(recentReviews) > 5 {
		recentReviews = recentReviews[:5]
	}
	return &PublicProfile{
		ID:             u.ID,
		Name:           u.Name,
		Role:           string(u.Role),
		Skills:         u.Skills,
		CreditScore:    u.CreditScore,
		AvgRating:      avg,
		ReviewCount:    len(reviews),
		CompletedCount: done,
		TrustScore:     computeTrustScore(avg, done, u.CreditScore),
		KYCVerified:    u.KYCVerified,
		TaxBound:       u.TaxBound,
		RecentOrders:   recentOrders,
		RecentReviews:  recentReviews,
	}, nil
}

// KYCStatus 查询当前用户的实名认证状态(经 kyc Provider)。
func (s *Service) KYCStatus(uid string) (verified bool, state string, kycRef string) {
	if s.KYC == nil {
		if u, ok := s.Store.GetUser(uid); ok {
			return u.KYCVerified, "local", ""
		}
		return false, "unknown", ""
	}
	v, st, _ := s.KYC.Status(uid)
	return v, st, ""
}

// SubmitKYC 提交实名认证(姓名+证件号)。模拟通道(Mock)提交即自动通过并打标 u.KYCVerified;
// 真实通道提交后状态为 pending, 由厂商回调/轮询刷新。返回受理单号。
func (s *Service) SubmitKYC(uid, name, idNo string) (ref string, verified bool, err error) {
	if s.KYC == nil {
		return "", false, fmt.Errorf("KYC 通道未初始化")
	}
	r, e := s.KYC.Submit(uid, name, idNo)
	if e != nil {
		return "", false, e
	}
	v, st, _ := s.KYC.Status(uid)
	// 模拟通道自动通过: 打标用户 KYC, 使合规闸门放行。
	if v {
		if u, ok := s.Store.GetUser(uid); ok {
			u.KYCVerified = true
			s.Store.PutUser(u)
		}
	}
	return r, v && st == "passed", nil
}

// TaskDetail 需求详情聚合(任务 + 发布者公开画像 + 关联订单)。
type TaskDetail struct {
	Task      *model.Task    `json:"task"`
	Publisher *PublicProfile `json:"publisher"`
	Order     *model.Order   `json:"order,omitempty"` // 已被接单后的关联订单(无则省略)
}

// GetTaskDetail 聚合需求详情(纯读, 不触碰合规闸门)。
func (s *Service) GetTaskDetail(taskID string) (*TaskDetail, error) {
	t, ok := s.Store.GetTask(taskID)
	if !ok {
		return nil, fmt.Errorf("需求不存在")
	}
	pub, _ := s.GetPublicProfile(t.RequesterID)
	var ord *model.Order
	if orders := s.Store.ListOrdersByTask(taskID); len(orders) > 0 {
		ord = orders[0]
	}
	return &TaskDetail{Task: t, Publisher: pub, Order: ord}, nil
}

// Workbench 我的工作台聚合视图。
type TaskItem struct {
	*model.Task
	OrderID     string `json:"order_id,omitempty"`
	OrderStatus string `json:"order_status,omitempty"`
}
type OrderItem struct {
	*model.Order
	TaskTitle string `json:"task_title"`
}
type WorkbenchStats struct {
	OpenTasks     int `json:"open_tasks"`      // 我发布的仍开放任务数
	ActiveOrders  int `json:"active_orders"`   // 进行中订单(active/delivering/delivered)
	NeedsMyAction int `json:"needs_my_action"` // 待我处理的订单(待托管/待验收/纠纷)
}
type Workbench struct {
	Profile           *PublicProfile  `json:"profile"`
	PublishedTasks    []TaskItem      `json:"published_tasks"`
	OrdersAsProvider  []OrderItem     `json:"orders_as_provider"`
	OrdersAsRequester []OrderItem     `json:"orders_as_requester"`
	Stats             WorkbenchStats  `json:"stats"`
}

// GetWorkbench 聚合当前用户的工作台(纯读)。
func (s *Service) GetWorkbench(uid string) (*Workbench, error) {
	prof, err := s.GetPublicProfile(uid)
	if err != nil {
		return nil, err
	}
	// 我发布的任务 + 关联订单状态。
	pubTasks := s.Store.ListTasksByOwner(uid)
	published := make([]TaskItem, 0, len(pubTasks))
	openCount := 0
	for _, t := range pubTasks {
		item := TaskItem{Task: t}
		if t.Status == model.TaskOpen {
			openCount++
		}
		if orders := s.Store.ListOrdersByTask(t.ID); len(orders) > 0 {
			item.OrderID = orders[0].ID
			item.OrderStatus = string(orders[0].Status)
		}
		published = append(published, item)
	}
	// 我参与的订单(按角色分组)。
	all := s.Store.ListOrdersByUser(uid)
	asProvider := make([]OrderItem, 0)
	asRequester := make([]OrderItem, 0)
	activeCount := 0
	actionCount := 0
	for _, o := range all {
		t, _ := s.Store.GetTask(o.TaskID)
		title := ""
		if t != nil {
			title = t.Title
		}
		item := OrderItem{Order: o, TaskTitle: title}
		switch {
		case o.ProviderID == uid:
			asProvider = append(asProvider, item)
		case o.RequesterID == uid:
			asRequester = append(asRequester, item)
		}
		switch o.Status {
		case model.OrderActive, model.OrderDelivering:
			activeCount++
		case model.OrderPendingEscrow:
			if o.RequesterID == uid {
				actionCount++
			}
		case model.OrderDelivered:
			if o.RequesterID == uid {
				actionCount++
			}
		case model.OrderDisputed:
			actionCount++
		}
	}
	return &Workbench{
		Profile:           prof,
		PublishedTasks:    published,
		OrdersAsProvider:  asProvider,
		OrdersAsRequester: asRequester,
		Stats: WorkbenchStats{
			OpenTasks:     openCount,
			ActiveOrders:  activeCount,
			NeedsMyAction: actionCount,
		},
	}, nil
}

// ---- P3 运营看板: 平台级指标聚合(只读, 不引入新写入面/合规闸门) ----

// PlatformMetrics 平台运营指标快照, 供运营(A001)看板展示。
type PlatformMetrics struct {
	UsersTotal   int            `json:"users_total"`
	Providers    int            `json:"providers"`
	Requesters   int            `json:"requesters"`
	Admins       int            `json:"admins"`
	TasksTotal   int            `json:"tasks_total"`
	TasksOpen    int            `json:"tasks_open"`
	OrdersTotal  int            `json:"orders_total"`
	OrdersByStatus map[string]int `json:"orders_by_status"`
	OrdersSettled   int        `json:"orders_settled"`
	GMVCents        int64     `json:"gmv_cents"` // 已结算订单交易额合计(分)
	PlatformBalanceCents int64 `json:"platform_balance_cents"` // 全部钱包可用+冻结合计(分)
	WithdrawalsTotal    int    `json:"withdrawals_total"`
	WithdrawalsPending  int    `json:"withdrawals_pending"`
	ComplianceRedLights int    `json:"compliance_red_lights"` // 未落地合规卡点数
}

// PlatformMetrics 聚合平台指标: 用户/任务/订单/资金水位/提现/合规红灯。
func (s *Service) PlatformMetrics() *PlatformMetrics {
	m := &PlatformMetrics{OrdersByStatus: map[string]int{}}

	for _, u := range s.Store.ListUsers() {
		m.UsersTotal++
		switch u.Role {
		case model.RoleProvider:
			m.Providers++
		case model.RoleRequester:
			m.Requesters++
		case model.RoleAdmin:
			m.Admins++
		}
	}

	for _, t := range s.Store.ListTasks() {
		m.TasksTotal++
		if t.Status == model.TaskOpen {
			m.TasksOpen++
		}
	}

	for _, o := range s.Store.ListOrders() {
		m.OrdersTotal++
		m.OrdersByStatus[string(o.Status)]++
		if o.Settled {
			m.OrdersSettled++
			m.GMVCents += o.AmountCents
		}
	}

	for _, w := range s.Store.ListWallets() {
		m.PlatformBalanceCents += w.BalanceCents + w.FrozenCents
	}

	for _, wd := range s.Store.ListWithdrawals() {
		m.WithdrawalsTotal++
		if wd.Status == model.WithdrawPending {
			m.WithdrawalsPending++
		}
	}

	if st := s.Compliance.GetState(); st != nil {
		for _, on := range st.Items {
			if !on {
				m.ComplianceRedLights++
			}
		}
	}

	return m
}
