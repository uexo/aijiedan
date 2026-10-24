// 揭盲可见性的时间边界测试。
//
// 为什么单独一个文件: 这条规则是最容易被改坏、又最不容易被发现的地方。
// 全项目时间戳都是 UnixMilli(model.Now), 若把 7 天按秒写成 7*86400,
// "7 天"会变成 84 秒 —— 所有评价一提交就自动可见, 而不会报任何错。
package model

import "testing"

func TestReviewVisibleBeforeAnySubmit(t *testing.T) {
	if ReviewVisible(nil, 1000) {
		t.Error("nil 评价不该可见")
	}
	if ReviewVisible(&Review{CreatedAt: 0}, 1000) {
		t.Error("created_at 为 0 的评价不该可见(时间没写进库)")
	}
}

func TestReviewVisibleRevealedFlagWins(t *testing.T) {
	now := int64(1_700_000_000_000)
	rv := &Review{CreatedAt: now, RevealedAt: now + 1}
	if !ReviewVisible(rv, now) {
		t.Error("已写揭盲时间的评价应当立即可见, 不必等 7 天")
	}
}

func TestReviewVisibleSevenDayBoundary(t *testing.T) {
	now := int64(1_700_000_000_000)
	rv := &Review{CreatedAt: now}

	if ReviewVisible(rv, now) {
		t.Error("刚提交不该可见")
	}
	if ReviewVisible(rv, now+ReviewAutoRevealMillis-1) {
		t.Error("差 1 毫秒满 7 天, 不该可见")
	}
	if !ReviewVisible(rv, now+ReviewAutoRevealMillis) {
		t.Error("刚好满 7 天, 应当可见")
	}
	if !ReviewVisible(rv, now+30*24*60*60*1000) {
		t.Error("30 天后应当可见")
	}
}

// 这一条护的是单位: 7 天必须是 604800000 毫秒, 不是 604800 秒。
func TestReviewAutoRevealIsMillisNotSeconds(t *testing.T) {
	const want = int64(7 * 24 * 60 * 60 * 1000)
	if ReviewAutoRevealMillis != want {
		t.Fatalf("ReviewAutoRevealMillis = %d, 期望 %d(毫秒)。若得到 604800, 说明按秒算了, "+
			"7 天会变成 84 秒", ReviewAutoRevealMillis, want)
	}
}
