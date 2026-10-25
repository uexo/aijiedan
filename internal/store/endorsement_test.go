// 背书写入路径的单元测试。
//
// 为什么这块必须单测:
//  1. "12 个月内第二条记 weight=0" 是一条时间规则, HTTP 用例没法把 CreatedAt 往前拨一年,
//     只能把时间当参数打进来。
//  2. 计数与写入必须是同一个原子点。分开做的话两个并发请求都会看到"我这才是第一条",
//     结果两条都记满分——而这个错误不会报错, 只会让刷背书变得毫无成本。
//  3. 零权重记录"不可见但存在"这件事, 只有直接查存储层才看得清:
//     列表里少一条是正确行为, 库里少一条才是丢数据。
//
// 跑法: go test ./internal/store/ -run Endorsement -v
package store

import (
	"errors"
	"testing"

	"zanbuddy/internal/model"
)

const dayMS = int64(24 * 60 * 60 * 1000)

// endorsable 造一对"客户-接单方"，返回两个订单号（一单只能写一条背书）。
func endorsable(t *testing.T, s *MemStore) (order1, order2 string) {
	t.Helper()
	s.PutUser(&model.User{ID: "U-REQ", Role: model.RoleRequester})
	s.PutUser(&model.User{ID: "U-PRO", Role: model.RoleProvider})
	s.PutTask(&model.Task{ID: "T-1", RequesterID: "U-REQ", Status: model.TaskMatched})
	for _, id := range []string{"O-E1", "O-E2"} {
		s.PutOrder(&model.Order{
			ID:          id,
			TaskID:      "T-1",
			RequesterID: "U-REQ",
			ProviderID:  "U-PRO",
			Status:      model.OrderCompleted,
		})
	}
	return "O-E1", "O-E2"
}

func newEndorsement(id, orderID string, at int64) *model.Endorsement {
	return &model.Endorsement{
		ID:        id,
		OrderID:   orderID,
		TaskID:    "T-1",
		AuthorID:  "U-REQ",
		TargetID:  "U-PRO",
		Relation:  model.RelationClient,
		Body:      "这个脚本能读抖音导出的表",
		Answers:   map[string]string{"delivered": "对得上", "again": "会"},
		CharCount: 12,
		Weight:    100,
		CreatedAt: at,
	}
}

// 一单一个人只能写一条。第二次必须报 ErrEndorsementExists，不是覆盖。
func TestEndorsementDuplicateRejected(t *testing.T) {
	s := NewMem()
	o1, _ := endorsable(t, s)

	if err := s.PutEndorsement(newEndorsement("EN-1", o1, 1000)); err != nil {
		t.Fatalf("首条应当写入成功, 得到 %v", err)
	}
	// 换个 ID 但同一单同一作者——若判重写在 ID 上，这条会成功，等于同一单能刷两条。
	err := s.PutEndorsement(newEndorsement("EN-1-B", o1, 2000))
	if !errors.Is(err, ErrEndorsementExists) {
		t.Fatalf("同一单同一作者的第二条应当被拒, 得到 %v", err)
	}
}

// 12 个月内的第二条记 0 权重：记录留着，但不进列表、不进匹配理由。
func TestEndorsementSecondWithinWindowGetsZeroWeight(t *testing.T) {
	s := NewMem()
	o1, o2 := endorsable(t, s)

	first := newEndorsement("EN-1", o1, 1000)
	if err := s.PutEndorsement(first); err != nil {
		t.Fatalf("首条应当写入成功, 得到 %v", err)
	}
	if first.Weight != 100 {
		t.Fatalf("首条 weight = %d, 期望 100", first.Weight)
	}

	// 第 364 天，仍在窗口内。
	second := newEndorsement("EN-2", o2, 1000+364*dayMS)
	if err := s.PutEndorsement(second); err != nil {
		t.Fatalf("第二条应当写入成功(只是不计权重), 得到 %v", err)
	}
	if second.Weight != 0 {
		t.Errorf("窗口内第二条 weight = %d, 期望 0", second.Weight)
	}

	// 记录没丢：按作者查得到两条，按被背书人查只剩一条。
	if got := len(s.ListEndorsementsByAuthor("U-REQ")); got != 2 {
		t.Errorf("我写出去的应当有 2 条, 实际 %d", got)
	}
	rv := s.ListEndorsementsByTarget("U-PRO")
	if len(rv) != 1 {
		t.Fatalf("收到并计权重的应当只有 1 条, 实际 %d", len(rv))
	}
	if rv[0].ID != "EN-1" {
		t.Errorf("留下的应当是首条 EN-1, 实际 %s", rv[0].ID)
	}
}

