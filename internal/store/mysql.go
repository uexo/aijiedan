// Package store - 真实 MySQL 实现(遵循 Store 接口)。
// 由 NewMySQL(dsn) 构建, DSN 从环境变量 ZANBUDDY_DB_DSN 读取(见 main.go)。
// 依赖 github.com/go-sql-driver/mysql; 仅 MySQL 模式才需要外部依赖, 内存模式仍零依赖。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"zanbuddy/internal/model"
)

type mysqlStore struct {
	db  *sql.DB
	seq int64
}

// NewMySQL 连接 MySQL 并初始化(建库后的表由 migrations/schema.sql 创建, 此处仅 seed 种子数据)。
func NewMySQL(dsn string) (*mysqlStore, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("mysql ping(请确认 192.168.3.18:3306 可达、账号密码正确、库已建): %w", err)
	}
	s := &mysqlStore{db: db}
	if err := s.Seed(); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return s, nil
}

// Seed 预置演示用户与合规默认状态(幂等, INSERT IGNORE 重复执行无害)。
func (s *mysqlStore) Seed() error {
	now := time.Now().UnixMilli()
	providers := []model.User{
		{ID: "P001", Name: "算法老王", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"python", "nlp", "rag"}, CreditScore: 92, TaxBound: true},
		{ID: "P002", Name: "前端小鹿", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"frontend", "vue", "typescript"}, CreditScore: 88, TaxBound: true},
		{ID: "P003", Name: "数据阿杰", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"data", "python", "etl"}, CreditScore: 80, TaxBound: false},
		{ID: "P004", Name: "全栈Mia", Role: model.RoleProvider, KYCVerified: true, Skills: []string{"fullstack", "go", "cloud"}, CreditScore: 95, TaxBound: true},
	}
	for i := range providers {
		providers[i].CreatedAt = now
		s.PutUser(&providers[i])
	}
	for _, u := range []model.User{
		// 手机号是演示用的需求方入口。没有它, 演示时没人能登录成发布方,
		// 认领就永远停在"待确认"——确认这一侧走不到。
		{ID: "R001", Name: "需求方甲", Phone: "13800000001", Role: model.RoleRequester, KYCVerified: true, CreditScore: 85, CreatedAt: now},
		{ID: "R002", Name: "茶饮品牌杨姐", Phone: "13800000002", Role: model.RoleRequester, KYCVerified: true, CreditScore: 78, CreatedAt: now},
		{ID: "R003", Name: "跨境小团队", Phone: "13800000003", Role: model.RoleRequester, KYCVerified: true, CreditScore: 83, CreatedAt: now},
		{ID: "A001", Name: "运营-admin", Role: model.RoleAdmin, KYCVerified: true, CreatedAt: now},
	} {
		s.PutUser(&u)
	}
	defaults := []struct {
		item string
		on   bool
	}{{"kyc", true}, {"icp", false}, {"tax_source", false}, {"escrow", false}}
	for _, d := range defaults {
		if _, err := s.db.Exec("INSERT IGNORE INTO compliance_state (`item`, `on`, note) VALUES (?,?,?)", d.item, boolToInt(d.on), ""); err != nil {
			return err
		}
	}
	// P2-1: 种子钱包(演示资金)。R001 预置 ¥10000 用于托管演示; 其余 0。
	seedWallets := map[string]int64{
		"R001": 1000000,
		"P001": 0, "P002": 0, "P003": 0, "P004": 0,
		"A001": 0,
	}
	for uid, bal := range seedWallets {
		if _, err := s.db.Exec("INSERT IGNORE INTO wallet (uid,balance_cents,frozen_cents,updated_at) VALUES (?,?,?,?)", uid, bal, 0, now); err != nil {
			return err
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

// nullStr 空字符串按 NULL 写入, 避免污染索引列。
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (s *mysqlStore) NextID(prefix string) string {
	n := atomic.AddInt64(&s.seq, 1)
	return prefix + "-" + time.Now().Format("20060102150405") + "-" + itoa(n)
}

// userCols user 表的查询列清单。
// 抽成常量的原因: 原先 4 处 SELECT 各写一遍完整列表, 加一列要改四处, 漏一处就是
// "少扫一个字段"的静默错(不报错, 只是值永远为空)。以后加列只改这里。
const userCols = "id,account,password_hash,real_name,phone,name,role,kyc_verified,skills,credit_score,tax_bound,ai_auto_reply,slug,onboarded,created_at"

// --- Users ---
func (s *mysqlStore) GetUser(id string) (*model.User, bool) {
	u := &model.User{}
	var skills []byte
	var kyc, tax, aiReply, onboarded int
	var account, phash, realName, phone sql.NullString
	err := s.db.QueryRow("SELECT "+userCols+" FROM `user` WHERE id=? AND deleted_at IS NULL", id).
		Scan(&u.ID, &account, &phash, &realName, &phone, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	u.Account = account.String
	u.PasswordHash = phash.String
	u.RealName = realName.String
	u.Phone = phone.String
	u.KYCVerified = kyc == 1
	u.TaxBound = tax == 1
	u.AIAutoReply = aiReply == 1
	u.Onboarded = onboarded == 1
	if len(skills) > 0 {
		_ = json.Unmarshal(skills, &u.Skills)
	}
	return u, true
}

func (s *mysqlStore) GetUserByAccount(account string) (*model.User, bool) {
	u := &model.User{}
	var skills []byte
	var kyc, tax, aiReply, onboarded int
	var phash, realName, phone sql.NullString
	err := s.db.QueryRow("SELECT "+userCols+" FROM `user` WHERE account=? AND deleted_at IS NULL", account).
		Scan(&u.ID, &u.Account, &phash, &realName, &phone, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	u.PasswordHash = phash.String
	u.RealName = realName.String
	u.Phone = phone.String
	u.KYCVerified = kyc == 1
	u.TaxBound = tax == 1
	u.AIAutoReply = aiReply == 1
	u.Onboarded = onboarded == 1
	if len(skills) > 0 {
		_ = json.Unmarshal(skills, &u.Skills)
	}
	return u, true
}

// PutUser 写用户。列清单必须与 userCols 对齐:
// 曾经只给 SELECT 加了新列却漏了这里的 INSERT/UPDATE, 结果 slug 和 onboarded 读出来永远是空,
// 表现为"档案明明存了但公开页一直 404"。加列时两处一起改。
func (s *mysqlStore) PutUser(u *model.User) {
	if u.Slug == "" {
		u.Slug = fallbackSlug(u.ID) // 唯一索引兜底, 见 store.go 里的说明
	}
	jb, _ := json.Marshal(u.Skills)
	_, _ = s.db.Exec("INSERT INTO `user` (id,account,password_hash,real_name,phone,name,role,kyc_verified,skills,credit_score,tax_bound,ai_auto_reply,slug,onboarded,created_at)"+
		" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"+
		" ON DUPLICATE KEY UPDATE account=VALUES(account),password_hash=VALUES(password_hash),real_name=VALUES(real_name),"+
		"phone=VALUES(phone),name=VALUES(name),role=VALUES(role),kyc_verified=VALUES(kyc_verified),skills=VALUES(skills),"+
		"credit_score=VALUES(credit_score),tax_bound=VALUES(tax_bound),ai_auto_reply=VALUES(ai_auto_reply),"+
		"slug=VALUES(slug),onboarded=VALUES(onboarded)",
		u.ID, nullStr(u.Account), nullStr(u.PasswordHash), nullStr(u.RealName), nullStr(u.Phone), u.Name, u.Role,
		boolToInt(u.KYCVerified), jb, u.CreditScore, boolToInt(u.TaxBound), boolToInt(u.AIAutoReply),
		u.Slug, boolToInt(u.Onboarded), u.CreatedAt)
}

// GetUserBySlug 按公开短号查用户。短号在 002 迁移里已回填为 id 小写并建唯一索引。
func (s *mysqlStore) GetUserBySlug(slug string) (*model.User, bool) {
	if slug == "" {
		return nil, false
	}
	u := &model.User{}
	var skills []byte
	var kyc, tax, aiReply, onboarded int
	var account, phash, realName, phone sql.NullString
	err := s.db.QueryRow("SELECT "+userCols+" FROM `user` WHERE slug=? AND deleted_at IS NULL", slug).
		Scan(&u.ID, &account, &phash, &realName, &phone, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt)
	if err != nil {
		return nil, false
	}
	u.Account = account.String
	u.PasswordHash = phash.String
	u.RealName = realName.String
	u.Phone = phone.String
	u.KYCVerified = kyc == 1
	u.TaxBound = tax == 1
	u.AIAutoReply = aiReply == 1
	u.Onboarded = onboarded == 1
	if len(skills) > 0 {
		_ = json.Unmarshal(skills, &u.Skills)
	}
	return u, true
}

// GetUserByPhone 按手机号查用户。user.phone 没有唯一索引(历史原因), 所以多个命中时取第一条,
// 由应用层在注册时保证不重复。
func (s *mysqlStore) GetUserByPhone(phone string) (*model.User, bool) {
	if phone == "" {
		return nil, false
	}
	u := &model.User{}
	var skills []byte
	var kyc, tax, aiReply, onboarded int
	var account, phash, realName, phoneCol sql.NullString
	err := s.db.QueryRow("SELECT "+userCols+" FROM `user` WHERE phone=? AND deleted_at IS NULL LIMIT 1", phone).
		Scan(&u.ID, &account, &phash, &realName, &phoneCol, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt)
	if err != nil {
		return nil, false
	}
	u.Account = account.String
	u.PasswordHash = phash.String
	u.RealName = realName.String
	u.Phone = phoneCol.String
	u.KYCVerified = kyc == 1
	u.TaxBound = tax == 1
	u.AIAutoReply = aiReply == 1
	u.Onboarded = onboarded == 1
	if len(skills) > 0 {
		_ = json.Unmarshal(skills, &u.Skills)
	}
	return u, true
}

// GetProfile 读供给方档案。JSON 列在建表时就是 NOT NULL 带默认值, 所以扫进 []byte 不会遇到 NULL。
func (s *mysqlStore) GetProfile(userID string) (*model.ProviderProfile, bool) {
	p := &model.ProviderProfile{}
	var skills, portfolio []byte
	err := s.db.QueryRow("SELECT user_id,headline,bio,skills,portfolio,availability,completeness,updated_at FROM provider_profile WHERE user_id=?", userID).
		Scan(&p.UserID, &p.Headline, &p.Bio, &skills, &portfolio, &p.Availability, &p.Completeness, &p.UpdatedAt)
	if err != nil {
		return nil, false
	}
	if len(skills) > 0 {
		_ = json.Unmarshal(skills, &p.Skills)
	}
	if len(portfolio) > 0 {
		_ = json.Unmarshal(portfolio, &p.Portfolio)
	}
	return p, true
}

func (s *mysqlStore) PutProfile(p *model.ProviderProfile) {
	sk, _ := json.Marshal(p.Skills)
	pf, _ := json.Marshal(p.Portfolio)
	_, _ = s.db.Exec(`INSERT INTO provider_profile (user_id,headline,bio,skills,portfolio,availability,completeness,updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE headline=VALUES(headline),bio=VALUES(bio),skills=VALUES(skills),
		portfolio=VALUES(portfolio),availability=VALUES(availability),completeness=VALUES(completeness),updated_at=VALUES(updated_at)`,
		p.UserID, p.Headline, p.Bio, sk, pf, p.Availability, p.Completeness, p.UpdatedAt)
}

func (s *mysqlStore) ListProviders() []*model.User {
	rows, err := s.db.Query("SELECT "+userCols+" FROM `user` WHERE role=? AND deleted_at IS NULL", model.RoleProvider)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u := &model.User{}
		var skills []byte
		var kyc, tax, aiReply, onboarded int
		var account, phash, realName, phone sql.NullString
		if err := rows.Scan(&u.ID, &account, &phash, &realName, &phone, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt); err != nil {
			return nil
		}
		u.Account = account.String
		u.PasswordHash = phash.String
		u.RealName = realName.String
		u.Phone = phone.String
		u.KYCVerified = kyc == 1
		u.TaxBound = tax == 1
		u.AIAutoReply = aiReply == 1
		u.Onboarded = onboarded == 1
		if len(skills) > 0 {
			_ = json.Unmarshal(skills, &u.Skills)
		}
		out = append(out, u)
	}
	return out
}

// ListUsers 返回全部用户(含需求方/接单方/运营), 供运营看板聚合。
func (s *mysqlStore) ListUsers() []*model.User {
	rows, err := s.db.Query("SELECT " + userCols + " FROM `user` WHERE deleted_at IS NULL")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u := &model.User{}
		var skills []byte
		var kyc, tax, aiReply, onboarded int
		var account, phash, realName, phone sql.NullString
		if err := rows.Scan(&u.ID, &account, &phash, &realName, &phone, &u.Name, &u.Role, &kyc, &skills, &u.CreditScore, &tax, &aiReply, &u.Slug, &onboarded, &u.CreatedAt); err != nil {
			return nil
		}
		u.Account = account.String
		u.PasswordHash = phash.String
		u.RealName = realName.String
		u.Phone = phone.String
		u.KYCVerified = kyc == 1
		u.TaxBound = tax == 1
		u.AIAutoReply = aiReply == 1
		u.Onboarded = onboarded == 1
		if len(skills) > 0 {
			_ = json.Unmarshal(skills, &u.Skills)
		}
		out = append(out, u)
	}
	return out
}

// --- Tasks ---
func (s *mysqlStore) PutTask(t *model.Task) {
	clar, _ := json.Marshal(t.Clarifications)
	items, _ := json.Marshal(t.SpecObj.Items)
	analysis := nullStr(t.AnalysisJSON)
	_, _ = s.db.Exec(`INSERT INTO task (id,requester_id,org_id,title,category,budget_cents,spec,deliverables,required_format,deadline_at,style_ref,samples,clarifications,items_json,translation,translation_mode,analysis_json,analysis_mode,status,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE requester_id=VALUES(requester_id),title=VALUES(title),category=VALUES(category),budget_cents=VALUES(budget_cents),spec=VALUES(spec),deliverables=VALUES(deliverables),required_format=VALUES(required_format),deadline_at=VALUES(deadline_at),style_ref=VALUES(style_ref),samples=VALUES(samples),clarifications=VALUES(clarifications),items_json=VALUES(items_json),translation=VALUES(translation),translation_mode=VALUES(translation_mode),analysis_json=VALUES(analysis_json),analysis_mode=VALUES(analysis_mode),status=VALUES(status),org_id=VALUES(org_id)`,
		t.ID, t.RequesterID, t.OrgID, t.Title, t.Category, t.BudgetCents, t.Spec,
		t.SpecObj.Deliverables, t.SpecObj.RequiredFormat, t.SpecObj.DeadlineAt, t.SpecObj.StyleRef, t.SpecObj.Samples,
		clar, nullStr(string(items)), nullStr(t.Translation), t.TranslationMode, analysis, t.AnalysisMode, t.Status, t.CreatedAt)
}

// ClaimOpenTask 用一条带条件的 UPDATE 完成"判断 + 写入"。
// 并发时 MySQL 行锁保证只有一个请求的 RowsAffected 是 1, 另一个自然拿到 0。
// 不能用 SELECT ... FOR UPDATE 包一段事务: 多一次往返, 而且要在事务里再写订单, 耦合更重。
func (s *mysqlStore) ClaimOpenTask(taskID string) bool {
	res, err := s.db.Exec("UPDATE task SET status=? WHERE id=? AND status=? AND deleted_at IS NULL",
		string(model.TaskMatched), taskID, string(model.TaskOpen))
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false
	}
	return n == 1
}

// WithdrawOpenTask 条件 UPDATE 一次搞定"是不是他的单 + 此刻还开不开放"。
// 把 requester_id 写进 WHERE 而不只是校验一次, 是为了让越权与"手慢了"在
// RowsAffected 上都表现为 0, 由上层再读一次库给出准确措辞; 写库本身绝不越权。
func (s *mysqlStore) WithdrawOpenTask(taskID, requesterID string) bool {
	res, err := s.db.Exec("UPDATE task SET status=? WHERE id=? AND requester_id=? AND status=? AND deleted_at IS NULL",
		string(model.TaskWithdrawn), taskID, requesterID, string(model.TaskOpen))
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false
	}
	return n == 1
}

