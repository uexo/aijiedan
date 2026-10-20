"""M0 收尾验收: 档案保存 → 落库确认 → 需求大厅列表。

这条链路是接单方的第一段路: 登录 → 填档案 → 大厅找单。
测试要证明两件事:
  1) 提交成功后读回来的档案和提交的一致(跳转前信息确实存住了)
  2) 大厅接口能给出可用的需求列表, 且筛选/排序/分页/白名单都对
只依赖标准库, 随项目长期保留。
"""
import json
import http.cookiejar
import random
import urllib.request
import urllib.parse
import urllib.error
import sys

BASE = "http://127.0.0.1:8082"
PHONE = "138" + "".join(random.choice("0123456789") for _ in range(8))
PASS = FAIL = 0
FAILS = []

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None, csrf=False):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    if csrf:
        for c in jar:
            if c.name == "csrf_token":
                req.add_header("X-CSRF-Token", c.value)
    try:
        with opener.open(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode())


def anon_get(path):
    op = urllib.request.build_opener()
    try:
        with op.open(BASE + path, timeout=10) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode())


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  PASS  {name}")
    else:
        FAIL += 1
        FAILS.append(f"{name} :: {detail}")
        print(f"  FAIL  {name}   {detail}")


HEADLINE = "把重复流程做成能跑的 Agent"
BIO = "做过 7 个客服工单类 Agent，最长的一个跑了两年"
SKILLS = ["python", "rag", "automation"]
PFL = [{"label": "退款审批 Agent", "url": "https://example.com/case/refund-agent"}]

print("[1] 新接单方登录")
st, d = call("POST", "/api/v2/auth/code", {"phone": PHONE})
check("发码 200", st == 200, st)
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE, "code": "000000", "role": "provider"})
data = d.get("data") or {}
check("登录 200", st == 200, f"{st} {str(d)[:140]}")
check("未建档 onboarded=false", data.get("onboarded") is False, data.get("onboarded"))
check("未建档先去档案页", "profile.html" in (data.get("next") or ""), data.get("next"))

print("[1b] 角色口径: 登录按钮传 supply / demand, 库里必须是 provider / requester")
# 已有会话时发码/登录也走 CSRF 校验, 先退出再切账号
call("POST", "/api/v2/auth/logout", {}, csrf=True)
PHONE2 = "137" + "".join(random.choice("0123456789") for _ in range(8))
call("POST", "/api/v2/auth/code", {"phone": PHONE2})
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE2, "code": "000000", "role": "supply"})
u2 = ((d.get("data") or {}).get("user") or {})
check("supply 登录 200", st == 200, f"{st} {str(d)[:140]}")
check("supply 落库为 provider", u2.get("role") == "provider", u2.get("role"))
check("supply 未建档先去档案页", "profile.html" in ((d.get("data") or {}).get("next") or ""), (d.get("data") or {}).get("next"))

PHONE3 = "136" + "".join(random.choice("0123456789") for _ in range(8))
call("POST", "/api/v2/auth/logout", {}, csrf=True)
call("POST", "/api/v2/auth/code", {"phone": PHONE3})
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE3, "code": "000000", "role": "demand"})
u3 = ((d.get("data") or {}).get("user") or {})
check("demand 登录 200", st == 200, st)
check("demand 落库为 requester", u3.get("role") == "requester", u3.get("role"))
check("需求方落点是大厅", "hall.html" in ((d.get("data") or {}).get("next") or ""), (d.get("data") or {}).get("next"))
# 回到主线的接单方会话
call("POST", "/api/v2/auth/logout", {}, csrf=True)
call("POST", "/api/v2/auth/code", {"phone": PHONE})
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE, "code": "000000", "role": "provider"})
check("回到主账号 200", st == 200, st)

print("[2] 空档案基线")
st, d = call("GET", "/api/v2/me")
p0 = (d.get("data") or {}).get("profile") or {}
check("me 200", st == 200, st)
check("完整度 0", p0.get("completeness") == 0, p0.get("completeness"))