// 过了 12 个月窗口，第二条重新计满权重。
func TestEndorsementSecondAfterWindowCounts(t *testing.T) {
	s := NewMem()
	o1, o2 := endorsable(t, s)

	if err := s.PutEndorsement(newEndorsement("EN-1", o1, 1000)); err != nil {
		t.Fatalf("首条应当写入成功, 得到 %v", err)
	}
	second := newEndorsement("EN-2", o2, 1000+366*dayMS)
	if err := s.PutEndorsement(second); err != nil {
		t.Fatalf("第二条应当写入成功, 得到 %v", err)
	}
	if second.Weight != 100 {
		t.Errorf("窗口外第二条 weight = %d, 期望 100", second.Weight)
	}
	if got := len(s.ListEndorsementsByTarget("U-PRO")); got != 2 {
		t.Errorf("两条都该计权重, 实际 %d", got)
	}
}

// 窗口按"同一对作者-被背书人"算，不是按作者算。
// 写成按作者算的话，一个客户给 A 写完再给 B 写，第二条就被白扣了权重。
func TestEndorsementWindowIsPerPair(t *testing.T) {
	s := NewMem()
	o1, o2 := endorsable(t, s)
	s.PutUser(&model.User{ID: "U-PRO2", Role: model.RoleProvider})

	if err := s.PutEndorsement(newEndorsement("EN-1", o1, 1000)); err != nil {
		t.Fatalf("首条应当写入成功, 得到 %v", err)
	}
	// 换一单(一单一条的硬约束) + 换一个人。
	other := newEndorsement("EN-2", o2, 2000)
	other.TargetID = "U-PRO2"
	if err := s.PutEndorsement(other); err != nil {
		t.Fatalf("给另一个人的背书应当写入成功, 得到 %v", err)
	}
	if other.Weight != 100 {
		t.Errorf("给另一个人的首条 weight = %d, 期望 100", other.Weight)
	}
}

// flagged 可见但已作废不可见 —— 这是"展示"与"推荐"分开的存储层那一半。
// 藏掉 flagged 等于替用户做了判断(复核结论还没出来), 但它的确有标记。
func TestEndorsementFlaggedVisibleVoidHidden(t *testing.T) {
	s := NewMem()
	o1, o2 := endorsable(t, s)

	bad := newEndorsement("EN-FLAGGED", o1, 1000)
	bad.Status = model.EndorseFlagged
	if err := s.PutEndorsement(bad); err != nil {
		t.Fatalf("写入应当成功, 得到 %v", err)
	}
	if got := len(s.ListEndorsementsByTarget("U-PRO")); got != 1 {
		t.Errorf("flagged 仍应可见, 实际 %d 条", got)
	}

	gone := newEndorsement("EN-VOID", o2, 2000)
	gone.Status = model.EndorseVoid
	if err := s.PutEndorsement(gone); err != nil {
		t.Fatalf("写入应当成功, 得到 %v", err)
	}
	// 两条都在库里，但列表里只该剩 flagged 那条。
	if got := len(s.ListEndorsementsByAuthor("U-REQ")); got != 2 {
		t.Errorf("按作者查应当有 2 条(记录不丢), 实际 %d", got)
	}
	if got := len(s.ListEndorsementsByTarget("U-PRO")); got != 1 {
		t.Errorf("void 不该出现在列表里, 实际 %d 条", got)
	}
}

// 追加按写入顺序返回，且只对存在的背书生效。
func TestEndorsementAppendOrderAndGone(t *testing.T) {
	s := NewMem()
	o1, _ := endorsable(t, s)
	if err := s.PutEndorsement(newEndorsement("EN-1", o1, 1000)); err != nil {
		t.Fatalf("写入应当成功, 得到 %v", err)
	}

	for i, body := range []string{"补一句：周报也一起给了。", "再补：后来改过一次，没另外收费。"} {
		a := &model.EndorsementAppend{
			ID:            "EA-" + string(rune('1'+i)),
			EndorsementID: "EN-1",
			AuthorID:      "U-REQ",
			Body:          body,
			CreatedAt:     int64(2000 + i),
		}
		if err := s.PutEndorsementAppend(a); err != nil {
			t.Fatalf("追加 %d 应当成功, 得到 %v", i, err)
		}
	}
	got := s.ListEndorsementAppends("EN-1")
	if len(got) != 2 {
		t.Fatalf("应当有 2 条追加, 实际 %d", len(got))
	}
	if got[0].Body != "补一句：周报也一起给了。" {
		t.Errorf("追加顺序不对, 第一条是 %q", got[0].Body)
	}

	// 给不存在的背书追加要被拒——接口层挡过一次，存储层再挡是防"删了还追加"。
	orphan := &model.EndorsementAppend{ID: "EA-9", EndorsementID: "EN-NOT-EXIST",
		AuthorID: "U-REQ", Body: "路人补一句", CreatedAt: 3000}
	if err := s.PutEndorsementAppend(orphan); !errors.Is(err, ErrEndorsementGone) {
		t.Fatalf("给不存在的背书追加应当被拒, 得到 %v", err)
	}
}
