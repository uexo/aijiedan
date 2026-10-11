// Package handler 实现HiveGet M0 的 HTTP 层: REST API + 合规拦截(409) + IM 的 SSE 推送 + 运营控制台。
// 仅依赖标准库(net/http), 不引入外部路由/框架, 便于零依赖编译。
package handler

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"hiveget/internal/auth"
	"hiveget/internal/clarify"
	"hiveget/internal/compliance"
	"hiveget/internal/middleware"
	"hiveget/internal/model"
	"hiveget/internal/service"
	"hiveget/internal/store"
)

//go:embed console.html
var consoleHTML []byte

//go:embed mobile.html
var mobileHTML []byte

// Handler 持有业务服务
type Handler struct {
	Svc *service.Service
}

func New(svc *service.Service) *Handler { return &Handler{Svc: svc} }

// ---- 响应封装 ----

type envelope struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

type blockEnvelope struct {
	Code    int                     `json:"code"`
	Msg     string                  `json:"msg"`
	Missing []model.ComplianceItem  `json:"missing"`
	CardRef string                  `json:"card_ref"`
}

type settleBlockEnvelope struct {
	Code    int                    `json:"code"`
	Msg     string                 `json:"msg"`
	Order   *model.Order           `json:"order"`
	Missing []model.ComplianceItem `json:"missing"`
	CardRef string                 `json:"card_ref"`
}

func (h *Handler) writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeBlock(w http.ResponseWriter, b *compliance.Block) {
	h.writeJSON(w, http.StatusConflict, blockEnvelope{
		Code:    409,
		Msg:     b.Reason,
		Missing: b.Missing,
		CardRef: b.CardRef,
	})
}

// ---- 鉴权辅助 ----

func (h *Handler) claims(r *http.Request) (*middleware.Claims, bool) {
	raw, ok := middleware.ExtractToken(r.Header.Get("Authorization"))
	if !ok {
		return nil, false
	}
	if strings.HasPrefix(raw, "hgk_") {
		// 组织开放 API 凭证(Bearer hgk_xxx) → 映射为组织 owner 身份, 供开放 API 调用
		return h.Svc.ResolveAPIKey(raw)
	}
	c, err := middleware.Parse(raw)
	if err != nil {
		return nil, false
	}
	return c, true
}

func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (*middleware.Claims, bool) {
	c, ok := h.claims(r)
	if !ok {
		h.writeJSON(w, http.StatusUnauthorized, envelope{Code: 401, Msg: "未登录或 token 无效"})
		return nil, false
	}
	return c, true
}

// optionalAuth 不强制登录: 有有效 token 则返回 claims, 否则返回 (nil,false) 但不写响应。
func (h *Handler) optionalAuth(r *http.Request) (*middleware.Claims, bool) {
	return h.claims(r)
}

// ---- 路由分发 ----

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 根路径: 运营控制台(交互式前端, 内嵌 console.html)
	if r.URL.Path == "/" || r.URL.Path == "" {
		h.landing(w, r)
		return
	}
	if r.URL.Path == "/m" {
		h.mobile(w, r)
		return
	}
	if r.URL.Path == "/console" {
		h.console(w, r)
		return
	}
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 3 || segs[0] != "api" || segs[1] != "v1" {
		h.writeJSON(w, http.StatusNotFound, envelope{Code: 404, Msg: "not found"})
		return
	}
	switch segs[2] {
	case "health":
		if r.Method == http.MethodGet {
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]string{"service": "hiveget-m0"}})
			return
		}
	case "auth":
		if r.Method == http.MethodPost {
			switch segs[3] {
			case "login":
				h.login(w, r)
				return
			case "register":
				h.register(w, r)
				return
			}
		}
		if r.Method == http.MethodGet && len(segs) >= 4 && segs[3] == "me" {
			if len(segs) >= 5 && segs[4] == "workbench" {
				h.handleWorkbench(w, r)
				return
			}
			h.me(w, r)
			return
		}
	case "tasks":
		h.handleTasks(w, r, segs)
		return
	case "matches":
		h.handleMatches(w, r, segs)
		return
	case "orders":
		h.handleOrders(w, r, segs)
		return
	case "im":
		h.handleIM(w, r, segs)
		return
	case "compliance":
		h.handleCompliance(w, r)
		return
	case "readiness":
		if r.Method == http.MethodGet {
			h.handleReadiness(w, r)
			return
		}
	case "clarify":
		h.handleClarify(w, r, segs)
		return
	case "wallet":
		h.handleWallet(w, r, segs)
		return
	case "notifications":
		h.handleNotifications(w, r, segs)
		return
	case "orgs":
		h.handleOrgs(w, r, segs)
		return
	case "users":
		h.handleUsers(w, r, segs)
		return
	case "meta":
		h.handleMeta(w, r, segs)
		return
	case "recommend":
		h.handleRecommend(w, r, segs)
		return
	case "kyc":
		h.handleKYC(w, r, segs)
		return
	case "payments":
		h.handlePayments(w, r, segs)
		return
	case "admin":
		h.handleAdmin(w, r, segs)
		return
	}
	h.writeJSON(w, http.StatusNotFound, envelope{Code: 404, Msg: "not found"})
}

// ---- Marketplace 首页(根路径 /) ----
// index.html 为单文件 SPA(需求大厅: 搜索→推荐→画像闭环), 由 web/ 经 /static/ 提供; 此处重定向。

