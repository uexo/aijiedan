package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"zanbuddy/internal/model"
	"zanbuddy/internal/trade"
)

// 邀请链接：发出去的那条短链，和它打开之后落到的那一页。
//
// 为什么短链单独占一个顶层路径（/t/{id}），而不是 /static/app/take.html?t=xxx：
// 这条 URL 是要被复制到微信里、被人念出来的。长度本身就是可用性——
// 一长串带问号和参数的地址，人不会手打，也不会相信它是"给你的一个活"。

// InviteOpen 把短链入口交给最外层路由（/t/{id} 不在 /api/v2 前缀下）。
//
// 为什么单独开一个导出方法而不是把整个 Routes() 挂在根上：v2 的每条端点都该在
// /api/v2 之下，唯一的例外就是这条要短到能念出来的链接。例外写在一处，读的人看得见。
func (s *Server) InviteOpen() http.Handler {
	return http.HandlerFunc(s.inviteOpen)
}

// inviteOpen 短链入口：记一次打开，然后把浏览器送到认领页。
//
// 埋点放在跳转之前、放在服务端：前端 JS 打点会被拦截器、无痕模式、脚本报错吃掉，
// 而这个数是用来判断"传到了没有"的——它一旦不准，判断就全是错的。
func (s *Server) inviteOpen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Demand.RecordInviteOpen(id)
	// 302 而不是 301：301 会被浏览器永久缓存，同一个链接第二次点就不经过服务端了，
	// 数的第二次打开会丢——而"他点开两次"恰恰说明他在犹豫，是有用的信号。
	http.Redirect(w, r, "/static/app/take.html?t="+id, http.StatusFound)
}

// taskViewer 「点开这条链接的人，现在能不能接」。
//
// 为什么不让他点一下试试: 试出来的答案要付一次被拒的代价, 而这个人此刻
// 对平台的全部了解就是这一页。第一屏就被拒的人不会去看第二屏。
type taskViewer struct {
	LoggedIn bool   `json:"logged_in"`
	Role     string `json:"role"`
	Mine     bool   `json:"mine"`      // 这单是他自己发的
	CanClaim bool   `json:"can_claim"` // 现在点「接这一单」会不会成功
	WhyCode  string `json:"why_code"`  // 不能接的机器码, 前端按它决定给哪个出口
	WhyNot   string `json:"why_not"`   // 不能接的人话原因
	WhyHint  string `json:"why_hint"`  // 该干什么
}

// publicTask 单条需求的公开详情。给认领页用。
//
// 为什么公开：大厅列表本来就是免登录可读的，同一条需求多一个直达入口，
// 不新增任何暴露。挡在登录后面反而会出现"点了链接先看登录页"——
// 收链接的人此刻还不知道这是个什么，让他先注册是把人劝退的最快办法。
//
// 边界：只有 open 的单给全部字段。已经有人接、或者已经收工的单只回
// 单号、标题、状态——那句话是"这单被人接了"，不需要把他写了两千字的需求书也一起给他看。
func (s *Server) publicTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.Store.GetTask(id)
	if !ok {
		WriteErr(w, r, http.StatusNotFound, "TASK_NOT_FOUND", "这条需求不在了，链接可能已经过期")
		return
	}
	v := s.toHallTask(t)
	if t.Status != model.TaskOpen {
		v = hallTask{
			ID:           t.ID,
			Title:        t.Title,
			Status:       string(t.Status),
			StateLabel:   taskStateLabel(t.Status),
			CategoryName: categoryLabel(t.Category),
		}
	}
	v.StateLabel = taskStateLabel(t.Status)

	vw := taskViewer{}
	if cl, ok := Claims(r); ok {
		vw.LoggedIn = true
		vw.Role = cl.Role
		vw.Mine = t.RequesterID == cl.UserID
		// 自己的单先判。ClaimCheck 按"资格先于状态"的顺序走，对发单人返回的是
		// ROLE_NOT_PROVIDER——那句"在登录页选接单方再进来"对他是错的答案：
		// 他本来就是发单的人，而且身份注册后不能改。真实原因只有一句：这是你自己的单。
		if vw.Mine {
			vw.WhyCode = "SELF_CLAIM"
			vw.WhyNot = "这一单是你自己发的，不能自己接"
			vw.WhyHint = "要推进这一单，去「我的单」里看谁接走了"
		} else if err := s.Trade.ClaimCheck(id, cl.UserID); err != nil {
			var te *trade.Error
			if errors.As(err, &te) {
				vw.WhyCode, vw.WhyNot, vw.WhyHint = te.Code, te.Msg, te.Hint
			}
		} else {
			vw.CanClaim = true
		}
	}
	WriteOK(w, r, map[string]interface{}{"task": v, "viewer": vw})
}

// taskInvite 发单方取自己这一单的"发出去"物料：一句话 + 一条短链 + 被打开的情况。
func (s *Server) taskInvite(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	inv, err := s.Demand.InviteOf(r.PathValue("id"), cl.UserID, baseURL(r))
	if err != nil {
		writeDemandErr(w, r, err)
		return
	}
	WriteOK(w, r, inv)
}

// myInviteStats 那个唯一的数：发出去的单，24 小时内有人点进来的比例。
func (s *Server) myInviteStats(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	WriteOK(w, r, s.Demand.InviteStatsOf(cl.UserID))
}

// baseURL 拼对外链接用的站点根。
//
// 不写死配置：演示时是 127.0.0.1:8082，上线是域名，写死一个就会在另一个环境里生成
// 一个点不开的链接——而这条链接是发给外部人的，点不开等于这一单没发。
// X-Forwarded-Proto 优先：放在反向代理后面时 r.TLS 是空的，不认这个头就会拼出 http。
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return scheme + "://" + host
}
