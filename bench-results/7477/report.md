# ベンチ分析 #7477 (PR #37 画像署名の HMAC の軽量化)

- ブランチ: `perf/image-signature-hmac` (`b173f0a`)。4台にデプロイ
- 計測: 03:17:45〜03:20:15 UTC の150秒 (4台)。ベンチの負荷走行 (t=0〜80秒) は計測に収まっている
- 結果: **PASS / score 46273** (前回 #6150 は 46215。+0.1%)。ベンチ1回ずつの比較なので、ぶれを含む
- 比較対象: #6150 (PR #32 JPEG 検証の軽量化、`c7256cb`)。コードの差分は `go/image_signature.go` のみ
- 計測の欠け: なし (4台とも pprof / alp / vmstat / pidstat / slow.txt あり)

## 概況

- ボトルネックの所在: **asia1 のアプリCPU (画像署名)** と **DB (advisory lock 待ち)**
- 狙いどおりの効果は出た
  - asia1 の `computeImageSignature`: 56.31s (70.95%) → **47.71s (66.57%)**、-15%
  - 署名1回あたりの時間 (`computeImageSignature` ÷ `sha256.blockAVX2`): 1.458 → 1.313 で、約10%減。事前の計測 (asia1 で 477µs → 431µs) と合う
  - asia1 のベンチ中の CPU: idle 24.1% → 29.6%、idle 5% 未満の秒数 8秒 → 3秒
  - asia1 の平均応答時間: feeds 0.237s → 0.150s、images 0.083s → 0.055s、memories 0.199s → 0.137s、posts 0.390s → 0.279s
- **スコアは変わらなかった**
  - ベンチマーカーのユーザー数のピークが 349 → 258 に減った。asia のリクエストは 20846 → 19691、europe / america は増えた (5911 → 6587、4430 → 4660)
  - 投稿 (`INSERT INTO posts`) は 908 → 749。asia の期間内の投稿が 304 → 229 に減った
  - ユーザーが増えなかった理由は、このデータからはわからない。ポータルの Stdout (リージョン・日ごとの人数、グランプリ) を見る必要がある
  - asia1 の CPU が空いても、スコアの上限を決めている要因は別にある可能性がある (推測)
- 悪化: `POST /api/auth/register` europe1 2.344s → 3.525s、asia1 0.280s → 0.513s。advisory lock 待ちが 平均 953ms → **1295ms**

## ベンチマーカーの行動 (timeline/report.txt より)

- 流れは前回と同じ。通知 (SSE) → `GET /api/posting-window/current` → `POST /api/posts` → `GET /api/feeds/friends`
- 投稿期間は asia → europe → america の順に約3秒ずつずれて開き、同じリージョンは約9秒ごと。18期間
- 管理者の `POST /api/admin/posting-window` の reqtime: p50 1.10s / 最大 **4.76s** (前回 最大 5.53s)。後半の期間ほど長い
  - 最後の america の期間は、開始が t=70.07 と遅れ、投稿は5件だけ (前回 12件)。最後の期間は1件20点なので影響が大きい
- 加点されない 4xx は前回と同じ傾向 (失敗ログイン、プロフィール画像の不正画像など)

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)

#6150 の数値は、手元に残っていた #6150 の access.log に同じ集計 (bench-timeline.py) をかけて出した。

