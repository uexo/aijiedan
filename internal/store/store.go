// Package store 提供存储抽象与内存实现(零依赖, M0 默认与测试用)。
// 另见 mysql.go: 真实 MySQL 实现遵循同一 Store 接口, 上层 service/handler 不感知。
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zanbuddy/internal/model"
)

// 存储层的哨兵错误。只用于"判断与写入落在同一个原子点"的那几个方法:
// 上层拿得到具体原因, 才能给用户一句说得通的话, 而不是统一报"服务出错"。
var (
	// ErrOrderGone 订单不存在。
	ErrOrderGone = errors.New("store: order not found")
	// ErrNotDeliverable 订单此刻不在可交付状态(别人先动过, 或还没开工/已交付)。
	ErrNotDeliverable = errors.New("store: order not deliverable")
	// ErrRevisionLimit 改稿轮次已用尽。
	ErrRevisionLimit = errors.New("store: revision limit reached")
	// ErrNotReviewable 订单此刻不在待验收状态。
	ErrNotReviewable = errors.New("store: order not reviewable")
	// ErrNoDelivery 订单标着已交付, 却没有对应的交付记录(数据不一致)。
	ErrNoDelivery = errors.New("store: no delivery record")
	// ErrReviewExists 这个人已经评过这一单了。
	// 一个人对同一单只能评一次: AvgRating 是对全部评价求均值, 重复评一次等于自己给自己刷分。
	ErrReviewExists = errors.New("store: review already submitted")
	// ErrEndorsementExists 这个人已经给这一单写过背书了。
	// 背书原文不可编辑, 想补充只能追加——所以重复提交不是"覆盖", 是拒收。
	ErrEndorsementExists = errors.New("store: endorsement already written")
	// ErrEndorsementGone 要追加的背书不存在(或已被置 void 后清理)。
	ErrEndorsementGone = errors.New("store: endorsement not found")
)

// fallbackSlug 给没有短号的用户补一个确定性短号。
//
// 为什么需要: v1 的注册路径不生成 slug, 而 002 迁移给 user.slug 建了唯一索引。
// 两条空值会立刻撞 1062, 表现为"第二个注册的人报服务端错误"。
// 用 id 的哈希前 3 字节: 确定性(重试不会产生新值)、固定 7 字符、碰撞概率可忽略。
func fallbackSlug(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= 8 {
		return strings.ToLower(id)
	}
	sum := sha256.Sum256([]byte(id))
	return "u" + hex.EncodeToString(sum[:3])
}

// Store 存储抽象: 内存(MemStore)与 MySQL(mysqlStore)都遵循此接口。
// 上层 service/handler 仅依赖接口, 切换存储不影响业务代码。

// TaskFilter 需求大厅检索条件(全部可选; 零值表示不约束)。
type TaskFilter struct {
	Category       string // 类目精确匹配(为空忽略)
	Status         string // 状态精确匹配(为空表示不限, 默认仅 open 由调用方决定)
	MinBudget      int64  // 预算下限(分); 0 忽略
	MaxBudget      int64  // 预算上限(分); 0 忽略
	Q              string // 关键词: 命中任务全文档(标题/描述/交付物/格式/风格/样例, 大小写不敏感); 为空忽略
	Skill          string // 技能/标签关键词: 同 Q 的全文档匹配; 为空忽略
	DeadlineAfter  int64  // 截止时间下限(ms 时间戳); 仅过滤已设截止的任务; 0 忽略
	DeadlineBefore int64  // 截止时间上限(ms 时间戳); 仅过滤已设截止的任务; 0 忽略
	Sort           string // newest | budget_desc | budget_asc; 默认 newest
	Limit          int    // 返回上限; <=0 表示不限
}

