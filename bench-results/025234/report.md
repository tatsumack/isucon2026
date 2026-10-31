# ベンチ分析 025234 (#31 実行計画11・12 + main)

- ブランチ: `codex/integrate-plan-11-12` (`12423b2`)。4台にデプロイ
- 計測: 11:52:34〜11:53:44 JST の70秒 (asia1 / europe1 / america1)。DB は pg_stat_statements の前後差分 (asia2/slow.txt)
- 結果: **PASS / score 27647** (#5119 比 +12.6%)。ベンチ1回ずつの比較なので、ぶれを含む
- ベンチ本体は 11:52:41〜11:53:39 JST で、計測の70秒に収まっている
- 比較対象: #5119 (score 24558、`84951c2`、計測150秒)。計測時間が違うので、COUNT ではなく平均と割合で比べる

## 計測で欠けているもの

- asia2 の vmstat / pidstat は取っていない (bench-capture.sh がアプリサーバ用のため)
- bench-capture.sh の `APP_BINARY` が `isucondition` のまま。pprof の集計はできている

## 概況

- ボトルネックの所在: **asia1 のアプリCPU**
  - asia1 は2コアで、vmstat の CPU 使用率は平均 76% (us 57.9 / sy 10.4)。計測の後半30秒はほぼ 100% に張り付いている。isureal が 128% (pidstat)
  - europe1 / america1 は idle 89%。#31 の転送 (実行計画11) で、重い API がすべて asia1 に集まった
- DB の待ち時間の 61% が投稿期間の advisory lock 待ち (183回、平均 1.09秒)
- 前回 (#5119) 比 (europe / america の平均応答時間)
  - `POST /api/auth/login`: 0.999s → **0.58s** (-42%)。改善
  - 2ユーザーの行ロック (`SELECT id FROM users WHERE id IN ($1,$2) ... FOR NO KEY UPDATE`): 平均 48.23ms → **6.99ms**。改善
  - `GET /api/feeds/:name`: 0.45s → 0.42s。ほぼ変わらない
  - `POST /api/posts`: 1.56s → 1.57s。変わらない (asia1 の CPU 待ちで相殺されていると考えられる。推測)
  - advisory lock: 平均 301ms → **1093ms**。悪化
- `GET /api/notifications` は平均 7.5〜10秒で SUM が最大だが、待ち受け型の API のため、遅さの原因ではないと考えられる (推測。実装は読んでいない)

## ベンチマーカーの行動 (Stdout より)

| | 5日目の人数 | グランプリの人数 | #5119 のグランプリ |
|---|---:|---:|---:|
| アジア | 39 | **3** | 71 |
| ヨーロッパ | 66 | 45 | 5 |
| アメリカ | 73 | 46 | 5 |

- europe / america は日ごとに人数が増えるようになった (#5119 では 7人・6人のまま増えなかった)。転送で europe / america の応答が速くなった効果と考えられる
- 逆にアジアのグランプリは 71人 → 3人に減った。asia1 の CPU が張り付いた計測後半と時期が重なるため、アジアのユーザーが asia1 の遅さで離脱した可能性が高い (推測。アジアのユーザー別の応答時間は見ていない)
- asia1 の CPU を空ければ、アジアの人数を落とさずに europe / america の伸びを保てる見込み

## ボトルネック上位

### 1. 投稿の JPEG デコード (asia1 CPU)
- 根拠: asia1 pprof の 45.4% が `image/jpeg.Decode`。ほぼすべてが `createPostHandler` から呼ばれている (47.8%)。#5119 では 31.3%
- 原因箇所: `go/post_api.go:79`。画像のサイズ (4096px 以下) を確かめるためだけに、画像全体をデコードしている
- 対策案: `jpeg.Decode` を `jpeg.DecodeConfig` に置き換え、ヘッダーから幅と高さだけ読む。壊れた JPEG を 400 にする検証が必要かは、マニュアルで確認する
- 期待効果: 大。asia1 の CPU の約半分が空く。転送で europe / america の投稿も asia1 で処理されるため、効果はさらに大きくなる
- リスク: `DecodeConfig` はヘッダーしか読まないので、本文が壊れた JPEG が通るようになる。ベンチマーカーが壊れた画像を送って 400 を期待しているなら FAIL になる。`profile_image_api.go:24` と `realmoji_api.go:58` も同じ形

### 2. 画像署名の計算 (asia1 CPU)
- 根拠: asia1 pprof の 37.1% が `computeImageSignature` (#5119 は 48.0%)。主に `listMemories` (29.5%) → `loadMemoryInteractions` → `newRealMojiResponse` (19.1%) と、`newPostResponse` (17.4%) から呼ばれている
- 原因箇所: `go/image_signature.go:64`。HMAC-SHA256 を1000回繰り返す。キャッシュは (鍵, 画像ID) ごとに直近の期限1つだけ (`image_signature.go:46`)
- 対策案: makino の M3 / action-plan.md 施策3 (署名の使い回し) を進める。キャッシュの当たり方 (期限が1分ごとに変わるため、1分たつと外れる) を見直す
- 期待効果: 大
- リスク: 期限の扱いを変えると、URL の有効期限の仕様に触れる。マニュアルの期限の条件を確認する

### 3. 投稿期間の advisory lock 待ち (DB)
- 根拠: `pg_advisory_xact_lock(hashtext('posting_window:' || region))` が 183回、平均 1092ms、DB 時間の 60.9% (#5119 は平均 301ms、18.4%)
- 原因箇所
  - 登録: `go/user_api.go:93` でリージョンのロックを取り、トランザクションの最後まで持つ。register は転送の対象外なので、europe1 / america1 からは DB との往復 (約100ms) ごとにロックを持ったまま待つ。europe / america の register は平均 2.15秒 (asia1 は 0.38秒)
  - 投稿期間の作成: `go/admin_posting_window_api.go:47` でロックを取り、`:72` の通知の一括 INSERT (14回、平均 826ms、1回あたり約1800行) が終わるまで持つ
- 対策案: register も asia1 へ転送してロックを持つ時間を短くする。または、通知の INSERT をロックの外に出す (登録との前後関係の保ち方は別に考える)
- 期待効果: 中。register と投稿期間の作成が速くなる
- リスク: ロックの目的は「投稿期間の開始時点の active ユーザーに通知する」こと。ロックの範囲を変えると、通知の漏れや重複が起きうる

### 4. export のクエリ (DB)
- 根拠: 65回、平均 439ms、DB 時間の 8.7% (#5119 は平均 381ms)。asia1 pprof の `exportPostsHandler` は 8.0%
- 原因箇所: `exportPostsHandler` (ファイルの行は未確認)。クエリが画像データ (`i.data`) まで JOIN して読んでいる
- 期待効果: 小〜中

### 5. image_objects の読み出し (DB)
- 根拠: `SELECT data FROM image_objects WHERE id = $1` が 3862回、平均 4.48ms、5.3% (#5119 は 3.11ms)
- 対策案: 画像は不変なので、asia1 のメモリか nginx でキャッシュする (`perf/image-fetch-cache` ブランチが該当しそう。未確認)
- 期待効果: 小

## 確認したこと / していないこと

- 確認した: 3台の alp / vmstat / pidstat / pprof、DB のクエリ差分、上記の原因箇所のコード
- 確認していない
  - asia2 の CPU / I/O
  - asia1 で転送されてきたリクエストの応答時間 (アプリに直接届くので、asia1 の nginx ログに載らない)
  - asia1 の `PUT /api/me/profile-image` が 126回中 108回 4xx になっている理由 (europe / america は 0回)。ベンチマーカーの想定どおりかはわからない