| リージョン | 投稿数 | 期間内に完了 | 前回 (#6150) | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 229 | 221 (97%) | 303 / 304 (100%) | 2.02s | 0.34s (前回 0.55s) |
| europe | 155 | 150 (97%) | 155 / 156 (99%) | 1.86s | 0.82s (前回 0.78s) |
| america | 133 | 131 (98%) | 140 / 141 (99%) | 2.10s | 0.82s (前回 0.94s) |
| 最後の期間 (3リージョン計) | 176 | 175 (99%) | 234 / 241 (97%) | - | - |

- 期間の割り当て: asia → europe → america (419)。2位の america → europe → asia は 402 で差が小さいため、リージョン別の数値は推定扱い。前回も 443 対 405
- 完了率はどのリージョンも 97% 以上で、取りこぼしはほぼない。スコアの差は完了率ではなく、投稿の件数 (ユーザー数) から来ている
- asia の取りこぼし (8件) は t=45〜54 の期間に集中している (64件中56件)

## ボトルネック上位

### 1. 画像署名の計算 (asia1 CPU)
- 根拠: asia1 pprof の 66.57% が `computeImageSignature` (47.71s)。呼び出し元は `listMemories` 47.19%、`newPostResponse` 35.82%
- 原因箇所: `go/image_signature.go` の `computeImageSignature` (HMAC-SHA256 を1000回)。呼び出しは `go/memory_api.go:89` (投稿)、`:127` (RealMoji)
- asia1 (Xeon 8275CL) には SHA-NI がなく、同じ署名が europe1 / america1 (Xeon 8488C、SHA-NI あり) の約3.3倍かかる (asia1 431µs、europe1 136µs)。europe1 / america1 の CPU はベンチ中も 98% 空いている
- 対策案
  - 署名の計算を europe1 / america1 に任せる。例: europe / america 向けの Memories を転送の対象から外す。または asia1 から署名をまとめて依頼する
  - Memories で返す署名の数を減らす (M3)
- 期待効果: 中〜大。ただし今回、署名の CPU を15%減らしてもスコアは変わらなかったので、asia1 の CPU がスコアの上限を決めているかは未確認
- リスク: 転送の構成 (実行計画11) に関わる。署名の値と期限の形式は変えてはいけない

### 2. 投稿期間の advisory lock 待ち (DB)
- 根拠: `pg_advisory_xact_lock` が 279回、平均 **1295ms**、DB 時間の 50.5% (前回 353回、平均 953ms、49.9%)。管理者の投稿期間の作成は最大 4.76s、register は europe1 で平均 3.525s
- 原因箇所
  - 投稿期間の作成: `go/admin_posting_window_api.go:47` でロックを取り、通知の一括 INSERT (17回、平均 684ms、1回あたり約1760行) が終わるまで持つ
  - 登録・リージョン移動・退会: `go/user_api.go:93`、`:221`、`go/account_deletion_api.go:32` で同じロックを取る
- 対策案
  - 登録・移動・退会は `pg_advisory_xact_lock_shared` (共有ロック) にし、投稿期間の作成だけ排他ロックのままにする。登録どうしが待ち合わなくなる
  - 通知の一括 INSERT をロックの外に出す (通知の漏れ・重複が起きない方法を別に考える)
- 期待効果: 中〜大。最後の期間の開始が早くなれば、1件20点の投稿が増える
- リスク: ロックの目的は「投稿期間の開始時点の active ユーザーに通知する」こと。共有ロックでも、投稿期間の作成と登録は今までどおり排他になる

### 3. export のクエリ (DB)
- 根拠: 182回、平均 466ms、DB 時間の 11.8% (前回 159回、平均 462ms)
- 原因箇所: `go/export_api.go` のクエリが `image_objects` を `id` だけで JOIN して `i.data` を読む。`image_objects` は `created_at` でパーティション分割され、主キーは `(id, created_at)`
- 対策案: 投稿の情報だけを先に取り、画像は `imageDataCache` か `id = ANY($1)` でまとめて取る。先に asia2 で EXPLAIN を取る
- 期待効果: 中 (1件20点)
- リスク: ZIP の中身 (画像のバイト列と並び順) を変えない

### 4. 画像の INSERT (DB I/O)
- 根拠: `INSERT INTO image_objects` 749回、平均 77ms、DB 時間の 8.1%。asia2 のベンチ中 iowait 22.1%
- 対策案: ディスクの種類と I/O の上限を確認する。PostgreSQL の永続性は下げない
- 期待効果: 小〜中

### 5. 2ユーザーの行ロック (DB)
- 根拠: `SELECT id FROM users WHERE id IN ($1, $2) AND status=$3 ... FOR NO KEY UPDATE` 2918回、平均 9.07ms (前回 12.11ms)
- 期待効果: 小

## 確認したこと / していないこと

- 確認した: 4台の vmstat / pidstat、3台の alp と pprof、DB のクエリ差分、timeline/report.txt (M2 を含む)、#6150 の M2 の再集計
- 確認していない: ポータルの Stdout (リージョン・日ごとの人数、グランプリ)。ユーザー数が減った理由。asia1 の CPU がスコアの上限を決めているかどうか
