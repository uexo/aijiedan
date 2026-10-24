# Zanbuddy · M0 技术骨架

> ⚠️ **本 README 的目录树停留在 v1（`internal/handler` + `internal/service`），已过期。**
> 实际代码为 v1/v2 混合态共 21 个包，`/api/v2/*` 端点全在 `internal/httpapi`。
> 环境、路径、代码结构、踩坑清单以 **`../项目事实基准.md`** 为准。

AI 接单平台（连接需求方与 AI 开发者的任务众包平台）的 **M0 最小可跑骨架**。
目标：在"不碰真实资金"的前提下，跑通 **发单 → 匹配 → 接单 → 托管 → 交付 → 验收 → 结算** 主链路，并把**合规校验与拦截逻辑**植入关键节点。

> 合规是 P0 一票否决项。本骨架默认仅 KYC 通过，ICP 备案 / 税源地 / 支付托管三项合规卡点默认关闭（红灯）。通过运营接口翻转模拟"合规线落地"，未落地时交易被拦截（HTTP 409）并指明对应执行卡。

## 技术栈

- **Go 1.27**（标准库 + `go-sql-driver/mysql`；内存模式仍零外部依赖，仅 MySQL 模式引入驱动）
- 存储：内存（默认）或 MySQL（设 `ZANBUDDY_DB_DSN` 切换持久化），DDL 见 `migrations/schema.sql`
- 资金托管 stub（生产替换为微信「担保交易 + 服务商分账」）
- IM stub（`Service` + `Provider`/`Moderator` 可切换接口，默认内存总线 + SSE，生产替换为腾讯云 IM；消息已落库持久化，重启不丢）
- 轻量 HS256 JWT（仅标准库，生产接真实 KYC 体系）

## 目录结构

> 实际为 **v1/v2 混合态**：`internal/handler` + `internal/service` 是 v1 遗留，
> 全部 `/api/v2/*` 端点位于 **`internal/httpapi`**。找 v2 路由请直接看 `httpapi/router.go`。

```
zanbuddy/
├── go.mod                        # module zanbuddy（依赖仅 mysql driver + x/crypto）
├── main.go                       # 入口: 装配各模块 + 种子 + 启动 :8080（ZANBUDDY_PORT 可覆盖）
├── zanbuddy.exe                  # 构建产物（测试脚本启动它，禁用 go run）
├── db_reset.py                   # 重置库；--demo 预置合规四门全开
├── migrations/
│   ├── schema.sql                # 建表打底（顺序敏感：ALTER 必须在其 CREATE 之后）
│   ├── 002_light_trade.sql       # 轻交易模式
│   └── 003_match_reason.sql      # 匹配理由表
├── internal/                     # 21 个包
│   ├── model/                    # 领域模型（Task 状态含 withdrawn）
│   ├── store/                    # Store 接口 + MemStore(store.go) + MySQL(mysql.go)，行为必须对齐
│   │
│   ├── ── v2 主干 ──
│   ├── identity/                 # 用户与供给方档案
│   ├── demand/                   # 发单 Publish / MyTasks / Withdraw
│   ├── trade/                    # 订单状态机（state.go 四态映射）
│   ├── match/                    # 匹配：factors 五因子 / reason 三类锚点理由 / service 召回裁剪
│   ├── httpapi/                  # ★ /api/v2/* 全部端点 + drain.go（修未读 body 的 RST）
│   │
│   ├── ── v1 遗留 ──
│   ├── handler/                  # 旧 REST + console/mobile 内嵌页（勿在此找 v2）
│   ├── service/                  # 旧主链路
│   ├── compliance/               # 合规闸门（409 拦截 + 翻转）
│   ├── payment/                  # 托管 stub（冻结/分账/退款）
│   ├── im/  notify/  kyc/        # IM / 通知中心 / 实名通道
│   │
│   ├── ── AI 相关 ──
│   ├── clarify/  reqai/  quote/  faq/  aihttp/
│   └── auth/  middleware/        # HS256 JWT 鉴权
│
├── web/                          # 静态前端（Go http.FileServer 托管于 /static/）
│   ├── landing.html              # 唯一入口：全部 CTA 已指向 app/login.html
│   ├── *.html + design-system.css    # v1 剩余页面（task/admin/org/auth，运营与旧流程用）
│   └── app/                      # ★ v2 前端：零构建 ES module，接 /api/v2
│       ├── assets/runtime.js     #   api / render / form / router
│       ├── login.html  hall.html  publish.html  profile.html
│       └── _flow.mjs             #   Playwright 流程测试（66 项）
│
└── test/
    ├── run_all_m0.sh             # 一键九套回归（每条前重置库 + 干净重启）
    ├── _verify_entry.mjs         # 入口与身份预选回归（6 项）
    ├── test_m0_v2/hall/publish/matches/claim/confirm/deliver/accept/withdraw.py
    └── smoke_test.py             # 主链路冒烟
```

### 前端入口（2026-10-24 收敛）

只有一条路径：`landing.html` → `app/login.html`（可带 `?role=demand|supply` 预选身份）→ `app/hall.html`。

v1 的 `auth.html` / `app.html` / `task.html` 仍在仓库，但落地页已不再指向它们；
运营后台走 `landing.html` → `admin.html`。静态原型目录 `web/v3/` 已删除（可从 git 历史恢复）。

## 系统前端重设计（2026-10-11，董事长拍板「重设计整个系统页面」）

基于 `ui-ux-pro-max` 设计系统（Glassmorphism 浅色 · 信任蓝 `#2563EB` · 强调橙 `#EA580C` · Plus Jakarta Sans），将 Zanbuddy 前端从 MVP 单文件深色控制台升级为完整系统页面套件（零构建、纯静态、同源调用既有 REST API）：

- **`design-system.css`**：统一设计 token + 组件类（玻璃卡片 / 状态药丸 / 侧边导航 / 统计卡 / 开关 / Toast）。
- **`landing.html`**：公开落地页（根路径 `/` 重定向至此），讲清语义匹配 / AI 澄清 / 资金托管 / 合规闸门价值主张与三角色。
- **`auth.html`**：登录 / 注册，角色感知（需求方 / 接单方 / 运营），运营登录直达 `admin.html`。
- **`app.html`**：需求方与接单方双角色工作台（发单 / 任务 / 智能推荐 / 订单 / 钱包 / 画像 + 组织管理入口）。
- **`task.html`**：任务详情 + 订单全生命周期闭环（托管 / 交付 / 验收结算 / 打回 / 评价）。
- **`org.html`**：组织 / 企业版治理（组织钱包充值与提现、组织发包、成员邀请、提现审批、开放 API Key + Webhook）。
- **`admin.html`**：运营后台（运营看板指标、上线就绪 Checklist、合规四门开关、提现审批、任务 / 订单监管）。
- 路由更新：`/` → `/static/landing.html`；`/console` → `/static/admin.html`；旧 `index.html` 改为重定向保留。

## 验收清单比对机制（2026-10-11，B 方案·规范结构化）

董事长明确验收顺序：**先逐项比对需求清单与完成清单，仅在"需求与完成清单严重不符"时才进入纠纷仲裁**，而非一来就仲裁。据此把验收做成"标尺 + 比对 + 兜底"三层：

- **需求清单结构化**：发单时需求方逐条列出 `RequirementItem`（服务端分配 `R1/R2…` ID），落 `task.items_json`，作为验收标尺。
- **交付按项对齐**：接单方交付时按需求项逐条填 `DeliveryItem`（完成与否 / 说明 / 链接），落 `delivery.items_json`。
- **验收逐项留痕**：需求方验收时对各需求项勾选达标 / 不达标，写入 `Order.Acceptance`（`acceptance_json`）；可勾选"严重不符"。
- **三层处理**：轻微不达标 → 打回重做（中间层，受 `MaxRevisions` 上限保护，已生效）；**严重不符**才出现「发起纠纷」入口 → 运营后台「纠纷仲裁」面板按退款比例结案（`/orders/{id}/resolve`）。
- **数据层**：`migrations/schema.sql` 增 `order.acceptance_json/severe_mismatch/accepted_at`、`task.items_json`、`delivery.items_json`；`mysql.go` 同步 Put/Get/List 与 `scanOrder`；`GET /orders/{id}` 现返回关联 `task`（前端比对所需）。
- **前端**：`app.html` 发单页加"需求清单"逐行输入；`task.html` 接单方交付按项填写、需求方验收渲染两栏比对表 + 逐项达标勾选 + 严重不符判定；`admin.html` 新增「纠纷仲裁」标签页（列出 disputed 订单、填退款比例 + 裁决说明结案）。
- 验证：MySQL 真源模式重建库、起服、curl 跑通 `发单(带清单)→接单→托管→按项交付→清单比对验收→结算` 与 `交付缺失→发起纠纷→运营仲裁全额退款`，钱包账（托管 / 结算 / 纠纷退款）均正确。

## 部分验收打回机制（2026-10-11，未达标不结算）

验收存在未达标项时，**订单不再结算、不打完成标签**，自动打回接单方修正后重新提交：

- **后端**（`service.go AcceptDelivery`）：逐项验收结果中存在 `pass=false` → 记录留痕后订单回到 `delivering`、最新交付标记 `rejected`、**不触发结算**，并站内信通知接单方未达标项清单；全部达标才走原结算链（合规闸门 → 放款 → completed）。`SettleResult` 新增 `RevisionRequested` 标记，handler 据此返回"已打回修正"消息。
- **前端**（`task.html`）：验收按钮文案随勾选联动——全达标显「验收并结算」、有未达标显「提交验收（未达标项将打回修正）」；打回后双方可见「验收比对结果（修正中）」卡片（达标项 ✓ / 未达标项 ✗ + 修正提示），接单方在下方重新提交交付。
- **资金安全**：未达标打回期间托管资金持续冻结、双方钱包分文不动；直至全达标结算按原规则放款（10% 佣金）。
- **纠纷结案单显示修正**（同日）：经仲裁结案的订单不再误显"验收通过，已结算"——状态徽标显「仲裁结案」，无逐项验收记录的纠纷单隐藏比对区（不再默认全部达标），横幅区分 正常验收 / 严重不符 / 仲裁结案 三态。
- 验证：MySQL 真源端到端实测 `发单(3项)→接单→托管→首轮交付→部分验收(R3未达标)→打回(delivering/未结算/钱包未动)→修正重交→全达标验收→结算(completed/P001+¥360/R001托管释放)`，10% 佣金正确；`go build`、内嵌 JS 语法校验通过。

## 启动

```bash
cd zanbuddy
go build -o zanbuddy.exe .        # 或 go run .
# 监听 http://localhost:8080
```

### 接入真实 MySQL（持久化）

默认用内存存储。要接外部 MySQL（如 `192.168.3.18:3306` 上的 Docker MySQL），按三步：

**1. 建库建表**（用仓库内置脚本，免装本机 MySQL 客户端；直连 `192.168.3.18` 的 Docker MySQL 8.0.46）：

```bash
# 重置（DROP+CREATE+应用 migrations/schema.sql）；--check 仅校验连通性/版本
python db_reset.py

# 推荐：--demo 重置并把合规四门预置为「dev/demo 就绪态」全开
python db_reset.py --demo
```

> 💡 **`--demo` 的用途**：出厂默认仅 KYC 通过，`icp`/`tax_source`/`escrow` 关闭，会硬拦截接单/托管/结算，导致冒烟跑不通整条交易链。`--demo` 即省去每次手工 `POST /api/v1/compliance` 开三道门（实现方式：重置后预置 `compliance_state` 四行全开，服务器启动时 `Seed()` 用 `INSERT IGNORE` 不会覆盖）。
> ⚠️ **这只是开发/演示态的「就绪开关」，不代表真实合规落地。** 真实合规（ICP 备案 / 税源地签约 / 支付托管开通）仍是上线前 checklist，不因 `--demo` 而减免。

> ⚠️ **环境纪律**：MySQL 唯一真源是 **192.168.3.18 上的 Docker 实例（8.0.46）**，**严禁使用本机 MySQL 5.7**（既不作数据库、也不作管理客户端）。数据面由 Go 应用 DSN 直连 192.168.3.18；控制面（建表/重置）统一走 `db_reset.py`（托管 Python venv + pymysql，见仓库根目录）。

**2. 确认远程连接授权**（Docker MySQL 默认 `root@localhost`，外部连需授权）：

```sql
CREATE USER IF NOT EXISTS 'zanbuddy'@'%' IDENTIFIED BY '你的密码';
GRANT ALL PRIVILEGES ON zanbuddy.* TO 'zanbuddy'@'%';
FLUSH PRIVILEGES;
```

**3. 设置 DSN 启动**：

```bash
# Windows
set ZANBUDDY_DB_DSN=zanbuddy:你的密码@tcp(192.168.3.18:3306)/zanbuddy?parseTime=true&charset=utf8mb4
go run .
```

- DSN 中 **不要硬编码密码到代码**，一律走环境变量。
- 启动后 `NewMySQL` 会自动 `Seed()` 预置演示用户（P001–P004/R001/A001）与合规默认状态（幂等，重复启动无害）。
- 合规开关翻转（admin 调 `/compliance`）会**持久化**到 `compliance_state` 表，重启不丢。
- 若连接失败，报错会提示「请确认 192.168.3.18:3306 可达、账号密码正确、库已建」。

