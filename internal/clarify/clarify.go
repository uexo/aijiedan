// Package clarify 实现 P1-2 AI 澄清：自然语言需求 → 多轮对话 → 结构化 spec。
//
// 设计要点：
//   - 仅依赖标准库(net/http)，不引入外部 LLM SDK；零新增第三方依赖。
//   - LLMClient 抽象两种实现：
//     * StubClient  —— 规则化离线兜底（无 API Key 也能跑通演示/冒烟），解析交付物/格式/截止/风格/预算/类目。
//     * OpenAIClient —— 调 OpenAI 兼容的 /v1/chat/completions（env 配置），JSON 抽取；解析失败自动回退规则化兜底。
//   - Session 维护多轮对话历史与草稿结构化 spec；Manager 内存会话存储。
//   - 抽取出的结构化字段衔接 P0-5 规则澄清(ClarifySpec)：缺必填项即生成中文澄清问题，不阻塞。
package clarify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"zanbuddy/internal/aihttp"
	"zanbuddy/internal/model"
)

// Message 单条对话消息。
type Message struct {
	Role    string `json:"role"` // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// ExtractedSpec LLM/规则从对话中抽取出的结构化需求规格（与 TaskSpec 对齐，外加 title/category/budget/spec）。
type ExtractedSpec struct {
	Title         string `json:"title"`
	Category      string `json:"category"`
	BudgetCents   int64  `json:"budget_cents"`
	Spec          string `json:"spec"`
	Deliverables  string `json:"deliverables"`
	RequiredFormat string `json:"required_format"`
	DeadlineAt    int64  `json:"deadline_at"` // 期望交付截止(ms 时间戳), 0 表示未填
	StyleRef      string `json:"style_ref"`
	Samples       string `json:"samples"`
}

// ToSpec 映射到 P0-5 的结构化 spec（用于发布/匹配）。
func (e ExtractedSpec) ToSpec() model.TaskSpec {
	return model.TaskSpec{
		Deliverables:   e.Deliverables,
		RequiredFormat: e.RequiredFormat,
		DeadlineAt:     e.DeadlineAt,
		StyleRef:       e.StyleRef,
		Samples:        e.Samples,
	}
}

// LLMClient 澄清对话与抽取抽象。
type LLMClient interface {
	// Chat 多轮澄清对话, 返回助手自然语言回复。
	Chat(ctx context.Context, sys string, hist []Message) (string, error)
	// Extract 从对话中抽取结构化 spec; 返回 nil 表示无法抽取。
	Extract(ctx context.Context, sys string, hist []Message) (*ExtractedSpec, error)
}

const sysClarify = `你是「联赞 Zanbuddy」AI 接单平台的智能需求澄清助手。
你的任务：与需求方多轮对话，把一句模糊的自然语言需求，逐步补全为可结构化匹配的任务规格。
必填字段（必须问到）：交付物形态与数量、交付格式与规范、期望交付截止时间。
建议字段（尽量引导）：风格/参考描述、参考样例、预算、类目。
要求：回复简洁、口语化、每次只聚焦最缺的 1-2 个问题；不要编造需求方没说过的信息；当必填项已齐备时，明确告诉对方可以发布。`

const sysExtract = `你是结构化抽取器。请严格只输出一个 JSON 对象（不要任何解释、不要 markdown 代码块），字段如下：
{
  "title": "简短任务标题(必填, 从对话推断)",
  "category": "类目: nlp|frontend|backend|data|design|video|audio|operations|general",
  "budget_cents": 整数(单位:分; 未提及填0),
  "spec": "需求摘要(一句话)",
  "deliverables": "交付物形态与数量(必填, 如 '10张竖版小红书封面'; 缺失填空串)",
  "required_format": "交付格式与规范(必填, 如 'PNG 1080x1080'; 缺失填空串)",
  "deadline_at": 整数(期望交付截止的 Unix 毫秒时间戳; 未提及填0),
  "style_ref": "风格/参考描述(缺失填空串)",
  "samples": "参考样例/附件说明(缺失填空串)"
}`

// ---- StubClient（规则化离线兜底） ----

// StubClient 不联网, 用规则从对话文本中抽取结构化字段, 适合演示与冒烟。
type StubClient struct{}

func (StubClient) Chat(_ context.Context, _ string, hist []Message) (string, error) {
	spec := parseSpec(hist)
	clar := model.ClarifySpec(spec.ToSpec())
	if len(clar) == 0 {
		return "已为你整理好需求规格 ✅ 可在右侧查看结构化结果并一键发布。若还有补充（如参考链接、验收标准）可继续告诉我。", nil
	}
	var b strings.Builder
	b.WriteString("为了给你精准匹配接单方，还差这几项：\n")
	for _, q := range clar {
		b.WriteString("• " + q + "\n")
	}
	b.WriteString("直接把答案发给我即可，例如「PNG 格式，10 月 20 号前交付」。")
	return b.String(), nil
}

func (StubClient) Extract(_ context.Context, _ string, hist []Message) (*ExtractedSpec, error) {
	s := parseSpec(hist)
	return &s, nil
}

// ---- OpenAIClient（可选，联网） ----

// OpenAIClient 调用 OpenAI 兼容的 chat/completions。无 Key 时不应被选用。
type OpenAIClient struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type oaReq struct {
	Model    string  `json:"model"`
	Messages []Message `json:"messages"`
}
type oaResp struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAIClient) chatOnce(ctx context.Context, sys string, hist []Message) (string, error) {
	msgs := []Message{{Role: "system", Content: sys}}
	msgs = append(msgs, hist...)
	body, _ := json.Marshal(oaReq{Model: c.Model, Messages: msgs})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var or oaResp
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return "", err
	}
	if or.Error != nil {
		return "", fmt.Errorf("llm error: %s", or.Error.Message)
	}
	if len(or.Choices) == 0 {
		return "", fmt.Errorf("llm empty choices")
	}
	return or.Choices[0].Message.Content, nil
}

