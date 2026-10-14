// HiveGet M0 技术骨架入口。
// 启动即构建: 存储(内存/MySQL) → 合规闸门 → 资金/IM stub → 主链路服务 → HTTP。
// 资金与IM为 stub, 合规项默认仅 KYC 通过; 通过 admin 接口翻转模拟合规线落地。
// 接真实 MySQL: 设置环境变量 HIVEGET_DB_DSN 后启动(见 README)。
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"hiveget/internal/clarify"
	"hiveget/internal/compliance"
	"hiveget/internal/faq"
	"hiveget/internal/handler"
	"hiveget/internal/im"
	"hiveget/internal/kyc"
	"hiveget/internal/model"
	"hiveget/internal/notify"
	"hiveget/internal/payment"
	"hiveget/internal/reqai"
	"hiveget/internal/service"
	"hiveget/internal/store"
)

func main() {
	// 存储选择: 默认内存(零依赖); 设置 HIVEGET_DB_DSN 则接 192.168.3.18 上的 MySQL。
	var st store.Store
	if dsn := os.Getenv("HIVEGET_DB_DSN"); dsn != "" {
		m, err := store.NewMySQL(dsn)
		if err != nil {
			log.Fatalf("MySQL 初始化失败: %v", err)
		}
		st = m
		log.Println("存储: MySQL (" + dsn + ")")
	} else {
		st = store.NewMem()
		log.Println("存储: 内存(零依赖); 设置 HIVEGET_DB_DSN 可切 MySQL")
	}

	comp := compliance.New(st.GetCompliance, st.SetCompliance)
	pay := payment.New()
	kycSvc := kyc.New() // KYC 通道: 默认 Mock(自动通过); HIVEGET_KYC_PROVIDER=real 走真实厂商骨架
	// 将 KYC Provider 状态钩子注入合规闸门, 使 KYC 从纯状态位改为可查询 Provider 状态。
	comp.SetKYCProvider(func(uid string) bool {
		v, _, _ := kycSvc.Status(uid)
		return v
	})
	cl := clarify.NewManager() // P1-2 AI 澄清会话管理(零新增依赖)

	// P2-2 IM: 默认 stub 内存总线(不依赖外部); 配置腾讯云凭证后切换 Provider/审核。
	imProv := im.NewStubProvider()
	if os.Getenv("HIVEGET_IM_PROVIDER") == "tencent" {
		appID, _ := strconv.Atoi(os.Getenv("HIVEGET_TENCENT_IM_APPID"))
		imProv = im.NewTencentIMProvider(appID,
			os.Getenv("HIVEGET_TENCENT_IM_SECRET"),
			os.Getenv("HIVEGET_TENCENT_IM_ADMIN"),
			os.Getenv("HIVEGET_TENCENT_IM_API"))
	}
	imMod := im.NewStubModerator()
	if os.Getenv("HIVEGET_IM_MODERATOR") == "tencent" {
		imMod = im.NewTencentModerator()
	}
	imSvc := im.NewService(st, imProv, imMod)

	// P2-3 通知: 默认仅站内信(落库); 配置真实通道 env 优先启用真实 Provider, 否则 HIVEGET_NOTIFY_EMAIL/SMS=1 启用骨架。
	var emailProv notify.Provider
	if os.Getenv("HIVEGET_SMTP_HOST") != "" {
		emailProv = notify.SMTPEmailProvider{
			Host: os.Getenv("HIVEGET_SMTP_HOST"), Port: os.Getenv("HIVEGET_SMTP_PORT"),
			User: os.Getenv("HIVEGET_SMTP_USER"), Pass: os.Getenv("HIVEGET_SMTP_PASS"),
			From: os.Getenv("HIVEGET_SMTP_FROM"),
		}
	} else if os.Getenv("HIVEGET_NOTIFY_EMAIL") == "1" {
		emailProv = notify.NewStubEmailProvider()
	}
	var smsProv notify.Provider
	if os.Getenv("HIVEGET_SMS_GATEWAY") != "" {
		smsProv = notify.HTTPSMSProvider{
			GatewayURL: os.Getenv("HIVEGET_SMS_GATEWAY"),
			SecretId:   os.Getenv("HIVEGET_SMS_KEYID"),
			SecretKey:  os.Getenv("HIVEGET_SMS_KEYSECRET"),
			Tpl:        os.Getenv("HIVEGET_SMS_TPL"),
		}
	} else if os.Getenv("HIVEGET_NOTIFY_SMS") == "1" {
		smsProv = notify.NewStubSMSProvider()
	}
	notifySvc := notify.NewService(st, emailProv, smsProv)
	faqEng := faq.NewEngine() // B3 AI 客服应答(规则优先 + 可选 LLM 兜底, 复用 B1 的 HIVEGET_LLM_API_KEY 开关)
	reqaiEng := reqai.NewEngine() // C1 AI 需求翻译/分析(规则保底 + 可选 LLM 兜底, 复用同一开关)
	svc := service.New(st, comp, pay, kycSvc, imSvc, cl, notifySvc, faqEng, reqaiEng)

	// 预置一个示例需求单, 便于开箱演示「撮合 → 接单」链路。
	seedTask(st)

	h := handler.New(svc)
	mux := http.NewServeMux()
	mux.Handle("/api/", h)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web")))) // 用户画像页等静态前端
	mux.Handle("/", h) // 根路径落地页

	log.Println("HiveGet M0 已启动: http://localhost:8080")
	log.Println("演示账号(登录 uid): R001=需求方, P001/P002/P003/P004=接单方, A001=运营")
	log.Println("合规状态: 默认仅 KYC 通过; 用 A001 调 POST /api/v1/compliance 翻转 icp/tax_source/escrow 模拟合规落地")
	log.Fatal(http.ListenAndServe(":8080", cors(mux)))
}

// cors 允许跨域(便于用户画像页从 file:// 或独立前端直接调用 API; 演示环境宽松策略)。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func seedTask(st store.Store) {
	t := &model.Task{
		ID:          "T-DEMO-001",
		RequesterID: "R001",
		Title:       "用 Python 搭建 RAG 问答系统",
		Category:    "nlp",
		BudgetCents: 300000, // ¥3000.00
		Spec:        "需要 python + rag + nlp 的检索增强生成问答, 支持文档上传与中文问答",
		SpecObj: model.TaskSpec{
			Deliverables:   "1 套可运行源码 + 部署文档",
			RequiredFormat: "Python 项目(zip) + README",
			DeadlineAt:     model.Now() + int64(7*24*3600*1000),
			StyleRef:       "生产级可维护代码",
		},
		Status:    model.TaskOpen,
		CreatedAt: model.Now(),
	}
	t.Clarifications = model.ClarifySpec(t.SpecObj)
	st.PutTask(t)
}