### ✅ 已验证（2026-10-04）

本机 Docker MySQL（`192.168.3.18:3306`，MySQL 8.0.46）**已允许 root 远程访问**，开发期可直接用 root 连，省去建专用账号：

```bash
# Windows（密码走环境变量，勿硬编码进代码/仓库）
set ZANBUDDY_DB_DSN=root:你的密码@tcp(192.168.3.18:3306)/zanbuddy?parseTime=true&charset=utf8mb4
go build -o zanbuddy.exe . && zanbuddy.exe
```

验证结果：`migrations/schema.sql` 建库建表成功；启动后 `NewMySQL` 自动 Seed；`smoke_test.py` **53/53 PASS**；落库核对 user / task / match_record / order(completed,settled=1) / delivery / review 表全部持久化。证明持久化、合规闸门、真实用户体系（注册/账号密码登录/RBAC 订单隔离）、评价信用闭环（双向评价→信用分重算→持久化）与需求结构化（结构化 spec + 缺字段澄清兜底）在真实 MySQL 下闭环无误；P1-1 智能匹配（词袋余弦语义召回 + 多因子精排 + 评价反馈闭环）已通过 53/53 冒烟验证。

> 生产环境仍建议按上面第 2 步创建最小权限专用账号（`zanbuddy@'%'`），不要用 root。

### ✅ 已验证（2026-10-06，P1-2 收尾）

P1-2 AI 澄清落地：新增 `internal/clarify` 包（零依赖规则化 Stub 兜底 + 可选 OpenAI 兼容 LLM，env 配置），自然语言需求 → 多轮对话 → 结构化 spec，衔接 P0-5 规则澄清。修复两处真实缺陷：① `parseFloat` 整数预算解析产生 `int64` 最小值（NaN 乘积）→ 已修；② `spec_complete` 误用含可选风格建议的 `ClarifySpec` 致必填齐备仍判不完整 → 新增 `RequiredClarifySpec` 仅判必填三项。内存模式 `smoke_test.py` **72/72 PASS**（含 P1-2 自然语言抽取、缺项澄清、多轮补充转完整、一键发布、403 越权）。服务以内存模式后台运行于 http://localhost:8080 可演示。

### ✅ 已验证（2026-10-06，P1-4 订单完整化）

P1-4 订单完整化落地：订单模型扩展里程碑（`milestones` JSON）、改稿上限（`max_revisions`，默认 3）、结算模式（`lump`/`milestone`）、取消/退款/纠纷字段；`service` 新增 `CancelOrder`（按比例退款：托管后取消退 50%）、`OpenDispute`、`ResolveDispute`（运营按 `refund_pct` 退款结案）、`ConfirmMilestone`（分阶段放款）、`RequestRevision`（受改稿上限约束，超限 409）；`handler` 接线 6 个新端点，越权（非参与方/非运营）前置返回 403、状态不符返回 409。线上 MySQL 已 ALTER 同步新列。内存模式 `smoke_test.py` **106/106 PASS**（含 P1-4 改稿上限拒绝、取消退款比例、里程碑分阶段结算、纠纷发起/运营结案退款、越权 403）。服务以内存模式后台运行于 http://localhost:8080 可演示（控制台订单详情新增「P1-4 订单操作」区）。

### ✅ 已验证（2026-10-06，P1-3 移动端核心页）

P1-3 移动端核心页落地：新增 `internal/handler/mobile.html`（移动端优先响应式 H5，底部四 Tab：大厅 / 发单 / 订单 / 我的·钱包），复用现有 REST API，无需新增业务逻辑。后端新增 `GET /api/v1/auth/me`（返回身份与信用分，供钱包页展示）与 `GET /m` 路由（serve H5）；运营登录后 H5 内置合规开关（`/compliance`），可移动端自闭环演示「发单 → 接单 → 托管」。接口已验证：`/m`=200、`/me` 未登录 401 / 登录后返回 `credit_score`；内存模式 `smoke_test.py` **106/106 PASS** 无回归（P1-3 为纯前端工程，不改变既有 API 口径）。服务以内存模式后台运行于 http://localhost:8080，移动端页 http://localhost:8080/m 可演示。

### ✅ 已验证（2026-10-06，P2-1 钱包/流水/提现骨架）

P2-1 资金账户体系落地（严守「不二清」红线，真实资金通道仍 stub，待合规线 C3 落地切换）：
- **数据模型**：`model` 新增 `Wallet`（余额/冻结）、`Ledger`（资金流水对账）、`Withdrawal`（提现申请）及 `TxType`/`WithdrawStatus` 常量。
- **通道层**：`payment.Service` 重构为持有 `Provider` 接口——`StubProvider`（默认，零依赖）+ `WeChatProvider`（骨架，env 配置商户号，真实 API 留 TODO），对外签名不变，service 调用点零改动。
- **账户/接口**：`store` 接口 + 内存/MySQL 双实现同步 wallet/ledger/withdrawal；`service` 在托管/结算/退款/纠纷/里程碑全链路接入钱包记账（需求方扣款冻结、接单方放款、平台佣金、退款回补、提现预占/审批）；`handler` 新增 `GET /wallet`、`POST /wallet/withdraw`、`GET /wallet/withdrawals`、`POST /wallet/withdrawals/{id}/approve`；`schema.sql` 新增 wallet/ledger/withdrawal 三张表（待董事长确认后在线上库执行）。
- **前端**：`console.html` 新增「④-B 钱包与提现」卡片（本人余额/冻结 + 最近流水 + admin 提现审批）；`mobile.html`「我的」页改为调 `GET /wallet` 展示真实余额/冻结 + 提现表单 + 最近流水。

接口与记账已验证：托管扣可用余额并增冻结、结算放接单方+平台佣金入账、提现申请冻结预占、admin 审批后余额扣减+解冻、纠纷退款回补需求方；内存模式 `smoke_test.py` **125/125 PASS**（新增 P2-1 全链路 19 项断言，含非运营审批 403、退款净额校验）。服务以内存模式后台运行于 http://localhost:8080 可演示。

> **诚实边界**：当前 `WeChatProvider` 为骨架，不碰真实资金；演示钱包是平台内记账模型，非真实银行余额。真实「微信支付担保交易 + 企业付款到银行卡 + 对账」需合规线 C3（支付托管资质）落地后切换，且届时须由真实签约状态驱动合规闸门，而非 admin 手动翻转。

### ✅ 已验证（2026-10-18，P3 资金通道可插拔：拉卡拉分账通 fail-closed）

合规线 C3 二清红线的技术解落地——平台「付款→托管→验收放款+抽佣」若由平台自有账户经手资金即触碰二清（国务院令768号），唯一合规解是**持牌机构分账**：资金全程留在监管账户，平台只下发分账指令。

- **新增 `LakalaProvider`**（`internal/payment/lakala.go`）：拉卡拉分账通真实形态骨架（托管下单/分账/退款/异步通知 + v3 风格 RSA-SHA256 签名）。`ZANBUDDY_PAY_PROVIDER=lakala` 且五项凭证齐全才 live=true 真正发起 HTTPS；**凭证不全时 fail-closed**——托管/分账/退款一律报错并列出缺失凭证，绝不静默降级（合规生命线，静默降级 = 假装有资金监管，比报错危险）。
- **通道显式选择**：`ZANBUDDY_PAY_PROVIDER = lakala | wechat | stub`；未设保持旧行为（设了微信商户号→微信，否则 stub）。`readiness` 的 `payment{mode,live,missing}` 自动上报新通道。
- **红线标注**：微信原生分账仅能分出订单额 **30%**，余款滞留平台商户号 = 仍触碰二清红线，生产不可用；wechat 通道降级为对照/过渡用途。
- **验证**：`test_p3_payprov.py` **17/17**（lakala 缺凭证时 readiness 上报、托管 409 拒绝含「拉卡拉」+ fail-closed 语义、订单不进 active、需求方钱包分毫未动）；全量回归 **147/147 全绿**（ECON 55 / ORG 58 / B3 17 / C1 17，MySQL 真源双跑）。
- **待外部**：拉卡拉签约（前置 ICP 备案 + 对公一类户）完成后填入 `ZANBUDDY_LAKALA_*` 即切换生产通道，代码零改动。

### ✅ 已验证（2026-10-06，P2-2 即时通讯骨架）

P2-2 即时通讯落地（解决 M0「重启丢消息、无会话列表/已读/审核」硬伤，严守平台内闭环，真实通道仍 stub，待合规线 C3 落地切换）：
- **通信层升级**：`internal/im` 由内存 `Bus` 重构为 `Service`，持有 `Provider` 接口（`StubProvider` 默认 + `TencentIMProvider` 骨架，env `ZANBUDDY_IM_PROVIDER=tencent` 切换）与 `Moderator` 接口（`StubModerator` 默认拦截规避平台交易词 + `TencentModerator` 骨架，env `ZANBUDDY_IM_MODERATOR=tencent` 切换）；对外签名不变，service 调用点零改动。
- **持久化**：`ChatMessage` / `Conversation` 落 `store`（内存 + MySQL 双实现）；`Send` 流程为 校验 → 审核 → 落库 → 触碰会话 → SSE 推送 → 异步调 Provider；命中规避词（微信/vx/私下/转账/支付宝/银行卡/电话 等）返回 `(nil, reason)` 被 400 拦截。
- **订单联动**：`AcceptMatch` 末尾自动 `SendSystem` 建会话并补 `order_id`，发单→接单后双方即在会话中可沟通交付细节。
- **端点**：`POST /im`（发，拦截返 400 + reason）、`GET /im?peer=`（SSE 首屏历史 + 实时）、`GET /im/conversations`（列表含未读）、`GET /im/conversations/{peer}`（历史）、`POST /im/conversations/{peer}/read`（已读清零）；权限前置 401。
- **前端**：`mobile.html` 新增第 5 个 Tab「💬 消息」（会话列表 + 聊天 + 进入即已读 + 2s 轮询规避 SSE 鉴权限制）；`console.html` 新增「④-C 即时通讯」卡片（会话列表/聊天/发送，提示试发"微信"被拦截）。

接口与链路已验证：消息收发落库、接收方未读 +1、进入会话已读清零、违规词被审核拦截、订单接单自动建会话补 `order_id`；内存模式 `smoke_test.py` **137/137 PASS**（新增 P2-2 全链路 12 项断言）。服务以内存模式后台运行于 http://localhost:8080 可演示。

> **诚实边界**：当前 `TencentIMProvider` / `TencentModerator` 为骨架（真实 API 留 TODO）；演示为平台内记账消息 + 本地总线推送，真实腾讯云 IM 通道、离线推送、已读回执与内容审核回写需合规线 C3 落地后切换。

### ✅ 已验证（2026-10-06，P2-3 通知中心骨架）

P2-3 通知中心落地（作为 IM 的自然延伸，严守平台内闭环，真实通道仍 stub，不依赖外部资质即可演示）：
- **通道层**：`internal/notify` 包 `Service` 持有 `Provider` 接口——`StubEmailProvider` / `StubSMSProvider`（默认 nil，env `ZANBUDDY_NOTIFY_EMAIL=1` / `ZANBUDDY_NOTIFY_SMS=1` 切换骨架）;`inapp` 站内信默认不经 Provider 直接落库。对外签名与 IM 一致（`Notify(uid,channel,title,body)`），service 调用点零改动。
- **数据模型**：`model` 新增 `Notification`（uid/channel/title/body/status/read_at）+ `ChannelInApp/Email/SMS` 与 `NotiUnread/Read` 常量；`store` 接口 + 内存/MySQL 双实现新增 5 方法（`SaveNotification/GetNotification/ListNotifications/MarkNotificationRead/CountUnread`），`schema.sql` 新增 `notification` 表。
- **业务事件接入**：`service` 在接单（`AcceptMatch` 双方）、交付（`SubmitDelivery` 待验收）、验收结算（`Settle` 正常双方 + 被合规拦截分支"结算待合规"）、纠纷（`OpenDispute` 通知对方 + 运营 A001 / `ResolveDispute` 结案双方）、提现审批（`ApproveWithdraw` 通过/驳回）处调用 `Notifier.Notify(...)`，形成"动作即通知"自动闭环。
- **端点**：`GET /api/v1/notifications`（倒序列表，默认 50）、`GET /api/v1/notifications/unread`（未读数）、`POST /api/v1/notifications/{id}/read`（标记已读，越权返回 404）。
- **前端**：`console.html` 新增「④-D 通知中心」卡片（未读角标 + 列表 + 标记已读）；`mobile.html`「💬 消息」Tab 内嵌通知列表 + 红点 badge + 标记已读（2s 轮询，与 IM 同源规避 SSE 鉴权限制）。

