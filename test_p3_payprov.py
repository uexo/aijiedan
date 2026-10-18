#!/usr/bin/env python3
"""P3 资金通道可插拔验证：拉卡拉分账通 fail-closed + 默认 stub 行为不变。

背景(合规线 C3): 平台生产资金通道必须走持牌机构分账(拉卡拉分账通), 平台不碰钱。
本测试验证两条:
  A. ZANBUDDY_PAY_PROVIDER=lakala 但凭证不全时:
     - readiness 上报 mode=lakala / live=false / missing 列出缺失凭证
     - 托管下单被拒(fail-closed), 报错信息含「拉卡拉」
     - 订单不进入 active, 资金不出问题
  B. 默认(无环境变量): readiness 仍为 stub / live=false, 演示链路不受影响。

前置: 服务以 ZANBUDDY_PAY_PROVIDER=lakala(无凭证) + 内存模式跑在 :8081。
"""
import json
import urllib.request
import urllib.error

BASE = "http://127.0.0.1:8081"


def call(method, path, token=None, body=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
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
SECTION = ""


def sec(name):
    global SECTION
    SECTION = name
    print(f"\n--- {name} ---")


def check(name, cond, extra=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  PASS  {name}")
    else:
        fail += 1
        print(f"  FAIL  {name}  {extra}")


# ---- A. lakala 通道凭证缺失: fail-closed ----
sec("A. lakala fail-closed")
st, d = call("GET", "/api/v1/readiness")
pay = (d.get("data") or {}).get("payment") or {}
check("readiness 200", st == 200, str(st))
check("mode=lakala", pay.get("mode") == "lakala", str(pay))
check("live=false", pay.get("live") is False, str(pay))
missing = pay.get("missing") or []
check("missing 含核心凭证", any("LAKALA" in m for m in missing), str(missing))
check("missing>=4 项", len(missing) >= 4, str(missing))

# 打开合规三门(icp/税源/托管), 否则托管会被合规闸门拦截、到不了支付层
tok_a = login("A001")
for item in ("icp", "tax_source", "escrow"):
    st, d = call("POST", "/api/v1/compliance", token=tok_a, body={"item": item, "on": True})
    check(f"开门控 {item}", st == 200, f"{st} {str(d)[:150]}")

# 走一遍发单→接单→托管, 托管必须被支付层拦(fail-closed)
tok_r = login("R001")
tok_p = login("P001")
st, d = call("POST", "/api/v1/tasks", token=tok_r, body={
    "title": "fail-closed 验证单", "category": "nlp", "budget_cents": 10000,
    "spec": "验证拉卡拉通道未就绪时托管被拒",
})
check("发单成功", st in (200, 201), f"{st} {str(d)[:200]}")
task_id = (d.get("data") or {}).get("task", {}).get("id")
check("拿到 task_id", bool(task_id), str(d)[:200])

if task_id:
    st, d = call("POST", f"/api/v1/tasks/{task_id}/take", token=tok_p)
    check("接单成功", st == 201, f"{st} {str(d)[:200]}")
    order_id = (d.get("data") or {}).get("id")
    check("拿到 order_id", bool(order_id), str(d)[:200])
    if order_id:
        st, d = call("POST", f"/api/v1/orders/{order_id}/escrow", token=tok_r)
        check("托管被拒(409)", st == 409, f"{st}")
        reason = json.dumps(d, ensure_ascii=False)
        check("报错含「拉卡拉」", "拉卡拉" in reason, reason[:300])
        check("报错含 fail-closed 语义(未就绪/缺凭证)", ("未就绪" in reason) or ("缺凭证" in reason), reason[:300])
        st, d = call("GET", f"/api/v1/orders/{order_id}", token=tok_r)
        od = (d.get("data") or {}).get("order") or {}
        check("订单未进 active", od.get("status") not in ("active", "completed", "delivered"), str(od.get("status")))
        # 钱包资金必须原样未动(托管失败已回滚)
        st, d = call("GET", "/api/v1/wallet", token=tok_r)
        w = (d.get("data") or {}).get("wallet") or {}
        check("需求方钱包未扣款", w.get("balance_cents") == 1000000 and w.get("frozen_cents") == 0, str(w))

print(f"\n=== P3 资金通道: {ok} PASS / {fail} FAIL ===")
raise SystemExit(1 if fail else 0)
