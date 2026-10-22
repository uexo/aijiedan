"""M0 撤单验收: 发错了要能收回来, 而且只能收回"还没人接"的那些。

这条链路要证明五件事:
  1) 谁能撤 —— 只有发单的人。别人拿到单号也撤不掉, 而且不会被告知这单的进展
  2) 撤完真的下架 —— 大厅里消失, 别人认领不到, 但"我发的单"里还查得到
  3) 有人接了就不能撤 —— 单方面撤回会让订单悬空, 那种情况必须走订单侧退回
  4) 撤单是原子的 —— 撤回和认领同时发生, 只能有一个成立
  5) 撤回是终态 —— 重复撤、撤完再接都不成立; 状态在库里是 withdrawn 不是 closed

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_withdraw.py
    BASE=http://127.0.0.1:8082 python test/test_m0_withdraw.py
"""
import http.client
import http.cookiejar
import json
import os
import random
import time
import threading
import urllib.error
import urllib.request

BASE = os.environ.get("BASE", "http://127.0.0.1:8082")
# 直连, 不走系统代理: 本机代理会把回环请求也接过去, 偶发掐断连接
NO_PROXY = urllib.request.ProxyHandler({})
PASS = FAIL = 0
FAILS = []

# 客户端连接层抖动, 见 Client.call 的说明
CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)
# 重放写请求会拿到这些"状态已经变了"的错误码; 它们不代表业务结论。
REPLAY_CONFLICTS = {
    "TASK_TAKEN", "TASK_NOT_OPEN", "TASK_ALREADY_TAKEN", "TASK_CHANGED",
    "ORDER_NOT_PENDING", "ORDER_NOT_TO_REVIEW", "ORDER_NOT_IN_PROGRESS",
    "REVISION_LIMIT", "DELIVERY_DUPLICATE", "CODE_ALREADY_USED", "SLUG_TAKEN",
}


