// endorsement_handlers.go 背书端点（契约 §3 表「背书」四行）。
//
// 这一层只做三件事：取身份、解 JSON、写信封。所有规则（12 字、三问、只能给
// 完成的单写、一人一单一条）都在 trade 层，因为同一套规则以后还要给
// 开放 API 和小程序用——写两遍必然漂。
package httpapi

import (
	"net/http"

	"zanbuddy/internal/model"
)

// endorsementView 一条背书的对外形状。
//
// 为什么不直接把 model.Endorsement 序列化出去：
// AuthorID / TargetID 是内部 ID，前端要的是名字；weight 是内部规则，
// 露给用户会引出"为什么我这条不算分"这种解释不清的问题（那本来就只该留痕）。
type endorsementView struct {
	ID         string            `json:"id"`
	OrderID    string            `json:"order_id"`
	TaskID     string            `json:"task_id"`
	TaskTitle  string            `json:"task_title"`
	AuthorID   string            `json:"author_id"`
	AuthorName string            `json:"author_name"`
	TargetID   string            `json:"target_id"`
	TargetName string            `json:"target_name"`
	Relation   string            `json:"relation"`
	Body       string            `json:"body"`
	Answers    map[string]string `json:"answers"`
	CharCount  int               `json:"char_count"`
	Status     string            `json:"status"`
	CreatedAt  int64             `json:"created_at"`
	Appends    []appendView      `json:"appends"`
}

type appendView struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) endorsementViewOf(e *model.Endorsement, withAppends bool) endorsementView {
	v := endorsementView{
		ID:        e.ID,
		OrderID:   e.OrderID,
		TaskID:    e.TaskID,
		AuthorID:  e.AuthorID,
		TargetID:  e.TargetID,
		Relation:  e.Relation,
		Body:      e.Body,
		Answers:   e.Answers,
		CharCount: e.CharCount,
		Status:    e.Status,
		CreatedAt: e.CreatedAt,
		Appends:   []appendView{},
	}
	if u, ok := s.Store.GetUser(e.AuthorID); ok {
		v.AuthorName = u.Name
	}
	if u, ok := s.Store.GetUser(e.TargetID); ok {
		v.TargetName = u.Name
	}
	if t, ok := s.Store.GetTask(e.TaskID); ok {
		v.TaskTitle = t.Title
	}
	if withAppends {
		for _, a := range s.Store.ListEndorsementAppends(e.ID) {
			v.Appends = append(v.Appends, appendView{ID: a.ID, Body: a.Body, CreatedAt: a.CreatedAt})
		}
	}
	return v
}

// getEndorsementInvite 写背书之前那屏要的全部信息：写谁、答哪三问、写成过没。
func (s *Server) getEndorsementInvite(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	inv, err := s.Trade.EndorsementInviteOf(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	resp := map[string]interface{}{
		"order_id":       inv.OrderID,
		"task_id":        inv.TaskID,
		"task_title":     inv.TaskTitle,
		"target_id":      inv.TargetID,
		"target_name":    inv.TargetName,
		"relation":       inv.Relation,
		"questions":      inv.Questions,
		"min_chars":      inv.MinChars,
		"max_chars":      inv.MaxChars,
		"answers_needed": inv.AnswersNeeded,
		"can_write":      inv.CanWrite,
		"why_not":        inv.WhyNot,
	}
	if inv.Mine != nil {
		resp["mine"] = s.endorsementViewOf(inv.Mine, true)
	} else {
		resp["mine"] = nil
	}
	WriteOK(w, r, resp)
}

// writeEndorsement 写一条背书。
//
// 201: 背书是一条新记录，而且是一条不可撤销的记录——它该有自己的位置。
func (s *Server) writeEndorsement(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Body    string            `json:"body"`
		Answers map[string]string `json:"answers"`
	}
	if !DecodeJSON(r, &body) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对")
		return
	}

	e, err := s.Trade.WriteEndorsement(r.PathValue("id"), cl.UserID, body.Body, body.Answers)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteCreated(w, r, s.endorsementViewOf(e, true))
}

// appendEndorsement 给一条背书追加内容。只能原作者写，且没有编辑接口。
func (s *Server) appendEndorsement(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Body string `json:"body"`
	}
	if !DecodeJSON(r, &body) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对")
		return
	}

	a, err := s.Trade.AppendEndorsement(r.PathValue("id"), cl.UserID, body.Body)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteCreated(w, r, map[string]interface{}{
		"id":             a.ID,
		"endorsement_id": a.EndorsementID,
		"body":           a.Body,
		"created_at":     a.CreatedAt,
	})
}

// myEndorsements 我收到的和我写出去的。
//
// 收到的那一列只有计权重的：零权重那条（一年内第二次合作）不进列表，
// 但它没丢，只是不计权重——用户看到"少一条"会以为自己被删了，所以下面另给一个总数。
func (s *Server) myEndorsements(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	received, given := s.Trade.MyEndorsements(cl.UserID)

	rv := make([]endorsementView, 0, len(received))
	for _, e := range received {
		rv = append(rv, s.endorsementViewOf(e, true))
	}
	gv := make([]endorsementView, 0, len(given))
	for _, e := range given {
		gv = append(gv, s.endorsementViewOf(e, true))
	}
	WriteOK(w, r, map[string]interface{}{
		"received": rv,
		"given":    gv,
		"total":    len(rv) + len(gv),
	})
}
