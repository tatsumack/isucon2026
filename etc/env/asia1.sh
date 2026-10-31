ISUREAL_REGION=asia
ISUREAL_DB_HOST=192.168.4.10
ISUREAL_DB_PORT=5432
ISUREAL_DB_USER=isureal
ISUREAL_DB_PASSWORD=isureal
ISUREAL_DB_NAME=isureal
ISUREAL_TRUSTED_PEERS=192.168.2.10,192.168.3.10
ISUREAL_SIGNING_URLS=http://192.168.2.10:8080,http://192.168.3.10:8080
ISUREAL_LOG_LEVEL=info
ISUREAL_PRIORITY_LIMIT=40
ISUREAL_PRIORITY_MAX_WAIT=1s
# adaptive (PR #80、priority_adaptive.go): 全体の上限 LIMIT + LIMIT/8 (40 なら 45) を維持し、空き枠をグループ間で貸し合う。#35726 の split (16 / 8 / 16) は、貸し借りができず、最後の期間の完了率が adaptive (#35183) より低かった。split に戻すなら、SCOPE を split、LIMIT を 32 にする (下の個別指定の枠が使われる)
ISUREAL_PRIORITY_SCOPE=adaptive
# split の枠 (投稿の経路 / heavy / その他)。#34626 では 16 / 8 / 8 で、その他が詰まった (ピークの平均待ち 8s)。その他だけ 16 に増やす (#34626 の対策案 1)
ISUREAL_PRIORITY_CRITICAL=16
ISUREAL_PRIORITY_HEAVY=8
ISUREAL_PRIORITY_OTHER=16
# PUT /api/me/profile-image を入れる枠 (heavy / critical / other / exempt)。新規ユーザーの入口で軽いので、heavy (Memories・export と同じ枠) から出す
ISUREAL_PRIORITY_PROFILE_IMAGE=critical
ISUREAL_SESSION_CACHE=1
