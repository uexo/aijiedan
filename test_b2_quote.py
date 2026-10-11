#!/usr/bin/env python3
"""B2 端到端验证: 接单方自动报价 → 采纳报价接单 → 落库校验。
重点: 确认报价放大 bug 已修复(锚定价落在 ¥100~¥5000 合理区间, 而非 ¥156870)。
"""
import json
import time
import urllib.request
import urllib.error

BASE = "http://127.0.0.1:8080"


def call(method, path, token=None, body=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode())
        except Exception:
            return e.code, {}


def login(uid):
    _, d = call("POST", "/api/v1/auth/login", body={"uid": uid})
    return d["data"]["token"]


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


print("=" * 60)
print("B2 端到端: 自动报价 → 采纳接单 → 落库")
print("=" * 60)

# 0. 登录
t_a = login("A001")              # 运营: 确保税源开启(接单闸门)
t_r = login("R001")              # 需求方
t_p = login("P001")              # 接单方
# 确保合规闸门(税源)开启, 否则接单被拦
call("POST", "/api/v1/compliance", token=t_a, body={"item": "tax_source", "on": True})

# 1. R001 发一个结构化 open 任务(frontend, 预算 ¥3000, 3 天截止)
dl = int(time.time() * 1000) + 3 * 24 * 3600 * 1000
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "B2报价验证单: React 后台页面",
    "category": "frontend",
    "budget_cents": 300000,
    "spec": "需要 react + typescript 写一个数据看板页面, 含图表组件",
    "deliverables": "1 个 React 页面 + 组件",
    "required_format": "TSX 源码",
    "deadline_at": dl,
})
check("发单成功(201)", s == 201, str(s))
task_id = d["data"]["task"]["id"]
print(f"  task_id = {task_id}")

# 2. P001 查询 AI 建议报价
s, d = call("POST", "/api/v1/quote", token=t_p, body={"task_id": task_id})
check("报价接口 200", s == 200, str(s))
q = d.get("data", {})
anchor = q.get("anchor_cents", 0)
minc = q.get("min_cents", 0)
maxc = q.get("max_cents", 0)
print(f"  报价原始返回: min={minc} anchor={anchor} max={maxc} below_estimate={q.get('below_estimate')}")
print(f"  锚定 ¥{anchor/100:.0f}  区间 ¥{minc/100:.0f} – ¥{maxc/100:.0f}")
print(f"  理由: {q.get('rationale')}")

# 3. 断言: 报价未放大(锚定 ≤ ¥5000, 即 ≤ 500000 分), 且 min<=anchor<=max
check("报价未放大(锚定 ≤ ¥5000)", anchor > 0 and anchor <= 500000, f"anchor_cents={anchor}")
check("区间合理(min<=anchor<=max)", minc <= anchor <= maxc, f"{minc}<={anchor}<={maxc}")
check("理由非空", len(q.get("rationale", [])) > 0, "")
check("货币=CNY", q.get("currency") == "CNY", str(q.get("currency")))

# 4. P001 采纳报价主动接单(传 provider_quote_cents)
s, d = call("POST", f"/api/v1/tasks/{task_id}/take", token=t_p, body={"provider_quote_cents": anchor})
check("采纳报价接单 201", s in (200, 201), str(s))
order_id = (d.get("data") or {}).get("id", "")
print(f"  order_id = {order_id}")
check("订单已生成", bool(order_id), order_id)

# 5. 落库校验: 查订单详情, 确认 provider_quote_cents 已持久化
s, d = call("GET", f"/api/v1/orders/{order_id}")
order = (d.get("data") or {}).get("order", {}) or d.get("data", {})
pqc = order.get("provider_quote_cents", None)
print(f"  订单落库 provider_quote_cents = {pqc} (期望 {anchor})")
check("报价落库正确", pqc == anchor, f"db={pqc} sent={anchor}")

print("=" * 60)
print(f"B2 结果: PASS={ok}  FAIL={fail}")
print("B2_OK" if fail == 0 else "B2_FAIL")
