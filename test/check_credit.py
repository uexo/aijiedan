"""信用事件落库体检（只读，不做任何写操作）。

为什么需要它: 验收链路的用例只能证明"接口返回 200"，证明不了"信用事件真的落进了库"。
飞轮断的典型症状是——订单状态往前走了，credit_event 里什么都没有，而所有用例依然全绿。
这个脚本直接用 SQL 核对两件事:

  1) 每一条 completed 订单, 必须恰好有 2 条 order_completed(需求方 + 接单方)
  2) 每一条 order_completed 的 delta 必须是 0(完成单只留痕, 不动分)

用法(跑完 test_m0_accept 后):
    ZANBUDDY_DB_PASSWORD=xxxx \\
      C:/Users/admin/.workbuddy/binaries/python/envs/default/Scripts/python.exe test/check_credit.py
"""
import os
import sys

import pymysql

HOST = os.getenv("ZANBUDDY_DB_HOST", "192.168.3.18")
PORT = int(os.getenv("ZANBUDDY_DB_PORT", "3306"))
USER = os.getenv("ZANBUDDY_DB_USER", "root")
PASSWORD = os.getenv("ZANBUDDY_DB_PASSWORD", "")
DB = os.getenv("ZANBUDDY_DB_NAME", "zanbuddy")

FAILS = []


def check(cond, msg):
    if cond:
        print(f"  ok   {msg}")
    else:
        print(f"  FAIL {msg}")
        FAILS.append(msg)


def main():
    if not PASSWORD:
        print("ERROR: 未设置 ZANBUDDY_DB_PASSWORD，已中止。")
        return 2

    cs = pymysql.connect(host=HOST, port=PORT, user=USER, password=PASSWORD, database=DB, charset="utf8mb4")
    try:
        with cs.cursor() as cur:
            cur.execute("SELECT kind, COUNT(*) FROM credit_event GROUP BY kind")
            kinds = dict(cur.fetchall())
            print(f"事件分布: {kinds or '(空)'}")

            cur.execute("SELECT status, COUNT(*) FROM `order` GROUP BY status")
            orders = dict(cur.fetchall())
            print(f"订单分布: {orders or '(空)'}")

            completed = orders.get("completed", 0)
            events = kinds.get("order_completed", 0)

            check(completed > 0, f"库里存在已完成订单（{completed} 单）")
            check(
                events == completed * 2,
                f"order_completed 条数 = 完成单数×2（实际 {events} 条 vs 期望 {completed * 2} 条）",
            )

            cur.execute(
                "SELECT COUNT(*) FROM credit_event WHERE kind='order_completed' AND delta <> 0"
            )
            bad_delta = cur.fetchone()[0]
            check(bad_delta == 0, f"所有 order_completed 的 delta 都是 0（异常 {bad_delta} 条）")

            # 半截状态: 订单完成了但事件没落
            cur.execute(
                """SELECT o.id, COUNT(c.id) AS n
                   FROM `order` o
                   LEFT JOIN credit_event c ON c.ref_id = o.id AND c.kind = 'order_completed'
                   WHERE o.status = 'completed'
                   GROUP BY o.id
                   HAVING n <> 2"""
            )
            half = cur.fetchall()
            check(not half, f"没有'订单完成但事件不全'的半截状态（异常 {len(half)} 单）")
            for oid, n in half[:5]:
                print(f"       {oid} 只有 {n} 条事件")

            # 幂等: 同一 (user,kind,ref) 不应重复
            cur.execute(
                """SELECT user_id, kind, ref_id, COUNT(*) c
                   FROM credit_event GROUP BY user_id, kind, ref_id HAVING c > 1"""
            )
            dup = cur.fetchall()
            check(not dup, f"没有重复事件（异常 {len(dup)} 组）")
    finally:
        cs.close()

    print()
    if FAILS:
        print(f"体检未通过: {len(FAILS)} 项")
        return 1
    print("体检通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