接口与链路已验证：接单/交付/结算/纠纷/提现均自动生成站内信、接收方未读 +1、列表含 inapp 未读项、标记已读后未读数 -1 且 status=read、跨用户标记被 404 拒、未登录查通知被 401 拒；内存模式 `smoke_test.py` 当前累计 **204/204 PASS**（P2-3 全链路 11 项 + P3 组织骨架 9 项 + Marketplace 发现/声誉 8 项 + 类目法/可信分 4 项 + 主动接单 4 项 + 任务详情/工作台 6 项 + P3 运营看板 3 项 + P3 专项 13 项）。服务以内存模式后台运行于 http://localhost:8080 可演示（控制台/移动端均可见通知中心、需求大厅筛选、任务详情与我的工作台、运营平台看板、组织钱包/发包/审批/开放 API）。

> **诚实边界**：当前 `StubEmailProvider` / `StubSMSProvider` 为骨架（真实 SMTP/短信 API 留 TODO）；演示以站内信（平台内落库）为主通道，真实邮件/短信推送需合规线 C3 落地后切换。通知与 IM 共同构成"平台内闭环触达"，外部通道切换同一范式（Stub 默认 + env 切换真实 Provider）。

### ✅ 已验证（2026-10-06，P3 企业版治理最小骨架）

P3 进入**设计阶段落地最小骨架**：先固化多租户数据模型（Org / OrgMember / RBAC 四角色）与组织级增删查接口，验证可编译、可演示、不与 M0 个人/C2C 链路耦合。完整架构见 `../P3企业版治理设计.md`。

- **数据模型**：`model.Org`（组织/企业租户，owner 为组织主，作为统一结算/开票主体走合规线 C）、`model.OrgMember`（org_id + user_id + role）；RBAC 角色 `owner / admin / member / viewer`。
- **存储契约**：`Store` 接口新增 6 方法（`SaveOrg` / `GetOrg` / `ListOrgsByOwner` / `SaveOrgMember` / `ListOrgMembers` / `GetOrgMember`），内存与 MySQL 双实现；`migrations/schema.sql` 新增 `org` + `org_member` 两表（含联合主键与索引）。
- **业务接口**：`service.CreateOrg`（创建者自动成为 owner）、`InviteOrgMember`（仅 owner/admin 可邀请，角色白名单校验）、`ListOrgs`、`ListOrgMembers`。
- **端点**：`POST /api/v1/orgs`（创建）、`GET /api/v1/orgs`（列出我作为 owner 的组织）、`GET /api/v1/orgs/{id}/members`（列成员）、`POST /api/v1/orgs/{id}/members`（邀请，需 owner/admin）。
- **诚实边界**：仅落地"组织创建/邀请/列表"示意接口，验证多租户数据模型与 RBAC 字段可用。**组织级发包、组织钱包、审批流、运营看板、开放 API + Webhook 均未实现**，由独立 P3 专项落地；当前不与 M0 主链路强耦合，不影响 P0–P2 演示。

### ✅ 已验证（2026-10-07，Marketplace 成熟度：需求大厅检索 + 用户声誉画像）

纯代码、不卡合规的 marketplace 能力补全：把「需求大厅」从「返回全部 open 任务」升级为可筛选/可搜索/可排序，并新增用户公开声誉画像端点（信用分/技能/历史评分/完成单数对外可见），复用既有 store 方法（评价/订单/用户）。

- **需求大厅检索**：`GET /tasks` 新增查询参数 `category`(类目)、`status`(状态)、`min_budget`/`max_budget`(预算分)、`q`(关键词命中 title/spec)、`sort`(newest/budget_desc/budget_asc)、`limit`；`store.TaskFilter` + `SearchTasks` 内存/MySQL 双实现（MySQL 参数化动态 WHERE + 白名单排序，防注入）。
- **用户声誉画像**：`GET /users/{id}`（公开，无需登录）→ 聚合 `PublicProfile`（id/name/role/skills/credit_score/avg_rating/review_count/completed_count/kyc/tax_bound），由 `GetUser` + `ListReviewsByReviewee` + `ListOrdersByUser` 聚合，无新增存储方法。
- **编码准则**：默认（无参数）仍仅返回 open 任务、按创建时间倒序，向后兼容；handler 新增 `atoi64` 安全解析、`store` 包导入。
- **诚实边界**：检索为内存/SQL 两层实现，已验证筛选/搜索/排序/预算过滤语义；画像为只读聚合，未引入新写入面。

### ✅ 已验证（2026-10-07，Marketplace 可信度与分类法）

让刚上线的需求大厅真正可用：前端有了「合法类目清单」的唯一事实来源，需求方有了「一眼排序 provider」的综合可信分。

- **类目法 taxonomy**：`model.TaskCategories`（12 项：nlp/copywriting/design/frontend/backend/mobile/data/ml/devops/audio/video/other）+ `IsValidCategory` 校验；`GET /meta/categories`（公开，无需登录）返回 id/label/desc，作为筛选下拉与发单类目白名单。
- **发单类目硬约束**：`PublishTask` 空类目默认归 `other`、非法类目被 `compliance.Block` 拦截（返回友好提示含端点指引），类目法成为唯一事实来源。
- **综合可信分 trust_score**：`GetPublicProfile` 新增 `trust_score`（0–100，透明公式：服务质量 avg_rating/5×40% + 交付经验 min(completed,20)/20×30% + 信用 credit/100×30%），与匹配分（MatchTask 五因子）相互独立，供需求方排序 provider。
- **编码准则**：类目法集中在 `model`，校验在 `service.PublishTask`，端点挂独立 `meta` 路由（与 `users` 平级），纯读无新写入面。
- **诚实边界**：类目法为静态枚举（新增类目改 `model.TaskCategories` 即可）；trust_score 不含税源/语义因子（那些属于撮合匹配分，避免概念混淆）。

### ✅ 已验证（2026-10-07，主动接单：需求大厅 → 认领）

补全核心 marketplace 闭环里「需求方浏览大厅 → 接单方主动抢单」这一环（此前仅有 AI 撮合 `MatchTask` 一条路径）。

- **主动认领端点**：`POST /tasks/{id}/take`（仅接单方）→ `service.TakeTask` 直接生成订单（待托管 `pending_escrow`），不经过 AI 推荐；与 `AcceptMatch` 共享订单结构、IM 会话、站内信通知。
- **约束**：任务须为 `open` 且非本人发布（先到先得，接单后转 `matched`）；支持里程碑分阶段结算（金额和须等于预算）；受 `ActAcceptTask` 闸门（KYC 用户级 + 税源 平台级+用户级）。
- **编码准则**：`handler` 在 `handleTasks` 新增 `take` 子路由（与 `match` 平级），复用 `AcceptResult`/`compliance.Block`；订单 `MatchID` 留空以区分主动接单与 AI 撮合。
- **诚实边界**：主动接单与 AI 撮合是两条并存的接单路径，均汇流到同一订单/托管/结算流程；未引入新存储方法（任务/订单/用户/IM/通知均已有）。

### ✅ 已验证（2026-10-07，P3 运营看板 / 平台指标）

P3 企业版的「运营看板」最小可用版落地：为运营（A001）提供一份只读的平台指标快照，复用既有 store 聚合方法，不引入新写入面或合规闸门。

- **存储契约**：`Store` 接口新增 3 个聚合读取方法（`ListUsers` / `ListTasks` / `ListWallets`），内存与 MySQL 双实现补齐；并加入编译期接口断言 `var _ Store = (*MemStore)(nil)` / `(*mysqlStore)(nil)`——任一实现缺方法都会编译失败，保证 Store 契约齐备。
- **业务聚合**：`service.PlatformMetrics()` 汇总——用户按角色计数（providers/requesters/admins）、任务总数/开放数、订单总数/按状态分布/已结算数、GMV（已结算订单金额合计，分）、平台钱包总水位（全部 `balance+frozen` 合计，分）、提现总数/待审数、合规红灯数（未落地合规卡点计数）。
- **端点**：`GET /api/v1/admin/metrics`（仅运营可访问；非运营 403、未登录 401），返回 `PlatformMetrics` 快照。
- **诚实边界**：当前为内存态聚合（与 M0 一致）；MySQL 模式同样可用，但指标为实时查询非预计算。组织级发包/组织钱包/审批流/开放 API + Webhook 仍未实现，由独立 P3 专项落地。

接口与聚合已验证：运营查指标返回关键字段齐全且 `users_total≥6`、非运营 403、未登录 401；内存模式 `smoke_test.py` 当前累计 **204/204 PASS**（新增 P3 运营看板 3 项断言）。服务以内存模式后台运行于 http://localhost:8080 可演示（运营登录后可在控制台查看平台看板数据）。

### ✅ 已验证（2026-10-07，P3 专项深化：组织钱包 / 发包 / 审批 / 开放 API + Webhook）

P3 企业版从「最小骨架」推向可商用：组织作为统一结算主体，发包与结算全部走组织钱包，并补齐审批流与开放生态（API Key + Webhook）。

- **存储契约**：`Store` 接口新增 13 个方法（`GetOrgWallet` / `GetOrCreateOrgWallet` / `PutOrgWallet` / `SaveApproval` / `GetApproval` / `ListApprovalsByOrg` / `SaveAPIKey` / `GetAPIKeyByHash` / `ListAPIKeysByOrg` / `SaveWebhook` / `ListWebhooksByOrg` / `SaveWebhookDelivery` / `ListWebhookDeliveriesByOrg`），内存与 MySQL 双实现补齐；编译期断言继续保证契约齐备。
- **模型**：新增 `OrgWallet` / `Approval` / `APIKey` / `Webhook` / `WebhookDelivery`；`Task`/`Order` 增加 `OrgID`；`middleware.Claims` 增加 `OrgID` 以支持 `Bearer hgk_xxx` 组织凭证。
- **组织钱包与发包**：`GetOrgWallet` / `DepositOrgWallet`（仅 owner/admin）/`CreateOrgTask`（发布带 OrgID 的 open 任务，不预扣款）/`GetOrgTasks`。**设计决策**：组织任务走与个人任务完全相同的「接单→托管→结算」管线——`CreateEscrow` 按 `OrgID` 从组织钱包扣托管（并跳过个人 KYC 闸门，合规在 org 层），`releaseToWallets` 按 `OrgID` 释放组织钱包；接单订单复制 `OrgID`，避免重复下单。
- **审批流**：`RequestOrgPayout`（成员发起→冻结金额+生成 pending 审批单）/`ApproveOrgPayout`（owner/admin 审批通过→真实出账解冻并生成提现记录+流水；驳回→解冻）。
- **开放 API + Webhook**：`CreateAPIKey`（明文 `hgk_` 仅返回一次，存储 SHA256 hash）/`ListAPIKeys`；`ResolveAPIKey` 将 API Key 解析为组织 owner 凭证（handler `claims()` 支持 `Bearer hgk_` 前缀）；`CreateWebhook` / `ListWebhooks` / `DispatchWebhook`（事件后台投递，落库 `WebhookDelivery`，支持 `task_created`/`order_settled`/`payout_approved`）。
- **端点**：`GET/POST /orgs/{id}/wallet`、`POST/GET /orgs/{id}/tasks`、`POST /orgs/{id}/payouts`、`GET /orgs/{id}/approvals`、`POST /orgs/{id}/approvals/{aid}/approve`、`POST/GET /orgs/{id}/apikeys`、`POST/GET /orgs/{id}/webhooks`、`GET /orgs/{id}/webhook-deliveries`。
- **诚实边界**：组织订单跳过个人 KYC 闸门（设计意图：B 端统一结算主体，合规在 org 层），但组织自身的 KYC/ICP 落地尚未作为独立闸门接入；Webhook 投递为 fire-and-forget（演示失败标记 `failed`，不重试）。MySQL 模式已端到端验证：DDL 以 `migrations/schema.sql` 为唯一真源（所有 ID 列 `VARCHAR(32)`，以容纳 `ORG-`/`AP-`/`WH-`/`AK-`/`WDV-` 等长前缀 ID），接入前需先 `CREATE DATABASE` 并执行该文件（NewMySQL 仅 Seed 演示数据、不自动建表）。

接口与全链路已验证：组织钱包充值仅 owner/admin（非 owner 403）、组织发包生成带 OrgID 任务、provider 接组织单生成带 OrgID 订单、组织托管从组织钱包扣款且余额不足 409、组织提现审批闭环（owner 审批出账、非 owner 审批 403）、开放 API Key 生成且 `hgk_` 明文仅一次、API Key 鉴权查组织钱包 200、错误 Key 401、Webhook 订阅 + task_created 事件真实投递 success；内存模式 `smoke_test.py` 累计 **218/218 PASS**（P3 专项 13 项 + 方案3 Marketplace 深化 8 项断言 + 方案2 6 项 + 方案3做深 6 项）。**MySQL 模式同口径已端到端验证 218/218 PASS（2026-10-07，方案2+方案3做深同样双模式验证）**：直接核对线上库落库行数与组织钱包余额/冻结，确认充值→托管扣款→提现审批出账的资金流真实持久化（非仅靠内存）。服务以 MySQL 模式后台运行于 http://localhost:8080 可演示（组织 owner 登录后可在控制台操作组织钱包/发包/审批/API Key/Webhook）。

### 方案2 合规线C / 真实支付通道（2026-10-07，董事长拍板）

把 KYC / 支付托管从「stub + 手动翻转」推向可替换的真实通道抽象，并接线支付结果回调：

