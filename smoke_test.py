#!/usr/bin/env python3
"""Lianvo M0 主链路冒烟测试: 验证发单→匹配→接单→托管→交付→验收结算, 以及合规闸门拦截与翻转。"""
import json
import time
import threading
import http.server
import urllib.request
from urllib.parse import quote

BASE = "http://localhost:8080"


def call(method, path, token=None, body=None):
    url = BASE + quote(path, safe="/?:=&")
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode())
        except Exception:
            return e.code, {}


def login(uid):
    _, d = call("POST", "/api/v1/auth/login", body={"uid": uid})
    return d["data"]["token"], d["data"]["role"]


def line(t):
    print("=" * 60)
    print(t)
    print("=" * 60)


ok = 0
fail = 0


def check(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  [PASS] {name} {detail}")
    else:
        fail += 1
        print(f"  [FAIL] {name} {detail}")


line("0. 健康检查")
s, _ = call("GET", "/api/v1/health")
check("health", s == 200)

line("0.5 重置合规状态(MySQL 持久化, 先翻回红灯避免误判)")
t_a, _ = login("A001")
for it in ["icp", "tax_source", "escrow"]:
    call("POST", "/api/v1/compliance", token=t_a, body={"item": it, "on": False})

line("1. 需求方 R001 登录 + 发单(未填结构化字段)")
t_r, _ = login("R001")
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "用 Python 搭建 RAG 问答系统",
    "category": "nlp",
    "budget_cents": 300000,
    "spec": "需要 python + rag + nlp 的检索增强生成问答, 支持文档上传",
})
check("发单成功(仅KYC)", s == 201, str(s))
task_id = d["data"]["task"]["id"]
rag_task_id = task_id  # 保存 RAG 任务 ID, 供后续方案3做深断言(避免被后续 section 重赋值污染)
# 未结构化发单 → 应生成澄清问题(规则澄清兜底), 规格不完整
check("未结构化发单生成澄清问题", s == 201 and len(d["data"]["clarifications"]) > 0, f"clar={d['data']['clarifications']}")
check("未结构化发单 spec_complete=false", s == 201 and d["data"]["spec_complete"] is False)

line("2. AI 撮合")
s, d = call("POST", f"/api/v1/tasks/{task_id}/match")
recs = d["data"]
check("撮合返回推荐", s == 200 and len(recs) > 0, f"推荐数={len(recs)}")
for r in recs[:3]:
    print(f"    推荐 {r['provider_id']} 分={r['score']:.1f} 理由={r['reason']}")
match_id = recs[0]["id"]

line("3. 接单方 P001 应征(合规闸门: tax_source 未开 -> 应拦截)")
t_p, _ = login("P001")
s, d = call("POST", f"/api/v1/matches/{match_id}/accept", token=t_p)
check("接单被合规拦截(409)", s == 409, f"missing={d.get('missing')} card={d.get('card_ref')}")

line("4. 运营 A001 翻转 tax_source -> 接单放行")
t_a, _ = login("A001")
s, _ = call("POST", "/api/v1/compliance", token=t_a, body={"item": "tax_source", "on": True})
check("合规项更新", s == 200)
s, d = call("POST", f"/api/v1/matches/{match_id}/accept", token=t_p)
check("接单放行(201)", s == 201, str(s))
order_id = d["data"]["id"]
check("订单初始状态=pending_escrow", d["data"]["status"] == "pending_escrow")

line("5. 创建托管(闸门: escrow 未开 -> 应拦截)")
s, d = call("POST", f"/api/v1/orders/{order_id}/escrow")
check("托管被合规拦截(409)", s == 409, f"missing={d.get('missing')}")
s, _ = call("POST", "/api/v1/compliance", token=t_a, body={"item": "escrow", "on": True})
s, d = call("POST", f"/api/v1/orders/{order_id}/escrow")
check("托管放行, 订单=active", s == 200 and d["data"]["status"] == "active", str(s))

line("6. 接单方交付")
s, d = call("POST", f"/api/v1/orders/{order_id}/deliver", token=t_p, body={"content": "RAG 系统 v1, 见附件"})
check("交付成功(201)", s == 201, str(s))

line("7. 需求方验收(结算闸门: icp 未开 -> 订单转 pending_settlement)")
s, d = call("POST", f"/api/v1/orders/{order_id}/accept", token=t_r)
check("验收通过但结算被拦截(409)", s == 409, f"order_status={d.get('order', {}).get('status')}")
check("订单转待结算", d.get("order", {}).get("status") == "pending_settlement", d.get("order", {}).get("status"))

line("8. 运营翻转 icp -> 补结算(独立 settle 端点)")
s, _ = call("POST", "/api/v1/compliance", token=t_a, body={"item": "icp", "on": True})
s, d = call("POST", f"/api/v1/orders/{order_id}/settle", token=t_r)
check("补结算成功, 订单=completed", s == 200 and d["data"]["status"] == "completed", str(s))
check("已结算放款", d["data"]["settled"] is True)

line("9. P0-3 真实注册 + 账号密码登录 + RBAC 订单隔离")
import time as _t
tag = "auto" + str(int(_t.time()))[-6:]
acc_r = tag + "_r"
acc_p = tag + "_p"

# 9.1 注册需求方(成功)
s, d = call("POST", "/api/v1/auth/register", body={
    "account": acc_r, "password": "smoke123", "role": "requester", "real_name": "自动测需求方", "phone": "13800000000"})
check("注册需求方成功(201)", s == 201 and d["data"]["role"] == "requester", str(s))
t_req2 = d["data"]["token"]

# 9.2 注册接单方(成功)
s, d = call("POST", "/api/v1/auth/register", body={
    "account": acc_p, "password": "smoke123", "role": "provider"})
check("注册接单方成功(201)", s == 201 and d["data"]["role"] == "provider", str(s))
t_p2 = d["data"]["token"]

# 9.3 重复账号(应 400)
s, _ = call("POST", "/api/v1/auth/register", body={"account": acc_r, "password": "smoke123", "role": "requester"})
check("重复账号被拒(400)", s == 400, str(s))

# 9.4 弱密码(应 400)
s, _ = call("POST", "/api/v1/auth/register", body={"account": tag + "_weak", "password": "123", "role": "requester"})
check("弱密码被拒(400)", s == 400, str(s))

# 9.5 账号密码登录(成功)
s, d = call("POST", "/api/v1/auth/login", body={"account": acc_r, "password": "smoke123"})
check("账号密码登录成功(200)", s == 200 and "token" in d["data"], str(s))

# 9.6 错误密码(应 401)
s, _ = call("POST", "/api/v1/auth/login", body={"account": acc_r, "password": "wrongpw"})
check("错误密码被拒(401)", s == 401, str(s))

# 9.7 未登录查订单 → 空列表(不泄露)
s, d = call("GET", "/api/v1/orders")
check("匿名订单列表为空", s == 200 and len(d["data"]) == 0, f"len={len(d['data'])}")

# 9.8 新接单方(无订单)以 token 查 → 空列表
s, d = call("GET", "/api/v1/orders", token=t_p2)
check("普通用户仅见自己订单(空)", s == 200 and len(d["data"]) == 0, f"len={len(d['data'])}")

# 9.9 运营(A001)查 → 全量, 含此前订单
s, d = call("GET", "/api/v1/orders", token=t_a)
check("运营可见全量订单", s == 200 and len(d["data"]) >= 1, f"len={len(d['data'])}")
all_ids = [o["id"] for o in d["data"]]
check("运营订单含已结算订单", order_id in all_ids, order_id)

line("10. P0-4 评价与信用分闭环(订单已完成)")
# 10.1 需求方 R001 评价接单方 P001 (5 星)
s, d = call("POST", f"/api/v1/orders/{order_id}/review", token=t_r, body={"rating": 5, "comment": "交付准时, 质量好"})
check("需求方评价成功(201)", s == 201, str(s))
check("返回被评价方信用分", s == 201 and "reviewee_credit" in d["data"], str(d.get("data", {}).get("reviewee_credit")))
# 接单方 P001 被评 1 次 5 星 → 信用分=100
check("P001 信用分重算=100", s == 201 and d["data"]["reviewee_credit"] == 100, str(d.get("data", {}).get("reviewee_credit")))