type Store interface {
	NextID(prefix string) string
	GetUser(id string) (*model.User, bool)
	GetUserByAccount(account string) (*model.User, bool)
	GetUserBySlug(slug string) (*model.User, bool)
	GetUserByPhone(phone string) (*model.User, bool)
	GetProfile(userID string) (*model.ProviderProfile, bool)
	PutProfile(p *model.ProviderProfile)
	PutUser(u *model.User)
	ListUsers() []*model.User
	ListProviders() []*model.User
	PutTask(t *model.Task)
	GetTask(id string) (*model.Task, bool)
	// ClaimOpenTask 把 open 改成 matched, 条件是"此刻仍是 open"。
	// 返回 false 表示没抢到(已被别人接走或状态不是 open)。
	//
	// 为什么必须是存储层的一个原子操作, 而不是 GetTask 判一下再 PutTask:
	// 两个接单方同时点认领, 读-判-写之间会双开, 结果是同一单生成两个订单,
	// 需求方要同时拒一个。判断和写入必须落在同一个锁 / 同一条 UPDATE 里。
	ClaimOpenTask(taskID string) bool
	// WithdrawOpenTask 把 open 改成 withdrawn, 条件是"此刻仍是 open 且是这个人发的"。
	// 返回 false 表示条件不成立(被人接走了 / 不是他的单 / 已经撤过)。
	//
	// 和 ClaimOpenTask 是同一类问题的两面: 需求方点撤回的同一秒可能有人点认领。
	// 上层先 GetTask 判一下再写, 中间这一瞬就能让"已经有人在做的单"从大厅消失,
	// 而订单还活着——需求方在大厅找不到自己的单, 接单方却在等他验收。
	WithdrawOpenTask(taskID, requesterID string) bool
	ListOpenTasks() []*model.Task
	ListTasks() []*model.Task
	// ListTasksByRequester 我发的需求(不限状态), 按创建时间倒序。
	// 与"我接的单"不同: 没人接的单不在任何订单里, 只能靠这条查出来。
	ListTasksByRequester(userID string) []*model.Task
	SearchTasks(f TaskFilter) []*model.Task
	PutMatch(m *model.MatchRecord)
	GetMatch(id string) (*model.MatchRecord, bool)
	ListMatchesByTask(taskID string) []*model.MatchRecord
	// ReplaceMatchReasons 用这批理由整体替换一次匹配的理由(先删后插, 同一原子点)。
	//
	// 为什么是"替换"而不是"追加": 推荐位每被刷新一次就重新算一次理由,
	// 追加的话库里会积累同一 match 的 N 轮快照, 事后回溯"当时推荐理由是什么"
	// 只能靠时间戳猜。替换之后 match_id 下的理由永远只有当前这一版,
	// match_record.created_at 保留首次推荐时间, 用来回答"这单什么时候开始匹配的"。
	ReplaceMatchReasons(matchID string, rs []*model.MatchReason)
	ListMatchReasons(matchID string) []*model.MatchReason
	PutOrder(o *model.Order)
	GetOrder(id string) (*model.Order, bool)
	// TransitionOrder 把订单从 from 改到 to, 条件是"此刻仍是 from"。
	// 返回 false 表示状态已不是 from(别人先动过)或写失败。
	//
	// 为什么不让上层 GetOrder 判一下再 PutOrder: 需求方双击确认、
	// 或者确认和退回同时点, 读-判-写之间会互相踩, 结果是订单状态被后到的请求覆盖。
	TransitionOrder(orderID string, from, to model.OrderStatus) bool
	// CancelPendingOrder 取消一个待确认订单, 同时把它占住的 task 退回 open。
	//
	// 两行必须在同一个事务里: 只退 task 不退订单, 同一单会出现两个活订单,
	// 需求方能同时确认两份, 同一件事付两次钱; 只退订单不退 task,
	// 单子从大厅消失且谁也接不到, 只能人工改库。
	CancelPendingOrder(orderID, taskID, by, reason string, at int64) bool
	// DeliverOrder 记一条交付, 同时把订单从 from 推进到 to, 两件事落在同一个原子点。
	//
	// 为什么不是 PutDelivery 再 TransitionOrder: 两步之间失败会留下
	// "交付记录在库里、订单还在进行中"的孤儿, 下一次交付又生成一轮,
	// 需求方看到两轮内容一样的交付。轮次也必须在这里算——
	// 两个并发的提交各自 Count 一次再各自 +1, 会同时拿到第 2 轮。
	//
	// 返回的 *model.Delivery 由实现生成 ID 与轮次, 上层不再猜。
	DeliverOrder(orderID, content, link string, items []model.DeliveryItem,
		from []model.OrderStatus, to model.OrderStatus, maxRound int, at int64) (*model.Delivery, error)
	// ReviewOrder 需求方验收: accept=true 订单完成、需求单关闭; accept=false 打回重做。
	//
	// 验收结论记在最后一轮交付上, 与订单状态在同一个原子点:
	// 只改订单不改交付, 打回原因就丢了, 接单方下一轮只能猜;
	// 只改交付不改订单, 单子永远停在待验收, 需求方点多少次都没反应。
	//
	// 打回轮次上限也在实现里判: 最后一轮已经用满 max_revisions 时再打回,
	// 接单方就交不出下一轮, 等于把单子打死。
	ReviewOrder(orderID string, accept bool, note string, at int64) error
	ListOrders() []*model.Order
	ListOrdersByUser(userID string) []*model.Order
	ListOrdersByTask(taskID string) []*model.Order
	ListTasksByOwner(ownerID string) []*model.Task
	PutDelivery(d *model.Delivery)
	GetDelivery(id string) (*model.Delivery, bool)
	ListDeliveriesByOrder(orderID string) []*model.Delivery
	PutReview(r *model.Review)
	// SubmitReview 提交一条评价, 满足条件时把同一单的两条一起揭盲并留痕。
	//
	// 为什么写入、揭盲、留痕要在一个方法里做完: 三者必须同生共死。
	// 只写不揭盲 → 对方的评价永远被压着; 揭了不写事件 → 事件流与事实不符,
	// 事后查不出"他这个分是从哪条评价来的"。
	// 重复提交返回 ErrReviewExists(唯一键 uk_order_reviewer 兜底)。
	SubmitReview(r *model.Review, at int64) error
	GetReview(id string) (*model.Review, bool)
	ListReviewsByOrder(orderID string) []*model.Review
	ListReviewsByReviewee(revieweeID string) []*model.Review
	// ListRevealedReviewsByReviewee 只返回已可见的评价, 用于算 AvgRating。
	//
	// 为什么不改 ListReviewsByReviewee 而是新开一个: 老方法还在给 v1 页面用,
	// 在冻结区里改行为等于让没人测的代码跟着变。新方法只让 v2 的匹配用。
	// 可见的判定见 model.ReviewVisible(已揭盲, 或单方提交已满 7 天)。
	ListRevealedReviewsByReviewee(revieweeID string, now int64) []*model.Review
	// --- 背书 (M4) ---
	//
	// PutEndorsement 落一条背书。同一订单同一作者重复写返回 ErrEndorsementExists
	// (唯一键 uk_order_author 兜底, 不靠应用层先查——先查再写中间有窗口)。
	//
	// 为什么权重也在这层算而不在 trade 层算一遍再传进来:
	// "同一对用户 12 个月只计一次" 要读历史记录, 读和写必须在同一个临界区。
	// 分成两步会出现两个并发请求都读到"没有历史", 于是都记 100 分。
	PutEndorsement(e *model.Endorsement) error
	GetEndorsement(id string) (*model.Endorsement, bool)
	GetEndorsementByOrderAuthor(orderID, authorID string) (*model.Endorsement, bool)
	// ListEndorsementsByTarget 某个人收到的、可见且计权重的背书, 新的在前。
	// "可见" = active 或 flagged(flagged 页面照常展示、带标记, 藏起来等于替用户做判断);
	// "计权重" = weight > 0(12 个月内第二条记 0, 记录留着只是不进列表)。
	// 要不要把 flagged 拿去当推荐理由, 是使用方的事——匹配侧自己再收一道, 见 match.rankableEndorsements。
	ListEndorsementsByTarget(targetID string) []*model.Endorsement
	// ListEndorsementsByAuthor 某个人写出去的背书(全部状态), 用于"我写过的"。
	ListEndorsementsByAuthor(authorID string) []*model.Endorsement
	PutEndorsementAppend(a *model.EndorsementAppend) error
	ListEndorsementAppends(endorsementID string) []*model.EndorsementAppend
	// ListCreditEvents 某个用户的信用事件流(按发生顺序)。
	// 写入不单独开口子: order_completed 由 ReviewOrder 在同一个事务/临界区里落,
	// 这样才能保证"订单完成"与"信用留痕"要么都在, 要么都不在。
	ListCreditEvents(userID string) []*model.CreditEvent
	GetCompliance() *model.ComplianceState
	SetCompliance(item model.ComplianceItem, on bool)

	// --- 钱包/流水/提现 (P2-1) ---
	GetWallet(uid string) (*model.Wallet, bool)
	GetOrCreateWallet(uid string) *model.Wallet
	PutWallet(w *model.Wallet)
	ListWallets() []*model.Wallet
	ListLedgerByUser(uid string) []*model.Ledger
	PutLedger(l *model.Ledger)
	ListWithdrawals() []*model.Withdrawal
	ListWithdrawalsByUser(uid string) []*model.Withdrawal
	GetWithdrawal(id string) (*model.Withdrawal, bool)
	PutWithdrawal(w *model.Withdrawal)

	// --- IM 会话与消息 (P2-2) ---
	SaveMessage(m *model.ChatMessage)
	ListMessages(convID string) []*model.ChatMessage
	GetConversation(cid string) (*model.Conversation, bool)
	GetOrCreateConversation(a, b, orderID string) *model.Conversation
	SaveConversation(c *model.Conversation)
	ListConversations(uid string) []*model.Conversation

	// --- 通知中心 (P2-3) ---
	SaveNotification(n *model.Notification)
	GetNotification(id string) (*model.Notification, bool)
	ListNotifications(uid string, limit int) []*model.Notification
	MarkNotificationRead(uid, id string) (*model.Notification, bool)
	CountUnread(uid string) int

	// --- 企业版治理 (P3 最小骨架: 组织/成员/角色) ---
	SaveOrg(o *model.Org)
	GetOrg(id string) (*model.Org, bool)
	ListOrgsByOwner(ownerUID string) []*model.Org
	SaveOrgMember(m *model.OrgMember)
	ListOrgMembers(orgID string) []*model.OrgMember
	GetOrgMember(orgID, uid string) (*model.OrgMember, bool)

	// P3 专项: 组织钱包 / 审批流 / 开放 API + Webhook
	GetOrgWallet(orgID string) (*model.OrgWallet, bool)
	GetOrCreateOrgWallet(orgID string) *model.OrgWallet
	PutOrgWallet(w *model.OrgWallet)
	SaveApproval(a *model.Approval)
	GetApproval(id string) (*model.Approval, bool)
	ListApprovalsByOrg(orgID string) []*model.Approval
	SaveAPIKey(k *model.APIKey)
	GetAPIKeyByHash(hash string) (*model.APIKey, bool)
	ListAPIKeysByOrg(orgID string) []*model.APIKey
	SaveWebhook(w *model.Webhook)
	ListWebhooksByOrg(orgID string) []*model.Webhook
	SaveWebhookDelivery(d *model.WebhookDelivery)
	ListWebhookDeliveriesByOrg(orgID string) []*model.WebhookDelivery
	ListOrdersByOrg(orgID string) []*model.Order
}