class Client:
    """一个浏览器身份。每个 Client 有自己的 cookie jar。"""

    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(NO_PROXY, urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, csrf=True):
        # 这台 Windows 机器上 Python 的 socket 偶发被本地中止(10053/10054),
        # 服务端日志干净、curl 与浏览器都不复现 —— 是客户端连接层抖动。
        # 三次退避: 单次抖动率不高, 但一条用例几百个请求, 只重试一次仍会假失败。
        # 写请求重放可能撞上"服务端其实已经做过了": 那些"状态已变"的 4xx 不是业务结论,
        # 此时把最初的连接错误抛出去, 而不是把 409 当答案——否则测试会静默给出错误的通过。
        first = None
        for attempt in range(3):
            try:
                st, d = self._call(method, path, body, csrf)
            except CONN_ERRORS as e:
                first = first or e
                time.sleep(0.15 * (attempt + 1))
                continue
            if first is not None and st >= 400 and ((d.get("error") or {}).get("code") in REPLAY_CONFLICTS):
                raise first
            return st, d
        raise first

    def _call(self, method, path, body, csrf):
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
                    raw = r.read()
                    if not raw:
                        raise ConnectionResetError('empty body')
                    return r.status, json.loads(raw.decode())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode())

    def login(self, phone, role):
        self.call("POST", "/api/v2/auth/code", {"phone": phone}, csrf=False)
        return self.call("POST", "/api/v2/auth/login", {"phone": phone, "code": "000000", "role": role}, csrf=False)

    def profile(self):
        return self.call("PUT", "/api/v2/me/profile", {
            "headline": "把重复流程做成能跑的 Agent",
            "bio": "做过客服工单自动化，交付前会先跑一遍。",
            "skills": ["python", "rag", "自动化"],
            "portfolio": [{"label": "工单分类器", "url": "https://example.com/case"}],
            "availability": 1,
        })

    def publish(self, **kw):
        return self.call("POST", "/api/v2/tasks", kw)

    def withdraw(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/withdraw" % task_id, {})

    def take(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/take" % task_id, {})

    def my_tasks(self):
        st, d = self.call("GET", "/api/v2/me/tasks")
        return (d.get("data") or {}).get("items") or []

    def hall(self):
        st, d = self.call("GET", "/api/v2/tasks?limit=100")
        return [t["id"] for t in (d.get("data") or {}).get("items") or []]


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


def errcode(d):
    return ((d or {}).get("error") or {}).get("code")


def data(d):
    return (d or {}).get("data") or {}


def valid(**over):
    base = {
        "title": "给茶叶店做一个抖音选品分析脚本",
        "category": "data",
        "spec": "店铺做茶叶，抖音小店每天导出订单表。想要一个脚本读表，算出哪几个 SKU 动销最快、"
                "哪些在压货，输出一张周报。跑在我自己的 Windows 机器上，不联网也行。",
        "budget_cents": 80000,
        "deliverables": "1 个 Python 脚本 + 一份使用说明",
        "items": ["能读抖音导出的 xlsx", "算出动销排名"],
    }
    base.update(over)
    return base


print("[0] 准备三个身份")

D = Client()  # 发单的人
st, d = D.login(new_phone("137"), "demand")
check("需求方 D 登录 200", st == 200, d)

X = Client()  # 另一个发单方: 撤别人的单必须失败
st, d = X.login(new_phone("136"), "demand")
check("需求方 X 登录 200", st == 200, d)

P = Client()  # 接单方
st, d = P.login(new_phone("138"), "supply")
check("接单方 P 登录 200", st == 200, d)
st, d = P.profile()
check("P 建档 200", st == 200, d)

ANON = Client()

print("[1] 谁能撤: 未登录、找不到、不是你的")

st, d = ANON.withdraw("T-DEMO-001")
check("未登录撤单 401", st == 401, (st, d))
check("未登录错误码 UNAUTHENTICATED", errcode(d) == "UNAUTHENTICATED", d)

st, d = D.withdraw("T-NOT-EXIST")
check("撤不存在的需求 404", st == 404, (st, d))
check("不存在错误码 TASK_NOT_FOUND", errcode(d) == "TASK_NOT_FOUND", d)

st, d = D.publish(**valid(title="先发一条再谈谁能撤的单"))
check("D 发单 201", st == 201, (st, d))
T1 = data(d).get("id", "")
check("新单状态 open", data(d).get("status") == "open", d)

st, d = X.withdraw(T1)
check("别人撤我的单 403", st == 403, (st, d))
check("错误码 NOT_TASK_OWNER", errcode(d) == "NOT_TASK_OWNER", d)

st, d = D.call("GET", "/api/v2/tasks?limit=100")
still_open = [t for t in (d.get("data") or {}).get("items") or [] if t["id"] == T1]
check("越权撤回之后单子仍在大厅", len(still_open) == 1, still_open)

print("[2] 正常撤回: 下架了, 但记录还在")

st, d = D.withdraw(T1)
check("发单方撤回 200", st == 200, (st, d))
check("返回状态 withdrawn", data(d).get("status") == "withdrawn", d)
check("状态标签「已撤回」", data(d).get("state_label") == "已撤回", d)
check("返回的是同一条单", data(d).get("id") == T1, d)

check("大厅里已经没有它", T1 not in D.hall(), T1)

mine = [t for t in D.my_tasks() if t.get("id") == T1]
check("我发的单里还查得到", len(mine) == 1, mine)
if mine:
    check("查回来的状态是 withdrawn", mine[0].get("status") == "withdrawn", mine[0])
    check("查回来的标签是「已撤回」", mine[0].get("state_label") == "已撤回", mine[0])

st, d = D.withdraw(T1)
check("重复撤回 409", st == 409, (st, d))
check("重复撤回错误码 TASK_NOT_OPEN", errcode(d) == "TASK_NOT_OPEN", d)

st, d = P.take(T1)
check("撤回过的不可能再被认领 409", st == 409, (st, d))

print("[3] 有人接了就不能撤")

st, d = D.publish(**valid(title="这一条待会儿要被人接走的单"))
check("D 再发一条 201", st == 201, (st, d))
T2 = data(d).get("id", "")

st, d = P.take(T2)
check("P 认领成功 201", st == 201, (st, d))
check("返回待确认", data(d).get("state") == "to_confirm", d)

st, d = D.withdraw(T2)
check("有人接了撤单 409", st == 409, (st, d))
check("错误码 TASK_ALREADY_TAKEN", errcode(d) == "TASK_ALREADY_TAKEN", d)
check("提示让走订单侧退回", "退回" in ((d.get("error") or {}).get("hint") or ""), d)

mine = [t for t in D.my_tasks() if t.get("id") == T2]
check("撤失败后状态仍是 matched", mine and mine[0].get("status") == "matched", mine)

st, d = P.call("GET", "/api/v2/me/orders")
orders = (d.get("data") or {}).get("items") or []
alive = [o for o in orders if o.get("task_id") == T2]
check("订单还在, 没被撤单带走", len(alive) == 1, orders)
if alive:
    check("订单仍是待确认", alive[0].get("state") == "to_confirm", alive[0])

print("[4] 不被牵连: 撤自己的单不影响别人的单")

st, d = D.publish(**valid(title="这条不动, 用来验证不会被别人撤掉"))
check("D 发 T3 201", st == 201, (st, d))
T3 = data(d).get("id", "")

st, d = X.publish(**valid(title="X 自己的一条单, 待会儿自己撤"))
check("X 发 T4 201", st == 201, (st, d))
T4 = data(d).get("id", "")

st, d = X.withdraw(T4)
check("X 撤自己的单 200", st == 200, (st, d))
check("T3 不受影响, 仍在大厅", T3 in D.hall(), T3)
check("T3 状态仍是 open", [t for t in D.my_tasks() if t.get("id") == T3][0].get("status") == "open", "")

print("[5] 越权探测不泄露进展")

# X 撤 D 那条已经有人接的单: 必须先说"不是你的", 不能说出"已经有人接了"
st, d = X.withdraw(T2)
check("撤别人的已接单 403(不是 409)", st == 403, (st, d))
check("错误码仍是 NOT_TASK_OWNER", errcode(d) == "NOT_TASK_OWNER", d)
check("不泄露这单已被接走", "接" not in ((d.get("error") or {}).get("message") or ""), d)

print("[6] CSRF 与并发")

st, d = D.call("POST", "/api/v2/tasks/%s/withdraw" % T3, {}, csrf=False)
check("缺 CSRF 头 403", st == 403, (st, d))
check("错误码 CSRF_MISSING", errcode(d) == "CSRF_MISSING", d)

# 撤回与认领同时打同一条单: 只能有一个成立
st, d = D.publish(**valid(title="撤回和认领同时打过来, 只能成一个"))
RACE = data(d).get("id", "")

res = {}


def fire(name, fn):
    try:
        res[name] = fn(RACE)
    except Exception as e:  # 连接抖动不该算成业务结果
        res[name] = ("EXC", str(e))


import threading

t1 = threading.Thread(target=fire, args=("withdraw", D.withdraw))
t2 = threading.Thread(target=fire, args=("take", P.take))
t1.start()
t2.start()
t1.join()
t2.join()

w_st = res.get("withdraw", (0,))[0]
k_st = res.get("take", (0,))[0]
check("撤回与认领只有一个成功", (w_st == 200) != (k_st == 201), (w_st, k_st))
checked = [t for t in D.my_tasks() if t.get("id") == RACE]
status = checked[0].get("status") if checked else "?"
if w_st == 200:
    check("撤回赢了: 单子是 withdrawn", status == "withdrawn", status)
    check("撤回赢了: 大厅里没有它", RACE not in D.hall(), RACE)
else:
    check("认领赢了: 单子是 matched", status == "matched", status)
    check("认领赢了: 大厅里也没有它", RACE not in D.hall(), RACE)

print("")
print("=" * 56)
print("通过 %d / 失败 %d" % (PASS, FAIL))
if FAILS:
    for f in FAILS:
        print("  ! " + f)
    raise SystemExit(1)
print("撤单链路 OK")