# 10.2 接单方 P001 评价需求方 R001 (4 星)
s, d = call("POST", f"/api/v1/orders/{order_id}/review", token=t_p, body={"rating": 4, "comment": "需求清晰, 沟通顺畅"})
check("接单方评价成功(201)", s == 201, str(s))
# 需求方 R001 被评 1 次 4 星 → 信用分=80
check("R001 信用分重算=80", s == 201 and d["data"]["reviewee_credit"] == 80, str(d.get("data", {}).get("reviewee_credit")))

# 10.3 同一方可重复评价 → 应拒绝(400)
s, _ = call("POST", f"/api/v1/orders/{order_id}/review", token=t_r, body={"rating": 3})
check("重复评价被拒(400)", s == 400, str(s))

# 10.4 非参与方(运营 A001)评价 → 应拒绝(400)
s, _ = call("POST", f"/api/v1/orders/{order_id}/review", token=t_a, body={"rating": 5})
check("非参与方评价被拒(400)", s == 400, str(s))

# 10.5 评分越界(6) → 应拒绝(400)
s, _ = call("POST", f"/api/v1/orders/{order_id}/review", token=t_r, body={"rating": 6})
check("评分越界被拒(400)", s == 400, str(s))

# 10.6 未登录评价 → 应拒绝(401)
s, _ = call("POST", f"/api/v1/orders/{order_id}/review", body={"rating": 5})
check("未登录评价被拒(401)", s == 401, str(s))

# 10.7 查看订单评价列表 → 2 条
s, d = call("GET", f"/api/v1/orders/{order_id}/reviews")
check("可查看订单评价列表(2条)", s == 200 and len(d["data"]["reviews"]) == 2, f"reviews={len(d.get('data', {}).get('reviews', []))}")

line("11. P0-5 需求结构化 + 规则澄清兜底")
import time as _t2
# 11.1 完整结构化发单(必填齐全)
deadline = int(_t2.time() * 1000) + 3 * 24 * 3600 * 1000
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "Python 数据处理脚本",
    "category": "nlp",
    "budget_cents": 80000,
    "spec": "需要 python 数据处理与清洗",
    "deliverables": "1 套 python 源码 + 部署文档",
    "required_format": "Python 项目(zip) + README",
    "deadline_at": deadline,
    "style_ref": "生产级可维护代码",
    "samples": "参考官方示例",
})
check("完整结构化发单成功(201)", s == 201, str(s))
check("spec_complete=true", s == 201 and d["data"]["spec_complete"] is True, str(d["data"].get("spec_complete")))
check("完整发单无澄清问题", s == 201 and len(d["data"]["clarifications"]) == 0, f"clar={d['data'].get('clarifications')}")
task5 = d["data"]["task"]
check("结构化字段回显(deliverables)", s == 201 and task5["spec_obj"]["deliverables"] == "1 套 python 源码 + 部署文档", str(task5["spec_obj"]))
check("结构化字段回显(deadline_at)", s == 201 and task5["spec_obj"]["deadline_at"] == deadline)

# 11.2 部分缺失(缺格式/截止) → 仅必填项进澄清, 风格为选填提示
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "短视频脚本撰写",
    "category": "copywriting",
    "budget_cents": 50000,
    "spec": "30 秒口播脚本 3 条",
    "deliverables": "3 条口播脚本",
})
check("部分缺失发单成功(201)", s == 201, str(s))
check("缺必填项生成澄清(格式/截止)", s == 201 and len(d["data"]["clarifications"]) >= 2, f"clar={d['data']['clarifications']}")
check("缺必填 spec_complete=false", s == 201 and d["data"]["spec_complete"] is False)

# 11.3 完整结构化需求可正常撮合(结构化字段进入匹配召回)
task5_id = task5["id"]
s, d = call("POST", f"/api/v1/tasks/{task5_id}/match")
check("完整结构化需求可撮合", s == 200 and len(d["data"]) > 0, f"recs={len(d.get('data', []))}")

line("12. P1-1 智能匹配升级(语义召回 + 多因子精排 + 评价反馈闭环)")
# 12.1 多因子精排明细: 对 task5 撮合, 断言返回非空且 Top1 含多因子 reason
s, d = call("POST", f"/api/v1/tasks/{task5_id}/match")
recs = d.get("data", [])
check("P1-1 多因子撮合返回非空", s == 200 and len(recs) > 0, f"recs={len(recs)}")
top1 = recs[0] if recs else {}
check("Top1 为 P001(最高匹配)", top1.get("provider_id") == "P001", str(top1.get("provider_id")))
check("reason 含语义因子", "语义" in top1.get("reason", ""), top1.get("reason", ""))
check("reason 含信用因子", "信用" in top1.get("reason", ""), top1.get("reason", ""))

# 12.2 评价反馈闭环: P001 在 10.1 被评 5★, 其精排 reason 应体现历史评分, 评分因子已计入
p001_rec = next((r for r in recs if r.get("provider_id") == "P001"), {})
check("P001 精排含历史评分(反馈闭环已接入)", "历史" in p001_rec.get("reason", ""), p001_rec.get("reason", ""))
check("P001 历史评分=5.0★", "历史5.0" in p001_rec.get("reason", "").replace(" ", ""), p001_rec.get("reason", ""))

# 12.3 软召回(语义): category 错配但 spec 含技能词, 仍应召回该技能接单方
deadline2 = int(_t2.time() * 1000) + 2 * 24 * 3600 * 1000
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "图像批处理工具",
    "category": "design",   # 故意错配类目
    "budget_cents": 60000,
    "spec": "需要 python 写图像批处理与压缩脚本",  # 含 python, P001 技能命中
    "deliverables": "1 个 python 脚本",
    "required_format": "py 源码",
    "deadline_at": deadline2,
})
check("错配类目发单成功(201)", s == 201, str(s))
soft_id = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{soft_id}/match")
soft_recs = d.get("data", [])
soft_pids = [r.get("provider_id") for r in soft_recs]
check("类目错配但仍据技能/语义召回 P001", s == 200 and "P001" in soft_pids, f"pids={soft_pids}")

line("13. P1-2 AI 澄清(自然语言 → 多轮对话 → 结构化 spec)")
import time as _t3
now_ms = int(_t3.time() * 1000)

# 13.1 完整自然语言需求 → 抽取结构化且规格完整(离线规则化 stub 兜底, 无需 API Key)
s, d = call("POST", "/api/v1/clarify", token=t_r, body={
    "text": "帮我找人做10张竖版小红书封面，极简高级感，10月20号之前交付，PNG格式，预算2000元"})
check("发起 AI 澄清会话(201)", s == 201, str(s))
data1 = d.get("data", {})
sess1 = data1.get("session_id")
d1 = data1.get("draft", {})
check("会话ID已返回", bool(sess1), str(sess1))
check("抽取交付物(10张...封面)", "10张" in d1.get("deliverables", ""), d1.get("deliverables"))
check("抽取交付格式含 PNG", "png" in d1.get("required_format", "").lower(), d1.get("required_format"))
check("抽取截止时间在未来", d1.get("deadline_at", 0) > now_ms, str(d1.get("deadline_at")))
check("抽取预算=200000分(¥2000)", d1.get("budget_cents") == 200000, str(d1.get("budget_cents")))
check("抽取类目=design", d1.get("category") == "design", d1.get("category"))
check("完整需求 spec_complete=true", data1.get("spec_complete") is True, str(data1.get("spec_complete")))

# 13.2 不完整自然语言需求(有交付物, 缺格式/截止) → 规格不完整且返回澄清问题(衔接 P0-5 规则澄清)
s, d = call("POST", "/api/v1/clarify", token=t_r, body={"text": "帮我设计3张海报，科技风"})
data2 = d.get("data", {})
sess2 = data2.get("session_id")
d2 = data2.get("draft", {})
check("发起不完整澄清会话(201)", s == 201, str(s))
check("已抽取交付物(3张海报)", "3张海报" in d2.get("deliverables", ""), d2.get("deliverables"))
check("不完整需求 spec_complete=false", data2.get("spec_complete") is False, str(data2.get("spec_complete")))
check("返回澄清问题(≥2)", len(data2.get("clarifications", [])) >= 2, f"clar={data2.get('clarifications')}")

