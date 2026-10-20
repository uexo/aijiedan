// Package trade 交易域: 认领、订单视图。
//
// 边界: 只碰 task 与 order 两张表, 需要用户信息时走 store 读, 不反向依赖身份域。
// 认领的规则全部在这里, httpapi 只负责把结果翻译成响应——换个入口(小程序、开放 API)
// 也不用把规则抄一遍。
package trade

import (
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
)

// 退回原因最多留 60 字: 这是写给接单方看的一句话, 不是投诉信。
// 真要说清楚在 IM 里说, 那里能贴截图。
const reasonMaxRunes = 60

// Claim 接单方主动认领一个开放需求。
//
// 判断顺序分两段: 先问"这个人有没有资格接", 再问"这一单还能不能接"。
// 顺序不能反——反过来的话, 需求方点到一个刚被接走的单, 会看到"这一单刚被接走了",
// 以为是自己手慢, 其实他压根不该有认领这个动作。权限问题要报权限问题。
func (s *Service) Claim(taskID, providerID string) (*model.Order, error) {
	u, ok := s.st.GetUser(providerID)
	if !ok {
		return nil, ErrNotProvider
	}
	if u.Role != model.RoleProvider {
		return nil, ErrNotProvider
	}
	// 档案是背书产品的地基: 没有定位这句话, 需求方看到认领只会问"你是谁"。
	// 也因此, 没档案的人不该有机会先把单锁住。
	p, hasProfile := s.st.GetProfile(providerID)
	if !hasProfile || !u.Onboarded || strings.TrimSpace(p.Headline) == "" {
		return nil, ErrNoProfile
	}

	t, ok := s.st.GetTask(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.Status != model.TaskOpen {
		return nil, ErrTaskTaken
	}
	if t.RequesterID == providerID {
		return nil, ErrSelfClaim
	}

	// 真正的抢单点。两个接单方同时点, 只有一个能过。
	if !s.st.ClaimOpenTask(taskID) {
		return nil, ErrTaskTaken
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
	// 现在只有"需求方确认认领"这一个动作。等交付、验收接进来时,
	// 这里按状态补齐即可, 页面只认 needs_me。
	v.NeedsMe = v.Role == string(model.RoleRequester) && st == model.LightToConfirm

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
	}
	return v
}