- **KYC Provider 抽象**（新增 `internal/kyc`）：定义 `Provider` 接口（`Submit` / `Status`），提供 `MockProvider`（dev 自动通过、无厂商资质可演示）与 `RealProvider` 骨架（`ZANBUDDY_KYC_PROVIDER=real` 切换）。`compliance` 闸门通过 `SetKYCProvider` 注入状态钩子，KYC 从「纯状态位 `u.KYCVerified`」升级为「可查询 Provider 状态」。新增端点 `POST /api/v1/kyc`（提交认证，模拟通道自动打标通过）、`GET /api/v1/kyc`（查状态）。
- **支付结果回调 webhook**（接线 `payment` 包既有微信 v3 骨架）：`payment.Service` 新增 `IsLive()` 与 `HandleNotify()`；真实通道下 `CreateEscrow` 仅冻结并令订单停在 `pending_escrow`，等待 `POST /api/v1/payments/notify` 回调解析出订单号与支付成败后再推进为 `active`（幂等：已是 active 直接视为成功）；模拟通道下同步记为 active，保证演示连贯。`WeChatProvider.Notify` 保留 AES-256-GCM 解密骨架（需补 `ZANBUDDY_WECHAT_APIV3_KEY`）。
- **Go-live 清单（上线前置，非代码）**：① ICP 备案（执行卡3）；② 微信支付商户号 + 服务商模式（执行卡2·担保交易 JSAPI）；③ 真实 KYC 厂商（阿里云/腾讯云实名）；④ 税源地合作主体（执行卡1）；⑤ 平台不沉淀资金（二清红线，资金经持牌通道冻结/分账）。

### 方案3 Marketplace 深化（2026-10-07，董事长拍板）

在已验证的 Marketplace 成熟度地基上深化「搜索 → 推荐 → 画像」闭环：

- **搜索索引**：`TaskFilter` 新增 `Skill`（技能/标签关键词，全文档匹配）、`DeadlineAfter`/`DeadlineBefore`（截止时间窗口）；MemStore 引入拉丁词倒排索引（`taskIndex`）快速收窄候选，中文走全文档子串匹配，内存 / MySQL（`LIKE` 多列）双实现一致。需求大厅 `GET /api/v1/tasks` 现已支持 `q`/`category`/`min_budget`/`max_budget`/`skill`/`deadline_after`/`deadline_before`/`sort`/`limit`，并新增 `sort=relevance`（接单方登录后按其与任务的匹配分降序，复用 AI 撮合口径）。
- **推荐位（接单方 feed）**：新增 `Service.RecommendTasks`，复用与 AI 撮合相同的多因子精排 `score(p, t)`，对全量 open 任务打分返回 Top10（自动排除接单方自己发布的任务），形成「好服务 → 高曝光」闭环；端点 `GET /api/v1/recommend/tasks`（仅接单方，需求方 403）。
- **推荐位（需求方 feed）**：新增 `Service.RecommendProviders(taskID)`，给定任务用同一 `score(p, t)` 对全量接单方排序返回 Top10，端点 `GET /api/v1/recommend/providers?task_id=`（任意登录用户，需求方首页用）。
- **用户画像页前端**：`PublicProfile` 扩出 `recent_orders` / `recent_reviews`（近期订单双视角 + 收到的互评，各 5 条），单文件静态页 `web/profile.html` 已渲染这两个区块；由 Go 内置 `http.FileServer` 托管于 `/static/profile.html`，并加 CORS 中间件（`Access-Control-Allow-Origin: *`）。

接口与全链路已验证：搜索 `q=rag`/`skill=python` 命中、截止窗口 `deadline_after` 含 / `deadline_before` 排除、推荐位 P001 返回 6 条且 top 分 81.3、需求方访问推荐位 403、推荐接单方 P001 对某任务返回 2 条且 top 分 83.4、KYC 模拟提交自动通过、`/static/profile.html` 可访问 200、画像含 recent_orders/recent_reviews。**内存模式与 MySQL 模式 smoke 均 218/218 PASS（方案2 新增 6 项 + 方案3做深新增 6 项断言，双模式通过）。**

### 方案3 前端做深（2026-10-07，董事长拍板「方案3 前端做深」）

把已验证的「搜索 → 推荐 → 画像」后端闭环在 UI 层串成可演示的前端（纯静态单页，不卡合规）：

- **Marketplace 枢纽页 `web/index.html`（新，根路径 `/` 入口）**：单文件 SPA（深色主题、自带 API 调用、零构建）。顶部演示账号登录（R001 / P001~P004 / A001）并角色感知。
  - **需求大厅**：关键词 `q` / 类目（下拉来自 `/meta/categories`，单一事实来源）/ 技能 `skill` / 预算区间 / 排序（最新 或 **接单方专属「相关度」**）检索，卡片展示类目 / 预算 / 状态 / 需求方 / 时间。
  - **相关度可视化**：接单方登录后，大厅卡片叠加 `/recommend/tasks` 的多因子匹配分（条形 + 可解释理由）；选「相关度」排序即按该分重排。
  - **推荐位（接单方「为你推荐」）**：登录为接单方时顶部 feed 直接拉取 `/recommend/tasks`（TopN，带匹配分条 + 理由），点击进任务详情。
  - **推荐位（需求方「推荐接单方」）**：进入任一任务详情，需求方可见「推荐接单方」面板（拉 `/recommend/providers?task_id=`，带可信分环 + 匹配分条 + 理由 + 画像链接）与「AI 撮合」按钮（POST `/tasks/{id}/match` 复用同口径）；接单方详情内可一键「我接这个单」（POST `/tasks/{id}/take`）。
  - 路由：根路径 `/` 302 到 `/static/`（目录索引即 `index.html`，单次跳转 200）；运营后台保留于 `/console`。
- **画像页交互增强 `web/profile.html`**：「近期订单（双视角）」「近期评价（收到的互评）」改为**可点击展开**（订单号 / 角色 / 金额 / 状态 / 创建时间、评价方 / 评分 / 内容 / 时间），新增「← 返回需求大厅」入口；公开接口仍无需登录。

实测（MySQL 模式在线）：`/`→`/static/` 单跳 200、`/console` 200、`/recommend/tasks`(P001) 返回 6 条（top 79.7）、`/recommend/providers?task_id=T-DEMO-001`(R001) 返回 2 条（P001 trust 75 / score 79.7，P003 trust 24 / score 30.7）、`/users/P001` 含 `recent_orders=5` / `recent_reviews=1`。后端端点此前已双模式 218/218 验证，前端为纯静态、同源调用，无新增后端改动。

### P3 运营侧前端（2026-10-07，董事长拍板「下一步 p3」）

把 P3 企业版已落地的后端（运营看板 + 组织治理全链路）在 UI 层串成可演示的前端。

- **运营 · 企业版单页 `web/admin.html`（入口 `/static/admin.html`，`web/index.html` 页脚已加链接）**：单文件 SPA（深色主题、沿用 `index.html` 的 `api()`/`login()` 范式、零构建）。顶部演示账号登录（R001 / P001~P004 / **A001 运营**）并角色感知，哈希路由 `#dash` / `#orgs` / `#org/{id}`。
  - **运营看板 `#dash`（仅 A001）**：拉 `GET /admin/metrics`，7 张指标卡（用户总数 / 任务 / 订单 / GMV / 平台资金水位 / 提现 / 合规红灯）+ 订单状态分布条形图；**平台合规闸门面板**（方案2 合规线 C 在 UI 落地，`GET /compliance` 查看、`POST /compliance {item,on}` 翻转 `kyc`/`tax_source`/`escrow`/`icp`，仅 A001）——接单需 `tax_source`、托管需 `escrow`、结算需 `icp`；非运营账号显示 403 提示。
  - **组织治理 `#orgs`**：创建组织（`POST /orgs`）、列出我名下组织；进入组织详情按成员角色动态显隐操作。
  - **组织详情 `#org/{id}`**：四角色 RBAC（owner / admin / member / viewer）驱动权限——概览（钱包余额/冻结/成员数/任务数）、成员（邀请，owner/admin）、任务与发包（发包，成员即可）、资金与审批（充值 owner/admin、提现申请 成员、审批 通过/驳回 owner/admin）、开放 API / Webhook（生成 API Key、订阅 Webhook、查看最近投递记录）。
- **后端路由修复（随本步一并落地）**：P3 骨架遗留两处 segment 计数写反导致 404——`POST /orgs/{id}/wallet/deposit`（应为 6 段误写 5）与 `GET /orgs/{id}/webhook-deliveries`（应为 5 段误写 6），已在 `internal/handler/handler.go` 修正，编译通过。

实测（MySQL 模式在线，全链路）：A001 看板 7 卡齐全（users=6 / tasks=1 / 平台资金水位=¥10000 / 合规红灯=3）；R001 建组织 → 邀请 P001 → 充值 ¥5000（wallet=500000）→ 发组织任务 → P001 提现申请 ¥800 生成审批单 → R001 审批通过（wallet 扣减至 420000，资金流真实持久化）→ 生成 API Key（`hgk_` 明文仅一次）→ 订阅 Webhook → 先订阅后发包验证 `task_created` 事件真实投递（deliveries n=1）。后端端点此前已双模式 218/218 验证；本步新增 2 个路由修复已编译并端到端复现。

#### P3 继续做深（2026-10-08，董事长拍板「下一步开发 p3」）

把企业版「组织任务 → 接单 → 托管」这条此前缺 UI 的闭环在 `admin.html` 上补完，并把方案2 合规线 C 的闸门接进运营看板。

- **组织任务接单 → 托管可视化（`#org/{id}` 新增「订单与托管」标签页 + 「任务与接单」页动作）**：
  - **任务与接单**：组织任务列表除展示状态外，按角色显隐动作——开放任务 + 当前登录为接单方成员 → 「接单」按钮（`POST /tasks/{id}/take`，生成带 `OrgID` 的订单，状态 `pending_escrow`）；已接单任务 + 当前为 owner/admin → 「托管（组织钱包扣款冻结）」按钮（`POST /orders/{orderID}/escrow`，从组织钱包扣款冻结、订单转 `active`）；其余角色给引导文案。
  - **订单与托管**：新增标签页拉 `GET /orgs/{id}/orders`（企业版统一结算视图），展示每笔订单的任务标题、接单方、金额、托管号、状态；与钱包「余额/冻结」联动呈现资金流。
- **后端新增 `GET /orgs/{id}/orders`**：`store.ListOrdersByOrg`（MemStore + mysqlStore 双实现）→ `service.GetOrgOrders` → handler 路由，补齐组织订单视图（此前缺此端点）。
- **运营看板加平台合规闸门面板**：A001 可在看板页一键「落地/关闭」`kyc`/`tax_source`/`escrow`/`icp`（方案2 合规线 C 的 UI 入口），落地后对应交易动作（接单/托管/结算）方可放行。

实测（MySQL 模式在线，全链路）：A001 落地 `tax_source`+`escrow` → R001 建组织/邀请 P001/充值 ¥5000 → R001 发组织任务 → **P001 接单**（order `pending_escrow`）→ R001 查组织订单（n=1）→ **R001 托管**（钱包 500000→470000、冻结 0→30000，订单转 `active`）。前端为纯静态、后端仅新增 1 个只读端点，编译通过。

#### P3 收尾：组织订单「交付 / 验收结算 / 补结算 / 评价」闭环 UI（2026-10-08，董事长拍板「继续下一步 p3 收尾」）

把组织「订单与托管」标签页从只读升级为按登录身份自适应的闭环操作台，复用既有订单生命周期 API（`POST /orders/{id}/deliver`、`/accept`、`/settle`、`/revise`、`/review`、`/milestones/{msID}/confirm`），零后端改动、纯静态前端。

- **订单卡片重做**：每笔订单展示任务标题、接单方、需求方、托管号、结算模式（`lump`/`milestone`）、金额、状态；动作区按当前登录身份（接单方/需求方/组织主）与订单状态动态渲染，给出明确的身份引导（如「验收需需求方 R001 登录」）。
- **一次性结算（lump）闭环**：`active/delivering` + 接单方 → **交付**（内联文本框提交，`delivered`）；`delivered` + 需求方 → **验收并结算**（`/accept` 触发 `Settle`，自动放款）或 **打回重做**（`/revise`）；验收若被合规拦截 → 订单转 `pending_settlement`，需求方/组织主可 **补结算**（`/settle`）；`completed` 双方可 **评价**（`/review`，刷新对方信用分）。
- **里程碑结算（milestone）兼容**：订单带 `milestones` 时逐阶段展示「✓已结算 / 待确认」与需求方 **确认阶段** 按钮（复用 `confirm-milestone`，全部完成自动 `completed`）；当前组织任务默认 lump，此分支为前向兼容。
- **交付记录查看**：任意状态可展开 `GET /orders/{id}` 拉取 deliveries，回看各轮交付内容与状态。
- **演示提示**：闭环需切换身份——接单/交付用 P001 等接单方登录，托管/验收/补结算用组织任务创建者（如 R001）登录，与运营看板合规闸门（先落地 `tax_source`/`escrow`/`icp`）配合。

