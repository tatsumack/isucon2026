# ベンチ #2540 分析 (score 7427 / PASS, ブランチ analysis/benchmarker-timeline)

計測の注意: bench-capture.sh (70秒) を 11:03:56 に開始したが、ベンチ開始が 11:04:18、負荷走行は 11:04:50〜11:05:48。
**alp / pprof / pg_stat_statements / vmstat は負荷走行の最初の約16秒と、その前の準備フェーズしか含まない。**
行動分析 (timeline/report.txt) は access.log 全体 (ベンチ全体) から作った。

## 概況
- スコア 7427 (前回 #940 は 4129)。違いは main に入ったインデックス (sql/002-performance-indexes.sql) が効いた分と考えられる (推測: 他に変更なし)
- CPUはどこも張り付いていない。asia1 us 12% / asia2 us+sy 13%, wa 5% / europe1, america1 はほぼアイドル
- ボトルネックの所在: **DBのロック待ち** と **europe/america → asia2 (DB) の往復遅延**
  - pg_stat_statements の上位3件がロック: `users FOR NO KEY UPDATE` (448回 mean 103ms, 28.5%)、`LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE` (37回 mean 909ms, 20.7%)、`posting_windows ... FOR UPDATE` (43回 mean 779ms, 20.6%)
  - POST /api/posts: asia1 avg 0.57s / europe1 avg 4.7s (max 7.6s) / america1 avg 4.6s
  - ベンチ出力「グランプリ: アジア 37 人, ヨーロッパ 1 人, アメリカ 2 人」。europe/america は各 6〜7 人いるのに最終日の検証を通過したのは1〜2人。europe/america は配点1.5倍なので、ここを通すのが一番大きい

## ベンチマーカーの行動 (timeline/report.txt より)
- **投稿期間**: 管理者が `POST /api/admin/posting-window` を **3秒ごと** に asia→europe→america の順で開く (計18回 = 6日 × 3リージョン)。同じリージョンの次の期間は約9秒後
- **投稿の流れ**: `GET /api/notifications` (SSE で通知を待つ) → `GET /api/posting-window/current` (86%が次に POST) → `POST /api/posts` → **94.5% が直後(1秒以内)に `GET /api/feeds/friends`**。これが「投稿後の友達フィードの検証通過」(10点、最終日20点)。1ユーザー1期間1回なので、投稿〜フィードを期間内に終えられるかが直接スコアになる
  - notifications の reqtime: asia1 avg 7.9s / europe1 10.4s / america1 11.3s。通知が届くまでの待ちを含む
  - 同一ユーザーの POST /api/posts の間隔 p50 10.4s (= 投稿期間1回に1投稿)
- **閲覧**: ログイン直後に `GET /api/memories?month` (24.7%) → 画像を連続取得。`GET /api/posts/me/current` の次は 97% が画像。画像の取得は全体で最多 (1861件、2点)
- **攻撃・不正リクエスト** (加点されない。安く返すと良い):
  - `POST /api/auth/login` の 401 が 559件、その後はほぼ即リトライ (556件)。ログインだけを繰り返すhostが13。続く 403 (24件) は IP BAN と思われる (go/ip_ban.go)
  - `GET /api/images` の 403 (237件、不正な署名) → 即リトライ218件。画像だけを取るhostが11
  - `PUT /api/me/profile-image` の 400 (78件) → リトライ、22ユーザーがこれを最後に止まる
- **負荷の上がり方**: アクティブユーザーは5秒ごとに 35→62人程度で、リクエスト数は 440→740/5秒。負荷が急に増える区間はない。負荷走行は初期化から約90秒で終わる
- **書き込み後の確認**: 投稿→フィード、RealMoji→RealMoji一覧、コメント→フィード/コメント一覧 が1秒以内に来る。キャッシュを入れるなら書き込み時に必ず無効化する必要がある

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より、基準値)
後から bench-timeline.py に追加した集計 (docs/action-plan-makino.md の M2)。同じ access.log から作った。

| リージョン | 投稿数 | 期間内に完了 | 前回 | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 103 | 103 (100%) | - | 1.53s | 1.03s |
| europe | 30 | **19 (63%)** | - | 3.24s | 5.15s |
| america | 18 | **12 (67%)** | - | 3.99s | 4.69s |
| 最後の期間 (3リージョン計) | 44 | 41 (93%) | - | - | - |

- 投稿数は ユーザー × 期間。同じ期間で削除してから再投稿した3件は数えない。最後の期間 (t=80.28 に3リージョン同時に開く) は終了時刻がログに無いので、ログの最後 (t=147.46) までに完了したものを別に数えた。内訳 asia 36/36、europe 2/5、america 3/3
- 期間の境界は admin の `POST /api/admin/posting-window` の**完了時刻**にした。このリクエストは 0.11〜3.37秒かかっていて (users のロック待ち、ボトルネック1)、期間の開始時刻はその途中で決まる。リクエストの開始を境界にすると europe 13/30 (43%)、america 8/18 (44%) になる。実際の値はこの間にある
  - 完了時刻を使うと境界が少し遅れる例が1件ある: t=82.47 の asia の投稿は、admin の応答 (t=82.68) より前に新しい期間で受理されている
