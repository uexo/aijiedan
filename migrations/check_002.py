#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check_002.py — 002_light_trade.sql 静态校验（仅标准库，不连数据库）

校验项：
  [1] 所有 CREATE TABLE 都带 IF NOT EXISTS
  [2] 无任何可空列（注释行除外）：拦截 `NULL` / `DEFAULT NULL` 形式
  [3] 每个 CREATE TABLE 都带 ENGINE=InnoDB
  [4] 每条语句都以分号结尾
附加项：
  [5] 不出现 CREATE DATABASE / USE（库名由连接串指定）
  [6] 保留字 order / user 作为表名时已用反引号包裹
  [7] 8 张新表与 4 条 ALTER 的目标表齐全

用法：python check_002.py [sql文件路径]
退出码：全部 PASS 为 0，任一 FAIL 为 1
"""

import re
import sys
from pathlib import Path

EXPECTED_TABLES = [
    "provider_profile",
    "endorsement",
    "endorsement_append",
    "match_reason",
    "credit_event",
    "abuse_flag",
    "audit_log",
    "idempotency_key",
]
EXPECTED_ALTER_TARGETS = ["user", "task", "order", "match_record"]
RESERVED_TABLE_NAMES = {"order", "user"}

NON_COLUMN_KEYWORDS = {
    "PRIMARY",
    "UNIQUE",
    "KEY",
    "INDEX",
    "CONSTRAINT",
    "FULLTEXT",
    "SPATIAL",
    "FOREIGN",
    "CHECK",
    "CREATE",
    "ALTER",
    "DROP",
}


def first_token(line):
    """取行首标识符（去反引号、转大写），用于区分列定义行与索引/约束行。"""
    return re.split(r"[\s(]", line.strip(), maxsplit=1)[0].strip("`").upper()


def is_not_column_line(line):
    stripped = line.strip()
    return not stripped or stripped.startswith(")") or first_token(stripped) in NON_COLUMN_KEYWORDS


def strip_comments(sql):
    """把 -- 行注释与 /* */ 块注释替换为空格，保留换行以维持行号。"""
    out = []
    i, n = 0, len(sql)
    in_str = False
    while i < n:
        ch = sql[i]
        if in_str:
            out.append(ch)
            if ch == "'":
                if i + 1 < n and sql[i + 1] == "'":
                    out.append("'")
                    i += 2
                    continue
                in_str = False
            i += 1
            continue
        if ch == "'":
            in_str = True
            out.append(ch)
            i += 1
            continue
        if ch == "-" and i + 1 < n and sql[i + 1] == "-":
            j = sql.find("\n", i)
            if j == -1:
                break
            out.append(" " * (j - i))
            i = j
            continue
        if ch == "/" and i + 1 < n and sql[i + 1] == "*":
            j = sql.find("*/", i + 2)
            j = n if j == -1 else j + 2
            out.append("".join("\n" if c == "\n" else " " for c in sql[i:j]))
            i = j
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def split_statements(code):
    """按分号切句（本文件无存储过程、无含分号的字符串字面量）。"""
    return [s for s in code.split(";") if s.strip()]


def line_no(text, pos):
    return text.count("\n", 0, pos) + 1


def create_table_blocks(code):
    """返回 [(表名, 起始行号, DDL块文本)]，块以分号结束。"""
    blocks = []
    pattern = re.compile(r"(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(`?[A-Za-z_][\w]*`?)")
    for m in pattern.finditer(code):
        end = code.find(";", m.start())
        end = len(code) if end == -1 else end
        blocks.append((m.group(1).strip("`"), line_no(code, m.start()), code[m.start():end]))
    return blocks


def main():
    sql_path = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).with_name("002_light_trade.sql")
    if not sql_path.is_file():
        print("[FATAL] 找不到 SQL 文件: %s" % sql_path)
        return 1

    sql = sql_path.read_text(encoding="utf-8")
    code = strip_comments(sql)
    statements = split_statements(code)
    blocks = create_table_blocks(code)
    failures = []

    print("=" * 66)
    print(" 002_light_trade.sql 静态校验")
    print(" 文件: %s" % sql_path)
    print("=" * 66)

    # [1] IF NOT EXISTS
    bad = []
    for m in re.finditer(r"(?i)\bCREATE\s+TABLE\b", code):
        tail = code[m.start():m.start() + 120]
        if not re.match(r"(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\b", tail):
            bad.append(line_no(code, m.start()))
    if blocks and not bad:
        print("[1] CREATE TABLE 均带 IF NOT EXISTS ............ PASS (%d/%d)" % (len(blocks), len(blocks)))
    else:
        print("[1] CREATE TABLE 均带 IF NOT EXISTS ............ FAIL")
        if not blocks:
            print("    未找到任何 CREATE TABLE 语句")
        for ln in bad:
            print("    第 %d 行: 缺少 IF NOT EXISTS" % ln)
        failures.append("[1]")

    # [2] 无可空列：先抹掉 NOT NULL，再找残留 NULL；同时显式拦 DEFAULT NULL
    nullable = []
    col_total = 0
    for table, start_ln, block in blocks:
        for idx, raw in enumerate(block.splitlines()):
            stripped = raw.strip()
            if is_not_column_line(raw):
                continue
            col_total += 1
            probe = re.sub(r"(?i)\bNOT\s+NULL\b", "", stripped)
            if re.search(r"(?i)\bNULL\b", probe) or re.search(r"(?i)DEFAULT\s+NULL", stripped):
                nullable.append((start_ln + idx, table, stripped))
    flagged_lines = {ln for ln, _, _ in nullable}
    stray = [m for m in re.finditer(r"(?i)DEFAULT\s+NULL", code)
             if line_no(code, m.start()) not in flagged_lines]
    if not nullable and not stray:
        print("[2] 无可空列 (NULL / DEFAULT NULL) ............. PASS (0 处 / 共 %d 个列定义)" % col_total)
    else:
        print("[2] 无可空列 (NULL / DEFAULT NULL) ............. FAIL (%d 处)" % (len(nullable) + len(stray)))
        for ln, table, text in nullable:
            print("    第 %d 行 [%s]: %s" % (ln, table, text))
        for m in stray:
            print("    第 %d 行: DEFAULT NULL" % line_no(code, m.start()))
        failures.append("[2]")

    # [3] ENGINE=InnoDB
    missing_engine = []
    for t, ln, b in blocks:
        if re.search(r"(?i)ENGINE\s*=\s*InnoDB", b):
            continue
        m = re.search(r"(?i)ENGINE\s*=\s*(\w+)", b)
        missing_engine.append((t, ln, m.group(1) if m else "缺失"))
    if blocks and not missing_engine:
        print("[3] 每个 CREATE TABLE 带 ENGINE=InnoDB ......... PASS (%d/%d)" % (len(blocks), len(blocks)))
    else:
        print("[3] 每个 CREATE TABLE 带 ENGINE=InnoDB ......... FAIL")
        for t, ln, engine in missing_engine:
            print("    第 %d 行 [%s]: ENGINE 为 %s，应为 InnoDB" % (ln, t, engine))
        failures.append("[3]")

    # [4] 分号结尾
    tail = code.rstrip()
    unterminated = []
    if tail and not tail.endswith(";"):
        last = statements[-1].strip().splitlines()[0] if statements else ""
        unterminated.append(last[:60])
    if not unterminated:
        print("[4] 每条语句以分号结尾 ......................... PASS (%d 条语句)" % len(statements))
    else:
        print("[4] 每条语句以分号结尾 ......................... FAIL")
        for s in unterminated:
            print("    末尾语句缺分号: %s ..." % s)
        failures.append("[4]")

    # [5] 不出现 CREATE DATABASE / USE
    hits = [(line_no(code, m.start()), m.group(0))
            for m in re.finditer(r"(?i)\b(CREATE\s+DATABASE|CREATE\s+SCHEMA|USE)\b", code)]
    if not hits:
        print("[5] 无 CREATE DATABASE / USE ................... PASS")
    else:
        print("[5] 无 CREATE DATABASE / USE ................... FAIL")
        for ln, kw in hits:
            print("    第 %d 行: %s" % (ln, kw))
        failures.append("[5]")

    # [6] 保留字表名必须加反引号
    unquoted = []
    for m in re.finditer(r"(?i)\b(?:CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?|ALTER\s+TABLE\s+)"
                         r"(`?[A-Za-z_][\w]*`?)", code):
        raw = m.group(1)
        name = raw.strip("`").lower()
        if name in RESERVED_TABLE_NAMES and not raw.startswith("`"):
            unquoted.append((line_no(code, m.start()), name))
    if not unquoted:
        print("[6] 保留字表名已用反引号包裹 .................. PASS")
    else:
        print("[6] 保留字表名已用反引号包裹 .................. FAIL")
        for ln, name in unquoted:
            print("    第 %d 行: %s 未加反引号" % (ln, name))
        failures.append("[6]")

    # [7] 对象齐全 + ALTER 位置在 CREATE TABLE 之后
    found_tables = [t for t, _, _ in blocks]
    alter_targets = [m.group(1).strip("`")
                     for m in re.finditer(r"(?i)\bALTER\s+TABLE\s+(`?[A-Za-z_][\w]*`?)", code)]
    problems = []
    for t in EXPECTED_TABLES:
        if t not in found_tables:
            problems.append("缺表 %s" % t)
    for t in found_tables:
        if t not in EXPECTED_TABLES:
            problems.append("多余表 %s" % t)
    if len(alter_targets) != len(EXPECTED_ALTER_TARGETS):
        problems.append("ALTER 数量为 %d，期望 %d" % (len(alter_targets), len(EXPECTED_ALTER_TARGETS)))
    for t in EXPECTED_ALTER_TARGETS:
        if t not in alter_targets:
            problems.append("缺 ALTER 目标 %s" % t)
    last_create_end = max((code.find(";", m.start()) for m in re.finditer(r"(?i)\bCREATE\s+TABLE\b", code)), default=-1)
    first_alter = re.search(r"(?i)\bALTER\s+TABLE\b", code)
    if first_alter and first_alter.start() < last_create_end:
        problems.append("存在排在 CREATE TABLE 之前的 ALTER（会触发 1146）")
    if not problems:
        print("[7] 8 张新表 / 4 条 ALTER / ALTER 位于建表之后 ... PASS")
    else:
        print("[7] 8 张新表 / 4 条 ALTER / ALTER 位于建表之后 ... FAIL")
        for p in problems:
            print("    %s" % p)
        failures.append("[7]")

    json_defaults = len(re.findall(r"(?i)DEFAULT\s*\(\s*JSON_(?:ARRAY|OBJECT)\s*\(\s*\)\s*\)", code))
    print("-" * 66)
    print("统计: 新增表 %d 张 | ALTER %d 条 | 列定义 %d 个 | JSON 默认值 %d 个 | 语句 %d 条 | 文件 %d 行"
          % (len(blocks), len(alter_targets), col_total, json_defaults,
             len(statements), len(sql.splitlines())))

    if failures:
        print("结果: FAIL (%d 项未通过: %s)" % (len(failures), ", ".join(failures)))
        return 1
    print("结果: ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
