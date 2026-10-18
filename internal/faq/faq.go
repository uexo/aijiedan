// Package faq 实现 B3「AI 客服应答」：IM 会话中的常见问题自动应答（FAQ bot）。
//
// 设计要点（对齐 B1 的可插拔 + 零依赖保底 + 失败回退范式）：
//   - 默认规则意图匹配（关键词 → 话术），零外部依赖，演示/冒烟/离线均可用。
//   - 可选 LLM 兜底：复用 B1 的开关（环境变量 LINKZAM_LLM_API_KEY），有 Key 才走真模型。
//   - 任何联网失败/超时自动回退规则化兜底或「转人工」提示，绝不挂死、绝不冒充真人。
//   - 仅依赖标准库 + 复用 internal/clarify（LLMClient 接口与 OpenAI 客户端）与 internal/model。
package faq

import (
	"context"
	"os"
	"strings"

	"linkzam/internal/clarify"
	"linkzam/internal/model"
)

// Reply AI 给出的应答建议。
type Reply struct {
	Text    string `json:"text"`    // 应答正文（不含「AI 自动回复」前缀，前缀由发送侧按场景添加）
	Intent  string `json:"intent"`  // 命中的意图: quote|timeline|delivery|acceptance|payment|refund|revision|invoice|comm|commission|kyc|settle|none
	Type    string `json:"type"`    // faq(规则命中) | llm(LLM 兜底) | ""(无建议)
	Matched bool   `json:"matched"` // 是否给出有效建议（false 时前端可提示「暂无 AI 建议，转人工」）
}

// intentRule 一条规则意图：关键词集合 + 话术。命中任一关键词即匹配（先到先得）。
type intentRule struct {
	intent string
	kws    []string
	reply  string
}

// rules 规则库。顺序即优先级，越具体越靠前。话术与平台真实规则对齐（10% 佣金、托管、KYC、税源地等）。
var rules = []intentRule{
	{
		intent: "quote",
		kws:    []string{"报价", "多少钱", "价格", "费用", "收费", "怎么收费", "报价区间", "报多少", "要多少", "预算多少", "开价"},
		reply:  "接单方可一键获取「AI 建议报价」：系统根据任务类目基准价、复杂度、紧急度及您的历史成交，给出建议报价区间。您可在任务详情页直接采纳或自行调整；含税/不含税请与需求方在订单中明确。",
	},
	{
		intent: "timeline",
		kws:    []string{"工期", "多久", "几天", "多长时间", "什么时候交", "交付时间", "交期", "加急", "赶时间", "什么时候能"},
		reply:  "交付工期由双方在订单中协商确认。接单方接单时请如实评估并在订单填写预计交付时间；如需加急，可在沟通中与需求方协商，加急通常对应更高报价。",
	},
	{
		intent: "delivery",
		kws:    []string{"怎么交付", "如何交付", "交付方式", "交付物", "提交", "交付在哪", "怎么交", "上传交付"},
		reply:  "请在订单页通过「交付」按钮，按需求清单逐项提交交付物，支持多轮修改。建议所有交付物留存记录，便于后续验收比对。",
	},
	{
		intent: "acceptance",
		kws:    []string{"验收", "怎么验收", "如何验收", "达标", "合格", "通过", "确认完成", "结算条件"},
		reply:  "需求方会按发布时填写的需求清单逐条验收，全部达标后订单才会结算放款；若有未达标项会退回接单方修正。交付前建议先自查需求清单。",
	},
	{
		intent: "payment",
		kws:    []string{"付款", "怎么付", "钱怎么", "安全吗", "托管", " escrow", "资金", "放款", "到账", "打款", "何时付款"},
		reply:  "为保障双方资金安全，款项由平台托管（escrow），需求方确认验收通过后才放款给接单方，全程不私下转账。",
	},
	{
		intent: "refund",
		kws:    []string{"退款", "退钱", "退单", "纠纷", "仲裁", "争议", "不满意", "没做好"},
		reply:  "若对验收或交付有争议，可发起纠纷由平台仲裁。托管资金在仲裁结论前不会被单方挪用。",
	},
	{
		intent: "revision",
		kws:    []string{"改稿", "修改", "几稿", "改几次", "能改吗", "返工", "调整", " revisions"},
		reply:  "默认支持 3 次修改（max_revisions），具体以订单约定为准。超出次数的大改建议与需求方协商补充报价。",
	},
	{
		intent: "invoice",
		kws:    []string{"发票", "开票", "税", "税源地", "报销", "开票吗"},
		reply:  "接单方完成个体入驻并绑定税源地（tax_bound）后，可向需求方提供合规发票。平台采用 10% 低佣金模式。",
	},
	{
		intent: "kyc",
		kws:    []string{"实名", "kyc", "认证", "审核资料", "入驻", "绑定税", "资格", "才能接单"},
		reply:  "接单方需完成实名认证（KYC）并绑定税源地，方可接单与结算；需求方可先浏览与发包。",
	},
	{
		intent: "settle",
		kws:    []string{"结算", "提现", "钱包", "钱到哪", "怎么提", "佣金多少", "抽成", "回款"},
		reply:  "订单验收通过后，托管款项将结算至您的平台钱包并扣取 10% 佣金，可发起提现；提现需先完成 KYC 与税源地绑定。",
	},
	{
		intent: "commission",
		kws:    []string{"佣金", "抽成", "平台费", "服务费", "手续费", "平台怎么赚钱"},
		reply:  "联赞（LinkZam）对 AI 开发者接单仅收取 10% 低佣金，并提供会员/企业 SaaS 等增值服务。",
	},
	{
		intent: "comm",
		kws:    []string{"私下", "微信", "加我", "站外", "绕过平台", "线下", "直接转", "电话联系"},
		reply:  "请全程在平台 IM 内沟通与交付，避免私下交易。私下转账、加微信等行为会被合规闸门拦截，且不受平台资金托管保护。",
	},
}

