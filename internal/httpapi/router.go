package httpapi

import (
	"net/http"

	"zanbuddy/internal/identity"
	"zanbuddy/internal/store"
)

// Server v2 接口。依赖只有身份域和存储(公开页要按 slug 查人)。
// 其他域接进来时也走这个结构, 每个域一个字段, 装配在 main 里完成。
type Server struct {
	Identity *identity.Service
	Store    store.Store
	Version  string
}

func NewServer(idsvc *identity.Service, st store.Store, version string) *Server {
	return &Server{Identity: idsvc, Store: st, Version: version}
}

// Routes 显式路由表。
//
// 为什么不用 v1 那种按路径段 switch 分派: v1 加一个端点要动三处(case 分支、
// 函数定义、路由注释), 而且最长的一个处理函数 130 行。这里一行注册一个端点,
// 路径参数由 net/http 自己解析(Go 1.22 起支持 {id} 占位)。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v2/healthz", s.health)
	mux.HandleFunc("GET /api/v2/readyz", s.ready)

	// 身份: 发码与登录不需要登录态, 其余要
	mux.HandleFunc("POST /api/v2/auth/code", s.authCode)
	mux.HandleFunc("POST /api/v2/auth/login", s.authLogin)
	mux.Handle("POST /api/v2/auth/logout", RequireAuth(http.HandlerFunc(s.authLogout)))
	mux.Handle("GET /api/v2/me", RequireAuth(http.HandlerFunc(s.me)))
	mux.Handle("PATCH /api/v2/me", RequireAuth(http.HandlerFunc(s.patchMe)))
	mux.Handle("PUT /api/v2/me/profile", RequireAuth(http.HandlerFunc(s.putProfile)))

	// 需求大厅: 公开可读。落地页 → 大厅 → 登录, 前台不该建在登录墙后面。
	mux.HandleFunc("GET /api/v2/tasks", s.listTasks)
	mux.HandleFunc("GET /api/v2/meta/categories", s.listCategories)

	// 公开页: 免登录, 是信用资产的对外出口
	mux.HandleFunc("GET /api/v2/public/profiles/{slug}", s.publicProfile)

	return WithTrace(WithAuth(RequireCSRF(mux)))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	WriteOK(w, r, map[string]interface{}{"service": "zanbuddy-v2", "version": s.Version})
}

// ready 检查依赖。DB 不通返 503, 但不影响 /healthz——
// 进程活着但依赖不健康时, 不该被容器编排直接重启, 也不该继续接流量。
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.Store.GetUser("R001"); !ok {
		WriteErr(w, r, http.StatusServiceUnavailable, "DEPENDENCY_DOWN", "存储不可用")
		return
	}
	WriteOK(w, r, map[string]string{"status": "ready"})
}
