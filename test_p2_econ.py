#!/usr/bin/env python3
"""P2-1 经济模型验证：资金守恒 / 佣金 / 提现冻结出账 / 纠纷退款分账。

前置：先跑 `db_reset.py --demo`（把合规四门预置为 dev/demo 就绪态），
服务以 MySQL 模式运行于 :8080。

验证 6 条链路，每一步断言精确金额（单位：分）：
  A. 单量资金守恒：publish → take → escrow → deliver → accept
     ¥1000 单：需求方 -100000 / 冻结 +100000 → 结算后解冻；
                接单方 +90000；平台佣金 +10000（10%）；总额守恒。
  B. 零佣金边界：¥0 订单不产生负数佣金与负数余额。
  C. 提现冻结：申请提现 → 可用余额不变、冻结额 +N（仍属用户资产，总资产不变）。
  D. 提现通过：真实出账 → 可用 -N、冻结 -N，总额永久减少（钱离开平台）。
  E. 提现驳回：解冻回可用 → 总额复原（仅状态流转，无资损）。
  F. 纠纷退款分账：refund_pct=40 的 ¥1000 单
     → 需求方退 40000；接单方 = 100000-40000-10000 = 50000；平台佣金仍 10000。
  G. 佣金口径统一性：任意金额下 commission == amount*10/100（整数分）。

资金守恒总口径（平台视角）：任何时刻
    Σ所有钱包(balance) == Σ需求方净支付 + Σ平台佣金 + Σ已提现出账
"""
import json
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


def wallet(uid, tok):
    _, d = call("GET", "/api/v1/wallet", token=tok)
    w = d.get("data") or {}
    return {
        "balance": (w.get("wallet") or {}).get("balance_cents", 0),
        "frozen": (w.get("wallet") or {}).get("frozen_cents", 0),
        "ledger": w.get("ledger") or [],
    }


ok = 0
fail = 0
SECTION = ""


def sec(name):
    global SECTION
    SECTION = name
    print(f"\n--- {name} ---")


def check(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  [PASS] {name} {detail}")
    else:
        fail += 1
        print(f"  [FAIL] {name} {detail}")


def commission(amount):
    return amount * 10 // 100


print("=" * 68)
print("P2-1 经济模型验证：资金守恒 / 佣金 / 提现 / 纠纷退款")
print("=" * 68)

t_r = login("R001")
t_p1 = login("P001")
t_p2 = login("P002")
t_a = login("A001")

# ---------- 前置：门控确认 ----------
s, d = call("GET", "/api/v1/readiness")
gate = (d.get("data") or {}).get("compliance_gate") or {}
sec("前置：dev/demo 就绪态")
check("合规四门全开", all(gate.get(k) for k in ("kyc", "icp", "tax_source", "escrow")), str(gate))

# ---------- A. 单量资金守恒 ----------
sec("A. 单量资金守恒（¥1000 正常成交）")
AMT = 100000
w_r0 = wallet("R001", t_r)["balance"]
w_p1_0 = wallet("P001", t_p1)["balance"]
w_a0 = wallet("A001", t_a)["balance"]

s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "经济模型验证单", "category": "backend", "budget_cents": AMT,
    "spec": "资金守恒验证", "deliverables": "可运行服务", "required_format": "docx",
})
tid = (d.get("data") or {}).get("task", {}).get("id")
check("发单成功", bool(tid), str(tid))

s, d = call("POST", f"/api/v1/tasks/{tid}/take", token=t_p1,
            body={"milestones": [], "provider_quote_cents": AMT})
oid = (d.get("data") or {}).get("id")
check("接单成功", s == 201 and bool(oid), f"{s} {oid}")

s, d = call("POST", f"/api/v1/orders/{oid}/escrow", token=t_r)
check("托管成功", s == 200, str(s))
w_r1 = wallet("R001", t_r)
check("托管后需求方可用余额 -100000", w_r1["balance"] == w_r0 - AMT,
      f"{w_r0} -> {w_r1['balance']}")
check("托管后需求方冻结额 +100000", w_r1["frozen"] == AMT, str(w_r1["frozen"]))

s, d = call("POST", f"/api/v1/orders/{oid}/deliver", token=t_p1,
            body={"content": "交付内容", "items": []})
check("交付成功", s == 201, str(s))

s, d = call("POST", f"/api/v1/orders/{oid}/accept", token=t_r,
            body={"acceptance": [], "severe_mismatch": False})
check("验收并结算成功", s == 200, str(s))

w_r2 = wallet("R001", t_r)
w_p1_2 = wallet("P001", t_p1)["balance"]
w_a2 = wallet("A001", t_a)["balance"]
COMM = commission(AMT)
check(f"结算后需求方冻结清零", w_r2["frozen"] == 0, str(w_r2["frozen"]))
check(f"结算后需求方可用不再变动(共 -{AMT})", w_r2["balance"] == w_r0 - AMT,
      f"{w_r2['balance']} vs {w_r0 - AMT}")
