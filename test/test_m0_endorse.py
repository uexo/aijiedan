"""M0 背书: 一单做完之后, 客户给接单方写一段具名的话。

这一条要证明的不是"能存一条文本", 而是"信用资产真的产生了":
  - 谁能写 —— 只有这一单的双方; 路人、别的接单方都不行
  - 什么时候能写 —— 只有已完成; 没验收的单不能写(背书是给做完的活写的)
  - 多长 —— 12 字是拒收线, 11 字拒、12 字收; 去空白后算
  - 三问 —— 至少答两问; 只答一问不算
  - 一人一单一条 —— 重复提交是拒收, 不是覆盖(原文不可改)
  - 只能追加 —— 原作者能加, 路人不能; 追加不可编辑
  - 权重 —— 同一对用户 12 个月内只有第一条计权重, 第二条留记录但不出现
  - 进推荐理由 —— 匹配理由里出现 endorsement 锚点, 且 ref 指回真实背书 ID

最后一条是这一整个功能的意义所在: 背书不进理由, 它就只是库里的文本。

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_endorse.py
    BASE=http://127.0.0.1:8080 python test/test_m0_endorse.py
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
T_MAIN = "T-DEMO-001"   # R001 的单, 走完全程再写背书
T_PENDING = "T-DEMO-002"  # R002 的单, 只交付不验收, 用来测"没走完不能写"

PASS = FAIL = 0
FAILS = []

CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)
REPLAY_CONFLICTS = {
    "TASK_TAKEN", "TASK_NOT_OPEN", "TASK_ALREADY_TAKEN", "TASK_CHANGED",
    "ORDER_NOT_PENDING", "ORDER_NOT_TO_REVIEW", "ORDER_NOT_IN_PROGRESS",
    "REVISION_LIMIT", "ALREADY_REVIEWED", "DUPLICATE_ENDORSEMENT", "CODE_ALREADY_USED",
    "SLUG_TAKEN",
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

    def publish(self, **kw):
        return self.call("POST", "/api/v2/tasks", kw)

    def claim(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/take" % task_id, {})

    def confirm(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/confirm" % order_id, {})

    def deliver(self, order_id, content, items=None):
        return self.call("POST", "/api/v2/orders/%s/deliveries" % order_id,
                         {"content": content, "items": items or []})

    def accept(self, order_id):
        return self.call("POST", "/api/v2/orders/%s/accept" % order_id, {})

    def invite(self, order_id):
        return self.call("GET", "/api/v2/orders/%s/endorsement" % order_id)

    def endorse(self, order_id, body, answers):
        return self.call("POST", "/api/v2/orders/%s/endorsement" % order_id,
                         {"body": body, "answers": answers})

    def append(self, endorsement_id, body):
        return self.call("POST", "/api/v2/endorsements/%s/appends" % endorsement_id, {"body": body})

    def mine(self):
        return self.call("GET", "/api/v2/me/endorsements")


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


def data_of(d):
    return d.get("data") or {}


def make_provider(headline):
    c = Client()
    st, d = c.login(new_phone("157"), "supply")
    assert st == 200, st
    # 登录返回的 user.id 在 data.user 里, 不在顶层。取错层会拿到 None,
    # 而"某人不在候选里"这类断言遇到 None 会恒真。
    uid = ((d.get("data") or {}).get("user") or {}).get("id")
    assert uid, d
    st, d = c.profile(headline)
    assert st == 200, d
    return c, uid


def valid_task(**over):
    base = {
        "title": "做一份抖音选品分析脚本",
        "category": "data",
        "spec": "店铺做茶叶，抖音小店每天导出订单表。想要一个脚本读表，算出哪几个 SKU 动销最快、"
                "哪些在压货，输出一张周报。跑在我自己的 Windows 机器上，不联网也行。",
        "budget_cents": 80000,
        "deliverables": "1 个 Python 脚本 + 一份使用说明",
        "required_format": "py 文件 + md",
        "items": ["能读抖音导出的 xlsx", "算出动销排名", "输出周报 markdown"],
    }
    base.update(over)
    return base


print("[0] 准备身份")
P1, P1_ID = make_provider("把重复流程做成能跑的 Agent")
P2, P2_ID = make_provider("只做能上线的页面前端")
check("两个接单方 ID 非空且不同", bool(P1_ID) and bool(P2_ID) and P1_ID != P2_ID, (P1_ID, P2_ID))
ANON = Client()

R1 = Client()
st, d = R1.login(OWNER_PHONE["r001"], "demand")
check("需求方一号能登录", st == 200, d)
R2 = Client()
st, d = R2.login(OWNER_PHONE["r002"], "demand")
check("需求方二号能登录", st == 200, d)

print("[1] 两张单: 一张走完, 一张只交付不验收")
st, d = P1.claim(T_MAIN)
check("P1 认领主单 201", st == 201, (st, d))
O1 = data_of(d).get("id") or ""
st, d = P2.claim(T_PENDING)
check("P2 认领待验收单 201", st == 201, (st, d))
O2 = data_of(d).get("id") or ""
check("两个订单号都拿到了", all(x.startswith("O-") for x in (O1, O2)), (O1, O2))
check("两个订单号不相同", O1 != O2, (O1, O2))

st, d = R2.confirm(O2)
check("R2 确认待验收单", st == 200, (st, d))
st, d = P2.deliver(O2, "只交了一版，还没验收")
check("P2 交付但不验收", st == 201, (st, d))

st, d = R1.confirm(O1)
check("R1 确认主单", st == 200, (st, d))
st, d = P1.deliver(O1, "做完了，启动脚本和说明都放进链接了", items=items_all_done())
check("P1 交付主单", st == 201, (st, d))

print("[2] 未验收的单: 邀请详情说清为什么不能写")
st, d = R1.invite(O2)
# R1 不是 O2 的当事人, 先测路人; 下面用 R2 测"单没走完"
check("非当事人读邀请详情 403", st == 403 and code_of(d) == "NOT_PARTICIPANT", (st, d))

st, d = R2.invite(O2)
check("当事人能读邀请详情 200", st == 200, (st, d))
inv = data_of(d)
check("没验收时 can_write=false", inv.get("can_write") is False, inv.get("can_write"))
check("没验收时给出原因", "验收" in (inv.get("why_not") or ""), inv.get("why_not"))
check("邀请详情带三问", len(inv.get("questions") or []) == 3, inv.get("questions"))
check("门槛写的是 12 字", inv.get("min_chars") == 12, inv.get("min_chars"))
check("至少答两问", inv.get("answers_needed") == 2, inv.get("answers_needed"))

st, d = R2.endorse(O2, "这个脚本能读抖音导出的表", {"delivered": "对得上", "again": "会"})
check("没验收就写背书 409", st == 409, (st, d))
check("错误码 ORDER_NOT_COMPLETED", code_of(d) == "ORDER_NOT_COMPLETED", d)

print("[3] 验收通过之后才能写")
st, d = R1.accept(O1)
check("R1 验收主单", st == 200, (st, d))

st, d = R1.invite(O1)
inv = data_of(d)
check("验收后 can_write=true", inv.get("can_write") is True, inv)
check("验收前 mine 为空", inv.get("mine") is None, inv.get("mine"))
check("邀请详情写明被背书人", inv.get("target_name") not in (None, ""), inv.get("target_name"))

st, d = ANON.invite(O1)
check("未登录读邀请详情 401", st == 401, (st, d))

print("[4] 路人不能写")
st, d = P2.endorse(O1, "这个脚本能读抖音导出的表", {"delivered": "对得上", "again": "会"})
check("别的接单方写背书 403", st == 403 and code_of(d) == "NOT_PARTICIPANT", (st, d))

print("[5] 12 字是拒收线, 不是建议")
st, d = R1.endorse(O1, "这个脚本能读抖音导的表", {"delivered": "对得上", "again": "会"})
check("11 字被拒 409", st == 409, (st, d))
check("错误码 ENDORSEMENT_TOO_SHORT", code_of(d) == "ENDORSEMENT_TOO_SHORT", d)
st, d = R1.endorse(O1, "   这个脚本能读抖音导的表   ", {"delivered": "对得上", "again": "会"})
check("11 字加空格仍被拒(去空白后算)", st == 409 and code_of(d) == "ENDORSEMENT_TOO_SHORT", (st, d))

# 三问在这里测: 必须赶在成功写入之前。写成功之后同一单再来,
# 会先被 DUPLICATE_ENDORSEMENT 挡掉——判断顺序是"资格→状态→内容",
# 查重属于状态, 内容排在最后。
st, d = R1.endorse(O1, "这个脚本能读抖音导出的表格", {"delivered": "对得上"})
check("正文够了但只答一问 409", st == 409, (st, d))
check("错误码 ENDORSEMENT_ANSWER_MISSING", code_of(d) == "ENDORSEMENT_ANSWER_MISSING", d)
st, d = R1.endorse(O1, "这个脚本能读抖音导出的表格", {"delivered": "  ", "again": "  "})
check("三问全是空白等于一题没答", st == 409 and code_of(d) == "ENDORSEMENT_ANSWER_MISSING", (st, d))

st, d = R1.endorse(O1, "这个脚本能读抖音导出的表",
                   {"delivered": "对得上", "again": "会", "nonsense": "不该入库"})
check("12 字正好通过 201", st == 201, (st, d))
E1 = data_of(d).get("id") or ""
check("拿到背书号且以 EN 开头", E1.startswith("EN"), E1)
check("char_count 入库时算好", data_of(d).get("char_count") == 12, data_of(d).get("char_count"))
check("多余 key 不入库", "nonsense" not in (data_of(d).get("answers") or {}), data_of(d).get("answers"))

print("[7] 一人一单一条, 重复是拒收不是覆盖")
st, d = R1.endorse(O1, "这个脚本能读抖音导出的表", {"delivered": "对得上", "again": "会"})
check("重复写 409", st == 409, (st, d))
check("错误码 DUPLICATE_ENDORSEMENT", code_of(d) == "DUPLICATE_ENDORSEMENT", d)

st, d = R1.invite(O1)
inv = data_of(d)
check("写完之后 can_write=false", inv.get("can_write") is False, inv.get("can_write"))
check("写完之后 mine 有内容", (inv.get("mine") or {}).get("id") == E1, inv.get("mine"))
check("为什么不能写: 指向追加", "追加" in (inv.get("why_not") or ""), inv.get("why_not"))
check("邀请详情能读回三问答案", (inv.get("mine") or {}).get("answers", {}).get("delivered") == "对得上",
      (inv.get("mine") or {}).get("answers"))

print("[8] 只能追加, 不能编辑")
st, d = R1.append(E1, "补一句：周报也一起给了，我自己改了两行就能用。")
check("原作者追加 201", st == 201, (st, d))
A1 = data_of(d).get("id") or ""
check("追加号以 EA 开头", A1.startswith("EA"), A1)

st, d = P1.append(E1, "我来给自己补一句好话")
check("被背书人不能追加 403", st == 403 and code_of(d) == "NOT_ENDORSEMENT_AUTHOR", (st, d))

st, d = ANON.append(E1, "路人补一句")
check("未登录不能追加 401", st == 401, (st, d))

st, d = R1.append(E1, "超" * 501)
check("追加 501 字被拒 400", st == 400 and code_of(d) == "ENDORSEMENT_APPEND_TOO_LONG", (st, d))

st, d = R1.append(E1, "   ")
check("空追加被拒 400", st == 400, (st, d))
check("空追加报的是自己的码, 不是「背书至少 12 字」",
      code_of(d) == "ENDORSEMENT_APPEND_EMPTY", code_of(d))

st, d = R1.invite(O1)
ap = (data_of(d).get("mine") or {}).get("appends") or []
check("追加能读回来", len(ap) == 1 and ap[0].get("id") == A1, ap)

st, d = R1.append("EN-NOT-EXIST", "给一条不存在的背书追加")
check("给不存在的背书追加 404", st == 404 and code_of(d) == "ENDORSEMENT_NOT_FOUND", (st, d))

print("[9] 接单方也能反向写, 关系不同")
st, d = P1.invite(O1)
inv = data_of(d)
check("接单方读到的是 peer 关系", inv.get("relation") == "peer", inv.get("relation"))
st, d = P1.endorse(O1, "需求方的验收意见写得很具体，改一轮就过了",
                   {"delivered": "对得上", "again": "会"})
check("接单方写背书 201", st == 201, (st, d))
check("这一条的关系是 peer", data_of(d).get("relation") == "peer", data_of(d).get("relation"))

print("[10] 我收到的 / 我写出去的")
st, d = R1.mine()
check("需求方读自己的背书 200", st == 200, (st, d))
mv = data_of(d)
check("需求方写出去 1 条", len(mv.get("given") or []) == 1, mv.get("given"))
check("需求方收到 1 条(接单方反向写的)", len(mv.get("received") or []) == 1, mv.get("received"))

st, d = P1.mine()
check("接单方读自己的背书 200", st == 200, (st, d))
mv = data_of(d)
check("接单方收到 1 条", len(mv.get("received") or []) == 1, len(mv.get("received") or []))
check("接单方收到的是 client 关系",
      (mv.get("received") or [{}])[0].get("relation") == "client",
      (mv.get("received") or [{}])[0].get("relation"))

print("[11] 同一对用户第二单: 记录留着, 权重为 0")
st, d = R1.publish(**valid_task(title="再做一份月度复盘脚本"))
check("需求方发第二单 201", st == 201, (st, d))
T2 = data_of(d).get("id") or ""
st, d = P1.claim(T2)
check("P1 接第二单 201", st == 201, (st, d))
O3 = data_of(d).get("id") or ""
check("第二单订单号与第一单不同", O3 != O1, (O1, O3))
st, d = R1.confirm(O3)
check("确认第二单", st == 200, (st, d))
st, d = P1.deliver(O3, "复盘脚本给你了，按月汇总", items=items_all_done())
check("交付第二单", st == 201, (st, d))
st, d = R1.accept(O3)
check("验收第二单", st == 200, (st, d))

st, d = R1.endorse(O3, "第二单也做完了，脚本能直接跑", {"delivered": "对得上", "again": "会"})
check("第二单也能写背书 201", st == 201, (st, d))
E2 = data_of(d).get("id") or ""
check("两条背书号不同", E2 != E1 and E2.startswith("EN"), (E1, E2))

st, d = P1.mine()
mv = data_of(d)
check("12 个月内第二条不计权重: 收到仍是 1 条", len(mv.get("received") or []) == 1,
      len(mv.get("received") or []))
check("计权重的那条是第一条", (mv.get("received") or [{}])[0].get("id") == E1,
      (mv.get("received") or [{}])[0].get("id"))

st, d = R1.mine()
mv = data_of(d)
check("写出去的两条都在(记录不丢)", len(mv.get("given") or []) == 2, len(mv.get("given") or []))

print("[12] 背书进推荐理由 —— 不进理由, 它就只是库里的文本")
st, d = R1.publish(**valid_task(title="第三单：把周报接到飞书上"))
check("需求方发第三单 201", st == 201, (st, d))
T3 = data_of(d).get("id") or ""
check("第三单号非空且与前两单不同", T3.startswith("T-") and T3 not in (T2,), T3)

st, d = R1.call("GET", "/api/v2/tasks/%s/matches" % T3)
check("发单方能看到谁能接 200", st == 200, (st, d))
cards = data_of(d).get("candidates") or []
check("候选不为空", len(cards) > 0, cards)

target = next((c for c in cards if c.get("user_id") == P1_ID), None)
check("P1 出现在候选里", target is not None, [c.get("user_id") for c in cards])
reasons = (target or {}).get("reasons") or []
anchors = [(r.get("anchor") or {}).get("type") for r in reasons]
check("理由里有 endorsement 锚点", "endorsement" in anchors, anchors)

er = next((r for r in reasons if (r.get("anchor") or {}).get("type") == "endorsement"), {})
check("锚点 ref 指回真实背书 ID", (er.get("anchor") or {}).get("ref") == E1, er)
check("理由里引了客户原话", "这个脚本能读" in (er.get("text") or ""), er.get("text"))
check("理由写明是谁写的", "客户" in (er.get("text") or ""), er.get("text"))

pe = (target or {}).get("primary_endorsement") or {}
check("候选卡上摆出背书", pe.get("id") == E1, pe)
check("背书署名不为空", pe.get("author_name") not in (None, ""), pe)
check("背书摘录不为空", (pe.get("excerpt") or "") != "", pe)
# 零权重那条不能顶掉计权重的那条: 这是"12 个月只计一次"在推荐侧的落地
check("零权重的第二条没有顶掉第一条", pe.get("id") != E2, (pe.get("id"), E2))

print("")
print("=" * 56)
print("背书: %d 通过, %d 失败" % (PASS, FAIL))
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
print("=" * 56)
raise SystemExit(1 if FAIL else 0)