实测（MySQL 模式在线，全链路）：A001 落地 `kyc`+`tax_source`+`escrow`+`icp` → R001 建组织/充值 ¥5000 → 发组织任务 → **P001 接单**（`pending_escrow`）→ **R001 托管**（钱包 500000→470000、冻结 30000，订单 `active`）→ **P001 交付**（`delivered`）→ **R001 验收结算**（订单 `completed`、`settled=true`，组织钱包 470000/冻结 0，接单方与平台佣金入账）→ 双方 **评价**（信用分刷新）。`web/admin.html` 内嵌脚本经 `node --check` 语法校验通过。

## 演示账号（登录 uid）

| uid  | 角色   | 说明                         |
|------|--------|------------------------------|
| R001 | 需求方 | 可发单、验收、结算           |
| P001 | 接单方 | 算法老王(python/nlp/rag, 已绑税源) |
| P002 | 接单方 | 前端小鹿                     |
| P003 | 接单方 | 数据阿杰(未绑税源 → 接单会被拦) |
| P004 | 接单方 | 全栈 Mia                     |
| A001 | 运营   | 可翻转合规开关               |

先 `POST /api/v1/auth/login {"uid":"R001"}`（演示登录）或 `POST /api/v1/auth/register` + `{"account":...,"password":...,"role":"requester"}`（真实注册后自动登录）拿 token，后续请求带 `Authorization: Bearer <token>`。

> **RBAC 订单隔离**：`GET /orders` 未登录返回空列表；普通用户仅见自己作为需求方/接单方的订单；仅运营（A001）可见全量。

## v2 接口（`/api/v2`，与 v1 并行）

2026-10-20 起新增。v1 保持不变，两条链路同进程共存，迁移窗口内互不影响。
设计依据：`../zanbuddy技术架构与开发方案-v2.md` 与 `../zanbuddy数据模型与接口契约-v2.md`。

### 信封与错误

```json
{"ok":true,"data":{},"error":null,"trace_id":"9f2c1a7b4e"}
{"ok":false,"data":null,"error":{"code":"CODE_WRONG","message":"验证码不对","hint":"开发环境是 000000","field":"code"},"trace_id":"9f2c1a7b4e"}
```

三处与 v1 不同：成功失败看 `ok` 不靠状态码猜；`error.field` 有值时前端直接标红对应输入框；每个响应带 `trace_id`，与日志、`audit_log` 对得上。

### 已实现端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v2/healthz` | 进程存活 |
| GET | `/api/v2/readyz` | 依赖检查，存储不可用返 503 |
| POST | `/api/v2/auth/code` | 发验证码；开发模式回显 `dev_code` |
| POST | `/api/v2/auth/login` | 手机号+验证码，不存在即注册 |
| POST | `/api/v2/auth/logout` | 清会话 |
| GET | `/api/v2/me` | 当前用户 + 档案 |
| PATCH | `/api/v2/me` | 改昵称（1–24 字，白名单只开这一个字段） |
| PUT | `/api/v2/me/profile` | 保存供给方档案 |
| GET | `/api/v2/tasks` | 需求大厅列表；`?q=&category=&sort=newest\|budget_desc\|budget_asc&limit=&offset=`，只返回 open |
| POST | `/api/v2/tasks` | 需求方发单，201 返回需求视图 + `clarifications`（还差什么）；登录即可发，不要求档案 |
| GET | `/api/v2/me/tasks` | 我发的需求（不限状态），按创建时间倒序；没人接的单只在这里能被找回来 |
| POST | `/api/v2/tasks/{id}/take` | 接单方认领；409 `TASK_TAKEN` 表示已被接走，403 `ROLE_NOT_PROVIDER` / `PROFILE_INCOMPLETE` 表示没资格 |
| POST | `/api/v2/tasks/{id}/withdraw` | 发单方撤回；只对还挂在大厅（open）的单生效，409 `TASK_ALREADY_TAKEN` 表示已有人接（走订单侧退回），403 `NOT_TASK_OWNER` 表示不是他的单 |
| GET | `/api/v2/tasks/{id}/matches` | 「谁能接」：最多 3 张候选卡，每张带最多 3 条带锚点的理由；仅发单方可读，单已不在 open 返 409 `TASK_NOT_OPEN` |
| POST | `/api/v2/orders/{id}/confirm` | 需求方点头，订单 pending_escrow → active（四态：待确认 → 进行中） |
| POST | `/api/v2/orders/{id}/decline` | 需求方退回，订单取消且任务重新开放；body `{reason}` 可选，超 60 字截断 |
| POST | `/api/v2/orders/{id}/deliveries` | 接单方交付，订单 active → delivered（四态：进行中 → 待验收）；body `{content, link, items[]}`，201 返回订单视图 |
| GET | `/api/v2/orders/{id}/deliveries` | 这一单的交付记录（含历史轮次），仅交易双方可读 |
| POST | `/api/v2/orders/{id}/accept` | 需求方验收通过，订单 delivered → completed（四态：已完成），需求单同时关闭 |
| POST | `/api/v2/orders/{id}/revisions` | 需求方打回，订单 delivered → delivering（四态：回到进行中）；body `{reason}` 必填，500 字内 |
| GET | `/api/v2/me/orders` | 我参与的单（接的 + 发的），带四态、对端定位/信用分、`needs_me`、交付轮次与最近一轮交付 |
| GET | `/api/v2/meta/categories` | 类目清单，前端筛选条的唯一来源 |
| GET | `/api/v2/public/profiles/{slug}` | 公开档案，免登录 |

### 发单

`POST /api/v2/tasks` 建的是需求（`task` 表，状态 open），不是订单。登录就能发，**不要求填档案**——
PRD 里档案只对供给方强制，需求方被档案页挡一次就会走人。

门槛分两档，这个划分是刻意的：

- **硬门槛（400 拦下）**：标题 4–60 字、类目在白名单里、说明 20–2000 字、预算 1 元–100 万元。
  缺这四样，单子挂在大厅里对别人没有意义，发出去只是噪声。
- **软提示（照发，返回 `clarifications`）**：交付物形态、交付格式、截止时间、风格参考。
  缺了仍然可读可报价，用提示催补比拒稿有用——填到一半被拒，人就直接走了。

上限不是审美问题，是列宽问题。`task.title` 是 `VARCHAR(128)`、`deliverables` 是 `VARCHAR(255)`、
`required_format` 是 `VARCHAR(128)`，而 `store.PutTask` 吞掉写库错误：一旦超长，MySQL 严格模式报 1406，
用户却收到 201——发出去的单在大厅里根本不存在。所以字段宽度在 `internal/demand` 里拦死，不指望数据库。
发单后还会 `GetTask` 复核一次，写失败就报错，不静默成功。

预算对外以分为单位。前端按元输入、乘 100 四舍五入（`0.1 * 100` 在浮点下是 `10.000000000000002`）。

`GET /api/v2/me/tasks` 是「我发的需求」。它和 `/me/orders` 不是一回事：后者是**有人接了的单**，
前者是**我挂出去的需求**——刚发出去还没人接的单不在任何订单里，不给这个接口，需求方发完单就找不到它了。

### 认领的并发语义

判断和写入落在同一个原子操作里（`Store.ClaimOpenTask`：内存模式一把写锁，MySQL 一条 `UPDATE ... WHERE status='open'`），
两人同时点只有一个拿到 201，另一个拿 409。这不是前端把卡片变灰，是任务真的从 open 变成 matched。
订单落库后还会 `GetOrder` 复核一次，写失败就把任务退回 open —— 宁可让人重试，也不能让单子凭空消失。

判断顺序是「先资格、后状态」：权限错误优先于「已被接走」，否则需求方点到一单被接走的需求会以为自己手慢。

### 确认与退回

认领只是「有人举手」，开工要需求方点头。`confirm` 只改一行订单状态（`TransitionOrder`，带
`AND status='pending_escrow'` 条件），两个窗口同时点只有一个 200，另一个 409。

`decline` 要动两行：取消订单 + 把任务退回 open。这两行在同一个事务里
（`Store.CancelPendingOrder`）——只退任务，同一单会出现两个活订单；只退订单，单子从大厅消失且谁也接不到。
退回原因按字截断到 60 字，接单方在「我接的单」里能看到。

v2 是轻交易：**确认开工不碰钱**，不预扣款、不进托管。将来接托管模式时，`trade.Confirm` 是唯一要动的地方。

### 交付

确认开工之后，球在接单方这边：不交，需求方没有东西可验收，这一单会永远停在进行中。
`POST /orders/{id}/deliveries` 把订单推到 `delivered`（对外四态 = 待验收）。

请求体：

```json
{
  "content": "RAG 问答系统已跑通，R3 的 Windows 启动脚本还差一版。",
  "link": "https://example.com/rag.zip",
  "items": [
    {"item_id": "R1", "done": true, "note": "PDF/Word 都通了"},
    {"item_id": "R3", "done": false, "note": "Windows 启动脚本明天补"}
  ]
}
```

规则（`internal/trade` 定，handler 只传输）：

- **只有这一单的接单方能交**。需求方、别的接单方一律 403 `NOT_ORDER_PROVIDER`；权限先判、状态后判。
- **只有进行中能交**。未确认开工 409「还没确认开工」；已交付 409「已经交付过了」；已结束 409「已经结束了」。对外统一 `ORDER_NOT_IN_PROGRESS`，具体卡在哪由 message 说清。
- **说明必填、有上限**：`content` 空白 400 `DELIVERY_EMPTY`，超 1000 字 400 `DELIVERY_TOO_LONG`（不静默截断——截掉的可能是最关键的验收说明）。`link` 只收 http/https，最长 500。
- **有需求清单就必须逐项交代**：缺项 / 不存在的项 / 重复项 400 `DELIVERY_ITEM_INVALID`（message 指出是哪几项）；没做完的项不写原因 400 `DELIVERY_ITEM_NOTE`。
- **轮次上限** `max_revisions`（默认 3）。轮次用完后需求方不能再打回，只能验收或走纠纷——否则打回之后接单方交不出下一轮，等于把单子打死。

交付记录与订单状态在同一个原子点落库（`Store.DeliverOrder`：内存模式一把锁，MySQL 一个事务 +
`SELECT ... FOR UPDATE`）。轮次在实现内部算——两个并发的提交各自 Count 一次再 +1，会同时拿到第 2 轮。

`needs_me` 跟着状态走：待确认 = 需求方，进行中 = 接单方，待验收 = 需求方。
已完成与已取消没人要动。待验收状态下双方都能读到最近一轮交付（`last_delivery`，含逐项结果与未完成数）。

前端：大厅「我接的单」里，进行中的单直接展开交付表单——说明 + 链接 + 按需求清单逐项勾选与备注；
顶部横幅提醒"有 N 单该交付了"；交付后这一条变成待验收，双方都能看到同一份内容。

### 验收与打回

交付只是"有东西可看"。需求方看完必须能做两件事之一，否则单子会永远停在待验收：

- `POST /orders/{id}/accept` → 订单 `completed`（四态 = 已完成），**同一个事务里把需求单置为 `closed`**。
  需求单停在 `matched` 的话，"这条需求到底做完了没"永远没人能回答。
- `POST /orders/{id}/revisions` → 订单回到 `delivering`（四态 = 进行中），接单方按打回说明再交一版。

规则与交付那一侧对称：

- **只有这一单的需求方能验收/打回**，接单方、别的接单方 403 `ROLE_NOT_REQUESTER`；资格先判、状态后判。
- **只有待验收能动**：未交付、已打回、已完成一律 409 `ORDER_NOT_TO_REVIEW`。
- **打回原因必填、不截断**：空白 400 `REVIEW_NOTE_EMPTY`，超 500 字 400 `REVIEW_NOTE_TOO_LONG`。
  接单方下一轮只凭这句话改，截掉的可能正是被点名的那一项。
- **轮次用完不能再打回**：最后一轮已经用满 `max_revisions` 时 409 `REVISION_LIMIT`，message 面向需求方
  （"不能再打回，要么验收要么走纠纷"）——接单方交付时看到的 `REVISION_LIMIT` 是另一句话，两种场景不共用文案。

验收结论写在**被打回/通过的那一轮交付**上（`delivery.status` = `submitted|accepted|rejected`，
外加 `review_note` / `reviewed_at`），历史轮次保留：接单方能翻回去看上一版被指了哪几项。
打回说明存在订单上会被下一轮覆盖，所以只能存在交付记录里。

`Store.ReviewOrder` 是同一个原子点（内存一把锁，MySQL 事务 + 行锁）：只改订单不改交付，打回原因就丢了；
只改交付不改订单，单子永远停在待验收，需求方点多少次都没反应。

轻交易下**验收不碰钱**：没有托管账户要释放，也不写钱包。将来开托管模式，放款就是在 `trade.Accept` 里加一行。

### 会话

HttpOnly Cookie（`zb_session`）+ CSRF 双提交（`csrf_token` cookie → `X-CSRF-Token` 头）。
浏览器不必读 token；移动端和开放 API 继续用 `Authorization: Bearer`。
`ZANBUDDY_JWT_SECRET` 未设置时启动会告警，生产必须显式配置。

### 开发期登录

验证码固定 `000000`，响应里也回显一份，不用翻日志。接真实短信通道（`ZANBUDDY_SMS_PROVIDER`）后自动关闭回显。

