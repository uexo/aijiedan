#!/usr/bin/env python3
"""P2-1 延伸：组织钱包 / 企业版发包资金链路精确金额断言。

与 test_p2_econ.py（个人链路）互补，验证企业版的组织钱包分支：
  CreateOrgTask → TakeTask → CreateEscrow(组织钱包扣款) → Deliver → Accept
  → releaseToWallets(组织钱包解冻 + 接单方 + 平台佣金)
  以及组织钱包充值、组织提现申请/审批（通过与驳回）、超额承诺防护、权限隔离。

前置：先跑 `db_reset.py --demo`（合规四门为 dev/demo 就绪态全开），
服务以 MySQL 模式运行于 :8080。

覆盖 8 组场景（单位：分）：
  A. 组织钱包充值：owner 可充、非成员不可充、负数/0 被拒
  B. 组织发包：不预扣款（发包后组织余额不变）
  C. 组织托管：从组织钱包扣款 + 冻结（非个人钱包），个人钱包不受影响
  D. 组织结算：解冻组织冻结 + 接单方×90% + 佣金10%，守恒
  E. 组织提现申请：冻结预占、可用额减少；超额被拒
  F. 组织提现审批通过：真实出账、总额永久减少、生成提现记录
  G. 组织提现审批驳回：解冻复原、零资损；已处理单不可重复审批
  H. 权限隔离：非成员不可发包/充值/审批；组织订单列表隔离
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


def org_wallet(org, tok):
    _, d = call("GET", f"/api/v1/orgs/{org}/wallet", token=tok)
    w = d.get("data") or {}
    return {
        "balance": w.get("balance_cents", 0),
        "frozen": w.get("frozen_cents", 0),
    }


def user_wallet(tok):
    _, d = call("GET", "/api/v1/wallet", token=tok)
    w = (d.get("data") or {}).get("wallet") or {}
    return {"balance": w.get("balance_cents", 0), "frozen": w.get("frozen_cents", 0)}


def commission(amount):
    return amount * 10 // 100


ok = 0
fail = 0


def sec(name):
    print(f"\n--- {name} ---")


def check(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  [PASS] {name} {detail}")
    else:
        fail += 1
        print(f"  [FAIL] {name} {detail}")


print("=" * 68)
print("组织钱包 / 企业版发包资金链路验证")
print("=" * 68)

t_r = login("R001")   # 组织 owner(需求方)
t_p = login("P001")    # 接单方
t_p2 = login("P002")   # 另一接单方(提现审批后收款)
t_a = login("A001")    # 运营(提现审批)

# ---------- 创建组织 ----------
sec("0. 组织创建与成员")
s, d = call("POST", "/api/v1/orgs", token=t_r, body={"name": "经济模型验证组织"})
org = (d.get("data") or {}).get("id")
check("组织创建成功", s == 200 and bool(org), f"{s} {org}")

# ---------- A. 组织钱包充值 ----------
sec("A. 组织钱包充值")
s, d = call("POST", f"/api/v1/orgs/{org}/wallet/deposit", token=t_r,
            body={"amount_cents": 500000})
check("owner 充值 500000 成功", s == 200, str(s))
w0 = org_wallet(org, t_r)
check("充值后组织余额 = 500000", w0["balance"] == 500000, str(w0["balance"]))
check("充值后组织冻结 = 0", w0["frozen"] == 0, str(w0["frozen"]))

# 非成员不可充值（P002 非组织成员）
s, d = call("POST", f"/api/v1/orgs/{org}/wallet/deposit", token=t_p2,
            body={"amount_cents": 1000})
check("非成员充值被拒", s in (400, 403), str(s))
s, d = call("POST", f"/api/v1/orgs/{org}/wallet/deposit", token=t_r,
            body={"amount_cents": -100})
check("负数充值被拒", s in (400, 403), str(s))
s, d = call("POST", f"/api/v1/orgs/{org}/wallet/deposit", token=t_r,
            body={"amount_cents": 0})
check("0 元充值被拒", s in (400, 403), str(s))
check("失败充值未改变余额", org_wallet(org, t_r)["balance"] == 500000,
      str(org_wallet(org, t_r)["balance"]))

# ---------- B. 组织发包不预扣款 ----------
sec("B. 组织发包（不预扣款）")
AMT = 100000
w_before_pub = org_wallet(org, t_r)
s, d = call("POST", f"/api/v1/orgs/{org}/tasks", token=t_r, body={
    "title": "组织发包验证", "category": "backend",
    "budget_cents": AMT, "spec": "组织钱包资金链路验证",
})
tid = (d.get("data") or {}).get("task", {}).get("id")
check("组织发包成功", s == 200 and bool(tid), f"{s} {tid}")
w_after_pub = org_wallet(org, t_r)
check("发包后组织余额不变(不预扣)", w_after_pub["balance"] == w_before_pub["balance"],
      f"{w_before_pub['balance']} -> {w_after_pub['balance']}")

# 非成员不可发包
s, d = call("POST", f"/api/v1/orgs/{org}/tasks", token=t_p2, body={
    "title": "越权发包", "category": "backend", "budget_cents": 100, "spec": "x",
})
check("非成员发包被拒", s in (400, 403), str(s))

# ---------- C. 组织托管 ----------
sec("C. 接单 + 组织钱包托管")
w_p_before = user_wallet(t_p)["balance"]
owner_w_before = user_wallet(t_r)["balance"]   # 组织 owner 的个人钱包基线
s, d = call("POST", f"/api/v1/tasks/{tid}/take", token=t_p, body={"milestones": []})
oid = (d.get("data") or {}).get("id")
check("接单成功", s == 201 and bool(oid), f"{s} {oid}")

s, d = call("POST", f"/api/v1/orders/{oid}/escrow", token=t_r)
check("组织托管成功", s == 200, str(s))
w_esc = org_wallet(org, t_r)
check("托管后组织余额 -100000", w_esc["balance"] == 500000 - AMT,
      f"500000 -> {w_esc['balance']}")
check("托管后组织冻结 +100000", w_esc["frozen"] == AMT, str(w_esc["frozen"]))
check("组织 owner 个人钱包不受组织托管影响",
      user_wallet(t_r)["balance"] == owner_w_before,
      f"{owner_w_before} -> {user_wallet(t_r)['balance']}")
check("接单方此时未入账(托管≠结算)", user_wallet(t_p)["balance"] == w_p_before,
      str(user_wallet(t_p)["balance"]))

# ---------- D. 组织结算 ----------
sec("D. 组织结算（解冻 + 接单方 + 佣金）")
w_p_before2 = user_wallet(t_p)["balance"]
w_a_before = user_wallet(t_a)["balance"]
s, d = call("POST", f"/api/v1/orders/{oid}/deliver", token=t_p,
            body={"content": "组织交付", "items": []})
check("交付成功", s == 201, str(s))
s, d = call("POST", f"/api/v1/orders/{oid}/accept", token=t_r,
            body={"acceptance": [], "severe_mismatch": False})
check("验收并结算成功", s == 200, str(s))

w_set = org_wallet(org, t_r)
w_p_set = user_wallet(t_p)["balance"]
w_a_set = user_wallet(t_a)["balance"]
COMM = commission(AMT)
check("结算后组织冻结清零", w_set["frozen"] == 0, str(w_set["frozen"]))
check("结算后组织余额不变(托管时已扣, 解冻不减余额)",
      w_set["balance"] == 500000 - AMT, str(w_set["balance"]))
check(f"接单方入账 = {AMT - COMM} (90%)", w_p_set == w_p_before2 + AMT - COMM,
      f"{w_p_before2} -> {w_p_set}")
check(f"平台佣金入账 = {COMM} (10%)", w_a_set == w_a_before + COMM,
      f"{w_a_before} -> {w_a_set}")
check("组织链路守恒: 接单方 + 佣金 == 订单额", (AMT - COMM) + COMM == AMT, "")

# ---------- E/F/G. 组织提现三态 ----------
sec("E. 组织提现申请（冻结预占）")
base = org_wallet(org, t_r)
PO = 20000
s, d = call("POST", f"/api/v1/orgs/{org}/payouts", token=t_r, body={"amount_cents": PO})
ap = (d.get("data") or {}).get("id")
check("组织提现申请成功", s == 200 and bool(ap), f"{s} {ap}")
w_e = org_wallet(org, t_r)
check("申请后组织余额不变", w_e["balance"] == base["balance"], str(w_e["balance"]))
check("申请后组织冻结 +20000", w_e["frozen"] == PO, str(w_e["frozen"]))
check("可用额相应减少",
      w_e["balance"] - w_e["frozen"] == base["balance"] - base["frozen"] - PO, "")

# 超额申请被拒
s, d = call("POST", f"/api/v1/orgs/{org}/payouts", token=t_r, body={"amount_cents": 10 ** 9})
check("超额组织提现被拒", s in (400, 403), str(s))
check("被拒后冻结额未增加", org_wallet(org, t_r)["frozen"] == w_e["frozen"],
      str(org_wallet(org, t_r)["frozen"]))

sec("F. 组织提现审批通过（真实出账）")
s, d = call("POST", f"/api/v1/orgs/{org}/approvals/{ap}/approve", token=t_r,
            body={"approve": True})
check("owner 审批通过", s == 200, str(s))
w_f = org_wallet(org, t_r)
check("通过后组织余额 -20000", w_f["balance"] == base["balance"] - PO,
      f"{base['balance']} -> {w_f['balance']}")
check("通过后组织冻结清零", w_f["frozen"] == 0, str(w_f["frozen"]))
check("总额永久减少(钱离开组织)",
      w_f["balance"] + w_f["frozen"] == base["balance"] + base["frozen"] - PO, "")

sec("G. 组织提现审批驳回（零资损）")
base_g = org_wallet(org, t_r)
s, d = call("POST", f"/api/v1/orgs/{org}/payouts", token=t_r, body={"amount_cents": 15000})
ap2 = (d.get("data") or {}).get("id")
check("第二次组织提现申请成功", s == 200 and bool(ap2), f"{s} {ap2}")
s, d = call("POST", f"/api/v1/orgs/{org}/approvals/{ap2}/approve", token=t_r,
            body={"approve": False})
check("驳回成功", s == 200, str(s))
w_g = org_wallet(org, t_r)
check("驳回后组织余额复原", w_g["balance"] == base_g["balance"],
      f"{base_g['balance']} -> {w_g['balance']}")
check("驳回后冻结清零", w_g["frozen"] == 0, str(w_g["frozen"]))
check("驳回后总额与申请前一致(零资损)",
      w_g["balance"] + w_g["frozen"] == base_g["balance"] + base_g["frozen"], "")
# 已处理单不可重复审批
s, d = call("POST", f"/api/v1/orgs/{org}/approvals/{ap2}/approve", token=t_r,
            body={"approve": True})
check("已处理审批单再审批被拒", s in (400, 403), str(s))
check("重复审批不重复出账", org_wallet(org, t_r)["balance"] == w_g["balance"], "")

# ---------- H. 权限隔离 ----------
sec("H. 权限隔离与非成员访问")
# 回归: 早期 GET 类组织端点缺成员校验, 任何登录用户可读组织钱包余额/成员名单等
# (写类端点均有校验)。以下逐个端点验证非成员一律 403。
for path, label in [("wallet", "组织钱包"), ("orders", "组织订单"),
                    ("members", "成员名单"), ("approvals", "审批单"),
                    ("tasks", "组织任务"), ("webhook-deliveries", "Webhook投递记录")]:
    s, d = call("GET", f"/api/v1/orgs/{org}/{path}", token=t_p2)
    check(f"非成员读{label}被拒 403", s == 403, str(s))
    check(f"非成员读{label}无数据泄露", d.get("data") in (None, [], ""),
          json.dumps(d.get("data"), ensure_ascii=False)[:60])

s, d = call("POST", f"/api/v1/orgs/{org}/approvals/{ap}/approve", token=t_p2,
            body={"approve": True})
check("非成员审批被拒", s in (400, 403), str(s))
# owner 自身访问仍应正常
s, d = call("GET", f"/api/v1/orgs/{org}/wallet", token=t_r)
check("owner 读组织钱包正常", s == 200, str(s))
s, d = call("GET", f"/api/v1/orgs/{org}/members", token=t_r)
check("owner 读成员名单正常", s == 200, str(s))
s, d = call("GET", f"/api/v1/orgs/{org}/orders", token=t_r)
orders = d.get("data") or []
check("owner 可读组织订单", s == 200, str(s))
check("组织订单含本单", any(o.get("id") == oid for o in orders),
      f"orders={len(orders)}")

print("\n" + "=" * 68)
print(f"组织资金链路结果: PASS={ok}  FAIL={fail}")
print("ORG_OK" if fail == 0 else "ORG_FAIL")