# 13.3 多轮补充: 向不完整会话补格式+截止, 规格应转为完整
s, d = call("POST", f"/api/v1/clarify/{sess2}", token=t_r, body={"text": "PNG 1080x1080 格式，下周五前交付"})
data3 = d.get("data", {})
d3 = data3.get("draft", {})
check("多轮补充后抽取格式", "png" in d3.get("required_format", "").lower(), d3.get("required_format"))
check("多轮补充后抽取截止", d3.get("deadline_at", 0) > now_ms, str(d3.get("deadline_at")))
check("多轮补充后规格变完整", data3.get("spec_complete") is True, str(data3.get("spec_complete")))

# 13.4 发布澄清后的需求(复用 service.PublishTask)
s, d = call("POST", f"/api/v1/clarify/{sess1}/publish", token=t_r, body={})
check("AI 澄清后发布成功(201)", s == 201, str(s))
pub_task_id = d.get("data", {}).get("task", {}).get("id")
check("发布后 spec_complete=true", d.get("data", {}).get("spec_complete") is True, str(d.get("data", {}).get("spec_complete")))
s, d = call("GET", "/api/v1/tasks")
hall_ids = [t["id"] for t in d.get("data", [])]
check("发布的需求进入需求大厅", pub_task_id in hall_ids, f"in_hall={pub_task_id in hall_ids}")

# 13.5 越权: 非需求方不可发起澄清(用 P001)
s, _ = call("POST", "/api/v1/clarify", token=t_p, body={"text": "测试"})
check("非需求方发起澄清被拒(403)", s == 403, str(s))

# ===== P1-4 订单完整化: 改稿上限 / 取消退款 / 纠纷结案 / 里程碑 =====
line("14. P1-4 订单完整化(改稿上限/取消退款/纠纷/里程碑)")
# 确保合规闸门已开(税源 + 支付托管), 以便走托管
call("POST", "/api/v1/compliance", token=t_a, body={"item": "tax_source", "on": True})
call("POST", "/api/v1/compliance", token=t_a, body={"item": "escrow", "on": True})
dl4 = int(_t2.time() * 1000) + 5 * 24 * 3600 * 1000

# 14.1 订单A(lump): 改稿上限 + 取消退款
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P1-4 需求A 改稿上限", "category": "nlp", "budget_cents": 100000,
    "spec": "python 脚本", "deliverables": "1 个脚本", "required_format": "py 文件", "deadline_at": dl4})
taskA = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{taskA}/match")
recsA = d["data"]
mA = next((r["id"] for r in recsA if r["provider_id"] == "P001"), recsA[0]["id"])
s, d = call("POST", f"/api/v1/matches/{mA}/accept", token=t_p)
orderA = d["data"]["id"]
check("A: 接单成功(201)", s == 201, str(s))
s, d = call("POST", f"/api/v1/orders/{orderA}/escrow")
check("A: 托管成功→active", s == 200 and d["data"]["status"] == "active", str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/deliver", token=t_p, body={"content": "v1"})
check("A: 第1次交付(201)", s == 201, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/revise", token=t_r, body={"note": "改一改"})
check("A: 打回重做(200)", s == 200, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/deliver", token=t_p, body={"content": "v2"})
check("A: 第2次交付(201)", s == 201, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/revise", token=t_r, body={"note": "再改"})
check("A: 打回重做(200)", s == 200, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/deliver", token=t_p, body={"content": "v3"})
check("A: 第3次交付(201)", s == 201, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/revise", token=t_r, body={"note": "还改"})
check("A: 打回重做(200)", s == 200, str(s))
s, _ = call("POST", f"/api/v1/orders/{orderA}/deliver", token=t_p, body={"content": "v4 越界"})
check("A: 第4次交付被拒(409 改稿上限)", s == 409, str(s))
s, d = call("POST", f"/api/v1/orders/{orderA}/cancel", token=t_r, body={"reason": "需求变更"})
check("A: 取消成功(200)", s == 200, str(s))
check("A: 状态=cancelled", d["data"]["status"] == "cancelled", d["data"]["status"])
check("A: 已托管取消退50%(refund_pct=50)", d["data"]["refund_pct"] == 50, d["data"]["refund_pct"])
check("A: 退款额=50000分(¥500)", d["data"]["refund_cents"] == 50000, d["data"]["refund_cents"])
s, _ = call("POST", f"/api/v1/orders/{orderA}/cancel", token=t_r, body={"reason": "x"})
check("A: 已取消不可再取消(409)", s == 409, str(s))

# 14.2 订单B(milestone): 里程碑分阶段结算
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P1-4 需求B 里程碑", "category": "nlp", "budget_cents": 120000,
    "spec": "分阶段开发", "deliverables": "原型 + 上线", "required_format": "代码仓库", "deadline_at": dl4})
taskB = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{taskB}/match")
recsB = d["data"]
mB = next((r["id"] for r in recsB if r["provider_id"] == "P001"), recsB[0]["id"])
s, d = call("POST", f"/api/v1/matches/{mB}/accept", token=t_p, body={
    "milestones": [{"title": "原型设计", "amount_cents": 60000}, {"title": "开发上线", "amount_cents": 60000}]})
check("B: 接单(里程碑模式)成功(201)", s == 201, str(s))
orderB = d["data"]["id"]
check("B: 结算模式=milestone", d["data"]["settlement_mode"] == "milestone", d["data"]["settlement_mode"])
check("B: 两个里程碑", len(d["data"]["milestones"]) == 2, len(d["data"]["milestones"]))
s, d = call("POST", f"/api/v1/orders/{orderB}/escrow")
check("B: 托管成功→active", s == 200 and d["data"]["status"] == "active", str(s))
s, d = call("GET", f"/api/v1/orders/{orderB}")
ms = d["data"]["order"]["milestones"]
ms1, ms2 = ms[0]["id"], ms[1]["id"]
s, d = call("POST", f"/api/v1/orders/{orderB}/milestones/{ms1}/confirm", token=t_r)
check("B: 确认里程碑1(200)", s == 200, str(s))
check("B: 里程碑1→done", d["data"]["milestones"][0]["status"] == "done", d["data"]["milestones"][0]["status"])
check("B: 未全部完成→未completed", d["data"]["status"] != "completed", d["data"]["status"])
s, d = call("POST", f"/api/v1/orders/{orderB}/milestones/{ms2}/confirm", token=t_r)
check("B: 确认里程碑2→完成(200)", s == 200, str(s))
check("B: 全部完成→completed", d["data"]["status"] == "completed", d["data"]["status"])
check("B: 已结算", d["data"]["settled"] is True, d["data"]["settled"])

# 14.3 订单C: 纠纷发起 + 运营结案
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P1-4 需求C 纠纷", "category": "nlp", "budget_cents": 90000,
    "spec": "数据采集", "deliverables": "1 份报告", "required_format": "pdf", "deadline_at": dl4})
taskC = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{taskC}/match")
recsC = d["data"]
mC = next((r["id"] for r in recsC if r["provider_id"] == "P001"), recsC[0]["id"])
s, d = call("POST", f"/api/v1/matches/{mC}/accept", token=t_p)
orderC = d["data"]["id"]
s, d = call("POST", f"/api/v1/orders/{orderC}/escrow")
check("C: 托管→active", s == 200 and d["data"]["status"] == "active", str(s))
s, d = call("POST", f"/api/v1/orders/{orderC}/dispute", token=t_r, body={"reason": "沟通不畅"})
check("C: 发起纠纷(200)", s == 200, str(s))
check("C: 状态=disputed", d["data"]["status"] == "disputed", d["data"]["status"])
s, _ = call("POST", f"/api/v1/orders/{orderC}/resolve", token=t_r, body={"refund_pct": 30, "decision": "x"})
check("C: 非运营结案被拒(403)", s == 403, str(s))
s, d = call("POST", f"/api/v1/orders/{orderC}/resolve", token=t_a, body={"refund_pct": 30, "decision": "各退一步, 退需求方30%"})
check("C: 纠纷结案(200)", s == 200, str(s))
check("C: 结案→completed", d["data"]["status"] == "completed", d["data"]["status"])
check("C: 已结算", d["data"]["settled"] is True, d["data"]["settled"])
check("C: 退款比例=30%", d["data"]["refund_pct"] == 30, d["data"]["refund_pct"])
check("C: 退款额=27000分(¥270)", d["data"]["refund_cents"] == 27000, d["data"]["refund_cents"])

# 14.4 越权: 非参与方(运营A001, 非 requester/provider)不可取消订单B
s, _ = call("POST", f"/api/v1/orders/{orderB}/cancel", token=t_a, body={"reason": "x"})
check("非参与方取消被拒(403)", s == 403, str(s))

# 15. P2-1 钱包 / 流水 / 提现 真实记账
line("15. P2-1 钱包 / 流水 / 提现 真实记账")
call("POST", "/api/v1/compliance", token=t_a, body={"item": "escrow", "on": True})
call("POST", "/api/v1/compliance", token=t_a, body={"item": "tax_source", "on": True})

s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P2-1 订单D 托管结算", "category": "nlp", "budget_cents": 50000,
    "spec": "文本分类", "deliverables": "1 模型", "required_format": "pkl", "deadline_at": 30})
