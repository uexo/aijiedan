// Package model 定义HiveGet M0 骨架的领域模型。
// 金额统一使用「分」(int64) 存储，避免浮点误差。
// 所有导出字段均带 json 标签, 作为 API 契约。
package model

import (
	"strings"
	"time"
)

// Role 用户角色
type Role string

const (
	RoleRequester Role = "requester" // 需求方
	RoleProvider  Role = "provider"  // 接单方(AI开发者)
	RoleAdmin     Role = "admin"     // 平台运营
)

// User 平台用户。M0 简化为内存结构，生产替换为 MySQL 行。
type User struct {
	ID           string   `json:"id"`
	Account      string   `json:"account,omitempty"`  // 登录账号(唯一, 注册用户才有)
	PasswordHash string   `json:"-"`                  // 密码哈希(PBKDF2), 永不外泄
	RealName     string   `json:"real_name,omitempty"` // 真实姓名(实名)
	Phone        string   `json:"phone,omitempty"`     // 手机号
	Name         string   `json:"name"`
	Role         Role     `json:"role"`
	KYCVerified  bool     `json:"kyc_verified"` // 实名认证(用户级)
	Skills       []string `json:"skills"`       // 接单方技能标签
	CreditScore  int      `json:"credit_score"` // 可携带信用分 0-100
	TaxBound     bool     `json:"tax_bound"`    // 是否已绑定税源地(接单方结算合规, 个体入驻)
	CreatedAt    int64    `json:"created_at"`
}

// TaskStatus 需求单状态
type TaskStatus string

const (
	TaskOpen   TaskStatus = "open"
	TaskMatched TaskStatus = "matched"
	TaskClosed TaskStatus = "closed"
)

// Task 需求单(AI开发任务)
type Task struct {
	ID          string      `json:"id"`
	RequesterID string      `json:"requester_id"`
	Title       string      `json:"title"`
	Category    string      `json:"category"` // 类目, 用于匹配召回
	BudgetCents int64       `json:"budget_cents"`
	Spec        string      `json:"spec"`     // 需求描述(自由文本摘要, 用于匹配召回)
	SpecObj     TaskSpec    `json:"spec_obj"` // 结构化规格(由 AI澄清/规则兜底补齐)
	Clarifications []string `json:"clarifications"` // 规则澄清问题清单(缺必填字段时生成)
	Status      TaskStatus  `json:"status"`
	OrgID       string      `json:"org_id,omitempty"` // 组织级发包归属(空=个人)
	CreatedAt   int64       `json:"created_at"`
}

// TaskSpec 结构化需求规格。M0 先用规则澄清兜底补齐, LLM 多轮澄清留待 P1-2。
// 必填(影响 spec_complete 与可报价): Deliverables / RequiredFormat / DeadlineAt。
// 建议(提升匹配精度): StyleRef / Samples。
type TaskSpec struct {
	Deliverables   string `json:"deliverables"`    // 交付物形态与数量, 如 "10 张竖版海报"
	RequiredFormat string `json:"required_format"` // 交付格式与规范, 如 "PNG 1080×1080"
	DeadlineAt     int64  `json:"deadline_at"`     // 期望交付截止(ms 时间戳), 0 表示未填
	StyleRef       string `json:"style_ref"`       // 风格/参考描述, 如 "极简风 / 附链接"
	Samples        string `json:"samples"`         // 参考样例/附件说明
}

// SpecComplete 必填结构化字段是否齐全(交付物/格式/截止)。
func (s TaskSpec) SpecComplete() bool {
	return strings.TrimSpace(s.Deliverables) != "" &&
		strings.TrimSpace(s.RequiredFormat) != "" &&
		s.DeadlineAt > 0
}

// ClarifySpec 规则澄清兜底: 返回缺失字段对应的中文澄清问题。
// 必填缺失直接提问; 建议项缺失仅作提示。LLM 澄清(P1-2)将在此基础上扩展多轮对话。
func ClarifySpec(s TaskSpec) []string {
	qs := []string{}
	if strings.TrimSpace(s.Deliverables) == "" {
		qs = append(qs, "请补充交付物形态与数量（如：10 张竖版海报 / 1 篇 1500 字文案）")
	}
	if strings.TrimSpace(s.RequiredFormat) == "" {
		qs = append(qs, "请明确交付格式与规范（如：PNG 1080×1080 / docx / mp4 1080p）")
	}
	if s.DeadlineAt <= 0 {
		qs = append(qs, "请填写期望交付截止时间")
	}
	if strings.TrimSpace(s.StyleRef) == "" {
		qs = append(qs, "建议补充风格/参考描述（如：极简风 / 附参考链接），以便接单方精准命中")
	}
	return qs
}

