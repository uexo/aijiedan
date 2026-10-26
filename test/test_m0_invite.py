"""M0 发出去: 一句能直接说出口的话 + 一条能念出来的短链 + 那个唯一的数。

这一条要证明的不是"多了一个端点"，而是「这张需求有没有从发单方传到接单方」这件事可测：

  - 话术 —— 由服务端生成，含标题、含金额、含链接；整钱不带两位小数
  - 短链 —— /t/{id} 302 到认领页；打不开的单不记命中
  - 命中 —— 打开一次记一次，不去重；单子不存在就不记（拼错的链接不该算进来）
  - 谁能看 —— 详情公开（大厅本来就公开），话术只有发单方拿得到
  - 能不能接 —— viewer 在点之前就说清楚：你自己的单/没填档案/身份不对/已被接走
  - 那个数 —— 24 小时内被打开的比例；还没到期的单单列，不压分母
  - 那个数要看得见 —— /me/tasks 每条带 open_count，发单/撤单返回也带，
    不然它只存在于 curl 里，发单方永远不知道自己那段话发出去没有

最后一条是这一段全部的意义：只看成交数是分不清"没人要"和"没人知道"的。

只依赖标准库，内存模式与 MySQL 模式都能跑:
    python test/test_m0_invite.py
    BASE=http://127.0.0.1:8080 python test/test_m0_invite.py
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
NO_PROXY = urllib.request.ProxyHandler({})

T_MAIN = "T-DEMO-001"
T_OTHER = "T-DEMO-002"

PASS = FAIL = 0
FAILS = []

CONN_ERRORS = (ConnectionResetError, ConnectionAbortedError, http.client.RemoteDisconnected)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None  # 不跟跳转, 否则永远看不到 302 与 Location


class Client:
    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(NO_PROXY, urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, csrf=True):
        first = None
        for attempt in range(3):
            try:
                return self._call(method, path, body, csrf)
            except CONN_ERRORS as e:
                first = first or e
                time.sleep(0.15 * (attempt + 1))
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
                return r.status, json.loads(raw.decode()) if raw else {}
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read().decode())

    def login(self, phone, role):
        self.call("POST", "/api/v2/auth/code", {"phone": phone}, csrf=False)
        return self.call("POST", "/api/v2/auth/login", {"phone": phone, "code": "000000", "role": role}, csrf=False)

    def profile(self, headline):
        return self.call("PUT", "/api/v2/me/profile", {
            "headline": headline,
            "bio": "交付前会先自己跑一遍。",
            "skills": ["python", "rag", "自动化"],
            "portfolio": [{"label": "工单分类器", "url": "https://example.com/case"}],
            "availability": 1,
        })

    def publish(self, **kw):
        return self.call("POST", "/api/v2/tasks", kw)

    def invite(self, task_id):
        return self.call("GET", "/api/v2/tasks/%s/invite" % task_id)

    def detail(self, task_id):
        return self.call("GET", "/api/v2/tasks/%s" % task_id)

    def stats(self):
        return self.call("GET", "/api/v2/me/invite-stats")

    def claim(self, task_id):
        return self.call("POST", "/api/v2/tasks/%s/take" % task_id, {})

    def my_tasks(self):
        return self.call("GET", "/api/v2/me/tasks")


def new_phone(prefix):
    return prefix + "".join(random.choice("0123456789") for _ in range(8))


def code_of(d):
    return ((d or {}).get("error") or {}).get("code") or ""


def data_of(d):
    return (d or {}).get("data") or {}


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print("  PASS  " + name)
    else:
        FAIL += 1
        FAILS.append("%s :: %s" % (name, detail))
        print("  FAIL  %s   %s" % (name, detail))


def open_short_link(task_id):
    """直接打短链, 不跟跳转。返回 (status, location)。"""
    op = urllib.request.build_opener(NO_PROXY, NoRedirect)
    try:
        with op.open(BASE + "/t/" + task_id, timeout=10) as r:
            return r.status, r.headers.get("Location") or ""
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Location") or ""


def make_task(title, budget_cents=300000):
    return {
        "title": title,
        "category": "nlp",
        "spec": "需要 python + rag，支持文档上传与中文问答，交付要能跑起来。",
        "budget_cents": budget_cents,
        "deliverables": "1 套源码 + 部署文档",
        "required_format": "Python 项目(zip) + README",
        "style_ref": "生产级可维护代码",
        "items": ["能读 PDF", "返回带引用片段"],
    }


print("[0] 准备身份")
R = Client()
st, _ = R.login("13800000001", "demand")
check("发单方 R001 能登录", st == 200, st)

# 一个没填档案的接单方, 和一个填好的接单方。两种 rejected 要报不同原因。
P_NEW = Client()
st, _ = P_NEW.login(new_phone("156"), "supply")
check("空档案接单方能登录", st == 200, st)

P_OK = Client()
st, _ = P_OK.login(new_phone("157"), "supply")
check("有档案接单方登录 200", st == 200, st)
st, d = P_OK.profile("把重复流程做成能跑的 Agent")
check("接单方档案能存", st == 200, (st, d))

ANON = Client()

# 命中次数取基线而不是直接断言 0: 手工调过接口、或者没重置库就重跑，
# 绝对数会变，但"打开一次涨一次"这件事不变。断言 delta 才测得到去重/漏记。
st, d = R.invite(T_MAIN)
BASE_OPENS = (data_of(d) or {}).get("opens") or 0
print("  (基线) 已记录打开 %d 次" % BASE_OPENS)

print("[1] 公开详情: 大厅本来就公开, 单条不该突然要登录")
st, d = ANON.detail(T_MAIN)
check("未登录也能看单条需求 200", st == 200, (st, d))
task = data_of(d).get("task") or {}
check("返回里有 task 对象", bool(task.get("id")), task)
check("开放中的单给全部字段", bool(task.get("spec")) and bool(task.get("deliverables")), task)
check("带人话状态标签", task.get("state_label") == "开放中", task.get("state_label"))
vw = data_of(d).get("viewer") or {}
check("未登录时 viewer.logged_in=false", vw.get("logged_in") is False, vw)
check("未登录时 can_claim=false", vw.get("can_claim") is False, vw)
check("未登录不报 why_code(留给登录后)", not vw.get("why_code"), vw)

st, d = ANON.detail("T-NOT-EXIST")
check("不存在的单 404", st == 404, (st, d))
check("错误码 TASK_NOT_FOUND", code_of(d) == "TASK_NOT_FOUND", d)

print("[2] 短链: 这条 URL 是要被人念出来的")
st, loc = open_short_link(T_MAIN)
check("/t/{id} 返回 302", st == 302, st)
check("跳到认领页", "take.html?t=" + T_MAIN in loc, loc)
check("用 302 不用 301(301 会被缓存, 第二次点就不经过服务端了)", st == 302, st)

print("[3] 话术: 发单的人卡住的不是复制, 是不知道怎么开口")
st, d = R.invite(T_MAIN)
check("发单方拿到话术 200", st == 200, (st, d))
inv = data_of(d)
sentence = inv.get("sentence") or ""
check("话术非空", bool(sentence), inv)
check("话术里有标题原话", "用 Python 搭建 RAG 问答系统" in sentence, sentence)
check("话术里有钱", "3000 元" in sentence, sentence)
check("整钱不带两位小数", "3000.00" not in sentence, sentence)
check("话术里带链接", "/t/" + T_MAIN in sentence, sentence)
check("链接是完整地址", (inv.get("url") or "").startswith("http"), inv.get("url"))
check("URL 用当前站点拼, 不写死配置", BASE.rstrip("/") in (inv.get("url") or ""), inv.get("url"))
check("新单可以发(can_redo)", inv.get("can_redo") is True, inv)
# [2] 已经打过一次短链, 所以此刻应当是基线 + 1。
check("打开次数只涨了 [2] 那一次", inv.get("opens") == BASE_OPENS + 1, (inv.get("opens"), BASE_OPENS))

st, d = P_OK.invite(T_MAIN)
check("接单方拿不到别人单的话术 403", st == 403, (st, d))
check("错误码 NOT_TASK_OWNER", code_of(d) == "NOT_TASK_OWNER", d)

st, d = ANON.invite(T_MAIN)
check("未登录拿话术 401", st == 401, (st, d))

print("[4] 命中: 打开一次记一次, 不去重")
st, _ = open_short_link(T_MAIN)
st, _ = open_short_link(T_MAIN)
st, d = R.invite(T_MAIN)
inv = data_of(d)
# 基线 + [2] 一次 + [4] 两次 = +3。不去重的话这里会一直是 +1。
check("打开两次记两次", inv.get("opens") == BASE_OPENS + 3, (inv.get("opens"), BASE_OPENS))
check("第一次打开的时刻被记下来", (inv.get("first_open_at") or 0) > 0, inv.get("first_open_at"))
check("第一次时刻不晚于现在", (inv.get("first_open_at") or 0) <= int(time.time() * 1000) + 5000,
      inv.get("first_open_at"))

st, _ = open_short_link("T-NOT-EXIST")
check("打一个不存在的单不报错(照常跳转, 由页面说'不在了')", st == 302, st)

print("[5] 能不能接: 在点之前就说清, 别让他点一下试试")
st, d = R.detail(T_MAIN)
vw = data_of(d).get("viewer") or {}
check("发单人看自己的单: mine=true", vw.get("mine") is True, vw)
check("发单人不能接自己的单", vw.get("can_claim") is False, vw)
check("原因码 SELF_CLAIM", vw.get("why_code") == "SELF_CLAIM", vw)
check("给了人话原因", bool(vw.get("why_not")), vw)

st, d = P_NEW.detail(T_MAIN)
vw = data_of(d).get("viewer") or {}
check("没填档案的接单方 can_claim=false", vw.get("can_claim") is False, vw)
check("原因码 PROFILE_INCOMPLETE", vw.get("why_code") == "PROFILE_INCOMPLETE", vw)
check("给了该干什么", bool(vw.get("why_hint")), vw)

st, d = P_OK.detail(T_MAIN)
vw = data_of(d).get("viewer") or {}
check("有档案的接单方 can_claim=true", vw.get("can_claim") is True, vw)
check("能接时 why_code 为空", not vw.get("why_code"), vw)
check("能接时也认得出不是自己的单", vw.get("mine") is False, vw)

print("[6] 接走之后: 同一条链接要给下一句人话, 而不是把需求书摊给外人")
st, d = P_OK.claim(T_MAIN)
check("接单方认领 201", st == 201, (st, d))
st, d = ANON.detail(T_MAIN)
task = data_of(d).get("task") or {}
check("已经有人接的单仍能打开(链接不该 404)", st == 200, (st, d))
check("状态标签变成「已有人接」", task.get("state_label") == "已有人接", task.get("state_label"))
check("不再给需求书全文", not task.get("spec"), task.get("spec"))
check("仍给单号和标题", bool(task.get("id")) and bool(task.get("title")), task)

st, d = R.invite(T_MAIN)
inv = data_of(d)
check("已有人接的单不能重发链接", inv.get("can_redo") is False, inv)
check("并给出为什么", "有人接了" in (inv.get("why_not") or ""), inv.get("why_not"))

print("[7] 那个数: 24 小时内被打开的比例")
st, d = R.stats()
check("能拿到统计 200", st == 200, (st, d))
s = data_of(d)
check("窗口是 24 小时", s.get("window_hours") == 24, s)
check("分母含这个人发过的全部单", (s.get("published") or 0) >= 1, s)
check("窗口内有单被打开过", (s.get("opened_in_time") or 0) >= 1, s)
check("比例落在 0..1", 0 <= (s.get("ratio") or 0) <= 1, s)

# 再发一张不打链接的单: 它是"还没到期", 不该被算进分母把比例压低。
st, d = R.publish(**make_task("第二单：把周报接到飞书上", 80000))
check("再发一单 201", st == 201, (st, d))
T2 = data_of(d).get("id") or ""

st, d = R.stats()
s2 = data_of(d)
check("新单被算进已发数", (s2.get("published") or 0) == (s.get("published") or 0) + 1, s2)
check("新单还没到期, 记在 too_early", (s2.get("too_early") or 0) >= 1, s2)
check("未到期的单不压低比例", (s2.get("ratio") or 0) == (s.get("ratio") or 0), (s.get("ratio"), s2.get("ratio")))

st, d = R.invite(T2)
inv2 = data_of(d)
check("第二单话术金额是 800 元(不是 80000)", "800 元" in (inv2.get("sentence") or ""), inv2.get("sentence"))

print("[8] 小数金额: 39.9 说成 39 是少给钱, 不是简洁")
st, d = R.publish(**make_task("第三单：改一版封面", 3990))
check("发一单 39.9 元的 201", st == 201, (st, d))
T3 = data_of(d).get("id") or ""
st, d = R.invite(T3)
check("39.9 元保留两位小数", "39.90 元" in (data_of(d).get("sentence") or ""), data_of(d).get("sentence"))

print("[9] 权限: 话术是发单人的东西, 别人不该拿到")
st, d = ANON.invite(T2)
check("未登录拿不到", st == 401, st)
st, d = P_OK.invite(T2)
check("别的接单方拿不到", st == 403, st)
st, d = R.invite("T-NOT-EXIST")
check("不存在的单 404", st == 404 and code_of(d) == "TASK_NOT_FOUND", (st, d))

print("[10] 那个数要长在发单方眼前, 不能只在 curl 里")
st, d = R.my_tasks()
items = data_of(d).get("items") or []
by_id = {t.get("id"): t for t in items}
check("我的单里能拿到刚发的这一条", T3 in by_id, list(by_id.keys()))
check("没被打开过就是 0", (by_id.get(T3) or {}).get("open_count") == 0, by_id.get(T3))
check("同一屏里别的单不串味", (by_id.get(T2) or {}).get("open_count") == 0, by_id.get(T2))

# 之前几段已经打过 T_MAIN, 所以它是"被人点过"的那一档
check("被打过的单数得出次数", (by_id.get(T_MAIN) or {}).get("open_count", 0) >= 1, by_id.get(T_MAIN))

open_short_link(T3)
open_short_link(T3)
st, d = R.my_tasks()
after = {t.get("id"): t for t in (data_of(d).get("items") or [])}
t3 = after.get(T3) or {}
check("打开两次就记两次", t3.get("open_count") == 2, t3.get("open_count"))
# 按 task_id 分桶, 不是全局计数: 混在一起的话, 一次误点会把所有单都变成"发到了"
check("同一屏里没被点的单还是 0", (after.get(T2) or {}).get("open_count") == 0, (after.get(T2) or {}).get("open_count"))

# 发单的 201 回的是 demand.View(发单页要用的那套字段), 不是列表项——
# 所以这里不要求它带 open_count: 刚发出去必然是 0, 而成功页上的话术
# 是另取 /tasks/{id}/invite, 那边自带 opens。字段跟着用它的人走。
st, d = R.publish(**make_task("第四单：把客服话术过一遍", 50000))
T4 = data_of(d).get("id") or ""

# 撤单返回走的是同一份列表映射, 少一个字段就会出现"刚撤的单少了类目名"那类问题
st, d = R.call("POST", "/api/v2/tasks/%s/withdraw" % T4, {})
check("撤退单返回也带 open_count", "open_count" in (data_of(d) or {}), data_of(d))
check("撤回后状态对", (data_of(d) or {}).get("status") == "withdrawn", data_of(d))

print("")
print("发出去: %d 通过, %d 失败" % (PASS, FAIL))
print("=" * 56)
if FAILS:
    print("失败项:")
    for f in FAILS:
        print("  - " + f)
raise SystemExit(1 if FAIL else 0)
