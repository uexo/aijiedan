"""M2 匹配验收: 发单之后, 需求方能看见"谁能接", 而且每张卡上的理由都指得出凭据。

这条链路要证明六件事:
  1) 谁能看 —— 只有发单的人。别人调这个接口看到的不只是几个名字,
     而是"这单现在有没有人抢", 那是发单方的信息
  2) 状态对不对 —— 已经有人接过、或者被撤回的单不该再推荐人, 推了点了也是 409
  3) 不多不少三个 —— 最多 3 张; 候选不足 3 人时返回实际数量, 不补位、不造假
  4) 谁进得来 —— 有档案、没暂停、有至少一条可指认的凭据。说不出为什么是他的人不进候选池
  5) 理由不编造 —— 每条理由都带锚点, 锚点指回真实存在的东西:
     技能理由指向他档案里真有的标签, 订单理由指向他真完成过的一单
  6) 干完一单, 理由会变多 —— 这条是"理由来自事实"的反向证明:
     同一个人在完成订单前后, 理由条数与内容不同, 而锚点始终指向那笔真实订单

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_matches.py
    BASE=http://127.0.0.1:8082 python test/test_m0_matches.py
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
PASS = FAIL = 0
FAILS = []

CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)
# 重放写请求会拿到这些"状态已经变了"的错误码; 它们不代表业务结论。
REPLAY_CONFLICTS = {
    "TASK_TAKEN", "TASK_NOT_OPEN", "TASK_ALREADY_TAKEN", "TASK_CHANGED",
    "ORDER_NOT_PENDING", "ORDER_NOT_TO_REVIEW", "ORDER_NOT_IN_PROGRESS",
    "ORDER_NOT_CONFIRMED", "REVISION_LIMIT", "CODE_ALREADY_USED", "SLUG_TAKEN",
}


class Client:
    """一个浏览器身份。每个 Client 有自己的 cookie jar。"""

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

    def set_profile(self, headline, skills, availability=1):
        return self.call("PUT", "/api/v2/me/profile", {
            "headline": headline,
            "bio": "做过同类项目, 交付前会先自己跑一遍。",
            "skills": skills,
            "portfolio": [],
            "availability": availability,
        })

    def publish(self, **kw):
        return self.call("POST", "/api/v2/tasks", kw)

    def matches(self, task_id):
        return self.call("GET", "/api/v2/tasks/%s/matches" % task_id)

    def take(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/take" % task_id, {})

    def confirm(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/confirm" % order_id, {})

    def deliver(self, order_id, content):
        return self.call("POST", "/api/v2/orders/%s/deliveries" % order_id, {"content": content})

    def accept(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/accept" % order_id, {})


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


def cards(d):
    return data(d).get("candidates") or []


def login_uid(d):
    """登录响应的 id 在 data.user.id 里。"""
    return (data(d).get("user") or {}).get("id")


def profile_uid(d):
    """建档响应的 user_id 在 data.profile.user_id 里。
    注意它不在顶层: 早先直接从 data 顶层取 user_id 拿到的是 None,
    而 None 会让所有"某人不在候选里"的断言静默通过, 属于测试自己骗自己。"""
    return (data(d).get("profile") or {}).get("user_id")


def by_id(cs, uid):
    if not uid:
        return None
    for c in cs:
        if c.get("user_id") == uid:
            return c
    return None


def reasons_of(card):
    return (card or {}).get("reasons") or []


def anchor_types(card):
    return [((r.get("anchor") or {}).get("type")) for r in reasons_of(card)]


print("[0] 准备身份: 一个发单方, 三个接单方, 外加一个不建档的和一个暂停接单的")

D = Client()   # 发单方
st, d = D.login(new_phone("137"), "demand")
check("需求方 D 登录 200", st == 200, d)

X = Client()   # 另一个发单方: 看别人的单必须被拒
st, d = X.login(new_phone("136"), "demand")
check("需求方 X 登录 200", st == 200, d)

P1 = Client()  # 对得上: python + llm + 客服自动化
st, d = P1.login(new_phone("138"), "supply")
check("接单方 P1 登录 200", st == 200, d)
st, d = P1.set_profile("把重复流程做成能跑起来的 Agent", ["python", "llm", "客服自动化"])
check("P1 建档 200", st == 200, d)
P1_ID = profile_uid(d)

P2 = Client()  # 只有 python 沾边
st, d = P2.login(new_phone("138"), "supply")
st, d = P2.set_profile("数据抓取与清洗, 跑得稳", ["python", "数据抓取", "爬虫"])
check("P2 建档 200", st == 200, d)
P2_ID = profile_uid(d)

P3 = Client()  # 完全不沾边: 只做视觉
st, d = P3.login(new_phone("138"), "supply")
st, d = P3.set_profile("只做视觉设计, 不写代码", ["海报设计", "插画", "logo"])
check("P3 建档 200", st == 200, d)
P3_ID = profile_uid(d)

P4 = Client()  # 登录了但没建档
st, d = P4.login(new_phone("138"), "supply")
check("P4 登录 200 但不建档", st == 200, d)
P4_ID = login_uid(d)

P5 = Client()  # 档案完整但明确暂停接单
st, d = P5.login(new_phone("138"), "supply")
st, d = P5.set_profile("暂时不接单", ["python", "客服自动化", "流程梳理"], availability=0)
check("P5 建档并暂停接单 200", st == 200, d)
P5_ID = profile_uid(d)

ANON = Client()

SEVEN_IDS = [P1_ID, P2_ID, P3_ID, P4_ID, P5_ID]
check("五个接单方的 user_id 都拿到了", all(SEVEN_IDS), SEVEN_IDS)
check("五个接单方是五个不同的人", len(set(SEVEN_IDS)) == 5, SEVEN_IDS)

print("[1] 发一单, 标题和说明都指向 AI 客服方向")

TASK = {
    "title": "客服退款审批 Agent",
    "category": "nlp",
    "spec": "把客服的退款审批做成一个能自动跑的 Agent, 输入订单号判断该退不该退, 用 Python 写, "
            "接进我们现有的客服系统。",
    "budget_cents": 300000,
}
st, d = D.publish(**TASK)
check("D 发单 201", st == 201, d)
T1 = data(d).get("id")
check("拿到任务号", bool(T1), d)

print("[2] 谁能看: 未登录、别人的单、不存在的单")

st, d = ANON.matches(T1)
check("未登录 401", st == 401, d)

st, d = X.matches(T1)
check("别人的单 403", st == 403, d)
check("错误码是 NOT_TASK_OWNER", errcode(d) == "NOT_TASK_OWNER", d)

st, d = D.matches("T-NOT-EXIST")
check("不存在的单 404", st == 404, d)
check("错误码是 TASK_NOT_FOUND", errcode(d) == "TASK_NOT_FOUND", d)

print("[3] 正常返回: 最多三张卡, 每张都有可点开的理由")

st, d = D.matches(T1)
check("发单本人 200", st == 200, d)
cs = cards(d)
check("返回了候选人", len(cs) > 0, d)
check("最多 3 张", len(cs) <= 3, len(cs))
check("count 与列表长度一致", data(d).get("count") == len(cs), data(d).get("count"))
check("task_id 回填正确", data(d).get("task_id") == T1, data(d).get("task_id"))
check("每人都有 match_id", all(c.get("match_id") for c in cs), cs)
check("每人都有理由", all(len(reasons_of(c)) > 0 for c in cs), cs)
check("每人理由不超过 3 条", all(len(reasons_of(c)) <= 3 for c in cs), cs)
scores = [c.get("score") for c in cs]
check("按匹配分降序", scores == sorted(scores, reverse=True), scores)

BAD_ANCHORS = {"", None}
check("每条理由都有锚点类型",
      all((r.get("anchor") or {}).get("type") not in BAD_ANCHORS for c in cs for r in reasons_of(c)), cs)
check("每条理由的锚点都指得出具体对象",
      all((r.get("anchor") or {}).get("ref") for c in cs for r in reasons_of(c)), cs)
check("锚点类型在约定集合内",
      all((r.get("anchor") or {}).get("type") in ("skill", "order", "endorsement")
          for c in cs for r in reasons_of(c)), cs)
check("每条理由都有展示文案",
      all(r.get("text") for c in cs for r in reasons_of(c)), cs)

print("[4] 谁进得来: 无关技能、没档案、暂停接单的都该被挡在外面")

check("P3(纯视觉) 不进候选", by_id(cs, P3_ID) is None, [c.get("name") for c in cs])
check("P4(没档案) 不进候选", by_id(cs, P4_ID) is None, [c.get("name") for c in cs])
check("P5(暂停接单) 不进候选", by_id(cs, P5_ID) is None, [c.get("name") for c in cs])
check("对得上的 P1 在候选里", by_id(cs, P1_ID) is not None, [c.get("user_id") for c in cs])
check("只有 python 沾边的 P2 也在候选里", by_id(cs, P2_ID) is not None, [c.get("user_id") for c in cs])

print("[5] 理由不编造: 还没干过单的人, 不该有订单锚点的理由")

for c in cs:
    types = anchor_types(c)
    check("新人 %s 没有 order 理由" % c.get("name"), "order" not in types, types)
    check("新人 %s 没有 endorsement 理由" % c.get("name"), "endorsement" not in types, types)
    check("新人 %s 的 stats.orders 为 0" % c.get("name"), (c.get("stats") or {}).get("orders") == 0, c.get("stats"))

p1_card = by_id(cs, P1_ID)
check("P1 的理由是技能锚点", "skill" in anchor_types(p1_card), anchor_types(p1_card))
p1_skill_refs = [(r.get("anchor") or {}).get("ref", "") for r in reasons_of(p1_card)]
check("技能锚点指向真实技能标签",
      all(ref.lower() in ("skill:python", "skill:llm", "skill:客服自动化") for ref in p1_skill_refs), p1_skill_refs)
check("P1 卡上有他的技能清单", set(["python", "llm", "客服自动化"]).issubset(set(p1_card.get("skills") or [])),
      p1_card.get("skills"))
p1_score_before = p1_card.get("score")

print("[6] 幂等: 刷新两次, 还是同一批推荐记录")

st, d2 = D.matches(T1)
cs2 = cards(d2)
same_ids = [c.get("match_id") for c in cs] == [c.get("match_id") for c in cs2]
check("两次刷新 match_id 不变", same_ids,
      ([c.get("match_id") for c in cs], [c.get("match_id") for c in cs2]))
check("两次刷新人数不变", len(cs) == len(cs2), (len(cs), len(cs2)))

print("[7] 让 P1 真干完一单, 再回来看他的理由有没有变多")

st, d = D.publish(title="整理客服聊天记录成周报", category="data",
                  spec="把客服系统导出的聊天记录按问题类型归类, 每周出一份 Excel 汇总, 用 Python 做。",
                  budget_cents=120000)
check("D 发第二单 201", st == 201, d)
T2 = data(d).get("id")

st, d = P1.take(T2)
check("P1 认领 201", st == 201, d)
O2 = data(d).get("id")
check("拿到订单号", bool(O2), d)

st, d = D.confirm(O2)
check("D 确认开工 200", st == 200, d)

st, d = P1.deliver(O2, "脚本已交付, 附一份使用说明和上个月的样例报表。")
check("P1 交付 201", st == 201, d)

st, d = D.accept(O2)
check("D 验收通过 200", st == 200, d)
check("订单已完成", data(d).get("status") == "completed", data(d).get("status"))

st, d = D.matches(T1)
cs3 = cards(d)
p1_after = by_id(cs3, P1_ID)
check("P1 还在候选里", p1_after is not None, [c.get("user_id") for c in cs3])
check("P1 的 stats.orders 变成 1", (p1_after or {}).get("stats", {}).get("orders") == 1,
      (p1_after or {}).get("stats"))
check("P1 多了一条 order 理由", "order" in anchor_types(p1_after), anchor_types(p1_after))
order_reason = [r for r in reasons_of(p1_after) if (r.get("anchor") or {}).get("type") == "order"]
check("order 理由的锚点是那笔真实订单",
      bool(order_reason) and order_reason[0]["anchor"]["ref"] == O2,
      order_reason)
check("order 理由的文案里提到这一单",
      bool(order_reason) and "整理客服聊天记录成周报"[:8] in order_reason[0]["text"],
      order_reason)
check("P1 的理由变多了", len(reasons_of(p1_after)) > len(reasons_of(p1_card)),
      (len(reasons_of(p1_card)), len(reasons_of(p1_after))))
# 历史因子只看互评均值(AvgRating/5*20), 而互评要到 M4 才产生。
# 所以这一单干完之后分数应当纹丝不动。如果它动了, 说明有人拿"完成单数"冒充评分——
# 那正是要防的: 分数涨了, 卡上的理由却写不出"客户给了几星"。
check("干完一单后分数暂不变(互评在 M4 才产生)", (p1_after or {}).get("score") == p1_score_before,
      (p1_score_before, (p1_after or {}).get("score")))
check("理由仍然不超过 3 条", len(reasons_of(p1_after)) <= 3, len(reasons_of(p1_after)))
check("没有凭空的 endorsement 理由", "endorsement" not in anchor_types(p1_after), anchor_types(p1_after))

print("[8] 状态: 单子被别人接走之后, 不该再推荐人")

st, d = P2.take(T1)
check("P2 认领 T1 201", st == 201, d)

st, d = D.matches(T1)
check("已有人接的单返 409", st == 409, d)
check("错误码是 TASK_NOT_OPEN", errcode(d) == "TASK_NOT_OPEN", d)

st, d = D.publish(title="给公众号写一组产品文案", category="copywriting",
                  spec="新品上市, 需要三篇公众号推文, 每篇八百字左右, 语气不要太硬。", budget_cents=50000)
T3 = data(d).get("id")
st, d = D.call("POST", "/api/v2/tasks/%s/withdraw" % T3, {})
check("撤回 T3 200", st == 200, d)
st, d = D.matches(T3)
check("撤回的单也返 409", st == 409, d)

print()
print("=" * 60)
print("通过 %d, 失败 %d" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
print("=" * 60)
raise SystemExit(1 if FAIL else 0)
