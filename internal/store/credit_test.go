// Package store 的第一个 Go 单元测试。
//
// 为什么先测这一块: 信用事件是飞轮的第一块齿轮, 而它有两个"看起来对、实际错"的
// 陷阱, 只有单测能压住——
//  1) 完成单不该涨分。若有人把 delta 写成非 0, 刷单就能刷信用。
//  2) 重试不该重复记事件。验收这一步会被双击、会被客户端重放,
//     写两遍等于完成一单记两笔。
//
// 跑法: go test ./internal/store/ -run Credit -v
package store

import (
	"errors"
	"testing"

	"zanbuddy/internal/model"
)

// newDeliveredOrder 造一单已交付待验收的订单。
func newDeliveredOrder(t *testing.T, s *MemStore, requesterID, providerID string) string {
	t.Helper()

	s.PutUser(&model.User{ID: requesterID, Role: model.RoleRequester, CreditScore: 60})
	s.PutUser(&model.User{ID: providerID, Role: model.RoleProvider, CreditScore: 70})
	s.PutTask(&model.Task{ID: "T-1", RequesterID: requesterID, Status: model.TaskMatched})

	const orderID = "O-1"
	s.PutOrder(&model.Order{
		ID:          orderID,
		TaskID:      "T-1",
		RequesterID: requesterID,
		ProviderID:  providerID,
		Status:      model.OrderDelivered,
	})
	s.PutDelivery(&model.Delivery{ID: "D-1", OrderID: orderID, Round: 1, Status: "submitted"})
	return orderID
}

// 验收通过: 双方各留一条 order_completed, delta 恒 0, 分数不动。
func TestCreditOrderCompletedLeavesTraceOnly(t *testing.T) {
	s := NewMem()
	orderID := newDeliveredOrder(t, s, "U-REQ", "U-PRO")

	if err := s.ReviewOrder(orderID, true, "", 1000); err != nil {
		t.Fatalf("验收应当成功, 得到 %v", err)
	}

	for _, uid := range []string{"U-REQ", "U-PRO"} {
		evs := s.ListCreditEvents(uid)
		if len(evs) != 1 {
			t.Fatalf("%s 应当恰好有 1 条信用事件, 得到 %d 条", uid, len(evs))
		}
		e := evs[0]
		if e.Kind != model.CreditOrderCompleted {
			t.Errorf("%s 事件类型 = %q, 期望 %q", uid, e.Kind, model.CreditOrderCompleted)
		}
		if e.RefID != orderID {
			t.Errorf("%s 事件 ref_id = %q, 期望订单号 %q", uid, e.RefID, orderID)
		}
		// 这一条是核心: 完成单只留痕, 不动分。
		if e.Delta != 0 {
			t.Errorf("%s 完成单的 delta = %d, 必须为 0(完成不等于好评)", uid, e.Delta)
		}
	}
}

// 打回不产生信用事件: 没成交就没有留痕。
func TestCreditNotWrittenOnReject(t *testing.T) {
	s := NewMem()
	orderID := newDeliveredOrder(t, s, "U-REQ", "U-PRO")

	if err := s.ReviewOrder(orderID, false, "改一下配色", 1000); err != nil {
		t.Fatalf("打回应当成功, 得到 %v", err)
	}
	if evs := s.ListCreditEvents("U-REQ"); len(evs) != 0 {
		t.Fatalf("打回后需求方不该有信用事件, 得到 %d 条", len(evs))
	}
	if evs := s.ListCreditEvents("U-PRO"); len(evs) != 0 {
		t.Fatalf("打回后接单方不该有信用事件, 得到 %d 条", len(evs))
	}
}

// 重放同一次验收: 状态已经变了, 第二次直接拒; 事件数不增加。
func TestCreditNotDuplicatedOnReplay(t *testing.T) {
	s := NewMem()
	orderID := newDeliveredOrder(t, s, "U-REQ", "U-PRO")

	if err := s.ReviewOrder(orderID, true, "", 1000); err != nil {
		t.Fatalf("首次验收应当成功, 得到 %v", err)
	}
	if err := s.ReviewOrder(orderID, true, "", 1001); !errors.Is(err, ErrNotReviewable) {
		t.Fatalf("重放验收应当返回 ErrNotReviewable, 得到 %v", err)
	}
	if evs := s.ListCreditEvents("U-REQ"); len(evs) != 1 {
		t.Fatalf("重放后需求方事件数 = %d, 期望 1", len(evs))
	}
	if evs := s.ListCreditEvents("U-PRO"); len(evs) != 1 {
		t.Fatalf("重放后接单方事件数 = %d, 期望 1", len(evs))
	}
}

// 幂等键是 (user_id, kind, ref_id): 同一订单同一人写两次只留一条。
// 这一条护的是"两个入口都写了、或者事务重试"的情况——状态守卫拦不住它。
func TestAppendCreditEventIdempotent(t *testing.T) {
	s := NewMem()
	s.PutUser(&model.User{ID: "U-1", CreditScore: 80})

	s.mu.Lock()
	s.appendCreditEventLocked("U-1", model.CreditOrderCompleted, "O-9", 0, 1000)
	s.appendCreditEventLocked("U-1", model.CreditOrderCompleted, "O-9", 0, 1002)
	s.mu.Unlock()

	if evs := s.ListCreditEvents("U-1"); len(evs) != 1 {
		t.Fatalf("同一 (user,kind,ref) 重复写应当只剩 1 条, 得到 %d 条", len(evs))
	}
}

// 不同 kind 不该互相顶掉: 将来 review_received 与 order_completed 会共存于同一订单。
func TestCreditDifferentKindsCoexist(t *testing.T) {
	s := NewMem()
	s.PutUser(&model.User{ID: "U-1", CreditScore: 80})

	s.mu.Lock()
	s.appendCreditEventLocked("U-1", model.CreditOrderCompleted, "O-9", 0, 1000)
	s.appendCreditEventLocked("U-1", model.CreditReviewReceived, "O-9", 4, 1001)
	s.mu.Unlock()

	if evs := s.ListCreditEvents("U-1"); len(evs) != 2 {
		t.Fatalf("两个 kind 应当各留一条, 得到 %d 条", len(evs))
	}
}
