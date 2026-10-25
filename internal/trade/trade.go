// Package trade 交易域: 认领、订单视图。
//
// 边界: 只碰 task 与 order 两张表, 需要用户信息时走 store 读, 不反向依赖身份域。
// 认领的规则全部在这里, httpapi 只负责把结果翻译成响应——换个入口(小程序、开放 API)
// 也不用把规则抄一遍。
package trade

import (
	"errors"
	"sort"
	"strings"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

// Service 交易服务。
type Service struct {
	st store.Store
}

func New(st store.Store) *Service { return &Service{st: st} }

// Error 业务错误。状态码与错误码在这一层定, handler 不再各自拼。
type Error struct {
	Status int
	Code   string
	Msg    string
	Hint   string
}

func (e *Error) Error() string { return e.Msg }

var (
	ErrTaskNotFound = &Error{404, "TASK_NOT_FOUND", "这个需求不在了", "可能已被需求方撤下，刷新大厅看看"}
	ErrTaskTaken    = &Error{409, "TASK_TAKEN", "这一单刚被接走了", "大厅里还有别的单，看看对口的"}
	ErrSelfClaim    = &Error{409, "SELF_CLAIM", "不能接自己发布的需求", ""}
	ErrNotProvider  = &Error{403, "ROLE_NOT_PROVIDER", "只有接单方可以认领", "在登录页选「接单方」再进来"}
	ErrNoProfile    = &Error{403, "PROFILE_INCOMPLETE", "档案还没填完", "先写一句定位，需求方才知道你是谁"}
	ErrAcceptFailed = &Error{500, "CLAIM_FAILED", "没接上，请再试一次", "这一单已经退回大厅"}

	ErrOrderNotFound = &Error{404, "ORDER_NOT_FOUND", "找不到这一单", "刷新页面看看"}
	ErrNotRequester  = &Error{403, "ROLE_NOT_REQUESTER", "只有发这条需求的人能点头", ""}
	ErrOrderNotWait  = &Error{409, "ORDER_NOT_PENDING", "这一单已经不在等确认", "刷新看看现在走到哪一步了"}
	ErrConfirmFailed = &Error{500, "CONFIRM_FAILED", "没确认上，请再试一次", ""}
	ErrDeclineFailed = &Error{500, "DECLINE_FAILED", "没退回，请再试一次", ""}

	// 交付这一侧。与确认侧对称: 资格先判、状态后判——
	// 路人点到一单已交付的活, 该看到"这不是你的单", 而不是"来晚了"。
	ErrNotOrderProvider = &Error{403, "NOT_ORDER_PROVIDER", "只有这一单的接单方能交付", "这单不是你接的，去看自己接的单"}
	ErrNotParticipant   = &Error{403, "NOT_PARTICIPANT", "这不是你的单", ""}
	ErrDeliveryEmpty    = &Error{400, "DELIVERY_EMPTY", "交付说明还要写一句", "写清楚做了什么、怎么用、有什么限制，需求方才好验收"}
	ErrDeliveryTooLong  = &Error{400, "DELIVERY_TOO_LONG", "交付说明太长了", "1000 字以内，细节放进交付物链接"}
	ErrDeliveryLinkBad  = &Error{400, "DELIVERY_LINK_INVALID", "交付物链接要是 http 或 https 开头的地址", ""}
	ErrDeliveryItemBad  = &Error{400, "DELIVERY_ITEM_INVALID", "交付清单对不上这一单的需求项", "刷新页面重新按项填写"}
	ErrDeliveryItemNote = &Error{400, "DELIVERY_ITEM_NOTE", "没做完的项要写一句原因", "需求方看到的是进度，不是空白"}
	ErrRevisionUsed     = &Error{409, "REVISION_LIMIT", "改稿轮次用完了", "和需求方商量一下，或者走纠纷"}
	ErrDeliverFailed    = &Error{500, "DELIVERY_FAILED", "没交上，请再试一次", ""}

	// 验收这一侧。accept 与 revise 都由需求方发起, 所以资格错误直接用 ErrNotRequester。
	ErrOrderNotReview   = &Error{409, "ORDER_NOT_TO_REVIEW", "这一单不在等验收", "刷新看看现在走到哪一步了"}
	ErrNoDeliveryRecord = &Error{500, "DELIVERY_MISSING", "这一单还没有交付记录", "让接单方重新交付一次"}
	// 打回打不动了, 是需求方在做决定, 说法要对着需求方:
	// 接单方那个 REVISION_LIMIT 是在交付时看到的, 两种场景不该共用一句话。
	ErrRevisionSpent   = &Error{409, "REVISION_LIMIT", "改稿轮次已经用完，不能再打回", "要么验收，要么走纠纷"}
	ErrReviewNoteEmpty = &Error{400, "REVIEW_NOTE_EMPTY", "打回要写一句要改什么", "接单方只凭这句话改，写清楚哪几项不合格"}
	ErrReviewNoteLong  = &Error{400, "REVIEW_NOTE_TOO_LONG", "打回说明太长了", "500 字以内，逐项说清哪几项不合格"}
	ErrReviewFailed    = &Error{500, "REVIEW_FAILED", "没提交上，请再试一次", ""}

	// 状态不对时的说法按情形分, 但对外只用一个错误码:
	// 前端要处理的是"这一单现在不能交付", 具体卡在哪一句由 message 说清。
	ErrNotStarted   = &Error{409, "ORDER_NOT_IN_PROGRESS", "这一单还没确认开工", "等需求方点头之后再交"}
	ErrDelivered    = &Error{409, "ORDER_NOT_IN_PROGRESS", "这一单已经交付过了", "等需求方验收；有问题他会打回"}
	ErrOrderSettled = &Error{409, "ORDER_NOT_IN_PROGRESS", "这一单已经结束了", ""}
	ErrStatusChange = &Error{409, "ORDER_NOT_IN_PROGRESS", "这一单的状态刚变过", "刷新看看现在走到哪一步了"}
)

// 交付说明与各字段的长度上限。上限是为了让待验收那一屏读得完:
// 需求方要判断的是"交付的东西对不对", 不是读一份需求书。
const (
	deliveryMaxRunes     = 1000
	deliveryLinkMaxLen   = 500
	deliveryNoteMaxRunes = 200
	// 打回说明比退回原因长得多: 退回是"我不跟你合作", 一句话就够;
	// 打回是"你这里不对", 要指明具体是哪几项、怎么不对。
	reviewNoteMaxRunes = 500
)

// 退回原因最多留 60 字: 这是写给接单方看的一句话, 不是投诉信。
// 真要说清楚在 IM 里说, 那里能贴截图。
const reasonMaxRunes = 60

// ClaimCheck 这个人现在能不能接这一单。nil 表示能接。
//
// 为什么单独抽出来: 「发出去的链接」那一页要在用户点之前就说清"你为什么接不了"。
// 如果那页自己再写一遍判断, 两份规则一定会漂, 而漂的方向永远是
// "页面说能接、点下去被拒"——用户会以为系统坏了, 而不是自己身份不对。
//
// 顺序: 先资格(是不是接单方、档案全不全), 后状态(单还在不在),
// 最后才是"这是不是你自己发的单"。权限问题要报权限问题。
func (s *Service) ClaimCheck(taskID, providerID string) error {
	u, ok := s.st.GetUser(providerID)
	if !ok {
		return ErrNotProvider
	}
	if u.Role != model.RoleProvider {
		return ErrNotProvider
	}
	// 档案是背书产品的地基: 没有定位这句话, 需求方看到认领只会问"你是谁"。
	// 也因此, 没档案的人不该有机会先把单锁住。
	p, hasProfile := s.st.GetProfile(providerID)
	if !hasProfile || !u.Onboarded || strings.TrimSpace(p.Headline) == "" {
		return ErrNoProfile
	}

	t, ok := s.st.GetTask(taskID)
	if !ok {
		return ErrTaskNotFound
	}
	if t.Status != model.TaskOpen {
		return ErrTaskTaken
	}
	if t.RequesterID == providerID {
		return ErrSelfClaim
	}
	return nil
}

// Claim 接单方主动认领一个开放需求。
func (s *Service) Claim(taskID, providerID string) (*model.Order, error) {
	if err := s.ClaimCheck(taskID, providerID); err != nil {
		return nil, err
	}

	// 真正的抢单点。两个接单方同时点, 只有一个能过。
	// ClaimCheck 刚判过一遍, 这里仍然可能失败——查与抢之间有一瞬,
	// 而那一瞬正是两个人同时点的时候。
	if !s.st.ClaimOpenTask(taskID) {
		return nil, ErrTaskTaken
	}

	// 抢到之后重新读一次: ClaimCheck 读的是抢之前的那份快照,
	// 而订单要落的是"抢到的那一刻"的单子(状态、金额、归属)。
	t, ok := s.st.GetTask(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}

	o := &model.Order{
		ID:             s.st.NextID("O"),
		TaskID:         t.ID,
		RequesterID:    t.RequesterID,
		ProviderID:     providerID,
		OrgID:          t.OrgID,
		AmountCents:    t.BudgetCents,
		Status:         model.OrderPendingEscrow, // 对外四态=待确认, 语义是"等需求方点头"
		MaxRevisions:   model.DefaultMaxRevisions,
		SettlementMode: model.SettleLump,
		CreatedAt:      model.Now(),
	}
	s.st.PutOrder(o)

	// 落库确认。写订单失败却把任务标成 matched, 结果是单子从大厅消失、谁的账上都没有,
	// 只能人工改库。退回 open 后接单人重试即可, 代价可控。
	if _, ok := s.st.GetOrder(o.ID); !ok {
		t.Status = model.TaskOpen
		s.st.PutTask(t)
		return nil, ErrAcceptFailed
	}
	return o, nil
}

// Confirm 需求方点头, 认领正式成立。
//
// 这一步刻意不碰钱。v2 是轻交易: 确认开工只表示"这一单算数了",
// 不预扣款、不进托管。将来打开托管模式时, 这里才是调支付通道的地方——
// 换句话说, 资金链路要接进来, 只有一个入口, 不散在页面和 handler 里。
func (s *Service) Confirm(orderID, uid string) (*model.Order, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	// 先问"你是不是这一单的需求方", 再问"这一单还等不等确认"。
	// 顺序反了, 不相干的人点到一单已开工的活会看到"已不在等确认", 以为自己来晚了。
	if o.RequesterID != uid {
		return nil, ErrNotRequester
	}
	if o.Status != model.OrderPendingEscrow {
		return nil, ErrOrderNotWait
	}
	if !s.st.TransitionOrder(orderID, model.OrderPendingEscrow, model.OrderActive) {
		return nil, ErrOrderNotWait
	}
	// 以库里的为准再读一次: 内存实现返回的是同一个指针,
	// MySQL 返回的是新对象, 前端拿到的东西不能两种模式不一样。
	if fresh, ok := s.st.GetOrder(orderID); ok {
		o = fresh
	}
	return o, nil
}

// Decline 需求方不认这个接单人, 把单子退回大厅。
//
// 与确认对称: 权限先判、状态后判。这里还多一层考虑——
// 退回必须让单子重新能被别人接, 否则一个不合适的人点了认领,
// 这单就死在"待确认"里, 需求方没有任何办法。
func (s *Service) Decline(orderID, uid, reason string) (*model.Order, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	if o.RequesterID != uid {
		return nil, ErrNotRequester
	}
	if o.Status != model.OrderPendingEscrow {
		return nil, ErrOrderNotWait
	}

	reason = strings.TrimSpace(reason)
	if r := []rune(reason); len(r) > reasonMaxRunes {
		reason = string(r[:reasonMaxRunes])
	}
	if reason == "" {
		reason = "需求方没有说明原因"
	}

	if !s.st.CancelPendingOrder(orderID, o.TaskID, uid, reason, model.Now()) {
		return nil, ErrOrderNotWait
	}
	if fresh, ok := s.st.GetOrder(orderID); ok {
		o = fresh
	}
	return o, nil
}

// Deliver 接单方交付。订单 active/delivering → delivered(对外=待验收)。
//
// 交付是整个轻交易里唯一"由接单方推着走"的一步: 认领之后单子停在进行中,
// 要让需求方有东西可验收, 必须有这一下。它也刻意不碰钱——
// 验收通过才算成交, 放款在验收那一跳(还没接上), 不在交付这一步。
//
// 判断顺序: 资格 → 状态 → 内容。内容问题最后报, 是因为
// "你没资格交"和"你写了空话"同时成立时, 前者才是他该先解决的事。
func (s *Service) Deliver(orderID, uid, content, link string, items []model.DeliveryItem) (*model.Order, *model.Delivery, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, nil, ErrOrderNotFound
	}
	if o.ProviderID != uid {
		return nil, nil, ErrNotOrderProvider
	}
	switch o.Status {
	case model.OrderActive, model.OrderDelivering:
		// 可交付
	case model.OrderPendingEscrow:
		return nil, nil, ErrNotStarted
	case model.OrderDelivered:
		return nil, nil, ErrDelivered
	default:
		return nil, nil, ErrOrderSettled
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, ErrDeliveryEmpty
	}
	if r := []rune(content); len(r) > deliveryMaxRunes {
		// 不截断: 截掉的可能是最关键的验收说明, 交出去就没法补了
		return nil, nil, ErrDeliveryTooLong
	}

	link = strings.TrimSpace(link)
	if len(link) > deliveryLinkMaxLen {
		return nil, nil, ErrDeliveryLinkBad
	}
	if link != "" && !isHTTPLink(link) {
		return nil, nil, ErrDeliveryLinkBad
	}

	var reqs []model.RequirementItem
	if t, ok := s.st.GetTask(o.TaskID); ok {
		reqs = t.SpecObj.Items
	}
	norm, err := normalizeItems(reqs, items)
	if err != nil {
		return nil, nil, err
	}

	maxRound := o.MaxRevisions
	if maxRound <= 0 {
		maxRound = model.DefaultMaxRevisions
	}
	d, err := s.st.DeliverOrder(orderID, content, link, norm,
		[]model.OrderStatus{model.OrderActive, model.OrderDelivering},
		model.OrderDelivered, maxRound, model.Now())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrderGone):
			return nil, nil, ErrOrderNotFound
		case errors.Is(err, store.ErrRevisionLimit):
			return nil, nil, ErrRevisionUsed
		case errors.Is(err, store.ErrNotDeliverable):
			// 走到这儿说明读完状态之后被别人动过(需求方刚打回并再次提交之类)
			return nil, nil, ErrStatusChange
		}
		return nil, nil, ErrDeliverFailed
	}

	// 以库里的为准再读一次: 内存实现返回同一指针, MySQL 返回新对象,
	// 前端在两种模式下拿到的东西必须一样。
	if fresh, ok := s.st.GetOrder(orderID); ok {
		o = fresh
	}
	return o, d, nil
}