func (s *mysqlStore) GetTask(id string) (*model.Task, bool) {
	t := &model.Task{}
	var clar, items, analysis []byte
	var translation, translationMode, analysisMode sql.NullString
	err := s.db.QueryRow("SELECT "+taskCols+" FROM task WHERE id=? AND deleted_at IS NULL", id).
		Scan(&t.ID, &t.RequesterID, &t.OrgID, &t.Title, &t.Category, &t.BudgetCents, &t.Spec,
			&t.SpecObj.Deliverables, &t.SpecObj.RequiredFormat, &t.SpecObj.DeadlineAt, &t.SpecObj.StyleRef, &t.SpecObj.Samples,
			&clar, &items, &translation, &translationMode, &analysis, &analysisMode, &t.Status, &t.CreatedAt)
	if err != nil {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	if len(clar) > 0 {
		_ = json.Unmarshal(clar, &t.Clarifications)
	}
	if len(items) > 0 {
		_ = json.Unmarshal(items, &t.SpecObj.Items)
	}
	t.Translation = translation.String
	t.TranslationMode = translationMode.String
	if len(analysis) > 0 {
		t.AnalysisJSON = string(analysis)
	}
	t.AnalysisMode = analysisMode.String
	return t, true
}
func (s *mysqlStore) ListOpenTasks() []*model.Task {
	rows, err := s.db.Query("SELECT "+taskCols+" FROM task WHERE status=? AND deleted_at IS NULL", model.TaskOpen)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Task
	for rows.Next() {
		t := &model.Task{}
		var clar, items, analysis []byte
		var translation, translationMode, analysisMode sql.NullString
		if err := rows.Scan(&t.ID, &t.RequesterID, &t.OrgID, &t.Title, &t.Category, &t.BudgetCents, &t.Spec,
			&t.SpecObj.Deliverables, &t.SpecObj.RequiredFormat, &t.SpecObj.DeadlineAt, &t.SpecObj.StyleRef, &t.SpecObj.Samples,
			&clar, &items, &translation, &translationMode, &analysis, &analysisMode, &t.Status, &t.CreatedAt); err != nil {
			return nil
		}
		if len(clar) > 0 {
			_ = json.Unmarshal(clar, &t.Clarifications)
		}
		if len(items) > 0 {
			_ = json.Unmarshal(items, &t.SpecObj.Items)
		}
		t.Translation = translation.String
		t.TranslationMode = translationMode.String
		if len(analysis) > 0 {
			t.AnalysisJSON = string(analysis)
		}
		t.AnalysisMode = analysisMode.String
		out = append(out, t)
	}
	return out
}

// ListTasks 返回全部任务(不限状态), 供运营看板聚合。
func (s *mysqlStore) ListTasks() []*model.Task {
	rows, err := s.db.Query("SELECT " + taskCols + " FROM task WHERE deleted_at IS NULL")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Task
	for rows.Next() {
		t := &model.Task{}
		var clar, items, analysis []byte
		var translation, translationMode, analysisMode sql.NullString
		if err := rows.Scan(&t.ID, &t.RequesterID, &t.OrgID, &t.Title, &t.Category, &t.BudgetCents, &t.Spec,
			&t.SpecObj.Deliverables, &t.SpecObj.RequiredFormat, &t.SpecObj.DeadlineAt, &t.SpecObj.StyleRef, &t.SpecObj.Samples,
			&clar, &items, &translation, &translationMode, &analysis, &analysisMode, &t.Status, &t.CreatedAt); err != nil {
			return nil
		}
		if len(clar) > 0 {
			_ = json.Unmarshal(clar, &t.Clarifications)
		}
		if len(items) > 0 {
			_ = json.Unmarshal(items, &t.SpecObj.Items)
		}
		t.Translation = translation.String
		t.TranslationMode = translationMode.String
		if len(analysis) > 0 {
			t.AnalysisJSON = string(analysis)
		}
		t.AnalysisMode = analysisMode.String
		out = append(out, t)
	}
	return out
}

// ListTasksByRequester 我发的需求, 按创建时间倒序。
// ORDER BY 带 id 兜底, 与内存实现保持一致: 同一毫秒发的两条不能来回跳。
func (s *mysqlStore) ListTasksByRequester(userID string) []*model.Task {
	rows, err := s.db.Query("SELECT "+taskCols+" FROM task WHERE requester_id=? AND deleted_at IS NULL ORDER BY created_at DESC, id DESC", userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]*model.Task, 0, 8)
	for rows.Next() {
		t := &model.Task{}
		if err := scanTask(t, rows); err != nil {
			return nil
		}
		out = append(out, t)
	}
	return out
}

