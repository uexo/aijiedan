// Package store 提供存储抽象与内存实现(零依赖, M0 默认与测试用)。
// 另见 mysql.go: 真实 MySQL 实现遵循同一 Store 接口, 上层 service/handler 不感知。
package store

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zanbuddy/internal/model"
)

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
	PutUser(u *model.User)
	ListUsers() []*model.User
	ListProviders() []*model.User
	PutTask(t *model.Task)
	GetTask(id string) (*model.Task, bool)
	ListOpenTasks() []*model.Task
	ListTasks() []*model.Task
	SearchTasks(f TaskFilter) []*model.Task
	PutMatch(m *model.MatchRecord)
	GetMatch(id string) (*model.MatchRecord, bool)
	ListMatchesByTask(taskID string) []*model.MatchRecord
	PutOrder(o *model.Order)
	GetOrder(id string) (*model.Order, bool)
	ListOrders() []*model.Order
	ListOrdersByUser(userID string) []*model.Order
	ListOrdersByTask(taskID string) []*model.Order
	ListTasksByOwner(ownerID string) []*model.Task
	PutDelivery(d *model.Delivery)
	GetDelivery(id string) (*model.Delivery, bool)
	ListDeliveriesByOrder(orderID string) []*model.Delivery
	PutReview(r *model.Review)
	GetReview(id string) (*model.Review, bool)
	ListReviewsByOrder(orderID string) []*model.Review
	ListReviewsByReviewee(revieweeID string) []*model.Review
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
	mu         sync.RWMutex
	users      map[string]*model.User
	tasks      map[string]*model.Task
	taskIndex  map[string]map[string]bool // 倒排索引: 拉丁词 -> 任务ID集合(加速关键词检索)
	matches    map[string]*model.MatchRecord
	orders     map[string]*model.Order
	deliveries map[string]*model.Delivery
	reviews    map[string]*model.Review
	wallets    map[string]*model.Wallet
	ledgers    map[string]*model.Ledger
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
		users:      map[string]*model.User{},
		tasks:      map[string]*model.Task{},
		taskIndex:  map[string]map[string]bool{},
		matches:    map[string]*model.MatchRecord{},
		orders:     map[string]*model.Order{},
		deliveries: map[string]*model.Delivery{},
		reviews:    map[string]*model.Review{},
		wallets:    map[string]*model.Wallet{},
		ledgers:    map[string]*model.Ledger{},
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
		s.users[providers[i].ID] = &providers[i]
	}
	s.users["R001"] = &model.User{ID: "R001", Name: "需求方甲", Role: model.RoleRequester, KYCVerified: true, CreatedAt: now}
	s.users["A001"] = &model.User{ID: "A001", Name: "运营-admin", Role: model.RoleAdmin, KYCVerified: true, CreatedAt: now}

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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
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
	switch f.Sort {
	case "budget_desc":
		sort.Slice(out, func(i, j int) bool { return out[i].BudgetCents > out[j].BudgetCents })
	case "budget_asc":
		sort.Slice(out, func(i, j int) bool { return out[i].BudgetCents < out[j].BudgetCents })
	default: // newest
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
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
func (s *MemStore) GetReview(id string) (*model.Review, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.reviews[id]
	return r, ok
}
func (s *MemStore) ListReviewsByOrder(orderID string) []*model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Review, 0)
	for _, r := range s.reviews {
		if r.OrderID == orderID {
			out = append(out, r)
		}
	}
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