// Accept 需求方验收通过, 这一单算成交。
//
// 轻交易下它不碰钱: 没有托管账户要释放, 也不写钱包。将来开托管模式时,
// 放款就是这一步里加的一行——所以千万别在页面或 handler 里另开一条"完成订单"的路。
//
// 验收会顺带关闭需求单: task 停在 matched 的话, "这条需求到底做完了没"
// 永远没人能回答, 以后做「我发的需求」页只能靠订单反推。
func (s *Service) Accept(orderID, uid string) (*model.Order, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	// 资格先判: 不相干的人点到一单待验收的活, 该看到"这不是你的单"。
	if o.RequesterID != uid {
		return nil, ErrNotRequester
	}
	if o.Status != model.OrderDelivered {
		return nil, ErrOrderNotReview
	}
	if err := s.st.ReviewOrder(orderID, true, "", model.Now()); err != nil {
		return nil, mapReviewErr(err)
	}
	if fresh, ok := s.st.GetOrder(orderID); ok {
		o = fresh
	}
	return o, nil
}

// Revise 需求方打回, 让接单方改一版。
//
// 原因必填且不截断: 这是接单方下一轮唯一知道"改什么"的依据。
// 交付说明超长会报错而不是截断, 同理——截掉的可能正是被点名的那一项。
func (s *Service) Revise(orderID, uid, note string) (*model.Order, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	if o.RequesterID != uid {
		return nil, ErrNotRequester
	}
	if o.Status != model.OrderDelivered {
		return nil, ErrOrderNotReview
	}

	note = strings.TrimSpace(note)
	if note == "" {
		return nil, ErrReviewNoteEmpty
	}
	if r := []rune(note); len(r) > reviewNoteMaxRunes {
		return nil, ErrReviewNoteLong
	}

	if err := s.st.ReviewOrder(orderID, false, note, model.Now()); err != nil {
		return nil, mapReviewErr(err)
	}
	if fresh, ok := s.st.GetOrder(orderID); ok {
		o = fresh
	}
	return o, nil
}