func (c *OpenAIClient) Chat(ctx context.Context, sys string, hist []Message) (string, error) {
	r, err := c.chatOnce(ctx, sys, hist)
	if err != nil {
		// 联网失败 / 超时 / 解析异常: 回退规则化澄清回复, 保证对话不中断(绝不挂)。
		return StubClient{}.Chat(ctx, sys, hist)
	}
	return r, nil
}

func (c *OpenAIClient) Extract(ctx context.Context, sys string, hist []Message) (*ExtractedSpec, error) {
	raw, err := c.chatOnce(ctx, sys, hist)
	if err != nil {
		// 联网失败: 回退规则化抽取, 保证功能不中断
		s := parseSpec(hist)
		return &s, nil
	}
	spec, err := parseJSONSpec(raw)
	if err != nil {
		s := parseSpec(hist)
		return &s, nil
	}
	return spec, nil
}

// NewClient 依据环境变量选择实现: 有 ZANBUDDY_LLM_API_KEY 用 OpenAI, 否则用规则化 Stub。
func NewClient() LLMClient {
	if key := os.Getenv("ZANBUDDY_LLM_API_KEY"); key != "" {
		base := os.Getenv("ZANBUDDY_LLM_BASE_URL")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		modelName := os.Getenv("ZANBUDDY_LLM_MODEL")
		if modelName == "" {
			modelName = "gpt-4o-mini"
		}
		return &OpenAIClient{BaseURL: base, APIKey: key, Model: modelName, HTTP: aihttp.New(30 * time.Second)}
	}
	return StubClient{}
}

// ---- Session & Manager ----

// Session 一次澄清会话。
type Session struct {
	ID          string     `json:"id"`
	RequesterID string     `json:"requester_id"`
	Hist        []Message  `json:"hist"`
	Draft       ExtractedSpec `json:"draft"`
	FirstReply  string     `json:"first_reply"`
	LastErr     string     `json:"last_err,omitempty"`
	CreatedAt   int64      `json:"created_at"`
}

