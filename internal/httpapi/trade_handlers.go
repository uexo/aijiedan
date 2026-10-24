package httpapi

import (
	"errors"
	"net/http"

	"zanbuddy/internal/model"
	"zanbuddy/internal/trade"
)

// claimTask 接单方认领一个开放需求。
//
// 鉴权只拦"有没有登录", 角色与档案是否合格由 trade.Claim 判:
// 这些条件以后在开放 API、小程序里同样成立, 写两遍就会漂。
func (s *Server) claimTask(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	o, err := s.Trade.Claim(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteCreated(w, r, s.Trade.View(o, cl.UserID))
}

// myOrders 我参与的单。接单方看自己接的, 需求方看别人接自己的。
func (s *Server) myOrders(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	items := s.Trade.MyOrders(cl.UserID)
	WriteOK(w, r, map[string]interface{}{
		"items": items,
		"total": len(items),
	})
}

// confirmOrder 需求方确认认领, 这一单正式开工。
//
// 200 而不是 201: 订单在认领那一刻就存在了, 这一步改的是状态, 没有新资源。
func (s *Server) confirmOrder(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	o, err := s.Trade.Confirm(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, s.Trade.View(o, cl.UserID))
}

// declineOrder 需求方把单子退回大厅。
//
// 原因可选: 逼人写理由的结果是满屏"不合适"三个字。
// 但给了就得存下来 —— 接单方有权知道为什么没成。
func (s *Server) declineOrder(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Reason string `json:"reason"`
	}
	// 空 body 也允许: 前端不填原因时直接 POST 过来
	_ = DecodeJSON(r, &body)

	o, err := s.Trade.Decline(r.PathValue("id"), cl.UserID, body.Reason)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, s.Trade.View(o, cl.UserID))
}

// deliverOrder 接单方交付。
//
// 201 而不是 200: 交付是一条新记录(第一次交付还带着"这一单进入待验收"),
// 不是把已有资源改个字段。
//
// 返回的是订单视图本身, 而不是交付记录: 前端拿它直接替换列表里的那一行,
// 状态、轮次、最近一轮内容都在里面, 不用再拉一次 /me/orders。
func (s *Server) deliverOrder(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Content string `json:"content"`
		Link    string `json:"link"`
		Items   []struct {
			ItemID string `json:"item_id"`
			Done   bool   `json:"done"`
			Note   string `json:"note"`
			Link   string `json:"link"`
		} `json:"items"`
	}
	if !DecodeJSON(r, &body) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对")
		return
	}

	items := make([]model.DeliveryItem, 0, len(body.Items))
	for _, it := range body.Items {
		items = append(items, model.DeliveryItem{ItemID: it.ItemID, Done: it.Done, Note: it.Note, Link: it.Link})
	}

	o, _, err := s.Trade.Deliver(r.PathValue("id"), cl.UserID, body.Content, body.Link, items)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteCreated(w, r, s.Trade.View(o, cl.UserID))
}

// listDeliveries 这一单的交付记录(含历史轮次), 只有交易双方能看。
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	items, err := s.Trade.Deliveries(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, map[string]interface{}{"items": items, "total": len(items)})
}

// acceptOrder 需求方验收通过。
//
// 200 而不是 201: 订单早就存在, 这一步只改状态, 没有新资源。
func (s *Server) acceptOrder(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	o, err := s.Trade.Accept(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, s.Trade.View(o, cl.UserID))
}

// reviseOrder 需求方打回, 让接单方改一版。
//
// 原因必填: 没有原因的打回等于"我不满意", 接单方下一轮只能瞎猜。
func (s *Server) reviseOrder(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Reason string `json:"reason"`
	}
	if !DecodeJSON(r, &body) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对")
		return
	}

	o, err := s.Trade.Revise(r.PathValue("id"), cl.UserID, body.Reason)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, s.Trade.View(o, cl.UserID))
}

// writeTradeErr 把交易域的错误翻成信封。未知错误不吐内部细节, 只给 trace。
func writeTradeErr(w http.ResponseWriter, r *http.Request, err error) {
	var te *trade.Error
	if errors.As(err, &te) {
		WriteErrField(w, r, te.Status, te.Code, te.Msg, "", te.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}

// reviewView 一条评价的对外形状。
//
// Revealed=false 时前端只该拿它来显示"你已提交，等对方"，
// 所以这里对未揭盲的那条仍然给出 rating——它是评价人自己写的，给他看没问题。
// 对方未揭盲的评价在 listReviews 里根本不会出现。
type reviewView struct {
	ID           string `json:"id"`
	OrderID      string `json:"order_id"`
	Rating       int    `json:"rating"`
	Comment      string `json:"comment"`
	ReviewerRole string `json:"reviewer_role"`
	ReviewerName string `json:"reviewer_name"`
	CreatedAt    int64  `json:"created_at"`
	Revealed     bool   `json:"revealed"`
	Mine         bool   `json:"mine"`
}

func (s *Server) reviewViewOf(rv *model.Review, viewerID string) reviewView {
	v := reviewView{
		ID:           rv.ID,
		OrderID:      rv.OrderID,
		Rating:       rv.Rating,
		Comment:      rv.Comment,
		ReviewerRole: string(rv.ReviewerRole),
		CreatedAt:    rv.CreatedAt,
		Revealed:     model.ReviewVisible(rv, model.Now()),
		Mine:         rv.ReviewerID == viewerID,
	}
	if u, ok := s.Store.GetUser(rv.ReviewerID); ok {
		v.ReviewerName = u.Name
	}
	return v
}

// submitReview 提交互评。
//
// 为什么互评放在订单下而不是新建一个 /reviews 资源:
// 一条评价脱离订单没有意义——它评的是"这一次交付", 不是"这个人"。
// 挂在订单下, 找评价只需要订单号, 也不会出现"评了一个不存在的单"。
func (s *Server) submitReview(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var body struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if !DecodeJSON(r, &body) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_JSON", "请求格式不对")
		return
	}

	rv, err := s.Trade.SubmitReview(r.PathValue("id"), cl.UserID, body.Rating, body.Comment)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}

	// 顺手带一句"现在能不能看见对方的": 用户评完最想知道的是这个,
	// 让他再发一次请求去问, 等于把一次交互拆成两次。
	st, _ := s.Trade.ListOrderReviews(r.PathValue("id"), cl.UserID)
	resp := map[string]interface{}{
		"review": s.reviewViewOf(rv, cl.UserID),
	}
	if st != nil {
		resp["counterpart_submitted"] = st.CounterpartDid
		resp["theirs"] = toReviewViews(s, st.Theirs, cl.UserID)
	}
	WriteOK(w, r, resp)
}

// listReviews 一单上的评价与揭盲状态。只有参与方能看。
func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	st, err := s.Trade.ListOrderReviews(r.PathValue("id"), cl.UserID)
	if err != nil {
		writeTradeErr(w, r, err)
		return
	}
	WriteOK(w, r, map[string]interface{}{
		"mine":                  toReviewViews(s, st.Mine, cl.UserID),
		"theirs":                toReviewViews(s, st.Theirs, cl.UserID),
		"counterpart_submitted": st.CounterpartDid,
		"can_write":             st.MineCanWrite,
		"auto_reveal_days":      model.ReviewAutoRevealDays,
	})
}

func toReviewViews(s *Server, rs []*model.Review, viewerID string) []reviewView {
	out := make([]reviewView, 0, len(rs))
	for _, rv := range rs {
		out = append(out, s.reviewViewOf(rv, viewerID))
	}
	return out
}
