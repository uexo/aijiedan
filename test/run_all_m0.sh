#!/bin/bash
# M0 全套回归。每条用例前重置数据库并干净重启服务。
#
# 为什么必须一条一重置: 种子需求是共享的(T-DEMO-001..003),
# 认领一张就少一张。连着跑, 后面的用例拿不到种子, 会在准备阶段就失败,
# 表现成"新增的功能把老用例弄挂了", 排查方向完全错。
#
# 用法(在仓库根目录, 服务不必事先启动):
#   bash test/run_all_m0.sh                 # MySQL 真源模式
#   ZANBUDDY_DB_DSN= bash test/run_all_m0.sh  # 内存模式
#
# 需要: zanbuddy.exe 已编译(启动服务用编译产物, 不用 go run)、
#       ZANBUDDY_DB_PASSWORD、db_reset.py 所在的托管 venv。
set -u
cd "$(dirname "$0")/.." || exit 1

PY="${PY:-D:/soft/anaconda/python.exe}"          # 跑用例的解释器
PYV="${PYV:-C:/Users/admin/.workbuddy/binaries/python/envs/default/Scripts/python.exe}"
PORT="${ZANBUDDY_PORT:-8082}"
export ZANBUDDY_PORT="$PORT"
MYSQL="${ZANBUDDY_DB_DSN-1}"                      # 空串 = 内存模式

TESTS="${*:-test_m0_v2 test_m0_hall test_m0_publish test_m0_matches test_m0_claim test_m0_confirm test_m0_deliver test_m0_accept test_m0_withdraw}"
FAILED=""

for t in $TESTS; do
  PID=$(netstat -ano | grep ":$PORT" | grep LISTENING | head -1 | awk '{print $5}')
  [ -n "${PID:-}" ] && taskkill //F //PID "$PID" >/dev/null 2>&1
  sleep 0.4

  # 内存模式的种子在进程启动时重建, 不需要重置库
  if [ "$MYSQL" != "0" ] && [ -n "${ZANBUDDY_DB_DSN:-}" ]; then
    "$PYV" db_reset.py --demo >/dev/null 2>&1 || { echo "RESET FAILED"; exit 1; }
  fi

  ./zanbuddy.exe > "test/_run_$t.log" 2>&1 &
  SRV=$!
  for _ in $(seq 1 30); do
    curl -s --noproxy '*' -o /dev/null "http://127.0.0.1:$PORT/api/v2/healthz" && break
    sleep 0.3
  done

  OUT=$("$PY" "test/$t.py" 2>&1)
  printf "%-16s %s\n" "$t" "$(echo "$OUT" | grep -E 'passed|通过|失败' | tail -1)"
  if echo "$OUT" | grep -qE "Traceback|FAIL|失败 [1-9]"; then
    FAILED="$FAILED $t"
    echo "$OUT" | tail -8 | sed 's/^/    /'
  fi
  kill $SRV >/dev/null 2>&1
  sleep 0.2
done

echo "===================="
if [ -n "$FAILED" ]; then
  echo "FAILED:$FAILED"
  exit 1
fi
echo "ALL GREEN"
