"""M0 验收: 交付之后需求方要能点头收工, 也能打回让接单方改。

交付只解决"有东西可看"。看完之后需求方必须能做两件事之一, 否则单子会永远停在待验收:
  1) 验收通过 —— 订单完成、需求单关闭, 这一单不再有任何动作
  2) 打回修改 —— 订单回到进行中, 接单方按打回说明再交一版
这条链路要证明:
  - 谁能做 —— 只有这一单的需求方; 接单方、别的接单方、路人都不能
  - 什么时候能做 —— 只有待验收; 未交付、已完成都不行
  - 打回必须写原因, 且原因落在被打回的那一轮交付上
  - 轮次用完不能再打回 (打回之后接单方就交不出下一轮, 等于把单子打死)
  - 并发验收只算一次

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_accept.py
    BASE=http://127.0.0.1:8080 python test/test_m0_accept.py
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

OWNER_PHONE = {"r001": "13800000001", "r002": "13800000002", "r003": "13800000003"}

T_WITH_ITEMS = "T-DEMO-001"   # R001, 3 项清单
T_NO_ITEMS = "T-DEMO-002"     # R002, 无清单
T_RACE = "T-DEMO-003"         # R003, 无清单

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

    def profile(self, headline):
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

    def deliver(self, order_id, content, link="", items=None):
        return self.call("POST", "/api/v2/orders/%s/deliveries" % order_id, {
            "content": content,
            "link": link,
            "items": items or [],
        })

    def accept(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/accept" % order_id, {})

    def revise(self, order_id, reason):
        return self.call("POST", "/api/v2/orders/%s/revisions" % order_id, {"reason": reason})

    def deliveries(self, order_id):
        return self.call("GET", "/api/v2/orders/%s/deliveries" % order_id)

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


def items_all_done():
    return [
        {"item_id": "R1", "done": True, "note": "PDF/Word 都通了"},
        {"item_id": "R2", "done": True, "note": "返回带引用片段"},
        {"item_id": "R3", "done": True, "note": "启动脚本已补 Windows 说明"},
    ]


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


def msg_of(d):
    return (d.get("error") or {}).get("message") or ""


def make_provider(headline):
    c = Client()
    st, _ = c.login(new_phone("157"), "supply")
    assert st == 200, st
    st, d = c.profile(headline)
    assert st == 200, d
    return c


print("[0] 准备身份与三张单")
P1 = make_provider("把重复流程做成能跑的 Agent")
P2 = make_provider("只做能上线的页面前端")
ANON = Client()

R1 = Client()
st, d = R1.login(OWNER_PHONE["r001"], "demand")
check("需求方一号能登录", st == 200, d)
R2 = Client()
st, d = R2.login(OWNER_PHONE["r002"], "demand")
check("需求方二号能登录", st == 200, d)
R3 = Client()
st, d = R3.login(OWNER_PHONE["r003"], "demand")
check("需求方三号能登录", st == 200, d)

st, d = P1.claim(T_WITH_ITEMS)
check("P1 认领带清单的需求 201", st == 201, (st, d))
O1 = ((d.get("data") or {}).get("id")) or ""

st, d = P2.claim(T_NO_ITEMS)
check("P2 认领无清单的需求 201", st == 201, (st, d))
O2 = ((d.get("data") or {}).get("id")) or ""

st, d = P2.claim(T_RACE)
check("P2 再认领一张用于并发验收的单 201", st == 201, (st, d))
O3 = ((d.get("data") or {}).get("id")) or ""
check("三个订单号都拿到了", all(x.startswith("O-") for x in (O1, O2, O3)), (O1, O2, O3))

print("[1] 谁能验收/打回: 先判资格, 再判状态")
st, d = ANON.accept(O1)
check("未登录验收 401", st == 401, st)
st, d = P1.accept(O1)
check("接单方自己验收 403", st == 403, (st, d))
check("错误码 ROLE_NOT_REQUESTER", code_of(d) == "ROLE_NOT_REQUESTER", d)
st, d = P1.revise(O1, "我自己打回自己")
check("接单方自己打回 403", st == 403, (st, d))
st, d = R2.accept(O1)
check("别的需求方验收别人的单 403", st == 403, (st, d))
check("拿到的还是权限错误, 不是状态错误", code_of(d) == "ROLE_NOT_REQUESTER", d)
st, d = R1.accept("O-NOT-EXIST")
check("验收不存在的订单 404", st == 404, (st, d))
check("错误码 ORDER_NOT_FOUND", code_of(d) == "ORDER_NOT_FOUND", d)

print("[2] 还没交付, 不能验收也不能打回")
st, d = R1.accept(O1)
check("待确认的单验收 409", st == 409, (st, d))
check("错误码 ORDER_NOT_TO_REVIEW", code_of(d) == "ORDER_NOT_TO_REVIEW", d)
st, d = R1.revise(O1, "还没交就让我打回")
check("待确认的单打回 409", st == 409, (st, d))

print("[3] 进到待验收")
st, d = R1.confirm(O1)
check("需求方确认 O1", st == 200, (st, d))
st, d = R2.confirm(O2)
check("需求方确认 O2", st == 200, (st, d))
st, d = R3.confirm(O3)
check("需求方确认 O3", st == 200, (st, d))

st, d = P1.deliver(O1, "第一版：R1/R2 已完成，R3 启动脚本只在 Linux 上验证过。",
                   link="https://example.com/rag-v1.zip", items=items_all_done())
check("P1 交付第一轮 201", st == 201, (st, d))
od = d.get("data") or {}
check("交付后进入待验收", od.get("state") == "to_review", od)
o = R1.find_order(O1)
check("需求方在待验收时 needs_me=True", (o or {}).get("needs_me") is True, o)
o = P1.find_order(O1)
check("接单方在待验时 needs_me=False", (o or {}).get("needs_me") is False, o)

print("[4] 打回: 原因必填、不截断, 打回后回到进行中")
st, d = R1.revise(O1, "   ")
check("空原因打回 400", st == 400, (st, d))
check("错误码 REVIEW_NOTE_EMPTY", code_of(d) == "REVIEW_NOTE_EMPTY", d)
st, d = R1.revise(O1, "长" * 501)
check("超过 500 字打回 400", st == 400, (st, d))
check("错误码 REVIEW_NOTE_TOO_LONG", code_of(d) == "REVIEW_NOTE_TOO_LONG", d)
check("超长是报错而不是静默截断", "太长" in msg_of(d), msg_of(d))

st, d = R1.revise(O1, "R3 的启动脚本只在 Linux 上验证过，Windows 用户起不来。补一份 Windows 说明再交。")
check("正常打回 200", st == 200, (st, d))
od = d.get("data") or {}
check("打回后四态=进行中", od.get("state") == "in_progress", od)
check("打回后内部状态=delivering", od.get("status") == "delivering", od)
last = od.get("last_delivery") or {}
check("最近一轮标成 rejected", last.get("status") == "rejected", last)
check("打回说明落在这一轮交付上", "Windows" in (last.get("review_note") or ""), last)
check("打回时间有值", int(last.get("reviewed_at") or 0) > 0, last)
check("交付轮次还是 1", od.get("delivery_round") == 1, od)

o = R1.find_order(O1)
check("打回后需求方不再需要动", (o or {}).get("needs_me") is False, o)
o = P1.find_order(O1)
check("打回后轮到接单方动手", (o or {}).get("needs_me") is True, o)
check("接单方能读到打回说明",
      "Windows" in (((o or {}).get("last_delivery") or {}).get("review_note") or ""), o)

print("[5] 打回后能再交一轮")
st, d = R1.accept(O1)
check("已打回的单不能再验收(状态已不是待验收)", st == 409, (st, d))
st, d = P1.deliver(O1, "第二版：补了 Windows 启动脚本与说明。",
                   link="https://example.com/rag-v2.zip", items=items_all_done())
check("P1 再交一轮 201", st == 201, (st, d))
od = d.get("data") or {}
check("又回到待验收", od.get("state") == "to_review", od)
check("轮次走到 2", od.get("delivery_round") == 2, od)
check("这一轮的 status=submitted",
      ((od.get("last_delivery") or {}).get("status")) == "submitted", od)

print("[6] 历史轮次留痕: 打回说明不被下一轮盖掉")
st, d = R1.deliveries(O1)
items = (d.get("data") or {}).get("items") or []
check("需求方读交付记录 200", st == 200, (st, d))
check("两条历史交付", len(items) == 2, items)
r1 = items[0] if items else {}
r2 = items[1] if len(items) > 1 else {}
check("第 1 轮标 rejected", r1.get("status") == "rejected", r1)
check("第 1 轮的打回说明还在", "Windows" in (r1.get("review_note") or ""), r1)
check("第 2 轮标 submitted", r2.get("status") == "submitted", r2)
check("第 2 轮没有打回说明", not (r2.get("review_note") or ""), r2)
st, d = P2.deliveries(O1)
check("非参与方读交付记录 403", st == 403, (st, d))

print("[7] 验收通过: 这一单结束")
st, d = R1.accept(O1)
check("验收通过 200", st == 200, (st, d))
od = d.get("data") or {}
check("四态=已完成", od.get("state") == "done", od)
check("四态中文=已完成", od.get("state_label") == "已完成", od)
check("内部状态=completed", od.get("status") == "completed", od)
check("完成后没人需要动", od.get("needs_me") is False, od)
o = R1.find_order(O1)
check("需求方在列表里看到已完成", (o or {}).get("state") == "done", o)
o = P1.find_order(O1)
check("接单方在列表里看到已完成", (o or {}).get("state") == "done", o)

st, d = R1.deliveries(O1)
items = (d.get("data") or {}).get("items") or []
check("通过的那一轮标 accepted",
      (items[-1] if items else {}).get("status") == "accepted", items)

print("[8] 已完成之后: 不能再验收、不能打回、不能再交")
st, d = R1.accept(O1)
check("重复验收 409", st == 409, (st, d))
check("错误码 ORDER_NOT_TO_REVIEW", code_of(d) == "ORDER_NOT_TO_REVIEW", d)
st, d = R1.revise(O1, "反悔了，再改改")
check("已完成再打回 409", st == 409, (st, d))
st, d = P1.deliver(O1, "再交一版")
check("已完成再交付 409", st == 409, (st, d))
check("错误码 ORDER_NOT_IN_PROGRESS", code_of(d) == "ORDER_NOT_IN_PROGRESS", d)

print("[9] 轮次用完不能再打回")
# O2 无清单, 连交三轮: 打完第 2 轮后再打回就该被拒
st, d = P2.deliver(O2, "第 1 版")
check("O2 第 1 轮交付 201", st == 201, (st, d))
st, d = R2.revise(O2, "人设不对，第 1 版偏硬广")
check("O2 第 1 次打回 200", st == 200, (st, d))
st, d = P2.deliver(O2, "第 2 版")
check("O2 第 2 轮交付 201", st == 201, (st, d))
st, d = R2.revise(O2, "第 2 版还是不对")
check("O2 第 2 次打回 200", st == 200, (st, d))
st, d = P2.deliver(O2, "第 3 版")
check("O2 第 3 轮交付 201", st == 201, (st, d))
check("轮次走到上限 3", ((d.get("data") or {}).get("delivery_round")) == 3, d)
st, d = R2.revise(O2, "还想再改")
check("轮次用完再打回 409", st == 409, (st, d))
check("错误码 REVISION_LIMIT", code_of(d) == "REVISION_LIMIT", d)
check("这句话是对需求方说的", "不能再打回" in msg_of(d), msg_of(d))
st, d = R2.accept(O2)
check("轮次用完后仍然可以验收", st == 200, (st, d))
check("验收后 O2 已完成", ((d.get("data") or {}).get("state")) == "done", d)

print("[10] 并发验收只算一次")
R3b = Client()
st, d = R3b.login(OWNER_PHONE["r003"], "demand")
check("同一需求方第二个会话能登录", st == 200, d)
st, d = P2.deliver(O3, "落地页做完了，移动端也过了")
check("O3 交付 201", st == 201, (st, d))

results = []
lock = threading.Lock()


def race(cl, oid):
    st, d = cl.accept(oid)
    with lock:
        results.append((st, code_of(d)))


ts = [threading.Thread(target=race, args=(c, O3)) for c in (R3, R3b)]
for t in ts:
    t.start()
for t in ts:
    t.join()

codes = sorted(st for st, _ in results)
check("两次并发验收: 一次 200 一次 409", codes == [200, 409], results)
o = R3.find_order(O3)
check("订单只完成一次", (o or {}).get("state") == "done", o)
st, d = R3b.deliveries(O3)
items = (d.get("data") or {}).get("items") or []
check("只留下一条 delivered 交付", len(items) == 1, items)

print("[11] 交付/验收不影响大厅")
st, hd = R1.call("GET", "/api/v2/tasks?limit=50")
left = [t["id"] for t in ((hd.get("data") or {}).get("items")) or []]
check("三张单都不在大厅", not ({T_WITH_ITEMS, T_NO_ITEMS, T_RACE} & set(left)), left)

print("")
print("=" * 56)
print("验收链路: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
print("=" * 56)
raise SystemExit(1 if FAIL else 0)
