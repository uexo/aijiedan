// Zanbuddy M0 技术骨架入口。
// 启动即构建: 存储(内存/MySQL) → 合规闸门 → 资金/IM stub → 主链路服务 → HTTP。
// 资金与IM为 stub, 合规项默认仅 KYC 通过; 通过 admin 接口翻转模拟合规线落地。
// 接真实 MySQL: 设置环境变量 ZANBUDDY_DB_DSN 后启动(见 README)。
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"zanbuddy/internal/clarify"
	"zanbuddy/internal/compliance"
	"zanbuddy/internal/demand"
	"zanbuddy/internal/faq"
	"zanbuddy/internal/handler"
	"zanbuddy/internal/httpapi"
	"zanbuddy/internal/identity"
	"zanbuddy/internal/im"
	"zanbuddy/internal/kyc"
	"zanbuddy/internal/model"
	"zanbuddy/internal/notify"
	"zanbuddy/internal/payment"
	"zanbuddy/internal/reqai"
	"zanbuddy/internal/service"
	"zanbuddy/internal/store"
	"zanbuddy/internal/trade"
)

func main() {
	// 存储选择: 默认内存(零依赖); 设置 ZANBUDDY_DB_DSN 则接 192.168.3.18 上的 MySQL。
	var st store.Store
	if dsn := os.Getenv("ZANBUDDY_DB_DSN"); dsn != "" {
		m, err := store.NewMySQL(dsn)
		if err != nil {
			log.Fatalf("MySQL 初始化失败: %v", err)
		}
		st = m
		log.Println("存储: MySQL (" + dsn + ")")
	} else {
		st = store.NewMem()
		log.Println("存储: 内存(零依赖); 设置 ZANBUDDY_DB_DSN 可切 MySQL")
	}

	comp := compliance.New(st.GetCompliance, st.SetCompliance)
	pay := payment.New()
	kycSvc := kyc.New() // KYC 通道: 默认 Mock(自动通过); ZANBUDDY_KYC_PROVIDER=real 走真实厂商骨架
	// 将 KYC Provider 状态钩子注入合规闸门, 使 KYC 从纯状态位改为可查询 Provider 状态。
	comp.SetKYCProvider(func(uid string) bool {
		v, _, _ := kycSvc.Status(uid)
		return v
	})
	cl := clarify.NewManager() // P1-2 AI 澄清会话管理(零新增依赖)

	// P2-2 IM: 默认 stub 内存总线(不依赖外部); 配置腾讯云凭证后切换 Provider/审核。
	imProv := im.NewStubProvider()
	if os.Getenv("ZANBUDDY_IM_PROVIDER") == "tencent" {
		appID, _ := strconv.Atoi(os.Getenv("ZANBUDDY_TENCENT_IM_APPID"))
		imProv = im.NewTencentIMProvider(appID,
			os.Getenv("ZANBUDDY_TENCENT_IM_SECRET"),
			os.Getenv("ZANBUDDY_TENCENT_IM_ADMIN"),
			os.Getenv("ZANBUDDY_TENCENT_IM_API"))
	}
	imMod := im.NewStubModerator()
	if os.Getenv("ZANBUDDY_IM_MODERATOR") == "tencent" {
		imMod = im.NewTencentModerator()
	}
	imSvc := im.NewService(st, imProv, imMod)

	// P2-3 通知: 默认仅站内信(落库); 配置真实通道 env 优先启用真实 Provider, 否则 ZANBUDDY_NOTIFY_EMAIL/SMS=1 启用骨架。
	var emailProv notify.Provider
	if os.Getenv("ZANBUDDY_SMTP_HOST") != "" {
		emailProv = notify.SMTPEmailProvider{
			Host: os.Getenv("ZANBUDDY_SMTP_HOST"), Port: os.Getenv("ZANBUDDY_SMTP_PORT"),
			User: os.Getenv("ZANBUDDY_SMTP_USER"), Pass: os.Getenv("ZANBUDDY_SMTP_PASS"),
			From: os.Getenv("ZANBUDDY_SMTP_FROM"),
		}
	} else if os.Getenv("ZANBUDDY_NOTIFY_EMAIL") == "1" {
		emailProv = notify.NewStubEmailProvider()
	}
	var smsProv notify.Provider
	if os.Getenv("ZANBUDDY_SMS_GATEWAY") != "" {
		smsProv = notify.HTTPSMSProvider{
			GatewayURL: os.Getenv("ZANBUDDY_SMS_GATEWAY"),
			SecretId:   os.Getenv("ZANBUDDY_SMS_KEYID"),
			SecretKey:  os.Getenv("ZANBUDDY_SMS_KEYSECRET"),
			Tpl:        os.Getenv("ZANBUDDY_SMS_TPL"),
		}
	} else if os.Getenv("ZANBUDDY_NOTIFY_SMS") == "1" {
		smsProv = notify.NewStubSMSProvider()
	}
	notifySvc := notify.NewService(st, emailProv, smsProv)
	faqEng := faq.NewEngine()     // B3 AI 客服应答(规则优先 + 可选 LLM 兜底, 复用 B1 的 ZANBUDDY_LLM_API_KEY 开关)
	reqaiEng := reqai.NewEngine() // C1 AI 需求翻译/分析(规则保底 + 可选 LLM 兜底, 复用同一开关)
	svc := service.New(st, comp, pay, kycSvc, imSvc, cl, notifySvc, faqEng, reqaiEng)

	// 预置示例需求单, 便于开箱演示「大厅 → 认领」链路。
	// 已存在的不覆盖: 否则重启服务会把已认领的单重置回 open, 演示数据就成了假象。
	seedTasks(st)

	h := handler.New(svc)

	// v2 接入层与身份域。v1 的 /api/v1 原样保留, 两条链路并行, 迁移窗口内互不影响。
	// SMS 未接真实通道时走开发模式: 验证码固定 000000 并在响应里回显, 生产接通道后自动关闭。
	idSvc := identity.New(st, os.Getenv("ZANBUDDY_SMS_PROVIDER") == "" || os.Getenv("ZANBUDDY_SMS_PROVIDER") == "log")
	tradeSvc := trade.New(st)
	demandSvc := demand.New(st)

	mux := http.NewServeMux()
	mux.Handle("/api/v2/", httpapi.NewServer(idSvc, tradeSvc, demandSvc, st, "2.0.0").Routes())
	mux.Handle("/api/", h)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web")))) // 用户画像页等静态前端
	mux.Handle("/", h)                                                                     // 根路径落地页

	if os.Getenv("ZANBUDDY_JWT_SECRET") == "" {
		log.Println("警告: ZANBUDDY_JWT_SECRET 未设置, 正在用开发默认值签名会话; 生产必须设置")
	}

	port := os.Getenv("ZANBUDDY_PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("Zanbuddy M0 已启动: http://localhost:" + port)
	log.Println("演示账号(登录 uid): R001=需求方, P001/P002/P003/P004=接单方, A001=运营")
	log.Println("合规状态: 默认仅 KYC 通过; 用 A001 调 POST /api/v1/compliance 翻转 icp/tax_source/escrow 模拟合规落地")
	log.Fatal(http.ListenAndServe(":"+port, cors(mux)))
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

// seedTasks 预置演示需求。够撑起一个像样的大厅: 六个类目、不同预算、不同截止日。
//
// 只在一个类目下堆六单会让筛选看着像摆设, 也会掩盖排序的问题。
// 已有的不覆盖(先 GetTask 再写), 认领过的单重启后还在原状态。
func seedTasks(st store.Store) {
	day := int64(24 * 3600 * 1000)
	now := model.Now()

	demo := []*model.Task{
		{
			ID: "T-DEMO-001", RequesterID: "R001", Title: "用 Python 搭建 RAG 问答系统",
			Category: "nlp", BudgetCents: 300000,
			Spec: "需要 python + rag + nlp 的检索增强生成问答, 支持文档上传与中文问答",
			SpecObj: model.TaskSpec{
				Deliverables: "1 套可运行源码 + 部署文档", RequiredFormat: "Python 项目(zip) + README",
				DeadlineAt: now + 7*day, StyleRef: "生产级可维护代码",
				// 带清单的两条需求: 交付要按项交代、验收要按项比对,
				// 演示时能看出"逐项"和"一句话交付"的区别。
				Items: []model.RequirementItem{
					{ID: "R1", Desc: "文档上传 + 解析(PDF/Word)"},
					{ID: "R2", Desc: "基于检索的中文问答接口"},
					{ID: "R3", Desc: "部署文档与启动脚本"},
				},
			},
		},
		{
			ID: "T-DEMO-002", RequesterID: "R002", Title: "小红书护肤品牌种草文案 10 篇",
			Category: "copywriting", BudgetCents: 120000,
			Spec: "面向 25-35 岁女性, 主打成分党, 每篇 400 字左右, 需要 3 个不同人设切入, 不要硬广腔",
			SpecObj: model.TaskSpec{
				Deliverables: "10 篇成稿 + 每篇 3 个标题备选", RequiredFormat: "飞书文档 / Word",
				DeadlineAt: now + 4*day, StyleRef: "口语化、有具体使用场景, 不堆形容词",
			},
		},
		{
			ID: "T-DEMO-003", RequesterID: "R003", Title: "SaaS 落地页重做（含移动端）",
			Category: "frontend", BudgetCents: 680000,
			Spec: "现有落地页转化率低, 需要重做首屏到定价区, 移动端优先, 接现有的表单接口",
			SpecObj: model.TaskSpec{
				Deliverables: "可上线页面 + 组件源码 + 移动端适配", RequiredFormat: "HTML/CSS/JS 或 React",
				DeadlineAt: now + 12*day, StyleRef: "克制的 B 端风格, 不要大色块渐变",
			},
		},
		{
			ID: "T-DEMO-004", RequesterID: "R001", Title: "客服对话数据清洗与意图标注",
			Category: "data", BudgetCents: 240000,
			Spec: "约 8 万条客服对话, 需要去重、脱敏、按 12 个意图分类, 输出可训练的标注集",
			SpecObj: model.TaskSpec{
				Deliverables: "清洗脚本 + 标注集 + 意图分布报告", RequiredFormat: "CSV + Python 脚本",
				DeadlineAt: now + 9*day, StyleRef: "标注规则要写清楚, 换人能复用",
				Items: []model.RequirementItem{
					{ID: "R1", Desc: "去重与手机号/地址脱敏"},
					{ID: "R2", Desc: "按 12 个意图分类的标注集"},
					{ID: "R3", Desc: "意图分布报告"},
				},
			},
		},
		{
			ID: "T-DEMO-005", RequesterID: "R002", Title: "产品宣传短视频剪辑 30 秒",
			Category: "video", BudgetCents: 150000,
			Spec: "素材已有, 需要剪出 30 秒竖版成片, 带字幕与节奏点, 适配抖音和小红书两个尺寸",
			SpecObj: model.TaskSpec{
				Deliverables: "成片 2 个尺寸 + 字幕文件", RequiredFormat: "MP4 (1080x1920 / 1080x1440)",
				DeadlineAt: now + 5*day, StyleRef: "前 3 秒必须有钩子, 不铺垫",
			},
		},
		{
			ID: "T-DEMO-006", RequesterID: "R003", Title: "电商订单对账脚本 + 每日报表",
			Category: "backend", BudgetCents: 360000,
			Spec: "对接平台订单导出与支付流水, 每天自动核对差异并生成报表, 差异条目要能定位到订单号",
			SpecObj: model.TaskSpec{
				Deliverables: "对账脚本 + 定时任务 + 报表模板", RequiredFormat: "Python + cron / 云函数",
				DeadlineAt: now + 10*day, StyleRef: "出错要能重跑, 不要人工补数",
			},
		},
	}

	for _, t := range demo {
		if _, exists := st.GetTask(t.ID); exists {
			continue // 认领过的单不重置, 否则重启一次演示就回到解放前
		}
		t.Status = model.TaskOpen
		t.CreatedAt = now
		t.Clarifications = model.ClarifySpec(t.SpecObj)
		st.PutTask(t)
	}
}
