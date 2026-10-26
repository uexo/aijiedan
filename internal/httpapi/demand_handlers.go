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

	items := s.toMyTasks(tasks)
	WriteOK(w, r, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

// withdrawTask 撤回一条还没人接的需求。
//
// 200 而不是 204: 前端要拿回这条需求的新状态去更新"我发的单"面板,
// 顺带拿到 state_label——让它自己猜"撤回了"该显示成什么, 早晚会对不上。
func (s *Server) withdrawTask(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	t, err := s.Demand.Withdraw(cl.UserID, r.PathValue("id"))
	if err != nil {
		writeDemandErr(w, r, err)
		return
	}
	WriteOK(w, r, s.toMyTasks([]*model.Task{t})[0])
}

// toMyTasks 需求单 → 列表项。「我的单」列表和撤单返回共用这一份映射:
// 各写一遍, 加字段时必漏一个, 表现为"刚撤的单少了类目名"。
//
// 发单的 201 不走这里: 它回的是 demand.View(带 deliverables/items/clarifications
// 那套发单页要用的字段), 是"刚发出去的东西长什么样", 不是"挂在列表里长什么样"。
// 打开次数也就没有出现在 201 里——那会儿它必然是 0, 而成功页上的话术是
// 另取 /tasks/{id}/invite, 那边自带 opens。
// 打开次数一次批量取: 逐条查表在"我发的单"里就是 N 次往返。这一屏最多几十条,
// 一次 in (...) 就够, 而分开查会在列表边界上悄悄变慢, 平时看不出来。
func (s *Server) toMyTasks(ts []*model.Task) []myTask {
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	opens := make(map[string]int, len(ids))
	for _, h := range s.Store.ListInviteHits(ids) {
		opens[h.TaskID]++
	}

	out := make([]myTask, 0, len(ts))
	for _, t := range ts {
		out = append(out, myTask{
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
			OpenCount:    opens[t.ID],
		})
	}
	return out
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
	// OpenCount 这条单的邀请链接被打开过几次。
	//
	// 和 /me/invite-stats 里的"传到了没有"不是一个问题: 那个算的是
	// "24 小时内首次打开算不算传到", 是个比例; 这个是"这条链接有没有人点过", 是个事实。
	// 卡片上要的是后者——0 次的意思是这段话还没发出去, 不是"指标不好"。
	OpenCount int `json:"open_count"`
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
	case model.TaskWithdrawn:
		// 「已撤回」而不是「已关闭」: 关闭听起来像单子做完了,
		// 撤回是没做成——在一列表里混着看, 这两个必须一眼分得开。
		return "已撤回"
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
