// Package reqai 提供两类 AI 需求理解能力:
//
//  1. Translate —— AI 需求翻译(需求方侧): 把需求方粗糙的描述,
//     重写为一份清晰、无歧义、可供接单方直接读懂并执行的「需求书」。
//  2. Analyze  —— AI 需求分析(接单方侧): 接单方打开任务时, AI 拆解
//     核心目标 / 交付物 / 验收要点 / 易误解点 / 需确认项, 防止理解偏差。
//
// 设计范式(与 B1 clarify / B3 faq 一致):
//   - 规则保底: 零依赖, 任何环境下都能产出可用结果。
//   - 可选 LLM 兜底: 仅当配置了 ZANBUDDY_LLM_API_KEY 才调用真实模型做润色/补全;
//     任何失败一律回退规则, 绝不挂、绝不返回空结果、绝不冒充真人。
package reqai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"zanbuddy/internal/clarify"
	"zanbuddy/internal/model"
)

// Translation AI 需求翻译结果(供接单方直接读懂的清晰需求书)。
type Translation struct {
	Doc      string   `json:"doc"`      // 完整可读需求书正文
	Title    string   `json:"title"`    // 需求标题
	Sections []string `json:"sections"` // 关键章节要点(供前端高亮)
	Mode     string   `json:"mode"`     // rule | llm
}

// Analysis AI 需求分析结果(接单方自我校核, 防止理解偏差)。
type Analysis struct {
	CoreGoal          string   `json:"core_goal"`          // 核心目标
	Deliverables      []string `json:"deliverables"`       // 交付物
	AcceptancePoints  []string `json:"acceptance_points"`  // 验收要点
	Misunderstandings []string `json:"misunderstandings"`  // 易误解点
	NeedConfirm       []string `json:"need_confirm"`       // 需向需求方确认的事项
	Mode              string   `json:"mode"`               // rule | llm
}

// Engine 需求翻译/分析引擎: 规则保底 + 可选 LLM 兜底。
type Engine struct {
	llm clarify.LLMClient
}

// NewEngine 构造引擎并复用 clarify 的客户端开关(配 Key 走真模型, 否则 Stub)。
func NewEngine() *Engine {
	return &Engine{llm: clarify.NewClient()}
}

// Mode 返回当前生效模式: llm(配了 ZANBUDDY_LLM_API_KEY) 或 rule(零依赖保底)。
func (e *Engine) Mode() string {
	if os.Getenv("ZANBUDDY_LLM_API_KEY") != "" {
		return "llm"
	}
	return "rule"
}

// ---- 对外入口 ----

// Translate 生成 AI 需求翻译。规则保底; 若 mode=llm 且调用成功, 用 LLM 润色正文。
func (e *Engine) Translate(t *model.Task) Translation {
	rule := ruleTranslate(t)
	if e.Mode() != "llm" {
		return rule
	}
	sys := `你是 Zanbuddy AI 接单平台的需求翻译助手。把需求方粗糙的描述，重写为一份清晰、无歧义、可直接交给接单方执行的需求书。保留所有客观信息（交付物、格式、期限、预算、风格），用条理分明的条目呈现，不要编造需求方未提供的信息，结尾提醒接单方开工前确认模糊项。`
	user := fmt.Sprintf("需求标题：%s\n需求描述：%s\n结构化补充：%+v", t.Title, t.Spec, t.SpecObj)
	out, err := e.llm.Chat(context.Background(), sys, []clarify.Message{{Role: "user", Content: user}})
	if err != nil || strings.TrimSpace(out) == "" {
		return rule // LLM 失败回退规则
	}
	rule.Doc = out
	rule.Mode = "llm"
	return rule
}

// Analyze 生成 AI 需求分析。规则保底; 若 mode=llm 且能解析, 用 LLM 补全结构化结果。
func (e *Engine) Analyze(t *model.Task) Analysis {
	rule := ruleAnalyze(t)
	if e.Mode() != "llm" {
		return rule
	}
	sys := `你是 Zanbuddy AI 接单平台的需求分析助手。针对一条开发任务，帮助接单方准确理解需求、避免理解偏差。请只输出一个 JSON 对象，字段：core_goal(字符串,核心目标)、deliverables(字符串数组,交付物)、acceptance_points(字符串数组,验收要点)、misunderstandings(字符串数组,易误解点)、need_confirm(字符串数组,需向需求方确认的事项)。不要编造需求方未提供的信息；need_confirm 必须包含开工前必须澄清的模糊项。`
	user := fmt.Sprintf("任务标题：%s\n需求描述：%s\n结构化补充：%+v", t.Title, t.Spec, t.SpecObj)
	out, err := e.llm.Chat(context.Background(), sys, []clarify.Message{{Role: "user", Content: user}})
	if err != nil {
		return rule // LLM 失败回退规则
	}
	if parsed, perr := parseAnalysisJSON(out); perr == nil && parsed.CoreGoal != "" && len(parsed.Deliverables) > 0 {
		parsed.Mode = "llm"
		return parsed
	}
	return rule // 解析失败回退规则
}

// ---- 规则实现 ----