```bash
curl -s -X POST localhost:8080/api/v2/auth/code -H 'Content-Type: application/json' -d '{"phone":"13800001234"}'
curl -s -c /tmp/ck.txt -X POST localhost:8080/api/v2/auth/login \
  -H 'Content-Type: application/json' -d '{"phone":"13800001234","code":"000000","role":"provider"}'
curl -s -b /tmp/ck.txt localhost:8080/api/v2/me
```

### 接单方的第一段路：登录 → 填档案 → 大厅

想接单的人进来只有两条路，页面必须自己判断走哪条：

1. 登录时选「接单方」，服务端按 `onboarded` 给落点：没建档去 `profile.html`，已建档去 `hall.html`。
   前端按钮传的是 `supply` / `demand`，库里存 `provider` / `requester`，`normalizeRole` 负责对齐——
   不转这一下，选了接单方的人会被注册成需求方，然后卡在登录页和大厅之间。
2. 档案页提交不是"存完刷新一下"：`PATCH /me`（昵称）→ `PUT /me/profile`（定位/技能/作品/接单状态）→
   再 `GET /me` 读一次库，逐字段比对提交值。比对通过才跳 `hall.html?saved=1`；
   不通过就留在原地报错，不把人带到一个"档案是空的"的大厅里。
3. 大厅首屏是公开接口，未登录也能看到需求列表（落地页 → 大厅 → 登录这条拉新路径不能被登录墙挡住）。
   卡片给判断所需的六件事：类目、预算、标题、说明摘要、需求方与信用、发布时间/截止。点标题展开交付物与交付格式。

### 迁移 002

`python db_reset.py` 会自动按顺序应用 `migrations/*.sql`（`schema.sql` 打底，其余按文件名接在后面，
不能靠目录遍历顺序——`002_` 的字典序在 `schema.sql` 前面，先跑 ALTER 会报 1146）。

在已有库上手动追加时：

```bash
python migrations/check_002.py                                    # 先静态自检
mysql -h192.168.3.18 -uroot -p zanbuddy < migrations/002_light_trade.sql
```

`ALTER` 部分不可重复执行（MySQL 8 不支持 `ADD COLUMN IF NOT EXISTS`），重复会报 1060，属预期。
`user.slug` 走「加列 → 回填 → 建唯一索引」三步，不能合成一条：存量行短号都是空串，直接建唯一索引会立刻冲突。

### 前端

`web/app/` 为 v2 前端骨架，路径 `/static/app/`：

| 页面 | 地址 |
|---|---|
| 登录 | `/static/app/login.html` |
| 供给方档案 | `/static/app/profile.html` |
| 需求大厅 | `/static/app/hall.html`（列表 + 认领 + 我接的单；需求方看到的是「等你确认 / 等你验收 / 我发的单」） |
| 发一条需求 | `/static/app/publish.html`（四项必填 + 三项建议 + 验收清单；两个角色都能发） |

零构建：原生 ES module，`assets/runtime.js` 提供 `api / html / validate / toast`，浏览器直接加载，改完刷新即可。

### 演示账号

验证码固定 `000000`（`ZANBUDDY_SMS_PROVIDER` 为空或 `log` 时为开发模式）。

| 手机号 | 身份 | 用途 |
|---|---|---|
| 任意 11 位 | 登录时选 | 新用户，接单方走「填档案 → 大厅」 |
| `13800000001` | 需求方甲（R001） | 发布方入口，名下两单，用来看「有人认领 / 等你确认」 |
| `13800000002` | 茶饮品牌杨姐（R002） | 同上 |
| `13800000003` | 跨境小团队（R003） | 同上 |

需求方这一侧没有单独的页面：登录后落大厅，面板标题按「有没有事等你」在
「等你确认」「等你验收」「等你处理」和「我发的单」之间切换——全在进行了还挂着「等你确认」，
是在催一件已经做完的事；把验收也写成「等你确认」，会让人以为又来了个认领。

### 验收

```bash
bash test/run_all_m0.sh        # 一次跑完下面九套：每条用例前重置库 + 干净重启服务
python test/test_m0_v2.py      # 44 项：健康检查、发码、凭证、登录注册、CSRF、档案规则、公开页白名单
python test/test_m0_hall.py    # 70 项：档案落库比对、角色口径、大厅筛选/排序/分页/脱敏、类目清单
python test/test_m0_publish.py # 92 项：谁能发单、四项必填与边界值、长字段拦截、澄清提示、我发的需求、自己发自己接不到
python test/test_m0_matches.py # 67 项：谁能看「谁能接」、最多 3 卡、谁进候选池、理由锚点指得出真实凭据、干完一单理由变多
python test/test_m0_claim.py   # 40 项：谁能认领、认领后下架、重复认领、我的单、并发抢单只成一单
python test/test_m0_confirm.py # 62 项：谁能点头、确认进入进行中、退回后重新开放、原因截断、并发确认只算一次
python test/test_m0_deliver.py # 69 项：谁能交付、未开工/重复交付、说明与链接校验、逐项清单、待验收双方可见
python test/test_m0_accept.py  # 82 项：谁能验收、打回原因必填不截断、轮次用完不能再打回、并发验收只算一次
python test/test_m0_withdraw.py # 46 项：谁能撤、撤完下架但记录还在、有人接了撤不了、越权不泄露进展、并发只成一个
node web/app/_flow.mjs         # 66 项：真浏览器 登录→填档案→大厅→认领→点头→交付→打回→重交→验收→自己发单→撤回→别人接走，截图到 web/app/_shots/
go test ./internal/store/ -run Credit -v   # 5 项 Go 单测：完成单只留痕不涨分、打回不留痕、重放不重复、幂等键、"干完活"与"干得好"两个 kind 共存
```

**信用事件落库要单独体检**（用例只证明接口返回 200，证明不了事件真的写进了库）：

```bash
ZANBUDDY_DB_PASSWORD=xxxx \
  C:/Users/admin/.workbuddy/binaries/python/envs/default/Scripts/python.exe test/check_credit.py
```

它核对四件事：每条 completed 订单恰好 2 条 `order_completed`、`delta` 全为 0、
没有「订单完成但事件不全」的半截状态、没有重复的 `(user,kind,ref)`。
飞轮断掉的典型症状就是——订单状态往前走了，`credit_event` 里什么都没有，而所有用例依然全绿。

**每条用例前必须重置数据库并重启服务**（`run_all_m0.sh` 已经这么做了）。
种子需求 `T-DEMO-001..003` 是共享的，认领一张就少一张；连着跑不重置，
后面的用例会在准备阶段拿不到种子，表现成「新功能把老用例弄挂了」，排查方向完全错。

`test_m0_confirm.py` 不写死任务号：它从大厅里找两张开放需求按需求方手机号登录，
所以跟在 `test_m0_claim.py` 后面跑也不会因为单子被占而失败。

`test_m0_deliver.py` 用种子里的 `T-DEMO-001`（带 3 项需求清单）与 `T-DEMO-002`（无清单）各走一遍：
这两条单在 MySQL 模式下也验证了 `delivery.items_json` / `link` 的落库与读回。

`test_m0_accept.py` 三张单分工：`T-DEMO-001` 走主链路（打回 → 重交 → 验收），
`T-DEMO-002` 连交三轮验证轮次上限，`T-DEMO-003` 用两个会话并发验收验证只成一次。

`test_m0_publish.py` 不依赖种子：它自己注册一个需求方、两个接单方，发一条单再让接单方接走。
最后一段专门验证「发单方自己接不到自己的单」——需求方角色接单拿 403 `ROLE_NOT_PROVIDER`，
接单方身份发的单被自己接拿 409 `SELF_CLAIM`，这两种是不同的事，错误码也分开。

`test_m0_withdraw.py` 自己注册需求方与接单方，不碰种子：它要证明的是
「撤完之后大厅里搜不到、`/me/tasks` 里还查得到、状态是 `withdrawn` 而不是 `closed`」，
以及「别人撤我的单只拿到 403，不会被顺带告知这单已经有人接了」——
后一条是越权探测的防线：错误顺序写反，等于把对手的进展告诉外人。

`test_m0_matches.py` 也不依赖种子：五个接单方各占一种情况（对得上 / 只沾边 / 完全无关 /
没建档 / 暂停接单），证明只有「有档案、没暂停、说得出为什么是他」的人进候选池。
核心是**理由必须指得出凭据**：技能理由指向档案里真有的标签，订单理由的 `anchor.ref`
就是那一单的订单号。让 P1 真跑完一单再回来看，理由会多一条、锚点指向这笔真实订单；
但**分数不动**——历史因子只看互评均值（`AvgRating/5×20`），而互评在 M4 才产生。
分数若在这时上涨，就说明有人拿「完成单数」冒充评分，卡上的理由却写不出「客户给了几星」。


> **本机坑（已找到根因，2026-10-22）**：以前记成「Python 与本服务偶发连接中止（10053/10054），
> 测试只能靠重试糊过去」。真正的根因在**服务端**：有些写端点的请求体没被读（认领只带 `{}`、
> 鉴权失败的请求在读 body 之前就被拒），客户端又默认 `Connection: close`，服务端回完响应立刻关连接，
> 接收缓冲区里还有未读字节时操作系统发的是 RST 而不是 FIN，客户端于是看到「连接被重置」——
> 业务其实已经成功。单人顺序请求就能复现（实测 25 轮里 9~12 轮）。
> 修法见 `internal/httpapi/drain.go`：写出响应头之前把请求体读干净。修完同一条压测 0/25。
>
> 另外两个坑不是服务端的：①Python 在 Windows 上**不会**给 `127.0.0.1` 绕过系统代理
> （`proxy_bypass` 返回 False），测试请求会被本机代理接走再掐断，所以测试客户端都显式挂了
> 空 `ProxyHandler`；②`urllib` 不接受非 ASCII URL（`?q=抖音` 要 `quote()`）。

## 核心 API（前缀 `/api/v1`）