func mapReviewErr(err error) error {
	switch {
	case errors.Is(err, store.ErrOrderGone):
		return ErrOrderNotFound
	case errors.Is(err, store.ErrNotReviewable):
		return ErrOrderNotReview
	case errors.Is(err, store.ErrRevisionLimit):
		return ErrRevisionSpent
	case errors.Is(err, store.ErrNoDelivery):
		return ErrNoDeliveryRecord
	}
	return ErrReviewFailed
}

// normalizeItems 校验并按需求项的原始顺序整理交付清单。
//
// 三条规则, 都是为了"需求方验收时能一项一项对上":
//   - 需求单有清单时, 每一项都必须交代(做没做), 不能只交一部分让人去猜;
//   - 清单里出现不存在的项, 说明前端拿的是旧数据, 直接让刷新, 不落脏数据;
//   - 没做完的项必须写一句原因, 否则验收时看到的是一个没解释的空洞。
func normalizeItems(reqs []model.RequirementItem, items []model.DeliveryItem) ([]model.DeliveryItem, error) {
	known := make(map[string]bool, len(reqs))
	for _, r := range reqs {
		known[r.ID] = true
	}

	byID := make(map[string]model.DeliveryItem, len(items))
	for _, it := range items {
		it.ItemID = strings.TrimSpace(it.ItemID)
		if it.ItemID == "" {
			continue
		}
		if !known[it.ItemID] {
			return nil, &Error{400, ErrDeliveryItemBad.Code, "交付清单里有这一单没有的需求项：" + it.ItemID, ErrDeliveryItemBad.Hint}
		}
		if _, dup := byID[it.ItemID]; dup {
			return nil, &Error{400, ErrDeliveryItemBad.Code, "同一项填了两次：" + it.ItemID, ErrDeliveryItemBad.Hint}
		}
		it.Note = strings.TrimSpace(it.Note)
		if r := []rune(it.Note); len(r) > deliveryNoteMaxRunes {
			it.Note = string(r[:deliveryNoteMaxRunes])
		}
		it.Link = strings.TrimSpace(it.Link)
		if len(it.Link) > deliveryLinkMaxLen || (it.Link != "" && !isHTTPLink(it.Link)) {
			return nil, ErrDeliveryLinkBad
		}
		if !it.Done && it.Note == "" {
			return nil, &Error{400, ErrDeliveryItemNote.Code, "第 " + it.ItemID + " 项没做完，得写一句原因", ErrDeliveryItemNote.Hint}
		}
		byID[it.ItemID] = it
	}

	if len(reqs) == 0 {
		if len(byID) > 0 {
			return nil, ErrDeliveryItemBad
		}
		return nil, nil
	}

	out := make([]model.DeliveryItem, 0, len(reqs))
	var missing []string
	for _, r := range reqs {
		it, ok := byID[r.ID]
		if !ok {
			missing = append(missing, r.ID)
			continue
		}
		out = append(out, it)
	}
	if len(missing) > 0 {
		return nil, &Error{400, ErrDeliveryItemBad.Code, "还有需求项没交代：" + strings.Join(missing, "、"), "每一项都要说明做没做完"}
	}
	return out, nil
}