func (h *Handler) landing(w http.ResponseWriter, r *http.Request) {
	// 新版玻璃拟态落地页(由 web/landing.html 经 /static/ 提供)。
	http.Redirect(w, r, "/static/landing.html", http.StatusFound)
}

// console 运营后台入口: 重定向到新版运营后台(由 web/admin.html 经 /static/ 提供)。
func (h *Handler) console(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/static/admin.html", http.StatusFound)
}

// mobile 提供移动端 H5 核心页(P1-3): 大厅/发单/订单/我的, 复用同一套 REST API。
func (h *Handler) mobile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mobileHTML)
}


// ---- auth ----

// login 支持两种方式:
//  1) uid 登录(演示/存量账号, 如 R001/P001/A001)
//  2) account+password 登录(真实注册用户)
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID      string `json:"uid"`
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// 账号密码登录
	if body.Account != "" && body.Password != "" {
		u, ok := h.Svc.Store.GetUserByAccount(body.Account)
		if !ok {
			h.writeJSON(w, 401, envelope{Code: 401, Msg: "账号不存在"})
			return
		}
		if !auth.VerifyPassword(u.PasswordHash, body.Password) {
			h.writeJSON(w, 401, envelope{Code: 401, Msg: "密码错误"})
			return
		}
		h.issueToken(w, u)
		return
	}

	// uid 演示登录
	if body.UID == "" {
		h.writeJSON(w, 400, envelope{Code: 400, Msg: "uid 或 account+password 必填"})
		return
	}
	u, ok := h.Svc.Store.GetUser(body.UID)
	if !ok {
		h.writeJSON(w, 400, envelope{Code: 400, Msg: "用户不存在: " + body.UID})
		return
	}
	h.issueToken(w, u)
}

func (h *Handler) issueToken(w http.ResponseWriter, u *model.User) {
	token, err := middleware.Sign(u.ID, string(u.Role))
	if err != nil {
		h.writeJSON(w, 500, envelope{Code: 500, Msg: "token 签发失败"})
		return
	}
	h.writeJSON(w, 200, envelope{Code: 200, Msg: "登录成功", Data: map[string]string{
		"token": token, "uid": u.ID, "role": string(u.Role), "name": u.Name, "account": u.Account,
	}})
}

