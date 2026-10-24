"""M0 互评: 一单走完之后, 双方互相评价, 但同时揭盲。

这一条要证明的不是"能提交一条评价", 而是"先写的人不吃亏":
  - 谁能评 —— 只有这一单的双方; 路人、别的接单方、别的需求方都不行
  - 什么时候能评 —— 只有已完成; 没走完的单不能评
  - 单方提交后对方看不见内容 —— 连打了几分都不能透, 否则先写的就输了
  - 双方都提交后才互相可见 —— 且是同一时刻
  - 一个人对同一单只能评一次 —— AvgRating 是求均值, 重复评等于刷分
  - 空正文也能提交, 且能正常读回 —— comment 列可空, 扫进 string 会静默失效

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_review.py
    BASE=http://127.0.0.1:8080 python test/test_m0_review.py
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

OWNER_PHONE = {"r001": "13800000001", "r002": "13800000002"}

T_MAIN = "T-DEMO-001"   # R001 的单, 用来走完全程
T_OTHER = "T-DEMO-002"  # R002 的单, 只认领不完成, 用来测"没走完不能评"

PASS = FAIL = 0
FAILS = []

CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)
REPLAY_CONFLICTS = {
    "TASK_TAKEN", "TASK_NOT_OPEN", "TASK_ALREADY_TAKEN", "TASK_CHANGED",
    "ORDER_NOT_PENDING", "ORDER_NOT_TO_REVIEW", "ORDER_NOT_IN_PROGRESS",
    "REVISION_LIMIT", "ALREADY_REVIEWED", "CODE_ALREADY_USED", "SLUG_TAKEN",
}


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(NO_PROXY, urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, csrf=True):
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
                    raise ConnectionResetError("empty body")
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

    def deliver(self, order_id, content, items=None):
        return self.call("POST", "/api/v2/orders/%s/deliveries" % order_id,
                         {"content": content, "items": items or []})

    def accept(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/accept" % order_id, {})

    def review(self, order_id, rating, comment=""):
        return self.call("POST", "/api/v2/orders/%s/reviews" % order_id, {"rating": rating, "comment": comment})

    def reviews(self, order_id):
        return self.call("GET", "/api/v2/orders/%s/reviews" % order_id)

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


def data_of(d):
    return d.get("data") or {}


print("[0] 准备身份与一张走完的单")
P1 = make_provider("把重复流程做成能跑的 Agent")
P2 = make_provider("只做能上线的页面前端")
ANON = Client()

R1 = Client()
st, d = R1.login(OWNER_PHONE["r001"], "demand")
check("需求方一号能登录", st == 200, d)
R2 = Client()
st, d = R2.login(OWNER_PHONE["r002"], "demand")
check("需求方二号能登录", st == 200, d)

st, d = P1.claim(T_MAIN)
check("P1 认领主单 201", st == 201, (st, d))
O1 = data_of(d).get("id") or ""
st, d = P2.claim(T_OTHER)
check("P2 认领另一单 201", st == 201, (st, d))
O2 = data_of(d).get("id") or ""
check("两个订单号都拿到了", all(x.startswith("O-") for x in (O1, O2)), (O1, O2))

# 把另一单推到"待验收"(有交付、未验收): 用它测"没走完不能评"
st, d = R2.confirm(O2)
check("R2 确认另一单", st == 200, (st, d))
st, d = P2.deliver(O2, "只交了一版，还没验收")
check("P2 交付另一单 201", st == 201, (st, d))

st, d = R1.confirm(O1)
check("R1 确认主单", st == 200, (st, d))
# 主单带 3 项清单, 交付必须逐项交代, 否则会被 DELIVERY_ITEM_INVALID 拦下。
st, d = P1.deliver(O1, "做完了，启动脚本和说明都放进链接了", items=items_all_done())
check("P1 交付主单 201", st == 201, (st, d))

print("[1] 没走完的单不能评")
st, d = R1.review(O1, 5, "还没验收就想给分")
check("待验收的单评价 409", st == 409, (st, d))
check("错误码 ORDER_NOT_COMPLETED", code_of(d) == "ORDER_NOT_COMPLETED", d)
st, d = R2.review(O2, 5, "同上")
check("另一张待验收的单也不能评 409", st == 409, (st, d))

print("[2] 走完这一单")
st, d = R1.accept(O1)
check("R1 验收通过 200", st == 200, (st, d))
check("订单已完成", data_of(d).get("state") == "done", d)

print("[3] 谁能评: 先判资格, 再判状态")
st, d = ANON.review(O1, 5, "路人")
check("未登录评价 401", st == 401, st)
st, d = P2.review(O1, 5, "不是我接的单")
check("别的接单方评价 403", st == 403, (st, d))
check("错误码 NOT_PARTICIPANT", code_of(d) == "NOT_PARTICIPANT", d)
st, d = R2.review(O1, 5, "不是我发的单")
check("别的需求方评价 403", st == 403, (st, d))
st, d = R1.review("O-NOT-EXIST", 5, "")
check("评价不存在的订单 404", st == 404, (st, d))
check("错误码 ORDER_NOT_FOUND", code_of(d) == "ORDER_NOT_FOUND", d)

print("[4] 分数与正文校验")
st, d = R1.review(O1, 0, "")
check("0 分 400", st == 400, (st, d))
check("错误码 RATING_RANGE", code_of(d) == "RATING_RANGE", d)
st, d = R1.review(O1, 6, "")
check("6 分 400", st == 400, (st, d))
st, d = R1.review(O1, 5, "长" * 501)
check("超过 500 字 400", st == 400, (st, d))
check("错误码 REVIEW_TOO_LONG", code_of(d) == "REVIEW_TOO_LONG", d)
check("超长是报错而不是静默截断", "太长" in msg_of(d), msg_of(d))

print("[5] 需求方先评: 单方提交, 对方看不见")
st, d = R1.review(O1, 5, "交付很干净，启动说明写得比我自己写的还清楚。")
check("需求方评 5 星 200", st == 200, (st, d))
rv = data_of(d).get("review") or {}
check("拿到评价 id", (rv.get("id") or "").startswith("RV"), rv)
check("评分落对", rv.get("rating") == 5, rv)
check("mine=true", rv.get("mine") is True, rv)
check("单方提交后未揭盲", rv.get("revealed") is False, rv)
check("返回里说对方还没提交", data_of(d).get("counterpart_submitted") is False, d)

st, d = R1.reviews(O1)
dd = data_of(d)
check("需求方读评价 200", st == 200, (st, d))
check("自己那条在 mine 里", len(dd.get("mine") or []) == 1, dd)
check("theirs 为空", len(dd.get("theirs") or []) == 0, dd)
check("can_write=false(已经评过)", dd.get("can_write") is False, dd)
check("说了对方还没提交", dd.get("counterpart_submitted") is False, dd)

print("[6] 关键一条: 接单方此时读不到对方打了几分")
st, d = P1.reviews(O1)
dd = data_of(d)
check("接单方读评价 200", st == 200, (st, d))
check("theirs 仍然为空(未揭盲不透内容)", len(dd.get("theirs") or []) == 0, dd)
check("但被告知对方已提交", dd.get("counterpart_submitted") is True, dd)
check("can_write=true(他还没评)", dd.get("can_write") is True, dd)
check("mine 为空", len(dd.get("mine") or []) == 0, dd)
# 订单视图也不该透分数, 只透"对方交没交"
o = P1.find_order(O1)
check("订单视图: 我还没评", (o or {}).get("my_review_done") is False, o)
check("订单视图: 对方已评", (o or {}).get("counterpart_review_done") is True, o)

print("[7] 重复提交只算一次")
st, d = R1.review(O1, 1, "改个一星试试")
check("同一人再评 409", st == 409, (st, d))
check("错误码 ALREADY_REVIEWED", code_of(d) == "ALREADY_REVIEWED", d)

print("[8] 接单方也提交: 两条一起揭盲")
st, d = P1.review(O1, 4, "")   # 故意不写正文, 覆盖 comment 可空/空串的读取路径
check("接单方评 4 星 200(空正文也能提交)", st == 200, (st, d))
rv = data_of(d).get("review") or {}
check("我这回提交就揭盲了", rv.get("revealed") is True, rv)
check("返回里带出对方那条", len(data_of(d).get("theirs") or []) == 1, d)
check("对方那条是已揭盲的", ((data_of(d).get("theirs") or [{}])[0]).get("revealed") is True, d)

st, d = R1.reviews(O1)
dd = data_of(d)
check("需求方现在看到两条", len(dd.get("mine") or []) == 1 and len(dd.get("theirs") or []) == 1, dd)
theirs = (dd.get("theirs") or [{}])[0]
check("看到对方打 4 星", theirs.get("rating") == 4, theirs)
check("空正文能正常读回(不是 NULL 扫描失败)", theirs.get("comment") == "", theirs)

st, d = P1.reviews(O1)
dd = data_of(d)
check("接单方也看到两条", len(dd.get("mine") or []) == 1 and len(dd.get("theirs") or []) == 1, dd)
theirs = (dd.get("theirs") or [{}])[0]
check("看到对方打 5 星", theirs.get("rating") == 5, theirs)
check("正文读得回来", "启动说明" in (theirs.get("comment") or ""), theirs)

o = R1.find_order(O1)
check("订单视图: 双方都评了", (o or {}).get("my_review_done") is True
      and (o or {}).get("counterpart_review_done") is True, o)

print("[9] 评完不改状态: 订单还是已完成, 谁都不用动")
o = R1.find_order(O1)
check("订单仍是 done", (o or {}).get("state") == "done", o)
check("评完没人需要动", (o or {}).get("needs_me") is False, o)

print("[10] 非参与方读评价也不许")
st, d = P2.reviews(O1)
check("别的接单方读评价 403", st == 403, (st, d))
check("错误码 NOT_PARTICIPANT", code_of(d) == "NOT_PARTICIPANT", d)
st, d = ANON.reviews(O1)
check("未登录读评价 401", st == 401, st)

print("")
print("=" * 56)
print("互评与揭盲: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
print("=" * 56)
raise SystemExit(1 if FAIL else 0)