func isHTTPLink(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// DeliveryView 一轮交付的对外样子。历史轮次也用它:
// 打回之后接单方要能看到上一轮交了什么、被指了哪几项。
type DeliveryView struct {
	ID      string               `json:"id"`
	Round   int                  `json:"round"`
	Content string               `json:"content"`
	Link    string               `json:"link,omitempty"`
	Items   []model.DeliveryItem `json:"items,omitempty"`
	Status  string               `json:"status"`
	// 本轮有几项没做完。前端不必自己数, 也不该假设 items 一定有值。
	UndoneCount int   `json:"undone_count"`
	CreatedAt   int64 `json:"created_at"`
	// 这一轮被验收成什么样。submitted=还在等验收, rejected=被打回(带说明),
	// accepted=验收通过。历史轮次保留, 接单方能翻回去看上次被指了哪几项。
	ReviewNote string `json:"review_note,omitempty"`
	ReviewedAt int64  `json:"reviewed_at,omitempty"`
}

func toDeliveryView(d *model.Delivery) DeliveryView {
	v := DeliveryView{
		ID:         d.ID,
		Round:      d.Round,
		Content:    d.Content,
		Link:       d.Link,
		Items:      d.Items,
		Status:     d.Status,
		CreatedAt:  d.CreatedAt,
		ReviewNote: d.ReviewNote,
		ReviewedAt: d.ReviewedAt,
	}
	for _, it := range d.Items {
		if !it.Done {
			v.UndoneCount++
		}
	}
	return v
}

// Deliveries 这一单的交付记录。只有交易双方能看:
// 交付内容是过程材料, 不是公开页上的东西。
func (s *Service) Deliveries(orderID, uid string) ([]DeliveryView, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	if o.RequesterID != uid && o.ProviderID != uid {
		return nil, ErrNotParticipant
	}
	list := s.st.ListDeliveriesByOrder(orderID)
	out := make([]DeliveryView, 0, len(list))
	for _, d := range list {
		out = append(out, toDeliveryView(d))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Round < out[j].Round })
	return out, nil
}