// register 真实用户注册: 账号+密码+角色, 可选真实姓名/手机号。
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Account  string `json:"account"`
		Password string `json:"password"`
		Role     string `json:"role"`
		RealName string `json:"real_name"`
		Phone    string `json:"phone"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	res := h.Svc.Register(body.Account, body.Password, body.Role, body.RealName, body.Phone)
	if res.Err != "" {
		h.writeJSON(w, 400, envelope{Code: 400, Msg: res.Err})
		return
	}
	// 注册成功直接签发 token, 免二次登录
	token, err := middleware.Sign(res.User.ID, string(res.User.Role))
	if err != nil {
		h.writeJSON(w, 500, envelope{Code: 500, Msg: "token 签发失败"})
		return
	}
	h.writeJSON(w, 201, envelope{Code: 201, Msg: "注册成功", Data: map[string]interface{}{
		"token": token, "uid": res.User.ID, "role": string(res.User.Role), "name": res.User.Name, "account": res.User.Account,
	}})
}

// me 返回当前登录用户身份与信用分, 供 H5 钱包页展示。
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	u, ok := h.Svc.Store.GetUser(c.UserID)
	if !ok {
		h.writeJSON(w, http.StatusNotFound, envelope{Code: 404, Msg: "用户不存在"})
		return
	}
	h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]interface{}{
		"uid":          u.ID,
		"role":         string(u.Role),
		"name":         u.Name,
		"account":      u.Account,
		"credit_score": u.CreditScore,
	}})
}

// handleWorkbench 我的工作台聚合视图: GET /api/v1/me/workbench
func (h *Handler) handleWorkbench(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	wb, err := h.Svc.GetWorkbench(c.UserID)
	if err != nil {
		h.writeJSON(w, 404, envelope{Code: 404, Msg: err.Error()})
		return
	}
	h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: wb})
}

// ---- tasks ----

func (h *Handler) handleTasks(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 3 && r.Method == http.MethodGet: // 需求大厅(支持检索/筛选/排序)
		q := r.URL.Query()
		f := store.TaskFilter{
			Category:       q.Get("category"),
			Status:         q.Get("status"),
			Q:              q.Get("q"),
			Skill:          q.Get("skill"),
			DeadlineAfter:  atoi64(q.Get("deadline_after")),
			DeadlineBefore: atoi64(q.Get("deadline_before")),
			Sort:           q.Get("sort"),
			MinBudget:      atoi64(q.Get("min_budget")),
			MaxBudget:      atoi64(q.Get("max_budget")),
			Limit:          int(atoi64(q.Get("limit"))),
		}
		// 相关度排序需要接单方身份(用其技能画像给需求打分); 匿名访问则按最新/预算等常规排序。
		providerID := ""
		if c, ok := h.optionalAuth(r); ok && c.Role == string(model.RoleProvider) {
			providerID = c.UserID
		}
		tasks := h.Svc.SearchTasks(providerID, f)
		h.writeJSON(w, 200, envelope{Code: 200, Data: tasks})
	case len(segs) == 3 && r.Method == http.MethodPost: // 发单
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		if c.Role != string(model.RoleRequester) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅需求方可发单"})
			return
		}
		var body struct {
			Title          string                 `json:"title"`
			Category       string                 `json:"category"`
			BudgetCents    int64                  `json:"budget_cents"`
			Spec           string                 `json:"spec"`
			Deliverables   string                 `json:"deliverables"`
			RequiredFormat string                 `json:"required_format"`
			DeadlineAt     int64                  `json:"deadline_at"`
			StyleRef       string                 `json:"style_ref"`
			Samples        string                 `json:"samples"`
			Items          []model.RequirementItem `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// 服务端为需求清单项分配稳定 ID(R1,R2,...), 便于交付/验收逐项对齐。
		items := make([]model.RequirementItem, 0, len(body.Items))
		for _, it := range body.Items {
			if strings.TrimSpace(it.Desc) == "" {
				continue
			}
			id := it.ID
			if id == "" {
				id = fmt.Sprintf("R%d", len(items)+1)
			}
			items = append(items, model.RequirementItem{ID: id, Desc: it.Desc})
		}
		specObj := model.TaskSpec{
			Deliverables:   body.Deliverables,
			RequiredFormat: body.RequiredFormat,
			DeadlineAt:     body.DeadlineAt,
			StyleRef:       body.StyleRef,
			Samples:        body.Samples,
			Items:          items,
		}
		res := h.Svc.PublishTask(c.UserID, body.Title, body.Category, body.BudgetCents, body.Spec, specObj)
		if res.Block != nil {
			h.writeBlock(w, res.Block)
			return
		}
		h.writeJSON(w, 201, envelope{Code: 201, Msg: "发单成功", Data: map[string]interface{}{
			"task":            res.Task,
			"spec_complete":   len(model.RequiredClarifySpec(specObj)) == 0,
			"clarifications":  res.Clarifications,
		}})
	case len(segs) == 4 && r.Method == http.MethodGet: // 需求详情(聚合发布者画像+关联订单)
		d, err := h.Svc.GetTaskDetail(segs[3])
		if err != nil {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Data: d})
	case len(segs) == 5 && segs[4] == "match" && r.Method == http.MethodPost: // AI撮合
		recs := h.Svc.MatchTask(segs[3])
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "撮合完成", Data: recs})
	case len(segs) == 5 && segs[4] == "take" && r.Method == http.MethodPost: // 主动接单(浏览大厅→认领)
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		if c.Role != string(model.RoleProvider) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅接单方可接单"})
			return
		}
		var body struct {
			Milestones []model.Milestone `json:"milestones"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res := h.Svc.TakeTask(segs[3], c.UserID, body.Milestones)
		if res.Block != nil {
			h.writeBlock(w, res.Block)
			return
		}
		h.writeJSON(w, 201, envelope{Code: 201, Msg: "接单成功, 订单待托管", Data: res.Order})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// ---- matches ----

func (h *Handler) handleMatches(w http.ResponseWriter, r *http.Request, segs []string) {
	if len(segs) == 5 && segs[4] == "accept" && r.Method == http.MethodPost {
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		if c.Role != string(model.RoleProvider) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅接单方可应征"})
			return
		}
		var body struct {
			Milestones []model.Milestone `json:"milestones"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res := h.Svc.AcceptMatch(segs[3], c.UserID, body.Milestones)
		if res.Block != nil {
			h.writeBlock(w, res.Block)
			return
		}
		h.writeJSON(w, 201, envelope{Code: 201, Msg: "接单成功, 订单待托管", Data: res.Order})
		return
	}
	h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
}

// ---- orders ----