// MemStore 内存实现(零依赖)。
type MemStore struct {
	mu            sync.RWMutex
	users         map[string]*model.User
	profiles      map[string]*model.ProviderProfile
	tasks         map[string]*model.Task
	taskIndex     map[string]map[string]bool // 倒排索引: 拉丁词 -> 任务ID集合(加速关键词检索)
	matches       map[string]*model.MatchRecord
	matchReasons  map[string][]*model.MatchReason // key: match_id
	orders        map[string]*model.Order
	deliveries    map[string]*model.Delivery
	reviews       map[string]*model.Review
	endorsements  map[string]*model.Endorsement
	endorseAppend map[string][]*model.EndorsementAppend // key: endorsement_id
	creditEvents  map[string]*model.CreditEvent         // key: event_id
	wallets       map[string]*model.Wallet
	ledgers       map[string]*model.Ledger
	withdrawals   map[string]*model.Withdrawal
	messages      map[string][]*model.ChatMessage
	conversations map[string]*model.Conversation
	notifications []*model.Notification
	orgs          map[string]*model.Org
	orgMembers    []*model.OrgMember
	orgWallets    map[string]*model.OrgWallet
	approvals     map[string]*model.Approval
	apiKeys       map[string]*model.APIKey
	webhooks      map[string]*model.Webhook
	webhookDels   map[string]*model.WebhookDelivery
	compliance    *model.ComplianceState
	seq           int64
}

func NewMem() *MemStore {
	s := &MemStore{
		users:         map[string]*model.User{},
		profiles:      map[string]*model.ProviderProfile{},
		tasks:         map[string]*model.Task{},
		taskIndex:     map[string]map[string]bool{},
		matches:       map[string]*model.MatchRecord{},
		matchReasons:  map[string][]*model.MatchReason{},
		orders:        map[string]*model.Order{},
		deliveries:    map[string]*model.Delivery{},
		reviews:       map[string]*model.Review{},
		endorsements:  map[string]*model.Endorsement{},
		endorseAppend: map[string][]*model.EndorsementAppend{},
		creditEvents:  map[string]*model.CreditEvent{},
		wallets:       map[string]*model.Wallet{},
		ledgers:       map[string]*model.Ledger{},
		withdrawals:   map[string]*model.Withdrawal{},
		messages:      map[string][]*model.ChatMessage{},
		conversations: map[string]*model.Conversation{},
		notifications: []*model.Notification{},
		orgs:          map[string]*model.Org{},
		orgMembers:    []*model.OrgMember{},
		orgWallets:    map[string]*model.OrgWallet{},
		approvals:     map[string]*model.Approval{},
		apiKeys:       map[string]*model.APIKey{},
		webhooks:      map[string]*model.Webhook{},
		webhookDels:   map[string]*model.WebhookDelivery{},
		compliance:    model.NewComplianceState(),
	}
	s.seed()
	return s
}

