// 互评与揭盲的单元测试。
//
// 为什么这块必须单测而不能只靠 HTTP 用例:
//  1) "7 天自动揭盲"是一个时间边界, HTTP 用例没法等 7 天, 只能把时间当参数打进去。
//  2) 揭盲是"两条一起可见", 而不是"各管各的"。搞错的话不会有任何报错,
//     只会表现为"先写的人被后来的人照抄评分"——一个没人能察觉的公平性漏洞。
//
// 跑法: go test ./internal/store/ ./internal/model/ -run Review -v
package store

import (
	"errors"
	"testing"

	"zanbuddy/internal/model"
)

// completedOrder 造一单已完成的订单(互评的前置状态)。
func completedOrder(t *testing.T, s *MemStore, requesterID, providerID string) string {
	t.Helper()
	orderID := newDeliveredOrder(t, s, requesterID, providerID)
	if err := s.ReviewOrder(orderID, true, "", 1000); err != nil {
		t.Fatalf("验收应当成功, 得到 %v", err)
	}
	return orderID
}

// 只有一方提交时: 未揭盲, 也不产生 review_received 事件。
// 事件留痕的意义是"这个人真的收到了一条可见的评价", 未揭盲的评价还不算收到。
func TestReviewNotRevealedUntilBothSubmit(t *testing.T) {
	s := NewMem()
	orderID := completedOrder(t, s, "U-REQ", "U-PRO")

	mine := &model.Review{ID: "RV-1", OrderID: orderID, TaskID: "T-1",
		ReviewerID: "U-REQ", RevieweeID: "U-PRO", ReviewerRole: model.RoleRequester, Rating: 5}
	if err := s.SubmitReview(mine, 2000); err != nil {
		t.Fatalf("首次提交应当成功, 得到 %v", err)
	}
	if mine.RevealedAt != 0 {
		t.Errorf("单方提交后 RevealedAt = %d, 期望 0(还没揭盲)", mine.RevealedAt)
	}

	// 此时只有验收那一条 order_completed, 没有 review_received。
	for _, uid := range []string{"U-REQ", "U-PRO"} {
		for _, e := range s.ListCreditEvents(uid) {
			if e.Kind == model.CreditReviewReceived {
				t.Errorf("%s 在未揭盲时不该有 review_received 事件", uid)
			}
		}
	}

	// 未揭盲的评价不计入历史分。
	if revs := s.ListRevealedReviewsByReviewee("U-PRO", 2100); len(revs) != 0 {
		t.Errorf("未揭盲的评价不该被算分, 得到 %d 条", len(revs))
	}
}

// 双方都提交: 两条同时揭盲, 每人一条 review_received, delta 恒 0。
func TestReviewBothSubmitRevealsTogether(t *testing.T) {
	s := NewMem()
	orderID := completedOrder(t, s, "U-REQ", "U-PRO")

	a := &model.Review{ID: "RV-1", OrderID: orderID, TaskID: "T-1",
		ReviewerID: "U-REQ", RevieweeID: "U-PRO", ReviewerRole: model.RoleRequester, Rating: 5}
	if err := s.SubmitReview(a, 2000); err != nil {
		t.Fatalf("需求方提交应当成功, 得到 %v", err)
	}

	b := &model.Review{ID: "RV-2", OrderID: orderID, TaskID: "T-1",
		ReviewerID: "U-PRO", RevieweeID: "U-REQ", ReviewerRole: model.RoleProvider, Rating: 4}
	if err := s.SubmitReview(b, 3000); err != nil {
		t.Fatalf("接单方提交应当成功, 得到 %v", err)
	}

	// 两条都应该是 3000 这一刻揭盲, 不是各自提交的时刻。
	if a.RevealedAt != 3000 {
		t.Errorf("需求方那条 RevealedAt = %d, 期望 3000(与后提交者同时)", a.RevealedAt)
	}
	if b.RevealedAt != 3000 {
		t.Errorf("接单方那条 RevealedAt = %d, 期望 3000", b.RevealedAt)
	}

	for _, uid := range []string{"U-REQ", "U-PRO"} {
		var got int
		for _, e := range s.ListCreditEvents(uid) {
			if e.Kind != model.CreditReviewReceived {
				continue
			}
			got++
			if e.Delta != 0 {
				t.Errorf("%s 的 review_received delta = %d, 期望 0(先通电不动参数)", uid, e.Delta)
			}
		}
		if got != 1 {
			t.Errorf("%s 应当恰好有 1 条 review_received, 得到 %d 条", uid, got)
		}
	}

	// 揭盲之后才进历史分: 5 和 4 各一条均值 4.5。
	if revs := s.ListRevealedReviewsByReviewee("U-PRO", 3100); len(revs) != 1 || revs[0].Rating != 5 {
		t.Errorf("揭盲后被评价方应能看到那条 5 星, 得到 %+v", revs)
	}
	if revs := s.ListRevealedReviewsByReviewee("U-REQ", 3100); len(revs) != 1 || revs[0].Rating != 4 {
		t.Errorf("揭盲后需求方应能看到那条 4 星, 得到 %+v", revs)
	}
}