func (h *Handler) handleOrders(w http.ResponseWriter, r *http.Request, segs []string) {
	if len(segs) == 7 && segs[4] == "milestones" && segs[6] == "confirm" && r.Method == http.MethodPost {
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		res := h.Svc.ConfirmMilestone(segs[3], c.UserID, segs[5])
		if res.Block != nil {
			h.writeBlock(w, res.Block)
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "里程碑已确认放款", Data: res.Order})
		return
	}
	if len(segs) == 3 && r.Method == http.MethodGet { // 订单列表(按角色隔离)
		c, ok := h.claims(r)
		var orders []*model.Order
		if !ok {
			// 未登录: 不泄露任何订单, 返回空列表
			orders = []*model.Order{}
		} else if c.Role == string(model.RoleAdmin) {
			orders = h.Svc.Store.ListOrders() // 运营可看全量
		} else {
			orders = h.Svc.Store.ListOrdersByUser(c.UserID) // 普通用户只看自己的
		}
		h.writeJSON(w, 200, envelope{Code: 200, Data: orders})
		return
	}
	if len(segs) == 4 && r.Method == http.MethodGet { // 订单详情(含任务/匹配与交付)
		o, ok := h.Svc.Store.GetOrder(segs[3])
		if !ok {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: "订单不存在"})
			return
		}
		task, _ := h.Svc.Store.GetTask(o.TaskID)
		matches := h.Svc.Store.ListMatchesByTask(o.TaskID)
		deliveries := h.Svc.Store.ListDeliveriesByOrder(o.ID)
		h.writeJSON(w, 200, envelope{Code: 200, Data: map[string]interface{}{
			"order":      o,
			"task":       task,
			"matches":    matches,
			"deliveries": deliveries,
		}})
		return
	}
	if len(segs) == 5 {
		if r.Method == http.MethodGet && segs[4] == "reviews" { // 查看订单评价
			o, ok := h.Svc.Store.GetOrder(segs[3])
			if !ok {
				h.writeJSON(w, 404, envelope{Code: 404, Msg: "订单不存在"})
				return
			}
			reviews := h.Svc.Store.ListReviewsByOrder(segs[3])
			revieweeID := o.ProviderID
			reviewee, _ := h.Svc.Store.GetUser(revieweeID)
			h.writeJSON(w, 200, envelope{Code: 200, Data: map[string]interface{}{
				"order_id":      segs[3],
				"reviews":       reviews,
				"reviewee_id":   revieweeID,
				"reviewee_credit": func() int {
					if reviewee != nil {
						return reviewee.CreditScore
					}
					return 0
				}(),
			}})
			return
		}
		if r.Method == http.MethodPost {
			switch segs[4] {
			case "escrow": // 创建托管(闸门: KYC+税源+支付)
				res := h.Svc.CreateEscrow(segs[3])
				if res.Block != nil {
					h.writeBlock(w, res.Block)
					return
				}
				h.writeJSON(w, 200, envelope{Code: 200, Msg: "托管成功, 订单进行中", Data: res.Order})
			case "deliver": // 提交交付
				c, ok := h.requireAuth(w, r)
				if !ok {
					return
				}
				var body struct {
					Content string                 `json:"content"`
					Items   []model.DeliveryItem   `json:"items"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				d, b := h.Svc.SubmitDelivery(segs[3], c.UserID, body.Content, body.Items)
				if b != nil {
					h.writeBlock(w, b)
					return
				}
				h.writeJSON(w, 201, envelope{Code: 201, Msg: "交付已提交, 待验收", Data: d})
			case "accept": // 验收(并触发结算闸门)
				c, ok := h.requireAuth(w, r)
				if !ok {
					return
				}
				var body struct {
					Acceptance     []model.AcceptanceItem `json:"acceptance"`
					SevereMismatch bool                   `json:"severe_mismatch"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				res := h.Svc.AcceptDelivery(segs[3], c.UserID, body.Acceptance, body.SevereMismatch)
				if res.Block != nil {
					h.writeJSON(w, http.StatusConflict, settleBlockEnvelope{
						Code: 409, Msg: "验收通过, 但结算被合规拦截(订单转待结算): " + res.Block.Reason,
						Order: res.Order, Missing: res.Block.Missing, CardRef: res.Block.CardRef,
					})
					return
				}
				h.writeJSON(w, 200, envelope{Code: 200, Msg: "验收完成, 已结算放款", Data: res.Order})
			case "settle": // 合规落地后补结算(订单处于 pending_settlement 时重试)
				o, ok := h.Svc.Store.GetOrder(segs[3])
				if !ok {
					h.writeJSON(w, 404, envelope{Code: 404, Msg: "订单不存在"})
					return
				}
				res := h.Svc.Settle(o)
				if res.Block != nil {
					h.writeBlock(w, res.Block)
					return
				}
				h.writeJSON(w, 200, envelope{Code: 200, Msg: "补结算成功, 已放款", Data: res.Order})
			case "review": // 评价(验收完成后双向互评之一)
				c, ok := h.requireAuth(w, r)
				if !ok {
					return
				}
				var body struct {
					Rating  int    `json:"rating"`
					Comment string `json:"comment"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				res := h.Svc.SubmitReview(segs[3], c.UserID, body.Rating, body.Comment)
				if res.Err != "" {
					// 区分状态: 订单未完成/重复评价/权限/参数
					code := 400
					o, oerr := h.Svc.Store.GetOrder(segs[3])
					if oerr && o.Status != model.OrderCompleted {
						code = 409
					}
					h.writeJSON(w, code, envelope{Code: code, Msg: res.Err})
					return
				}
			h.writeJSON(w, 201, envelope{Code: 201, Msg: "评价成功, 已更新对方信用分", Data: map[string]interface{}{
				"review":          res.Review,
				"reviewee_id":     res.Reviewee.ID,
				"reviewee_credit": res.Reviewee.CreditScore,
			}})
		case "revise": // 需求方打回重做(仅 delivered 状态)
			c, ok := h.requireAuth(w, r)
			if !ok {
				return
			}
			var body struct {
				Note string `json:"note"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if b := h.Svc.RequestRevision(segs[3], c.UserID, body.Note); b != nil {
				h.writeBlock(w, b)
				return
			}
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "已打回重做, 接单方可再次交付"})
		case "cancel": // 取消订单(参与方; 按比例退款)
			c, ok := h.requireAuth(w, r)
			if !ok {
				return
			}
			o, oerr := h.Svc.Store.GetOrder(segs[3])
			if !oerr {
				h.writeJSON(w, 404, envelope{Code: 404, Msg: "订单不存在"})
				return
			}
			if c.UserID != o.RequesterID && c.UserID != o.ProviderID {
				h.writeJSON(w, 403, envelope{Code: 403, Msg: "非订单参与方, 无权取消"})
				return
			}
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res := h.Svc.CancelOrder(segs[3], c.UserID, body.Reason)
			if res.Block != nil {
				h.writeBlock(w, res.Block)
				return
			}
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "订单已取消, 已按比例退款", Data: res.Order})
		case "dispute": // 发起纠纷(参与方)
			c, ok := h.requireAuth(w, r)
			if !ok {
				return
			}
			o, oerr := h.Svc.Store.GetOrder(segs[3])
			if !oerr {
				h.writeJSON(w, 404, envelope{Code: 404, Msg: "订单不存在"})
				return
			}
			if c.UserID != o.RequesterID && c.UserID != o.ProviderID {
				h.writeJSON(w, 403, envelope{Code: 403, Msg: "非订单参与方, 无权发起纠纷"})
				return
			}
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res := h.Svc.OpenDispute(segs[3], c.UserID, body.Reason)
			if res.Block != nil {
				h.writeBlock(w, res.Block)
				return
			}
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "纠纷已发起, 等待运营仲裁", Data: res.Order})
		case "resolve": // 运营仲裁结案(admin)
			c, ok := h.requireAuth(w, r)
			if !ok {
				return
			}
			if c.Role != string(model.RoleAdmin) {
				h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅运营可仲裁结案"})
				return
			}
			var body struct {
				RefundPct int    `json:"refund_pct"`
				Decision  string `json:"decision"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res := h.Svc.ResolveDispute(segs[3], c.UserID, body.RefundPct, body.Decision)
			if res.Block != nil {
				h.writeBlock(w, res.Block)
				return
			}
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "纠纷已结案", Data: res.Order})
		default:
			h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
		}
			return
		}
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
		return
	}
	h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
}

// ---- IM (SSE) ----

func (h *Handler) handleIM(w http.ResponseWriter, r *http.Request, segs []string) {
	if r.Method == http.MethodPost && len(segs) == 3 { // 发送消息
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		var body struct {
			To   string `json:"to"`
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		msg, reason := h.Svc.SendMessage(c.UserID, body.To, body.Body)
		if msg == nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: reason})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "已发送", Data: msg})
		return
	}
	if r.Method == http.MethodGet && len(segs) == 3 { // SSE 实时流(首屏历史 + 实时推送)
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		peer := r.URL.Query().Get("peer")
		if peer == "" {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: "peer 必填"})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			h.writeJSON(w, 500, envelope{Code: 500, Msg: "不支持 SSE"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		_, ch := h.Svc.IM.Subscribe(c.UserID, peer)
		for _, m := range h.Svc.IM.Recent(c.UserID, peer) {
			h.writeSSE(w, flusher, m)
		}
		for {
			select {
			case m := <-ch:
				h.writeSSE(w, flusher, m)
			case <-time.After(30 * time.Second):
				h.writeSSE(w, flusher, "ping")
			case <-r.Context().Done():
				return
			}
		}
	}
	if r.Method == http.MethodGet && len(segs) == 4 && segs[3] == "conversations" { // 会话列表(含未读)
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		convs := h.Svc.ListConversations(c.UserID)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: convs})
		return
	}
	if r.Method == http.MethodGet && len(segs) == 5 && segs[3] == "conversations" { // 会话历史
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		msgs := h.Svc.GetMessages(c.UserID, segs[4])
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: msgs})
		return
	}
	if r.Method == http.MethodPost && len(segs) == 6 && segs[3] == "conversations" && segs[5] == "read" { // 标记已读
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		n := h.Svc.MarkRead(c.UserID, segs[4])
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "已读", Data: map[string]int{"cleared": n}})
		return
	}
	h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
}

func (h *Handler) writeSSE(w http.ResponseWriter, f http.Flusher, v interface{}) {
	b, _ := json.Marshal(v)
	_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
	f.Flush()
}

// ---- clarify (P1-2 AI 澄清) ----
// 路由: POST /api/v1/clarify (start) | /api/v1/clarify/{id} (continue) |
//       /api/v1/clarify/{id}/extract | /api/v1/clarify/{id}/publish
func (h *Handler) handleClarify(w http.ResponseWriter, r *http.Request, segs []string) {
	if r.Method != http.MethodPost {
		h.writeJSON(w, 405, envelope{Code: 405, Msg: "method not allowed"})
		return
	}
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	if c.Role != string(model.RoleRequester) {
		h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅需求方可使用 AI 澄清发单"})
		return
	}

	switch {
	case len(segs) == 3: // 发起澄清会话
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s := h.Svc.Clarify.Start(c.UserID, strings.TrimSpace(body.Text))
		h.writeClarify(w, s, s.FirstReply, 201)
	case len(segs) == 4: // 继续多轮对话
		s, err := h.Svc.Clarify.Continue(segs[3], readClarifyText(r))
		if err != nil {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: err.Error()})
			return
		}
		h.writeClarify(w, s, s.FirstReply, 200)
	case len(segs) == 5 && segs[4] == "extract": // 重新抽取结构化
		s, err := h.Svc.Clarify.Extract(segs[3])
		if err != nil {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: err.Error()})
			return
		}
		h.writeClarify(w, s, "", 200)
	case len(segs) == 5 && segs[4] == "publish": // 发布澄清后的需求
		s, ok := h.Svc.Clarify.Get(segs[3])
		if !ok {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: "会话不存在: " + segs[3]})
			return
		}
		res := h.Svc.PublishTask(c.UserID, s.Draft.Title, s.Draft.Category, s.Draft.BudgetCents, s.Draft.Spec, s.Draft.ToSpec())
		if res.Block != nil {
			h.writeBlock(w, res.Block)
			return
		}
		h.writeJSON(w, 201, envelope{Code: 201, Msg: "AI 澄清后发布成功", Data: map[string]interface{}{
			"task":           res.Task,
			"spec_complete":  len(model.RequiredClarifySpec(s.Draft.ToSpec())) == 0,
			"clarifications": res.Clarifications,
		}})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

func readClarifyText(r *http.Request) string {
	var body struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return strings.TrimSpace(body.Text)
}

func (h *Handler) writeClarify(w http.ResponseWriter, s *clarify.Session, reply string, code int) {
	spec := s.Draft.ToSpec()
	clar := model.ClarifySpec(spec) // 含可选风格建议, 用于 UI 引导
	// spec_complete 只看必填项, 不受可选风格建议影响
	h.writeJSON(w, code, envelope{Code: code, Msg: "ok", Data: map[string]interface{}{
		"session_id":    s.ID,
		"reply":         reply,
		"draft":         s.Draft,
		"clarifications": clar,
		"spec_complete": len(model.RequiredClarifySpec(spec)) == 0,
		"last_err":      s.LastErr,
	}})
}

// ---- compliance (admin) ----

func (h *Handler) handleCompliance(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && len(strings.Split(strings.Trim(r.URL.Path, "/"), "/")) == 3 {
		st := h.Svc.Compliance.GetState()
		h.writeJSON(w, 200, envelope{Code: 200, Data: st})
		return
	}
	if r.Method == http.MethodPost && len(strings.Split(strings.Trim(r.URL.Path, "/"), "/")) == 3 {
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		if c.Role != string(model.RoleAdmin) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅运营可操作合规开关"})
			return
		}
		var body struct {
			Item string `json:"item"`
			On   bool   `json:"on"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.Svc.Compliance.Set(model.ComplianceItem(body.Item), body.On)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "合规项已更新", Data: h.Svc.Compliance.GetState()})
		return
	}
	h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
}

