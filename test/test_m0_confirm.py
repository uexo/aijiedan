"""M0 确认验收: 认领之后, 需求方这一侧能点头, 也能不点头。

认领只是"有人举手", 单子要真开工, 还差需求方这一下。这条链路要证明:
  1) 谁能点头 —— 只有发这条需求的人; 接单方、路人、没登录的一个都不行
  2) 点头之后单子真的进入进行中, 两边看到的说法一致
  3) 不点头能退回大厅 —— 单子重新开放, 别人接得走
  4) 退回是订单和任务一起改的 —— 不会留下"已取消但还占着"的单
  5) 并发点头只算一次

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_confirm.py
    BASE=http://127.0.0.1:8080 python test/test_m0_confirm.py
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

# 客户端连接层抖动, 见 call() 的说明
CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)
# 重放写请求会拿到这些"状态已经变了"的错误码: 它们不代表业务结论, 不能当响应返回
REPLAY_CONFLICTS = {
    "TASK_TAKEN", "TASK_NOT_OPEN", "TASK_ALREADY_TAKEN", "TASK_CHANGED",
    "ORDER_NOT_PENDING", "ORDER_NOT_TO_REVIEW", "ORDER_NOT_IN_PROGRESS",
    "REVISION_LIMIT", "DELIVERY_DUPLICATE", "CODE_ALREADY_USED", "SLUG_TAKEN",
}

# 种子需求方的手机号, 见 internal/store 的 Seed。短号 → 手机号
OWNER_PHONE = {"r001": "13800000001", "r002": "13800000002", "r003": "13800000003"}

PASS = FAIL = 0
FAILS = []


class Client:
    """一个浏览器身份。每个 Client 有自己的 cookie jar, 并发测试才不会串会话。"""

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

    def confirm(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/confirm" % order_id, {})

    def decline(self, order_id, reason=""):
        return self.call("POST", "/api/v2/orders/%s/decline" % order_id, {"reason": reason})

    def orders(self):
        st, d = self.call("GET", "/api/v2/me/orders")
        if st != 200:
            return st, []
        return st, (d.get("data") or {}).get("items") or []

    def find_order(self, order_id):
        _, items = self.orders()
        for o in items:
            if o.get("id") == order_id:
                return o
        return None


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


def code_of(d):
    return (d.get("error") or {}).get("code")


def make_provider(headline):
    c = Client()
    st, _ = c.login(new_phone("156"), "supply")
    assert st == 200, st
    st, d = c.profile(headline)
    assert st == 200, d
    return c


print("[0] 准备身份")
P1 = make_provider("把重复流程做成能跑的 Agent")
P2 = make_provider("只做能上线的页面前端")
P3 = make_provider("数据清洗与标注, 换人能复用")
ANON = Client()

# 两张开放的需求, 尽量分属两个不同的需求方:
# 这样"别人的单我能不能点头"才是真的在测权限, 而不是在测同一个人。
_, hd = P1.call("GET", "/api/v2/tasks?limit=50")
open_tasks = [t for t in (((hd.get("data") or {}).get("items")) or [])
              if t.get("requester_slug") in OWNER_PHONE]
check("大厅里有种子需求方发的开放需求", len(open_tasks) >= 2,
      [t.get("id") for t in open_tasks])

T1, SLUG1 = open_tasks[0]["id"], open_tasks[0]["requester_slug"]
second = [t for t in open_tasks[1:] if t["requester_slug"] != SLUG1] or open_tasks[1:]
T2, SLUG2 = second[0]["id"], second[0]["requester_slug"]
check("两单分属不同需求方", SLUG1 != SLUG2, (T1, SLUG1, T2, SLUG2))

R1 = Client()
st, d = R1.login(OWNER_PHONE[SLUG1], "demand")
check("需求方一号能登录", st == 200, d)
check("角色是需求方", ((d.get("data") or {}).get("user") or {}).get("role") == "requester", d)

R2 = Client()
st, d = R2.login(OWNER_PHONE[SLUG2], "demand")
check("需求方二号能登录", st == 200, d)

Q3 = Client()
Q3.login(new_phone("137"), "demand")  # 第三个需求方, 这两单都不是他的

print("[1] 没登录的人不能点头")
st, d = ANON.confirm("O-NOT-EXIST")
check("未登录确认 401", st == 401, st)
st, d = R1.confirm("O-NOT-EXIST")
check("确认不存在的订单 404", st == 404, (st, d))
check("错误码 ORDER_NOT_FOUND", code_of(d) == "ORDER_NOT_FOUND", d)

print("[2] P1 认领 T1, 需求方能看到这个人和他的定位")
st, d = P1.claim(T1)
check("P1 认领 201", st == 201, (st, d))
O1 = ((d.get("data") or {}).get("id")) or ""
check("拿到订单号", O1.startswith("O-"), O1)
check("接单方看到的四态=待确认", (d.get("data") or {}).get("state") == "to_confirm", d)
check("接单方这边 needs_me=False", (d.get("data") or {}).get("needs_me") is False, d)

o = R1.find_order(O1)
check("需求方能看见这一单", o is not None, O1)
check("需求方视角 role=requester", (o or {}).get("role") == "requester", o)
check("需求方 needs_me=True（该他动了）", (o or {}).get("needs_me") is True, o)
check("四态中文=待确认", (o or {}).get("state_label") == "待确认", o)
check("看到接单方的一句话定位",
      (o or {}).get("counterparty_headline") == "把重复流程做成能跑的 Agent",
      (o or {}).get("counterparty_headline"))
check("看到接单方信用分", int((o or {}).get("counterparty_credit") or 0) > 0, o)
check("看到接单方名字", bool((o or {}).get("counterparty")), o)

print("[3] 谁能点头: 只有发这条需求的人")
st, d = P1.confirm(O1)
check("接单方自己确认 403", st == 403, (st, d))
check("错误码 ROLE_NOT_REQUESTER", code_of(d) == "ROLE_NOT_REQUESTER", d)
st, d = P2.confirm(O1)
check("别的接单方确认 403", st == 403, (st, d))
st, d = R2.confirm(O1)
check("别的需求方确认 403", st == 403, (st, d))
check("别人的单报权限不报状态", code_of(d) == "ROLE_NOT_REQUESTER", d)

print("[4] 点头之后进入进行中")
st, d = R1.confirm(O1)
check("确认 200", st == 200, (st, d))
od = d.get("data") or {}
check("四态=进行中", od.get("state") == "in_progress", od)
check("四态中文=进行中", od.get("state_label") == "进行中", od)
check("内部状态=active", od.get("status") == "active", od)
check("确认完不再需要他动", od.get("needs_me") is False, od)

o = P1.find_order(O1)
check("接单方看到进行中", (o or {}).get("state") == "in_progress", o)
# 确认之后球在接单方这边: 该他交付了。needs_me 不是"永远给需求方",
# 而是"这一步该谁动", 所以这里跟着状态从需求方转到接单方。
check("确认后轮到接单方交付(needs_me=True)", (o or {}).get("needs_me") is True, o)

st, d = R1.confirm(O1)
check("重复确认 409", st == 409, (st, d))
check("错误码 ORDER_NOT_PENDING", code_of(d) == "ORDER_NOT_PENDING", d)
st, d = R1.decline(O1, "不做了")
check("开工之后再退回 409", st == 409, (st, d))
check("错误码 ORDER_NOT_PENDING（退回）", code_of(d) == "ORDER_NOT_PENDING", d)

st, hd = R1.call("GET", "/api/v2/tasks?limit=50")
left = [t["id"] for t in ((hd.get("data") or {}).get("items")) or []]
check("开工的单不在大厅里", T1 not in left, left)

print("[5] 不点头: 退回大厅, 别人接得走")
st, d = P2.claim(T2)
check("P2 认领 T2 201", st == 201, (st, d))
O2 = ((d.get("data") or {}).get("id")) or ""

LONG = "工期太紧等不了这么久" * 8  # 故意超 60 字, 验证按字截断
st, d = R2.decline(O2, LONG)
check("退回 200", st == 200, (st, d))
od = d.get("data") or {}
check("四态=已取消", od.get("state") == "cancelled", od)
check("内部状态=cancelled", od.get("status") == "cancelled", od)
reason = od.get("cancel_reason") or ""
check("退回原因存下来了", len(reason) > 0, reason)
check("原因截到 60 字以内", len(reason) <= 60, len(reason))
check("原因保留开头", reason.startswith("工期太紧"), reason)

o = P2.find_order(O2)
check("接单方看到已取消", (o or {}).get("state") == "cancelled", o)
check("接单方能看到退回原因", (o or {}).get("cancel_reason") == reason, o)

st, hd = R2.call("GET", "/api/v2/tasks?limit=50")
back = [t["id"] for t in ((hd.get("data") or {}).get("items")) or []]
check("退回之后单子重新出现在大厅", T2 in back, back)

st, d = R2.decline(O2, "再退一次")
check("重复退回 409", st == 409, (st, d))
check("错误码 ORDER_NOT_PENDING（重复退）", code_of(d) == "ORDER_NOT_PENDING", d)
st, d = R2.confirm(O2)
check("已取消的单不能确认 409", st == 409, (st, d))

st, d = P3.claim(T2)
check("退回后别人能接走 201", st == 201, (st, d))
O3 = ((d.get("data") or {}).get("id")) or ""
check("新订单号与原订单不同", O3 != O2, (O3, O2))

print("[6] 并发点头只算一次")
# 同一部手机开两个窗口: 各自先登录好, 再同时点确认。
# 不能在两个线程里同时登录——验证码是一次性的, 后一个登录会失败,
# 那样测到的是"登录偶尔失败", 不是"确认会不会算两次"。
R2b = Client()
st, _ = R2b.login(OWNER_PHONE[SLUG2], "demand")
check("第二个窗口登录成功", st == 200, st)

results = []
lock = threading.Lock()


def fire(client):
    st, d = client.confirm(O3)
    with lock:
        results.append((st, code_of(d) if st != 200 else ""))


ts = [threading.Thread(target=fire, args=(c,)) for c in (R2, R2b)]
for t in ts:
    t.start()
for t in ts:
    t.join()

oks = [r for r in results if r[0] == 200]
conflicts = [r for r in results if r[0] == 409]
check("两个窗口同时确认, 只成一个", len(oks) == 1, results)
check("另一个拿到 409 ORDER_NOT_PENDING",
      len(conflicts) == 1 and conflicts[0][1] == "ORDER_NOT_PENDING", results)

o = P3.find_order(O3)
check("最终状态是进行中", (o or {}).get("state") == "in_progress", o)

print("[7] 订单账目对得上")
check("O1 进行中", (R1.find_order(O1) or {}).get("state") == "in_progress", O1)
check("O2 已取消", (R2.find_order(O2) or {}).get("state") == "cancelled", O2)
check("O3 进行中", (R2.find_order(O3) or {}).get("state") == "in_progress", O3)

_, p3items = P3.orders()
check("P3 只有一单（退回过的那单没多出一条）", len(p3items) == 1,
      [o.get("id") for o in p3items])

# 同一张需求上不能同时存在两个活订单: 退回过的那单已取消, 新单在进行中
_, r2items = R2.orders()
same_task = [o for o in r2items if o.get("task_id") == T2]
live = [o for o in same_task if o.get("state") != "cancelled"]
check("同一张需求只有一个活订单", len(live) == 1, [o.get("id") for o in same_task])
check("退回过的那单还在账上（可追溯）", len(same_task) == 2, [o.get("id") for o in same_task])

print("[8] 需求方账号不会被当成接单方")
st, d = R1.claim(T1)
check("需求方不能认领 403", st == 403, (st, d))

print("")
print("结果: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
raise SystemExit(1 if FAIL else 0)