// seed 预置接单方候选与示例需求方/运营, 便于演示主链路与匹配召回。
func (s *MemStore) seed() {
	now := time.Now().UnixMilli()
	providers := []model.User{
		{ID: "P001", Name: "算法老王", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"python", "nlp", "rag"}, CreditScore: 92, TaxBound: true},
		{ID: "P002", Name: "前端小鹿", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"frontend", "vue", "typescript"}, CreditScore: 88, TaxBound: true},
		{ID: "P003", Name: "数据阿杰", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"data", "python", "etl"}, CreditScore: 80, TaxBound: false},
		{ID: "P004", Name: "全栈Mia", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"fullstack", "go", "cloud"}, CreditScore: 95, TaxBound: true},
	}
	for i := range providers {
		providers[i].CreatedAt = now
		// 走 PutUser 而不是直接塞 map: 短号(公开页地址)的补齐逻辑只在那里,
		// 绕过去的人在大厅里没有短号, 公开档案页也就没有地址。
		s.PutUser(&providers[i])
	}
	// 三个需求方各带一个演示手机号。没有手机号的话, 演示时没人能"登录成需求方",
	// 认领就只能眼看着停在待确认——确认这一侧永远走不到。
	// 大厅演示也需要不止一个需求方: 同一张脸挂六个单, 看着像自导自演。
	for _, u := range []*model.User{
		{ID: "R001", Name: "需求方甲", Phone: "13800000001", Role: model.RoleRequester, KYCVerified: true, CreditScore: 85, CreatedAt: now},
		{ID: "R002", Name: "茶饮品牌杨姐", Phone: "13800000002", Role: model.RoleRequester, KYCVerified: true, CreditScore: 78, CreatedAt: now},
		{ID: "R003", Name: "跨境小团队", Phone: "13800000003", Role: model.RoleRequester, KYCVerified: true, CreditScore: 83, CreatedAt: now},
		{ID: "A001", Name: "运营-admin", Role: model.RoleAdmin, KYCVerified: true, CreatedAt: now},
	} {
		s.PutUser(u)
	}

	// P2-1: 为种子用户初始化钱包(演示资金)。R001 预置余额用于托管演示; 其余为 0。
	for uid := range s.users {
		w := &model.Wallet{UID: uid, BalanceCents: 0, FrozenCents: 0, UpdatedAt: now}
		if uid == "R001" {
			w.BalanceCents = 1000000 // ¥10000.00 演示资金
		}
		s.wallets[uid] = w
	}
}

// NextID 生成业务ID(前缀+时间戳+序列), 生产替换为雪花ID。
func (s *MemStore) NextID(prefix string) string {
	n := atomic.AddInt64(&s.seq, 1)
	return prefix + "-" + time.Now().Format("20060102150405") + "-" + itoa(n)
}

// --- Users ---
func (s *MemStore) GetUser(id string) (*model.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	return u, ok
}
func (s *MemStore) GetUserByAccount(account string) (*model.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Account == account {
			return u, true
		}
	}
	return nil, false
}
func (s *MemStore) PutUser(u *model.User) {
	if u.Slug == "" {
		u.Slug = fallbackSlug(u.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
}

// GetUserBySlug 按公开短号查用户。公开信用页 /p/{slug} 用, slug 为空时不匹配任何人。
func (s *MemStore) GetUserBySlug(slug string) (*model.User, bool) {
	if slug == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Slug == slug {
			return u, true
		}
	}
	return nil, false
}

// GetUserByPhone 按手机号查用户。验证码登录的第一跳: 有号即登录, 无号即注册。
func (s *MemStore) GetUserByPhone(phone string) (*model.User, bool) {
	if phone == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.Phone == phone {
			return u, true
		}
	}
	return nil, false
}

// GetProfile 返回档案副本而非指针。mysqlStore 每次查询都是新对象, 内存实现若返指针,
// 调用方改了返回值却没落库, 两套实现就会出现行为差异——一致性测试会盯这一条。
func (s *MemStore) GetProfile(userID string) (*model.ProviderProfile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[userID]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.Skills = append([]string(nil), p.Skills...)
	cp.Portfolio = append([]model.PortfolioItem(nil), p.Portfolio...)
	return &cp, true
}

func (s *MemStore) PutProfile(p *model.ProviderProfile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	cp.Skills = append([]string(nil), p.Skills...)
	cp.Portfolio = append([]model.PortfolioItem(nil), p.Portfolio...)
	s.profiles[p.UserID] = &cp
}
func (s *MemStore) ListProviders() []*model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.User
	for _, u := range s.users {
		if u.Role == model.RoleProvider {
			out = append(out, u)
		}
	}
	return out
}

// ListUsers 返回全部用户(含需求方/接单方/运营), 供运营看板聚合。
func (s *MemStore) ListUsers() []*model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	return out
}

// --- Tasks ---
func (s *MemStore) PutTask(t *model.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = t
	s.indexTask(t) // 维护倒排索引
}
func (s *MemStore) GetTask(id string) (*model.Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}
func (s *MemStore) ListOpenTasks() []*model.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Task
	for _, t := range s.tasks {
		if t.Status == model.TaskOpen {
			out = append(out, t)
		}
	}
	return out
}

// ClaimOpenTask 判断与写入在同一把写锁内完成, 这是内存实现能给出的等价原子性。
func (s *MemStore) ClaimOpenTask(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok || t.Status != model.TaskOpen {
		return false
	}
	t.Status = model.TaskMatched
	s.indexTask(t)
	return true
}

// WithdrawOpenTask 撤单的原子版: 归属与状态在同一把写锁内一起校验。
// 少了归属这一条, 任何人拿到单号都能把别人的单撤掉。
func (s *MemStore) WithdrawOpenTask(taskID, requesterID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok || t.Status != model.TaskOpen || t.RequesterID != requesterID {
		return false
	}
	t.Status = model.TaskWithdrawn
	s.indexTask(t)
	return true
}

// ListTasks 返回全部任务(不限状态), 供运营看板聚合。
func (s *MemStore) ListTasks() []*model.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out
}