// RequiredClarifySpec 仅返回「必填」缺失项的澄清问题, 用于判定 spec_complete。
// 与 ClarifySpec 的区别: 不含风格/参考等可选建议项, 因此必填齐备即视为规格完整、可发布。
func RequiredClarifySpec(s TaskSpec) []string {
	qs := []string{}
	if strings.TrimSpace(s.Deliverables) == "" {
		qs = append(qs, "请补充交付物形态与数量（如：10 张竖版海报 / 1 篇 1500 字文案）")
	}
	if strings.TrimSpace(s.RequiredFormat) == "" {
		qs = append(qs, "请明确交付格式与规范（如：PNG 1080×1080 / docx / mp4 1080p）")
	}
	if s.DeadlineAt <= 0 {
		qs = append(qs, "请填写期望交付截止时间")
	}
	return qs
}

// CategoryDef 任务类目定义(类目法 taxonomy): 蜂单聚焦 AI 开发者垂直领域。
// 这是需求大厅筛选/发单下拉的"唯一事实来源", 前端不再硬编码类目。
type CategoryDef struct {
	ID    string `json:"id"`    // 机器标识(小写, 用于 task.category 存储与检索)
	Label string `json:"label"` // 中文展示名
	Desc  string `json:"desc"`  // 类目释义(辅助需求方选类)
}

// TaskCategories 平台当前开放的类目清单。新增类目只需在此追加。
var TaskCategories = []CategoryDef{
	{ID: "nlp", Label: "自然语言处理", Desc: "文本生成、摘要、翻译、对话、Prompt 工程"},
	{ID: "copywriting", Label: "文案/内容", Desc: "营销文案、小红书/公众号、脚本、SEO 内容"},
	{ID: "design", Label: "设计/视觉", Desc: "海报、Logo、UI、插画、品牌视觉"},
	{ID: "frontend", Label: "前端/Web", Desc: "页面、组件、Tailwind、可视化、小程序"},
	{ID: "backend", Label: "后端/服务端", Desc: "API、数据库、Go/Py/Node 服务、微服务"},
	{ID: "mobile", Label: "移动端", Desc: "iOS/Android、React Native、Flutter"},
	{ID: "data", Label: "数据/分析", Desc: "爬取、清洗、分析、BI、报表"},
	{ID: "ml", Label: "机器学习/模型", Desc: "训练、微调、推理部署、Agent 编排"},
	{ID: "devops", Label: "部署/运维", Desc: "CI/CD、容器、云、监控、自动化"},
	{ID: "audio", Label: "音频/语音", Desc: "TTS/ASR、配音、音频处理"},
	{ID: "video", Label: "视频", Desc: "剪辑、生成、字幕、特效"},
	{ID: "other", Label: "其他", Desc: "以上未覆盖的 AI 开发需求"},
}

// IsValidCategory 校验类目是否合法(发单/检索时使用)。
func IsValidCategory(c string) bool {
	for _, d := range TaskCategories {
		if d.ID == c {
			return true
		}
	}
	return false
}

// MatchStatus 匹配记录状态
type MatchStatus string

const (
	MatchRecommended MatchStatus = "recommended"
	MatchAccepted    MatchStatus = "accepted"
	MatchRejected    MatchStatus = "rejected"
)

// MatchRecord AI撮合产出: 某任务对某接单方的推荐
type MatchRecord struct {
	ID         string      `json:"id"`
	TaskID     string      `json:"task_id"`
	ProviderID string      `json:"provider_id"`
	Score      float64     `json:"score"`  // 0-100 综合匹配分
	Reason     string      `json:"reason"` // 推荐理由(可解释)
	Status     MatchStatus `json:"status"`
	CreatedAt  int64       `json:"created_at"`
}

// OrderStatus 订单状态机
type OrderStatus string

const (
	OrderPendingEscrow     OrderStatus = "pending_escrow"     // 待托管(合规闸门未过则卡在此)
	OrderActive            OrderStatus = "active"            // 托管成功, 进行中
	OrderDelivering        OrderStatus = "delivering"        // 接单方交付中
	OrderDelivered         OrderStatus = "delivered"         // 已交付, 待验收
	OrderCompleted         OrderStatus = "completed"         // 验收完成
	OrderDisputed          OrderStatus = "disputed"          // 纠纷中
	OrderCancelled         OrderStatus = "cancelled"         // 已取消(按状态退款)
	OrderPendingSettlement OrderStatus = "pending_settlement" // 合规未就绪, 等待结算
)