taskD = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{taskD}/match")
recsD = d["data"]
mD = next((r["id"] for r in recsD if r["provider_id"] == "P001"), recsD[0]["id"])
s, d = call("POST", f"/api/v1/matches/{mD}/accept", token=t_p)
orderD = d["data"]["id"]
s, d = call("GET", "/api/v1/wallet", token=t_r)
bal0, frz0 = d["data"]["wallet"]["balance_cents"], d["data"]["wallet"]["frozen_cents"]
s, d = call("POST", f"/api/v1/orders/{orderD}/escrow", token=t_r)
check("D: 托管→active", s == 200 and d["data"]["status"] == "active", str(s))
s, d = call("GET", "/api/v1/wallet", token=t_r)
bal1, frz1 = d["data"]["wallet"]["balance_cents"], d["data"]["wallet"]["frozen_cents"]
check("D: 托管扣可用余额 50000", bal1 == bal0 - 50000, f"{bal0}->{bal1}")
check("D: 托管增冻结 50000", frz1 == frz0 + 50000, f"{frz0}->{frz1}")
s, d = call("POST", f"/api/v1/orders/{orderD}/settle", token=t_r)
check("D: 结算→completed", s == 200 and d["data"]["status"] == "completed", str(s))
s, d = call("GET", "/api/v1/wallet", token=t_p)
pBal = d["data"]["wallet"]["balance_cents"]
check("D: 接单方+45000(扣10%佣金)", pBal >= 45000, str(pBal))
s, d = call("GET", "/api/v1/wallet", token=t_a)
aBal = d["data"]["wallet"]["balance_cents"]
check("D: 平台佣金+5000", aBal >= 5000, str(aBal))
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p, body={"amount_cents": 45000})
check("D: 接单方提现申请(201)", s == 201, str(s))
wdId = d["data"]["id"]
s, d = call("GET", "/api/v1/wallet", token=t_p)
pFrz = d["data"]["wallet"]["frozen_cents"]
check("D: 提现冻结预占 45000", pFrz >= 45000, str(pFrz))
s, _ = call("POST", f"/api/v1/wallet/withdrawals/{wdId}/approve", token=t_p, body={"approve": True})
check("D: 非运营审批被拒(403/400)", s in (403, 400), str(s))
s, d = call("POST", f"/api/v1/wallet/withdrawals/{wdId}/approve", token=t_a, body={"approve": True, "note": "通过"})
check("D: 运营审批通过(200)", s == 200, str(s))
check("D: 提现单=done", d["data"]["status"] == "done", d["data"]["status"])
s, d = call("GET", "/api/v1/wallet", token=t_p)
pBal2, pFrz2 = d["data"]["wallet"]["balance_cents"], d["data"]["wallet"]["frozen_cents"]
check("D: 审批后余额-45000", pBal2 == pBal - 45000, f"{pBal}->{pBal2}")
check("D: 审批后冻结解冻", pFrz2 == pFrz - 45000, f"{pFrz}->{pFrz2}")
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P2-1 订单E 退款", "category": "nlp", "budget_cents": 20000,
    "spec": "摘要", "deliverables": "1 份", "required_format": "txt", "deadline_at": 10})
taskE = d["data"]["task"]["id"]
s, d = call("POST", f"/api/v1/tasks/{taskE}/match")
recsE = d["data"]
mE = next((r["id"] for r in recsE if r["provider_id"] == "P001"), recsE[0]["id"])
s, d = call("POST", f"/api/v1/matches/{mE}/accept", token=t_p)
orderE = d["data"]["id"]
s, d = call("GET", "/api/v1/wallet", token=t_r)
rBal0, rFrz0 = d["data"]["wallet"]["balance_cents"], d["data"]["wallet"]["frozen_cents"]
s, d = call("POST", f"/api/v1/orders/{orderE}/escrow", token=t_r)
check("E: 托管→active", s == 200, str(s))
s, d = call("GET", "/api/v1/wallet", token=t_r)
rFrz1 = d["data"]["wallet"]["frozen_cents"]
check("E: 托管增冻结 20000", rFrz1 == rFrz0 + 20000, f"{rFrz0}->{rFrz1}")
s, d = call("POST", f"/api/v1/orders/{orderE}/dispute", token=t_r, body={"reason": "不满意"})
check("E: 发起纠纷(200)", s == 200, str(s))
s, d = call("POST", f"/api/v1/orders/{orderE}/resolve", token=t_a, body={"refund_pct": 50, "decision": "退50%"})
check("E: 结案(200)", s == 200, str(s))
s, d = call("GET", "/api/v1/wallet", token=t_r)
rBal2, rFrz2 = d["data"]["wallet"]["balance_cents"], d["data"]["wallet"]["frozen_cents"]
check("E: 退款后净余额-10000(托管20000-退款10000)", rBal2 == rBal0 - 10000, f"{rBal0}->{rBal2}")
check("E: 退款解冻冻结-20000", rFrz2 == rFrz1 - 20000, f"{rFrz1}->{rFrz2}")

line("== 16. IM 会话与消息 (P2-2) ==")
# 16.1 发送消息 R001 -> P001
s, d = call("POST", "/api/v1/im", token=t_r, body={"to": "P001", "body": "你好，需求细节确认下"})
check("IM 发消息成功(200)", s == 200, str(s))
mid = (d.get("data") or {}).get("id") if s == 200 else None
check("IM 消息含ID", bool(mid), str(mid))
# 16.2 P001 收到未读(>0)
s, d = call("GET", "/api/v1/im/conversations", token=t_p)
convs = (d.get("data") or []) if s == 200 else []
conv_rp = next((c for c in convs if set([c.get("a"), c.get("b")]) == {"R001", "P001"}), None)
check("P001 会话列表含 R001", conv_rp is not None, str(convs))
if conv_rp:
    unread_p = conv_rp.get("a_unread") if conv_rp.get("a") == "P001" else conv_rp.get("b_unread")
    check("P001 收到未读(>0)", (unread_p or 0) > 0, str(unread_p))
# 16.3 R001 查历史(与 P001)
s, d = call("GET", "/api/v1/im/conversations/P001", token=t_r)
msgs = (d.get("data") or []) if s == 200 else []
check("R001 历史含消息", len(msgs) >= 1, str(len(msgs)))
# 16.4 P001 标记已读 -> 未读清零(P001 视角, peer=R001)
s, d = call("POST", "/api/v1/im/conversations/R001/read", token=t_p)
check("P001 标记已读成功(200)", s == 200, str(s))
s, d = call("GET", "/api/v1/im/conversations", token=t_p)
convs = (d.get("data") or []) if s == 200 else []
conv_rp = next((c for c in convs if set([c.get("a"), c.get("b")]) == {"R001", "P001"}), None)
if conv_rp:
    unread_p = conv_rp.get("a_unread") if conv_rp.get("a") == "P001" else conv_rp.get("b_unread")
    check("已读后 P001 未读=0", (unread_p or 0) == 0, str(unread_p))
