"""M0 验收: v2 接入层 + 身份域 + 档案。

覆盖: 健康检查、发码、凭证校验、登录注册、CSRF 双提交、档案完整度、公开页字段白名单。
只依赖标准库, 随项目长期保留。
"""
import http.client
import http.cookiejar
import json
import random
import time
import urllib.request
import urllib.parse
import urllib.error
import sys

BASE = "http://127.0.0.1:8082"
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
# 每次跑用新号, 否则第二次进来是登录而不是注册, is_new 断言就失效
PHONE = "139" + "".join(random.choice("0123456789") for _ in range(8))
PASS = FAIL = 0
FAILS = []

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(NO_PROXY, urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None, csrf=False, auth=True):
    # 连接层抖动与重试规则, 同各 Client.call 的说明
    first = None
    for attempt in range(3):
        try:
            st, d = _call(method, path, body, csrf)
        except CONN_ERRORS as e:
            first = first or e
            time.sleep(0.15 * (attempt + 1))
            continue
        if first is not None and st >= 400 and ((d.get("error") or {}).get("code") in REPLAY_CONFLICTS):
            raise first
        return st, d
    raise first


def _call(method, path, body, csrf):
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
            raw = r.read()
            if not raw:
                raise ConnectionResetError('empty body')
            return r.status, json.loads(raw.decode())
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


print("[1] 健康检查")
st, d = call("GET", "/api/v2/healthz")
check("healthz 200", st == 200, st)
check("信封有 ok/data/trace_id", d.get("ok") is True and d.get("trace_id"), str(d)[:120])
check("error 为 null", d.get("error") is None, str(d.get("error")))

st, d = call("GET", "/api/v2/readyz")
check("readyz 200", st == 200, st)

print("[2] 发码")
st, d = call("POST", "/api/v2/auth/code", {"phone": PHONE})
check("发码 200", st == 200, st)
check("dev_code 有值", (d.get("data") or {}).get("dev_code") == "000000", str(d)[:150])

st, d = call("POST", "/api/v2/auth/code", {"phone": "12345"})
check("错号 400", st == 400, st)
check("code=INVALID_PHONE", (d.get("error") or {}).get("code") == "INVALID_PHONE", str(d)[:150])
check("field=phone", (d.get("error") or {}).get("field") == "phone", str(d.get("error")))

print("[3] 登录凭证校验")
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE, "code": "999999", "role": "provider"})
check("错码 409", st == 409, st)
check("code=CODE_WRONG", (d.get("error") or {}).get("code") == "CODE_WRONG", str(d)[:150])

print("[4] 登录注册")
st, d = call("POST", "/api/v2/auth/login", {"phone": PHONE, "code": "000000", "role": "provider"})
check("登录 200", st == 200, f"{st} {str(d)[:150]}")
data = d.get("data") or {}
user = data.get("user") or {}
check("is_new=true", data.get("is_new") is True, str(data)[:150])
check("role=provider", user.get("role") == "provider", user.get("role"))
check("slug 已分配", bool(user.get("slug")), str(user)[:150])
check("next 指向档案页", "profile.html" in (data.get("next") or ""), data.get("next"))
names = [c.name for c in jar]
check("下发 session cookie", "zb_session" in names, str(names))
check("下发 csrf cookie", "csrf_token" in names, str(names))
check("session 是 HttpOnly", any(c.name == "zb_session" and c.has_nonstandard_attr("HttpOnly") for c in jar), str([(c.name, c._rest) for c in jar]))

print("[5] 当前用户")
st, d = call("GET", "/api/v2/me")
check("me 200", st == 200, st)
mv = d.get("data") or {}
check("返回 user", bool(mv.get("user")), str(mv)[:150])
check("返回空 profile 不报错", mv.get("profile") is not None, str(mv.get("profile")))
check("未暴露密码哈希", "password_hash" not in json.dumps(mv), "泄露")