// ListTasksByRequester 我发的需求, 按创建时间倒序。
// 排序带 id 兜底: 同一个人连着发两条, created_at 可能同毫秒,
// 只按时间排会让这两条在列表里来回跳。
func (s *MemStore) ListTasksByRequester(userID string) []*model.Task {
	s.mu.RLock()
	out := make([]*model.Task, 0, 8)
	for _, t := range s.tasks {
		if t.RequesterID == userID {
			out = append(out, t)
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// SearchTasks 需求大厅检索: 倒排索引收窄 + 分面过滤 + 排序 + 截断。
// 默认(Status 为空)仅返回 open; 调用方可显式传 Status 放宽。
// 关键词(Q/Skill)支持中英文: 拉丁词走倒排索引快速收窄, 中文走全文档子串匹配。
func (s *MemStore) SearchTasks(f TaskFilter) []*model.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 1) 倒排索引收窄候选(仅对含拉丁词的关键词生效)。
	var cand map[string]bool
	useIdx := false
	if tk := tokenize(f.Q); len(tk) > 0 {
		cand = s.indexLookup(tk)
		useIdx = true
	}
	if tk := tokenize(f.Skill); len(tk) > 0 {
		sk := s.indexLookup(tk)
		if useIdx {
			cand = intersect(cand, sk)
		} else {
			cand = sk
			useIdx = true
		}
	}
	// 3) 索引只存整词: 输 "py" 查不到 "python", 输 "用 P" 里那个单字母 p 也不在索引里。
	// 空结果一律回落到全量扫描 + 子串匹配——宁可慢一点, 不能让人搜到 0 条。
	if useIdx && len(cand) == 0 {
		useIdx = false
	}
	// 2) 遍历候选(或全量)应用分面过滤。
	var out []*model.Task
	for id, t := range s.tasks {
		if f.Status != "" {
			if t.Status != model.TaskStatus(f.Status) {
				continue
			}
		} else if t.Status != model.TaskOpen {
			continue
		}
		if useIdx && !cand[id] {
			continue
		}
		if f.Category != "" && t.Category != f.Category {
			continue
		}
		if f.MinBudget > 0 && t.BudgetCents < f.MinBudget {
			continue
		}
		if f.MaxBudget > 0 && t.BudgetCents > f.MaxBudget {
			continue
		}
		if f.DeadlineAfter > 0 && t.SpecObj.DeadlineAt != 0 && t.SpecObj.DeadlineAt < f.DeadlineAfter {
			continue
		}
		if f.DeadlineBefore > 0 && t.SpecObj.DeadlineAt != 0 && t.SpecObj.DeadlineAt > f.DeadlineBefore {
			continue
		}
		if f.Q != "" && !matchKeyword(t, f.Q) {
			continue
		}
		if f.Skill != "" && !matchKeyword(t, f.Skill) {
			continue
		}
		out = append(out, t)
	}
	// 排序带 id 兜底(与 mysql.go 的 ORDER BY 对齐): 种子需求的 created_at 相同,
	// 只比时间的话同分的几条顺序随机, 翻页会重复或漏项。
	switch f.Sort {
	case "budget_desc":
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].BudgetCents != out[j].BudgetCents {
				return out[i].BudgetCents > out[j].BudgetCents
			}
			return out[i].ID < out[j].ID
		})
	case "budget_asc":
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].BudgetCents != out[j].BudgetCents {
				return out[i].BudgetCents < out[j].BudgetCents
			}
			return out[i].ID < out[j].ID
		})
	default: // newest
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].CreatedAt != out[j].CreatedAt {
				return out[i].CreatedAt > out[j].CreatedAt
			}
			return out[i].ID < out[j].ID
		})
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// taskDoc 任务全文档(小写): 用于关键词检索与倒排索引。
func taskDoc(t *model.Task) string {
	return strings.ToLower(t.Category + " " + t.Title + " " + t.Spec + " " +
		t.SpecObj.Deliverables + " " + t.SpecObj.RequiredFormat + " " +
		t.SpecObj.StyleRef + " " + t.SpecObj.Samples)
}

// tokenize 提取拉丁/数字词(小写); 中文因无空格不做切分, 由子串匹配兜底。
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
}

// indexLookup 多词 AND(交集)查询倒排索引。
func (s *MemStore) indexLookup(tokens []string) map[string]bool {
	if len(tokens) == 0 {
		return nil
	}
	result := map[string]bool{}
	first := true
	for _, tk := range tokens {
		set, ok := s.taskIndex[tk]
		if !ok {
			return map[string]bool{} // 某词无命中 → 交集为空
		}
		if first {
			for id := range set {
				result[id] = true
			}
			first = false
		} else {
			for id := range result {
				if !set[id] {
					delete(result, id)
				}
			}
		}
	}
	return result
}

// intersect 两集合交集。
func intersect(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for id := range a {
		if b[id] {
			out[id] = true
		}
	}
	return out
}

// matchKeyword 关键词命中: 含拉丁词则要求全部拉丁词出现在文档; 纯中文则整串子串匹配。
func matchKeyword(t *model.Task, kw string) bool {
	toks := tokenize(kw)
	if len(toks) == 0 {
		return strings.Contains(taskDoc(t), strings.ToLower(kw))
	}
	doc := taskDoc(t)
	for _, tk := range toks {
		if !strings.Contains(doc, tk) {
			return false
		}
	}
	return true
}

// indexTask 将任务全文档的拉丁词写入倒排索引(幂等: 重复 Put 不破坏)。
func (s *MemStore) indexTask(t *model.Task) {
	for _, tk := range tokenize(taskDoc(t)) {
		if s.taskIndex[tk] == nil {
			s.taskIndex[tk] = map[string]bool{}
		}
		s.taskIndex[tk][t.ID] = true
	}
}

// --- Matches ---
func (s *MemStore) PutMatch(m *model.MatchRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matches[m.ID] = m
}
func (s *MemStore) GetMatch(id string) (*model.MatchRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.matches[id]
	return m, ok
}
func (s *MemStore) ListMatchesByTask(taskID string) []*model.MatchRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.MatchRecord
	for _, m := range s.matches {
		if m.TaskID == taskID {
			out = append(out, m)
		}
	}
	return out
}

// ReplaceMatchReasons 整体替换, 按 rank_no 排好返回。
// 副本写入: 调用方随后可能继续用自己那份切片, 直接存引用会让它和库里的"同一份"被改乱。
func (s *MemStore) ReplaceMatchReasons(matchID string, rs []*model.MatchReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*model.MatchReason, len(rs))
	for i, r := range rs {
		c := *r
		cp[i] = &c
	}
	s.matchReasons[matchID] = cp
}

func (s *MemStore) ListMatchReasons(matchID string) []*model.MatchReason {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.matchReasons[matchID]
	out := make([]*model.MatchReason, len(src))
	for i, r := range src {
		c := *r
		out[i] = &c
	}
	return out
}

// --- Orders ---
func (s *MemStore) PutOrder(o *model.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[o.ID] = o
}
func (s *MemStore) GetOrder(id string) (*model.Order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orders[id]
	return o, ok
}

// TransitionOrder 检查与改写在同一个写锁内完成。
func (s *MemStore) TransitionOrder(orderID string, from, to model.OrderStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok || o.Status != from {
		return false
	}
	o.Status = to
	return true
}

// CancelPendingOrder 订单与任务在同一把锁内一起改, 内存版的"事务"。
func (s *MemStore) CancelPendingOrder(orderID, taskID, by, reason string, at int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[orderID]
	if !ok || o.Status != model.OrderPendingEscrow {
		return false
	}
	o.Status = model.OrderCancelled
	o.CancelledBy = by
	o.CancelReason = reason
	o.CancelledAt = at
	if t, ok := s.tasks[taskID]; ok && t.Status == model.TaskMatched {
		t.Status = model.TaskOpen
		s.indexTask(t)
	}
	return true
}