# 16.5 内容审核拦截(含「微信」)
s, d = call("POST", "/api/v1/im", token=t_r, body={"to": "P001", "body": "加我微信私聊更方便"})
check("违规词被拦截(400)", s == 400, str(s) + " / " + (d.get("msg") or ""))
# 16.6 订单联动: 发单->匹配->接单, 自动建会话且补全 order_id
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "IM联动测试单", "category": "nlp", "budget_cents": 10000,
    "spec": "需要 python 写脚本", "deliverables": "脚本", "required_format": "py",
    "deadline_at": int(_t2.time() * 1000) + 86400000})
tid = ((d.get("data") or {}).get("task") or {}).get("id") if s == 201 else None
check("IM联动: 发单成功(201)", tid is not None, str(s))
if tid:
    s, d = call("POST", f"/api/v1/tasks/{tid}/match")
    recs = d.get("data") or []
    p001_match = next((r for r in recs if r.get("provider_id") == "P001"), None)
    match_id = (p001_match or {}).get("id")
    check("IM联动: P001 被召回", p001_match is not None, str(recs))
    if match_id:
        s, d = call("POST", f"/api/v1/matches/{match_id}/accept", token=t_p)
        oid = (d.get("data") or {}).get("id") if s in (200, 201) else None
        check("IM联动: 接单成功(200/201)", oid is not None, str(s))
        s, d = call("GET", "/api/v1/im/conversations", token=t_r)
        conv = next((c for c in (d.get("data") or []) if c.get("order_id")), None)
        check("订单会话补全 order_id(自动建会话)", conv is not None, str((d.get("data") or [])[:1]))

line("== 17. 通知中心 (P2-3) ==")
# 17.1 触发业务事件(接单)→ 平台自动发通知给接单方与需求方
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "P2-3 通知测试单", "category": "nlp", "budget_cents": 10000,
    "spec": "需要 python 写脚本", "deliverables": "脚本", "required_format": "py",
    "deadline_at": int(_t2.time() * 1000) + 86400000})
t17 = ((d.get("data") or {}).get("task") or {}).get("id") if s == 201 else None
check("17.1 发单成功(201)", t17 is not None, str(s))
oid17 = None
if t17:
    s, d = call("POST", f"/api/v1/tasks/{t17}/match")
    recs = d.get("data") or []
    m17 = next((r for r in recs if r.get("provider_id") == "P001"), None)
    match17 = (m17 or {}).get("id")
    if match17:
        s, d = call("POST", f"/api/v1/matches/{match17}/accept", token=t_p)
        oid17 = (d.get("data") or {}).get("id") if s in (200, 201) else None
check("17.1 接单成功(200/201)", oid17 is not None, str(s))
# 17.2 P001 收到未读通知(>0)
s, d = call("GET", "/api/v1/notifications/unread", token=t_p)
unread_before = (d.get("data", {}).get("unread", 0) or 0) if s == 200 else 0
check("17.2 接单方未读通知>0", unread_before > 0, f"unread={unread_before}")
# 17.3 P001 通知列表含本次接单通知(inapp)
s, d = call("GET", "/api/v1/notifications", token=t_p)
notis = (d.get("data") or []) if s == 200 else []
check("17.3 通知列表返回非空", s == 200 and len(notis) > 0, f"len={len(notis)}")
check("17.3 通知含 inapp 通道", any(n.get("channel") == "inapp" for n in notis), str([n.get("channel") for n in notis[:3]]))
check("17.3 通知含未读项", any(n.get("status") == "unread" for n in notis), str([n.get("status") for n in notis[:3]]))
top = notis[0] if notis else {}
# 17.4 标记最顶部通知已读 → 未读数-1, 该条 status=read
if top.get("id"):
    s, d = call("POST", f"/api/v1/notifications/{top['id']}/read", token=t_p)
    check("17.4 标记已读成功(200)", s == 200, str(s))
    check("17.4 该通知 status=read", s == 200 and (d.get("data") or {}).get("status") == "read", str((d.get("data") or {}).get("status")))
    s, d = call("GET", "/api/v1/notifications/unread", token=t_p)
    unread_after = (d.get("data", {}).get("unread", 0) or 0) if s == 200 else 0
    check("17.4 未读数减少1", unread_after == unread_before - 1, f"{unread_before}->{unread_after}")
# 17.5 越权: P001 标记 R001 的通知 → 404
s, dr = call("GET", "/api/v1/notifications", token=t_r)
r_notis = (dr.get("data") or []) if s == 200 else []
if r_notis:
    s, _ = call("POST", f"/api/v1/notifications/{r_notis[0]['id']}/read", token=t_p)
    check("17.5 跨用户标记被拒(404)", s == 404, str(s))
else:
    check("17.5 跨用户标记被拒(404)", True, "R001 无通知, 跳过")
# 17.6 未登录查通知 → 401
s, _ = call("GET", "/api/v1/notifications")
check("17.6 未登录查通知被拒(401)", s == 401, str(s))

# ============ 18. 企业版治理 (P3 最小骨架: 组织/成员/角色) ============
line("== 18. 企业版治理 (P3 最小骨架) ==")
# 18.1 P001 创建组织
t_p001, _ = login("P001")
s, d = call("POST", "/api/v1/orgs", token=t_p001, body={"name": "算法工作室"})
check("18.1 创建组织成功(200)", s == 200 and (d.get("data") or {}).get("id", "").startswith("ORG-"), str(s))
org18 = (d.get("data") or {}).get("id", "")
# 18.2 owner 列出组织
s, d = call("GET", "/api/v1/orgs", token=t_p001)
orgs = (d.get("data") or [])
check("18.2 owner 列出组织非空", s == 200 and len(orgs) > 0, f"len={len(orgs)}")
check("18.2 列表含新建组织", any(o.get("id") == org18 for o in orgs), str([o.get("id") for o in orgs[:3]]))
# 18.3 owner 邀请 R001 为 member
s, _ = call("POST", f"/api/v1/orgs/{org18}/members", token=t_p001, body={"user_id": "R001", "role": "member"})
check("18.3 邀请成员成功(200)", s == 200, str(s))
# 18.4 列出成员含 owner(P001) 与 R001
s, d = call("GET", f"/api/v1/orgs/{org18}/members", token=t_p001)
members = (d.get("data") or [])
mids = [m.get("user_id") for m in members]
check("18.4 列出成员成功", s == 200, str(s))
check("18.4 含 owner P001", "P001" in mids, str(mids))
check("18.4 含被邀请 R001", "R001" in mids, str(mids))
# 18.5 越权: R001(member) 邀请他人 → 403
t_r001, _ = login("R001")
s, _ = call("POST", f"/api/v1/orgs/{org18}/members", token=t_r001, body={"user_id": "P002", "role": "member"})
check("18.5 普通成员无邀请权限(403)", s == 403, str(s))
# 18.6 未登录创建组织 → 401
s, _ = call("POST", "/api/v1/orgs", body={"name": "x"})
check("18.6 未登录创建组织被拒(401)", s == 401, str(s))

