#!/bin/bash
# ベンチ実行中の計測をまとめて行う (各サーバ上で実行する)
# 使い方: bench-capture.sh [計測秒数] [app|db]
#   app: アプリサーバ (asia1/europe1/america1)。vmstat, pidstat, pprof, alp
#   db : DBサーバ (asia2)。vmstat, pidstat, pg_stat_statements
#   全台で、プロセス別のディスクの読み書き (pidstat -d、1秒ごと) も pidstat_disk.txt に取る
#   app は、優先制御の枠ごとの待ち (priority: group=... の行) を priority.log に取る (asia1 だけに出る)
#   実行直後にベンチを開始する。終了後に結果ディレクトリのパスを出力する
set -eu

APP_BINARY=/home/isucon/webapp/go/isureal
PPROF_URL=http://localhost:6060/debug/pprof/profile
# パスパラメータを含むURIをまとめる (server.goのルーティングと揃える)
ALP_MATCHING_GROUPS='^/api/users/[^/]+$,^/api/posts/[^/]+$,^/api/posts/[^/]+/comments$,^/api/posts/[^/]+/realmojis$,^/api/feeds/[^/]+$,^/api/memories/[^/]+$,^/api/comments/[^/]+$,^/api/realmojis/[^/]+$,^/api/images/[^/]+$,^/api/friend-requests/[^/]+/[^/]+$,^/api/relationships/[^/]+$,^/api/friendships/[^/]+$,^/api/follows/[^/]+$,^/api/blocks/[^/]+$,^/api/admin/users/[^/]+/certification$,^/assets/.+$'
export PATH=/home/isucon/local/go/bin:/usr/local/go/bin:/home/isucon/go/bin:${PATH}
PSQL="sudo -u postgres psql -d isureal -X -q"

DURATION=${1:-150}
ROLE=${2:-app}
OUT=/home/isucon/bench/$(date "+%H%M%S")
mkdir -p ${OUT}
START=$(date "+%Y-%m-%d %H:%M:%S")

# 前回までのログ・統計が混ざらないようにリセットする
if [ "$ROLE" = "app" ]; then
    sudo truncate -s 0 /var/log/nginx/access.log
else
    $PSQL -c "SELECT pg_stat_statements_reset()" > /dev/null
fi

# 計測 (CPU全体 / プロセス別CPU / プロセス別ディスク / Goのプロファイル)
vmstat 1 ${DURATION} > ${OUT}/vmstat.txt &
pidstat -u 5 $((DURATION / 5)) > ${OUT}/pidstat.txt &
# 他ユーザーのプロセス (nginx, postgres, journald) の /proc/<pid>/io を読むため sudo で動かす
sudo pidstat -d 1 ${DURATION} > ${OUT}/pidstat_disk.txt &
if [ "$ROLE" = "app" ]; then
    curl -s -o ${OUT}/cpu.pb.gz "${PPROF_URL}?seconds=${DURATION}" || echo "pprof failed" > ${OUT}/pprof_error.txt &
fi
wait

# 集計
if [ "$ROLE" = "app" ]; then
    # 枠ごとの待ち (priority: group=... limited / waited / gave_up / avg_wait)。10秒ごとに、待ちがあった枠だけ出る。
    # 起動時の枠は journalctl -u isureal-go.service -g 'priority: split limits' で確認する
    { sudo journalctl -u isureal-go.service -o cat --since "${START}" --no-pager | grep -E 'priority(_adaptive)?:' || true; } > ${OUT}/priority.log
    sudo /usr/local/bin/alp ltsv --file /var/log/nginx/access.log --sort sum -r -m "$ALP_MATCHING_GROUPS" > ${OUT}/alp.txt
    if [ -s ${OUT}/cpu.pb.gz ]; then
        go tool pprof -top -cum -nodecount=40 ${APP_BINARY} ${OUT}/cpu.pb.gz > ${OUT}/pprof_cum.txt 2>&1
        go tool pprof -top -nodecount=40 ${APP_BINARY} ${OUT}/cpu.pb.gz > ${OUT}/pprof_flat.txt 2>&1
    fi
else
    $PSQL > ${OUT}/slow.txt <<'EOS'
SELECT calls,
       round(total_exec_time::numeric, 0) AS total_ms,
       round(mean_exec_time::numeric, 2) AS mean_ms,
       rows,
       round((100 * total_exec_time / sum(total_exec_time) OVER ())::numeric, 1) AS pct,
       left(regexp_replace(query, '\s+', ' ', 'g'), 300) AS query
FROM pg_stat_statements
ORDER BY total_exec_time DESC
LIMIT 20;
EOS
fi

echo ${OUT}
