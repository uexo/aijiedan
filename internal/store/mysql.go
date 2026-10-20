// Package store - 真实 MySQL 实现(遵循 Store 接口)。
// 由 NewMySQL(dsn) 构建, DSN 从环境变量 ZANBUDDY_DB_DSN 读取(见 main.go)。
// 依赖 github.com/go-sql-driver/mysql; 仅 MySQL 模式才需要外部依赖, 内存模式仍零依赖。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"

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
	rows, err := s.db.Query("SELECT "+userCols+" FROM `user` WHERE deleted_at IS NULL")
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
func (s *mysqlStore) GetTask(id string) (*model.Task, bool) {	t := &model.Task{}
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

// --- Orders ---
func (s *mysqlStore) PutOrder(o *model.Order) {
	mil, _ := json.Marshal(o.Milestones)
	acc, _ := json.Marshal(o.Acceptance)
	_, _ = s.db.Exec(`INSERT INTO ` + "`order`" + ` (id,task_id,requester_id,org_id,provider_id,match_id,amount_cents,status,escrow_id,settled,max_revisions,milestones,settlement_mode,refund_cents,refund_pct,cancelled_by,cancel_reason,cancelled_at,dispute_reason,disputed_by,disputed_at,resolved_by,resolved_at,resolved_note,acceptance_json,severe_mismatch,accepted_at,created_at,provider_quote_cents)
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
	_, _ = s.db.Exec(`INSERT INTO delivery (id,order_id,round,content,link,items_json,status,created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE round=VALUES(round),content=VALUES(content),link=VALUES(link),items_json=VALUES(items_json),status=VALUES(status)`,
		d.ID, d.OrderID, d.Round, d.Content, nullStr(d.Link), nullStr(string(items)), d.Status, d.CreatedAt)
}
func (s *mysqlStore) GetDelivery(id string) (*model.Delivery, bool) {
	d := &model.Delivery{}
	var items []byte
	var link sql.NullString
	err := s.db.QueryRow("SELECT id,order_id,round,content,link,items_json,status,created_at FROM delivery WHERE id=?", id).
		Scan(&d.ID, &d.OrderID, &d.Round, &d.Content, &link, &items, &d.Status, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	d.Link = link.String
	if len(items) > 0 {
		_ = json.Unmarshal(items, &d.Items)
	}
	return d, true
}
func (s *mysqlStore) ListDeliveriesByOrder(orderID string) []*model.Delivery {
	rows, err := s.db.Query("SELECT id,order_id,round,content,link,items_json,status,created_at FROM delivery WHERE order_id=? ORDER BY round", orderID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*model.Delivery
	for rows.Next() {
		d := &model.Delivery{}
		var items []byte
		var link sql.NullString
		if err := rows.Scan(&d.ID, &d.OrderID, &d.Round, &d.Content, &link, &items, &d.Status, &d.CreatedAt); err != nil {
			return nil
		}
		d.Link = link.String
		if len(items) > 0 {
			_ = json.Unmarshal(items, &d.Items)
		}
		out = append(out, d)
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
func (s *mysqlStore) GetReview(id string) (*model.Review, bool) {
	r := &model.Review{}
	err := s.db.QueryRow("SELECT id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at FROM review WHERE id=?", id).
		Scan(&r.ID, &r.OrderID, &r.TaskID, &r.ReviewerID, &r.RevieweeID, &r.ReviewerRole, &r.Rating, &r.Comment, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return r, true
}
func (s *mysqlStore) ListReviewsByOrder(orderID string) []*model.Review {
	rows, err := s.db.Query("SELECT id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at FROM review WHERE order_id=?", orderID)
	if err != nil {
		return []*model.Review{}
	}
	defer rows.Close()
	out := make([]*model.Review, 0)
	for rows.Next() {
		r := &model.Review{}
		if err := rows.Scan(&r.ID, &r.OrderID, &r.TaskID, &r.ReviewerID, &r.RevieweeID, &r.ReviewerRole, &r.Rating, &r.Comment, &r.CreatedAt); err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}
func (s *mysqlStore) ListReviewsByReviewee(revieweeID string) []*model.Review {
	rows, err := s.db.Query("SELECT id,order_id,task_id,reviewer_id,reviewee_id,reviewer_role,rating,comment,created_at FROM review WHERE reviewee_id=?", revieweeID)
	if err != nil {
		return []*model.Review{}
	}
	defer rows.Close()
	out := make([]*model.Review, 0)
	for rows.Next() {
		r := &model.Review{}
		if err := rows.Scan(&r.ID, &r.OrderID, &r.TaskID, &r.ReviewerID, &r.RevieweeID, &r.ReviewerRole, &r.Rating, &r.Comment, &r.CreatedAt); err != nil {
			return nil
		}
		out = append(out, r)
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