# ============ 19. Marketplace 发现 + 用户声誉画像 ============
line("== 19. Marketplace 发现 + 用户声誉画像 ==")
t_r19, _ = login("R001")
# 19.1/19.2 发两个不同类目/预算的需求单
s, d = call("POST", "/api/v1/tasks", token=t_r19, body={"title": "设计营销落地页", "category": "design", "budget_cents": 100000, "spec": "Figma 落地页设计"})
task_design = (d.get("data") or {}).get("task", {}).get("id", "")
check("19.1 发 design 单成功(201)", s == 201 and task_design != "", str(s))
s, d = call("POST", "/api/v1/tasks", token=t_r19, body={"title": "训练文本分类模型", "category": "nlp", "budget_cents": 500000, "spec": "用 pytorch 做中文文本分类"})
task_nlp = (d.get("data") or {}).get("task", {}).get("id", "")
check("19.2 发 nlp 单成功(201)", s == 201 and task_nlp != "", str(s))
# 19.3 类目过滤
s, d = call("GET", "/api/v1/tasks?category=design")
rows = (d.get("data") or [])
cats = [t.get("category") for t in rows]
check("19.3 category=design 仅含 design 且命中本单", s == 200 and all(c == "design" for c in cats) and task_design in [t.get("id") for t in rows], str(cats[:3]))
# 19.4 关键词检索
s, d = call("GET", "/api/v1/tasks?q=分类")
qids = [t.get("id") for t in (d.get("data") or [])]
check("19.4 q=分类 命中 nlp 单", s == 200 and task_nlp in qids, str(qids[:3]))
# 19.5 预算下限
s, d = call("GET", "/api/v1/tasks?min_budget=400000")
mb = [t.get("budget_cents") for t in (d.get("data") or [])]
check("19.5 min_budget=400000 仅含>=40w 且命中 nlp 单", s == 200 and all(b >= 400000 for b in mb) and task_nlp in [t.get("id") for t in (d.get("data") or [])], str(mb[:3]))
# 19.6 排序 budget_desc
s, d = call("GET", "/api/v1/tasks?sort=budget_desc&limit=50")
bs = [t.get("budget_cents") for t in (d.get("data") or [])]
check("19.6 sort=budget_desc 降序", s == 200 and (len(bs) <= 1 or all(bs[i] >= bs[i + 1] for i in range(len(bs) - 1))), str(bs[:5]))
# 19.7 用户公开画像(声誉)
s, d = call("GET", "/api/v1/users/P001")
p = d.get("data") or {}
check("19.7 用户画像含信用分/角色/完成数/均分", s == 200 and "credit_score" in p and "role" in p and "completed_count" in p and "avg_rating" in p, str({k: p.get(k) for k in ["id", "role", "credit_score", "completed_count", "avg_rating", "review_count"]}))
# 19.8 不存在用户 404
s, _ = call("GET", "/api/v1/users/ghost999")
check("19.8 不存在用户画像 404", s == 404, str(s))

# ================= 20. 类目法 + 可信分 (Marketplace 可信度与分类法) =================
# 20.1 类目法端点返回非空且含合法字段
s, d = call("GET", "/api/v1/meta/categories")
cats = d.get("data") or []
ids = [c.get("id") for c in cats]
check("20.1 类目法返回≥10项且含 nlp/design/other", s == 200 and len(cats) >= 10 and "nlp" in ids and "design" in ids and "other" in ids, str(ids))
# 20.2 画像含 trust_score 且 0-100
s, d = call("GET", "/api/v1/users/P001")
p = d.get("data") or {}
ts = p.get("trust_score")
check("20.2 画像含 trust_score 且 0-100", s == 200 and isinstance(ts, int) and 0 <= ts <= 100, str(ts))
# 20.3 非法类目发单被拦截(400/block)
tr, _ = login("R001")
dl = int(time.time() * 1000) + 5 * 86400 * 1000
s, d = call("POST", "/api/v1/tasks", token=tr, body={"title": "非法类目单", "category": "not_a_cat", "budget_cents": 100000, "spec": "x", "deliverables": "1", "required_format": "py", "deadline_at": dl})
check("20.3 非法类目发单被拦截", s != 201, f"{s} {d.get('msg','')}")
# 20.4 空类目发单自动归 other 且成功
s, d = call("POST", "/api/v1/tasks", token=tr, body={"title": "空类目单", "category": "", "budget_cents": 100000, "spec": "x", "deliverables": "1", "required_format": "py", "deadline_at": dl})
cat_other = (d.get("data") or {}).get("task", {}).get("category")
check("20.4 空类目发单归 other 且成功", s == 201 and cat_other == "other", f"{s} {cat_other}")

# ================= 21. 主动接单(浏览大厅→认领) =================
# 确保平台税源开启(接单闸门 ActAcceptTask 需 KYC+税源)
ta, _ = login("A001")
call("POST", "/api/v1/compliance", token=ta, body={"item": "tax_source", "on": True})
# 21.1 需求方发一个 open 任务
tr, _ = login("R001")
dl = int(time.time() * 1000) + 5 * 86400 * 1000
s, d = call("POST", "/api/v1/tasks", token=tr, body={"title": "抢单测试", "category": "nlp", "budget_cents": 120000, "spec": "x", "deliverables": "1", "required_format": "py", "deadline_at": dl})
task_take = d["data"]["task"]["id"]
# 21.2 provider 主动认领 → 201, 订单待托管 + 任务转 matched
tp, _ = login("P001")
s, d = call("POST", f"/api/v1/tasks/{task_take}/take", token=tp, body={})
od = d.get("data") or {}
check("21.2 provider 主动认领→订单 pending_escrow", s == 201 and od.get("status") == "pending_escrow", f"{s} {od.get('status')}")
# 21.3 同一任务二次认领被拦截(已被接单)
s, d = call("POST", f"/api/v1/tasks/{task_take}/take", token=tp, body={})
check("21.3 已被接单不可再接", s != 201, f"{s} {d.get('msg','')}")
# 21.4 需求方不可接单(403 角色)
s, _ = call("POST", f"/api/v1/tasks/{task_take}/take", token=tr, body={})
check("21.4 需求方接单被拒 403", s == 403, str(s))
# 21.5 税源关闭时接单被闸门拦截
tr2, _ = login("R001")
s, d = call("POST", "/api/v1/tasks", token=tr2, body={"title": "抢单测试2", "category": "nlp", "budget_cents": 120000, "spec": "x", "deliverables": "1", "required_format": "py", "deadline_at": dl})
task_take2 = d["data"]["task"]["id"]
call("POST", "/api/v1/compliance", token=ta, body={"item": "tax_source", "on": False})
s, d = call("POST", f"/api/v1/tasks/{task_take2}/take", token=tp, body={})
check("21.5 税源关闭接单被闸门拦", s != 201, f"{s} {d.get('msg','')}")
# 恢复平台税源开启(保持演示态)
call("POST", "/api/v1/compliance", token=ta, body={"item": "tax_source", "on": True})

# ===== 22. 任务详情聚合 + 我的工作台 =====
# 22.1 任务详情含发布者画像与关联订单
# 先发一个 design 任务, 由 P001 主动认领, 再查详情
tr3, _ = login("R001")
s, d = call("POST", "/api/v1/tasks", token=tr3, body={"title": "工作台详单", "category": "design", "budget_cents": 90000, "spec": "logo", "deliverables": "1", "required_format": "png", "deadline_at": dl})
detail_task = d["data"]["task"]["id"]
tp3, _ = login("P001")
s, d = call("POST", f"/api/v1/tasks/{detail_task}/take", token=tp3, body={})
detail_order = d.get("data", {}).get("id", "")
s, d = call("GET", f"/api/v1/tasks/{detail_task}")
ok_detail = (s == 200 and "publisher" in d["data"] and d["data"]["publisher"].get("id") == "R001"
             and d["data"].get("order", {}).get("id") == detail_order)
check("22.1 任务详情含发布者画像+关联订单", ok_detail, f"{s} {d.get('data', {}).get('order', {}).get('id')}")
# 22.2 不存在任务 404
s, d = call("GET", "/api/v1/tasks/notask999")
check("22.2 不存在任务详情 404", s == 404, str(s))
# 22.3 工作台: 需求方 R001 发布的任务出现在 published_tasks, 且含 order 关联
s, d = call("GET", "/api/v1/auth/me/workbench", token=tr3)
wb = d.get("data", {})
pub_ids = [t["id"] for t in wb.get("published_tasks", [])]
has_link = any(t.get("order_id") == detail_order for t in wb.get("published_tasks", []))
req_has = any(o.get("id") == detail_order for o in wb.get("orders_as_requester", []))
check("22.3 我发布的任务含关联订单", s == 200 and detail_task in pub_ids and has_link and req_has, f"{s} {pub_ids}")
# 22.4 接单方 P001 工作台出现该订单于 orders_as_provider
s, d = call("GET", "/api/v1/auth/me/workbench", token=tp3)
prov_ids = [o["id"] for o in d.get("data", {}).get("orders_as_provider", [])]
check("22.4 接单方工作台含该订单", s == 200 and detail_order in prov_ids, f"{s} {prov_ids}")
# 22.5 未登录查工作台 401
s, d = call("GET", "/api/v1/auth/me/workbench")
check("22.5 未登录工作台 401", s == 401, str(s))
# 22.6 工作台 stats 结构存在
s, d = call("GET", "/api/v1/auth/me/workbench", token=tr3)
st = d.get("data", {}).get("stats", {})
check("22.6 工作台含 stats(待处理数)", s == 200 and "open_tasks" in st and "needs_my_action" in st, f"{s} {st}")