// ---- readiness (上线就绪态 / 真实方案2 checklist) ----
// GET /api/v1/readiness 返回生产就绪态: 资金通道 / KYC 通道 / ICP 备案 / 合规四门 / 存储模式。
// 仅暴露布尔与「缺失的环境变量名」, 不回显任何密钥值; 运营后台「上线就绪」面板据此渲染 checklist。
func (h *Handler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	stMode := "memory"
	if os.Getenv("HIVEGET_DB_DSN") != "" {
		stMode = "mysql"
	}
	pMode, pLive, pMissing := h.Svc.Payment.Readiness()
	kMode, kLive, kMissing := h.Svc.KYC.Readiness()
	cs := h.Svc.Compliance.GetState()
	gate := map[string]bool{}
	if cs != nil {
		gate["kyc"] = cs.Items[model.ItemKYC]
		gate["tax_source"] = cs.Items[model.ItemTaxSource]
		gate["escrow"] = cs.Items[model.ItemEscrow]
		gate["icp"] = cs.Items[model.ItemICP]
	}
	icpFiled := gate["icp"]
	// 生产就绪 = 真实资金通道就绪 + 真实KYC就绪 + 合规四门全开(税源/托管/ICP/实名)。
	goLive := pLive && kLive && gate["tax_source"] && gate["escrow"] && icpFiled
	h.writeJSON(w, 200, envelope{Code: 200, Data: map[string]any{
		"storage_mode": stMode,
		"embedding":    map[string]any{"mode": h.Svc.Embedder.Mode()},
		"payment":      map[string]any{"mode": pMode, "live": pLive, "missing": pMissing},
		"kyc":          map[string]any{"mode": kMode, "live": kLive, "missing": kMissing},
		"icp_filed":    icpFiled,
		"compliance_gate": gate,
		"go_live_ready":  goLive,
	}})
}