| 方法 | 路径 | 说明 | 合规闸门 |
|------|------|------|----------|
| POST | `/auth/register` | 真实注册(账号+密码, PBKDF2 哈希; 角色限 requester/provider) | — |
| POST | `/auth/login` | 登录拿 token（支持 `uid` 演示登录，或 `account`+`password` 密码登录） | — |
| GET  | `/tasks` | 需求大厅(开放任务)；支持检索 `?category=&status=&min_budget=&max_budget=&q=&skill=&deadline_after=&deadline_before=&sort=newest\|budget_desc\|budget_asc\|relevance&limit=`（`sort=relevance` 需接单方 token） | — |
| GET  | `/tasks/{id}` | 需求详情(聚合：任务本体 + 发布者公开声誉画像 + 关联订单摘要；不存在 404) | — |
| POST | `/tasks` | 需求方发单（支持结构化字段：deliverables/required_format/deadline_at/style_ref/samples；缺必填项返回 `clarifications` 澄清问题，不阻塞） | KYC |
| POST | `/tasks/{id}/take` | 接单方从需求大厅主动认领 open 任务（直接生成订单待托管；先到先得、不可接自己发布的需求、受 KYC+税源闸门） | 仅接单方 |
| POST | `/tasks/{id}/match` | AI 撮合(Top5)：轻量语义召回 + 多因子精排(技能/语义/信用/税源/历史评分) | — |
| POST | `/matches/{id}/accept` | 接单方应征→生成订单 | KYC + 税源 |
| GET  | `/recommend/tasks` | 推荐位(接单方首页 feed)：按 `score(p,t)` 推荐 open 任务 Top10 | 仅接单方 |
| GET  | `/recommend/providers?task_id=` | 推荐位(需求方首页 feed)：按 `score(p,t)` 推荐接单方 Top10 | 登录 |
| POST | `/kyc` | 提交实名认证(name+id_no)；模拟通道自动通过并打标 | 登录 |
| GET  | `/kyc` | 查询当前用户 KYC 状态 | 登录 |
| POST | `/payments/notify` | 支付结果异步回调(微信支付结果通知等)：解析订单号与支付成败，驱动 pending_escrow→active（幂等） | — |
| POST | `/orders/{id}/escrow` | 创建资金托管 | KYC + 税源 + 托管 |
| GET  | `/orders/{id}` | 订单详情(含匹配/交付) | — |
| POST | `/orders/{id}/deliver` | 接单方交付(多轮) | — |
| POST | `/orders/{id}/accept` | 需求方验收(触发结算) | KYC + 税源 + ICP + 托管 |
| POST | `/orders/{id}/settle` | 合规落地后补结算 | KYC + 税源 + ICP + 托管 |
| POST | `/orders/{id}/review` | 验收完成后双向评价(评分 1-5) → 重算被评价方信用分 | 登录 + 须为参与方 + 订单须 completed |
| GET  | `/orders/{id}/reviews` | 查看订单评价 + 接单方当前信用分 | — |
| POST | `/im` | 发 IM 消息（命中规避词被内容审核拦截，返回 400 + 拦截原因） | 登录 |
| GET  | `/im?peer=` | SSE 实时消息流（首屏历史 + 实时推送） | 登录 |
| GET  | `/im/conversations` | 会话列表（含双方未读计数） | 登录 |
| GET  | `/im/conversations/{peer}` | 指定会话历史（移动端轮询拉取） | 登录 |
| POST | `/im/conversations/{peer}/read` | 标记与某会话已读（未读清零） | 登录 |
| GET  | `/compliance` | 查合规状态 | — |
| POST | `/compliance` | 运营翻转合规项(icp/tax_source/escrow) | 仅运营 |
| GET  | `/auth/me` | 当前登录用户身份与信用分(uid/role/name/account/credit_score)，供 H5 钱包页展示 | 登录 |
| GET  | `/auth/me/workbench` | 我的工作台聚合(发布任务列表+关联订单状态 / 接单方订单 / 需求方订单 分组 + 待办统计：open_tasks/active_orders/needs_my_action)；供前端 dashboard | 登录 |
| GET  | `/wallet` | 当前用户钱包与最近流水（余额/冻结） | 登录 |
| POST | `/wallet/withdraw` | 接单方提现申请（冻结预占，待 admin 审批） | 登录 + 接单方 |
| GET  | `/wallet/withdrawals` | 提现列表（pending 优先）；仅运营可见全量 | 仅运营 |
| POST | `/wallet/withdrawals/{id}/approve` | 运营审批提现（通过则真实出账+解冻，驳回则解冻回可用） | 仅运营 |
| GET  | `/notifications` | 当前用户通知列表（倒序，默认 50；inapp/email/sms 通道） | 登录 |
| GET  | `/notifications/unread` | 当前用户未读通知数 | 登录 |
| POST | `/notifications/{id}/read` | 标记某条通知已读（须属于该用户，越权返回 404） | 登录 |
| POST | `/orgs` | 创建组织（创建者自动成为 owner） | 登录 |
| GET  | `/orgs` | 列出我作为 owner 的组织 | 登录 |
| GET  | `/orgs/{id}/members` | 列出组织成员 | 登录 |
| POST | `/orgs/{id}/members` | 邀请成员（仅 owner/admin；角色 owner/admin/member/viewer） | 登录 + owner/admin |
| GET  | `/orgs/{id}/wallet` | 组织钱包视图（balance/frozen，成员可见） | 登录 |
| POST | `/orgs/{id}/wallet` | 组织钱包充值（仅 owner/admin） | 登录 + owner/admin |
| POST | `/orgs/{id}/tasks` | 组织级发包：发布 open 任务（带 OrgID，不预扣款；组织任务走与个人相同的「接单→托管→结算」管线，托管/结算按 OrgID 走组织钱包） | 登录 |
| GET  | `/orgs/{id}/tasks` | 列出组织发布的任务 | 登录 |
| GET  | `/orgs/{id}/orders` | 组织订单列表（企业版统一结算视图，按 OrgID 聚合） | 登录 |
| POST | `/orgs/{id}/payouts` | 组织钱包提现申请（冻结金额并生成 pending 审批单，待 owner/admin 审批） | 登录 |
| GET  | `/orgs/{id}/approvals` | 组织审批单列表 | 登录 |
| POST | `/orgs/{id}/approvals/{aid}/approve` | 审批组织提现（仅 owner/admin；通过则真实出账解冻，驳回则解冻） | 登录 + owner/admin |
| POST | `/orgs/{id}/apikeys` | 生成组织开放 API Key（明文仅返回一次，存 SHA256 hash；仅 owner/admin） | 登录 + owner/admin |
| GET  | `/orgs/{id}/apikeys` | 列出组织 API Key（不含明文与 hash） | 登录 |
| POST | `/orgs/{id}/webhooks` | 订阅组织事件 Webhook（task_created/order_settled/payout_approved） | 登录 |
| GET  | `/orgs/{id}/webhooks` | 列出组织 Webhook 订阅 | 登录 |
| GET  | `/orgs/{id}/webhook-deliveries` | 组织 Webhook 投递记录（验证事件真实投递） | 登录 |
| GET  | `/admin/metrics` | P3 运营看板：平台指标快照（用户/任务/订单/GMV/资金水位/提现/合规红灯），只读聚合 | 仅运营 |
| GET  | `/users/{id}` | Marketplace 用户公开声誉画像（credit_score/avg_rating/review_count/completed_count/trust_score/skills；无需登录） | — |
| GET  | `/meta/categories` | 类目法 taxonomy（id/label/desc），需求大厅筛选下拉 + 发单类目白名单的唯一事实来源；无需登录 | — |

> **移动端 H5 页面**：访问 `http://localhost:8080/m` 打开 P1-3 移动端核心页（大厅 / 发单 / 订单 / 我的·钱包），复用上述全部 REST API；运营登录后 H5 内置合规开关，可移动端自闭环演示「发单→接单→托管」全流程。
| POST | `/clarify` | P1-2 AI 澄清：需求方发自然语言需求 → 建会话 → 抽取结构化 spec（离线规则化 stub 兜底，无需 API Key） | 仅需求方 |
| POST | `/clarify/{id}` | 多轮补充澄清（追加消息 → 刷新结构化草稿） | 仅需求方 |
| POST | `/clarify/{id}/extract` | 重新抽取结构化草稿（不新增对话） | 仅需求方 |
| POST | `/clarify/{id}/publish` | 一键发布澄清后的需求（复用 `PublishTask`，缺必填返回 `clarifications` 不阻塞） | 仅需求方 + KYC |
| POST | `/orders/{id}/revise` | P1-4 需求方打回重做（仅 `delivered` 状态，受 `max_revisions` 改稿上限约束，超限 409） | 仅需求方(参与方) |
| POST | `/orders/{id}/cancel` | P1-4 取消订单（参与方；按比例退款 `refund_pct`，状态转 `cancelled`） | 仅参与方(需求方/接单方) + 状态非终态 |
| POST | `/orders/{id}/dispute` | P1-4 发起纠纷（参与方；状态转 `disputed`） | 仅参与方 + 进行中状态 |
| POST | `/orders/{id}/resolve` | P1-4 运营仲裁结案（按 `refund_pct` 退款，状态转 `completed`） | 仅运营 |
| POST | `/orders/{id}/milestones/{msid}/confirm` | P1-4 里程碑确认放款（仅 `milestone` 模式；全阶段完成才 `completed`） | 仅需求方 |

## 合规闸门演示（一键复现）

一键跑完整 MySQL 模式端到端冒烟（自动重置库 → 构建 → 起服务 → 全链路断言 → 停服务）：

```bash
bash e2e_mysql.sh          # 默认 python 需能 import pymysql(托管 venv)
PYTHON=/path/to/venv/python bash e2e_mysql.sh
```

> 密钥安全：脚本不硬编码密码，从 `.env`（已 gitignore）或环境变量读 `ZANBUDDY_DB_PASSWORD` / `ZANBUDDY_DB_DSN`；没有 `.env` 时复制 `.env.example` 为 `.env` 填写即可。当前 MySQL 模式全链路 **218/218 PASS**（含 M2 语义匹配升级后的批量向量召回回归）。

手动分步演示：

```bash
python db_reset.py         # 先重置 MySQL(纪律要求)
go build -o zanbuddy.exe . && ./zanbuddy.exe &
python smoke_test.py
```

测试覆盖：发单(仅KYC放行) → 撮合(命中技能打分) → 接单(税源未开被 409 拦, 翻税源后放行) → 托管(托管未开被 409 拦, 翻托管后放行) → 交付 → 验收(ICP 未开 → 订单转 pending_settlement 被 409 拦) → 翻 ICP → 补结算放款完成；另含 P0-3 真实注册 / 账号密码登录 / 重复账号与弱密码拦截 / RBAC 订单隔离；P0-4 双向评价（需求方 5★ / 接单方 4★ → 信用分分别重算为 100 / 80）、重复评价 / 非参与方 / 越界评分 / 未登录均被拒；P0-5 需求结构化（缺交付物/格式/截止返回 `clarifications` 澄清问题、完整结构化发单 `spec_complete=true` 且字段回显）；P1-1 智能匹配（多因子 reason 含语义/信用/历史、Top1 为最高分接单方、评价反馈闭环使高评分接单方加权、类目错配仍可据技能/语义软召回）；P1-2 AI 澄清（自然语言 → 抽取结构化交付物/格式/截止/预算/类目、缺项生成必填澄清问题、多轮补充后规格转完整、澄清后一键发布并进入需求大厅、非需求方越权被 403 拒）；P1-4 订单完整化（lump 模式改稿上限第 4 次交付被 409 拦、取消订单按比例退 50%、已取消不可再取消 409、里程碑模式分阶段确认放款至 completed、纠纷发起后非运营结案 403 / 运营按 30% 退款结案、非参与方取消 403）；**P2-1 钱包/流水/提现**（托管扣可用余额并增冻结、结算放接单方+平台佣金入账、纠纷退款回补需求方、接单方提现申请冻结预占、非运营审批提现被 403 拒、admin 审批通过后真实出账并解冻、退款净额校验）；**P2-2 即时通讯**（消息收发落库持久化、接收方未读计数 +1、进入会话标记已读清零、命中规避词"加我微信"被 400 审核拦截、订单接单后自动建会话并补 `order_id`、会话列表含未读角标）；**P2-3 通知中心**（接单/交付/验收结算/纠纷/提现审批均自动生成站内信、接收方未读 +1、列表含 inapp 未读项、标记已读后未读数 -1 且 status=read、跨用户标记被 404 拒、未登录查通知被 401 拒）；**P3 企业版治理最小骨架**（创建组织→owner 自动入库、owner 列组织非空、邀请成员成功、成员列表含 owner+被邀请人、普通成员无邀请权限 403、未登录创建被 401 拒）；**Marketplace 成熟度**（需求大厅按 category 过滤仅含该类目、关键词 q 命中标题/规格、预算下限 min_budget 仅含达标任务、sort=budget_desc 降序、用户公开画像含信用分/角色/完成数/均分、不存在用户画像 404）；**Marketplace 可信度与分类法**（类目法端点返回≥10 项含 nlp/design/other、画像含 trust_score 且 0–100、非法类目发单被拦截、空类目自动归 other 成功）；**主动接单**（provider 主动认领 open 任务→订单 pending_escrow、已被接单不可再接 409、需求方接单 403、税源关闭被闸门拦 409）；**任务详情与工作台**（需求详情聚合发布者画像+关联订单、不存在任务 404、我发布的任务含关联订单、接单方工作台含该订单、未登录工作台 401、工作台含待处理统计）；**P3 运营看板**（运营查平台指标关键字段齐全、非运营 403、未登录 401）；**P3 专项深化**（组织钱包充值仅 owner/admin、非 owner 充值 403、组织发包生成带 OrgID 的 open 任务、provider 接组织单生成带 OrgID 订单、组织托管从组织钱包扣款且余额不足 409、组织提现审批闭环 owner 审批出账、非 owner 审批 403、开放 API Key 生成且 `hgk_` 明文仅一次、API Key 鉴权查组织钱包 200、错误 Key 401、Webhook 订阅 + task_created 事件真实投递 success）；**方案3 Marketplace 深化**（搜索索引：`GET /api/v1/tasks` 新增 `skill` 关键词与 `deadline_after`/`deadline_before` 截止窗口过滤、MemStore 拉丁词倒排索引加速检索；推荐位 `GET /api/v1/recommend/tasks` 接单方 feed 复用多因子精排返回 Top10、需求方越权 403；用户画像页 `web/profile.html` 静态托管 + CORS 可跨域调用）。**内存模式与 MySQL 模式均已 212/212 PASS（MySQL 模式为端到端真实落库验证）。**

## 上线就绪态（真实方案2 · 生产前置检查）

真实方案2（合规线C）的骨架已落地：`payment` 走微信支付 v3 形态（签名/JSAPI下单/分账/退款/回调解密骨架），`kyc` 走真实厂商通道骨架，合规四门由平台级开关驱动。但**模拟态与生产态仅差"环境变量是否填全"**——填全即 live，否则自动退回 stub/mock，不触真实资金与厂商。

为让"去生产前还差什么"一目了然，提供两处一致的检查入口：

- **端点**：`GET /api/v1/readiness`（无需登录），返回 `storage_mode` / `embedding{mode}` / `clarify{mode}` / `payment{mode,live,missing}` / `kyc{mode,live,missing}` / `icp_filed` / `compliance_gate` / `go_live_ready`。仅回显布尔与**缺失的环境变量名**，绝不回显密钥值。
- **运营后台**：运营看板新增「**上线就绪**」标签页（`#ready`，A001 登录后可见），按资金通道 / KYC / 合规四门逐项打勾，并列出当前通道下**仍需补齐的环境变量**清单，直接对照 `.env.example` 填充即可。

判定 `go_live_ready = 真实资金通道就绪 && 真实KYC就绪 && 合规四门全开`。当前演示态（未填微信/KYC 凭证）恒为 `false`，业务闭环仍可在 stub/mock 下完整跑通。

**去生产需补齐的环境变量**（完整清单见 `.env.example`）：

