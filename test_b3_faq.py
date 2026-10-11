#!/usr/bin/env python3
"""B3 端到端验证: AI 客服应答（IM FAQ bot）全链路。
覆盖:
  1. readiness 上报 faq.mode == "faq"(无 LLM Key, 规则兜底)
  2. AI 建议回复端点 /api/v1/im/{peer}/ai-reply（规则命中 quote → 返回话术）
  3. AI 自动回复钩子: 收件人开启 AIAutoReply 后, 入站提问自动生成 is_ai=true 的 AI 消息
  4. 人工采纳 AI 建议发送(ai_type="suggest", is_ai=false)
  5. 开关 /api/v1/me/ai-auto-reply GET/POST 持久化
  6. 负向: 收件人未开启自动回复, 入站提问不产生 AI 消息
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
print("B3 端到端: AI 客服应答（FAQ bot / 自动回复 / 建议回复）")
print("=" * 64)

t_r = login("R001")            # 需求方(发问方)
t_p1 = login("P001")           # 接单方(自动回复开启方)
t_p3 = login("P003")           # 接单方(自动回复关闭方, 负向对照)

# 1. readiness: faq.mode
s, d = call("GET", "/api/v1/readiness")
faq_mode = ((d.get("data") or {}).get("faq") or {}).get("mode")
print(f"  faq.mode = {faq_mode}")
check("readiness 上报 faq.mode=faq(规则兜底)", faq_mode == "faq", str(faq_mode))

# 2. AI 建议回复端点(规则命中)
s, d = call("POST", "/api/v1/im/P001/ai-reply", token=t_r, body={"text": "这个任务报价多少合适？"})
rep = (d.get("data") or {})
print(f"  建议回复: matched={rep.get('matched')} type={rep.get('type')} intent={rep.get('intent')}")
check("建议回复 200", s == 200, str(s))
check("规则命中 matched=true", rep.get("matched") is True, str(rep.get("matched")))
check("命中意图=quote", rep.get("intent") == "quote", str(rep.get("intent")))
check("返回话术非空", bool(rep.get("text")), "")

# 3. 自动回复开关: P001 开启
s, d = call("POST", "/api/v1/me/ai-auto-reply", token=t_p1, body={"on": True})
check("开启自动回复 200", s == 200, str(s))
s, d = call("GET", "/api/v1/me/ai-auto-reply", token=t_p1)
check("开关已持久化(on=true)", (d.get("data") or {}).get("ai_auto_reply") is True, str(d.get("data")))

# 4. 触发自动回复: R001 向 P001 提问 → 应自动产生 is_ai=true 的 AI 消息
s, d = call("POST", "/api/v1/im", token=t_r, body={"to": "P001", "body": "请问报价多少合适呀"})
check("发送提问 200", s == 200, str(s))
s, d = call("GET", "/api/v1/im/conversations/P001", token=t_r)
msgs = (d.get("data") or [])
ai_msgs = [m for m in msgs if m.get("is_ai") is True]
print(f"  会话消息数={len(msgs)}  AI自动回复消息数={len(ai_msgs)}")
check("自动回复产生 is_ai 消息", len(ai_msgs) >= 1, f"ai_msgs={len(ai_msgs)}")
if ai_msgs:
    am = ai_msgs[-1]
    check("AI消息含透明显式前缀", am.get("body", "").startswith("🤖 AI 自动回复："), am.get("body", "")[:20])
    check("AI消息 ai_type=faq", am.get("ai_type") == "faq", str(am.get("ai_type")))

# 5. 人工采纳 AI 建议发送(ai_type=suggest, is_ai=false)
s, d = call("POST", "/api/v1/im", token=t_r, body={"to": "P001", "body": "感谢，我考虑一下", "ai_type": "suggest"})
check("采纳建议发送 200", s == 200, str(s))
sent = (d.get("data") or {})
check("采纳消息 is_ai=false", sent.get("is_ai") is False, str(sent.get("is_ai")))
check("采纳消息 ai_type=suggest", sent.get("ai_type") == "suggest", str(sent.get("ai_type")))

# 6. 负向: P003 未开启自动回复, R001 提问不应产生 AI 消息
s, d = call("GET", "/api/v1/me/ai-auto-reply", token=t_p3)
check("P003 默认关闭", (d.get("data") or {}).get("ai_auto_reply") is False, str(d.get("data")))
_, before = call("GET", "/api/v1/im/conversations/P003", token=t_r)
before_ai = len([m for m in (before.get("data") or []) if m.get("is_ai")])
s, d = call("POST", "/api/v1/im", token=t_r, body={"to": "P003", "body": "怎么付款？平台安全吗"})
check("向 P003 发送 200", s == 200, str(s))
_, after = call("GET", "/api/v1/im/conversations/P003", token=t_r)
after_ai = len([m for m in (after.get("data") or []) if m.get("is_ai")])
check("未开启自动回复则不产生 AI 消息", after_ai == before_ai, f"before={before_ai} after={after_ai}")

print("=" * 64)
print(f"B3 结果: PASS={ok}  FAIL={fail}")
print("B3_OK" if fail == 0 else "B3_FAIL")