print("[3] 提交档案")
payload = {
    "headline": HEADLINE,
    "bio": BIO,
    "skills": SKILLS,
    "portfolio": PFL,
    "availability": 1,
}
st, d = call("PUT", "/api/v2/me/profile", payload, csrf=True)
check("保存 200", st == 200, f"{st} {str(d)[:140]}")
check("完整度 100", ((d.get("data") or {}).get("profile") or {}).get("completeness") == 100,
      str((d.get("data") or {}).get("profile"))[:140])

print("[4] 跳转前落库确认（读回来的必须和提交的一致）")
st, d = call("GET", "/api/v2/me")
mv = d.get("data") or {}
p = mv.get("profile") or {}
u = mv.get("user") or {}
check("me 200", st == 200, st)
check("headline 一致", p.get("headline") == HEADLINE, p.get("headline"))
check("bio 一致", p.get("bio") == BIO, str(p.get("bio"))[:60])
check("skills 一致", sorted(p.get("skills") or []) == sorted(SKILLS), str(p.get("skills")))
check("portfolio 一致", [x.get("url") for x in (p.get("portfolio") or [])] == [PFL[0]["url"]],
      str(p.get("portfolio"))[:120])
check("availability 一致", p.get("availability") == 1, p.get("availability"))
check("onboarded 已置 true", u.get("onboarded") is True, u.get("onboarded"))
check("完整度 100", p.get("completeness") == 100, p.get("completeness"))

print("[4b] 昵称：个人信息的一部分，大厅要叫得出名字")
st, d = call("PATCH", "/api/v2/me", {"name": "算法老王"}, csrf=True)
check("改昵称 200", st == 200, f"{st} {str(d)[:120]}")
check("响应里已是新昵称", ((d.get("data") or {}).get("user") or {}).get("name") == "算法老王", str(d)[:120])
st, d = call("GET", "/api/v2/me")
check("读回来还是新昵称", ((d.get("data") or {}).get("user") or {}).get("name") == "算法老王", str(d)[:120])
st, d = call("PATCH", "/api/v2/me", {"name": "   "}, csrf=True)
check("空昵称 400", st == 400, st)
check("code=NAME_INVALID", (d.get("error") or {}).get("code") == "NAME_INVALID", str(d)[:120])
check("field=name", (d.get("error") or {}).get("field") == "name", str(d.get("error")))

print("[5] 已建档后的落点")
st, d = call("POST", "/api/v2/auth/logout", {}, csrf=True)
check("登出 200", st == 200, st)
call("POST", "/api/v2/auth/code", {"phone": PHONE})  # 验证码一次性, 二次登录要重发
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE, "code": "000000", "role": "provider"})
data = d.get("data") or {}
check("二次登录 200", st == 200, st)
check("onboarded=true", data.get("onboarded") is True, data.get("onboarded"))
check("落点指向需求大厅", "hall.html" in (data.get("next") or ""), data.get("next"))

print("[6] 大厅列表")
st, d = call("GET", "/api/v2/tasks")
check("列表 200", st == 200, f"{st} {str(d)[:140]}")
lst = d.get("data") or {}
items = lst.get("items") or []
check("items 是数组", isinstance(items, list), type(items).__name__)
check("有演示需求", lst.get("total", 0) >= 1, str(lst.get("total")))
if items:
    t = items[0]
    check("有 id", bool(t.get("id")), str(t)[:120])
    check("有标题", bool(t.get("title")), str(t)[:120])
    check("预算是整数分", isinstance(t.get("budget_cents"), int), str(t.get("budget_cents")))
    check("状态只有 open", t.get("status") == "open", t.get("status"))
    check("类目有中文名", bool(t.get("category_name")), t.get("category_name"))
    check("有需求方昵称", bool(t.get("requester")), t.get("requester"))
    check("无 requester_id 字段", "requester_id" not in json.dumps(t, ensure_ascii=False), t)
    check("分页字段齐全", isinstance(lst.get("has_more"), bool) and isinstance(lst.get("next_offset"), int),
          str({k: lst.get(k) for k in ("total", "limit", "offset", "next_offset", "has_more")}))