// 结算模式
const (
	SettleLump      = "lump"      // 一次性结算(默认)
	SettleMilestone = "milestone" // 里程碑分阶段结算
)

// 里程碑状态
const (
	MilestonePending = "pending"
	MilestoneDone    = "done"
)

// 默认改稿轮次上限(超出需协商或发起纠纷)
const DefaultMaxRevisions = 3

// Milestone 分阶段交付节点: 每阶段确认完成后释放对应款项(stub)。
type Milestone struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	AmountCents int64  `json:"amount_cents"`
	Status      string `json:"status"` // pending / done
	DoneAt      int64  `json:"done_at"`
}

// Order 订单(撮合成交后生成)
type Order struct {
	ID          string      `json:"id"`
	TaskID      string      `json:"task_id"`
	RequesterID string      `json:"requester_id"`
	ProviderID  string      `json:"provider_id"`
	MatchID     string      `json:"match_id"`
	AmountCents int64       `json:"amount_cents"`
	Status      OrderStatus `json:"status"`
	EscrowID    string      `json:"escrow_id"` // 支付托管ID(stub)
	Settled     bool        `json:"settled"`   // 是否已结算放款
	CreatedAt   int64       `json:"created_at"`

	// P1-4 订单完整化扩展
	MaxRevisions   int         `json:"max_revisions"`           // 改稿轮次上限(默认3)
	Milestones     []Milestone `json:"milestones,omitempty"`    // 里程碑(仅 milestone 模式非空)
	SettlementMode string      `json:"settlement_mode"`        // lump / milestone
	RefundCents    int64       `json:"refund_cents"`           // 退款金额(分)
	RefundPct      int         `json:"refund_pct"`             // 退款比例(0-100)
	CancelledBy    string      `json:"cancelled_by,omitempty"` // 取消发起方
	CancelReason   string      `json:"cancel_reason,omitempty"`
	CancelledAt    int64       `json:"cancelled_at"`
	DisputeReason  string      `json:"dispute_reason,omitempty"` // 纠纷事由
	DisputedBy     string      `json:"disputed_by,omitempty"`
	DisputedAt     int64       `json:"disputed_at"`
	ResolvedBy     string      `json:"resolved_by,omitempty"`   // 纠纷处理人(admin)
	ResolvedAt     int64       `json:"resolved_at"`
	ResolvedNote   string      `json:"resolved_note,omitempty"` // 纠纷处理结果说明
	OrgID          string      `json:"org_id,omitempty"`        // 组织级发包归属(空=个人)
}

// Delivery 交付物(支持多轮改稿)
type Delivery struct {
	ID        string `json:"id"`
	OrderID   string `json:"order_id"`
	Round     int    `json:"round"`  // 第几轮交付
	Content   string `json:"content"`
	Status    string `json:"status"` // submitted / accepted / rejected
	CreatedAt int64  `json:"created_at"`
}

// ComplianceItem 合规前置项
type ComplianceItem string

const (
	ItemKYC       ComplianceItem = "kyc"        // 用户实名认证(用户级)
	ItemICP       ComplianceItem = "icp"        // ICP备案(卡3)
	ItemTaxSource ComplianceItem = "tax_source" // 税源地签约(卡1)
	ItemEscrow    ComplianceItem = "escrow"     // 支付托管开通(卡2)
)

// Review 双向评价: 订单完成后, 参与双方互评。
// 评分 1-5; 被评价者的信用分由此重算(均值映射 0-100)。
type Review struct {
	ID           string `json:"id"`
	OrderID      string `json:"order_id"`
	TaskID       string `json:"task_id"`
	ReviewerID   string `json:"reviewer_id"`  // 评价方(订单参与方)
	RevieweeID   string `json:"reviewee_id"`  // 被评价方(订单另一方)
	ReviewerRole Role   `json:"reviewer_role"` // 评价方角色(用于展示)
	Rating       int    `json:"rating"`        // 1-5
	Comment      string `json:"comment"`
	CreatedAt    int64  `json:"created_at"`
}

// ComplianceState 平台级合规开关。生产存配置/DB; M0 存内存, 由 admin 翻转模拟合规线落地。
type ComplianceState struct {
	Items map[ComplianceItem]bool `json:"items"`
	Note  string                  `json:"note"`
}

