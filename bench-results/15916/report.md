# ベンチ分析 #15916 (画像署名の計算を asia1 から europe1 / america1 へ移す)

- ブランチ: `tatsumack/画像asiaから外す` (`9bee65d`、main `081fa9f` から)。アプリ3台にデプロイ。asia2 は変更なし
- 計測: 05:07:58〜05:10:28 UTC の150秒 (4台)。ベンチは 05:08:21 開始、05:10:41 終了 (負荷は 05:10:15 頃まで)
- 結果: **PASS / score 77148**。同じブランチの1回目 (#15626) は **75413**。main と同じ構成の #14832 は 55227 (**+40% / +37%**)
- 計測の欠け: なし。#15626 は計測の開始がずれ、pprof と alp はベンチの最初の約14秒分しかない。#15626 は score と vmstat、timeline だけを参考にする (`bench-results/15626/`)

## 概況

- **asia1 の CPU から画像署名がほぼ消えた**
  - pprof のサンプル合計: 80.48s (#14832) → **45.43s**。`computeImageSignature` は上位40件 (0.23s 超) に出ない (#14832 は 62.5%)
  - `sha256.blockAVX2` は flat 2.74s (6.0%) 残る。新しい投稿の応答 (`createPostHandler` → `newPostResponse` 1.79s) と、画像の取得時の検証のため。どちらも依頼の対象外
  - asia1 のログに `remote image signature failed` は0件 (依頼の失敗・タイムアウトなし)
- **europe / america の Memories・export は、往復が1回増えたのに速くなった** (asia1 が空いたため)
  - europe1 `GET /api/memories`: 0.589s → **0.352s**。export: 1.572s → 2.126s (件数 20 → 37)
  - america1 `GET /api/memories`: 0.416s。feed: europe1 0.550s → **0.257s**
- **asia のユーザーが増え、asia1 は負荷の後半で再び張り付いた**
  - Stdout の asia の人数: 1日目から 18, 38, 83, 228, 517人 (#11801 の5日目は 157人。#15626 は 413人)
  - asia1 の vmstat: ベンチの t≈47〜90秒は idle 0〜5%。内訳は us 約40%、**sy 約37%**、iowait 13〜22%。ディスクの読み込みは後半 46〜64MB/s まで増えた
  - pprof の flat 1位は `Syscall6` 25.6% (ソケットとファイルの read / write)。画像の読み書き (`ImageStore.load` 4.21s、`ImageStore.Save` 2.38s) と、投稿の受信 (`Bind` 2.26s、`io.ReadAll` 0.90s)
- asia2 (DB) は idle 63%、iowait 7.6%。後半は us 約52%

## ベンチマーカーの行動 (timeline/report.txt より)

- 流れは前回と同じ。通知 (SSE) → `GET /api/posting-window/current` → `POST /api/posts` → `GET /api/feeds/friends`
- 投稿期間は asia → europe → america の順に開く (採用 1233、2位 1021)。admin の reqtime は最大 1.92s (america の最後の期間)
- **asia の5番目の期間が長い (t=53.60〜72.92、19秒)**。投稿 476件。asia1 が張り付いた時間帯と重なる
- 最後の期間 (t≈73〜): asia 6件中1件、europe 116件中61件、**america 58件中0件**が完了。Stdout ではグランプリ アジア0人、ヨーロッパ47人、アメリカ27人
  - #15626 でも、`america の投稿期間を開けません: POST /api/admin/posting-window: 応答がタイムアウトしました` が出て、グランプリ アメリカ0人
- 加点されない 4xx: `POST /api/auth/login` の 401 が 466件 (465件は即リトライ)。`GET /api/notifications` の 499 が 531件

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)

| リージョン | 投稿数 | 期間内に完了 | 前回 (#14832) | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 811 | 788 (97%) | 630 / 654 (96%) | 4.07s | 1.57s |
| europe | 267 | 266 (100%) | 166 / 171 (97%) | 3.12s | 1.20s |
| america | 336 | 331 (99%) | 107 / 112 (96%) | 2.19s | 1.31s |
| 最後の期間 (3リージョン計) | 180 | 62 (34%) | 360 / 360 (100%) | - | - |

- 最後の期間を除いた投稿は 937 → 1414件 (+51%)。europe / america の倍率1.5の投稿が 283 → 603件
- 最後の期間は大きく下がった (100% → 34%)。asia1 が張り付いた時間帯に入るため (推測)。america の最後の期間は lag が無く、割り当ても疑わしい
- 開始→投稿の p90 は asia 14.96s。通知 p50 0.49s、反応 p50 1.53s なので、asia の遅れは反応 (SSE → 投稿開始) の後半の伸び (p90 3.84s) と、前の期間の処理の遅れ (推測)

## ボトルネック上位

### 1. asia1 の CPU とディスク (画像の読み書き、投稿の受信)
- 根拠: 後半 idle 0〜5%、sy 約37%、iowait 13〜22%、ディスクの読み込み最大 64MB/s。pprof flat の 25.6% が `Syscall6`。`ImageStore.load` 4.21s (export から 2.71s)、`createPostHandler` 8.15s (`Save` 2.38s、`Bind` 2.26s、`newPostResponse` 1.79s)
- 原因箇所: `go/image_store.go` の `readImageFile` / `Save`、`go/export_api.go` (画像の読み込み)、`go/post_api.go` の `createPostHandler`
- 対策案: export の画像の読み込みを europe1 / america1 にも置く、または ZIP を作る処理を転送元で行う。投稿の応答の署名も依頼の対象に加える (1.79s)。asia1 のページキャッシュに収まらない初期画像の読み方を見直す
- 期待効果: 大。最後の期間 (1件20点) の完了率が 34% に落ちたのは、この時間帯の asia1 の飽和と重なる
- リスク: 画像削除後5秒の条件、export の内容 (初期データの期待値と一致すること)

### 2. 最後の期間 (america の投稿期間を開けない・完了0件)
- 根拠: #15626 で admin の `POST /api/admin/posting-window` がタイムアウト。#15916 は america の最後の期間で完了0件、admin reqtime 1.92s
- 原因箇所: `go/admin_posting_window_api.go` (未調査)
- 対策案: まず america の最後の期間で何が起きたか (投稿は58件あるがフィードが完了しない) を timeline.tsv で調べる
- 期待効果: 大 (最後の期間は1件20点、america は倍率1.5)
- リスク: 原因が未特定

### 3. ユーザー検索のクエリ (DB)
- 根拠: 2,172回、平均 14.81ms、合計 32.2秒、DB 時間の 18.6% (1位)
- 原因箇所: `SELECT ... FROM users WHERE status=$5 AND id<>$1 AND ...` (`GET /api/users`、asia1 で 1,376回 / 平均 0.367s)
- 対策案: 検索条件に合うインデックス、または検索結果のキャッシュ
- 期待効果: 中

### 4. 画像の閲覧権限の確認クエリ (DB)
- 根拠: 12,293回、平均 2.13ms、合計 26.2秒、DB 時間の 15.1%
- 対策案: #14832 の報告と同じ

## 確認したこと / していないこと

- 確認した
  - 4台の vmstat、3台の alp、asia1 の pprof (`-peek` で `createPostHandler` と `ImageStore.load` の内訳)、asia2 の slow.txt
  - timeline/report.txt (M2、エラーの後の行動)、ポータルの Stdout (#15626、#15916)
  - asia1 のアプリのログで、署名の依頼の失敗が0件
- 確認していない
  - europe1 / america1 側で署名の計算に使った CPU (vmstat では us 10% 前後で、余裕がある)
  - america の最後の期間で完了0件になった直接の理由、#15626 の admin のタイムアウトの理由
  - 最後の期間が #14832 より悪くなったのが、この変更 (asia のユーザーが増えたこと) によるものか、ぶれか
