// Package demand 需求域: 发单与撤回。
//
// 边界: 只碰 task 表。需要用户信息时走 store 读, 不反向依赖身份域。
// 校验全部在这里, httpapi 只负责传输——换个入口(小程序、开放 API)不用把规则抄一遍。
package demand

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"zanbuddy/internal/model"
	"zanbuddy/internal/store"
)

// Service 需求服务。
type Service struct {
	st store.Store
}

func New(st store.Store) *Service { return &Service{st: st} }

// Error 业务错误。状态码与错误码在这一层定, handler 不再各自拼。
type Error struct {
	Status int
	Code   string
	Msg    string
	Hint   string
}

func (e *Error) Error() string { return e.Msg }

var (
	ErrTitleShort = &Error{400, "TITLE_TOO_SHORT", "标题至少 4 个字", "一句话说清要做什么, 接单方才扫得下去"}
	ErrTitleLong  = &Error{400, "TITLE_TOO_LONG", "标题太长了", "60 字以内, 细节写进需求说明"}
	ErrSpecShort  = &Error{400, "SPEC_TOO_SHORT", "需求说明至少 20 个字", "写清背景、要什么、给谁用。含糊的单没人敢接"}
	ErrSpecLong   = &Error{400, "SPEC_TOO_LONG", "需求说明太长了", "2000 字以内; 大段素材请放链接"}
	ErrCategory   = &Error{400, "CATEGORY_INVALID", "类目不在清单里", "从 GET /api/v2/meta/categories 里选一个"}

	ErrBudgetLow = &Error{400, "BUDGET_TOO_LOW", "预算至少 1 元", "把预算写清楚, 接单方才好判断要不要接"}
	// 单位是分。真实的 AI 外包单到不了十万, 超过这个数基本是把"元"当成了"分"多打两个零。
	ErrBudgetHigh = &Error{400, "BUDGET_TOO_HIGH", "预算超出上限（10 万元）", "金额单位是分: 500 元要填 50000"}

	ErrDeadlinePast = &Error{400, "DEADLINE_IN_PAST", "截止时间要晚于现在", "想不出具体日期就别填, 先挂在没有截止的状态"}
	ErrItemEmpty    = &Error{400, "ITEM_EMPTY", "需求清单里有空项", "删掉它, 或者写一句要什么"}
	ErrItemTooMany  = &Error{400, "ITEM_TOO_MANY", "需求清单最多 12 项", "挑关键几条, 太细的写进需求说明"}

	ErrPublishFailed = &Error{500, "PUBLISH_FAILED", "没发出去, 请再试一次", ""}

	// 撤单的四种"撤不了"。分开写是因为这四种要用户做的事完全不同:
	// 找不到 = 别找了, 不是你的 = 别点了, 有人接了 = 先跟人谈, 已经撤过 = 刷新看。
	ErrTaskNotFound = &Error{404, "TASK_NOT_FOUND", "没有这条需求", "可能已经删掉, 或者链接不对"}
	ErrNotRequester = &Error{403, "NOT_TASK_OWNER", "这条需求不是你发的", "只有发单的人能撤回"}
	ErrTaskTaken    = &Error{409, "TASK_ALREADY_TAKEN", "已经有人接了，不能直接撤回", "先在「我的单」里把这一单退回或取消，单子才会重新开放"}
	ErrTaskDone     = &Error{409, "TASK_NOT_OPEN", "这条需求已经收工或撤回过", "只能撤回还挂在大厅、没人接的单"}
	ErrWithdrawRace = &Error{409, "TASK_CHANGED", "刚好有人接了这一单", "刷新一下看看，这条已经在推进里了"}
)

// 长度上限。上限不是审美问题, 是列宽问题:
// task.title 是 VARCHAR(128)、deliverables VARCHAR(255)、required_format VARCHAR(128)、
// style_ref VARCHAR(255)、samples VARCHAR(512)。而 store.PutTask 吞掉写库错误,
// 一旦超长, MySQL 严格模式报 1406, 用户却收到 201——发出去的单在大厅里根本不存在。
// 所以字段宽度必须在这里拦死, 不能指望数据库。
const (
	titleMinRunes = 4
	titleMaxRunes = 60

	specMinRunes = 20
	specMaxRunes = 2000

	budgetMinCents = 100       // 1 元
	budgetMaxCents = 100000000 // 100 万元: 真实 AI 外包单到不了这个数

	itemsMax         = 12
	itemDescMaxRunes = 100

	deliverablesMaxRunes   = 80
	requiredFormatMaxRunes = 40
	styleRefMaxRunes       = 80
	samplesMaxRunes        = 160
)