// OrderView 订单在"我的单"里的样子。只出展示要用的字段, 不整包吐 model.Order:
// 里面有争议处理人、退款比例这些内部字段, 不该出现在列表接口里。
type OrderView struct {
	ID               string `json:"id"`
	TaskID           string `json:"task_id"`
	TaskTitle        string `json:"task_title"`
	AmountCents      int64  `json:"amount_cents"`
	Status           string `json:"status"`
	State            string `json:"state"`
	StateLabel       string `json:"state_label"`
	Role             string `json:"role"`
	Counterparty     string `json:"counterparty"`
	CounterpartySlug string `json:"counterparty_slug"`
	// 对方的一句话定位与信用分。需求方点头之前要看的就是这两样:
	// 认领只是"有人举手", 凭这两样才谈得上判断举得对不对。
	CounterpartyHeadline string `json:"counterparty_headline"`
	CounterpartyCredit   int    `json:"counterparty_credit"`
	// NeedsMe 表示"该你动了"。判定放服务端: 后面还会长出
	// 交付后等验收、验收后等背书这些动作, 页面不必每加一步就改一遍 if。
	NeedsMe      bool   `json:"needs_me"`
	CancelReason string `json:"cancel_reason,omitempty"`
	DeadlineAt   int64  `json:"deadline_at"`
	CreatedAt    int64  `json:"created_at"`

	// 需求清单(需求方发单时列的项)。交付要按项交代、验收要按项比对,
	// 少了这份清单, 前端就只能在自由文本里猜"这一项算不算做到"。
	Requirements []model.RequirementItem `json:"requirements,omitempty"`
	// 交付进度: 轮到第几轮 / 上限几轮 / 最近一轮交付。
	// 一次性给全, 前端不用为"还能不能改"再问一个接口。
	DeliveryRound int           `json:"delivery_round"`
	MaxRevisions  int           `json:"max_revisions"`
	DeliveredAt   int64         `json:"delivered_at,omitempty"`
	LastDelivery  *DeliveryView `json:"last_delivery,omitempty"`

	// 互评状态。只在订单已完成时有意义, 所以其他状态一律 false。
	//
	// CounterpartReviewDone 只表示"对方交没交", 不含分数——
	// 揭盲前连分数都不能透, 否则先写的人就被动了。
	// 但"交没交"必须透: 不说的话, 用户会以为对方不打算评, 于是自己也拖着,
	// 同时揭盲变成互相等待的死锁。
	MyReviewDone          bool `json:"my_review_done"`
	CounterpartReviewDone bool `json:"counterpart_review_done"`
}

