package httpapi

import (
	"errors"
	"net/http"

	"zanbuddy/internal/demand"
	"zanbuddy/internal/model"
)

// publishTask 需求方发一条需求。
//
// 201: 发单是真的新建了一条资源(和认领不同, 认领是给已有的单建订单)。
// 返回体直接给需求方此刻要的东西: 单号(拿去核对)、状态、以及"还能补什么"
// 的澄清清单——补全提示不该让他自己去找文档。
func (s *Server) publishTask(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)

	var in demand.Input
	if !DecodeJSON(r, &in) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对，请刷新页面重试")
		return
	}

	v, err := s.Demand.Publish(cl.UserID, in)
	if err != nil {
		writeDemandErr(w, r, err)
		return
	}
	WriteCreated(w, r, v)
}

// myTasks 我发的需求。
//
// 与 /me/orders 分开: 那是"有人接了的单", 这是"我挂出去的需求"。
// 刚发出去还没人接的单只在这里出现——不给这个接口, 需求方发完单就找不到自己的单了。
func (s *Server) myTasks(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	tasks := s.Demand.MyTasks(cl.UserID)

	items := make([]myTask, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, myTask{
			ID:           t.ID,
			Title:        t.Title,
			Category:     t.Category,
			CategoryName: categoryLabel(t.Category),
			BudgetCents:  t.BudgetCents,
			Spec:         t.Spec,
			DeadlineAt:   t.SpecObj.DeadlineAt,
			ItemCount:    len(t.SpecObj.Items),
			Status:       string(t.Status),
			StateLabel:   taskStateLabel(t.Status),
			CreatedAt:    t.CreatedAt,
		})
	}
	WriteOK(w, r, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

// myTask 我发的需求, 列表项。不吐 requester_id / org_id / 翻译缓存这些
// 只有服务端自己用得上的字段。
type myTask struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	CategoryName string `json:"category_name"`
	BudgetCents  int64  `json:"budget_cents"`
	Spec         string `json:"spec"`
	DeadlineAt   int64  `json:"deadline_at"`
	ItemCount    int    `json:"item_count"`
	Status       string `json:"status"`
	StateLabel   string `json:"state_label"`
	CreatedAt    int64  `json:"created_at"`
}

// taskStateLabel 需求单的三态标签。
//
// 需求单和订单是两套状态: 订单有四态(待确认/进行中/待验收/已完成),
// 需求单只有"挂着/有人接/收工"三种。混着用会让需求方在"我发的单"里
// 看到"待确认", 以为还要自己点一下。
func taskStateLabel(st model.TaskStatus) string {
	switch st {
	case model.TaskOpen:
		return "开放中"
	case model.TaskMatched:
		return "已有人接"
	case model.TaskClosed:
		return "已完成"
	default:
		return string(st)
	}
}

func writeDemandErr(w http.ResponseWriter, r *http.Request, err error) {
	var de *demand.Error
	if errors.As(err, &de) {
		WriteErrField(w, r, de.Status, de.Code, de.Msg, "", de.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}
