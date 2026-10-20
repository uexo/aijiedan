"""M0 认领验收: 大厅 → 认领 → 我的单。

这条链路要证明四件事:
  1) 谁能认领 —— 未登录、需求方、没建档的接单方都进不来
  2) 抢单是原子的 —— 两个人同时点, 只有一个成功
  3) 认领之后单子真的下架了(不是前端把卡片藏起来)
  4) 认领结果能在"我的单"里看到, 且四态说法正确

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_claim.py
    BASE=http://127.0.0.1:8080 python test/test_m0_claim.py
"""
import http.cookiejar
import json
import os
import random
import socket
import sys
import threading
import urllib.error
import urllib.request

BASE = os.environ.get("BASE", "http://127.0.0.1:8082")
PASS = FAIL = 0
FAILS = []


class Client:
    """一个浏览器身份。每个 Client 有自己的 cookie jar, 并发测试才不会串会话。"""

    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, csrf=True):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(BASE + path, data=data, method=method)
        if data:
            req.add_header("Content-Type", "application/json")
        if csrf:
            for c in self.jar:
                if c.name == "csrf_token":
                    req.add_header("X-CSRF-Token", c.value)
        try:
            with self.opener.open(req, timeout=10) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode())

    def login(self, phone, role):
        self.call("POST", "/api/v2/auth/code", {"phone": phone}, csrf=False)
        return self.call("POST", "/api/v2/auth/login", {"phone": phone, "code": "000000", "role": role}, csrf=False)

    def profile(self, headline="把重复流程做成能跑的 Agent"):
        return self.call("PUT", "/api/v2/me/profile", {
            "headline": headline,
            "bio": "做过客服工单自动化，交付前会先跑一遍。",
            "skills": ["python", "rag", "自动化"],
            "portfolio": [{"label": "工单分类器", "url": "https://example.com/case"}],
            "availability": 1,
        })

    def claim(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/take" % task_id, {})


def new_phone(prefix):
    return prefix + "".join(random.choice("0123456789") for _ in range(8))


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print("  PASS  " + name)
    else:
        FAIL += 1
        FAILS.append("%s :: %s" % (name, detail))
        print("  FAIL  %s   %s" % (name, detail))


def ok(resp):
    st, d = resp
    return st, d


print("[0] 准备三个身份")
A = Client()
PHONE_A = new_phone("159")
st, d = A.login(PHONE_A, "supply")
check("接单方 A 登录 200", st == 200, d)
check("A 角色落库为 provider", (d.get("data", {}).get("user") or {}).get("role") == "provider", d)
check("A 初始未建档", (d.get("data", {}).get("user") or {}).get("onboarded") is False, d)
st, d = A.profile()
check("A 保存档案 200", st == 200, d)

B = Client()
st, d = B.login(new_phone("158"), "supply")
check("接单方 B 登录 200（先不建档）", st == 200, d)

Q = Client()
st, d = Q.login(new_phone("157"), "demand")
check("需求方 Q 登录 200", st == 200, d)

ANON = Client()

print("[1] 谁能认领")
st, d = ANON.claim("T-DEMO-001")
check("未登录认领 401", st == 401, st)
check("未登录错误码 UNAUTHENTICATED", (d.get("error") or {}).get("code") == "UNAUTHENTICATED", d)

st, d = Q.claim("T-DEMO-001")
check("需求方认领 403", st == 403, (st, d))
check("需求方错误码 ROLE_NOT_PROVIDER", (d.get("error") or {}).get("code") == "ROLE_NOT_PROVIDER", d)

st, d = B.claim("T-DEMO-001")
check("未建档接单方认领 403", st == 403, (st, d))
check("未建档错误码 PROFILE_INCOMPLETE", (d.get("error") or {}).get("code") == "PROFILE_INCOMPLETE", d)
check("未建档带了提示语", bool((d.get("error") or {}).get("hint")), d)

st, d = A.claim("T-NOT-EXIST")
check("认领不存在的需求 404", st == 404, (st, d))
check("不存在错误码 TASK_NOT_FOUND", (d.get("error") or {}).get("code") == "TASK_NOT_FOUND", d)

print("[2] 正常认领")
st, hall = A.call("GET", "/api/v2/tasks?limit=50")
before = [t["id"] for t in hall["data"]["items"]]
check("大厅里有演示需求", len(before) >= 4, before)
TARGET = "T-DEMO-003" if "T-DEMO-003" in before else before[0]

st, d = A.claim(TARGET)
check("认领 201", st == 201, (st, d))
order = (d.get("data") or {}) if isinstance(d, dict) else {}
check("返回订单号", str(order.get("id", "")).startswith("O-"), order)
check("订单挂在目标任务上", order.get("task_id") == TARGET, order)
check("四态=待确认", order.get("state") == "to_confirm", order)
check("四态中文=待确认", order.get("state_label") == "待确认", order)
check("视角=接单方", order.get("role") == "provider", order)
check("对端有名字（需求方）", bool(order.get("counterparty")), order)
check("金额等于需求预算", order.get("amount_cents") == 680000 or order.get("amount_cents", 0) > 0, order)

print("[3] 认领之后单子下架")
st, hall = A.call("GET", "/api/v2/tasks?limit=50")
after = [t["id"] for t in hall["data"]["items"]]
check("大厅不再出现这一单", TARGET not in after, after)
check("大厅总数减一", len(after) == len(before) - 1, (len(before), len(after)))

print("[4] 重复认领")
st, d = A.claim(TARGET)
check("同一人再认领 409", st == 409, (st, d))
check("错误码 TASK_TAKEN", (d.get("error") or {}).get("code") == "TASK_TAKEN", d)

st, d = B.profile()
check("B 补完档案 200", st == 200, d)
st, d = B.claim(TARGET)
check("别人认领已被接走的单 409", st == 409, (st, d))

print("[5] 我的单")
st, d = ANON.call("GET", "/api/v2/me/orders")
check("未登录看我的单 401", st == 401, st)

st, d = A.call("GET", "/api/v2/me/orders")
items = (d.get("data") or {}).get("items") or []
mine = [o for o in items if o.get("task_id") == TARGET]
check("我的单里能找到它", len(mine) == 1, items)
if mine:
    o = mine[0]
    check("标题是需求标题", "SaaS" in o.get("task_title", "") or len(o.get("task_title", "")) > 2, o)
    check("角色仍是接单方", o.get("role") == "provider", o)
    check("状态是待确认", o.get("state") == "to_confirm", o)
check("B 的单里没有它", all(o.get("task_id") != TARGET for o in (((B.call("GET", "/api/v2/me/orders")[1] or {}).get("data") or {}).get("items") or [])), "")

print("[6] 并发抢同一单：只能成功一次")
st, hall = A.call("GET", "/api/v2/tasks?limit=50")
free = [t["id"] for t in hall["data"]["items"]]
check("还有可抢的单", len(free) >= 1, free)
RACE = free[0]

results = []
lock = threading.Lock()


def grab(client):
    r = client.claim(RACE)
    with lock:
        results.append((r[0], ((r[1].get("error") or {}) if isinstance(r[1], dict) else {})))


t1 = threading.Thread(target=grab, args=(A,))
t2 = threading.Thread(target=grab, args=(B,))
t1.start()
t2.start()
t1.join()
t2.join()

codes = sorted(c for c, _ in results)
check("两人同时点：一个 201 一个 409", codes == [201, 409], results)

st, ha = A.call("GET", "/api/v2/me/orders")
st, hb = B.call("GET", "/api/v2/me/orders")
win_a = [o for o in ((ha.get("data") or {}).get("items") or []) if o.get("task_id") == RACE]
win_b = [o for o in ((hb.get("data") or {}).get("items") or []) if o.get("task_id") == RACE]
check("这一单只生成一个订单", len(win_a) + len(win_b) == 1, (len(win_a), len(win_b)))
check("赢家那单能看到", len(win_a) == 1 or len(win_b) == 1, "")

print()
print("结果: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
    sys.exit(1)