// NewComplianceState 默认状态: 用户实名默认通过(M0简化), 其余合规卡点默认未落地(红灯)。
func NewComplianceState() *ComplianceState {
	return &ComplianceState{
		Items: map[ComplianceItem]bool{
			ItemKYC:       true,  // M0: 登录即视为已实名, 生产接真实KYC
			ItemICP:       false, // 卡3 备案 - 未落地
			ItemTaxSource: false, // 卡1 税源地 - 未落地
			ItemEscrow:    false, // 卡2 支付托管 - 未落地
		},
		Note: "M0默认: 仅KYC通过; ICP/税源地/支付托管待合规线(执行卡)落地后由admin翻转",
	}
}

// Now 辅助: 当前毫秒时间戳
func Now() int64 { return time.Now().UnixMilli() }

// ---- P2-2 IM 会话与消息(持久化) ----

// MsgStatus 消息状态(内容审核驱动)
const (
	MsgNormal    = "normal"    // 正常
	MsgReviewing = "reviewing" // 审核中(预留)
	MsgBlocked   = "blocked"   // 被拦截(命中审核规则, 不投递)
)

// ChatMessage IM 单条消息(持久化落库, 重启不丢)
type ChatMessage struct {
	ID        string `json:"id"`
	ConvID    string `json:"conv_id"`
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// Conversation 会话(双方 + 可选订单关联, 含未读计数)
type Conversation struct {
	ID        string `json:"id"`
	A         string `json:"a"`          // 一方 uid
	B         string `json:"b"`          // 另一方 uid
	OrderID   string `json:"order_id"`   // 关联订单(可选, 订单建立时写入)
	LastBody  string `json:"last_body"`  // 最后一条消息预览
	LastAt    int64  `json:"last_at"`    // 最后消息时间
	AUnread   int    `json:"a_unread"`   // A 的未读数
	BUnread   int    `json:"b_unread"`   // B 的未读数
	UpdatedAt int64  `json:"updated_at"`
}

// Peer 返回会话的另一方
func (c *Conversation) Peer(uid string) string {
	if uid == c.A {
		return c.B
	}
	return c.A
}

// UnreadOf 返回某方的未读数
func (c *Conversation) UnreadOf(uid string) int {
	if uid == c.A {
		return c.AUnread
	}
	if uid == c.B {
		return c.BUnread
	}
	return 0
}

// ---- P2-3 通知中心(站内信/邮件/短信, 平台→用户单向) ----

// NotiStatus 通知状态
const (
	NotiUnread = "unread" // 未读
	NotiRead   = "read"   // 已读
)

// NotiChannel 通知通道
const (
	ChannelInApp = "inapp" // 站内信(默认, 落库即达)
	ChannelEmail = "email" // 邮件(骨架, 待接 SMTP/腾讯云 SES)
	ChannelSMS   = "sms"   // 短信(骨架, 待接腾讯云 SMS)
)

// Notification 平台发给用户的通知(单向: 平台→用户)。
// 落库持久化, 重启不丢; 未读聚合 + 已读标记。邮件/短信为骨架通道, 演示走平台内闭环。
type Notification struct {
	ID        string `json:"id"`
	UID       string `json:"uid"`                  // 接收用户
	Channel   string `json:"channel"`              // inapp/email/sms
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`              // unread/read
	CreatedAt int64  `json:"created_at"`
	ReadAt    int64  `json:"read_at,omitempty"`    // 已读时间(未读为 0)
}

// ---- P3 企业版治理(设计阶段最小骨架: 组织 / 成员 / 角色) ----
// 多租户隔离维度统一用 OrgID: 个人用户拥有默认 Org(ORG-PERSONAL-<uid>)，组织用户归属显式 Org。

// Org 角色(组织内 RBAC)。
const (
	OrgRoleOwner  = "owner"  // 组织主: 管理组织/成员/解散
	OrgRoleAdmin  = "admin"  // 管理员: 管理成员/发包/验收结算
	OrgRoleMember = "member" // 成员: 发包/接单
	OrgRoleViewer = "viewer" // 只读: 查看报表
)

// Org 组织(企业租户)。OwnerID 为创建者 uid(组织主); 组织作为统一结算/开票主体走合规线 C。
type Org struct {
	ID        string `json:"id"`        // ORG-xxx
	Name      string `json:"name"`
	OwnerID   string `json:"owner_id"`  // 组织主 uid
	Plan      string `json:"plan"`      // free / team / enterprise
	Status    string `json:"status"`    // active / suspended
	CreatedAt int64  `json:"created_at"`
}

// OrgMember 组织成员与角色。
type OrgMember struct {
	OrgID   string `json:"org_id"`
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	JoinedAt int64  `json:"joined_at"`
}

// ---- P3 专项: 组织钱包 / 审批流 / 开放 API + Webhook ----

// OrgWallet 组织资金账户(企业版统一结算/开票主体)。平台内记账, 生产由通道回写。
type OrgWallet struct {
	OrgID        string `json:"org_id"`
	BalanceCents int64  `json:"balance_cents"` // 可用余额(分)
	FrozenCents  int64  `json:"frozen_cents"`  // 冻结(托管/审批预占, 分)
	UpdatedAt    int64  `json:"updated_at"`
}

// ApprovalType 审批单类型
const (
	ApprovalOrgPayout = "org_payout" // 组织钱包提现审批
)

// ApprovalStatus 审批状态
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// Approval 组织内审批单(member 发起 → owner/admin 审批)。
type Approval struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	Type        string `json:"type"`
	RefID       string `json:"ref_id,omitempty"` // 关联对象(如提现记录)
	AmountCents int64  `json:"amount_cents"`
	Status      string `json:"status"`
	CreatedBy   string `json:"created_by"`          // 发起人 uid
	ApprovedBy  string `json:"approved_by,omitempty"`
	Note        string `json:"note,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	ResolvedAt  int64  `json:"resolved_at,omitempty"`
}

// APIKey 组织开放 API 凭证(Bearer hgk_xxx)。KeyHash 存储, 明文仅创建时返回一次。
type APIKey struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	KeyHash   string `json:"-"`
	Label     string `json:"label"`
	Scopes    string `json:"scopes"` // 逗号分隔: tasks,wallet,webhooks
	Status    string `json:"status"` // active / revoked
	CreatedAt int64  `json:"created_at"`
}

