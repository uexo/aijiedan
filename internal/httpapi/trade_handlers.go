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

// writeTradeErr 把交易域的错误翻成信封。未知错误不吐内部细节, 只给 trace。
func writeTradeErr(w http.ResponseWriter, r *http.Request, err error) {
	var te *trade.Error
	if errors.As(err, &te) {
		WriteErrField(w, r, te.Status, te.Code, te.Msg, "", te.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}
