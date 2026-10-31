# ベンチ分析 030752 (PR #23 perf/misc-item10 + main)

- 対象の PR: [#23 実行計画10の小さい施策をまとめて入れる (Memories・エクスポート・nginx・PostgreSQL)](https://github.com/tatsumack/isucon2026/pull/23)
- 結論: **マージは見送り、PR は close**。main (025839、score 46215) より 5.5% 低かったため。他にやることがなくなったら、変更を分けて再検討する

- ブランチ: `perf/misc-item10` (`71f6d9c`)。4台にデプロイ
- 計測: 12:07:52 JST から70秒。asia1 / europe1 / america1 は bench-capture.sh、asia2 は vmstat / pidstat と pg_stat_statements の前後差分 (asia2/slow.txt)
- 結果: **PASS / score 43661** (025234 の 27647 から +57.9%)。ベンチ1回ずつの比較なので、ぶれを含む
- ベンチ本体は 12:08:16〜12:09:14 JST。計測 (12:07:52〜12:09:02) は**最後の約12秒 (グランプリの途中から) を取れていない**
- 比較対象: 025234 (#31、score 27647、`12423b2`)。計測時間は同じ70秒
- 025234 との差分: main 側の JPEG 検証 (`jpeg_validate.go`) と、このブランチの nginx / PostgreSQL の設定、`export_api.go`、`memory_api.go`

## 概況

- ボトルネックの所在: **asia2 (DB) の I/O 待ちとロック待ち**に移った
  - asia2: us 32.5 / sy 9.5 / idle 37.0 / **wa 21.4** (2コア)。ベンチの後半はほぼ常に 85〜99% 使用
  - asia1: idle 52.3% (025234 は 24.1%)。JPEG のデコードが pprof から消え、CPU に余裕ができた
  - europe1 / america1: idle 97%
- 025234 との比較 (平均応答時間)
  - asia1 `POST /api/posts`: 0.933s → **0.229s**。JPEG デコードがなくなった効果
  - europe / america `POST /api/posts`: 1.57s → **0.57〜0.60s**
  - europe / america `GET /api/feeds/:name`: 0.42〜0.45s → **0.21〜0.24s**
  - europe `POST /api/auth/register`: 2.15s → **3.80s** (悪化)。america は 2.15s → 2.57s
  - asia1 で処理した件数は増えた (feeds 888 → 2054、memories 802 → 946、friend-requests 承認 384 → 605)
- DB の合計時間は 328s → 748s。処理量が増えたうえに、待ちも増えている

## ベンチマーカーの行動 (Stdout より)

| | 5日目の人数 | グランプリの人数 | 025234 のグランプリ |
|---|---:|---:|---:|
| アジア | 110 | **21** | 3 |
| ヨーロッパ | 70 | **70** | 45 |
| アメリカ | 69 | **66** | 46 |

- 3リージョンとも人数が増えた。アジアは5日目に 110人まで増えた (025234 は 39人)。asia1 の CPU が空いた効果と考えられる
- ヨーロッパ・アメリカは5日目の人数がほぼそのままグランプリに残った
- アジアは 110人中 21人しかグランプリに残っていない。最終日の asia の開始が europe / america より約7秒遅い (12:09:11)。グランプリの時間帯は計測できていないので、理由はわからない
- `user moved` (リージョン変更) が2件出た。リージョン変更は advisory lock を2つ取る処理 (`user_api.go:227`)

## ボトルネック上位

### 1. 投稿期間の advisory lock 待ち (DB)
- 根拠: 288回、平均 1235ms、DB 時間の 47.6% (025234 は 183回、1093ms、60.9%)
- 原因箇所: `go/user_api.go:93` (登録)、`go/admin_posting_window_api.go:47` と `:72` (投稿期間の作成と通知の一括 INSERT。15回、平均 998ms)
- europe / america の register は転送されず、DB との往復 (約100ms) ごとにロックを持ったまま待つ。register の遅化 (europe 3.80s) と一致する
- 対策案: register を asia1 へ転送する。通知の INSERT をロックの外に出す
- 期待効果: 大。DB 時間の約半分を占める
- リスク: 投稿期間の開始時点の active ユーザーに通知する、という前後関係を崩さないこと

### 2. export の画像の一括読み出し (DB I/O)
- 根拠: `SELECT id, data FROM image_objects WHERE id = ANY($1)` が 1198回、平均 69.1ms、11.1%、22482行。export は asia1 で 106回、平均 0.80s (025234 は 0.60s)
- 原因箇所: `go/export_api.go:59` (このブランチの変更)。画像の本体 (`data`) をまとめて読んでいる
- asia2 の wa 21.4% の主な原因と考えられる (推測。クエリごとの I/O 量は見ていない)
- 対策案: 画像本体を毎回 DB から読まない。画像は不変なので、アプリのメモリかディスクにキャッシュする
- 期待効果: 中〜大。I/O 待ちが減れば、他のクエリも速くなる
- リスク: キャッシュは `/initialize` で必ず作り直す

### 3. 画像の書き込み (DB I/O)
- 根拠: `INSERT INTO image_objects` が 743回、平均 97.5ms、9.7% (025234 は平均 20.4ms)
- 原因: I/O 待ちに巻き込まれていると考えられる (推測)。2 が減れば一緒に速くなる見込み

### 4. 画像署名の計算 (asia1 CPU)
- 根拠: asia1 pprof の 70.2% が `computeImageSignature`。`listMemories` 46.9%、`newPostResponse` 39.4%、`newRealMojiResponse` 30.1%
- 原因箇所: `go/image_signature.go:64` (HMAC を1000回)
- 今は asia1 に余裕があるので、すぐには効かない。DB が速くなって asia1 の負荷が上がると、次のボトルネックになる
- 期待効果: 中 (今の時点では)

### 5. 画像の1件ずつの読み出し (DB)
- 根拠: `SELECT data FROM image_objects WHERE id = $1` が 6346回、平均 5.66ms、4.8%
- 対策案: 2 と同じキャッシュでまとめて減らせる

## 計測について

- asia2 の計測開始直後 (ベンチ前半) に 30〜50% の CPU 使用と 40% 前後の I/O 待ちがある。`/initialize` の処理と考えられる (`SELECT count(*) ...` 6.3秒 1回など)
- vmstat は約22行ごとにヘッダー行が入る。平均は数値の行だけで計算した

## 確認したこと / していないこと

- 確認した: 4台の CPU、3台の alp / pprof、DB のクエリ差分、上記の原因箇所のコード (export_api.go:59、user_api.go:93、admin_posting_window_api.go:47,72、image_signature.go:64)
- 確認していない: ベンチ最後の約12秒 (グランプリ)、asia2 のディスク I/O 量 (iostat は取っていない)、PostgreSQL の設定変更 (shared_buffers 1GB など) 単体の効果、転送されてきたリクエストの asia1 での応答時間

## main (025839、score 46215) との比較

このブランチの前の main (#32 まで、`c7256cb`) のベンチは 46215 で、今回の 43661 は **-5.5%**。PR #23 はマージせずに close した。
025839 は150秒でベンチ全体を計測、今回は70秒で最後の約12秒が欠けているので、件数ではなく平均で比べる。

| DB のクエリ (平均) | 025839 (main) | 030752 (このブランチ) |
|---|---:|---:|
| advisory lock | 953ms | 1235ms |
| `INSERT INTO image_objects` | 82ms | 97ms |
| `SELECT data FROM image_objects WHERE id = $1` | 4.57ms | 5.66ms |
| export 1回あたりの DB 時間 | 462ms (1クエリ) | 約600ms (一覧 73ms + 画像の一括読み出し 約7.6回 × 69ms) |
| Memories の RealMoji / コメント取得 (1回あたり) | 2.66ms / 2.02ms (IN 句の長さごとに別クエリとして集計されるため、一部のみ) | 15.8ms / 9.3ms (1993回ずつ、DB 時間の 6.7%) |
| 2ユーザーの行ロック | 12.1ms | 2.9ms |

悪化の原因と考えられるもの (推測。変更ごとに切り分けたベンチはしていない)
- `go/export_api.go`: 画像を20件ずつ取るようにした結果、export 1回あたりの DB 往復と DB 時間が増えた
- `go/memory_api.go`: 投稿ごとに50件に絞る window 関数 (`row_number() OVER (PARTITION BY ...)`) のクエリが重い。1回あたりの時間が RealMoji で約6倍、コメントで約4.6倍になった
- nginx (gzip / upstream keepalive) と PostgreSQL の設定 (shared_buffers 1GB など) の効果は、このベンチでは区別できない

次に試すなら、このブランチの変更を nginx / PostgreSQL の設定 / export / memories に分けて、1つずつベンチする
