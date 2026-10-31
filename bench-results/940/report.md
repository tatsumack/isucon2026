# ベンチ #940 分析 (score 4129 / PASS, ブランチ setup/measurement)

計測の失敗: pprofは取得できなかった (直前の計測と重なり "cpu profiling already in use")。alp / vmstat / pidstat / pg_stat_statements は取得できた。

## 概況
- どのサーバもCPUは張り付いていない。asia1 アプリCPU 約41% (2コア中)、asia2 DB CPU us+sy 約50%、europe1/america1 はほぼアイドル (5%)
- ボトルネックの所在: **DBのクエリ時間 (インデックス不足とロック待ち)** と **europe/america→asia2(DB) の往復遅延 約100ms**
  - europe1/america1 → asia2 の ping RTT は 100.8ms (asia1 → asia2 は 1.0ms)
  - そのため europe/america のAPIは軒並み 1〜4秒 (例: POST /api/posts avg asia1 1.48s / europe1 3.83s / america1 3.71s)
  - ベンチ出力では「グランプリ: アジア 16 人, ヨーロッパ 0 人, アメリカ 3 人」(europeとamericaが少ない理由は推測: 遅延による遅れ投稿)
- 前回比: 初回のため比較なし (ベースライン #187 は 2677)

## ボトルネック上位
### 1. 画像取得クエリが posts / realmojis をフルスキャンしている
- 根拠: pg_stat_statements 1位。835回 / total 364s / mean 436ms / DB時間の36.6%。GET /api/images/:id は asia1 で 887回 sum 401s
- 原因箇所: go/image_api.go:68-84 の EXISTS (`posts.image_id`, `realmojis.image_id`)。sql/001-schema.sql に posts(image_id) と realmojis(image_id) のインデックスがない (posts 62.5万行、realmojis 120万行)
- 対策案: `CREATE INDEX ON posts(image_id)` と `CREATE INDEX ON realmojis(image_id)` を追加する
- 期待効果: 大。DB時間の1/3を占めており、DBのCPUとロックの保持時間も下がる
- リスク: 小。インデックスの追加のみで、レスポンスは変わらない。/initialize で消えないこと (reset.sql はTRUNCATE/DELETEのみなので残る) は要確認

### 2. users行ロック・posting_windowsロックの待ち
- 根拠: `SELECT id FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE` 821回 mean 203ms、`posting_windows ... FOR UPDATE` 150回 mean 902ms、`LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE` 44回 mean 2159ms
- 原因箇所: go/session.go:131 (ログインのたびにusers行ロック)、go/post_api.go:95,107 (投稿時にusers行とposting_windowsをロック)、go/admin_posting_window_api.go:46 と go/user_api.go:93 (usersテーブル全体をロック)
- 対策案: まず1のインデックスでトランザクション全体を短くし、それでも残るなら、ロックの範囲を減らす (テーブルロックを行ロックやユニーク制約に置き換えるなど)
- 期待効果: 中〜大 (推測: 投稿期間の開始時の全体ロックが投稿を止めている)
- リスク: 大。並行時の整合性 (二重投稿、退会との競合など) に関わるので、仕様を確認してから変える

### 3. europe/america から DB への往復回数が多い
- 根拠: RTT 100ms。europe1/america1 の各APIは、クエリ数 × 100ms 程度遅い (POST /api/friend-requests europe1 avg 3.17s、PUT /api/follows/:id 3.9〜4.8s)
- 原因箇所: 各ハンドラがトランザクション内でクエリを逐次発行している (例: go/post_api.go のcreatePost)
- 対策案: 1リクエストあたりのクエリ数を減らす (まとめる・不要なSELECTを省く)。リージョンごとにキャッシュやDBを持つ構成は効果が大きいが、整合性の設計が必要
- 期待効果: 大 (europe/americaのスコアに直結する。推測)
- リスク: 中〜大。構成変更は初期化と整合性の作り直しが必要

### 4. 通知 (SSE) が500msごとにDBをポーリングしている
- 根拠: GET /api/notifications は asia1 111回 sum 1096s (1接続 約10秒)。1回のポーリングで getUser と notifications の2クエリを発行する
- 原因箇所: go/notification_api.go:38-80
- 対策案: notifications に (user_id, created_at) のインデックスを追加する。将来的にはアプリ内で通知を配信し、ポーリングをやめる
- 期待効果: 中 (通知の遅れは、時間内投稿の判定に効くと推測)
- リスク: 中。複数台構成ではアプリ内の配信だけでは他リージョンに届かない

### 5. friendships の OR 検索で user_high_id 側にインデックスがない
- 根拠: `WHERE user_low_id=$1 OR user_high_id=$1` 949回 mean 41ms。IN版も上位に多数
- 原因箇所: sql/001-schema.sql:115-123 (UNIQUE(user_low_id,user_high_id) のみ)
- 対策案: `CREATE INDEX ON friendships(user_high_id)` を追加する
- 期待効果: 小〜中
- リスク: 小