// Input 发单入参。字段名与请求体一致, handler 直接解成这个结构。
type Input struct {
	Title          string   `json:"title"`
	Category       string   `json:"category"`
	Spec           string   `json:"spec"`
	BudgetCents    int64    `json:"budget_cents"`
	DeadlineAt     int64    `json:"deadline_at"` // ms 时间戳, 0 表示不设
	Deliverables   string   `json:"deliverables"`
	RequiredFormat string   `json:"required_format"`
	StyleRef       string   `json:"style_ref"`
	Samples        string   `json:"samples"`
	Items          []string `json:"items"` // 需求清单, 服务端按序编号
}

// View 发单后的返回。只出需求方此刻关心的东西, 不整包吐 model.Task
// (里面有 requester_id、org_id、translation 之类跟他无关的字段)。
type View struct {
	ID             string                  `json:"id"`
	Title          string                  `json:"title"`
	Category       string                  `json:"category"`
	BudgetCents    int64                   `json:"budget_cents"`
	Spec           string                  `json:"spec"`
	Deliverables   string                  `json:"deliverables"`
	RequiredFormat string                  `json:"required_format"`
	StyleRef       string                  `json:"style_ref"`
	DeadlineAt     int64                   `json:"deadline_at"`
	Items          []model.RequirementItem `json:"items"`
	Status         string                  `json:"status"`
	CreatedAt      int64                   `json:"created_at"`
	Clarifications []string                `json:"clarifications"`
}

// Publish 发单。
//
// 门槛分两档, 这个划分是刻意的:
//   - 硬门槛(拦下来): 标题、类目、说明、预算。缺这四样, 单子挂在大厅里对别人没有意义,
//     发出去也是噪声, 不如当场说清楚差什么。
//   - 软提示(照发, 返回 clarifications): 交付物形态、交付格式、截止时间、风格参考。
//     这些缺了单子仍然可读可报价, 用提示催补比拒稿有用——发单页填到一半被拒,
//     人就直接走了。
func (s *Service) Publish(requesterID string, in Input) (*View, error) {
	title := strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(title); n < titleMinRunes {
		return nil, ErrTitleShort
	} else if n > titleMaxRunes {
		return nil, ErrTitleLong
	}

	category := strings.TrimSpace(in.Category)
	if category == "" || !model.IsValidCategory(category) {
		return nil, ErrCategory
	}

	spec := strings.TrimSpace(in.Spec)
	if n := utf8.RuneCountInString(spec); n < specMinRunes {
		return nil, ErrSpecShort
	} else if n > specMaxRunes {
		return nil, ErrSpecLong
	}

	if in.BudgetCents < budgetMinCents {
		return nil, ErrBudgetLow
	}
	if in.BudgetCents > budgetMaxCents {
		return nil, ErrBudgetHigh
	}

	// 截止时间只接受空或将来。过去的时间不是"已过期", 是填错了。
	if in.DeadlineAt > 0 && in.DeadlineAt <= model.Now() {
		return nil, ErrDeadlinePast
	}

	deliverables := strings.TrimSpace(in.Deliverables)
	if utf8.RuneCountInString(deliverables) > deliverablesMaxRunes {
		return nil, fieldTooLong("交付物形态", deliverablesMaxRunes)
	}
	requiredFormat := strings.TrimSpace(in.RequiredFormat)
	if utf8.RuneCountInString(requiredFormat) > requiredFormatMaxRunes {
		return nil, fieldTooLong("交付格式", requiredFormatMaxRunes)
	}
	styleRef := strings.TrimSpace(in.StyleRef)
	if utf8.RuneCountInString(styleRef) > styleRefMaxRunes {
		return nil, fieldTooLong("风格参考", styleRefMaxRunes)
	}
	samples := strings.TrimSpace(in.Samples)
	if utf8.RuneCountInString(samples) > samplesMaxRunes {
		return nil, fieldTooLong("参考样例", samplesMaxRunes)
	}

	items, err := normalizeItems(in.Items)
	if err != nil {
		return nil, err
	}

	t := &model.Task{
		ID:          s.st.NextID("T"),
		RequesterID: requesterID,
		Title:       title,
		Category:    category,
		BudgetCents: in.BudgetCents,
		Spec:        spec,
		SpecObj: model.TaskSpec{
			Deliverables:   deliverables,
			RequiredFormat: requiredFormat,
			DeadlineAt:     in.DeadlineAt,
			StyleRef:       styleRef,
			Samples:        samples,
			Items:          items,
		},
		Status:    model.TaskOpen,
		CreatedAt: model.Now(),
	}
	// 复用 v1 的规则澄清: 缺哪个必填项就生成哪一句, 前端拿它当"还能补什么"的清单。
	t.Clarifications = model.ClarifySpec(t.SpecObj)

	s.st.PutTask(t)

	// 写库后复核。PutTask 不返回错误(接口如此), 内存模式不会失败,
	// 但 MySQL 侧一旦有约束冲突就只能靠这一步发现——静默成功比报错更难查:
	// 用户以为发出去了, 大厅里却没有, 回头找客服也复现不出来。
	if _, ok := s.st.GetTask(t.ID); !ok {
		return nil, ErrPublishFailed
	}

	return &View{
		ID:             t.ID,
		Title:          t.Title,
		Category:       t.Category,
		BudgetCents:    t.BudgetCents,
		Spec:           t.Spec,
		Deliverables:   t.SpecObj.Deliverables,
		RequiredFormat: t.SpecObj.RequiredFormat,
		StyleRef:       t.SpecObj.StyleRef,
		DeadlineAt:     t.SpecObj.DeadlineAt,
		Items:          t.SpecObj.Items,
		Status:         string(t.Status),
		CreatedAt:      t.CreatedAt,
		Clarifications: t.Clarifications,
	}, nil
}

