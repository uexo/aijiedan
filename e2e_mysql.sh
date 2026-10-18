#!/usr/bin/env bash
# LinkZam MySQL 模式端到端冒烟: 重置库 -> 构建 -> 起服务 -> 跑全链路冒烟 -> 停服务。
# 用法(在仓库根目录执行):
#   bash e2e_mysql.sh
#   PYTHON=/path/to/venv/python bash e2e_mysql.sh   # 若默认 python 无 pymysql
#
# 密钥安全: 本脚本不硬编码任何密码。它从 .env(已 gitignore) 或环境变量读取:
#   LINKZAM_DB_PASSWORD  (db_reset.py 必须)
#   LINKZAM_DB_DSN       (服务必须; 缺省按 LINKZAM_DB_HOST/PASSWORD 拼装)
#   LINKZAM_DB_HOST      (缺省 192.168.3.18)
# 没有 .env 时, 把 .env.example 复制为 .env 并填值即可。
set -euo pipefail
cd "$(dirname "$0")"

# 载入本地 .env(gitignore, 不进仓库); 不存在则忽略
if [ -f .env ]; then set -a; . ./.env; set +a; fi

: "${LINKZAM_DB_PASSWORD:?请设置 LINKZAM_DB_PASSWORD(或复制 .env.example 为 .env 填写)}"
: "${LINKZAM_DB_HOST:=192.168.3.18}"
: "${LINKZAM_DB_DSN:=root:${LINKZAM_DB_PASSWORD}@tcp(${LINKZAM_DB_HOST}:3306)/linkzam?charset=utf8mb4&parseTime=true}"

PY="${PYTHON:-python}"

echo "== 1. 重置 MySQL(干净基线) =="
"$PY" db_reset.py

echo "== 2. 构建 =="
go build -o linkzam.exe .

echo "== 3. 启动服务(后台) =="
LINKZAM_DB_DSN="$LINKZAM_DB_DSN" ./linkzam.exe > mysql_mode.log 2>&1 &
SRV=$!
trap 'MSYS_NO_PATHCONV=1 taskkill /F /IM linkzam.exe 2>/dev/null || kill $SRV 2>/dev/null || true' EXIT

for i in $(seq 1 15); do
  code=$(curl -s -m 2 -o /dev/null -w "%{http_code}" http://localhost:8080/api/v1/health 2>/dev/null || true)
  if [ "$code" = "200" ]; then echo "服务就绪(${i}s)"; break; fi
  sleep 1
done

echo "== 4. 全链路冒烟 =="
"$PY" smoke_test.py
rc=$?
echo "== 冒烟退出码: $rc =="
exit $rc
