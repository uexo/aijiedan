"""M0 交付: 确认开工之后, 接单方要能把东西交上去, 单子才走到待验收。

确认只解决"这一单算不算数"。单子进入进行中之后, 真正的推力在接单方这边:
不交, 需求方没有东西可验收, 这一单会永远停在进行中。这条链路要证明:
  1) 谁能交 —— 只有这一单的接单方; 需求方、别的接单方、路人都不能
  2) 什么时候能交 —— 没确认开工不能交; 交过了不能重复交
  3) 交什么 —— 说明必填, 链接只收 http/https, 有清单就必须逐项交代
  4) 交完之后 —— 订单进 delivered(对外=待验收), 双方看到同一份内容
  5) 交付记录只有交易双方能读

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_deliver.py
    BASE=http://127.0.0.1:8080 python test/test_m0_deliver.py
"""
import http.client
import http.cookiejar
import json
import os
import random
import time
import urllib.error
import urllib.request

BASE = os.environ.get("BASE", "http://127.0.0.1:8082")
# 直连, 不走系统代理: 本机代理会把回环请求也接过去, 偶发掐断连接
NO_PROXY = urllib.request.ProxyHandler({})

# 种子需求对应的需求方手机号, 见 internal/store 的 Seed
OWNER_PHONE = {"r001": "13800000001", "r002": "13800000002", "r003": "13800000003"}

# 两条带需求清单的种子需求(main.go seedTasks), 用来验"逐项交付"
T_WITH_ITEMS = "T-DEMO-001"
T_NO_ITEMS = "T-DEMO-002"
T_NOT_STARTED = "T-DEMO-003"

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
    st, _ = c.login(new_phone("156"), "supply")
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

st, d = P1.claim(T_WITH_ITEMS)
check("P1 认领带清单的需求 201", st == 201, (st, d))
O1 = ((d.get("data") or {}).get("id")) or ""
check("拿到订单号", O1.startswith("O-"), O1)

st, d = P2.claim(T_NO_ITEMS)
check("P2 认领无清单的需求 201", st == 201, (st, d))
O2 = ((d.get("data") or {}).get("id")) or ""

st, d = P1.claim(T_NOT_STARTED)
check("P1 再认领一张还没确认开工的单 201", st == 201, (st, d))
O3 = ((d.get("data") or {}).get("id")) or ""

print("[1] 没登录、非本单接单方都不能交付")
st, d = ANON.deliver(O1, "我是路人")
check("未登录交付 401", st == 401, st)
st, d = P1.deliver("O-NOT-EXIST", "交个不存在的单")
check("交付不存在的订单 404", st == 404, (st, d))
check("错误码 ORDER_NOT_FOUND", code_of(d) == "ORDER_NOT_FOUND", d)
st, d = P2.deliver(O1, "别人的单")
check("别的接单方交付 403", st == 403, (st, d))
check("错误码 NOT_ORDER_PROVIDER", code_of(d) == "NOT_ORDER_PROVIDER", d)
st, d = R1.deliver(O1, "我自己交")
check("需求方替接单方交付 403", st == 403, (st, d))
check("需求方拿到的是权限错误而不是状态错误", code_of(d) == "NOT_ORDER_PROVIDER", d)
st, d = P2.deliveries(O1)
check("非参与方读交付记录 403", st == 403, (st, d))
check("错误码 NOT_PARTICIPANT", code_of(d) == "NOT_PARTICIPANT", d)
st, d = ANON.deliveries(O1)
check("未登录读交付记录 401", st == 401, st)

print("[2] 还没确认开工, 不能交")
st, d = P1.deliver(O3, "先交为敬")
check("未开工交付 409", st == 409, (st, d))
check("错误码 ORDER_NOT_IN_PROGRESS", code_of(d) == "ORDER_NOT_IN_PROGRESS", d)
check("说的是还没开工", "还没确认开工" in msg_of(d), msg_of(d))

print("[3] 需求方点头, 两张单进入进行中")
st, d = R1.confirm(O1)
check("需求方确认 O1", st == 200 and (d.get("data") or {}).get("state") == "in_progress", (st, d))
st, d = R2.confirm(O2)
check("需求方确认 O2", st == 200 and (d.get("data") or {}).get("state") == "in_progress", (st, d))

o = P1.find_order(O1)
check("接单方在待交付时 needs_me=True", (o or {}).get("needs_me") is True, o)
check("订单带上了需求清单", len((o or {}).get("requirements") or []) == 3, o)
check("清单项按 R1/R2/R3 排列",
      [r.get("id") for r in ((o or {}).get("requirements") or [])] == ["R1", "R2", "R3"], o)

print("[4] 内容校验: 说明必填, 长度有上限, 链接只收 http/https")
st, d = P1.deliver(O1, "   ")
check("空交付说明 400", st == 400, (st, d))
check("错误码 DELIVERY_EMPTY", code_of(d) == "DELIVERY_EMPTY", d)
st, d = P1.deliver(O1, "长" * 1001)
check("超过 1000 字 400", st == 400, (st, d))
check("错误码 DELIVERY_TOO_LONG", code_of(d) == "DELIVERY_TOO_LONG", d)
st, d = P1.deliver(O1, "交了", link="javascript:alert(1)")
check("非 http 链接 400", st == 400, (st, d))
check("错误码 DELIVERY_LINK_INVALID", code_of(d) == "DELIVERY_LINK_INVALID", d)