// ---- admin (P3 运营看板) ----

// handleAdmin 运营专用端点(仅运营 A001 可访问)。
//   GET /api/v1/admin/metrics -> 平台运营指标快照(只读聚合)
func (h *Handler) handleAdmin(w http.ResponseWriter, r *http.Request, segs []string) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	if c.Role != string(model.RoleAdmin) {
		h.writeJSON(w, http.StatusForbidden, envelope{Code: 403, Msg: "仅运营可访问运营看板"})
		return
	}
	if r.Method == http.MethodGet && len(segs) >= 4 && segs[3] == "metrics" {
		h.writeJSON(w, 200, envelope{Code: 200, Data: h.Svc.PlatformMetrics()})
		return
	}
	h.writeJSON(w, http.StatusNotFound, envelope{Code: 404, Msg: "not found"})
}

// ---- wallet (P2-1 钱包/流水/提现) ----

// handleWallet 路由:
//   GET  /api/v1/wallet                         -> 当前用户钱包+最近流水
//   POST /api/v1/wallet/withdraw                -> 接单方提现申请(body: amount_cents)
//   GET  /api/v1/wallet/withdrawals             -> admin 提现列表(pending 优先)
//   POST /api/v1/wallet/withdrawals/{id}/approve-> admin 审批(body: approve, note)
func (h *Handler) handleWallet(w http.ResponseWriter, r *http.Request, segs []string) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	switch {
	case len(segs) == 3 && r.Method == http.MethodGet:
		// 我的钱包
		h.writeJSON(w, 200, envelope{Code: 200, Data: h.Svc.GetWallet(c.UserID)})

	case len(segs) == 4 && segs[3] == "withdraw" && r.Method == http.MethodPost:
		// 接单方提现申请
		var body struct {
			AmountCents int64 `json:"amount_cents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res := h.Svc.RequestWithdraw(c.UserID, body.AmountCents)
		if res.Block != nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: res.Block.Reason})
			return
		}
		h.writeJSON(w, 201, envelope{Code: 201, Msg: "提现申请已提交, 待运营审批", Data: res.Withdrawal})

	case len(segs) == 4 && segs[3] == "withdrawals" && r.Method == http.MethodGet:
		// admin 提现列表
		if c.Role != string(model.RoleAdmin) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅运营可查看提现列表"})
			return
		}
		list, block := h.Svc.ListWithdrawals(c.UserID)
		if block != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: block.Reason})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Data: list})

	case len(segs) == 6 && segs[3] == "withdrawals" && segs[5] == "approve" && r.Method == http.MethodPost:
		// admin 审批提现
		if c.Role != string(model.RoleAdmin) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅运营可审批提现"})
			return
		}
		var body struct {
			Approve bool   `json:"approve"`
			Note    string `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res := h.Svc.ApproveWithdraw(segs[4], c.UserID, body.Approve, body.Note)
		if res.Block != nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: res.Block.Reason})
			return
		}
		msg := "提现已通过并出账"
		if !body.Approve {
			msg = "提现已驳回, 金额解冻回可用余额"
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: msg, Data: res.Withdrawal})

	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// ---- notifications (P2-3 通知中心) ----