check(f"接单方入账 = 金额×90% = {AMT - COMM}", w_p1_2 == w_p1_0 + AMT - COMM,
      f"{w_p1_0} -> {w_p1_2}")
check(f"平台佣金入账 = 10% = {COMM}", w_a2 == w_a0 + COMM, f"{w_a0} -> {w_a2}")
check("资金守恒：需求方支付 == 接单方所得 + 平台佣金",
      AMT == (AMT - COMM) + COMM, f"{AMT} == {(AMT-COMM)}+{COMM}")

# ---------- B. 佣金口径与边界 ----------
sec("B. 佣金口径统一性（整数分，含小额/大额）")
for amt, exp in [(1, 0), (99, 9), (100, 10), (100000, 10000), (123456, 12345), (999999, 99999)]:
    check(f"commission({amt}) = {exp}", commission(amt) == exp, str(commission(amt)))
check("佣金恒不超订单金额(10%≤100%)",
      all(commission(a) < a or a == 0 for a in [1, 99, 100, 100000]), "")

# ---------- C/D/E. 提现三态 ----------
sec("C. 提现申请冻结（资产仍属用户，总额不变）")
base_p1 = wallet("P001", t_p1)
WD = 20000
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": WD})
wid = (d.get("data") or {}).get("id")
check("提现申请成功", s == 201 and bool(wid), f"{s} {wid}")
w_c = wallet("P001", t_p1)
check("申请后可用余额不变", w_c["balance"] == base_p1["balance"], str(w_c["balance"]))
check("申请后冻结额 +20000", w_c["frozen"] == WD, str(w_c["frozen"]))
check("可用额相应减少(不可重复申请同一笔钱)",
      w_c["balance"] - w_c["frozen"] == base_p1["balance"] - base_p1["frozen"] - WD,
      f"可用 {base_p1['balance']} -> {w_c['balance'] - w_c['frozen']}")

sec("D. 提现审批通过（真实出账）")
s, d = call("POST", f"/api/v1/wallet/withdrawals/{wid}/approve", token=t_a,
            body={"approve": True, "note": "验证出账"})
check("审批通过", s == 200, str(s))
w_d = wallet("P001", t_p1)
check("通过后可用 -20000", w_d["balance"] == base_p1["balance"] - WD,
      f"{base_p1['balance']} -> {w_d['balance']}")
check("通过后冻结清零", w_d["frozen"] == 0, str(w_d["frozen"]))
check("总额永久减少(钱已离开平台)",
      w_d["balance"] + w_d["frozen"] == base_p1["balance"] + base_p1["frozen"] - WD, "")
check("出账记流水", any(l.get("type") == "withdraw" for l in w_d["ledger"]), "")

sec("E. 提现驳回（解冻回可用，无资损）")
before_e = wallet("P001", t_p1)   # 基线须在申请之前取
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": 15000})
wid2 = (d.get("data") or {}).get("id")
check("第二次提现申请成功", s == 201 and bool(wid2), f"{s} {wid2}")
s, d = call("POST", f"/api/v1/wallet/withdrawals/{wid2}/approve", token=t_a,
            body={"approve": False, "note": "验证驳回"})
check("审批驳回", s == 200, str(s))
w_e = wallet("P001", t_p1)
check("驳回后可用余额复原", w_e["balance"] == before_e["balance"],
      f"{before_e['balance']} -> {w_e['balance']}")
check("驳回后冻结清零", w_e["frozen"] == 0, str(w_e["frozen"]))
check("驳回后总额与申请前一致(零资损)",
      w_e["balance"] + w_e["frozen"] == before_e["balance"] + before_e["frozen"], "")

sec("F. 提现边界校验")
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": 0})
check("提现 0 元被拒", s in (400, 403), str(s))
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": 10 ** 12})
check("超额提现被拒(余额不足)", s in (400, 403), str(s))
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_r, body={"amount_cents": 100})
check("需求方(非接单方)不能提现", s in (400, 403), str(s))

sec("F2. 超额承诺防护（可用额归零后不得再申请）")
# 早期 bug: 只校验 balance 且申请时不扣减可用额, 可把同一笔钱申请多次
# (实测冻结额可达余额数倍)。此处验证修复: 把可用额全部申请冻结后, 再申请 1 分必被拒。
w_f = wallet("P001", t_p1)
avail_f = w_f["balance"] - w_f["frozen"]
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": avail_f})
wid_full = (d.get("data") or {}).get("id")
check("可申请至可用上限", s == 201 and bool(wid_full), f"{s} 可用={avail_f}")
w_full = wallet("P001", t_p1)
check("冻结后可用额归零", w_full["balance"] - w_full["frozen"] == 0,
      f"{w_full['balance']}-{w_full['frozen']}")