// DeliverOrder 一把写锁内完成: 判状态 → 数轮次 → 落交付 → 推进订单。
// 内存实现里这把锁就是"事务"。
func (s *MemStore) DeliverOrder(orderID, content, link string, items []model.DeliveryItem,
	from []model.OrderStatus, to model.OrderStatus, maxRound int, at int64) (*model.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	o, ok := s.orders[orderID]
	if !ok {
		return nil, ErrOrderGone
	}
	okFrom := false
	for _, st := range from {
		if o.Status == st {
			okFrom = true
			break
		}
	}
	if !okFrom {
		return nil, ErrNotDeliverable
	}

	round := 0
	for _, d := range s.deliveries {
		if d.OrderID == orderID {
			round++
		}
	}
	round++
	if round > maxRound {
		return nil, ErrRevisionLimit
	}

	d := &model.Delivery{
		ID:        s.NextID("D"),
		OrderID:   orderID,
		Round:     round,
		Content:   content,
		Link:      link,
		Items:     items,
		Status:    "submitted",
		CreatedAt: at,
	}
	s.deliveries[d.ID] = d
	o.Status = to
	return d, nil
}

// ReviewOrder 内存实现: 一把写锁内完成"判状态 + 写交付结论 + 推订单状态"。
func (s *MemStore) ReviewOrder(orderID string, accept bool, note string, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	o, ok := s.orders[orderID]
	if !ok {
		return ErrOrderGone
	}
	if o.Status != model.OrderDelivered {
		return ErrNotReviewable
	}

	var last *model.Delivery
	rounds := 0
	for _, d := range s.deliveries {
		if d.OrderID != orderID {
			continue
		}
		rounds++
		if last == nil || d.Round > last.Round {
			last = d
		}
	}
	if last == nil {
		return ErrNoDelivery
	}

	if accept {
		last.Status = "accepted"
		last.ReviewedAt = at
		o.Status = model.OrderCompleted
		o.AcceptedAt = at
		if t, ok := s.tasks[o.TaskID]; ok {
			t.Status = model.TaskClosed
		}
		// 飞轮留痕: 完成单只记事件、delta 恒 0(完成不等于好评)。
		// 写在同一把写锁里, 保证"订单已完成"与"信用事件已落"不会只成立一半。
		s.appendCreditEventLocked(o.RequesterID, model.CreditOrderCompleted, o.ID, 0, at)
		s.appendCreditEventLocked(o.ProviderID, model.CreditOrderCompleted, o.ID, 0, at)
		return nil
	}

	limit := o.MaxRevisions
	if limit <= 0 {
		limit = model.DefaultMaxRevisions
	}
	if rounds >= limit {
		return ErrRevisionLimit
	}
	last.Status = "rejected"
	last.ReviewNote = note
	last.ReviewedAt = at
	o.Status = model.OrderDelivering
	return nil
}
func (s *MemStore) ListOrders() []*model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Order, 0, len(s.orders))
	for _, o := range s.orders {
		out = append(out, o)
	}
	return out
}
func (s *MemStore) ListOrdersByUser(userID string) []*model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Order, 0)
	for _, o := range s.orders {
		if o.RequesterID == userID || o.ProviderID == userID {
			out = append(out, o)
		}
	}
	return out
}

func (s *MemStore) ListOrdersByTask(taskID string) []*model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Order, 0)
	for _, o := range s.orders {
		if o.TaskID == taskID {
			out = append(out, o)
		}
	}
	return out
}

func (s *MemStore) ListOrdersByOrg(orgID string) []*model.Order {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Order, 0)
	for _, o := range s.orders {
		if o.OrgID == orgID {
			out = append(out, o)
		}
	}
	return out
}

func (s *MemStore) ListTasksByOwner(ownerID string) []*model.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Task, 0)
	for _, t := range s.tasks {
		if t.RequesterID == ownerID {
			out = append(out, t)
		}
	}
	return out
}

// --- Deliveries ---
func (s *MemStore) PutDelivery(d *model.Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries[d.ID] = d
}
func (s *MemStore) GetDelivery(id string) (*model.Delivery, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.deliveries[id]
	return d, ok
}
func (s *MemStore) ListDeliveriesByOrder(orderID string) []*model.Delivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Delivery
	for _, d := range s.deliveries {
		if d.OrderID == orderID {
			out = append(out, d)
		}
	}
	return out
}

// --- Reviews ---
func (s *MemStore) PutReview(r *model.Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews[r.ID] = r
}

// SubmitReview 内存实现: 一把写锁内完成"判重 + 落评价 + 揭盲 + 留痕"。
//
// 揭盲时机只有一种: 同一单里已经存在对方那条评价。此刻两条一起可见——
// 这是"同时揭盲"的全部实现, 没有第二个开关。
func (s *MemStore) SubmitReview(r *model.Review, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ex := range s.reviews {
		if ex.OrderID == r.OrderID && ex.ReviewerID == r.ReviewerID {
			return ErrReviewExists
		}
	}

	r.CreatedAt = at
	r.RevealedAt = 0
	s.reviews[r.ID] = r

	// 找对侧: 同订单、由另一个人写的评价。
	var other *model.Review
	for _, ex := range s.reviews {
		if ex.OrderID == r.OrderID && ex.ReviewerID != r.ReviewerID {
			other = ex
			break
		}
	}
	if other == nil {
		return nil // 只有我这一条, 保持未揭盲, 等对方
	}

	r.RevealedAt = at
	if other.RevealedAt == 0 {
		other.RevealedAt = at
	}
	// 留痕: 每人一条 review_received, ref 用各自收到的那条评价的 id。
	// delta 恒 0——分数由 AvgRating 派生, 事件只负责说明"这条评价真实存在"。
	s.appendCreditEventLocked(r.RevieweeID, model.CreditReviewReceived, r.ID, 0, at)
	s.appendCreditEventLocked(other.RevieweeID, model.CreditReviewReceived, other.ID, 0, at)
	return nil
}

// --- Endorsements (M4) ---

