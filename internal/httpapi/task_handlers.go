package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

// hallTask 大厅卡片视图。字段是白名单:
// 需求方的手机号、内部 ID 一概不出这个接口, 只给昵称、公开短号和信用分。
type hallTask struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Category       string `json:"category"`
	CategoryName   string `json:"category_name"`
	BudgetCents    int64  `json:"budget_cents"`
	Spec           string `json:"spec"`
	Deliverables   string `json:"deliverables"`
	RequiredFormat string `json:"required_format"`
	StyleRef       string `json:"style_ref"`
	DeadlineAt     int64  `json:"deadline_at"`
	CreatedAt      int64  `json:"created_at"`
	Status         string `json:"status"`
	Requester      string `json:"requester"`
	RequesterSlug  string `json:"requester_slug"`
	CreditScore    int    `json:"requester_credit"`
	// StateLabel 这一单现在处于什么状态的人话说法(开放中/已有人接/已完成/已撤回)。
	// 放进来是因为认领页要告诉点开链接的人"这张单现在还能不能接",
	// 让他自己从 status 猜, 前端早晚猜错一个。
	StateLabel string `json:"state_label"`
}

// listTasks 需求大厅列表。公开可读: 没登录也能看平台上有哪些需求,
// 这是拉新页面(落地页 → 大厅 → 登录), 拦在登录后面等于把人挡在门外。
//
// 只返回 open。其他状态属于交易双方的过程信息, 不该出现在公开列表里。
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := queryInt(q.Get("limit"), 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset := queryInt(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	// 排序走白名单: 未知值回落 newest, 不把用户输入拼进排序表达式。
	sortBy := q.Get("sort")
	switch sortBy {
	case "budget_desc", "budget_asc":
	default:
		sortBy = "newest"
	}

	f := store.TaskFilter{
		Status:    string(model.TaskOpen),
		Category:  strings.TrimSpace(q.Get("category")),
		Q:         strings.TrimSpace(q.Get("q")),
		MinBudget: int64(queryInt(q.Get("min_budget"), 0)),
		MaxBudget: int64(queryInt(q.Get("max_budget"), 0)),
		Sort:      sortBy,
	}

	all := s.Store.SearchTasks(f)
	total := len(all)

	items := make([]hallTask, 0, limit)
	for i := offset; i < total && len(items) < limit; i++ {
		items = append(items, s.toHallTask(all[i]))
	}

	next := offset + len(items)
	resp := map[string]interface{}{
		"items":       items,
		"total":       total,
		"limit":       limit,
		"offset":      offset,
		"next_offset": next,
		"has_more":    next < total,
	}
	WriteOK(w, r, resp)
}

func (s *Server) toHallTask(t *model.Task) hallTask {
	v := hallTask{
		ID:             t.ID,
		Title:          t.Title,
		Category:       t.Category,
		CategoryName:   categoryLabel(t.Category),
		BudgetCents:    t.BudgetCents,
		Spec:           t.Spec,
		Deliverables:   t.SpecObj.Deliverables,
		RequiredFormat: t.SpecObj.RequiredFormat,
		StyleRef:       t.SpecObj.StyleRef,
		DeadlineAt:     t.SpecObj.DeadlineAt,
		CreatedAt:      t.CreatedAt,
		Status:         string(t.Status),
		StateLabel:     taskStateLabel(t.Status),
		Requester:      "需求方",
	}
	if u, ok := s.Store.GetUser(t.RequesterID); ok {
		if strings.TrimSpace(u.Name) != "" {
			v.Requester = u.Name
		}
		v.RequesterSlug = u.Slug
		v.CreditScore = u.CreditScore
	}
	return v
}

// listCategories 类目清单。前端筛选条不再硬编码, 新增类目只改 model.TaskCategories。
func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	out := make([]item, 0, len(model.TaskCategories))
	for _, c := range model.TaskCategories {
		out = append(out, item{ID: c.ID, Label: c.Label})
	}
	WriteOK(w, r, map[string]interface{}{"items": out})
}

func categoryLabel(id string) string {
	for _, c := range model.TaskCategories {
		if c.ID == id {
			return c.Label
		}
	}
	return id
}

// queryInt 宽松解析整数查询参数: 非法值等于没传, 不 400。
// 筛选条件写错不该让人看到报错页, 回落到默认值即可。
func queryInt(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}