// 路由:
//   GET  /api/v1/notifications                  -> 当前用户通知列表(倒序)
//   GET  /api/v1/notifications/unread           -> 当前用户未读数
//   POST /api/v1/notifications/{id}/read        -> 标记某条已读(须属于该用户)
func (h *Handler) handleNotifications(w http.ResponseWriter, r *http.Request, segs []string) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	switch {
	case len(segs) == 3 && r.Method == http.MethodGet:
		// 通知列表(倒序, 默认 50)
		list := h.Svc.ListNotifications(c.UserID, 50)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: list})

	case len(segs) == 4 && segs[3] == "unread" && r.Method == http.MethodGet:
		// 未读数
		n := h.Svc.UnreadCount(c.UserID)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]int{"unread": n}})

	case len(segs) == 5 && segs[4] == "read" && r.Method == http.MethodPost:
		// 标记已读
		n, found := h.Svc.MarkNotificationRead(c.UserID, segs[3])
		if !found {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: "通知不存在或无权操作"})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "已读", Data: n})

	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// ---- P3 企业版治理(设计阶段最小骨架: 组织/成员) ----

func (h *Handler) handleOrgs(w http.ResponseWriter, r *http.Request, segs []string) {
	c, ok := h.requireAuth(w, r)
	if !ok {
		return
	}
	switch {
	case len(segs) == 3 && r.Method == http.MethodPost:
		// 创建组织: {"name":"..."}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: "组织名不能为空"})
			return
		}
		o, err := h.Svc.CreateOrg(c.UserID, body.Name)
		if err != nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "组织已创建", Data: o})

	case len(segs) == 3 && r.Method == http.MethodGet:
		// 列出我作为 owner 的组织
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.ListOrgs(c.UserID)})

	case len(segs) == 5 && segs[4] == "members" && r.Method == http.MethodGet:
		// 列出组织成员: /orgs/{id}/members
		members := h.Svc.ListOrgMembers(segs[3])
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: members})

	case len(segs) == 5 && segs[4] == "members" && r.Method == http.MethodPost:
		// 邀请成员: /orgs/{id}/members  {"user_id":"...","role":"..."}
		var body struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.UserID == "" {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: "user_id 不能为空"})
			return
		}
		if err := h.Svc.InviteOrgMember(segs[3], c.UserID, body.UserID, body.Role); err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "已邀请"})

	case len(segs) == 5 && segs[4] == "wallet" && r.Method == http.MethodGet:
		// 组织钱包: GET /orgs/{id}/wallet
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.GetOrgWallet(segs[3])})

	case len(segs) == 6 && segs[4] == "wallet" && segs[5] == "deposit" && r.Method == http.MethodPost:
		// 组织钱包充值(owner/admin): POST /orgs/{id}/wallet/deposit {"amount_cents":...}
		var body struct {
			AmountCents int64 `json:"amount_cents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := h.Svc.DepositOrgWallet(segs[3], c.UserID, body.AmountCents); err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "充值成功", Data: h.Svc.GetOrgWallet(segs[3])})

	case len(segs) == 5 && segs[4] == "tasks" && r.Method == http.MethodPost:
		// 组织级发包: POST /orgs/{id}/tasks {"title","category","budget_cents","spec"}
		var body struct {
			Title       string `json:"title"`
			Category    string `json:"category"`
			BudgetCents int64  `json:"budget_cents"`
			Spec        string `json:"spec"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		t, err := h.Svc.CreateOrgTask(segs[3], c.UserID, body.Title, body.Category, body.BudgetCents, body.Spec)
		if err != nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "组织任务已发布", Data: map[string]interface{}{"task": t}})

	case len(segs) == 5 && segs[4] == "tasks" && r.Method == http.MethodGet:
		// 组织发布的任务: GET /orgs/{id}/tasks
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.GetOrgTasks(segs[3])})

	case len(segs) == 5 && segs[4] == "payouts" && r.Method == http.MethodPost:
		// 组织钱包提现申请: POST /orgs/{id}/payouts {"amount_cents":...}
		var body struct {
			AmountCents int64 `json:"amount_cents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a, err := h.Svc.RequestOrgPayout(segs[3], c.UserID, body.AmountCents)
		if err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "提现申请已提交", Data: a})

	case len(segs) == 7 && segs[4] == "approvals" && segs[6] == "approve" && r.Method == http.MethodPost:
		// 审批组织提现: POST /orgs/{id}/approvals/{approvalID}/approve {"approve":true/false}
		var body struct {
			Approve bool `json:"approve"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a, err := h.Svc.ApproveOrgPayout(segs[3], c.UserID, segs[5], body.Approve)
		if err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "审批完成", Data: a})

	case len(segs) == 5 && segs[4] == "approvals" && r.Method == http.MethodGet:
		// 组织审批单列表: GET /orgs/{id}/approvals
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.Store.ListApprovalsByOrg(segs[3])})

	case len(segs) == 5 && segs[4] == "apikeys" && r.Method == http.MethodPost:
		// 生成组织开放 API Key: POST /orgs/{id}/apikeys {"label","scopes"}
		var body struct {
			Label  string `json:"label"`
			Scopes string `json:"scopes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, k, err := h.Svc.CreateAPIKey(segs[3], c.UserID, body.Label, body.Scopes)
		if err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "API Key 已生成(明文仅此一次)", Data: map[string]interface{}{"key": raw, "api_key": k}})

	case len(segs) == 5 && segs[4] == "apikeys" && r.Method == http.MethodGet:
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.ListAPIKeys(segs[3])})

	case len(segs) == 5 && segs[4] == "webhooks" && r.Method == http.MethodPost:
		// 订阅 Webhook: POST /orgs/{id}/webhooks {"url","events":[...]}
		var body struct {
			URL    string   `json:"url"`
			Events []string `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		wh, err := h.Svc.CreateWebhook(segs[3], c.UserID, body.URL, body.Events)
		if err != nil {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "Webhook 已订阅", Data: wh})

	case len(segs) == 5 && segs[4] == "webhooks" && r.Method == http.MethodGet:
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.ListWebhooks(segs[3])})

	case len(segs) == 5 && segs[4] == "webhook-deliveries" && r.Method == http.MethodGet:
		// 组织 Webhook 投递记录(验证事件已真实投递)。
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.Store.ListWebhookDeliveriesByOrg(segs[3])})

	case len(segs) == 5 && segs[4] == "orders" && r.Method == http.MethodGet:
		// 组织订单列表(企业版统一结算视图): GET /orgs/{id}/orders
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.GetOrgOrders(segs[3])})

	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// handleUsers 用户公开声誉画像(市场需求大厅配套, 无需登录即可查)。