// PutEndorsement 内存实现：判重 + 定权重 + 落库在一把写锁里做完。
//
// 为什么权重不能由调用方算好再传进来：
// "同一对用户 12 个月内只计一次"要读历史。读和写分两处，两个并发请求
// 会同时读到"没有历史"，于是两条都记 100——两个人来回背几次就成"长期合作伙伴"了。
// MySQL 版用 SELECT ... FOR UPDATE 做同一件事。
func (s *MemStore) PutEndorsement(e *model.Endorsement) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ex := range s.endorsements {
		if ex.OrderID == e.OrderID && ex.AuthorID == e.AuthorID {
			return ErrEndorsementExists
		}
	}

	if e.Weight > 0 {
		since := e.CreatedAt - int64(model.EndorsementWeightWindowDays)*24*60*60*1000
		for _, ex := range s.endorsements {
			if ex.AuthorID == e.AuthorID && ex.TargetID == e.TargetID &&
				ex.Weight > 0 && ex.CreatedAt >= since {
				e.Weight = 0
				break
			}
		}
	}
	if e.Status == "" {
		e.Status = model.EndorseActive
	}
	if e.Source == "" {
		e.Source = model.EndorseLive
	}
	s.endorsements[e.ID] = e
	return nil
}

func (s *MemStore) GetEndorsement(id string) (*model.Endorsement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.endorsements[id]
	return e, ok
}

func (s *MemStore) GetEndorsementByOrderAuthor(orderID, authorID string) (*model.Endorsement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.endorsements {
		if e.OrderID == orderID && e.AuthorID == authorID {
			return e, true
		}
	}
	return nil, false
}

// ListEndorsementsByTarget 收到的、可见且计权重的背书，新的在前。
// 排序带 ID 兜底：同毫秒写入的两条（用例里常见）靠 map 遍历会给出随机顺序。
func (s *MemStore) ListEndorsementsByTarget(targetID string) []*model.Endorsement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Endorsement, 0)
	for _, e := range s.endorsements {
		if e.TargetID == targetID && model.Weighted(e) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *MemStore) ListEndorsementsByAuthor(authorID string) []*model.Endorsement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Endorsement, 0)
	for _, e := range s.endorsements {
		if e.AuthorID == authorID {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *MemStore) PutEndorsementAppend(a *model.EndorsementAppend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 只能给存在的背书追加。接口层已经挡过一次，这里再挡是防"删了还追加"。
	if _, ok := s.endorsements[a.EndorsementID]; !ok {
		return ErrEndorsementGone
	}
	s.endorseAppend[a.EndorsementID] = append(s.endorseAppend[a.EndorsementID], a)
	return nil
}

func (s *MemStore) ListEndorsementAppends(endorsementID string) []*model.EndorsementAppend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.endorseAppend[endorsementID]
	out := make([]*model.EndorsementAppend, len(src))
	copy(out, src)
	return out
}

func (s *MemStore) GetReview(id string) (*model.Review, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.reviews[id]
	return r, ok
}

// ListReviewsByOrder 一张订单上的全部评价。
// 排序必须有 id 兜底: 同一秒提交的两条 created_at 相同, 只按时间排每次顺序都可能不同。
func (s *MemStore) ListReviewsByOrder(orderID string) []*model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Review, 0)
	for _, r := range s.reviews {
		if r.OrderID == orderID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (s *MemStore) ListReviewsByReviewee(revieweeID string) []*model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Review, 0)
	for _, r := range s.reviews {
		if r.RevieweeID == revieweeID {
			out = append(out, r)
		}
	}
	return out
}

// ListRevealedReviewsByReviewee 只算已可见的评价。
// 未揭盲的那条对谁都不该产生影响——连算分也不该, 否则"先写的一方"
// 能靠猜对方给几分来调整自己的措辞。
func (s *MemStore) ListRevealedReviewsByReviewee(revieweeID string, now int64) []*model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Review, 0)
	for _, r := range s.reviews {
		if r.RevieweeID == revieweeID && model.ReviewVisible(r, now) {
			out = append(out, r)
		}
	}
	return out
}

// --- Credit Events ---

// appendCreditEventLocked 幂等追加一条信用事件。调用方必须已持有写锁。
//
// 幂等键与库里的 uk_user_kind_ref 保持一致: (user_id, kind, ref_id)。
// 重复写入直接跳过, 不做 ON DUPLICATE 覆盖——信用事件一旦发生就不该被改写。
func (s *MemStore) appendCreditEventLocked(userID, kind, refID string, delta int, at int64) {
	if userID == "" || refID == "" {
		return
	}
	for _, e := range s.creditEvents {
		if e.UserID == userID && e.Kind == kind && e.RefID == refID {
			return
		}
	}
	score := 0
	if u, ok := s.users[userID]; ok && u != nil {
		score = u.CreditScore + delta
	}
	ev := &model.CreditEvent{
		ID:         s.NextID("CE"),
		UserID:     userID,
		Kind:       kind,
		RefID:      refID,
		Delta:      delta,
		ScoreAfter: score,
		CreatedAt:  at,
	}
	s.creditEvents[ev.ID] = ev
}

func (s *MemStore) ListCreditEvents(userID string) []*model.CreditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.CreditEvent, 0)
	for _, e := range s.creditEvents {
		if e.UserID == userID {
			out = append(out, e)
		}
	}
	// 排序用 id 兜底: 同一秒内多条事件 created_at 相同, 只按时间排会来回抖。
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// --- Compliance ---
func (s *MemStore) GetCompliance() *model.ComplianceState { return s.compliance }
func (s *MemStore) SetCompliance(item model.ComplianceItem, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compliance.Items[item] = on
}

// --- Wallets / Ledger / Withdrawals (P2-1) ---

func (s *MemStore) GetWallet(uid string) (*model.Wallet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.wallets[uid]
	return w, ok
}

func (s *MemStore) GetOrCreateWallet(uid string) *model.Wallet {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.wallets[uid]; ok {
		return w
	}
	w := &model.Wallet{UID: uid, UpdatedAt: model.Now()}
	s.wallets[uid] = w
	return w
}

func (s *MemStore) PutWallet(w *model.Wallet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.UpdatedAt = model.Now()
	s.wallets[w.UID] = w
}

// ListWallets 返回全部钱包, 供运营看板聚合平台资金水位。
func (s *MemStore) ListWallets() []*model.Wallet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Wallet, 0, len(s.wallets))
	for _, w := range s.wallets {
		out = append(out, w)
	}
	return out
}

func (s *MemStore) ListLedgerByUser(uid string) []*model.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Ledger
	for _, l := range s.ledgers {
		if l.UID == uid {
			out = append(out, l)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *MemStore) PutLedger(l *model.Ledger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledgers[l.ID] = l
}

func (s *MemStore) ListWithdrawals() []*model.Withdrawal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Withdrawal, 0, len(s.withdrawals))
	for _, w := range s.withdrawals {
		out = append(out, w)
	}
	return out
}