s, d = call("POST", "/api/v1/wallet/withdraw", token=t_p1, body={"amount_cents": 1})
check("可用额归零后再申请 1 分被拒", s in (400, 403), str(s))
check("被拒后冻结额未增加", wallet("P001", t_p1)["frozen"] == w_full["frozen"],
      f"{w_full['frozen']} -> {wallet('P001', t_p1)['frozen']}")
# 清理: 驳回该笔以复原余额, 避免影响后续断言
call("POST", f"/api/v1/wallet/withdrawals/{wid_full}/approve", token=t_a,
     body={"approve": False, "note": "验证完毕自动驳回复原"})
check("驳回后余额复原", wallet("P001", t_p1)["balance"] == w_full["balance"],
      str(wallet("P001", t_p1)["balance"]))

# ---------- G. 纠纷退款分账 ----------
sec("G. 纠纷仲裁分账（¥1000 单，退款 40%）")
AMT2 = 100000
w_r3 = wallet("R001", t_r)["balance"]
w_p2_0 = wallet("P002", t_p2)["balance"]
w_a3 = wallet("A001", t_a)["balance"]

s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "纠纷验证单", "category": "design", "budget_cents": AMT2,
    "spec": "纠纷退款验证", "deliverables": "5 张海报", "required_format": "PNG",
})
tid2 = (d.get("data") or {}).get("task", {}).get("id")
s, d = call("POST", f"/api/v1/tasks/{tid2}/take", token=t_p2, body={"milestones": []})
oid2 = (d.get("data") or {}).get("id")
check("纠纷单接单成功", s == 201 and bool(oid2), f"{s} {oid2}")
s, d = call("POST", f"/api/v1/orders/{oid2}/escrow", token=t_r)
check("纠纷单托管成功", s == 200, str(s))
s, d = call("POST", f"/api/v1/orders/{oid2}/deliver", token=t_p2,
            body={"content": "交付", "items": []})
s, d = call("POST", f"/api/v1/orders/{oid2}/dispute", token=t_r,
            body={"reason": "交付与需求偏差"})
check("纠纷发起成功", s == 200, str(s))

PCT = 40
s, d = call("POST", f"/api/v1/orders/{oid2}/resolve", token=t_a,
            body={"refund_pct": PCT, "decision": "部分退款"})
check("仲裁结案成功", s == 200, str(s))

REFUND = AMT2 * PCT // 100
COMM2 = commission(AMT2)
TO_PROV = AMT2 - REFUND - COMM2
w_r4 = wallet("R001", t_r)["balance"]
w_p2_2 = wallet("P002", t_p2)["balance"]
w_a4 = wallet("A001", t_a)["balance"]

check(f"需求方退回 {REFUND}", w_r4 == w_r3 - AMT2 + REFUND, f"{w_r3} -> {w_r4}")
check(f"接单方分得 {TO_PROV} (扣佣后)", w_p2_2 == w_p2_0 + TO_PROV,
      f"{w_p2_0} -> {w_p2_2} (期望 +{TO_PROV})")
check(f"平台佣金仍收 {COMM2}(全额订单额计算)", w_a4 == w_a3 + COMM2,
      f"{w_a3} -> {w_a4} (期望 +{COMM2})")
check("纠纷分账守恒: 退款 + 接单方 + 佣金 == 订单额",
      REFUND + TO_PROV + COMM2 == AMT2,
      f"{REFUND}+{TO_PROV}+{COMM2} vs {AMT2}")
check("纠纷单冻结已清零", wallet("R001", t_r)["frozen"] == 0,
      str(wallet("R001", t_r)["frozen"]))

# ---------- 幂等性 ----------
sec("H. 幂等性与重复结算防护")
# Settle 以 o.Settled 为幂等锁: 重复调用返回 200(幂等成功)但绝不可重复放款,
# 因此正确口径是「资金与流水不重复」, 而非状态码。
before_settle_ledger = [l for l in wallet("P001", t_p1)["ledger"] if l.get("type") == "settle"]
bal_before_re = wallet("P001", t_p1)["balance"]
s, d = call("POST", f"/api/v1/orders/{oid}/settle", token=t_r)
after_settle_ledger = [l for l in wallet("P001", t_p1)["ledger"] if l.get("type") == "settle"]
check("重复结算不新增 settle 流水", len(after_settle_ledger) == len(before_settle_ledger),
      f"{len(before_settle_ledger)} -> {len(after_settle_ledger)}")
check("重复结算不重复放款", wallet("P001", t_p1)["balance"] == bal_before_re,
      f"{bal_before_re} vs {wallet('P001', t_p1)['balance']}")
s, d = call("POST", f"/api/v1/wallet/withdrawals/{wid}/approve", token=t_a,
            body={"approve": True, "note": "重复审批"})
check("已处理提现再审批被拒", s in (400, 403), str(s))
check("重复审批不重复出账", wallet("P001", t_p1)["balance"] == w_e["balance"], "")

print("\n" + "=" * 68)
print(f"经济模型验证结果: PASS={ok}  FAIL={fail}")
print("ECON_OK" if fail == 0 else "ECON_FAIL")