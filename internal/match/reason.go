package match

import (
	"fmt"
	"sort"
	"strings"
)

// MaxReasons 一张卡最多几条理由。
const MaxReasons = 3

// 锚点类型。与 match_reason.anchor_type 的取值一一对应，
// 前端据此决定点开 ↗ 之后抽屉里展示什么。
const (
	AnchorSkill       = "skill"
	AnchorOrder       = "order"
	AnchorEndorsement = "endorsement"
)

// Reason 一条带锚点的推荐理由。
// 没有锚点的理由不会被生成——AnchorType 为空的结构体不该出现在结果里。
type Reason struct {
	Text        string `json:"text"`
	AnchorType  string `json:"anchor_type"`
	AnchorRef   string `json:"anchor_ref"`
	AnchorLabel string `json:"anchor_label"`
	Factor      string `json:"factor"`
	FactorScore int    `json:"factor_score"`
}

// Facts 生成理由需要的可指认事实。
//
// 与 Profile 分开是因为来源不同：Profile 是"现在他值多少分"，
// Facts 是"有什么东西可以指给需求方看"。前者会变，后者是凭据。
type Facts struct {
	CompletedOrders  int    // 已完成的订单数
	LatestOrderID    string // 最近一单的订单号（锚点 ref）
	LatestOrderTitle string // 最近一单的需求标题（锚点 label 的一半）
	EndorsementCount int    // 收到的背书条数（M4 之前恒为 0，因而这条理由不产出）
	EndorsementID    string
	EndorsementLabel string
}

// BuildReasons 按五因子生成理由，产出不出的因子直接跳过。
//
// 这张表是硬规则，不是"尽量凑三条"：
//
//	技能      → skill 锚点，条件：技能标签与需求文本有交集
//	语义      → 不产出。语义相近没有具体凭据，"描述很像"指不出任何东西
//	信用      → order 锚点，条件：有已完成订单。没有单的信用分只是注册时给的初始值
//	税源      → 不产出。它是资格条件，不是"为什么是他"的理由
//	历史评分  → endorsement 锚点，条件：有背书。M4 之前表是空的，因此恒不产出
//
// 所以现在实际最多 2 条。需求方看到 1 条卡不会觉得缺，看到"综合匹配度高"才会。
func BuildReasons(s Score, f Facts) []Reason {
	cands := make([]Reason, 0, 3)

	if len(s.Hits) > 0 {
		cands = append(cands, Reason{
			Text:        skillText(s.Hits),
			AnchorType:  AnchorSkill,
			AnchorRef:   "skill:" + strings.ToLower(strings.TrimSpace(s.Hits[0])),
			AnchorLabel: "档案技能标签",
			Factor:      "skill",
			FactorScore: int(s.Skill + 0.5),
		})
	}

	if f.CompletedOrders > 0 {
		cands = append(cands, Reason{
			Text:        orderText(f),
			AnchorType:  AnchorOrder,
			AnchorRef:   f.LatestOrderID,
			AnchorLabel: orderLabel(f),
			Factor:      "credit",
			FactorScore: int(s.Credit + 0.5),
		})
	}

	if f.EndorsementCount > 0 {
		cands = append(cands, Reason{
			Text:        endorsementText(f),
			AnchorType:  AnchorEndorsement,
			AnchorRef:   f.EndorsementID,
			AnchorLabel: f.EndorsementLabel,
			Factor:      "history",
			FactorScore: int(s.History + 0.5),
		})
	}

	// 按因子分降序：分数高的因子是这个人被选中的主因，先说主因。
	// sort.SliceStable 而不是 Slice：三条理由的分数可能相同（比如技能 0 分但仍有命中），
	// 不稳定的排序会让同一张卡两次刷新顺序不同，看起来像数据在跳。
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].FactorScore > cands[j].FactorScore })
	if len(cands) > MaxReasons {
		cands = cands[:MaxReasons]
	}
	return cands
}

// Summary 把理由压成一句话，存 match_record.reason（VARCHAR(255)）。
// 只取第一条：match_record 需要一个便于列表展示的短句，
// 完整理由在 match_reason 表里，不在这里重复。
func Summary(rs []Reason) string {
	if len(rs) == 0 {
		return ""
	}
	return rs[0].Text
}

func skillText(hits []string) string {
	const show = 3
	shown := hits
	if len(shown) > show {
		shown = shown[:show]
	}
	text := "技能对得上：" + strings.Join(shown, "、")
	if len(hits) > show {
		text += fmt.Sprintf(" 等 %d 项", len(hits))
	}
	return text
}

func orderText(f Facts) string {
	if f.LatestOrderTitle == "" {
		return fmt.Sprintf("已完成 %d 单", f.CompletedOrders)
	}
	title := f.LatestOrderTitle
	if runes := []rune(title); len(runes) > 16 {
		title = string(runes[:16]) + "…"
	}
	if f.CompletedOrders == 1 {
		return fmt.Sprintf("完成过一单「%s」", title)
	}
	return fmt.Sprintf("已完成 %d 单，最近一单是「%s」", f.CompletedOrders, title)
}

func orderLabel(f Facts) string {
	if f.LatestOrderTitle == "" {
		return "已完成订单"
	}
	return f.LatestOrderTitle + " · 已完成"
}

func endorsementText(f Facts) string {
	return fmt.Sprintf("收到 %d 条来自真实客户的背书", f.EndorsementCount)
}