// 同一人对同一单只能评一次。AvgRating 是求均值, 重复评等于自己刷自己的分。
func TestReviewDuplicateRejected(t *testing.T) {
	s := NewMem()
	orderID := completedOrder(t, s, "U-REQ", "U-PRO")

	a := &model.Review{ID: "RV-1", OrderID: orderID, TaskID: "T-1",
		ReviewerID: "U-REQ", RevieweeID: "U-PRO", ReviewerRole: model.RoleRequester, Rating: 5}
	if err := s.SubmitReview(a, 2000); err != nil {
		t.Fatalf("首次提交应当成功, 得到 %v", err)
	}
	dup := &model.Review{ID: "RV-9", OrderID: orderID, TaskID: "T-1",
		ReviewerID: "U-REQ", RevieweeID: "U-PRO", ReviewerRole: model.RoleRequester, Rating: 1}
	if err := s.SubmitReview(dup, 2100); !errors.Is(err, ErrReviewExists) {
		t.Fatalf("重复提交应当返回 ErrReviewExists, 得到 %v", err)
	}
	if got := s.ListReviewsByOrder(orderID); len(got) != 1 {
		t.Errorf("重复提交后评价数 = %d, 期望 1", len(got))
	}
}

// 列表顺序必须带 id 兜底: 同一毫秒提交的两条 created_at 相同。
func TestReviewListOrderStable(t *testing.T) {
	s := NewMem()
	orderID := completedOrder(t, s, "U-REQ", "U-PRO")

	for i, rv := range []*model.Review{
		{ID: "RV-B", ReviewerID: "U-REQ", RevieweeID: "U-PRO", ReviewerRole: model.RoleRequester, Rating: 5},
		{ID: "RV-A", ReviewerID: "U-PRO", RevieweeID: "U-REQ", ReviewerRole: model.RoleProvider, Rating: 3},
	} {
		rv.OrderID, rv.TaskID = orderID, "T-1"
		if err := s.SubmitReview(rv, 2000+int64(i)); err != nil {
			t.Fatalf("提交第 %d 条失败: %v", i, err)
		}
	}
	// 把两条的时间搓成同一毫秒, 验证确实靠 id 兜底而不是碰巧有序。
	s.mu.Lock()
	for _, r := range s.reviews {
		r.CreatedAt = 2000
	}
	s.mu.Unlock()

	for i := 0; i < 5; i++ {
		got := s.ListReviewsByOrder(orderID)
		if len(got) != 2 || got[0].ID != "RV-A" || got[1].ID != "RV-B" {
			t.Fatalf("第 %d 次读取顺序不稳定: %v,%v", i, got[0].ID, got[1].ID)
		}
	}
}