func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 4 && r.Method == http.MethodGet:
		// 公开画像: /users/{id}
		p, err := h.Svc.GetPublicProfile(segs[3])
		if err != nil {
			h.writeJSON(w, 404, envelope{Code: 404, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: p})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// handleMeta 平台元数据(类目法等), 纯读、无需登录。
func (h *Handler) handleMeta(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 4 && segs[3] == "categories" && r.Method == http.MethodGet:
		// 类目法: 需求大厅筛选/发单下拉的唯一事实来源。
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: h.Svc.ListCategories()})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// handleRecommend 推荐位: 接单方首页 feed(按多因子匹配分推荐 open 任务) + 需求方首页 feed(推荐接单方)。
func (h *Handler) handleRecommend(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 4 && segs[3] == "tasks" && r.Method == http.MethodGet:
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		if c.Role != string(model.RoleProvider) {
			h.writeJSON(w, 403, envelope{Code: 403, Msg: "仅接单方可查看推荐任务"})
			return
		}
		recs := h.Svc.RecommendTasks(c.UserID)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: recs})
	case len(segs) == 4 && segs[3] == "providers" && r.Method == http.MethodGet:
		// 需求方首页 feed: 给定任务推荐高匹配接单方。任意登录用户可用(需求方为主)。
		if _, ok := h.requireAuth(w, r); !ok {
			return
		}
		taskID := r.URL.Query().Get("task_id")
		if taskID == "" {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: "task_id 必填"})
			return
		}
		recs := h.Svc.RecommendProviders(taskID)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: recs})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// handleKYC 实名认证通道: 提交认证 / 查询状态(均须登录)。
func (h *Handler) handleKYC(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 3 && r.Method == http.MethodPost:
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name"`
			IDNo string `json:"id_no"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ref, verified, err := h.Svc.SubmitKYC(c.UserID, body.Name, body.IDNo)
		if err != nil {
			h.writeJSON(w, 400, envelope{Code: 400, Msg: err.Error()})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]interface{}{
			"ref":       ref,
			"verified":  verified,
			"kyc_state": map[string]bool{"kyc_verified": verified},
		}})
	case len(segs) == 3 && r.Method == http.MethodGet:
		c, ok := h.requireAuth(w, r)
		if !ok {
			return
		}
		v, st, _ := h.Svc.KYCStatus(c.UserID)
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]interface{}{
			"verified": v,
			"state":    st,
		}})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// handlePayments 支付通道回调: 接收微信支付等异步结果通知, 驱动订单托管生效。
func (h *Handler) handlePayments(w http.ResponseWriter, r *http.Request, segs []string) {
	switch {
	case len(segs) == 4 && segs[3] == "notify" && r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		orderID, ok := h.Svc.OnPaymentNotify(body)
		if !ok {
			// 回调需被通道视为已接收(返回 200), 但标记业务未处理(便于排障)。
			h.writeJSON(w, 200, envelope{Code: 200, Msg: "ack但订单未命中", Data: map[string]string{"order_id": orderID}})
			return
		}
		h.writeJSON(w, 200, envelope{Code: 200, Msg: "ok", Data: map[string]string{"order_id": orderID}})
	default:
		h.writeJSON(w, 404, envelope{Code: 404, Msg: "not found"})
	}
}

// atoi64 安全解析整数字符串(失败返回 0), 用于查询参数。
func atoi64(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