// sysFaq LLM 兜底系统提示：始终自称 AI 客服、引导平台内交易、简洁、不确定转人工。
const sysFaq = `你是「联赞 LinkZam」AI 接单平台的 AI 客服助手。请用简洁中文回答接单方与需求方在合作沟通中的常见问题（报价、工期、交付、验收、付款托管、退款、改稿、发票、合规等）。
务必遵守：
1) 始终表明你是 AI 客服，不冒充真人；
2) 引导用户在平台内完成交易，绝不建议私下联系或加微信；
3) 控制在 2-3 句话；
4) 不确定的事项，明确建议用户等待人工回复。`

// Engine FAQ 引擎：持有可选 LLM 客户端（复用 B1 clarify 开关）。
type Engine struct {
	llm clarify.LLMClient
}

// NewEngine 构造引擎；有 LINKZAM_LLM_API_KEY 时内部客户端为 OpenAI，否则为规则化 Stub（不会真正联网）。
func NewEngine() *Engine {
	return &Engine{llm: clarify.NewClient()}
}

// Mode 返回当前能力: "llm"(已配 API Key, 可 LLM 兜底) 或 "faq"(仅规则)。
func (e *Engine) Mode() string {
	if os.Getenv("LINKZAM_LLM_API_KEY") != "" {
		return model.AITypeLLM
	}
	return model.AITypeFAQ
}

// Suggest 针对一条入站消息给出应答建议。
//   - 规则命中 → Reply{Type: faq, Matched: true}
//   - 未命中且 LLM 可用且消息像提问 → 调 LLM 兜底（失败回退转人工）→ Reply{Type: llm, Matched: true}
//   - 无任何可用建议 → Reply{Type: "", Matched: false}（前端提示转人工）
//
// taskTitle 用于给 LLM 提供上下文（可选）。
func (e *Engine) Suggest(userMsg, taskTitle string) Reply {
	msg := strings.TrimSpace(userMsg)
	if msg == "" {
		return Reply{Intent: "none"}
	}
	// 1) 规则意图匹配（零依赖, 优先）
	if intent, reply, ok := matchIntent(msg); ok {
		return Reply{Text: reply, Intent: intent, Type: model.AITypeFAQ, Matched: true}
	}
	// 2) LLM 兜底：仅当配置且消息像提问
	if e.Mode() == model.AITypeLLM && looksLikeQuestion(msg) {
		if text, ok := e.llmFallback(msg, taskTitle); ok && strings.TrimSpace(text) != "" {
			return Reply{Text: text, Intent: "llm", Type: model.AITypeLLM, Matched: true}
		}
	}
	// 3) 无建议
	return Reply{Intent: "none"}
}

// matchIntent 规则匹配：返回 (intent, reply, matched)。
func matchIntent(msg string) (string, string, bool) {
	lower := strings.ToLower(msg)
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return r.intent, r.reply, true
			}
		}
	}
	return "", "", false
}

// looksLikeQuestion 粗判是否像提问（决定是否值得调用 LLM 兜底, 避免对闲聊/表情刷 LLM）。
func looksLikeQuestion(msg string) bool {
	if strings.ContainsAny(msg, "？?") {
		return true
	}
	for _, w := range []string{"吗", "呢", "怎么", "如何", "什么", "为什么", "能否", "可以", "是否", "有没有", "能不能", "求", "请问", "帮我看", "咨询"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// llmFallback 调用 LLM 生成回复；任何错误返回 ( "", false ) 由上层转人工兜底。
func (e *Engine) llmFallback(msg, taskTitle string) (string, bool) {
	ctx := context.Background()
	sys := sysFaq
	if taskTitle != "" {
		sys += "\n当前任务标题（仅供参考，不要复述）：" + taskTitle
	}
	hist := []clarify.Message{{Role: "user", Content: msg}}
	reply, err := e.llm.Chat(ctx, sys, hist)
	if err != nil {
		return "", false
	}
	return reply, true
}