func (s *MemStore) ListWithdrawalsByUser(uid string) []*model.Withdrawal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Withdrawal
	for _, w := range s.withdrawals {
		if w.UID == uid {
			out = append(out, w)
		}
	}
	return out
}

func (s *MemStore) GetWithdrawal(id string) (*model.Withdrawal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.withdrawals[id]
	return w, ok
}

func (s *MemStore) PutWithdrawal(w *model.Withdrawal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withdrawals[w.ID] = w
}

// itoa 轻量整数转字符串(避免依赖 strconv 以保持零依赖心智)。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---- IM 会话与消息 (P2-2) ----

// convID 会话ID(双方uid排序拼接, 与 im 包保持一致)。
func convID(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}

func (s *MemStore) SaveMessage(m *model.ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[m.ConvID] = append(s.messages[m.ConvID], m)
}

func (s *MemStore) ListMessages(convID string) []*model.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*model.ChatMessage{}, s.messages[convID]...)
}

func (s *MemStore) GetConversation(cid string) (*model.Conversation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.conversations[cid]
	return c, ok
}

func (s *MemStore) GetOrCreateConversation(a, b, orderID string) *model.Conversation {
	cid := convID(a, b)
	now := model.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.conversations[cid]; ok {
		return c
	}
	c := &model.Conversation{ID: cid, A: a, B: b, OrderID: orderID, UpdatedAt: now}
	s.conversations[cid] = c
	return c
}

func (s *MemStore) SaveConversation(c *model.Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[c.ID] = c
}

func (s *MemStore) ListConversations(uid string) []*model.Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []*model.Conversation
	for _, c := range s.conversations {
		if c.A == uid || c.B == uid {
			res = append(res, c)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].UpdatedAt > res[j].UpdatedAt
	})
	return res
}

// ---- 通知中心 (P2-3) ----

func (s *MemStore) SaveNotification(n *model.Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, n)
}

func (s *MemStore) GetNotification(id string) (*model.Notification, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.notifications {
		if n.ID == id {
			return n, true
		}
	}
	return nil, false
}

func (s *MemStore) ListNotifications(uid string, limit int) []*model.Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Notification
	for _, n := range s.notifications {
		if n.UID == uid {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *MemStore) MarkNotificationRead(uid, id string) (*model.Notification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.notifications {
		if n.ID == id && n.UID == uid {
			if n.Status != model.NotiRead {
				n.Status = model.NotiRead
				n.ReadAt = model.Now()
			}
			return n, true
		}
	}
	return nil, false
}

func (s *MemStore) CountUnread(uid string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := 0
	for _, n := range s.notifications {
		if n.UID == uid && n.Status == model.NotiUnread {
			c++
		}
	}
	return c
}

// ---- 企业版治理 (P3 最小骨架: 组织/成员/角色) ----

func (s *MemStore) SaveOrg(o *model.Org) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs[o.ID] = o
}

func (s *MemStore) GetOrg(id string) (*model.Org, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orgs[id]
	return o, ok
}

func (s *MemStore) ListOrgsByOwner(ownerUID string) []*model.Org {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Org
	for _, o := range s.orgs {
		if o.OwnerID == ownerUID {
			out = append(out, o)
		}
	}
	return out
}

func (s *MemStore) SaveOrgMember(m *model.OrgMember) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同 (org_id, user_id) 覆盖更新角色。
	for _, em := range s.orgMembers {
		if em.OrgID == m.OrgID && em.UserID == m.UserID {
			em.Role = m.Role
			em.JoinedAt = m.JoinedAt
			return
		}
	}
	s.orgMembers = append(s.orgMembers, m)
}

func (s *MemStore) ListOrgMembers(orgID string) []*model.OrgMember {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.OrgMember
	for _, m := range s.orgMembers {
		if m.OrgID == orgID {
			out = append(out, m)
		}
	}
	return out
}

func (s *MemStore) GetOrgMember(orgID, uid string) (*model.OrgMember, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.orgMembers {
		if m.OrgID == orgID && m.UserID == uid {
			return m, true
		}
	}
	return nil, false
}

// ---- P3 专项: 组织钱包 / 审批流 / 开放 API + Webhook (MemStore) ----

func (s *MemStore) GetOrgWallet(orgID string) (*model.OrgWallet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.orgWallets[orgID]
	return w, ok
}

func (s *MemStore) GetOrCreateOrgWallet(orgID string) *model.OrgWallet {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.orgWallets[orgID]; ok {
		return w
	}
	w := &model.OrgWallet{OrgID: orgID, UpdatedAt: model.Now()}
	s.orgWallets[orgID] = w
	return w
}

func (s *MemStore) PutOrgWallet(w *model.OrgWallet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.UpdatedAt = model.Now()
	s.orgWallets[w.OrgID] = w
}

func (s *MemStore) SaveApproval(a *model.Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals[a.ID] = a
}

func (s *MemStore) GetApproval(id string) (*model.Approval, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.approvals[id]
	return a, ok
}

func (s *MemStore) ListApprovalsByOrg(orgID string) []*model.Approval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Approval
	for _, a := range s.approvals {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	return out
}

func (s *MemStore) SaveAPIKey(k *model.APIKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiKeys[k.ID] = k
}

func (s *MemStore) GetAPIKeyByHash(hash string) (*model.APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.apiKeys {
		if k.KeyHash == hash {
			return k, true
		}
	}
	return nil, false
}

func (s *MemStore) ListAPIKeysByOrg(orgID string) []*model.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.APIKey
	for _, k := range s.apiKeys {
		if k.OrgID == orgID {
			out = append(out, k)
		}
	}
	return out
}

func (s *MemStore) SaveWebhook(w *model.Webhook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhooks[w.ID] = w
}

func (s *MemStore) ListWebhooksByOrg(orgID string) []*model.Webhook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.Webhook
	for _, w := range s.webhooks {
		if w.OrgID == orgID {
			out = append(out, w)
		}
	}
	return out
}

func (s *MemStore) SaveWebhookDelivery(d *model.WebhookDelivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhookDels[d.ID] = d
}

func (s *MemStore) ListWebhookDeliveriesByOrg(orgID string) []*model.WebhookDelivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*model.WebhookDelivery
	for _, d := range s.webhookDels {
		if d.OrgID == orgID {
			out = append(out, d)
		}
	}
	return out
}

// 编译期接口断言: 任一实现缺方法都会在此编译失败, 保证 Store 契约齐备。
var (
	_ Store = (*MemStore)(nil)
	_ Store = (*mysqlStore)(nil)
)
