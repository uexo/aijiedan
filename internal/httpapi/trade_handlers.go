package httpapi

import (
	"errors"
	"net/http"

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

// writeTradeErr 把交易域的错误翻成信封。未知错误不吐内部细节, 只给 trace。
func writeTradeErr(w http.ResponseWriter, r *http.Request, err error) {
	var te *trade.Error
	if errors.As(err, &te) {
		WriteErrField(w, r, te.Status, te.Code, te.Msg, "", te.Hint)
		return
	}
	WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题，稍后再试")
}