func ruleTranslate(t *model.Task) Translation {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("【需求标题】%s\n", t.Title))
	if t.Spec != "" {
		b.WriteString(fmt.Sprintf("\n【需求概述】%s\n", t.Spec))
	}
	dels := splitLines(t.SpecObj.Deliverables)
	for _, it := range t.SpecObj.Items {
		dels = append(dels, it.Desc)
	}
	if len(dels) > 0 {
		b.WriteString("\n【交付物清单】\n")
		for i, d := range dels {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, d))
		}
	}
	if t.SpecObj.RequiredFormat != "" {
		b.WriteString(fmt.Sprintf("\n【交付格式与规范】%s\n", t.SpecObj.RequiredFormat))
	}
	if t.SpecObj.DeadlineAt > 0 {
		b.WriteString(fmt.Sprintf("\n【交付期限】%s\n", formatDeadline(t.SpecObj.DeadlineAt)))
	}
	if t.SpecObj.StyleRef != "" {
		b.WriteString(fmt.Sprintf("\n【风格 / 参考】%s\n", t.SpecObj.StyleRef))
	}
	if t.SpecObj.Samples != "" {
		b.WriteString(fmt.Sprintf("\n【参考样例】%s\n", t.SpecObj.Samples))
	}
	if t.BudgetCents > 0 {
		b.WriteString(fmt.Sprintf("\n【预算参考】¥%.2f（需求方挂出预算，最终以双方协商为准）\n", float64(t.BudgetCents)/100))
	}
	b.WriteString("\n【验收提示】接单方交付前请逐项对照「交付物清单」；验收以需求方在平台内确认的需求项为准。\n")
	b.WriteString("\n【协作约定】所有沟通、报价、交付、验收请在 Zanbuddy 平台内完成，平台提供资金托管与争议处理，保障双方权益。\n")

	sections := []string{}
	if t.Spec != "" {
		sections = append(sections, "需求概述")
	}
	if len(dels) > 0 {
		sections = append(sections, "交付物清单")
	}
	if t.SpecObj.RequiredFormat != "" {
		sections = append(sections, "交付格式与规范")
	}
	if t.SpecObj.DeadlineAt > 0 {
		sections = append(sections, "交付期限")
	}
	if t.BudgetCents > 0 {
		sections = append(sections, "预算参考")
	}
	return Translation{Doc: b.String(), Title: t.Title, Sections: sections, Mode: "rule"}
}

func ruleAnalyze(t *model.Task) Analysis {
	a := Analysis{Mode: "rule"}
	if t.Spec != "" {
		a.CoreGoal = t.Spec
	} else {
		a.CoreGoal = t.Title
	}
	for _, d := range splitLines(t.SpecObj.Deliverables) {
		a.Deliverables = append(a.Deliverables, d)
	}
	for _, it := range t.SpecObj.Items {
		a.Deliverables = append(a.Deliverables, it.Desc)
	}
	for _, d := range a.Deliverables {
		a.AcceptancePoints = append(a.AcceptancePoints, fmt.Sprintf("确认「%s」已交付且符合约定格式与数量", d))
	}
	a.Misunderstandings = []string{
		"「交付格式 / 尺寸 / 分辨率」若需求书未写清，开工前务必确认，避免返工。",
		"「修改次数 / 改稿范围」常被口头承诺无限改，建议事先约定免费修改轮次。",
		"「版权与商用授权」归属易生纠纷，需明确交付物是否含商用授权。",
		"「交付方式」平台不托管文件，需约定通过何种方式传递源文件与成品。",
	}
	if t.SpecObj.RequiredFormat == "" {
		a.NeedConfirm = append(a.NeedConfirm, "交付格式与规范未明确，开工前请与需求方确认。")
	}
	if t.SpecObj.DeadlineAt == 0 {
		a.NeedConfirm = append(a.NeedConfirm, "交付截止时间未填写，请确认预期工期与里程碑。")
	}
	if t.BudgetCents == 0 {
		a.NeedConfirm = append(a.NeedConfirm, "预算未填写，报价前确认需求方心理价位。")
	}
	if len(t.SpecObj.Items) == 0 && t.SpecObj.Deliverables == "" {
		a.NeedConfirm = append(a.NeedConfirm, "交付物清单为空，建议需求方在平台逐条列出需求项以便验收比对。")
	}
	a.NeedConfirm = append(a.NeedConfirm, "确认验收标准（以平台需求项为标尺）与验收时限。")
	a.NeedConfirm = append(a.NeedConfirm, "确认是否含源文件、商用授权与售后修改范围。")
	return a
}

// ---- 工具函数 ----

// splitLines 把换行/分号分隔的文本拆成条目, 过滤空串。
func splitLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '；' || r == ';' || r == '，' || r == ','
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// formatDeadline 把毫秒时间戳格式化为可读日期; 越界返回空串。
func formatDeadline(ms int64) string {
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(ms)
	if t.Year() < 2000 || t.Year() > 2100 {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

// parseAnalysisJSON 从 LLM 输出中提取并解析 Analysis JSON(兼容 ```json 围栏与多余文本)。
func parseAnalysisJSON(raw string) (Analysis, error) {
	s := strings.TrimSpace(raw)
	// 去掉 ```json ... ``` 围栏
	if i := strings.Index(s, "```"); i >= 0 {
		// 取最后一个 ``` 之后的内容或围栏内部
		if j := strings.Index(s[i+3:], "```"); j >= 0 {
			s = s[i+3 : i+3+j]
		} else {
			s = s[i+3:]
		}
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "json") {
			s = strings.TrimSpace(strings.TrimPrefix(s, "json"))
		}
	}
	// 截取首个 { 到最后一个 } 之间
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return Analysis{}, fmt.Errorf("no json object found")
	}
	s = s[start : end+1]
	var a Analysis
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return Analysis{}, err
	}
	return a, nil
}