- 期間の割り当て: asia → europe → america (スコア 181、2位の asia → america → europe は 143)。region はログに無いので推定だが、全18期間で期間開始から 0.62秒以内に そのリージョンの SSE が閉じている
- europe / america の内訳 (p50): 通知 (期間開始 → SSE が閉じる) 0.76s / 0.55s、反応 (SSE → 投稿開始) 2.41s / 3.11s、投稿→フィード 5.15s / 4.69s。asia はそれぞれ 0.34s / 1.10s / 1.03s。通知はほぼ差がなく、投稿の前後のリクエスト (posting-window/current、POST /api/posts、友達フィード) で遅れている
- 期間の開始自体も遅れている: admin のリクエストが長い (最大3.4秒) と、その分だけ期間の開始が遅れる。同じリージョンの次の期間は予定どおり約9秒後に始まるので、期間が短くなる (例: europe の t=53.65〜59.63 は6秒しかなく、0/3)

## ボトルネック上位
### 1. 投稿期間の開始時に users テーブル全体をロックしている
- 根拠: `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE` 37回 mean 909ms。POST /api/admin/posting-window asia1 max 3.2s。3秒ごとに開くので、毎回ほかの users の行ロック (投稿、ログイン、友達申請など) を止めている
- 原因箇所: go/admin_posting_window_api.go:46 (テーブルロック)、:72-75 (同じトランザクション内でリージョン全ユーザー分の notifications を INSERT。7回 mean 628ms / 14674行)
- 対策案: テーブルロックをやめ、posting_windows の行ロックや advisory lock でリージョン単位に直列化する。notifications の一括 INSERT を軽くする (通知をユーザー別の行で持たず、投稿期間から導出するなど)
- 期待効果: 大。投稿期間の開始直後の POST /api/posts が待たされなくなり、期間内の投稿〜フィード検証が通りやすくなる
- リスク: 中。投稿期間の重複防止と、退会・登録との整合性 (誰に通知を出すか) を保つこと。リモートに `worktree-fix-posting-window-lock` ブランチがあるので、同じ施策を誰かが進めていないか確認する

### 2. POST /api/posts が posting_windows の行を FOR UPDATE で取り、全員が直列になる
- 根拠: `SELECT ... FROM posting_windows WHERE region=$1 AND ended_at IS NULL FOR UPDATE` 43回 mean 779ms。同じリージョンの投稿が全員同じ1行を取り合う
- 原因箇所: go/post_api.go:107-109 (見つからないと同じクエリをもう1回投げる)。:95 の users FOR NO KEY UPDATE とあわせてロックを長く持つ
- 対策案: 期間の行は `FOR SHARE` (または ロックなしで読み、投稿の一意制約で重複を防ぐ) にする
- 期待効果: 大。投稿の待ち時間が減る (特に投稿が集中する期間の開始直後)
- リスク: 中。期間の終了 (ended_at の更新) と投稿が競合したときの判定 (遅れ投稿かどうか) を変えないこと

### 3. europe/america のリクエストが asia2 との往復を何度も繰り返す
- 根拠: europe1/america1 は CPU 1% なのに、POST /api/auth/login avg 1.4s、GET /api/feeds avg 1.4s、POST /api/posts avg 4.7s。#940 で RTT 100ms を確認済み。1リクエストで10往復以上していると推測
- 原因箇所: session.go:52 のセッション取得 (全リクエスト)、notification_api.go:39-55 (500msごとに getUser と通知の2クエリ)、post_api.go の投稿トランザクション (BEGIN/ロック/SELECT/INSERT/COMMIT がそれぞれ1往復)
- 対策案: クエリ数を減らす (JOIN でまとめる、トランザクションをやめられる所はやめる)、セッションやユーザー情報をアプリ内にキャッシュする
- 期待効果: 大。europe/america は配点1.5倍で、グランプリ通過が 1〜2人/6〜7人に留まっている
- リスク: 中。キャッシュは「書き込み後の読み取りに反映」「リージョンをまたぐ場合も同じ」という規定があるので、退会・BAN・ログアウトでの無効化に注意

### 4. 通知 (SSE) が 500ms ごとに DB を2回ポーリングしている
- 根拠: GET /api/notifications は同時に約50本つながる。1本あたり毎秒4クエリ。europe/america ではこの往復も 100ms ずつかかり、通知が届くのが遅れる (推測)
- 原因箇所: go/notification_api.go:37-80
- 対策案: アプリ内で投稿期間の開始を検知して待っている接続にまとめて知らせる (ただし開始は asia1 に来るので、他リージョンへの伝え方が必要)。最低限、getUser を毎回やらない、間隔を短くして通知の遅れを減らす
- 期待効果: 中。通知が早く届けば、期間内に投稿できるユーザーが増える
- リスク: 中。退会・リージョン変更時に接続を切る挙動 (user.Region != session.Region) を保つこと

### 5. 不正なログインと不正な署名の画像リクエストが多い
- 根拠: 401 のログイン 559件 (全ログインの52%)、403 の画像 237件。どちらも即リトライされる
- 原因箇所: go/user_api.go:136 (ログインで users を FOR NO KEY UPDATE)、go/image_api.go の署名検証で DB を引いている (`SELECT u.secret_key_hash FROM image_objects ...` 595回)
- 対策案: ログイン失敗の判定をロックなしで先に行う、署名検証に必要な値をキャッシュする
- 期待効果: 小〜中。加点されないリクエストなので、DBとロックの負荷を減らす効果のみ
- リスク: 小〜中。IP BAN の判定 (失敗回数の数え方) と 403 を返す条件を変えないこと