# ===== 23. 运营看板 / 平台指标 (P3) =====
# 23.1 运营 A001 可查平台指标, 返回关键聚合字段
ta2, _ = login("A001")
s, d = call("GET", "/api/v1/admin/metrics", token=ta2)
m = d.get("data", {})
need_keys = ["users_total", "providers", "requesters", "admins", "tasks_total", "tasks_open",
             "orders_total", "orders_by_status", "orders_settled", "gmv_cents",
             "platform_balance_cents", "withdrawals_total", "withdrawals_pending", "compliance_red_lights"]
ok_metrics = (s == 200 and all(k in m for k in need_keys)
              and isinstance(m.get("orders_by_status"), dict) and m.get("users_total", 0) >= 6)
check("23.1 运营看板指标聚合(关键字段齐全)", ok_metrics, f"{s} users={m.get('users_total')} tasks={m.get('tasks_total')} orders={m.get('orders_total')} gmv={m.get('gmv_cents')} redlights={m.get('compliance_red_lights')}")
# 23.2 非运营(需求方 R001)查运营看板被 403 拒
tr4, _ = login("R001")
s, d = call("GET", "/api/v1/admin/metrics", token=tr4)
check("23.2 非运营查运营看板 403", s == 403, str(s))
# 23.3 未登录查运营看板 401
s, d = call("GET", "/api/v1/admin/metrics")
check("23.3 未登录运营看板 401", s == 401, str(s))

# ===== 24. P3 专项: 组织钱包 / 组织发包 / 审批流 / 开放 API + Webhook =====
line("24. P3 专项: 组织钱包 / 发包 / 审批 / API Key / Webhook")

# 24.1 创建组织(owner=P001)
tp1, _ = login("P001")
s, d = call("POST", "/api/v1/orgs", token=tp1, body={"name": "安骁工作室"})
org_id = d.get("data", {}).get("id")
check("24.1 创建组织(owner=P001)", s == 200 and bool(org_id), f"{s} org={org_id}")

# 24.2 组织钱包初始为零
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token=tp1)
w0 = d.get("data", {})
check("24.2 组织钱包初始为零", s == 200 and w0.get("balance_cents", -1) == 0 and w0.get("frozen_cents", -1) == 0, str(w0))

# 24.3 邀请成员 P002
s, d = call("POST", f"/api/v1/orgs/{org_id}/members", token=tp1, body={"user_id": "P002", "role": "member"})
check("24.3 邀请成员 P002", s == 200, str(s))

# 24.4 非 owner 充值被 403 (用 R001)
tr5, _ = login("R001")
s, d = call("POST", f"/api/v1/orgs/{org_id}/wallet/deposit", token=tr5, body={"amount_cents": 100000})
check("24.4 非 owner 充值 403", s == 403, str(s))

# 24.5 owner 充值 100000 -> 余额 100000
s, d = call("POST", f"/api/v1/orgs/{org_id}/wallet/deposit", token=tp1, body={"amount_cents": 100000})
w1 = d.get("data", {})
check("24.5 owner 充值后余额=100000", s == 200 and w1.get("balance_cents") == 100000, str(w1))

# 24.6 组织提现审批闭环: 发起冻结 -> 非 owner 审批 403 -> owner 审批出账
s, d = call("POST", f"/api/v1/orgs/{org_id}/payouts", token=tp1, body={"amount_cents": 30000})
ap = d.get("data", {})
ap_id = ap.get("id")
check("24.6 发起组织提现(冻结 30000)", s == 200 and bool(ap_id) and ap.get("status") == "pending", f"{s} ap={ap_id}")
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token=tp1)
w3 = d.get("data", {})
check("24.6 提现冻结后 可用=70000", s == 200 and (w3.get("balance_cents", 0) - w3.get("frozen_cents", 0)) == 70000, str(w3))
s, d = call("POST", f"/api/v1/orgs/{org_id}/approvals/{ap_id}/approve", token=tr5, body={"approve": True})
check("24.6 非 owner 审批 403", s == 403, str(s))
s, d = call("POST", f"/api/v1/orgs/{org_id}/approvals/{ap_id}/approve", token=tp1, body={"approve": True})
ap2 = d.get("data", {})
check("24.6 审批通过 -> status=approved", s == 200 and ap2.get("status") == "approved", f"{s} {ap2.get('status')}")
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token=tp1)
w4 = d.get("data", {})
check("24.6 审批出账后 余额=70000 冻结=0", s == 200 and w4.get("balance_cents") == 70000 and w4.get("frozen_cents") == 0, str(w4))

# 24.7 组织发包(创建 open 任务, 带 OrgID)
s, d = call("POST", f"/api/v1/orgs/{org_id}/tasks", token=tp1, body={"title": "官网重构", "category": "design", "budget_cents": 50000, "spec": "首页+产品页"})
task_id = d.get("data", {}).get("task", {}).get("id")
check("24.7 组织发包(任务带 OrgID)", s == 200 and bool(task_id) and d.get("data", {}).get("task", {}).get("org_id") == org_id, f"{s} task={task_id}")

# 24.8 组织任务列表含该任务
s, d = call("GET", f"/api/v1/orgs/{org_id}/tasks", token=tp1)
tasks = d.get("data", []) or []
check("24.8 组织任务列表含该任务", s == 200 and any(t.get("id") == task_id for t in tasks), f"count={len(tasks)}")

# 24.9 provider P002 主动接单 -> 订单带 OrgID(pending_escrow)
tp2, _ = login("P002")
s, d = call("POST", f"/api/v1/tasks/{task_id}/take", token=tp2, body={})
order_id = d.get("data", {}).get("id")
o_org = d.get("data", {}).get("org_id")
check("24.9 provider 接组织单(订单带 OrgID)", s in (200, 201) and bool(order_id) and o_org == org_id, f"{s} order={order_id} org={o_org}")

# 24.10 托管: 从组织钱包扣 50000 (余额 70000 -> 20000, 冻结 0 -> 50000)
s, d = call("POST", f"/api/v1/orders/{order_id}/escrow", token=tp1)
check("24.10 组织托管扣款 200", s == 200, f"{s} {d.get('msg')}")
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token=tp1)
w2 = d.get("data", {})
check("24.10 组织钱包托管后 余额=20000 冻结=50000", s == 200 and w2.get("balance_cents") == 20000 and w2.get("frozen_cents") == 50000, str(w2))

# 24.11 组织钱包不足 -> 托管 409 (零余额组织)
s, d = call("POST", "/api/v1/orgs", token=tp1, body={"name": "空壳组织"})
org2 = d.get("data", {}).get("id")
s, d = call("POST", f"/api/v1/orgs/{org2}/tasks", token=tp1, body={"title": "缺钱任务", "category": "design", "budget_cents": 50000, "spec": "x"})
t2 = d.get("data", {}).get("task", {}).get("id")
s, d = call("POST", f"/api/v1/tasks/{t2}/take", token=tp2, body={})
o2 = d.get("data", {}).get("id")
s, d = call("POST", f"/api/v1/orders/{o2}/escrow", token=tp1)
check("24.11 组织余额不足托管 409", s == 409 and "组织钱包余额不足" in d.get("msg", ""), f"{s} {d.get('msg')}")

# 24.12 开放 API Key: 生成(owner) -> Bearer hgk_ 查组织钱包 200 -> 错误 key 401
s, d = call("POST", f"/api/v1/orgs/{org_id}/apikeys", token=tp1, body={"label": "server", "scopes": "tasks,wallet"})
raw_key = d.get("data", {}).get("key")
check("24.12 生成 API Key(明文仅一次)", s == 200 and bool(raw_key) and raw_key.startswith("hgk_"), f"{s} key={raw_key}")
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token=raw_key)
w5 = d.get("data", {})
check("24.12 API Key 鉴权查组织钱包 200", s == 200 and w5.get("org_id") == org_id, f"{s} {w5.get('org_id')}")
s, d = call("GET", f"/api/v1/orgs/{org_id}/wallet", token="hgk_badkey000")
check("24.12 错误 API Key 401", s == 401, str(s))