print("[7] 免登录可读（拉新路径：落地页 → 大厅 → 登录）")
st, d = anon_get("/api/v2/tasks?limit=5")
check("匿名 200", st == 200, st)
check("匿名也有信封", (d.get("ok") is True and d.get("trace_id")), str(d)[:120])

print("[8] 关键词与类目筛选")
st, d = call("GET", "/api/v2/tasks?q=rag")
si = (d.get("data") or {}).get("items") or []
check("q=rag 200", st == 200, st)
check("q=rag 命中", len(si) >= 1, str(len(si)))
check("命中项确实含 rag", all("rag" in json.dumps(x, ensure_ascii=False).lower() for x in si) if si else False,
      str(si)[:160])

st, d = call("GET", "/api/v2/tasks?q=zzzz-no-such-thing")
check("无结果 200", st == 200, st)
check("无结果空数组", (d.get("data") or {}).get("items") == [], str((d.get("data") or {}).get("items"))[:80])

st, d = call("GET", "/api/v2/tasks?category=nlp")
ci = (d.get("data") or {}).get("items") or []
check("category=nlp 200", st == 200, st)
check("类目过滤生效", ci and all(x.get("category") == "nlp" for x in ci), str([x.get("category") for x in ci])[:80])

st, d = call("GET", "/api/v2/tasks?category=design")
check("空类目 200", st == 200, st)
check("空类目 0 条", (d.get("data") or {}).get("items") == [], str((d.get("data") or {}).get("items"))[:80])

print("[9] 排序与分页")
st, d = call("GET", "/api/v2/tasks?sort=budget_desc")
bs = [x.get("budget_cents") for x in ((d.get("data") or {}).get("items") or [])]
check("sort=budget_desc 200", st == 200, st)
check("预算非递增", bs == sorted(bs, reverse=True), str(bs)[:100])

st, d = call("GET", "/api/v2/tasks?limit=1")
d1 = d.get("data") or {}
check("limit=1 生效", len(d1.get("items") or []) == 1, str(len(d1.get("items") or [])))
total = d1.get("total") or 0
check("limit 不改变 total", total >= 1, total)
if total > 1:
    check("has_more=true", d1.get("has_more") is True, d1.get("has_more"))
    st, d2 = call("GET", "/api/v2/tasks?limit=1&offset=1")
    first = ((d1.get("items") or [{}])[0]).get("id")
    second = (((d2.get("data") or {}).get("items") or [{}])[0]).get("id")
    check("offset 翻页换了一条", second and second != first, f"{first} vs {second}")

st, d = call("GET", "/api/v2/tasks?limit=999&sort=whatever")
check("非法参数容错 200", st == 200, st)
check("limit 被夹到 50 以内", len((d.get("data") or {}).get("items") or []) <= 50,
      len((d.get("data") or {}).get("items") or []))

print("[10] 类目清单")
st, d = call("GET", "/api/v2/meta/categories")
cats = (d.get("data") or {}).get("items") or []
check("类目 200", st == 200, st)
check("类目非空", len(cats) >= 10, len(cats))
check("类目有 id/label", all(c.get("id") and c.get("label") for c in cats), str(cats)[:120])
check("含 nlp", any(c.get("id") == "nlp" for c in cats), str([c.get("id") for c in cats])[:100])

print("[11] 脱敏")
raw = json.dumps(lst, ensure_ascii=False)
check("列表不含手机号", PHONE not in raw, "泄露手机号")
check("列表不含内部用户 ID", '"requester_id"' not in raw, raw[:120])

print("[12] 未登录不能写档案")
op = urllib.request.build_opener()
req = urllib.request.Request(BASE + "/api/v2/me/profile", data=b'{"headline":"x"}', method="PUT")
req.add_header("Content-Type", "application/json")
try:
    with op.open(req, timeout=10) as r:
        st = r.status
except urllib.error.HTTPError as e:
    st = e.code
check("匿名写档案 401", st == 401, st)

print(f"\n{PASS} passed, {FAIL} failed")
for f in FAILS:
    print("  FAILED:", f)
sys.exit(1 if FAIL else 0)
