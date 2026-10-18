// Package quote 实现 B2「接单方自动报价」：依据任务类目 / 复杂度 / 紧急度 + 接单方画像
// （历史成交均价、互评星级）给出建议报价区间与可解释理由。
//
// 设计要点：
//   - 默认零依赖的确定性启发式估价（规则化保底），无需任何外部服务即可运行；
//   - 若配置了 ZANBUDDY_LLM_API_KEY，则额外调用 LLM 生成一段「报价话术建议」(ai_note)，
//     调用失败自动忽略，不影响区间计算；
//   - 所有金额单位为「分」，与平台一致；对外统一 CNY。
package quote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"zanbuddy/internal/aihttp"
)

// Currency 平台计价货币。
const Currency = "CNY"

// Input 估价输入：任务侧 + 接单方侧特征。
type Input struct {
	Category             string  // 任务类目
	Spec                 string  // 需求描述 + 交付物 + 格式（用于复杂度关键词/篇幅）
	ItemsCount           int     // 结构化需求项数量（验收标尺项数）
	DeadlineAt           int64   // 期望交付截止(ms 时间戳), 0 表示未填
	RequesterBudgetCents int64   // 需求方发布的预算(分), 0 表示未填
	ProviderRating       float64 // 接单方历史互评均值(0-5), 0 表示新人不计溢价
	ProviderAvgDealCents int64   // 接单方历史已完成订单的「净收入」均值(分), 0 表示无历史
}

// Quote 建议报价结果。
type Quote struct {
	MinCents       int64    `json:"min_cents"`       // 建议下限(分)
	AnchorCents    int64    `json:"anchor_cents"`    // 建议锚定价(分)
	MaxCents       int64    `json:"max_cents"`       // 建议上限(分)
	Currency       string   `json:"currency"`
	BelowEstimate  bool     `json:"below_estimate"`  // 需求方预算低于建议下限
	Rationale      []string `json:"rationale"`       // 可解释理由（逐项）
	AINote         string   `json:"ai_note,omitempty"` // 可选 LLM 报价话术建议
	Basis          map[string]any `json:"basis"`    // 各因子原始值（便于前端/调试）
}

// categoryBase 各类目「标准任务」基准价（CNY，单位：元），代表该类目常见交付的起步价。
var categoryBase = map[string]float64{
	"design":     800,
	"copywriting": 500,
	"frontend":   1200,
	"backend":    1500,
	"mobile":     1500,
	"data":       1000,
	"ml":         2000,
	"devops":     1200,
	"nlp":        1200,
	"audio":      600,
	"video":      1200,
	"other":      800,
}

// complexityKws 提升复杂度的关键词（命中越多，报价乘数越高）。
var complexityKws = []string{
	"多端", "跨端", "响应式", "微服务", "高并发", "复杂", "全套", "多个", "多语言",
	"大数据", "实时", "高可用", "企业级", "定制", "集成", "重构", "性能", "安全", "多端适配",
}

