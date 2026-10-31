# ベンチ分析 #6150 (計測 025839、PR #32 JPEG 検証の軽量化 + main)

- ブランチ: `perf/jpeg-header-validation` (`c7256cb`)。4台にデプロイ
- 計測: 02:58:39〜03:01:09 UTC の150秒 (4台)。ベンチの負荷走行は 02:58:52 (`/api/initialize`) 〜 03:00:05 頃で、計測に収まっている
- 結果: **PASS / score 46215** (前回 #5712 の 27647 から +67.2%)。ベンチ1回ずつの比較なので、ぶれを含む
- 比較対象: #5712 (PR #31、score 27647、計測70秒)。どちらもベンチ全体を含むので、件数もおおよそ比べられる
- 詳細な分析は途中。以下は確認できた数値のみ

## 計測で欠けているもの

- なし (4台とも pprof / alp / vmstat / pidstat / slow.txt あり)

## 概況

- ボトルネックの所在: **asia1 のアプリCPU (画像署名)** と **DB (advisory lock 待ち、画像の INSERT)**
- asia1 (2コア) のベンチ中の CPU: us 60.4 / sy 15.7 / idle 24.1 (前回 us 70.8 / sy 12.7 / idle 16.9)。idle 5% 未満の秒数は 25秒 → 8秒
- pprof から `image/jpeg.Decode` が消えた (前回は asia1 の 45.4%)
- 処理量が増えた (前回比、DB の calls / asia1 の alp)
  - 投稿 (`INSERT INTO posts`): 544 → 908 (+67%)
  - 画像の取得 (`SELECT data FROM image_objects`): 3862 → 7234
  - asia1 の `GET /api/memories`: 802 → 1704、`GET /api/me/export`: 60 → 121、RealMoji の投稿: 74 → 150
- `POST /api/posts` の平均応答時間: asia1 0.933s → 0.390s、europe1 1.566s → 0.690s、america1 1.550s → 0.741s
- 悪化: `POST /api/auth/register` america1 2.150s → 3.467s、europe1 2.155s → 2.344s

## ボトルネック上位 (詳細な分析は後で追記する)

### 1. 画像署名の計算 (asia1 CPU)
- 根拠: asia1 pprof の 70.95% が `computeImageSignature` (前回 37.1%)。呼び出し元は `listMemories` 49.2% (うち `loadMemoryInteractions` → `newRealMojiResponse` 31.7%)、`newPostResponse` 37.6%
- 原因箇所: `go/image_signature.go:64` (HMAC-SHA256 を1000回)。呼び出しは `go/memory_api.go:89`、`:127`
- europe1 / america1 の CPU は idle 98% で、Memories も asia1 へ転送されている

### 2. 投稿期間の advisory lock 待ち (DB)
- 根拠: 353回、平均 953ms、DB 時間の 49.9% (前回 183回、平均 1093ms、60.9%)

### 3. 画像の INSERT (DB I/O)
- 根拠: `INSERT INTO image_objects` 908回、平均 82ms (前回 545回、平均 20.4ms)、DB 時間の 11.0%
- asia2 のベンチ中: iowait 21.5%、書き込み 平均 25MB/s

### 4. export のクエリ (DB)
- 根拠: 159回、平均 462ms、DB 時間の 10.9% (前回 65回、平均 439ms)

### 5. 2ユーザーの行ロック (DB)
- 根拠: `SELECT id FROM users WHERE id IN ($1, $2) AND status=$3 ... FOR NO KEY UPDATE` 3026回、平均 12.1ms (前回 1711回、平均 6.99ms)

## その他

- `PUT /api/me/profile-image` で 502 が3件 (asia1)。nginx の error.log は `sendfile() failed (32: Broken pipe)` / `Connection reset by peer`。アプリが本文を読み終える前に応答して接続を閉じたと考えられる (推測)。アプリの panic・再起動はない。プロフィール画像の処理は今回変えていない。前回も 5XX が2件ある
- POST /api/posts と RealMoji の 4xx は0件

## 確認したこと / していないこと

- 確認した: 4台の vmstat / pidstat、3台の alp、asia1 の pprof、DB のクエリ差分、timeline/report.txt の件数とステータス
- 確認していない: ベンチの Stdout (リージョン別の人数・グランプリ)、ボトルネックごとの対策案、M2 の期間内完了率