print("[5] 有清单就得逐项交代")
st, d = P1.deliver(O1, "交了一部分", items=[{"item_id": "R1", "done": True}])
check("只交一项 400", st == 400, (st, d))
check("错误码 DELIVERY_ITEM_INVALID", code_of(d) == "DELIVERY_ITEM_INVALID", d)
check("指出还差哪几项", "R2" in msg_of(d) and "R3" in msg_of(d), msg_of(d))
st, d = P1.deliver(O1, "交了个不存在的项", items=[{"item_id": "R9", "done": True}])
check("不存在的需求项 400", st == 400, (st, d))
check("指出是哪一项", "R9" in msg_of(d), msg_of(d))
st, d = P1.deliver(O1, "同一项填两次", items=[
    {"item_id": "R1", "done": True}, {"item_id": "R1", "done": True},
    {"item_id": "R2", "done": True}, {"item_id": "R3", "done": True}])
check("重复项 400", st == 400, (st, d))
st, d = P1.deliver(O1, "有一项没做完", items=[
    {"item_id": "R1", "done": True}, {"item_id": "R2", "done": True},
    {"item_id": "R3", "done": False}])
check("未完成项不写原因 400", st == 400, (st, d))
check("错误码 DELIVERY_ITEM_NOTE", code_of(d) == "DELIVERY_ITEM_NOTE", d)

print("[6] 正常交付: 进入待验收, 双方看到同一份内容")
st, d = P1.deliver(
    O1, "RAG 问答系统已可跑，R3 的部署脚本还差 Windows 说明。",
    link="https://example.com/rag.zip",
    items=[
        {"item_id": "R1", "done": True, "note": "PDF/Word 都通了"},
        {"item_id": "R2", "done": True, "note": "返回带引用片段"},
        {"item_id": "R3", "done": False, "note": "Windows 启动脚本明天补"},
    ])
check("交付 201", st == 201, (st, d))
od = d.get("data") or {}
check("四态=待验收", od.get("state") == "to_review", od)
check("四态中文=待验收", od.get("state_label") == "待验收", od)
check("内部状态=delivered", od.get("status") == "delivered", od)
check("交付轮次=1", od.get("delivery_round") == 1, od)
last = od.get("last_delivery") or {}
check("带回最近一轮交付", last.get("content", "").startswith("RAG 问答系统已可跑"), last)
check("交付链接留存", last.get("link") == "https://example.com/rag.zip", last)
check("统计出 1 项未完成", last.get("undone_count") == 1, last)
check("逐项清单按 R1/R2/R3 落库",
      [i.get("item_id") for i in (last.get("items") or [])] == ["R1", "R2", "R3"], last)
check("未完成项的原因没丢",
      any(i.get("item_id") == "R3" and "Windows" in (i.get("note") or "") for i in (last.get("items") or [])), last)
check("交完之后接单方不再需要他动", od.get("needs_me") is False, od)

o = R1.find_order(O1)
check("需求方看到待验收", (o or {}).get("state") == "to_review", o)
check("需求方看到交付内容", ((o or {}).get("last_delivery") or {}).get("content", "").startswith("RAG 问答系统已可跑"), o)
check("需求方看到对方是接单方", (o or {}).get("role") == "requester", o)

print("[7] 交过就不能再交")
st, d = P1.deliver(O1, "再交一次")
check("重复交付 409", st == 409, (st, d))
check("错误码 ORDER_NOT_IN_PROGRESS", code_of(d) == "ORDER_NOT_IN_PROGRESS", d)
check("说的是已经交付过", "已经交付过" in msg_of(d), msg_of(d))

print("[8] 交付记录: 双方可读, 轮次与内容对得上")
st, d = P1.deliveries(O1)
items = (d.get("data") or {}).get("items") or []
check("接单方读交付记录 200", st == 200, (st, d))
check("一条交付", len(items) == 1, items)
check("轮次=1", (items[0] if items else {}).get("round") == 1, items)
check("交付状态=submitted", (items[0] if items else {}).get("status") == "submitted", items)
st, d = R1.deliveries(O1)
check("需求方读交付记录 200", st == 200, (st, d))
check("需求方读到同一条", len((d.get("data") or {}).get("items") or []) == 1, d)

print("[9] 没有需求清单的单: 只交说明也能走通")
st, d = P2.deliver(O2, "随便交个不存在的项", items=[{"item_id": "R1", "done": True}])
check("无清单的单不接受清单项 400", st == 400, (st, d))
check("错误码 DELIVERY_ITEM_INVALID", code_of(d) == "DELIVERY_ITEM_INVALID", d)

st, d = P2.deliver(O2, "10 篇成稿与标题备选都在文档里，人设按三组切分。",
                   link="https://example.com/copy")
check("无清单的单交付 201", st == 201, (st, d))
od = d.get("data") or {}
check("进入待验收", od.get("state") == "to_review", od)
check("没有逐项清单", (od.get("last_delivery") or {}).get("items") in (None, []), od)
check("未完成项计数=0", (od.get("last_delivery") or {}).get("undone_count") == 0, od)

print("[10] 交付不影响大厅与其它单")
st, hd = R1.call("GET", "/api/v2/tasks?limit=50")
left = [t["id"] for t in ((hd.get("data") or {}).get("items")) or []]
check("已交付的单不在大厅", T_WITH_ITEMS not in left and T_NO_ITEMS not in left, left)
check("没确认开工的单也不在大厅(已被认领)", T_NOT_STARTED not in left, left)

print("")
print("=" * 56)
print("交付链路: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
print("=" * 56)
raise SystemExit(1 if FAIL else 0)
