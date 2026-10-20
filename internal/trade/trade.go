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
)

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
	DeadlineAt       int64  `json:"deadline_at"`
	CreatedAt        int64  `json:"created_at"`
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
		ID:          o.ID,
		TaskID:      o.TaskID,
		TaskTitle:   "需求 " + o.TaskID,
		AmountCents: o.AmountCents,
		Status:      string(o.Status),
		CreatedAt:   o.CreatedAt,
		Counterparty: "",
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
	if u, ok := s.st.GetUser(otherID); ok {
		if strings.TrimSpace(u.Name) != "" {
			v.Counterparty = u.Name
		}
		v.CounterpartySlug = u.Slug
	}
	if t, ok := s.st.GetTask(o.TaskID); ok {
		if strings.TrimSpace(t.Title) != "" {
			v.TaskTitle = t.Title
		}
		v.DeadlineAt = t.SpecObj.DeadlineAt
	}
	return v
}
