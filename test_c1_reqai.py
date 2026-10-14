#!/usr/bin/env python3
"""C1 端到端验证: AI 需求翻译(需求方侧) + AI 需求分析(接单方侧)。
覆盖:
  1. readiness 上报 reqai.mode == "rule"(无 LLM Key, 规则兜底)
  2. 需求方(R001)发布任务
  3. 需求方生成 AI 需求翻译 → 200, doc 非空, mode=rule, 落库(GET 任务可见 translation JSON)
  4. 接单方(P001)越权生成翻译 → 403(仅需求方本人可生成)
  5. 接单方(P001)生成 AI 需求分析 → 200, core_goal/交付物/需确认项非空, 落库(analysis_json)
  6. 负向: 翻译与分析在无 LLM Key 时走规则, 绝不空、绝不挂
"""
import json
import urllib.request
import urllib.error

BASE = "http://127.0.0.1:8080"


def call(method, path, token=None, body=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode())
        except Exception:
            return e.code, {}


def login(uid):
    _, d = call("POST", "/api/v1/auth/login", body={"uid": uid})
    return d["data"]["token"]


ok = 0
fail = 0


def check(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print(f"  [PASS] {name} {detail}")
    else:
        fail += 1
        print(f"  [FAIL] {name} {detail}")


print("=" * 64)
print("C1 端到端: AI 需求翻译 + AI 需求分析")
print("=" * 64)

t_r = login("R001")   # 需求方(发布者)
t_p = login("P001")   # 接单方

# 1. readiness: reqai.mode
s, d = call("GET", "/api/v1/readiness")
reqai_mode = ((d.get("data") or {}).get("reqai") or {}).get("mode")
print(f"  reqai.mode = {reqai_mode}")
check("readiness 上报 reqai.mode=rule(规则兜底)", reqai_mode == "rule", str(reqai_mode))

# 2. 发布任务(需求方)
s, d = call("POST", "/api/v1/tasks", token=t_r, body={
    "title": "做 5 张竖版国潮风小红书封面",
    "category": "design",
    "budget_cents": 80000,
    "spec": "帮我做 5 张竖版国潮风小红书封面，用于新品推广，10 月 20 号前交付，预算 800 元。",
    "deliverables": "5 张竖版小红书封面",
    "required_format": "PNG 1080x1440",
    "deadline_at": 0,
    "style_ref": "国潮风",
    "items": [{"desc": "首页主封面"}, {"desc": "产品卖点封面"}],
})
check("发布任务 200/201", s in (200, 201), str(s))
task = (d.get("data") or {}).get("task") or {}
tid = task.get("id")
print(f"  task.id = {tid}")
check("任务已创建且返回 id", bool(tid), str(tid))

# 3. 需求方生成 AI 需求翻译
s, d = call("POST", f"/api/v1/tasks/{tid}/translate", token=t_r)
tr = d.get("data") or {}
print(f"  翻译 mode={tr.get('mode')} doc长度={len(tr.get('doc') or '')}")
check("生成翻译 200", s == 200, str(s))
check("翻译 doc 非空", bool(tr.get("doc")), "")
check("翻译 mode=rule", tr.get("mode") == "rule", str(tr.get("mode")))
check("翻译含交付物清单", "交付物清单" in (tr.get("doc") or ""), "")

# 3b. 落库校验: GET 任务可见 translation JSON
s, d = call("GET", f"/api/v1/tasks/{tid}")
tt = (d.get("data") or {}).get("task") or {}
try:
    persisted_tr = json.loads(tt.get("translation") or "{}")
except Exception:
    persisted_tr = {}
check("翻译已落库(translation 非空)", bool(tt.get("translation")), "")
check("落库翻译可解析且含 doc", bool(persisted_tr.get("doc")), str(persisted_tr.keys()))

# 4. 接单方越权生成翻译 → 403
s, d = call("POST", f"/api/v1/tasks/{tid}/translate", token=t_p)
print(f"  接单方越权翻译: status={s} msg={(d.get('msg') or '')[:40]}")
check("非需求方生成翻译被拒(403)", s == 403, str(s))

# 5. 接单方生成 AI 需求分析
s, d = call("POST", f"/api/v1/tasks/{tid}/analyze", token=t_p)
an = d.get("data") or {}
print(f"  分析 mode={an.get('mode')} core_goal={an.get('core_goal')} 交付物数={len(an.get('deliverables') or [])} 需确认数={len(an.get('need_confirm') or [])}")
check("生成分析 200", s == 200, str(s))
check("分析 core_goal 非空", bool(an.get("core_goal")), "")
check("分析 deliverables 非空列表", isinstance(an.get("deliverables"), list) and len(an.get("deliverables")) > 0, str(an.get("deliverables")))
check("分析 need_confirm 非空列表", isinstance(an.get("need_confirm"), list) and len(an.get("need_confirm")) > 0, str(an.get("need_confirm")))
check("分析含易误解点", isinstance(an.get("misunderstandings"), list) and len(an.get("misunderstandings")) > 0, "")

# 5b. 落库校验: GET 任务可见 analysis_json
s, d = call("GET", f"/api/v1/tasks/{tid}")
tt2 = (d.get("data") or {}).get("task") or {}
try:
    persisted_an = json.loads(tt2.get("analysis_json") or "{}")
except Exception:
    persisted_an = {}
check("分析已落库(analysis_json 非空)", bool(tt2.get("analysis_json")), "")
check("落库分析可解析且含 core_goal", bool(persisted_an.get("core_goal")), str(persisted_an.keys()))

print("=" * 64)
print(f"C1 结果: PASS={ok}  FAIL={fail}")
print("C1_OK" if fail == 0 else "C1_FAIL")