// SearchTasks MySQL 实现: 参数化动态 WHERE + 白名单排序。
func (s *mysqlStore) SearchTasks(f TaskFilter) []*model.Task {
	const cols = taskCols
	var where []string
	var args []interface{}
	where = append(where, "deleted_at IS NULL")
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	} else {
		where = append(where, "status=?")
		args = append(args, model.TaskOpen)
	}
	if f.Category != "" {
		where = append(where, "category=?")
		args = append(args, f.Category)
	}
	if f.MinBudget > 0 {
		where = append(where, "budget_cents>=?")
		args = append(args, f.MinBudget)
	}
	if f.MaxBudget > 0 {
		where = append(where, "budget_cents<=?")
		args = append(args, f.MaxBudget)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, "(title LIKE ? OR spec LIKE ? OR deliverables LIKE ? OR required_format LIKE ? OR style_ref LIKE ? OR samples LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like, like, like)
	}
	if sk := strings.TrimSpace(f.Skill); sk != "" {
		where = append(where, "(title LIKE ? OR spec LIKE ? OR deliverables LIKE ? OR required_format LIKE ? OR style_ref LIKE ? OR samples LIKE ?)")
		like := "%" + sk + "%"
		args = append(args, like, like, like, like, like, like)
	}
	if f.DeadlineAfter > 0 {
		where = append(where, "deadline_at >= ?")
		args = append(args, f.DeadlineAfter)
	}
	if f.DeadlineBefore > 0 {
		where = append(where, "deadline_at <= ?")
		args = append(args, f.DeadlineBefore)
	}
	// 排序必须带 id 兜底: 种子需求的 created_at 是同一个值,
	// 只按时间排会让同分的几条在翻页时互相换位, 第二页又出现第一页的单。
	order := "created_at DESC,id ASC"
	switch f.Sort {
	case "budget_desc":
		order = "budget_cents DESC,id ASC"
	case "budget_asc":
		order = "budget_cents ASC,id ASC"
	}
	qry := "SELECT " + cols + " FROM task WHERE " + strings.Join(where, " AND ") + " ORDER BY " + order
	if f.Limit > 0 {
		qry += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := s.db.Query(qry, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Task
	for rows.Next() {
		t := &model.Task{}
		var clar, items, analysis []byte
		var translation, translationMode, analysisMode sql.NullString
		if err := rows.Scan(&t.ID, &t.RequesterID, &t.OrgID, &t.Title, &t.Category, &t.BudgetCents, &t.Spec,
			&t.SpecObj.Deliverables, &t.SpecObj.RequiredFormat, &t.SpecObj.DeadlineAt, &t.SpecObj.StyleRef, &t.SpecObj.Samples,
			&clar, &items, &translation, &translationMode, &analysis, &analysisMode, &t.Status, &t.CreatedAt); err != nil {
			return nil
		}
		if len(clar) > 0 {
			_ = json.Unmarshal(clar, &t.Clarifications)
		}
		if len(items) > 0 {
			_ = json.Unmarshal(items, &t.SpecObj.Items)
		}
		t.Translation = translation.String
		t.TranslationMode = translationMode.String
		if len(analysis) > 0 {
			t.AnalysisJSON = string(analysis)
		}
		t.AnalysisMode = analysisMode.String
		out = append(out, t)
	}
	return out
}

// --- Matches ---
func (s *mysqlStore) PutMatch(m *model.MatchRecord) {
	_, _ = s.db.Exec(`INSERT INTO match_record (id,task_id,provider_id,score,reason,status,created_at)
		VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE score=VALUES(score),reason=VALUES(reason),status=VALUES(status)`,
		m.ID, m.TaskID, m.ProviderID, m.Score, m.Reason, m.Status, m.CreatedAt)
}
func (s *mysqlStore) GetMatch(id string) (*model.MatchRecord, bool) {
	m := &model.MatchRecord{}
	err := s.db.QueryRow("SELECT id,task_id,provider_id,score,reason,status,created_at FROM match_record WHERE id=?", id).
		Scan(&m.ID, &m.TaskID, &m.ProviderID, &m.Score, &m.Reason, &m.Status, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return m, true
}
func (s *mysqlStore) ListMatchesByTask(taskID string) []*model.MatchRecord {
	rows, err := s.db.Query("SELECT id,task_id,provider_id,score,reason,status,created_at FROM match_record WHERE task_id=?", taskID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.MatchRecord
	for rows.Next() {
		m := &model.MatchRecord{}
		if err := rows.Scan(&m.ID, &m.TaskID, &m.ProviderID, &m.Score, &m.Reason, &m.Status, &m.CreatedAt); err != nil {
			return nil
		}
		out = append(out, m)
	}
	return out
}

// ReplaceMatchReasons 在一个事务里删旧插新。
//
// 不在 Go 侧先查再拼: 两个人同时刷新推荐页时, 查-删-插之间会互相插队,
// 最后 match_id 下留着两版理由混在一起, rank_no 还会重复。
// DELETE + INSERT 放在同一个事务里, 外面只可能看到替换前或替换后。
func (s *mysqlStore) ReplaceMatchReasons(matchID string, rs []*model.MatchReason) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM match_reason WHERE match_id=?", matchID); err != nil {
		return
	}
	for _, r := range rs {
		if _, err := tx.Exec(`INSERT INTO match_reason
			(id,match_id,rank_no,text,anchor_type,anchor_ref,anchor_label,factor,factor_score,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.MatchID, r.RankNo, r.Text, r.AnchorType, r.AnchorRef,
			r.AnchorLabel, r.Factor, r.FactorScore, r.CreatedAt); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

func (s *mysqlStore) ListMatchReasons(matchID string) []*model.MatchReason {
	rows, err := s.db.Query(`SELECT id,match_id,rank_no,text,anchor_type,anchor_ref,anchor_label,factor,factor_score,created_at
		FROM match_reason WHERE match_id=? ORDER BY rank_no`, matchID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.MatchReason
	for rows.Next() {
		r := &model.MatchReason{}
		if err := rows.Scan(&r.ID, &r.MatchID, &r.RankNo, &r.Text, &r.AnchorType, &r.AnchorRef,
			&r.AnchorLabel, &r.Factor, &r.FactorScore, &r.CreatedAt); err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}

// --- Orders ---
func (s *mysqlStore) PutOrder(o *model.Order) {
	mil, _ := json.Marshal(o.Milestones)
	acc, _ := json.Marshal(o.Acceptance)
	_, _ = s.db.Exec(`INSERT INTO `+"`order`"+` (id,task_id,requester_id,org_id,provider_id,match_id,amount_cents,status,escrow_id,settled,max_revisions,milestones,settlement_mode,refund_cents,refund_pct,cancelled_by,cancel_reason,cancelled_at,dispute_reason,disputed_by,disputed_at,resolved_by,resolved_at,resolved_note,acceptance_json,severe_mismatch,accepted_at,created_at,provider_quote_cents)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE task_id=VALUES(task_id),requester_id=VALUES(requester_id),org_id=VALUES(org_id),provider_id=VALUES(provider_id),match_id=VALUES(match_id),amount_cents=VALUES(amount_cents),status=VALUES(status),escrow_id=VALUES(escrow_id),settled=VALUES(settled),max_revisions=VALUES(max_revisions),milestones=VALUES(milestones),settlement_mode=VALUES(settlement_mode),refund_cents=VALUES(refund_cents),refund_pct=VALUES(refund_pct),cancelled_by=VALUES(cancelled_by),cancel_reason=VALUES(cancel_reason),cancelled_at=VALUES(cancelled_at),dispute_reason=VALUES(dispute_reason),disputed_by=VALUES(disputed_by),disputed_at=VALUES(disputed_at),resolved_by=VALUES(resolved_by),resolved_at=VALUES(resolved_at),resolved_note=VALUES(resolved_note),acceptance_json=VALUES(acceptance_json),severe_mismatch=VALUES(severe_mismatch),accepted_at=VALUES(accepted_at),provider_quote_cents=VALUES(provider_quote_cents)`,
		o.ID, o.TaskID, o.RequesterID, o.OrgID, o.ProviderID, o.MatchID, o.AmountCents, o.Status, o.EscrowID, boolToInt(o.Settled),
		o.MaxRevisions, nullStr(string(mil)), o.SettlementMode, o.RefundCents, o.RefundPct,
		nullStr(o.CancelledBy), nullStr(o.CancelReason), o.CancelledAt, nullStr(o.DisputeReason), nullStr(o.DisputedBy), o.DisputedAt,
		nullStr(o.ResolvedBy), o.ResolvedAt, nullStr(o.ResolvedNote), nullStr(string(acc)), boolToInt(o.SevereMismatch), o.AcceptedAt, o.CreatedAt, o.ProviderQuoteCents)
}

// orderCols SELECT 列(顺序须与 scanOrder 一致)
const orderCols = "id,task_id,requester_id,org_id,provider_id,match_id,amount_cents,status,escrow_id,settled,max_revisions,milestones,settlement_mode,refund_cents,refund_pct,cancelled_by,cancel_reason,cancelled_at,dispute_reason,disputed_by,disputed_at,resolved_by,resolved_at,resolved_note,acceptance_json,severe_mismatch,accepted_at,created_at,provider_quote_cents"

// scanOrder 把一行扫描进 *model.Order(列顺序见 orderCols)。row 满足 sql.Row/sql.Rows 的 Scan 接口。
func scanOrder(o *model.Order, row interface{ Scan(...interface{}) error }) error {
	var escrow, mode, cancelBy, cancelReason, disputeReason, disputedBy, resolvedBy, resolvedNote, acceptance sql.NullString
	var settled, maxRev, refundPct, severeMismatch int
	var refundCents, cancelledAt, disputedAt, resolvedAt, acceptedAt int64
	var mil []byte
	if err := row.Scan(&o.ID, &o.TaskID, &o.RequesterID, &o.OrgID, &o.ProviderID, &o.MatchID, &o.AmountCents, &o.Status, &escrow, &settled, &maxRev, &mil, &mode, &refundCents, &refundPct, &cancelBy, &cancelReason, &cancelledAt, &disputeReason, &disputedBy, &disputedAt, &resolvedBy, &resolvedAt, &resolvedNote, &acceptance, &severeMismatch, &acceptedAt, &o.CreatedAt, &o.ProviderQuoteCents); err != nil {
		return err
	}
	o.EscrowID = escrow.String
	o.Settled = settled == 1
	o.MaxRevisions = maxRev
	o.RefundCents = refundCents
	o.RefundPct = refundPct
	o.CancelledAt = cancelledAt
	o.DisputedAt = disputedAt
	o.ResolvedAt = resolvedAt
	o.AcceptedAt = acceptedAt
	o.SevereMismatch = severeMismatch == 1
	if len(acceptance.String) > 0 {
		_ = json.Unmarshal([]byte(acceptance.String), &o.Acceptance)
	}
	o.SettlementMode = mode.String
	o.CancelledBy = cancelBy.String
	o.CancelReason = cancelReason.String
	o.DisputeReason = disputeReason.String
	o.DisputedBy = disputedBy.String
	o.ResolvedBy = resolvedBy.String
	o.ResolvedNote = resolvedNote.String
	if len(mil) > 0 {
		_ = json.Unmarshal(mil, &o.Milestones)
	}
	return nil
}

// taskCols SELECT 列(顺序须与 scanTask 一致)
const taskCols = "id,requester_id,org_id,title,category,budget_cents,spec,deliverables,required_format,deadline_at,style_ref,samples,clarifications,items_json,translation,translation_mode,analysis_json,analysis_mode,status,created_at"

// scanTask 把一行扫描进 *model.Task(列顺序见 taskCols)。row 满足 sql.Row/sql.Rows 的 Scan 接口。
func scanTask(t *model.Task, row interface{ Scan(...interface{}) error }) error {
	var clar, items, analysis []byte
	var translation, translationMode, analysisMode sql.NullString
	if err := row.Scan(&t.ID, &t.RequesterID, &t.OrgID, &t.Title, &t.Category, &t.BudgetCents, &t.Spec,
		&t.SpecObj.Deliverables, &t.SpecObj.RequiredFormat, &t.SpecObj.DeadlineAt, &t.SpecObj.StyleRef, &t.SpecObj.Samples,
		&clar, &items, &translation, &translationMode, &analysis, &analysisMode, &t.Status, &t.CreatedAt); err != nil {
		return err
	}
	if len(clar) > 0 {
		_ = json.Unmarshal(clar, &t.Clarifications)
	}
	if len(items) > 0 {
		_ = json.Unmarshal(items, &t.SpecObj.Items)
	}
	t.Translation = translation.String
	t.TranslationMode = translationMode.String
	if len(analysis) > 0 {
		t.AnalysisJSON = string(analysis)
	}
	t.AnalysisMode = analysisMode.String
	return nil
}

func (s *mysqlStore) GetOrder(id string) (*model.Order, bool) {
	o := &model.Order{}
	row := s.db.QueryRow("SELECT "+orderCols+" FROM `order` WHERE id=?", id)
	if err := scanOrder(o, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, false
		}
		return nil, false
	}
	return o, true
}

// TransitionOrder 用带条件的 UPDATE 完成"判断 + 写入", 同类操作见 ClaimOpenTask。
func (s *mysqlStore) TransitionOrder(orderID string, from, to model.OrderStatus) bool {
	res, err := s.db.Exec("UPDATE `order` SET status=? WHERE id=? AND status=?",
		string(to), orderID, string(from))
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	return err == nil && n == 1
}

// CancelPendingOrder 订单与任务在一个事务里改。任一语句失败整体回滚,
// 不留"订单已取消但单子还占着"或"单子退回但订单还挂着"的中间态。
func (s *mysqlStore) CancelPendingOrder(orderID, taskID, by, reason string, at int64) bool {
	tx, err := s.db.Begin()
	if err != nil {
		return false
	}
	res, err := tx.Exec("UPDATE `order` SET status=?,cancelled_by=?,cancel_reason=?,cancelled_at=? WHERE id=? AND status=?",
		string(model.OrderCancelled), by, reason, at, orderID, string(model.OrderPendingEscrow))
	if err != nil {
		_ = tx.Rollback()
		return false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		_ = tx.Rollback()
		return false
	}
	if _, err := tx.Exec("UPDATE task SET status=? WHERE id=? AND status=?",
		string(model.TaskOpen), taskID, string(model.TaskMatched)); err != nil {
		_ = tx.Rollback()
		return false
	}
	return tx.Commit() == nil
}

// DeliverOrder 交付记录与订单状态在同一个事务里落库。
//
// 先用 SELECT ... FOR UPDATE 锁住订单行: 两个并发的"提交交付"在库上排队,
// 后到的那个读到的是 delivered, 直接按"已交付"拒掉, 不会各数一次轮次后都 +1。
func (s *mysqlStore) DeliverOrder(orderID, content, link string, items []model.DeliveryItem,
	from []model.OrderStatus, to model.OrderStatus, maxRound int, at int64) (*model.Delivery, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	// 提交后这次 Rollback 是空操作, 但出错路径必须回滚, 所以不在成功分支里返回。
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRow("SELECT status FROM `order` WHERE id=? FOR UPDATE", orderID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrOrderGone
		}
		return nil, err
	}
	okFrom := false
	for _, st := range from {
		if model.OrderStatus(status) == st {
			okFrom = true
			break
		}
	}
	if !okFrom {
		return nil, ErrNotDeliverable
	}

	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM delivery WHERE order_id=?", orderID).Scan(&n); err != nil {
		return nil, err
	}
	round := n + 1
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
	itemsJSON, _ := json.Marshal(items)
	if _, err := tx.Exec("INSERT INTO delivery (id,order_id,round,content,link,items_json,status,created_at) VALUES (?,?,?,?,?,?,?,?)",
		d.ID, d.OrderID, d.Round, d.Content, nullStr(d.Link), nullStr(string(itemsJSON)), d.Status, d.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("UPDATE `order` SET status=? WHERE id=?", string(to), orderID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}

// ReviewOrder 验收/打回。事务内锁订单行, 再写最后一轮交付的结论。
//
// 先锁订单再读交付: 与 DeliverOrder 保持同一加锁顺序, 两边不会互相等死。
func (s *mysqlStore) ReviewOrder(orderID string, accept bool, note string, at int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status, taskID, requesterID, providerID string
	var maxRev int
	if err := tx.QueryRow("SELECT status,task_id,max_revisions,requester_id,provider_id FROM `order` WHERE id=? FOR UPDATE", orderID).
		Scan(&status, &taskID, &maxRev, &requesterID, &providerID); err != nil {
		if err == sql.ErrNoRows {
			return ErrOrderGone
		}
		return err
	}
	if model.OrderStatus(status) != model.OrderDelivered {
		return ErrNotReviewable
	}

	var lastID string
	var lastRound int
	if err := tx.QueryRow("SELECT id,round FROM delivery WHERE order_id=? ORDER BY round DESC LIMIT 1", orderID).
		Scan(&lastID, &lastRound); err != nil {
		if err == sql.ErrNoRows {
			return ErrNoDelivery
		}
		return err
	}

	if accept {
		if _, err := tx.Exec("UPDATE delivery SET status='accepted',reviewed_at=? WHERE id=?", at, lastID); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE `order` SET status=?,accepted_at=? WHERE id=?",
			string(model.OrderCompleted), at, orderID); err != nil {
			return err
		}
		// 需求单随之关闭: 大厅只看 open, 但"这条需求到底做完了没"要靠这个状态回答。
		if taskID != "" {
			if _, err := tx.Exec("UPDATE task SET status=? WHERE id=?", string(model.TaskClosed), taskID); err != nil {
				return err
			}
		}
		// 飞轮留痕: 完成单只记事件、delta 恒 0(完成不等于好评, 分要靠 review_received 才动)。
		// 与订单状态同事务: 不会出现"订单已完成但信用事件没落"的半截状态。
		if err := s.appendCreditEventTx(tx, requesterID, model.CreditOrderCompleted, orderID, 0, at); err != nil {
			return err
		}
		if err := s.appendCreditEventTx(tx, providerID, model.CreditOrderCompleted, orderID, 0, at); err != nil {
			return err
		}
		return tx.Commit()
	}

	limit := maxRev
	if limit <= 0 {
		limit = model.DefaultMaxRevisions
	}
	if lastRound >= limit {
		return ErrRevisionLimit
	}
	if _, err := tx.Exec("UPDATE delivery SET status='rejected',review_note=?,reviewed_at=? WHERE id=?",
		note, at, lastID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE `order` SET status=? WHERE id=?", string(model.OrderDelivering), orderID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) ListOrders() []*model.Order {
	rows, err := s.db.Query("SELECT " + orderCols + " FROM `order`")
	if err != nil {
		return []*model.Order{}
	}
	defer rows.Close()
	out := make([]*model.Order, 0)
	for rows.Next() {
		o := &model.Order{}
		if err := scanOrder(o, rows); err != nil {
			return []*model.Order{}
		}
		out = append(out, o)
	}
	return out
}

func (s *mysqlStore) ListOrdersByUser(userID string) []*model.Order {
	rows, err := s.db.Query("SELECT "+orderCols+" FROM `order` WHERE (requester_id=? OR provider_id=?)", userID, userID)
	if err != nil {
		return []*model.Order{}
	}
	defer rows.Close()
	out := make([]*model.Order, 0)
	for rows.Next() {
		o := &model.Order{}
		if err := scanOrder(o, rows); err != nil {
			return []*model.Order{}
		}
		out = append(out, o)
	}
	return out
}

func (s *mysqlStore) ListOrdersByOrg(orgID string) []*model.Order {
	rows, err := s.db.Query("SELECT "+orderCols+" FROM `order` WHERE org_id=?", orgID)
	if err != nil {
		return []*model.Order{}
	}
	defer rows.Close()
	out := make([]*model.Order, 0)
	for rows.Next() {
		o := &model.Order{}
		if err := scanOrder(o, rows); err != nil {
			return []*model.Order{}
		}
		out = append(out, o)
	}
	return out
}

func (s *mysqlStore) ListOrdersByTask(taskID string) []*model.Order {
	rows, err := s.db.Query("SELECT "+orderCols+" FROM `order` WHERE task_id=?", taskID)
	if err != nil {
		return []*model.Order{}
	}
	defer rows.Close()
	out := make([]*model.Order, 0)
	for rows.Next() {
		o := &model.Order{}
		if err := scanOrder(o, rows); err != nil {
			return []*model.Order{}
		}
		out = append(out, o)
	}
	return out
}

func (s *mysqlStore) ListTasksByOwner(ownerID string) []*model.Task {
	rows, err := s.db.Query("SELECT "+taskCols+" FROM task WHERE requester_id=?", ownerID)
	if err != nil {
		return []*model.Task{}
	}
	defer rows.Close()
	out := make([]*model.Task, 0)
	for rows.Next() {
		t := &model.Task{}
		if err := scanTask(t, rows); err != nil {
			return []*model.Task{}
		}
		out = append(out, t)
	}
	return out
}

// --- Deliveries ---
func (s *mysqlStore) PutDelivery(d *model.Delivery) {
	items, _ := json.Marshal(d.Items)
	_, _ = s.db.Exec(`INSERT INTO delivery (id,order_id,round,content,link,items_json,status,review_note,reviewed_at,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE round=VALUES(round),content=VALUES(content),link=VALUES(link),items_json=VALUES(items_json),status=VALUES(status),review_note=VALUES(review_note),reviewed_at=VALUES(reviewed_at)`,
		d.ID, d.OrderID, d.Round, d.Content, nullStr(d.Link), nullStr(string(items)), d.Status, nullStr(d.ReviewNote), d.ReviewedAt, d.CreatedAt)
}
func (s *mysqlStore) GetDelivery(id string) (*model.Delivery, bool) {
	d := &model.Delivery{}
	var items []byte
	var link, reviewNote sql.NullString
	err := s.db.QueryRow("SELECT id,order_id,round,content,link,items_json,status,review_note,reviewed_at,created_at FROM delivery WHERE id=?", id).
		Scan(&d.ID, &d.OrderID, &d.Round, &d.Content, &link, &items, &d.Status, &reviewNote, &d.ReviewedAt, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	d.Link = link.String
	d.ReviewNote = reviewNote.String
	if len(items) > 0 {
		_ = json.Unmarshal(items, &d.Items)
	}
	return d, true
}
func (s *mysqlStore) ListDeliveriesByOrder(orderID string) []*model.Delivery {
	rows, err := s.db.Query("SELECT id,order_id,round,content,link,items_json,status,review_note,reviewed_at,created_at FROM delivery WHERE order_id=? ORDER BY round", orderID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Delivery
	for rows.Next() {
		d := &model.Delivery{}
		var items []byte
		var link, reviewNote sql.NullString
		if err := rows.Scan(&d.ID, &d.OrderID, &d.Round, &d.Content, &link, &items, &d.Status, &reviewNote, &d.ReviewedAt, &d.CreatedAt); err != nil {
			return nil
		}
		d.Link = link.String
		d.ReviewNote = reviewNote.String
		if len(items) > 0 {
			_ = json.Unmarshal(items, &d.Items)
		}
		out = append(out, d)
	}
	return out
}

// --- Endorsements (M4) ---
//
// 为什么不写 credit_event：背书自己的表已经是一等资产，谁给谁写的、什么时候写的、
// 原文是什么，都能直接查。credit_event 回答的是"他这个分是从哪条来的"，
// 而背书的 weight（100/0）目前不参与任何分数计算——历史因子吃的是 AvgRating。
// 现在加一个 delta 恒 0 的第三种 kind，等于多一处要维护、要体检、要写进契约的东西，
// 换不到任何可解释性。等背书真的开始影响分数那天再加，那时它才有事件可讲。
const endorCols = "id,order_id,task_id,author_id,target_id,relation,body,answers,char_count,status,weight,source,created_at"

// PutEndorsement MySQL 实现：一个事务里 判重 → 定权重 → 落库。
//
// 定权重必须 SELECT ... FOR UPDATE：两个并发请求同时读到"没有历史"，
// 就会各自记 100 分，"12 个月只计一次"直接失效。
func (s *mysqlStore) PutEndorsement(e *model.Endorsement) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 先查一次，给用户一句"你写过了"而不是让唯一键报 500。唯一键仍是最终兜底。
	var dup string
	switch err := tx.QueryRow("SELECT id FROM endorsement WHERE order_id=? AND author_id=?",
		e.OrderID, e.AuthorID).Scan(&dup); err {
	case nil:
		return ErrEndorsementExists
	case sql.ErrNoRows:
	default:
		return err
	}

	if e.Weight > 0 {
		since := e.CreatedAt - int64(model.EndorsementWeightWindowDays)*24*60*60*1000
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM endorsement
			WHERE author_id=? AND target_id=? AND weight>0 AND created_at>=? FOR UPDATE`,
			e.AuthorID, e.TargetID, since).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			e.Weight = 0
		}
	}

	if e.Status == "" {
		e.Status = model.EndorseActive
	}
	if e.Source == "" {
		e.Source = model.EndorseLive
	}
	// answers 是 JSON 列：map 必须自己序列化。存字符串进去会被 MySQL 当 JSON 字符串，
	// 读出来反序列化就变成 "{}" 的字符串而不是对象。
	answers, err := json.Marshal(e.Answers)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO endorsement
		(id,order_id,task_id,author_id,target_id,relation,body,answers,char_count,status,weight,source,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.OrderID, e.TaskID, e.AuthorID, e.TargetID, e.Relation, e.Body,
		string(answers), e.CharCount, e.Status, e.Weight, e.Source, e.CreatedAt); err != nil {
		var me *mysqldriver.MySQLError
		if errors.As(err, &me) && me.Number == 1062 {
			return ErrEndorsementExists
		}
		return err
	}
	return tx.Commit()
}

func (s *mysqlStore) GetEndorsement(id string) (*model.Endorsement, bool) {
	e, err := scanEndorsement(s.db.QueryRow("SELECT "+endorCols+" FROM endorsement WHERE id=?", id))
	if err != nil {
		return nil, false
	}
	return e, true
}

func (s *mysqlStore) GetEndorsementByOrderAuthor(orderID, authorID string) (*model.Endorsement, bool) {
	e, err := scanEndorsement(s.db.QueryRow("SELECT "+endorCols+
		" FROM endorsement WHERE order_id=? AND author_id=?", orderID, authorID))
	if err != nil {
		return nil, false
	}
	return e, true
}

func (s *mysqlStore) ListEndorsementsByTarget(targetID string) []*model.Endorsement {
	return s.queryEndorsements("SELECT "+endorCols+` FROM endorsement
		WHERE target_id=? AND status IN ('active','flagged') AND weight>0
		ORDER BY created_at DESC, id DESC`, targetID)
}

func (s *mysqlStore) ListEndorsementsByAuthor(authorID string) []*model.Endorsement {
	return s.queryEndorsements("SELECT "+endorCols+` FROM endorsement
		WHERE author_id=? AND status<>'void' ORDER BY created_at DESC, id DESC`, authorID)
}

func (s *mysqlStore) queryEndorsements(q string, args ...interface{}) []*model.Endorsement {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	out := make([]*model.Endorsement, 0)
	for rows.Next() {
		e, err := scanEndorsement(rows)
		if err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

// scanEndorsement 统一的背书行读取。
//
// answers 必须过 []byte：JSON 列一旦扫进 string 会报类型不匹配，
// 症状是"背书一条都查不到"——而不是它真的没有记录。
func scanEndorsement(sc interface{ Scan(...interface{}) error }) (*model.Endorsement, error) {
	e := &model.Endorsement{}
	var answers []byte
	if err := sc.Scan(&e.ID, &e.OrderID, &e.TaskID, &e.AuthorID, &e.TargetID,
		&e.Relation, &e.Body, &answers, &e.CharCount, &e.Status, &e.Weight, &e.Source, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Answers = map[string]string{}
	if len(answers) > 0 {
		_ = json.Unmarshal(answers, &e.Answers)
	}
	return e, nil
}

func (s *mysqlStore) PutEndorsementAppend(a *model.EndorsementAppend) error {
	// 背书不存在时不要静默插入：追加挂在一个不存在的 ID 上，页面上永远不会出现。
	var one int
	switch err := s.db.QueryRow("SELECT 1 FROM endorsement WHERE id=?", a.EndorsementID).Scan(&one); err {
	case nil:
	case sql.ErrNoRows:
		return ErrEndorsementGone
	default:
		return err
	}
	_, err := s.db.Exec(`INSERT INTO endorsement_append (id,endorsement_id,author_id,body,created_at)
		VALUES (?,?,?,?,?)`, a.ID, a.EndorsementID, a.AuthorID, a.Body, a.CreatedAt)
	return err
}

func (s *mysqlStore) ListEndorsementAppends(endorsementID string) []*model.EndorsementAppend {
	rows, err := s.db.Query(`SELECT id,endorsement_id,author_id,body,created_at
		FROM endorsement_append WHERE endorsement_id=? ORDER BY created_at ASC, id ASC`, endorsementID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	out := make([]*model.EndorsementAppend, 0)
	for rows.Next() {
		a := &model.EndorsementAppend{}
		if err := rows.Scan(&a.ID, &a.EndorsementID, &a.AuthorID, &a.Body, &a.CreatedAt); err != nil {
			return nil
		}
		out = append(out, a)
	}
	return out
}

// --- 邀请链接命中 ---

func (s *mysqlStore) PutInviteHit(h *model.InviteHit) {
	if h == nil || h.TaskID == "" {
		return
	}
	if h.ID == "" {
		h.ID = "IH-" + time.Now().Format("20060102150405") + "-" + itoa(atomic.AddInt64(&s.seq, 1))
	}
	if h.OpenedAt == 0 {
		h.OpenedAt = time.Now().UnixMilli()
	}
	// 写失败不报错也不拦页面: 记不上这一笔, 损失的是指标, 不是用户的动作。
	// 为了一个打点把「点开链接的人」挡在门外, 是把统计放在了使用前面。
	_, _ = s.db.Exec("INSERT INTO invite_hit (id, task_id, opened_at) VALUES (?,?,?)",
		h.ID, h.TaskID, h.OpenedAt)
}

func (s *mysqlStore) ListInviteHits(taskIDs []string) []*model.InviteHit {
	if len(taskIDs) == 0 {
		return nil
	}
	// 占位符按实际数量拼: IN (?,?,?) 的长度必须和参数个数一致,
	// 少一个就是语法错误, 多一个会静默按 NULL 比较、查不到任何行。
	ph := make([]string, len(taskIDs))
	args := make([]interface{}, len(taskIDs))
	for i, id := range taskIDs {
		ph[i] = "?"
		args[i] = id
	}
	q := "SELECT id, task_id, opened_at FROM invite_hit WHERE task_id IN (" +
		strings.Join(ph, ",") + ") ORDER BY opened_at ASC, id ASC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]*model.InviteHit, 0, len(taskIDs))
	for rows.Next() {
		var h model.InviteHit
		if err := rows.Scan(&h.ID, &h.TaskID, &h.OpenedAt); err != nil {
			return nil
		}
		out = append(out, &h)
	}
	return out
}

// --- Reviews ---
func (s *mysqlStore) PutReview(r *model.Review) {
	_, _ = s.db.Exec(`INSERT INTO review (id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE rating=VALUES(rating),comment=VALUES(comment)`,
		r.ID, r.OrderID, r.TaskID, r.ReviewerID, r.RevieweeID, string(r.ReviewerRole), r.Rating, r.Comment, r.CreatedAt)
}

// SubmitReview MySQL 实现: 一个事务里 判重 → 落库 → 找对侧 → 同时揭盲 → 留痕。
//
// 并发下的顺序问题: 两个人几乎同时提交时, 后到的那一方会看到对方那条并触发揭盲;
// 先到的那一方提交完就退出, 由后到的补上。所以不会出现"两个人都提交了但都没揭盲"。
// 找对侧时加 FOR UPDATE, 保证同一单的揭盲只有一个事务在做。
func (s *mysqlStore) SubmitReview(r *model.Review, at int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 先查一次, 给用户一句"你评过了"而不是让唯一键报 500。唯一键仍是最终兜底。
	var dup string
	switch err := tx.QueryRow("SELECT id FROM review WHERE order_id=? AND reviewer_id=?",
		r.OrderID, r.ReviewerID).Scan(&dup); err {
	case nil:
		return ErrReviewExists
	case sql.ErrNoRows:
		// 正常路径
	default:
		return err
	}

	r.CreatedAt = at
	r.RevealedAt = 0
	if _, err := tx.Exec(`INSERT INTO review (id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at,revealed_at)
		VALUES (?,?,?,?,?,?,?,?,?,0)`,
		r.ID, r.OrderID, r.TaskID, r.ReviewerID, r.RevieweeID, string(r.ReviewerRole), r.Rating, r.Comment, at); err != nil {
		return mapReviewInsertErr(err)
	}

	var otherID, otherReviewee string
	switch err := tx.QueryRow("SELECT id,reviewee_id FROM review WHERE order_id=? AND reviewer_id<>? FOR UPDATE",
		r.OrderID, r.ReviewerID).Scan(&otherID, &otherReviewee); err {
	case sql.ErrNoRows:
		return tx.Commit() // 只有我这一条, 保持未揭盲, 等对方
	case nil:
		// 继续
	default:
		return err
	}

	if _, err := tx.Exec("UPDATE review SET revealed_at=? WHERE order_id=? AND revealed_at=0", at, r.OrderID); err != nil {
		return err
	}
	// 内存里的这条也要同步标记揭盲: handler 紧接着要用这个对象回响应,
	// 只改库不改对象, 用户提交完看到的还是"未揭盲"。
	r.RevealedAt = at
	// 留痕: 每人一条 review_received, ref 用各自收到的那条评价 id。
	// delta 恒 0——分数由 AvgRating 派生, 事件只负责说明"这条评价真实存在"。
	if err := s.appendCreditEventTx(tx, r.RevieweeID, model.CreditReviewReceived, r.ID, 0, at); err != nil {
		return err
	}
	if err := s.appendCreditEventTx(tx, otherReviewee, model.CreditReviewReceived, otherID, 0, at); err != nil {
		return err
	}
	return tx.Commit()
}

// mapReviewInsertErr 把并发下撞唯一键的 1062 翻成 ErrReviewExists。
func mapReviewInsertErr(err error) error {
	var me *mysqldriver.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return ErrReviewExists
	}
	return err
}

// scanReview 统一的评价行读取。
//
// comment 列可空(VARCHAR NULL): 直接扫进 string 会在"评价不写文字"时
// 报 converting NULL to string, 表现为查不到任何评价。必须过 NullString。
func scanReview(sc interface{ Scan(...interface{}) error }, withReveal bool) (*model.Review, error) {
	r := &model.Review{}
	var comment sql.NullString
	var revealed sql.NullInt64
	var err error
	if withReveal {
		err = sc.Scan(&r.ID, &r.OrderID, &r.TaskID, &r.ReviewerID, &r.RevieweeID,
			&r.ReviewerRole, &r.Rating, &comment, &r.CreatedAt, &revealed)
	} else {
		err = sc.Scan(&r.ID, &r.OrderID, &r.TaskID, &r.ReviewerID, &r.RevieweeID,
			&r.ReviewerRole, &r.Rating, &comment, &r.CreatedAt)
	}
	if err != nil {
		return nil, err
	}
	r.Comment = comment.String
	if revealed.Valid {
		r.RevealedAt = revealed.Int64
	}
	return r, nil
}

// reviewCols 统一的列清单。
//
// revealed_at 必须在这里出现: 它缺过一次, 症状是"双方都提交了但永远显示未揭盲"——
// 查询按 9 列读, RevealedAt 就恒为 0, 而 update 语句本身是对的, 排查时看着毫无破绽。
const reviewCols = "id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at,revealed_at"

func (s *mysqlStore) GetReview(id string) (*model.Review, bool) {
	r, err := scanReview(s.db.QueryRow("SELECT "+reviewCols+" FROM review WHERE id=?", id), true)
	if err != nil {
		return nil, false
	}
	return r, true
}

// ListReviewsByOrder 一张订单上的全部评价, 含未揭盲的(调用方自己用 model.ReviewVisible 判)。
// 排序带 id 兜底: 同一秒提交的两条 created_at 相同, 只按时间排顺序会抖。
func (s *mysqlStore) ListReviewsByOrder(orderID string) []*model.Review {
	rows, err := s.db.Query("SELECT "+reviewCols+" FROM review WHERE order_id=? ORDER BY created_at,id", orderID)
	if err != nil {
		return []*model.Review{}
	}
	defer rows.Close()
	out := make([]*model.Review, 0)
	for rows.Next() {
		r, err := scanReview(rows, true)
		if err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}

func (s *mysqlStore) ListReviewsByReviewee(revieweeID string) []*model.Review {
	rows, err := s.db.Query("SELECT "+reviewCols+" FROM review WHERE reviewee_id=? ORDER BY created_at,id", revieweeID)
	if err != nil {
		return []*model.Review{}
	}
	defer rows.Close()
	out := make([]*model.Review, 0)
	for rows.Next() {
		r, err := scanReview(rows, true)
		if err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}

// ListRevealedReviewsByReviewee 只算已可见的评价。
//
// 两个条件都要: 已揭盲(revealed_at>0), 或者单方提交满 7 天(created_at 早于 now-7d)。
// 第二条是派生判定, 所以这里直接交给 SQL 做, 免得把全表评价捞进内存再筛。
func (s *mysqlStore) ListRevealedReviewsByReviewee(revieweeID string, now int64) []*model.Review {
	cutoff := now - model.ReviewAutoRevealMillis
	rows, err := s.db.Query("SELECT "+reviewCols+` FROM review
		WHERE reviewee_id=? AND (revealed_at>0 OR created_at<=?)
		ORDER BY created_at,id`, revieweeID, cutoff)
	if err != nil {
		return []*model.Review{}
	}
	defer rows.Close()
	out := make([]*model.Review, 0)
	for rows.Next() {
		r, err := scanReview(rows, true)
		if err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}

// --- Credit Events ---

// appendCreditEventTx 幂等写一条信用事件。调用方必须已开启事务。
//
// 幂等键是库里的 uk_user_kind_ref(user_id,kind,ref_id), 撞键时用 no-op 更新收场:
// 信用事件一旦发生就不该被改写, 重试只应该"不报错", 不该"覆盖分数"。
func (s *mysqlStore) appendCreditEventTx(tx *sql.Tx, userID, kind, refID string, delta int, at int64) error {
	if userID == "" || refID == "" {
		return nil
	}
	var score int
	if err := tx.QueryRow("SELECT credit_score FROM `user` WHERE id=? AND deleted_at IS NULL", userID).Scan(&score); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		score = 0 // 用户不存在时不写脏数据, 只把快照置 0
	}
	score += delta
	_, err := tx.Exec(`INSERT INTO credit_event (id,user_id,kind,ref_id,delta,score_after,created_at)
		VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE id=id`,
		s.NextID("CE"), userID, kind, refID, delta, score, at)
	return err
}

func (s *mysqlStore) ListCreditEvents(userID string) []*model.CreditEvent {
	rows, err := s.db.Query("SELECT id,user_id,kind,ref_id,delta,score_after,created_at FROM credit_event WHERE user_id=? ORDER BY created_at,id", userID)
	if err != nil {
		return []*model.CreditEvent{}
	}
	defer rows.Close()
	out := make([]*model.CreditEvent, 0)
	for rows.Next() {
		e := &model.CreditEvent{}
		if err := rows.Scan(&e.ID, &e.UserID, &e.Kind, &e.RefID, &e.Delta, &e.ScoreAfter, &e.CreatedAt); err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

// --- Compliance ---
func (s *mysqlStore) GetCompliance() *model.ComplianceState {
	st := model.NewComplianceState()
	rows, err := s.db.Query("SELECT `item`, `on` FROM compliance_state")
	if err != nil {
		return st
	}
	defer rows.Close()
	for rows.Next() {
		var item string
		var on int
		if err := rows.Scan(&item, &on); err != nil {
			return st
		}
		st.Items[model.ComplianceItem(item)] = on == 1
	}
	return st
}
func (s *mysqlStore) SetCompliance(item model.ComplianceItem, on bool) {
	_, _ = s.db.Exec("INSERT INTO compliance_state (`item`, `on`, note) VALUES (?,?,?) ON DUPLICATE KEY UPDATE `on`=VALUES(`on`)", string(item), boolToInt(on), "")
}

// --- Wallets / Ledger / Withdrawals (P2-1) ---

func (s *mysqlStore) GetWallet(uid string) (*model.Wallet, bool) {
	w := &model.Wallet{}
	err := s.db.QueryRow("SELECT uid,balance_cents,frozen_cents,updated_at FROM wallet WHERE uid=?", uid).
		Scan(&w.UID, &w.BalanceCents, &w.FrozenCents, &w.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return w, true
}

func (s *mysqlStore) GetOrCreateWallet(uid string) *model.Wallet {
	w, ok := s.GetWallet(uid)
	if ok {
		return w
	}
	now := model.Now()
	_, _ = s.db.Exec("INSERT IGNORE INTO wallet (uid,balance_cents,frozen_cents,updated_at) VALUES (?,?,?,?)", uid, 0, 0, now)
	return &model.Wallet{UID: uid, BalanceCents: 0, FrozenCents: 0, UpdatedAt: now}
}

func (s *mysqlStore) PutWallet(w *model.Wallet) {
	w.UpdatedAt = model.Now()
	_, _ = s.db.Exec(`INSERT INTO wallet (uid,balance_cents,frozen_cents,updated_at) VALUES (?,?,?,?)
		ON DUPLICATE KEY UPDATE balance_cents=VALUES(balance_cents),frozen_cents=VALUES(frozen_cents),updated_at=VALUES(updated_at)`,
		w.UID, w.BalanceCents, w.FrozenCents, w.UpdatedAt)
}

// ListWallets 返回全部钱包, 供运营看板聚合平台资金水位。
func (s *mysqlStore) ListWallets() []*model.Wallet {
	rows, err := s.db.Query("SELECT uid,balance_cents,frozen_cents,updated_at FROM wallet")
	if err != nil {
		return []*model.Wallet{}
	}
	defer rows.Close()
	out := make([]*model.Wallet, 0)
	for rows.Next() {
		w := &model.Wallet{}
		if err := rows.Scan(&w.UID, &w.BalanceCents, &w.FrozenCents, &w.UpdatedAt); err != nil {
			return []*model.Wallet{}
		}
		out = append(out, w)
	}
	return out
}

func (s *mysqlStore) ListLedgerByUser(uid string) []*model.Ledger {
	rows, err := s.db.Query("SELECT id,uid,order_id,type,amount_cents,balance_after,external_id,note,created_at FROM ledger WHERE uid=? ORDER BY created_at DESC", uid)
	if err != nil {
		return []*model.Ledger{}
	}
	defer rows.Close()
	out := make([]*model.Ledger, 0)
	for rows.Next() {
		l := &model.Ledger{}
		var orderID, extID, note sql.NullString
		if err := rows.Scan(&l.ID, &l.UID, &orderID, &l.Type, &l.AmountCents, &l.BalanceAfter, &extID, &note, &l.CreatedAt); err != nil {
			return []*model.Ledger{}
		}
		l.OrderID = orderID.String
		l.ExternalID = extID.String
		l.Note = note.String
		out = append(out, l)
	}
	return out
}

func (s *mysqlStore) PutLedger(l *model.Ledger) {
	if l.CreatedAt == 0 {
		l.CreatedAt = model.Now()
	}
	_, _ = s.db.Exec(`INSERT INTO ledger (id,uid,order_id,type,amount_cents,balance_after,external_id,note,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE balance_after=VALUES(balance_after),external_id=VALUES(external_id)`,
		l.ID, l.UID, nullStr(l.OrderID), l.Type, l.AmountCents, l.BalanceAfter, nullStr(l.ExternalID), nullStr(l.Note), l.CreatedAt)
}

func (s *mysqlStore) ListWithdrawals() []*model.Withdrawal {
	rows, err := s.db.Query("SELECT id,uid,amount_cents,status,created_at,resolved_at,note FROM withdrawal ORDER BY created_at DESC")
	if err != nil {
		return []*model.Withdrawal{}
	}
	defer rows.Close()
	out := make([]*model.Withdrawal, 0)
	for rows.Next() {
		w := &model.Withdrawal{}
		var resolvedAt sql.NullInt64
		var note sql.NullString
		if err := rows.Scan(&w.ID, &w.UID, &w.AmountCents, &w.Status, &w.CreatedAt, &resolvedAt, &note); err != nil {
			return []*model.Withdrawal{}
		}
		w.ResolvedAt = resolvedAt.Int64
		w.Note = note.String
		out = append(out, w)
	}
	return out
}

func (s *mysqlStore) ListWithdrawalsByUser(uid string) []*model.Withdrawal {
	rows, err := s.db.Query("SELECT id,uid,amount_cents,status,created_at,resolved_at,note FROM withdrawal WHERE uid=? ORDER BY created_at DESC", uid)
	if err != nil {
		return []*model.Withdrawal{}
	}
	defer rows.Close()
	out := make([]*model.Withdrawal, 0)
	for rows.Next() {
		w := &model.Withdrawal{}
		var resolvedAt sql.NullInt64
		var note sql.NullString
		if err := rows.Scan(&w.ID, &w.UID, &w.AmountCents, &w.Status, &w.CreatedAt, &resolvedAt, &note); err != nil {
			return []*model.Withdrawal{}
		}
		w.ResolvedAt = resolvedAt.Int64
		w.Note = note.String
		out = append(out, w)
	}
	return out
}

func (s *mysqlStore) GetWithdrawal(id string) (*model.Withdrawal, bool) {
	w := &model.Withdrawal{}
	var resolvedAt sql.NullInt64
	var note sql.NullString
	err := s.db.QueryRow("SELECT id,uid,amount_cents,status,created_at,resolved_at,note FROM withdrawal WHERE id=?", id).
		Scan(&w.ID, &w.UID, &w.AmountCents, &w.Status, &w.CreatedAt, &resolvedAt, &note)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	w.ResolvedAt = resolvedAt.Int64
	w.Note = note.String
	return w, true
}

func (s *mysqlStore) PutWithdrawal(w *model.Withdrawal) {
	if w.CreatedAt == 0 {
		w.CreatedAt = model.Now()
	}
	_, _ = s.db.Exec(`INSERT INTO withdrawal (id,uid,amount_cents,status,created_at,resolved_at,note)
		VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE status=VALUES(status),resolved_at=VALUES(resolved_at),note=VALUES(note)`,
		w.ID, w.UID, w.AmountCents, w.Status, w.CreatedAt, w.ResolvedAt, nullStr(w.Note))
}

// ---- IM 会话与消息 (P2-2) ----

func (s *mysqlStore) SaveMessage(m *model.ChatMessage) {
	if m.CreatedAt == 0 {
		m.CreatedAt = model.Now()
	}
	_, _ = s.db.Exec(`INSERT INTO chat_message (id,conv_id,from_id,to_id,body,status,is_ai,ai_type,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE body=VALUES(body),status=VALUES(status),is_ai=VALUES(is_ai),ai_type=VALUES(ai_type)`,
		m.ID, m.ConvID, m.FromID, m.ToID, m.Body, m.Status, boolToInt(m.IsAI), m.AIType, m.CreatedAt)
}

func (s *mysqlStore) ListMessages(convID string) []*model.ChatMessage {
	rows, err := s.db.Query("SELECT id,conv_id,from_id,to_id,body,status,is_ai,ai_type,created_at FROM chat_message WHERE conv_id=? ORDER BY created_at ASC", convID)
	if err != nil {
		return []*model.ChatMessage{}
	}
	defer rows.Close()
	out := make([]*model.ChatMessage, 0)
	for rows.Next() {
		m := &model.ChatMessage{}
		var isAI int
		if err := rows.Scan(&m.ID, &m.ConvID, &m.FromID, &m.ToID, &m.Body, &m.Status, &isAI, &m.AIType, &m.CreatedAt); err != nil {
			return []*model.ChatMessage{}
		}
		m.IsAI = isAI == 1
		out = append(out, m)
	}
	return out
}

func (s *mysqlStore) GetConversation(cid string) (*model.Conversation, bool) {
	c := &model.Conversation{}
	err := s.db.QueryRow("SELECT id,a,b,order_id,last_body,last_at,a_unread,b_unread,updated_at FROM conversation WHERE id=?", cid).
		Scan(&c.ID, &c.A, &c.B, &c.OrderID, &c.LastBody, &c.LastAt, &c.AUnread, &c.BUnread, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return c, true
}

func (s *mysqlStore) GetOrCreateConversation(a, b, orderID string) *model.Conversation {
	cid := convID(a, b)
	if c, ok := s.GetConversation(cid); ok {
		return c
	}
	now := model.Now()
	_, _ = s.db.Exec("INSERT IGNORE INTO conversation (id,a,b,order_id,last_body,last_at,a_unread,b_unread,updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
		cid, a, b, nullStr(orderID), "", 0, 0, 0, now)
	return &model.Conversation{ID: cid, A: a, B: b, OrderID: orderID, UpdatedAt: now}
}

func (s *mysqlStore) SaveConversation(c *model.Conversation) {
	c.UpdatedAt = model.Now()
	_, _ = s.db.Exec(`INSERT INTO conversation (id,a,b,order_id,last_body,last_at,a_unread,b_unread,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE order_id=VALUES(order_id),last_body=VALUES(last_body),last_at=VALUES(last_at),a_unread=VALUES(a_unread),b_unread=VALUES(b_unread),updated_at=VALUES(updated_at)`,
		c.ID, c.A, c.B, nullStr(c.OrderID), c.LastBody, c.LastAt, c.AUnread, c.BUnread, c.UpdatedAt)
}

func (s *mysqlStore) ListConversations(uid string) []*model.Conversation {
	rows, err := s.db.Query("SELECT id,a,b,order_id,last_body,last_at,a_unread,b_unread,updated_at FROM conversation WHERE a=? OR b=? ORDER BY updated_at DESC", uid, uid)
	if err != nil {
		return []*model.Conversation{}
	}
	defer rows.Close()
	out := make([]*model.Conversation, 0)
	for rows.Next() {
		c := &model.Conversation{}
		var orderID sql.NullString
		if err := rows.Scan(&c.ID, &c.A, &c.B, &orderID, &c.LastBody, &c.LastAt, &c.AUnread, &c.BUnread, &c.UpdatedAt); err != nil {
			return []*model.Conversation{}
		}
		c.OrderID = orderID.String
		out = append(out, c)
	}
	return out
}

// ---- 通知中心 (P2-3) ----

func (s *mysqlStore) SaveNotification(n *model.Notification) {
	if n.CreatedAt == 0 {
		n.CreatedAt = model.Now()
	}
	_, _ = s.db.Exec(`INSERT INTO notification (id,uid,channel,title,body,status,created_at,read_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE status=VALUES(status),read_at=VALUES(read_at)`,
		n.ID, n.UID, n.Channel, n.Title, n.Body, n.Status, n.CreatedAt, nullInt64(n.ReadAt))
}

func (s *mysqlStore) GetNotification(id string) (*model.Notification, bool) {
	n := &model.Notification{}
	var readAt sql.NullInt64
	err := s.db.QueryRow("SELECT id,uid,channel,title,body,status,created_at,read_at FROM notification WHERE id=?", id).
		Scan(&n.ID, &n.UID, &n.Channel, &n.Title, &n.Body, &n.Status, &n.CreatedAt, &readAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	n.ReadAt = readAt.Int64
	return n, true
}

func (s *mysqlStore) ListNotifications(uid string, limit int) []*model.Notification {
	rows, err := s.db.Query("SELECT id,uid,channel,title,body,status,created_at,read_at FROM notification WHERE uid=? ORDER BY created_at DESC LIMIT ?", uid, limit)
	if err != nil {
		return []*model.Notification{}
	}
	defer rows.Close()
	out := make([]*model.Notification, 0)
	for rows.Next() {
		n := &model.Notification{}
		var readAt sql.NullInt64
		if err := rows.Scan(&n.ID, &n.UID, &n.Channel, &n.Title, &n.Body, &n.Status, &n.CreatedAt, &readAt); err != nil {
			return []*model.Notification{}
		}
		n.ReadAt = readAt.Int64
		out = append(out, n)
	}
	return out
}

func (s *mysqlStore) MarkNotificationRead(uid, id string) (*model.Notification, bool) {
	n, ok := s.GetNotification(id)
	if !ok || n.UID != uid {
		return nil, false
	}
	now := model.Now()
	_, _ = s.db.Exec("UPDATE notification SET status=?, read_at=? WHERE id=?", model.NotiRead, now, id)
	n.Status = model.NotiRead
	n.ReadAt = now
	return n, true
}

func (s *mysqlStore) CountUnread(uid string) int {
	var c int
	err := s.db.QueryRow("SELECT COUNT(*) FROM notification WHERE uid=? AND status=?", uid, model.NotiUnread).Scan(&c)
	if err != nil {
		return 0
	}
	return c
}

// ---- 企业版治理 (P3 最小骨架: 组织/成员/角色) ----

func (s *mysqlStore) SaveOrg(o *model.Org) {
	_, _ = s.db.Exec(`INSERT INTO org (id,name,owner_id,plan,status,created_at)
		VALUES (?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE name=VALUES(name), owner_id=VALUES(owner_id), plan=VALUES(plan), status=VALUES(status)`,
		o.ID, o.Name, o.OwnerID, o.Plan, o.Status, o.CreatedAt)
}

func (s *mysqlStore) GetOrg(id string) (*model.Org, bool) {
	var o model.Org
	err := s.db.QueryRow("SELECT id,name,owner_id,plan,status,created_at FROM org WHERE id=?", id).
		Scan(&o.ID, &o.Name, &o.OwnerID, &o.Plan, &o.Status, &o.CreatedAt)
	if err != nil {
		return nil, false
	}
	return &o, true
}

func (s *mysqlStore) ListOrgsByOwner(ownerUID string) []*model.Org {
	rows, err := s.db.Query("SELECT id,name,owner_id,plan,status,created_at FROM org WHERE owner_id=?", ownerUID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Org
	for rows.Next() {
		var o model.Org
		if err := rows.Scan(&o.ID, &o.Name, &o.OwnerID, &o.Plan, &o.Status, &o.CreatedAt); err == nil {
			out = append(out, &o)
		}
	}
	return out
}

func (s *mysqlStore) SaveOrgMember(m *model.OrgMember) {
	_, _ = s.db.Exec(`INSERT INTO org_member (org_id,user_id,role,joined_at)
		VALUES (?,?,?,?)
		ON DUPLICATE KEY UPDATE role=VALUES(role), joined_at=VALUES(joined_at)`,
		m.OrgID, m.UserID, m.Role, m.JoinedAt)
}

func (s *mysqlStore) ListOrgMembers(orgID string) []*model.OrgMember {
	rows, err := s.db.Query("SELECT org_id,user_id,role,joined_at FROM org_member WHERE org_id=?", orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.OrgMember
	for rows.Next() {
		var m model.OrgMember
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &m.JoinedAt); err == nil {
			out = append(out, &m)
		}
	}
	return out
}

func (s *mysqlStore) GetOrgMember(orgID, uid string) (*model.OrgMember, bool) {
	var m model.OrgMember
	err := s.db.QueryRow("SELECT org_id,user_id,role,joined_at FROM org_member WHERE org_id=? AND user_id=?", orgID, uid).
		Scan(&m.OrgID, &m.UserID, &m.Role, &m.JoinedAt)
	if err != nil {
		return nil, false
	}
	return &m, true
}

// ---- P3 专项: 组织钱包 / 审批流 / 开放 API + Webhook (mysqlStore) ----

func (s *mysqlStore) GetOrgWallet(orgID string) (*model.OrgWallet, bool) {
	var w model.OrgWallet
	err := s.db.QueryRow("SELECT org_id,balance_cents,frozen_cents,updated_at FROM org_wallet WHERE org_id=?", orgID).
		Scan(&w.OrgID, &w.BalanceCents, &w.FrozenCents, &w.UpdatedAt)
	if err != nil {
		return nil, false
	}
	return &w, true
}

func (s *mysqlStore) GetOrCreateOrgWallet(orgID string) *model.OrgWallet {
	if w, ok := s.GetOrgWallet(orgID); ok {
		return w
	}
	w := &model.OrgWallet{OrgID: orgID, UpdatedAt: model.Now()}
	_, _ = s.db.Exec("INSERT INTO org_wallet (org_id,balance_cents,frozen_cents,updated_at) VALUES (?,?,?,?)", w.OrgID, 0, 0, w.UpdatedAt)
	return w
}

func (s *mysqlStore) PutOrgWallet(w *model.OrgWallet) {
	w.UpdatedAt = model.Now()
	_, _ = s.db.Exec(`INSERT INTO org_wallet (org_id,balance_cents,frozen_cents,updated_at) VALUES (?,?,?,?)
		ON DUPLICATE KEY UPDATE balance_cents=VALUES(balance_cents),frozen_cents=VALUES(frozen_cents),updated_at=VALUES(updated_at)`,
		w.OrgID, w.BalanceCents, w.FrozenCents, w.UpdatedAt)
}

func (s *mysqlStore) SaveApproval(a *model.Approval) {
	_, _ = s.db.Exec(`INSERT INTO approval (id,org_id,type,ref_id,amount_cents,status,created_by,approved_by,note,created_at,resolved_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE status=VALUES(status),approved_by=VALUES(approved_by),note=VALUES(note),resolved_at=VALUES(resolved_at),ref_id=VALUES(ref_id),amount_cents=VALUES(amount_cents)`,
		a.ID, a.OrgID, a.Type, a.RefID, a.AmountCents, a.Status, a.CreatedBy, a.ApprovedBy, a.Note, a.CreatedAt, a.ResolvedAt)
}

func (s *mysqlStore) GetApproval(id string) (*model.Approval, bool) {
	var a model.Approval
	err := s.db.QueryRow("SELECT id,org_id,type,ref_id,amount_cents,status,created_by,approved_by,note,created_at,resolved_at FROM approval WHERE id=?", id).
		Scan(&a.ID, &a.OrgID, &a.Type, &a.RefID, &a.AmountCents, &a.Status, &a.CreatedBy, &a.ApprovedBy, &a.Note, &a.CreatedAt, &a.ResolvedAt)
	if err != nil {
		return nil, false
	}
	return &a, true
}

func (s *mysqlStore) ListApprovalsByOrg(orgID string) []*model.Approval {
	rows, err := s.db.Query("SELECT id,org_id,type,ref_id,amount_cents,status,created_by,approved_by,note,created_at,resolved_at FROM approval WHERE org_id=?", orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Approval
	for rows.Next() {
		var a model.Approval
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Type, &a.RefID, &a.AmountCents, &a.Status, &a.CreatedBy, &a.ApprovedBy, &a.Note, &a.CreatedAt, &a.ResolvedAt); err == nil {
			out = append(out, &a)
		}
	}
	return out
}

func (s *mysqlStore) SaveAPIKey(k *model.APIKey) {
	_, _ = s.db.Exec(`INSERT INTO api_key (id,org_id,key_hash,label,scopes,status,created_at) VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE label=VALUES(label),scopes=VALUES(scopes),status=VALUES(status)`,
		k.ID, k.OrgID, k.KeyHash, k.Label, k.Scopes, k.Status, k.CreatedAt)
}

func (s *mysqlStore) GetAPIKeyByHash(hash string) (*model.APIKey, bool) {
	var k model.APIKey
	err := s.db.QueryRow("SELECT id,org_id,key_hash,label,scopes,status,created_at FROM api_key WHERE key_hash=?", hash).
		Scan(&k.ID, &k.OrgID, &k.KeyHash, &k.Label, &k.Scopes, &k.Status, &k.CreatedAt)
	if err != nil {
		return nil, false
	}
	return &k, true
}

func (s *mysqlStore) ListAPIKeysByOrg(orgID string) []*model.APIKey {
	rows, err := s.db.Query("SELECT id,org_id,key_hash,label,scopes,status,created_at FROM api_key WHERE org_id=?", orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.APIKey
	for rows.Next() {
		var k model.APIKey
		if err := rows.Scan(&k.ID, &k.OrgID, &k.KeyHash, &k.Label, &k.Scopes, &k.Status, &k.CreatedAt); err == nil {
			out = append(out, &k)
		}
	}
	return out
}

func (s *mysqlStore) SaveWebhook(w *model.Webhook) {
	events, _ := json.Marshal(w.Events)
	_, _ = s.db.Exec(`INSERT INTO webhook (id,org_id,url,events,secret,status,created_at) VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE url=VALUES(url),events=VALUES(events),secret=VALUES(secret),status=VALUES(status)`,
		w.ID, w.OrgID, w.URL, string(events), w.Secret, w.Status, w.CreatedAt)
}

func (s *mysqlStore) ListWebhooksByOrg(orgID string) []*model.Webhook {
	rows, err := s.db.Query("SELECT id,org_id,url,events,secret,status,created_at FROM webhook WHERE org_id=?", orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Webhook
	for rows.Next() {
		var w model.Webhook
		var events string
		if err := rows.Scan(&w.ID, &w.OrgID, &w.URL, &events, &w.Secret, &w.Status, &w.CreatedAt); err == nil {
			_ = json.Unmarshal([]byte(events), &w.Events)
			out = append(out, &w)
		}
	}
	return out
}

func (s *mysqlStore) SaveWebhookDelivery(d *model.WebhookDelivery) {
	_, _ = s.db.Exec(`INSERT INTO webhook_delivery (id,org_id,webhook_id,event,status,attempts,created_at) VALUES (?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE status=VALUES(status),attempts=VALUES(attempts)`,
		d.ID, d.OrgID, d.WebhookID, d.Event, d.Status, d.Attempts, d.CreatedAt)
}

func (s *mysqlStore) ListWebhookDeliveriesByOrg(orgID string) []*model.WebhookDelivery {
	rows, err := s.db.Query("SELECT id,org_id,webhook_id,event,status,attempts,created_at FROM webhook_delivery WHERE org_id=?", orgID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.WebhookDelivery
	for rows.Next() {
		var d model.WebhookDelivery
		if err := rows.Scan(&d.ID, &d.OrgID, &d.WebhookID, &d.Event, &d.Status, &d.Attempts, &d.CreatedAt); err == nil {
			out = append(out, &d)
		}
	}
	return out
}