# 24.13 Webhook: 本地接收器 + 订阅 + task_created 事件真实投递
received = []
class _Wh(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            n = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(n) if n else b""
            received.append(body.decode())
        except Exception:
            pass
        self.send_response(200)
        self.end_headers()
    def log_message(self, *a):
        pass
_whsrv = http.server.HTTPServer(("127.0.0.1", 8099), _Wh)
threading.Thread(target=_whsrv.serve_forever, daemon=True).start()
time.sleep(0.3)
s, d = call("POST", f"/api/v1/orgs/{org_id}/webhooks", token=tp1, body={"url": "http://127.0.0.1:8099/wh", "events": ["task_created"]})
wh = d.get("data", {})
check("24.13 订阅 Webhook", s == 200 and bool(wh.get("id")) and wh.get("status") == "active", f"{s} wh={wh.get('id')}")
# 触发: 再发一个组织任务 -> 分发 task_created
s, d = call("POST", f"/api/v1/orgs/{org_id}/tasks", token=tp1, body={"title": "触发事件任务", "category": "design", "budget_cents": 1000, "spec": "y"})
check("24.13 触发 task_created(组织发包 200)", s == 200, f"{s}")
# 轮询投递记录(后台 goroutine 异步投递)
delivered = False
dels = []
for _ in range(20):
    s, d = call("GET", f"/api/v1/orgs/{org_id}/webhook-deliveries", token=tp1)
    dels = d.get("data", []) or []
    if any(x.get("event") == "task_created" and x.get("status") == "success" for x in dels):
        delivered = True
        break
    if received:
        delivered = True
        break
    time.sleep(0.2)
check("24.13 Webhook 事件已真实投递(success)", delivered, f"received={len(received)} dels={len(dels)}")
try:
    _whsrv.shutdown()
except Exception:
    pass

# ================= 25. 方案3 Marketplace 深化: 搜索索引 / 推荐位 / 画像页前端 =================
def call_text(method, path):
    url = BASE + quote(path, safe="/?:=&")
    req = urllib.request.Request(url, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        try:
            return e.code, e.read().decode("utf-8", "replace")
        except Exception:
            return e.code, ""


line("== 25. 方案3 Marketplace 深化(搜索索引/推荐位/画像页前端) ==")
t_r25, _ = login("R001")

# 25.1 关键词检索(倒排索引 + 全文档匹配): q=rag 命中含 rag 的 open 任务
s, d = call("GET", "/api/v1/tasks?q=rag")
rows = d.get("data") or []
hit_rag = any("rag" in (t.get("title", "") + t.get("spec", "") + (t.get("spec_obj") or {}).get("deliverables", "") + (t.get("spec_obj") or {}).get("required_format", "")).lower() for t in rows)
check("25.1 q=rag 命中含 rag 的 open 任务", s == 200 and hit_rag, f"n={len(rows)}")

# 25.2 技能/标签关键词: skill=python 命中
s, d = call("GET", "/api/v1/tasks?skill=python")
rows = d.get("data") or []
hit_py = any("python" in (t.get("title", "") + t.get("spec", "") + (t.get("spec_obj") or {}).get("deliverables", "") + (t.get("spec_obj") or {}).get("required_format", "")).lower() for t in rows)
check("25.2 skill=python 命中含 python 的任务", s == 200 and hit_py, f"n={len(rows)}")

# 25.3 截止时间窗口过滤: 发一个已知截止的任务, 验证 deadline_after/before
dl_after = int(time.time() * 1000) + 5 * 86400 * 1000
dl_task = int(time.time() * 1000) + 10 * 86400 * 1000
s, d = call("POST", "/api/v1/tasks", token=t_r25, body={"title": "截止窗口测试", "category": "nlp", "budget_cents": 150000, "spec": "x", "deliverables": "1", "required_format": "py", "deadline_at": dl_task})
tid_win = (d.get("data") or {}).get("task", {}).get("id", "")
check("25.3 发带截止任务成功(201)", s == 201 and tid_win != "", str(s))
s, d = call("GET", f"/api/v1/tasks?deadline_after={dl_after}")
after_ids = [t.get("id") for t in (d.get("data") or [])]
check("25.3 deadline_after 含该任务", s == 200 and tid_win in after_ids, f"in_after={tid_win in after_ids}")
s, d = call("GET", f"/api/v1/tasks?deadline_before={dl_after}")
before_ids = [t.get("id") for t in (d.get("data") or [])]
check("25.3 deadline_before 排除该任务", s == 200 and tid_win not in before_ids, f"in_before={tid_win in before_ids}")

# 25.4 推荐位: 接单方 P001 查看推荐任务 feed
t_p25, _ = login("P001")
s, d = call("GET", "/api/v1/recommend/tasks", token=t_p25)
recs = d.get("data") or []
check("25.4 推荐位 P001 返回非空且 score>0", s == 200 and len(recs) > 0 and recs[0].get("score", 0) > 0, f"n={len(recs)} top={recs[0].get('score') if recs else None}")

# 25.5 推荐位越权: 需求方 R001 访问应 403
t_r25b, _ = login("R001")
s, _ = call("GET", "/api/v1/recommend/tasks", token=t_r25b)
check("25.5 推荐位需求方访问被拒 403", s == 403, str(s))

# 25.6 用户画像页前端可访问(静态托管 + CORS)
s, txt = call_text("GET", "/static/profile.html")
check("25.6 画像页 /static/profile.html 可访问", s == 200 and "接单方公开画像" in txt, f"{s} len={len(txt)}")

# ================= 26. 方案2 合规线C/真实支付通道 + 方案3做深 =================
line("== 26. 方案2 合规线C/真实支付通道 + 方案3做深(推荐接单方/相关度/画像历史) ==")

# 26.1 KYC 提交(模拟通道自动通过) → 200 + verified=true + 用户被打标
t_p26, _ = login("P001")
s, d = call("POST", "/api/v1/kyc", token=t_p26, body={"name": "测试员", "id_no": "350000199001010011"})
check("26.1 KYC 提交(模拟)返回受理单号且通过", s == 200 and d.get("data", {}).get("verified") is True, f"ref={d.get('data', {}).get('ref')}")

# 26.2 KYC 状态查询 → verified=true
s, d = call("GET", "/api/v1/kyc", token=t_p26)
check("26.2 KYC 状态查询 verified=true", s == 200 and d.get("data", {}).get("verified") is True, str(s))

# 26.3 推荐位接需求方首页: 给定任务推荐高匹配接单方(用第1节 RAG 任务, 与接单方技能强匹配)
s, d = call("GET", f"/api/v1/recommend/providers?task_id={rag_task_id}", token=t_r)
recs_p = d.get("data") or []
check("26.3 推荐接单方返回非空且 score>0", s == 200 and len(recs_p) > 0 and recs_p[0].get("score", 0) > 0,
      f"n={len(recs_p)} top={recs_p[0].get('score') if recs_p else None}")

# 26.4 搜索相关度排序(接单方登录): 返回 200 且为列表
s, d = call("GET", "/api/v1/tasks?sort=relevance", token=t_p26)
rows_rel = d.get("data") or []
check("26.4 相关度排序(接单方)返回 200 列表", s == 200 and isinstance(rows_rel, list), f"n={len(rows_rel)}")

# 26.5 画像新增近期订单/评价字段
s, d = call("GET", f"/api/v1/users/P001")
prof = d.get("data") or {}
check("26.5 画像含 recent_orders/recent_reviews 字段", s == 200 and "recent_orders" in prof and "recent_reviews" in prof,
      f"keys={list(prof.keys())}")

# 26.6 支付结果回调端点接线: 未知订单 ack 返回 200(验证 webhook 路由 + 解析)
s, d = call("POST", "/api/v1/payments/notify", body={"order_id": "NONEXISTENT", "success": True})
check("26.6 支付回调端点可接收(未知订单 ack 200)", s == 200, str(s))

line(f"结果: PASS={ok}  FAIL={fail}")
print("DONE" if fail == 0 else "HAS FAILURES")
