"""M0 发单验收: 需求方自己发一条需求, 它出现在大厅, 别人能接。

这条链路要证明五件事:
  1) 谁能发 —— 登录就能发, 不要求填档案(按 PRD 只对供给方强制)
  2) 必填四项拦得住 —— 标题、类目、说明、预算, 缺一样都发不出去
  3) 边界值得守住 —— 4 字标题、1 元预算、上限、字段长度都是列宽问题不是审美问题
  4) 发出去的单真的在大厅里, 而且"我发的需求"里能找回来
  5) 自己发的单自己接不了, 另一个人接得到 —— 这才叫发单成功

只依赖标准库, 内存模式与 MySQL 模式都能跑:
    python test/test_m0_publish.py
    BASE=http://127.0.0.1:8082 python test/test_m0_publish.py
"""
import http.cookiejar
import json
import os
import random
import urllib.error
import urllib.request
from urllib.parse import quote

BASE = os.environ.get("BASE", "http://127.0.0.1:8082")
PASS = FAIL = 0
FAILS = []


class Client:
    """一个浏览器身份。每个 Client 有自己的 cookie jar。"""

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


# 一份各项都合法的基线, 各用例只改一处, 失败时能确定是改的那一处引起的
def valid(**over):
    base = {
        "title": "给茶叶店做一个抖音选品分析脚本",
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


print("[0] 准备三个身份")

D = Client()  # 需求方: 发单的人
PHONE_D = new_phone("137")
st, d = D.login(PHONE_D, "demand")
check("需求方 D 登录 200", st == 200, d)
check("D 角色为 requester", (data(d).get("user") or {}).get("role") == "requester", d)
check("D 未建档也能进(发单不要求档案)", (data(d).get("user") or {}).get("onboarded") is False, d)

P = Client()  # 接单方: 待会儿来接 D 发的单
PHONE_P = new_phone("138")
st, d = P.login(PHONE_P, "supply")
check("接单方 P 登录 200", st == 200, d)
st, d = P.profile()
check("P 保存档案 200", st == 200, d)

X = Client()  # 路人需求方: 验证"别人的单我看得到, 但不在我的需求里"
st, d = X.login(new_phone("136"), "demand")
check("路人 X 登录 200", st == 200, d)

print("[1] 未登录不能发单")
N = Client()
st, d = N.publish(**valid())
check("未登录发单 401", st == 401, (st, d))

print("[2] 必填四项：标题、类目、说明、预算")

st, d = D.publish(**valid(title="做图"))
check("标题 2 字被拒 400", st == 400, (st, d))
check("错误码 TITLE_TOO_SHORT", errcode(d) == "TITLE_TOO_SHORT", d)

st, d = D.publish(**valid(title="   "))
check("标题只有空格被拒", st == 400 and errcode(d) == "TITLE_TOO_SHORT", (st, d))

st, d = D.publish(**valid(title="茶叶选品"))
check("标题正好 4 字通过", st == 201, (st, d))

st, d = D.publish(**valid(title="标" * 61))
check("标题 61 字被拒 400", st == 400, (st, d))
check("错误码 TITLE_TOO_LONG", errcode(d) == "TITLE_TOO_LONG", d)

st, d = D.publish(**valid(title="标" * 60))
check("标题正好 60 字通过", st == 201, (st, d))

st, d = D.publish(**valid(category=""))
check("类目为空被拒 400", st == 400, (st, d))
check("错误码 CATEGORY_INVALID", errcode(d) == "CATEGORY_INVALID", d)

st, d = D.publish(**valid(category="占星"))
check("类目不在清单被拒", st == 400 and errcode(d) == "CATEGORY_INVALID", (st, d))

st, d = D.publish(**valid(spec="做个脚本"))
check("说明太短被拒 400", st == 400, (st, d))
check("错误码 SPEC_TOO_SHORT", errcode(d) == "SPEC_TOO_SHORT", d)

st, d = D.publish(**valid(spec="说" * 19))
check("说明 19 字被拒", st == 400 and errcode(d) == "SPEC_TOO_SHORT", (st, d))

st, d = D.publish(**valid(spec="说" * 2001))
check("说明 2001 字被拒 400", st == 400, (st, d))
check("错误码 SPEC_TOO_LONG", errcode(d) == "SPEC_TOO_LONG", d)

st, d = D.publish(**valid(budget_cents=0))
check("预算 0 被拒 400", st == 400, (st, d))
check("错误码 BUDGET_TOO_LOW", errcode(d) == "BUDGET_TOO_LOW", d)

st, d = D.publish(**valid(budget_cents=-100))
check("预算负数被拒", st == 400 and errcode(d) == "BUDGET_TOO_LOW", (st, d))

st, d = D.publish(**valid(budget_cents=99))
check("预算 0.99 元被拒(不足 1 元)", st == 400 and errcode(d) == "BUDGET_TOO_LOW", (st, d))

st, d = D.publish(**valid(budget_cents=100, title="一元钱的需求测试一下"))
check("预算正好 1 元通过", st == 201, (st, d))

st, d = D.publish(**valid(budget_cents=100000001))
check("预算超上限被拒 400", st == 400, (st, d))
check("错误码 BUDGET_TOO_HIGH", errcode(d) == "BUDGET_TOO_HIGH", d)

st, d = D.publish(**valid(budget_cents=100000000, title="上限金额的需求测试"))
check("预算正好到上限通过", st == 201, (st, d))

print("[3] 截止时间只接受空或将来")
st, d = D.publish(**valid(deadline_at=1000000000000))
check("截止时间在过去被拒 400", st == 400, (st, d))
check("错误码 DEADLINE_IN_PAST", errcode(d) == "DEADLINE_IN_PAST", d)

st, d = D.publish(**valid(deadline_at=0, title="没有截止时间的需求单"))
check("不填截止时间可以发", st == 201, (st, d))

print("[4] 长字段按列宽拦下来（超长写不进库，静默成功比报错更糟）")
st, d = D.publish(**valid(deliverables="交" * 81))
check("交付物 81 字被拒 400", st == 400, (st, d))
check("错误码 FIELD_TOO_LONG", errcode(d) == "FIELD_TOO_LONG", d)

st, d = D.publish(**valid(required_format="格" * 41))
check("交付格式 41 字被拒", st == 400 and errcode(d) == "FIELD_TOO_LONG", (st, d))

st, d = D.publish(**valid(items=["项" * 101]))
check("需求项 101 字被拒", st == 400 and errcode(d) == "FIELD_TOO_LONG", (st, d))

st, d = D.publish(**valid(items=["第%d项" % i for i in range(13)]))
check("需求项 13 条被拒 400", st == 400, (st, d))
check("错误码 ITEM_TOO_MANY", errcode(d) == "ITEM_TOO_MANY", d)

print("[5] 正常发单")

st, d = D.publish(**valid(title="抖音选品分析脚本（发单验收用）"))
check("发单成功 201", st == 201, (st, d))
v = data(d)
TID = v.get("id", "")
check("返回单号 T 开头", TID.startswith("T"), TID)
check("状态为 open", v.get("status") == "open", v)
check("标题原样回读", v.get("title") == "抖音选品分析脚本（发单验收用）", v.get("title"))
check("预算以分为单位回读", v.get("budget_cents") == 80000, v.get("budget_cents"))
check("类目回读", v.get("category") == "data", v.get("category"))
check("交付物回读", v.get("deliverables") == "1 个 Python 脚本 + 一份使用说明", v.get("deliverables"))
check("交付格式回读", v.get("required_format") == "py 文件 + md", v.get("required_format"))
check("创建时间已填", (v.get("created_at") or 0) > 0, v.get("created_at"))
check("三项清单都在", len(v.get("items") or []) == 3, v.get("items"))
check("清单编号 R1/R2/R3", [i.get("id") for i in (v.get("items") or [])] == ["R1", "R2", "R3"], v.get("items"))
check("清单描述按输入顺序", (v.get("items") or [{}])[0].get("desc") == "能读抖音导出的 xlsx", v.get("items"))

print("[6] 澄清提示：缺的是提示不是拒稿")
st, d = D.publish(**{
    "title": "只要四项必填的最小需求",
    "category": "other",
    "spec": "这一条只填了必填的四项，没有交付物形态、没有交付格式、也没有截止时间，看看系统怎么说。",
    "budget_cents": 50000,
})
check("只填必填四项也能发出去", st == 201, (st, d))
clar = data(d).get("clarifications") or []
check("返回了 4 条澄清提示", len(clar) == 4, clar)
check("提示里点到交付物", any("交付物" in c for c in clar), clar)
check("提示里点到交付格式", any("格式" in c for c in clar), clar)
check("提示里点到截止", any("截止" in c for c in clar), clar)
check("提示里点到风格", any("风格" in c for c in clar), clar)

st, d = D.publish(**valid(
    title="四项必填加全部建议项都填齐",
    style_ref="参考某某茶具品牌的周报样式",
))
check("建议项填齐后照样发出去", st == 201, (st, d))
check("只差截止时间就只剩一条提示", len(data(d).get("clarifications") or []) == 1, data(d).get("clarifications"))

print("[7] 发出去的单在大厅里")

st, d = D.call("GET", "/api/v2/tasks?limit=50&sort=newest")
ids = [t.get("id") for t in (data(d).get("items") or [])]
check("大厅列表 200", st == 200, st)
check("刚发的单出现在大厅", TID in ids, ids[:5])
if TID in ids:
    me = [t for t in data(d)["items"] if t.get("id") == TID][0]
    check("大厅里预算显示为分", me.get("budget_cents") == 80000, me.get("budget_cents"))
    check("大厅里带类目中文名", me.get("category_name") == "数据/分析", me.get("category_name"))
    check("大厅卡片不出需求方 id", "requester_id" not in me, list(me.keys()))

st, d = D.call("GET", "/api/v2/tasks?q=" + quote("抖音选品") + "&limit=20")
ids = [t.get("id") for t in (data(d).get("items") or [])]
check("能按标题关键词搜到自己发的单", TID in ids, ids)

st, d = D.call("GET", "/api/v2/tasks?q=" + quote("茶") + "&limit=20")
check("单字搜索也能命中(子串匹配)", st == 200 and len(data(d).get("items") or []) > 0, st)

print("[8] 我发的需求")

st, d = D.call("GET", "/api/v2/me/tasks")
items = data(d).get("items") or []
ids = [t.get("id") for t in items]
check("我的需求 200", st == 200, st)
check("刚发的单在「我发的需求」里", TID in ids, ids[:5])
if TID in ids:
    me = [t for t in items if t.get("id") == TID][0]
    check("状态标签是「开放中」", me.get("state_label") == "开放中", me.get("state_label"))
    check("清单条数回读为 3", me.get("item_count") == 3, me.get("item_count"))
    check("我的需求不出 requester_id", "requester_id" not in me, list(me.keys()))

st, d = X.call("GET", "/api/v2/me/tasks")
xids = [t.get("id") for t in (data(d).get("items") or [])]
check("别人的需求不出现在我这里", TID not in xids, xids[:5])

st, d = N.call("GET", "/api/v2/me/tasks")
check("未登录看我的需求 401", st == 401, (st, d))

print("[9] 自己发的单自己接不了，别人接得到")

st, d = D.call("POST", "/api/v2/tasks/%s/take" % TID, {})
check("需求方角色接单被拒 403", st == 403, (st, d))
check("错误码 ROLE_NOT_PROVIDER", errcode(d) == "ROLE_NOT_PROVIDER", d)

P2 = Client()  # 第二个接单方, 用来验证"先到先得"
st, d = P2.login(new_phone("135"), "supply")
check("接单方 P2 登录 200", st == 200, d)
st, d = P2.profile()
check("P2 保存档案 200", st == 200, d)

st, d = P.call("POST", "/api/v2/tasks/%s/take" % TID, {})
check("接单方 P 接得到这条单", st == 201, (st, d))
oid = data(d).get("id", "")
check("认领后生成订单", oid != "", oid)
check("认领后订单四态为待确认", data(d).get("state") == "to_confirm", data(d).get("state"))

st, d = P2.call("POST", "/api/v2/tasks/%s/take" % TID, {})
check("后来的人再接被拒 409", st == 409, (st, d))
check("错误码 TASK_TAKEN", errcode(d) == "TASK_TAKEN", d)

st, d = D.call("GET", "/api/v2/me/tasks")
me = [t for t in (data(d).get("items") or []) if t.get("id") == TID]
check("被人接走后状态变「已有人接」", me and me[0].get("state_label") == "已有人接", me)

st, d = D.call("GET", "/api/v2/me/orders")
inbox = [o for o in (data(d).get("items") or []) if o.get("id") == oid]
check("需求方在「我的单」里看到这单", len(inbox) == 1, oid)
check("需求方这边该点头", inbox and inbox[0].get("needs_me") is True, inbox)

print("[10] 接单方也能发自己的需求")
st, d = P.publish(**valid(title="接单方发的需求也要能发出去", category="design", budget_cents=30000))
check("接单方发单 201", st == 201, (st, d))
P_TID = data(d).get("id", "")

st, d = P.call("POST", "/api/v2/tasks/%s/take" % P_TID, {})
check("接单方接自己发的单被拒 409", st == 409, (st, d))
check("错误码 SELF_CLAIM", errcode(d) == "SELF_CLAIM", d)

st, d = X.call("POST", "/api/v2/tasks/%s/take" % P_TID, {})
check("需求方角色接别人的单也被拒 403", st == 403 and errcode(d) == "ROLE_NOT_PROVIDER", (st, d))

st, d = P2.call("POST", "/api/v2/tasks/%s/take" % P_TID, {})
check("另一个接单方能接走它", st == 201, (st, d))

st, d = X.publish(**{
    "title": "缺少预算的需求",
    "category": "other",
    "spec": "这一条故意不填预算，看看会不会被拦下来。",
})
check("缺预算被拒 400", st == 400, (st, d))
check("错误码 BUDGET_TOO_LOW", errcode(d) == "BUDGET_TOO_LOW", d)

print("\n发单链路 %d/%d 通过" % (PASS, PASS + FAIL))
if FAIL:
    print("\n失败项:")
    for f in FAILS:
        print("  - " + f)
    raise SystemExit(1)
