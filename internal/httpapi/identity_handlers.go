package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"zanbuddy/internal/identity"
	"zanbuddy/internal/model"
)

// meView 当前用户视图。profile 恒非 nil(没建档时是空档案), 前端不用写分支。
type meView struct {
	User    *model.User            `json:"user"`
	Profile *model.ProviderProfile `json:"profile"`
}

// authCode 发验证码。devMode 下把码放进 dev_code 字段直接返回, 省去每次查日志。
// 生产接短信通道后 dev_code 不再出现, 响应里只有 sent。
func (s *Server) authCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	if !DecodeJSON(r, &in) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_REQUEST", "请求格式不对")
		return
	}
	code, err := s.Identity.SendCode(in.Phone)
	if err != nil {
		mapIdentityErr(w, r, err)
		return
	}
	resp := map[string]interface{}{"sent": true}
	if code != "" {
		resp["dev_code"] = code
		resp["hint"] = "开发环境验证码，生产不返回"
	}
	WriteOK(w, r, resp)
}

// authLogin 验证码登录。手机号不存在即注册, 身份取第一次登录时的倾向。
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
		Role  string `json:"role"`
	}
	if !DecodeJSON(r, &in) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_REQUEST", "请求格式不对")
		return
	}
	res, err := s.Identity.LoginByCode(in.Phone, strings.TrimSpace(in.Code), in.Role)
	if err != nil {
		mapIdentityErr(w, r, err)
		return
	}
	SetSession(w, r, res.Token)
	WriteOK(w, r, map[string]interface{}{
		"user":      res.User,
		"token":     res.Token, // 移动端 / 开放 API 用; 浏览器有 Cookie 可忽略
		"is_new":    res.IsNew,
		"onboarded": res.Onboard,
		"next":      nextPath(res.User),
	})
}

// nextPath 登录后的默认落点。
// 接单方没建档先去档案页(不填档案召回池里就没有他), 建完档直接落到需求大厅;
// 需求方也先给大厅: 他现在能看的只有这一页, 给个 404 不如给个能逛的列表。
func nextPath(u *model.User) string {
	if u.Role == model.RoleProvider && !u.Onboarded {
		return "/static/app/profile.html"
	}
	return "/static/app/hall.html"
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	ClearSession(w)
	WriteOK(w, r, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	u, p, err := s.Identity.Me(cl.UserID)
	if err != nil {
		mapIdentityErr(w, r, err)
		return
	}
	WriteOK(w, r, meView{User: u, Profile: p})
}

// patchMe 改昵称。只开这一个字段:
// 角色切换涉及权限, 不在"改资料"接口里做, 避免一次传错就升权。
func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var in struct {
		Name *string `json:"name"`
	}
	if !DecodeJSON(r, &in) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_REQUEST", "请求格式不对")
		return
	}
	u, p, err := s.Identity.Me(cl.UserID)
	if err != nil {
		mapIdentityErr(w, r, err)
		return
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len([]rune(n)) > 24 {
			WriteErrField(w, r, http.StatusBadRequest, "NAME_INVALID", "昵称请填 1 到 24 个字", "name", "")
			return
		}
		u.Name = n
		s.Store.PutUser(u)
	}
	WriteOK(w, r, meView{User: u, Profile: p})
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	cl, _ := Claims(r)
	var in identity.ProfileInput
	if !DecodeJSON(r, &in) {
		WriteErr(w, r, http.StatusBadRequest, "BAD_REQUEST", "请求格式不对")
		return
	}
	p, err := s.Identity.SaveProfile(cl.UserID, in)
	if err != nil {
		mapIdentityErr(w, r, err)
		return
	}
	WriteOK(w, r, map[string]interface{}{"profile": p})
}

// publicProfile 免登录的公开展示。字段是白名单而不是黑名单:
// 少列一个不会被泄露, 多列一个就可能把手机号带出去。
func (s *Server) publicProfile(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	u, ok := s.Store.GetUserBySlug(slug)
	if !ok {
		WriteErr(w, r, http.StatusNotFound, "NOT_FOUND", "没有这个档案")
		return
	}
	p, _ := s.Store.GetProfile(u.ID)
	if p == nil {
		p = &model.ProviderProfile{UserID: u.ID}
	}
	WriteOK(w, r, map[string]interface{}{
		"slug":         u.Slug,
		"name":         u.Name,
		"headline":     p.Headline,
		"bio":          p.Bio,
		"skills":       p.Skills,
		"portfolio":    p.Portfolio,
		"completeness": p.Completeness,
		"credit_score": u.CreditScore,
		"availability": p.Availability,
		"stats":        map[string]int{"orders": 0, "endorsements": 0, "avg_days": 0},
		"endorsements": []interface{}{}, // 背书功能在 M4, 现在返回空数组而不是 null
	})
}

// mapIdentityErr 把域错误翻成 HTTP。状态码的选择有语义:
// 409 是"规则不允许", 400 是"你填错了", 429 是"太快了"。
// 前端按 code 决定提示语气, 不按状态码猜。
func mapIdentityErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidPhone):
		WriteErrField(w, r, http.StatusBadRequest, "INVALID_PHONE", err.Error(), "phone", "11 位手机号，不用加 +86")
	case errors.Is(err, identity.ErrCodeExpired):
		WriteErrField(w, r, http.StatusConflict, "CODE_EXPIRED", err.Error(), "code", "点右上角重新获取")
	case errors.Is(err, identity.ErrCodeWrong):
		WriteErrField(w, r, http.StatusConflict, "CODE_WRONG", err.Error(), "code", "开发环境是 000000")
	case errors.Is(err, identity.ErrTooManyTry):
		WriteErrField(w, r, http.StatusTooManyRequests, "TOO_MANY_TRIES", err.Error(), "code", "一分钟后再试")
	case errors.Is(err, identity.ErrSkillsRange):
		WriteErrField(w, r, http.StatusBadRequest, "SKILLS_RANGE", err.Error(), "skills", "选太多等于没选，推荐理由会说不清")
	case errors.Is(err, identity.ErrUnauthorized):
		WriteErr(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error())
	default:
		WriteErr(w, r, http.StatusInternalServerError, "INTERNAL", "服务出了点问题")
	}
}