// Say 追加一条用户消息, 让 LLM 回复, 并刷新结构化草稿。
func (s *Session) Say(c LLMClient, userMsg string) (string, error) {
	s.Hist = append(s.Hist, Message{Role: "user", Content: userMsg})
	reply, err := c.Chat(context.Background(), sysClarify, s.Hist)
	if err != nil {
		return "", err
	}
	s.Hist = append(s.Hist, Message{Role: "assistant", Content: reply})
	if spec, e := c.Extract(context.Background(), sysExtract, s.Hist); e == nil && spec != nil {
		s.Draft = *spec
	}
	return reply, nil
}

// ExtractNow 重新从对话抽取结构化草稿。
func (s *Session) ExtractNow(c LLMClient) (*ExtractedSpec, error) {
	spec, err := c.Extract(context.Background(), sysExtract, s.Hist)
	if err != nil {
		return nil, err
	}
	if spec != nil {
		s.Draft = *spec
	}
	return spec, nil
}

// Manager 内存会话存储（M0 用; 生产换 Redis/DB）。
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	client   LLMClient
	seq      int64
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}, client: NewClient()}
}

// Mode 返回当前 LLM 实现标识: "llm"(已配 API Key) 或 "stub"(规则化离线兜底)。
func (m *Manager) Mode() string {
	if _, ok := m.client.(*OpenAIClient); ok {
		return "llm"
	}
	return "stub"
}

// Start 新建会话并发起首轮澄清(传入初始需求文本)。返回会话(含首轮回复与草稿)。
func (m *Manager) Start(requesterID, initialText string) *Session {
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("CL-%d-%d", time.Now().UnixNano(), m.seq)
	s := &Session{ID: id, RequesterID: requesterID, CreatedAt: model.Now()}
	m.sessions[id] = s
	m.mu.Unlock()

	reply, err := s.Say(m.client, initialText)
	if err != nil {
		s.LastErr = err.Error()
		s.FirstReply = "AI 澄清服务暂不可用：" + err.Error()
		return s
	}
	s.FirstReply = reply
	return s
}

// Continue 在已有会话中追加用户消息并获取回复。
func (m *Manager) Continue(id, userMsg string) (*Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", id)
	}
	reply, err := s.Say(m.client, userMsg)
	if err != nil {
		s.LastErr = err.Error()
		return s, err
	}
	s.FirstReply = reply // 复用字段承载最近一次回复, 便于前端取用
	return s, nil
}

// Get 取会话。
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Extract 重新抽取某会话的结构化草稿(不新增对话)。
func (m *Manager) Extract(id string) (*Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", id)
	}
	if _, err := s.ExtractNow(m.client); err != nil {
		return s, err
	}
	return s, nil
}

// ---- 规则化抽取（Stub 与 OpenAI 兜底共用） ----