// MyTasks 我发的需求, 按创建时间倒序。
//
// 与"我接的单"不是一回事: 那是订单(有人接了才有), 这是需求本身——
// 没人接的单只在这里能看见, 大厅里翻两页就找不着了。
func (s *Service) MyTasks(requesterID string) []*model.Task {
	return s.st.ListTasksByRequester(requesterID)
}

// Withdraw 撤回一条还挂在大厅的需求。
//
// 只允许撤 open。一旦有人认领(matched), 这单已经牵进一份订单:
// 单方面撤回会让订单悬空——接单方那边还显示"进行中", 需求方这边单子没了。
// 那种情况要走订单侧的退回/取消, 两条链路各自负责自己的状态。
//
// 判断顺序是先资格后状态: 先问"是不是你发的", 再问"能不能撤"。
// 反过来的话, 别人试你的单号会收到"已经有人接了", 等于泄露了这单的进展。
func (s *Service) Withdraw(requesterID, taskID string) (*model.Task, error) {
	t, ok := s.st.GetTask(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	if t.RequesterID != requesterID {
		return nil, ErrNotRequester
	}
	if t.Status == model.TaskMatched {
		return nil, ErrTaskTaken
	}
	if t.Status != model.TaskOpen {
		return nil, ErrTaskDone
	}

	// 上面那几步只是为了让错误话说得准, 真正定生死的是这一步:
	// 如果这两行之间有人认领了, 这里的条件 UPDATE 会失败, 不会撤掉一个活单。
	if !s.st.WithdrawOpenTask(taskID, requesterID) {
		return nil, ErrWithdrawRace
	}

	// 写库复核。和发单一样: 内存模式不会失败, 但 MySQL 侧一旦被约束挡住,
	// 用户会以为撤成功了, 大厅里单子还在——这种假成功比报错难处理得多。
	nt, ok := s.st.GetTask(taskID)
	if !ok || nt.Status != model.TaskWithdrawn {
		return nil, ErrWithdrawRace
	}
	return nt, nil
}

// normalizeItems 校验并按输入顺序编号。空清单是合法的(发单时不列清单也能发),
// 但一旦列了, 就不能有空项——清单是验收时逐项比对的标尺, 空项会让标尺失效。
func normalizeItems(raw []string) ([]model.RequirementItem, error) {
	cleaned := make([]string, 0, len(raw))
	for _, it := range raw {
		d := strings.TrimSpace(it)
		if d == "" {
			continue // 前端动态加行会留下空行, 空行等于没填
		}
		if utf8.RuneCountInString(d) > itemDescMaxRunes {
			return nil, fieldTooLong("需求项", itemDescMaxRunes)
		}
		cleaned = append(cleaned, d)
	}
	if len(cleaned) > itemsMax {
		return nil, ErrItemTooMany
	}
	out := make([]model.RequirementItem, 0, len(cleaned))
	for i, d := range cleaned {
		out = append(out, model.RequirementItem{
			ID:   fmt.Sprintf("R%d", i+1),
			Desc: d,
		})
	}
	return out, nil
}

// fieldTooLong 生成带字段名的超长错误。用同一句"太长了"会让用户挨个字段找。
func fieldTooLong(label string, max int) *Error {
	return &Error{
		Status: 400,
		Code:   "FIELD_TOO_LONG",
		Msg:    label + "太长了",
		Hint:   fmt.Sprintf("%d 字以内", max),
	}
}
