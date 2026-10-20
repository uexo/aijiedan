package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"zanbuddy/internal/middleware"
)

// Cookie 名。session 是 HttpOnly 的 JWT, csrf 是可读随机串。
const (
	cookieSession = "zb_session"
	cookieCSRF    = "csrf_token"
	headerCSRF    = "X-CSRF-Token"
)

type ctxKey int

const (
	ctxTrace ctxKey = iota
	ctxClaims
)

// TraceID 取本次请求的追踪号。中间件没跑时返回空串, 不 panic。
func TraceID(r *http.Request) string {
	if v, ok := r.Context().Value(ctxTrace).(string); ok {
		return v
	}
	return ""
}

// WithTrace 生成 8 字节追踪号并回写响应头。
// 放响应头的原因: 前端 5xx 提示要显示这个号, 用户截图就能定位到日志。
func WithTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newToken(8)
		w.Header().Set("X-Trace-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxTrace, id)))
	})
}

// SetSession 下发会话。同时下发 csrf cookie 供前端读取后放进请求头。
//
// HttpOnly 的选择: v1 用 localStorage 存 token, 只要有一个 XSS 点, 24 小时有效的
// token 就被读走。Cookie 加 HttpOnly 之后 JS 读不到, XSS 也偷不走会话。
// Secure 在本地开发(HTTP)下不能开, 否则浏览器直接丢弃 cookie, 所以按请求协议判断。
func SetSession(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     cookieSession,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   24 * 3600,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieCSRF,
		Value:    newToken(16),
		Path:     "/",
		HttpOnly: false, // 前端必须读得到, 才能放进 X-CSRF-Token
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   24 * 3600,
	})
}

// ClearSession 退出登录。
func ClearSession(w http.ResponseWriter) {
	for _, n := range []string{cookieSession, cookieCSRF} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1})
	}
}

// ClaimsFrom 解析当前会话。先看 Cookie, 再看 Authorization: Bearer。
// 两套都留的原因: 浏览器用 Cookie, 移动端和开放 API 用 Bearer。
func ClaimsFrom(r *http.Request) (*middleware.Claims, bool) {
	if c, err := r.Cookie(cookieSession); err == nil && c.Value != "" {
		if cl, err := middleware.Parse(c.Value); err == nil {
			return cl, true
		}
	}
	if raw, ok := middleware.ExtractToken(r.Header.Get("Authorization")); ok {
		if cl, err := middleware.Parse(raw); err == nil {
			return cl, true
		}
	}
	return nil, false
}

// WithAuth 注入 claims(可选)。没登录也放行, 由各 handler 决定要不要强制。
// 这样同一套路由能给公开页和登录页共用。
func WithAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cl, ok := ClaimsFrom(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), ctxClaims, cl))
		}
		next.ServeHTTP(w, r)
	})
}

// Claims 取中间件注入的会话。
func Claims(r *http.Request) (*middleware.Claims, bool) {
	cl, ok := r.Context().Value(ctxClaims).(*middleware.Claims)
	return cl, ok && cl != nil
}

// RequireAuth 强制登录。未登录返 401, 前端按 code 跳登录并带回跳地址。
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := Claims(r); !ok {
			WriteErr(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF 双提交校验。只在已有会话的写请求上生效:
// 登录和发码时还没有会话, 强行要 csrf 会多一次无意义的前置请求。
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if _, err := r.Cookie(cookieSession); err != nil {
			next.ServeHTTP(w, r) // 未登录, 没有可被 CSRF 利用的会话
			return
		}
		c, err := r.Cookie(cookieCSRF)
		if err != nil || c.Value == "" {
			WriteErr(w, r, http.StatusForbidden, "CSRF_MISSING", "页面已过期，刷新后重试")
			return
		}
		got := strings.TrimSpace(r.Header.Get(headerCSRF))
		if got == "" {
			// 有 cookie 没头: 前端漏带, 与"带了但对不上"是两种问题, 分开报便于排查
			WriteErr(w, r, http.StatusForbidden, "CSRF_MISSING", "请求缺少 CSRF 头，刷新页面后重试")
			return
		}
		if got != c.Value {
			WriteErr(w, r, http.StatusForbidden, "CSRF_MISMATCH", "页面已过期，刷新后重试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
