#!/usr/bin/env python3
"""
HiveGet MySQL 重置脚本（不依赖本机 MySQL 客户端）。

目标库：192.168.3.18:3306 上的 Docker MySQL 8.0.46（库名 hiveget）。
用法：
    python db_reset.py            # DROP+CREATE 并重新应用 migrations/schema.sql
    python db_reset.py --demo     # 同上，并预置合规四门为「dev/demo 就绪态」全开
    python db_reset.py --check    # 仅检查连通性与服务端版本，不重置

纪律：MySQL 模式冒烟前必须先 DROP+重建库，避免历史 smoke 累积行数致精确值断言失真。
本项目 MySQL 唯一真源是 192.168.3.18 的 Docker 实例，严禁使用本机 MySQL 5.7 作为
数据库或管理客户端。

--demo 的语义（董事长 2026-10-11 阶段决策：合规是上线前专项，当前重心是先把系统跑通）：
    默认出厂仅 KYC 通过，icp/tax_source/escrow 关闭，导致接单/托管/结算被硬拦截，
    冒烟无法跑通整条交易链。--demo 即把这三道门控预置为开启（dev/demo 就绪态），
    省去每次手工 POST /api/v1/compliance。真实合规签约仍是上线前 checklist。
    实现方式：reset 后预置 compliance_state 四行全开；服务器启动时 Seed() 用的是
    INSERT IGNORE，不会覆盖已存在的行，因此无需改动任何 Go 代码。
"""
import argparse
import os
import sys

import pymysql

# 连接参数全部可经环境变量覆盖；密码禁止硬编码，必须走 HIVEGET_DB_PASSWORD。
HOST = os.getenv("HIVEGET_DB_HOST", "192.168.3.18")
PORT = int(os.getenv("HIVEGET_DB_PORT", "3306"))
USER = os.getenv("HIVEGET_DB_USER", "root")
PASSWORD = os.getenv("HIVEGET_DB_PASSWORD", "")
DB = os.getenv("HIVEGET_DB_NAME", "hiveget")
SCHEMA = os.path.join(os.path.dirname(__file__), "migrations", "schema.sql")


def connect(db=None):
    if not PASSWORD:
        raise SystemExit(
            "ERROR: 未设置 HIVEGET_DB_PASSWORD 环境变量，已中止（禁止在源码硬编码密码）。\n"
            "用法: HIVEGET_DB_PASSWORD=xxxx python db_reset.py"
        )
    return pymysql.connect(
        host=HOST, port=PORT, user=USER, password=PASSWORD,
        database=db, autocommit=True, connect_timeout=8,
    )


def check():
    conn = connect()
    cur = conn.cursor()
    cur.execute("SELECT VERSION()")
    ver = cur.fetchone()[0]
    cur.execute("SELECT @@hostname, @@port")
    host, port = cur.fetchone()
    cur.execute("SHOW DATABASES LIKE %s", (DB,))
    exists = bool(cur.fetchone())
    conn.close()
    print(f"REMOTE SERVER VERSION: {ver}")
    print(f"SERVER HOST/PORT: {host}:{port}")
    print(f"DATABASE '{DB}' EXISTS: {exists}")
    assert ver.startswith("8.0"), "服务端不是 8.0.x，疑似连错库，已中止"
    assert host != "localhost" and "127.0.0.1" not in str(host), "疑似连到本机，已中止"
    print("OK: 已连接到 192.168.3.18 的 Docker MySQL，未使用本机 5.7。")


def _strip_comments(sql: str) -> str:
    """移除 -- 行注释（含注释内含分号的情况），保护单/双引号内的内容。"""
    out = []
    in_single = in_double = False
    i, n = 0, len(sql)
    while i < n:
        c = sql[i]
        if c == "'" and not in_double:
            in_single = not in_single
            out.append(c)
            i += 1
        elif c == '"' and not in_single:
            in_double = not in_double
            out.append(c)
            i += 1
        elif not in_single and not in_double and c == "-" and i + 1 < n and sql[i + 1] == "-":
            while i < n and sql[i] != "\n":
                i += 1
        else:
            out.append(c)
            i += 1
    return "".join(out)


DEMO_GATES = ("kyc", "icp", "tax_source", "escrow")


def _enable_demo_gates():
    """把合规四门预置为开启（dev/demo 就绪态）。须在 reset 建表之后调用。"""
    conn = connect(DB)
    cur = conn.cursor()
    for item in DEMO_GATES:
        cur.execute(
            "INSERT INTO compliance_state (`item`, `on`, note) VALUES (%s, 1, %s) "
            "ON DUPLICATE KEY UPDATE `on`=1",
            (item, "dev/demo 就绪态（db_reset.py --demo 预置，非真实合规落地）"),
        )
    cur.execute("SELECT `item`, `on` FROM compliance_state ORDER BY `item`")
    rows = cur.fetchall()
    conn.close()
    print("DEMO 合规四门已预置为开启: " + ", ".join(f"{i}={'on' if o else 'off'}" for i, o in rows))


def reset(demo=False):
    conn = connect()
    cur = conn.cursor()
    cur.execute(f"DROP DATABASE IF EXISTS `{DB}`")
    cur.execute(
        f"CREATE DATABASE `{DB}` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"
    )
    conn.close()

    conn = connect(DB)
    cur = conn.cursor()
    with open(SCHEMA, "r", encoding="utf-8") as f:
        sql = f.read()
    # 先剥离 -- 行注释（带单/双引号保护，避免误伤 COMMENT '...' 中的内容），
    # 再按 ';' 拆分。注意 schema.sql 的注释里可能含分号，必须先去注释再拆。
    sql = _strip_comments(sql)
    stmts = [s.strip() for s in sql.split(";") if s.strip()]
    for st in stmts:
        cur.execute(st)
    conn.close()

    # 校验
    conn = connect(DB)
    cur = conn.cursor()
    cur.execute("SHOW TABLES")
    tables = [r[0] for r in cur.fetchall()]
    conn.close()
    print(f"RESET OK: {len(tables)} 张表已重建于 {HOST}:{PORT}/{DB}")

    if demo:
        _enable_demo_gates()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="仅检查连通性，不重置")
    ap.add_argument("--demo", action="store_true",
                    help="重置后把合规四门预置为开启（dev/demo 就绪态），省去手工开门控")
    args = ap.parse_args()
    try:
        if args.check:
            check()
        else:
            check()
            reset(demo=args.demo)
    except Exception as e:  # noqa
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
