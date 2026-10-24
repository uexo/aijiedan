// review.go 互评: 提交、揭盲、可见性。
//
// 为什么互评要单独一个文件而不是塞进 trade.go:
// trade.go 管的是"这一单走到哪一步", 互评管的是"这一单走完之后留下什么"。
// 前者是过程, 后者是沉淀。混在一起以后, 改沉淀规则要读一遍状态机, 容易碰坏。
//
// 揭盲规则见 model.ReviewVisible: 双方都提交才互相可见; 单方满 7 天自动可见。
// 报复性评价是互评制度最常见的死法——谁先写谁先暴露, 后写的人照着报复。
// 这条规则是为了让"先写"不再是劣势。
package trade

import (
	"errors"
	"strings"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

var (
	ErrRatingRange   = &Error{400, "RATING_RANGE", "打分要在 1 到 5 之间", "1 星很不满，5 星很满意"}
	ErrReviewTooLong = &Error{400, "REVIEW_TOO_LONG", "评价太长了", "500 字以内，说清一件事比列十条更有用"}
	ErrAlreadyReview = &Error{409, "ALREADY_REVIEWED", "你已经评过这一单了", "评价提交后不能改，这是为了让分数可信"}
	ErrOrderNotDone  = &Error{409, "ORDER_NOT_COMPLETED", "这一单还没走完", "验收通过之后再来评价对方"}
	ErrNoParticipant = &Error{403, "NOT_PARTICIPANT", "这不是你的单", "只有这一单的双方可以互评"}
	ErrReviewSave    = &Error{500, "REVIEW_SAVE_FAILED", "评价没存上，请再试一次", ""}
)

// 评价正文上限。评价是给别人看的判断, 不是投诉材料:
// 500 字说得清一单的活干得怎么样。超长报错不截断——截掉的可能正是最关键那句。
const reviewCommentMaxRunes = 500

// SubmitReview 提交一条互评。
//
// 顺序: 资格(是不是这单的人) → 状态(这单做完了没) → 内容(分数与正文)。
// 顺序反了, 路人点到一单没做完的活会先被告知"这单还没走完",
// 等于从错误里读到别人的进展。
func (s *Service) SubmitReview(orderID, uid string, rating int, comment string) (*model.Review, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}

	// 资格: 只允许这一单的双方。自己不能评自己, 所以按"是不是参与方"判就够。
	var revieweeID string
	var role model.Role
	switch uid {
	case o.RequesterID:
		revieweeID, role = o.ProviderID, model.RoleRequester
	case o.ProviderID:
		revieweeID, role = o.RequesterID, model.RoleProvider
	default:
		return nil, ErrNoParticipant
	}
	if revieweeID == "" || revieweeID == uid {
		return nil, ErrNoParticipant
	}

	if o.Status != model.OrderCompleted {
		return nil, ErrOrderNotDone
	}

	if rating < 1 || rating > 5 {
		return nil, ErrRatingRange
	}
	comment = strings.TrimSpace(comment)
	if r := []rune(comment); len(r) > reviewCommentMaxRunes {
		return nil, ErrReviewTooLong
	}

	rv := &model.Review{
		ID:           s.st.NextID("RV"),
		OrderID:      orderID,
		TaskID:       o.TaskID,
		ReviewerID:   uid,
		RevieweeID:   revieweeID,
		ReviewerRole: role,
		Rating:       rating,
		Comment:      comment,
	}
	if err := s.st.SubmitReview(rv, model.Now()); err != nil {
		if errors.Is(err, store.ErrReviewExists) {
			return nil, ErrAlreadyReview
		}
		return nil, ErrReviewSave
	}
	return rv, nil
}

// OrderReviews 一单上的评价, 按"是不是我写的"分成两边。
//
// 为什么要把"对方那条现在还看不见"单独说清楚:
// 前端如果只拿到空列表, 用户会以为对方不打算评, 于是自己也拖着不评——
// 这就把"同时揭盲"变成了"互相等待"的死锁。必须明确告诉他"对方还没提交"。
type OrderReviews struct {
	Mine           []*model.Review // 我写的(一定有内容, 即使未揭盲也只有我能看见)
	Theirs         []*model.Review // 对方写的且已可见的
	CounterpartDid bool            // 对方是否已经提交(哪怕还没揭盲)
	MineCanWrite   bool            // 我还能不能评(订单已完成且我还没评)
}

// ListOrderReviews 读取一单的评价状态。只有参与方能看。
//
// 未揭盲的对方评价, 这里不返回内容, 连"他打了几分"都不给——
// 一旦能读到分数, 先写的意义就消失了。
func (s *Service) ListOrderReviews(orderID, uid string) (*OrderReviews, error) {
	o, ok := s.st.GetOrder(orderID)
	if !ok {
		return nil, ErrOrderNotFound
	}
	if uid != o.RequesterID && uid != o.ProviderID {
		return nil, ErrNoParticipant
	}

	now := model.Now()
	all := s.st.ListReviewsByOrder(orderID)
	out := &OrderReviews{Mine: []*model.Review{}, Theirs: []*model.Review{}}
	mineWritten := false
	for _, r := range all {
		if r.ReviewerID == uid {
			mineWritten = true
			out.Mine = append(out.Mine, r)
			continue
		}
		out.CounterpartDid = true
		if model.ReviewVisible(r, now) {
			out.Theirs = append(out.Theirs, r)
		}
	}
	out.MineCanWrite = o.Status == model.OrderCompleted && !mineWritten
	return out, nil
}