// Suggest 核心估价：确定性启发式，返回建议报价区间。
func Suggest(inp Input) *Quote {
	base := categoryBase[inp.Category]
	if base == 0 {
		base = categoryBase["other"]
	}

	// 1) 复杂度乘数（加性分项，封顶 2.6）
	itemsExtra := minf(0.8, 0.1*float64(inp.ItemsCount))
	hits := 0
	lower := strings.ToLower(inp.Spec)
	// 中文关键词按原串匹配（ToLower 仅对英文生效）
	for _, kw := range complexityKws {
		if strings.Contains(inp.Spec, kw) || strings.Contains(lower, strings.ToLower(kw)) {
			hits++
		}
	}
	kwExtra := minf(0.6, 0.12*float64(hits))
	runeCount := float64(len([]rune(inp.Spec)))
	lenExtra := minf(0.5, 0.04*(runeCount/100))
	complexity := 1 + itemsExtra + kwExtra + lenExtra
	if complexity > 2.6 {
		complexity = 2.6
	}

	// 2) 紧急度乘数
	urgency, urgDesc := urgencyMult(inp.DeadlineAt)

	// 3) 接单方星级溢价（0-5；新人=0 不计）
	var ratingPremium float64
	if inp.ProviderRating > 0 {
		ratingPremium = (inp.ProviderRating - 4.0) * 0.05 // 5★→+5%, 4.5★→+2.5%, 3.5★→-2.5%
	}

	// 4) 锚定价：有历史均净收入则融合历史（权重 0.3），否则纯启发式
	var anchorRaw float64
	if inp.ProviderAvgDealCents > 0 {
		anchorRaw = 0.7*base*complexity*urgency + 0.3*(float64(inp.ProviderAvgDealCents) / 100)
	} else {
		anchorRaw = base * complexity * urgency * (1 + ratingPremium)
	}
	anchorCents := round10Yuan(anchorRaw)
	minCents := round10Yuan(float64(anchorCents) * 85 / 100 / 100)
	maxCents := round10Yuan(float64(anchorCents) * 125 / 100 / 100)

	below := false
	if inp.RequesterBudgetCents > 0 && inp.RequesterBudgetCents < minCents {
		below = true
	}

	rationale := []string{
		fmt.Sprintf("类目基准：%s 标准任务约 ¥%.0f", inp.Category, base),
		fmt.Sprintf("复杂度：需求项 %d 项 / 关键词命中 %d / 篇幅 %.0f 字 → ×%.2f", inp.ItemsCount, hits, runeCount, complexity),
		fmt.Sprintf("紧急度：%s → ×%.2f", urgDesc, urgency),
	}
	if inp.ProviderRating > 0 {
		rationale = append(rationale, fmt.Sprintf("接单方星级：%.1f★ → 溢价 %+.1f%%", inp.ProviderRating, ratingPremium*100))
	}
	if inp.ProviderAvgDealCents > 0 {
		rationale = append(rationale, fmt.Sprintf("历史单均净收入：¥%.0f（已纳入参考）", float64(inp.ProviderAvgDealCents)/100))
	}
	rationale = append(rationale, fmt.Sprintf("建议锚定 ¥%.0f（区间 ¥%.0f – ¥%.0f）", float64(anchorCents)/100, float64(minCents)/100, float64(maxCents)/100))
	if below {
		rationale = append(rationale, fmt.Sprintf("⚠️ 需求方预算 ¥%.0f 低于建议下限，建议还价或审慎评估承接", float64(inp.RequesterBudgetCents)/100))
	}

	q := &Quote{
		MinCents:      minCents,
		AnchorCents:   anchorCents,
		MaxCents:      maxCents,
		Currency:      Currency,
		BelowEstimate: below,
		Rationale:     rationale,
		Basis: map[string]any{
			"category_base_yuan":   base,
			"complexity_mult":      fmt.Sprintf("%.2f", complexity),
			"urgency_mult":         fmt.Sprintf("%.2f", urgency),
			"rating_premium_pct":   fmt.Sprintf("%.1f", ratingPremium*100),
			"history_avg_yuan":     float64(inp.ProviderAvgDealCents) / 100,
			"anchor_raw_yuan":      anchorRaw,
		},
	}

	// 可选 LLM 话术建议（失败忽略，不影响区间）
	if key := os.Getenv("ZANBUDDY_LLM_API_KEY"); key != "" {
		if note, err := llmNote(inp, q); err == nil && note != "" {
			q.AINote = note
			q.Rationale = append(q.Rationale, "AI 话术："+note)
		}
	}
	return q
}

func urgencyMult(deadlineAt int64) (float64, string) {
	if deadlineAt <= 0 {
		return 1.0, "未设截止（按常规排期）"
	}
	days := float64(deadlineAt-time.Now().UnixMilli()) / float64(24*3600*1000)
	switch {
	case days < 0:
		return 1.35, "已过期/极紧迫"
	case days <= 3:
		return 1.35, "≤3 天（加急）"
	case days <= 7:
		return 1.2, "≤7 天（较紧）"
	case days <= 14:
		return 1.1, "≤14 天（略紧）"
	default:
		return 1.0, ">14 天（宽裕）"
	}
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// round10Yuan 把「元」向上/下取整到最近的 10 元，再返回分。
func round10Yuan(yuan float64) int64 {
	r := yuan / 10
	// 四舍五入到整数 10 元档
	ri := int64(r + 0.5)
	return ri * 10 * 100
}

// llmNote 调用 OpenAI 兼容 chat/completions 生成一段报价话术建议（可选增强）。
func llmNote(inp Input, q *Quote) (string, error) {
	base := os.Getenv("ZANBUDDY_LLM_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	modelName := os.Getenv("ZANBUDDY_LLM_MODEL")
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	sys := `你是接单平台的报价顾问。基于任务特征与接单方画像，输出一句简短(≤40字)的报价策略建议，` +
		`例如提醒加急费、强调历史口碑、或提示预算偏低需协商。只输出建议本身，不要解释。`
	user := fmt.Sprintf("类目=%s, 需求描述=%q, 需求项=%d, 截止=%d(ms), 需求方预算=%d分, 接单方星级=%.1f, 历史单均=%d分, 建议报价区间=%d~%d分。",
		inp.Category, inp.Spec, inp.ItemsCount, inp.DeadlineAt, inp.RequesterBudgetCents, inp.ProviderRating, inp.ProviderAvgDealCents, q.MinCents, q.MaxCents)
	body, _ := json.Marshal(map[string]any{
		"model":    modelName,
		"messages": []map[string]string{{"role": "system", "content": sys}, {"role": "user", "content": user}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ZANBUDDY_LLM_API_KEY"))
	resp, err := aihttp.New(20 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var or struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return "", err
	}
	if len(or.Choices) == 0 {
		return "", fmt.Errorf("llm empty choices")
	}
	return strings.TrimSpace(or.Choices[0].Message.Content), nil
}