| 通道 | 触发变量 | 必需项（缺一则退回 stub/mock） |
|---|---|---|
| 资金（微信支付 v3） | `ZANBUDDY_WECHAT_MCHID` | `APPID` / `SERIAL` / `KEY_PEM` / `APIV3_KEY` / `NOTIFY` |
| KYC（真实厂商） | `ZANBUDDY_KYC_PROVIDER=real` | `APIKEY` / `BASEURL` |
| ICP 备案 | A001 翻转 `icp` 开关 | 备案号落到执行卡3，由运营开启 |
| 税源地 / 托管 | A001 翻转 `tax_source` / `escrow` | 签约凭证驱动（卡1 / 卡2） |

> 注：`APIV3_KEY` 是支付回调 AES-256-GCM 解密的唯一密钥，未配时真实回调无法解密（已在 `Notify` 内显式拦截，不再静默吞掉）；多行 PEM 私钥建议从文件或字面换行注入。

## 生产替换指引

1. **存储**：已支持 MySQL（设 `ZANBUDDY_DB_DSN` 启动即切持久化），生产改为直连托管库；`migrations/schema.sql` 已对齐字段（金额用分、雪花 ID、软删）。
2. **资金**：将 `payment.Service` 替换为微信支付「担保交易下单 + 服务商分账」，严禁二清/资金池。
3. **IM**：将 `im.Bus` 替换为腾讯云 IM SDK（离线推送 + 已读回执 + 内容审核回写）。
4. **合规**：`compliance.State.Items` 改为由真实签约状态（备案回执 / 税源地协议 / 支付开通凭证）驱动，而非 admin 手动翻转。
5. **匹配**：M1 已落地轻量语义召回（词袋余弦，零依赖）+ 多因子精排 + 评价反馈闭环；**M2 已完成**——语义层抽象为可插拔 `Embedder`（`internal/match/embed.go`）：默认 `BagOfWordsEmbedder` 复用 M1 词袋向量（行为完全等价、零依赖保底），设 `ZANBUDDY_EMBEDDING_MODE=llm` + `ZANBUDDY_EMBEDDING_API` 即切换 OpenAI 兼容 `/embeddings` 稠密向量（支持 OpenAI/Ollama/自建），调用失败自动回退词袋，召回不挂。召回入口（MatchTask / RecommendTasks / RecommendProviders）统一 batch embed 后 per-pair 向量余弦；需求侧文档 `taskDoc` 已融合 P1-2 澄清后的 `SpecObj`（Deliverables/RequiredFormat/StyleRef/Samples），spec 越完整语义越聚焦。`GET /api/v1/readiness` 返回 `embedding.mode`。

## 启用真实大模型（AI 能力升级 · B1）

Zanbuddy 的「AI 语义匹配」与「AI 需求澄清」默认以**零依赖保底版**运行（词袋语义 + 正则澄清），无需任何外部 Key 即可演示。配置环境变量即从保底版升级为真实大模型版，**联网失败 / 超时自动回退保底版，绝不挂**。

| 能力 | 触发变量 | 行为 |
|---|---|---|
| 需求澄清（`/clarify`） | `ZANBUDDY_LLM_API_KEY` | 非空即调 OpenAI 兼容 `/v1/chat/completions`；`Chat` 与 `Extract` 任一失败均回退规则化兜底 |
| 语义匹配（`MatchTask`/`RecommendTasks`/`RecommendProviders`） | `ZANBUDDY_EMBEDDING_MODE=llm` + `ZANBUDDY_EMBEDDING_API` | 调 `/embeddings` 产出稠密向量；`Service.embed` 失败自动回退词袋 |
| 联网代理 | `ZANBUDDY_AI_PROXY`（或系统 `HTTP_PROXY`/`HTTPS_PROXY`） | 适配中国大陆访问 OpenAI / DeepSeek / 本地 Ollama 等 |

`GET /api/v1/readiness` 实时回显当前生效模式：`clarify.mode ∈ {stub, llm}`、`embedding.mode ∈ {bag-of-words, llm}`、`faq.mode ∈ {faq, llm}`、`reqai.mode ∈ {rule, llm}`。

## 接单方自动报价（AI 能力升级 · B2）

接单方在任务详情页可一键获取「AI 建议报价」：`POST /api/v1/quote`（仅接单方）传入 `task_id`，按 **类目基准价 × 复杂度乘数 × 紧急度乘数 + 历史单均净收入融合** 给出建议锚定价与区间（`min_cents`/`anchor_cents`/`max_cents`），并附 `rationale` 理由清单。金额经 `round10Yuan` 分/元换算（已修复早期 ×100 放大 bug）。接单方可「采纳建议并接单」（传 `provider_quote_cents`），订单落库该报价，详情页展示。需求方预算低于建议下限时提示 `below_estimate`。

## AI 客服应答（AI 能力升级 · B3）

接单 IM 会话中的常见问题自动应答（FAQ bot），**规则优先 + 可选 LLM 兜底**（`internal/faq`），复用 B1 的 `ZANBUDDY_LLM_API_KEY` 开关：

- **AI 建议回复**：`POST /api/v1/im/{peer}/ai-reply`（需登录）基于入站消息（或对方最近一条消息）生成应答建议，`matched` 时返回 `text`/`intent`/`type`；前端 task.html 聊天面板「🤖 AI 建议回复」一键填入，发送时带 `ai_type="suggest"` 标记「AI 辅助」（真人审核后发出，`is_ai=false`）。
- **AI 离线自动回复**：用户开启开关（`POST /api/v1/me/ai-auto-reply` `{on}`）后，对发往自己的提问自动生成一条 AI 应答代己回发（`is_ai=true`、`ai_type=faq|llm`），消息体带「🤖 AI 自动回复：」透明显式前缀，**明确标记 AI、不冒充真人**；收件人未开启则不触发（防环路：自动回复本身不走二次钩子）。
- **规则库**：默认零依赖关键词意图匹配（报价/工期/交付/验收/付款托管/退款/改稿/发票/合规/佣金/私下交易等），命中即返回话术；未命中且配置 LLM 时调模型兜底（失败回退转人工）。
- **前端**：`web/task.html` 订单页新增 IM 沟通面板（轮询拉取 + 发送 + AI 建议回复 + 自动回复开关），AI 消息以徽标区分（🤖 AI 自动回复 / AI 辅助）。
- **存储**：`chat_message` 表新增 `is_ai`/`ai_type`，`user` 表新增 `ai_auto_reply`；MySQL 与内存双模式落库。
- **测试**：`test_b3_faq.py` 端到端 17/17 PASS（readiness 上报 / 建议回复 / 自动回复钩子 / 采纳 suggested / 开关持久化 / 负向对照）。

## AI 需求翻译 + AI 需求分析（AI 能力升级 · C1）

围绕「需求方 → 接单方」的理解偏差，新增两类互补的 AI 能力（`internal/reqai`，**规则保底 + 可选 LLM 兜底**，复用 B1 的 `ZANBUDDY_LLM_API_KEY` 开关；任何失败一律回退规则，绝不挂、绝不空）：

- **AI 需求翻译（需求方侧）**：把需求方粗糙的描述重写为一份清晰、无歧义、可供接单方直接读懂并执行的「需求书」。仅任务发布者本人可生成/重生成（`POST /api/v1/tasks/{id}/translate`）；规则版组合「需求概述 / 交付物清单 / 交付格式与规范 / 交付期限 / 风格参考 / 预算参考 / 验收提示 / 协作约定」条目，配置 LLM 时由模型润色正文（失败回退规则）。结果落库 `task.translation`（JSON）+ `task.translation_mode`。
- **AI 需求分析（接单方侧）**：接单方（或任意登录用户）打开任务时一键生成（`POST /api/v1/tasks/{id}/analyze`），自动拆解 **核心目标 / 交付物 / 验收要点 / 易误解点 / 需向需求方确认项**，防止理解偏差；规则版基于结构化规格 + 启发式（格式/改稿轮次/版权授权/交付方式/必填项缺失），配置 LLM 时由模型补全结构化结果（解析失败回退规则）。结果落库 `task.analysis_json` + `task.analysis_mode`。
- **前端**：`web/task.html` 任务详情页新增两块卡片——「🤖 AI 需求翻译（清晰需求书）」（需求方可见生成/重生成按钮 + 展示；接单方只读）与「🧠 AI 需求分析（接单方校核）」（任意登录用户可生成/展示）；`web/app.html` 发单成功后直接跳到任务详情页，作为「生成 AI 需求翻译」的快捷入口。
- **存储**：`task` 表新增 `translation`(TEXT) / `translation_mode`(VARCHAR) / `analysis_json`(JSON) / `analysis_mode`(VARCHAR)；`migrations/schema.sql` 在 `CREATE TABLE task` 之后追加 `ALTER`（顺序敏感，否则 reset 报 1146）。MySQL 与内存双模式落库（已修复扫描 NULL 文本列到 `string` 的兼容问题，改用 `sql.NullString`）。
- **测试**：`test_c1_reqai.py` 端到端 17/17 PASS（readiness 上报 reqai.mode / 发布任务 / 需求方生成翻译并落库 / 接单方越权翻译被拒 / 接单方生成分析并落库 / 负向：无 LLM Key 走规则且不空不挂）。

## P2-1 经济模型验证（资金守恒 / 佣金 / 提现 / 纠纷退款）

用 `test_p2_econ.py` 对全链路资金模型做**精确金额断言**（单位：分），覆盖 8 组场景，**55/55 PASS**。这是「把系统跑通」阶段对商业模式最硬的验证——每一步的金额都可断言、可复现：

- **单量资金守恒（¥1000 正常成交）**：需求方托管时可用 −100000 / 冻结 +100000 → 结算后冻结清零；接单方 +90000（金额×90%）、平台佣金 +10000（10%），满足 `需求方支付 == 接单方所得 + 平台佣金`。
- **佣金口径统一性**：`commission = amount × 10 / 100`（整数分），对 1/99/100/100000/123456/999999 逐一断言，且佣金恒不超订单金额。
- **提现三态**：
  - *申请冻结*：可用余额不变、冻结额 +N、可用额（balance − frozen）相应减少，资金未出平台；
  - *审批通过*：真实出账，可用 −N、冻结 −N，总额永久减少（钱离开平台），并记 `withdraw` 流水；
  - *审批驳回*：解冻回可用，总额与申请前一致（零资损）。
- **提现边界**：0 元、超额、非接单方提现均被拒。
- **纠纷仲裁分账（退款 40%）**：需求方退回 40000、接单方分得 50000（= 100000 − 退款 − 佣金）、平台佣金仍按全额订单额收 10000，满足 `退款 + 接单方 + 佣金 == 订单额`。
- **幂等性**：重复结算不新增 settle 流水、不重复放款（`Settle` 以 `o.Settled` 为幂等锁，返回 200 但不放款）；已处理提现再审批被拒，不重复出账。

> 🐛 **本次验证发现并修复一个真实资损风险（提现双花）**：早期 `RequestWithdraw` 只校验 `balance` 且申请时不扣减可用额，同一笔钱可被**重复申请提现**（实测冻结额可达余额 2 倍），若两笔都被审批通过即造成超发。修复为按 **可用额 = balance − frozen** 校验（`internal/service/service.go`），并新增「可用额归零后再申请 1 分必被拒」的回归断言。

## 组织钱包 / 企业版发包资金链路（P2-1 延伸）

企业版走独立的**组织钱包**分支（与个人钱包完全隔离），用 `test_p2_org.py` 做同款精确金额断言，覆盖 8 组场景，**58/58 PASS**：

- **组织钱包充值**：owner/admin 可充；非成员、负数、0 元均被拒；失败充值不改变余额。
- **组织发包**：`CreateOrgTask` **不预扣款**（发包后组织余额不变），非成员不可发包。
- **组织托管**：`CreateEscrow` 走 `OrgID` 分支，从**组织钱包**扣款 + 冻结（不碰 owner 个人钱包）；owner 个人钱包余额不受组织托管影响；托管 ≠ 结算，接单方此时未入账。
- **组织结算**：`releaseToWallets` 解冻组织冻结，**组织余额不再变动**（托管时已扣），接单方 +90%、平台佣金 +10%，满足 `接单方 + 佣金 == 订单额`。
- **组织提现三态**：申请冻结（可用额 `balance − frozen` 减、超额被拒）→ 审批通过（真实出账、总额永久减少）→ 审批驳回（解冻复原、零资损）；已处理审批单不可重复审批、不重复出账。
- **权限隔离**：非成员对**全部**组织资源一律 403。

> 🔒 **本次验证发现并修复一个真实越权漏洞（组织信息泄露）**：组织各**写**类端点（充值/发包/提现/审批）一直有成员校验，但**读**类端点（`GET /orgs/{id}/wallet|orders|members|approvals|tasks|webhook-deliveries`）完全没校验——实测**任何登录用户都能读到组织钱包余额、冻结额与成员名单**。修复：新增 `Service.RequireOrgMember` 并在 6 个 GET 端点统一校验（`internal/handler/handler.go`），owner/admin 自身访问不受影响。

## 重要声明

本骨架**不处理真实资金**，仅验证业务主链路与合规拦截设计。上线收钱前必须完成《合规落地执行卡》四卡（域名实名+ICP 备案 / 税源地签约 / 支付托管开通 / 成本测算），约 4–6 周外部签约期。