print("[6] CSRF 双提交")
st, d = call("PUT", "/api/v2/me/profile", {"headline": "把重复流程做成能跑的 Agent"}, csrf=False)
check("缺 CSRF 头 403", st == 403, f"{st} {str(d)[:120]}")
check("code=CSRF_MISSING", (d.get("error") or {}).get("code") == "CSRF_MISSING", str(d)[:150])

print("[7] 档案规则")
st, d = call("PUT", "/api/v2/me/profile", {"skills": ["python", "rag"]}, csrf=True)
check("技能 2 个被拒 400", st == 400, f"{st} {str(d)[:150]}")
check("code=SKILLS_RANGE", (d.get("error") or {}).get("code") == "SKILLS_RANGE", str(d)[:150])

st, d = call("PUT", "/api/v2/me/profile", {"headline": "把重复流程做成能跑的 Agent", "skills": ["python", "rag", "客服自动化"]}, csrf=True)
check("保存成功 200", st == 200, f"{st} {str(d)[:150]}")
p = (d.get("data") or {}).get("profile") or {}
check("完整度 65 (20+30+15)", p.get("completeness") == 65, f"got {p.get('completeness')}")

st, d = call("PUT", "/api/v2/me/profile", {
    "bio": "做过 7 个客服工单类 Agent，最长的一个跑了两年",
    "portfolio": [{"label": "退款审批 Agent", "url": "https://example.com/case/refund-agent"}],
}, csrf=True)
check("补全后 200", st == 200, st)
p = (d.get("data") or {}).get("profile") or {}
check("完整度 100", p.get("completeness") == 100, f"got {p.get('completeness')}")
check("部分保存不清空已有字段", p.get("headline") == "把重复流程做成能跑的 Agent", p.get("headline"))
check("技能保留 3 个", len(p.get("skills") or []) == 3, str(p.get("skills")))

st, d = call("PUT", "/api/v2/me/profile", {"skills": ["a", "b", "c", "d", "e", "f"]}, csrf=True)
check("技能 6 个被拒", st == 400, f"{st} {str(d)[:120]}")

print("[8] 引导状态")
st, d = call("GET", "/api/v2/me")
u2 = ((d.get("data") or {}).get("user") or {})
check("已置 onboarded", u2.get("onboarded") is True, str(u2)[:150])

print("[9] 公开页")
slug = user.get("slug")
anon = urllib.request.build_opener(NO_PROXY, )  # 不带 cookie, 模拟陌生人
try:
    with anon.open(BASE + "/api/v2/public/profiles/" + urllib.parse.quote(slug), timeout=10) as r:
        st, d = r.status, json.loads(r.read().decode())
except urllib.error.HTTPError as e:
    st, d = e.code, json.loads(e.read().decode())
check("免登录可访问 200", st == 200, st)
pv = d.get("data") or {}
check("有 headline", bool(pv.get("headline")), str(pv)[:150])
raw = json.dumps(pv, ensure_ascii=False)
check("不含 phone", PHONE not in raw, "泄露手机号")
check("不含内部 id", "\"id\"" not in raw, raw[:150])
check("背书空数组非 null", pv.get("endorsements") == [], str(pv.get("endorsements")))

try:
    with anon.open(BASE + "/api/v2/public/profiles/" + urllib.parse.quote("不存在的号"), timeout=10) as r:
        st = r.status
except urllib.error.HTTPError as e:
    st = e.code
check("不存在 404", st == 404, st)

print("[10] 登出与未登录")
st, d = call("POST", "/api/v2/auth/logout", {}, csrf=True)
check("登出 200", st == 200, st)
st, d = call("GET", "/api/v2/me")
check("登出后 401", st == 401, st)
check("code=UNAUTHENTICATED", (d.get("error") or {}).get("code") == "UNAUTHENTICATED", str(d)[:150])

print(f"\n{PASS} passed, {FAIL} failed")
for f in FAILS:
    print("  FAILED:", f)
sys.exit(1 if FAIL else 0)