// MyOrders 我参与的单(接的 + 发的), 按创建时间倒序。
func (s *Service) MyOrders(uid string) []OrderView {
	orders := s.st.ListOrdersByUser(uid)
	out := make([]OrderView, 0, len(orders))
	for _, o := range orders {
		out = append(out, s.View(o, uid))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// View 单个订单的对外视图。viewerID 决定 counterparty 是"需求方"还是"接单方"。
func (s *Service) View(o *model.Order, viewerID string) OrderView {
	v := OrderView{
		ID:           o.ID,
		TaskID:       o.TaskID,
		TaskTitle:    "需求 " + o.TaskID,
		AmountCents:  o.AmountCents,
		Status:       string(o.Status),
		CreatedAt:    o.CreatedAt,
		CancelReason: o.CancelReason,
		MaxRevisions: o.MaxRevisions,
	}

	st := StateOf(o.Status)
	v.State = string(st)
	v.StateLabel = StateLabel(st)

	var otherID string
	if o.ProviderID == viewerID {
		v.Role = string(model.RoleProvider)
		otherID = o.RequesterID
	} else {
		v.Role = string(model.RoleRequester)
		otherID = o.ProviderID
	}
	// 每个四态只有一个"该动的人":
	//   待确认 → 需求方点头; 进行中 → 接单方交付; 待验收 → 需求方验收/打回。
	// 已完成与已取消没人要动, 都是 false。
	v.NeedsMe = (st == model.LightToConfirm && v.Role == string(model.RoleRequester)) ||
		(st == model.LightInProgress && v.Role == string(model.RoleProvider)) ||
		(st == model.LightToReview && v.Role == string(model.RoleRequester))

	if u, ok := s.st.GetUser(otherID); ok {
		if strings.TrimSpace(u.Name) != "" {
			v.Counterparty = u.Name
		}
		v.CounterpartySlug = u.Slug
		v.CounterpartyCredit = u.CreditScore
	}
	if p, ok := s.st.GetProfile(otherID); ok {
		v.CounterpartyHeadline = strings.TrimSpace(p.Headline)
	}
	if t, ok := s.st.GetTask(o.TaskID); ok {
		if strings.TrimSpace(t.Title) != "" {
			v.TaskTitle = t.Title
		}
		v.DeadlineAt = t.SpecObj.DeadlineAt
		v.Requirements = t.SpecObj.Items
	}

	// 交付进度: 轮次按记录数算, 最近一轮取 round 最大的那条。
	// 内存实现的遍历顺序是随机的, 不能取"最后遍历到的那个"。
	if ds := s.st.ListDeliveriesByOrder(o.ID); len(ds) > 0 {
		last := ds[0]
		for _, d := range ds[1:] {
			if d.Round > last.Round {
				last = d
			}
		}
		v.DeliveryRound = len(ds)
		dv := toDeliveryView(last)
		v.LastDelivery = &dv
		v.DeliveredAt = last.CreatedAt
	}

	// 互评状态只在已完成时查: 没走完的单查了必然是空的,
	// 而 /me/orders 一次要算好几单, 能省一次查询就省一次。
	if o.Status == model.OrderCompleted {
		for _, rv := range s.st.ListReviewsByOrder(o.ID) {
			if rv.ReviewerID == viewerID {
				v.MyReviewDone = true
			} else {
				v.CounterpartReviewDone = true
			}
		}
	}
	return v
}