// Webhook 组织事件订阅(任务创建/订单结算/提现审批等事件后台投递)。
type Webhook struct {
	ID        string   `json:"id"`
	OrgID     string   `json:"org_id"`
	URL       string   `json:"url"`
	Events    []string `json:"events"` // task_created / order_settled / payout_approved
	Secret    string   `json:"-"`
	Status    string   `json:"status"` // active / paused
	CreatedAt int64    `json:"created_at"`
}

// WebhookDelivery 投递记录(演示用, 内存/MySQL 均落库)。
type WebhookDelivery struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	WebhookID string `json:"webhook_id"`
	Event     string `json:"event"`
	Status    string `json:"status"` // pending / success / failed
	Attempts  int    `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
}

// ---- P2-1 资金账户体系(钱包/流水/提现) ----
// 红线: 平台不经手沉淀资金(二清)。真实资金走微信支付担保交易+分账(WeChatProvider);
// 此处 Wallet/Ledger 为平台内记账模型, 用于演示资金流向与对账; 生产由通道回写 ExternalID。

// TxType 资金流水类型
const (
	TxEscrow     = "escrow"     // 托管冻结(需求方支出, 资金进入平台托管)
	TxSettle     = "settle"     // 结算放款(接单方收入)
	TxCommission = "commission" // 平台佣金收入
	TxRefund     = "refund"     // 退款(退回需求方)
	TxWithdraw   = "withdraw"   // 提现(接单方出, 到银行卡)
	TxDeposit    = "deposit"    // 充值(演示, 需求方入)
)

// Wallet 用户钱包账户(平台内记账)
type Wallet struct {
	UID          string `json:"uid"`
	BalanceCents int64  `json:"balance_cents"` // 可用余额(分)
	FrozenCents  int64  `json:"frozen_cents"`  // 冻结中(已托管未结算 / 提现申请中)
	UpdatedAt    int64  `json:"updated_at"`
}

// Ledger 资金流水(对账核心): 每笔资金变动一条记录
type Ledger struct {
	ID           string `json:"id"`
	UID          string `json:"uid"`
	OrderID      string `json:"order_id,omitempty"`
	Type         string `json:"type"`         // TxEscrow/TxSettle/...
	AmountCents  int64  `json:"amount_cents"` // 正数=入账, 负数=出账
	BalanceAfter int64  `json:"balance_after"`
	ExternalID   string `json:"external_id,omitempty"` // 微信支付单号等(通道回写)
	Note         string `json:"note,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// WithdrawStatus 提现状态
const (
	WithdrawPending  = "pending"
	WithdrawApproved = "approved"
	WithdrawRejected = "rejected"
	WithdrawDone     = "done"
)

// Withdrawal 提现申请(接单方将可提现余额提至银行卡)
type Withdrawal struct {
	ID          string `json:"id"`
	UID         string `json:"uid"`
	AmountCents int64  `json:"amount_cents"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	ResolvedAt  int64  `json:"resolved_at,omitempty"`
	Note        string `json:"note,omitempty"`
}