var (
	reDeliver   = regexp.MustCompile(`(\d+)\s*(张|篇|个|份|套|条|页|秒|分钟|小时|版|支|组|张图|张海报)\s*([\p{Han}A-Za-z0-9/×x* ]{0,24}?)(?:[，。；,.;\s]|$)`)
	reDateMD    = regexp.MustCompile(`(\d{1,2})\s*[月/]\s*(\d{1,2})\s*[日号]?`)
	reRelDays   = regexp.MustCompile(`(\d+)\s*天`)
	reWeeks     = regexp.MustCompile(`(\d+)\s*周`)
	reMonths    = regexp.MustCompile(`(\d+)\s*个?月`)
	reBudgetU   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(万元|千元|元|块|w|W)`)
	reBudgetCtx = regexp.MustCompile(`(?:预算|报价|花费|价格|费用|约)\s*[:：]?\s*(\d+(?:\.\d+)?)\s*(万元|千元|元|块|w|W)?`)
	reDim       = regexp.MustCompile(`\d{3,4}\s*[×x*]\s*\d{3,4}`)
)

func userText(hist []Message) string {
	var b strings.Builder
	for _, m := range hist {
		if m.Role == "user" {
			b.WriteString(m.Content + "\n")
		}
	}
	return b.String()
}

func parseSpec(hist []Message) ExtractedSpec {
	src := strings.TrimSpace(userText(hist))
	e := ExtractedSpec{}

	// 标题/摘要: 取首条用户消息, 去口语前缀
	first := ""
	for _, m := range hist {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			first = m.Content
			break
		}
	}
	first = strings.TrimSpace(first)
	for _, p := range []string{"帮我", "我想", "我需要", "麻烦", "请", "找个", "找人", "需要", "我想找人", "能不能", "想做一个", "想做", "做一个", "我要", "希望"} {
		first = strings.TrimPrefix(first, p)
	}
	e.Title = strings.TrimSpace(first)
	e.Spec = e.Title

	// 类目
	e.Category = detectCategory(src)

	// 交付物
	if m := reDeliver.FindStringSubmatch(src); m != nil {
		deliver := strings.TrimSpace(m[1] + m[2] + m[3])
		e.Deliverables = deliver
	}

	// 交付格式
	var fmts []string
	lower := strings.ToLower(src)
	for _, kw := range []string{"png", "jpg", "jpeg", "gif", "pdf", "docx", "doc", "ppt", "pptx", "mp4", "mov", "svg", "zip", "psd", "ai", "txt"} {
		if strings.Contains(lower, kw) {
			fmts = append(fmts, strings.ToUpper(kw))
		}
	}
	for _, kw := range []string{"横版", "竖版", "竖屏", "横屏", "高清", "1080p"} {
		if strings.Contains(src, kw) {
			fmts = append(fmts, kw)
		}
	}
	if d := reDim.FindString(src); d != "" {
		fmts = append(fmts, strings.ReplaceAll(d, " ", ""))
	}
	if len(fmts) > 0 {
		e.RequiredFormat = strings.Join(fmts, " ")
	}

	// 截止时间
	e.DeadlineAt = detectDeadline(src)

	// 风格
	for _, kw := range []string{"极简", "简约", "高级感", "ins风", "ins", "日系", "科技感", "商务", "国潮", "复古", "可爱", "清新", "北欧", "性冷淡", "杂志风", "editorial", "孟菲斯", "赛博", "卡通", "扁平", "3d", "手绘", "中国风", "ins风"} {
		if strings.Contains(strings.ToLower(src), strings.ToLower(kw)) {
			e.StyleRef = kw
			break
		}
	}

	// 预算
	e.BudgetCents = detectBudget(src)

	// 样例提示
	if strings.Contains(src, "参考") || strings.Contains(src, "样例") || strings.Contains(src, "样稿") || strings.Contains(src, "附件") || strings.Contains(src, "链接") || strings.Contains(src, "类似") {
		e.Samples = "需求方提及了参考/样例说明，发布后可在详情补充链接"
	}

	return e
}

func detectCategory(s string) string {
	// 与 model.TaskCategories 的 ID 对齐：nlp|copywriting|design|frontend|backend|mobile|data|ml|devops|audio|video|other。
	// 顺序即优先级（copywriting 先于 nlp，避免"文案/小红书"被误判为自然语言处理）。
	rules := []struct {
		cat string
		kws []string
	}{
		{"copywriting", []string{"文案", "小红书", "公众号", "脚本", "seo", "内容营销", "软文", "推文", "种草", "带货文案"}},
		{"design", []string{"设计", "海报", "封面", "logo", "插画", "ui", "美工", "banner", "包装", "排版", "ps", "视觉", "画", "品牌"}},
		{"nlp", []string{"自然语言", "问答", "rag", "nlp", "翻译", "语言模型", "llm", "摘要", "聊天机器人", "chatbot", "prompt", "知识库"}},
		{"frontend", []string{"前端", "网页", "h5", "页面", "vue", "react", "小程序", "网站", "首页", "tailwind", "可视化"}},
		{"backend", []string{"后端", "全栈", "go", "服务", "api", "接口", "微服务", "数据库", "java", "python", "node", "服务端"}},
		{"mobile", []string{"移动端", "ios", "android", "react native", "flutter", "app开发"}},
		{"data", []string{"数据", "爬虫", "etl", "分析", "可视化", "报表", "清洗", "建模", "bi"}},
		{"ml", []string{"机器学习", "模型", "训练", "微调", "推理", "agent", "智能体", "深度学习"}},
		{"devops", []string{"运维", "部署", "ci/cd", "容器", "云", "监控", "自动化", "docker", "k8s"}},
		{"audio", []string{"音频", "配音", "播客", "音乐", "tts", "asr"}},
		{"video", []string{"视频", "剪辑", "动画", "短片", "宣传片", "pr", "ae"}},
	}
	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(strings.ToLower(s), strings.ToLower(kw)) {
				return r.cat
			}
		}
	}
	return "other"
}

func detectDeadline(s string) int64 {
	now := time.Now()
	if m := reDateMD.FindStringSubmatch(s); m != nil {
		mo, _ := atoi(m[1])
		da, _ := atoi(m[2])
		if mo >= 1 && mo <= 12 && da >= 1 && da <= 31 {
			year := now.Year()
			t := time.Date(year, time.Month(mo), da, 18, 0, 0, 0, now.Location())
			if t.Before(now) {
				t = t.AddDate(1, 0, 0)
			}
			return t.UnixMilli()
		}
	}
	if m := reMonths.FindStringSubmatch(s); m != nil {
		n, _ := atoi(m[1])
		return now.AddDate(0, n, 0).Add(18 * time.Hour).UnixMilli()
	}
	if m := reWeeks.FindStringSubmatch(s); m != nil {
		n, _ := atoi(m[1])
		return now.AddDate(0, 0, n*7).Add(18 * time.Hour).UnixMilli()
	}
	if m := reRelDays.FindStringSubmatch(s); m != nil {
		n, _ := atoi(m[1])
		return now.AddDate(0, 0, n).Add(18 * time.Hour).UnixMilli()
	}
	if strings.Contains(s, "下周") {
		// 下一个周一
		days := (8 - int(now.Weekday())) % 7
		if days == 0 {
			days = 7
		}
		return now.AddDate(0, 0, days).Add(18 * time.Hour).UnixMilli()
	}
	if strings.Contains(s, "明天") {
		return now.AddDate(0, 0, 1).Add(18 * time.Hour).UnixMilli()
	}
	if strings.Contains(s, "后天") {
		return now.AddDate(0, 0, 2).Add(18 * time.Hour).UnixMilli()
	}
	return 0
}

func detectBudget(s string) int64 {
	best := int64(0)
	if m := reBudgetU.FindStringSubmatch(s); m != nil {
		v, _ := parseFloat(m[1])
		unit := m[2]
		best = toCents(v, unit)
	}
	if best == 0 {
		if m := reBudgetCtx.FindStringSubmatch(s); m != nil {
			v, _ := parseFloat(m[1])
			unit := m[2]
			if unit == "" {
				unit = "元"
			}
			best = toCents(v, unit)
		}
	}
	return best
}

func toCents(v float64, unit string) int64 {
	switch unit {
	case "万元":
		return int64(v * 1e6)
	case "千元":
		return int64(v * 1e5)
	case "w", "W":
		return int64(v * 1e6)
	default: // 元/块
		return int64(v * 100)
	}
}

func parseJSONSpec(raw string) (*ExtractedSpec, error) {
	raw = strings.TrimSpace(raw)
	// 去掉可能的 ```json ... ``` 围栏
	if i := strings.Index(raw, "```"); i >= 0 {
		// 取最后一个 ``` 之后的或中间
		start := i + 3
		if j := strings.Index(raw[start:], "```"); j >= 0 {
			raw = raw[start : start+j]
		} else {
			raw = raw[start:]
		}
	}
	raw = strings.TrimSpace(raw)
	// 截取第一个 { 到最后一个 }
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var e ExtractedSpec
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func atoi(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, len(s) > 0
}

func parseFloat(s string) (float64, bool) {
	// 仅支持非负十进制(整数或小数), 足够预算解析
	dot := false
	var n float64
	var frac float64
	var fscale float64
	seen := false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			seen = true
			if dot {
				fscale *= 10
				frac = frac*10 + float64(c-'0')
			} else {
				n = n*10 + float64(c-'0')
			}
		} else if c == '.' && !dot {
			dot = true
			fscale = 1
		} else {
			break
		}
	}
	if !seen {
		return 0, false
	}
	if fscale > 0 {
		return n + frac/fscale, true
	}
	return n, true
}
