# ベンチ分析 #13634 (nginx の接続設定: upstream keepalive と worker_connections)

- ブランチ: `codex/nginx-connection-tuning` (`64f5695`)。main (`b9209de`、PR #43 の投稿期間の作成の順番の変更を含む) に、nginx の設定変更だけを足したもの。4台にデプロイ
  - `etc/nginx/nginx.conf`: `worker_rlimit_nofile 65535`、`worker_connections 768 → 8192`
  - `etc/nginx/sites-available/isureal.conf`: `upstream isureal_app` に `keepalive 128`、`proxy_http_version 1.1`、`Connection ""`
- 計測: 04:40:01 UTC から150秒 (4台)。結果ディレクトリ: asia1 `044001` / europe1 `044002` / america1 `044003` / asia2 `044004`
- 結果: **PASS / score 59497**。これまでで最高。#12732 は 52133 (+14.1%)、#11801 は 54926 (+8.3%)、#8691 は 58984 (+0.9%)。ベンチ1回ずつの比較なので、ぶれを含む
- 計測の欠け: なし (4台とも pprof / alp / vmstat / pidstat / slow.txt あり)
- 比較対象: #12732 (main に PR #43 を入れた構成)。#12732 との差は nginx の設定だけ (今回のブランチに他の変更は無い)

## 概況

- **スコアは上がったが、nginx の設定の効果とは言えない**
  - リクエスト数 (asia / europe / america): 24,364 / 8,350 / 3,779。#12732 は 27,731 / 4,788 / 2,752。asia の比率は 79% → 67% に下がり、倍率 1.5 の europe / america の比率が上がった。比率が変わるだけでスコアは動く
  - asia1 の CPU は #12732 と同じ。150秒平均の idle は 56.4% (#12732: 57.5%)、idle 5% 未満の秒数は 43秒 (42秒)。europe1 / america1 の idle も 92.6% / 94.0% (93.5% / 94.3%)。asia2 は 74.6% (72.4%)
  - asia1 のエンドポイントごとの平均も #12732 とほぼ同じ。`GET /api/feeds/:id` 0.444s (0.448s)、`GET /api/images/:id` 0.153s (0.152s)
  - nginx の CPU は両方の計測で小さく、ボトルネックではなかった (pidstat の見方が粗いので、相対的な見立て)
- ボトルネックは前回と同じ asia1 の CPU (画像署名)。下の 1
- 通知の `INSERT`: 18回 / 平均 232.7ms (#12732: 17回 / 217.9ms)。投稿期間の作成の reqtime は最大 1.40s (asia)、1.20s (europe) (#12732: 最大 1.71s)

## ベンチマーカーの行動 (timeline/report.txt より)

- 総リクエスト 36,493 (#12732: 35,271)。ユーザー (host) 574 (459)
- 流れは前回と同じ。通知 (SSE) → `GET /api/posting-window/current` → `POST /api/posts` → `GET /api/feeds/friends`
- 投稿期間は 3秒ごとに asia → europe → america の順に開く。最後の期間は 3リージョンを同時に開く (t=71.5〜72.1)
- 最後の期間: asia 295件 (282件が完了)、europe 109件 (109件)、america 45件 (45件)。#11801 で起きた「最後の期間の投稿が0件」は起きなかった (#12732 でも起きなかった)

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)

| リージョン | 投稿数 | 期間内に完了 | 前回 (#12732) | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 533 | 527 (99%) | 604 / 639 (95%) | 3.10s | 1.04s |
| europe | 255 | 240 (94%) | 140 / 146 (96%) | 3.06s | 1.43s |
| america | 125 | 121 (97%) | 102 / 104 (98%) | 2.49s | 1.58s |
| 最後の期間 (3リージョン計) | 449 | 436 (97%) | 336 / 339 (99%) | - | - |
| (全体、最後の期間を除く) | 913 | 888 (97%) | 846 / 889 (95%) | 2.99s | 1.21s |

- 最後の期間を含めた投稿数は 1,362 件 (#12732: 1,228 件。+10.9%)。完了は 1,324 件 (97%)
- 期間の割り当て: asia → europe → america (893)。2位の asia → america → europe は 689 で、差は十分ある。割り当てが疑わしい期間: 18期間中1つ (europe の最後の期間。lag が無い)
- europe の完了率が 96% → 94% に下がった。europe の期間 (t=50.23) で 84件中 70件の完了 (reqtime 0.31s)。asia の投稿が多い期間と重なるので、asia1 の負荷の影響と考えられる (推測)
- 開始→投稿の p90 は asia で 10.19s、全体で 8.24s。反応 (SSE→投稿開始) は p50 1.66s、p90 3.21s

## ボトルネック上位

### 1. asia1 の CPU (画像署名)
- 根拠: pprof の 65.2% が `computeImageSignature` (SHA-256 の `blockAVX2` が主)。呼び出し元は `listMemories` が 41.3%、`newPostResponse` が 37.9%、`loadMemoryInteractions` が 27.6%。asia1 の idle 5% 未満が 43秒。europe1 / america1 の idle は 90% 以上
- 原因箇所: `go/image_signature.go:68` (`computeImageSignature`)、`go/image_signature.go:107` (`signedImageURL`)
- 対策案: #11801 / #12732 の1番と同じ。署名の計算を、CPU に余裕のある europe1 / america1 に任せる。Memories や投稿の応答で返す署名の数を減らす
- 期待効果: 大。asia に偏る負荷では、asia1 の CPU が上限を決めている
- リスク: 署名の値と有効期限の形式は変えてはいけない

### 2. 画像の閲覧権限の確認クエリ (DB)
- 根拠: `SELECT u.secret_key_hash, i.media_type, (u.profile_image_id = i.id OR EXISTS (...))` が 8,626回、平均 1.55ms、合計 13.4秒、DB 時間の 15.6% (#12732: 8,514回 / 1.86ms / 15.2%)
- 原因箇所: `go/image_api.go` (`u.profile_image_id = i.id OR EXISTS (...)` のクエリ)
- 対策案: 署名の検証とプロセス内キャッシュで済ませる。無効化は、画像の削除・投稿の削除・プロフィール画像の変更・RealMoji の削除の4か所
- 期待効果: 中
- リスク: 閲覧権限 (削除後5秒以内に見えなくなること) を崩さない

### 3. 友達申請の承認のロック (DB)
- 根拠: `SELECT id FROM users WHERE id IN ($1, $2) AND status=$3 ORDER BY id FOR NO KEY UPDATE` が 4,348回、平均 2.35ms、合計 10.2秒、DB 時間の 11.9% (#12732: 3,846回 / 2.77ms / 10.2%)
- 原因箇所: `go/relationship.go:38` と `go/relationship.go:52` (2人の users 行を `FOR NO KEY UPDATE`)
- 対策案: 1回 2.4ms は、主キー参照にしては遅い。同じユーザーへの集中でロック待ちが出ている可能性がある (推測)。asia2 の `pg_locks` で待ちの相手を確認してから、ロックの範囲を見直す
- 期待効果: 小〜中
- リスク: 友達申請の二重承認や、ブロックとの前後関係を崩さない

### 4. ユーザー検索 (DB)
- 根拠: `SELECT ... FROM users WHERE status=$5 AND id<>$1 AND (... search_username LIKE $4 OR ...)` が 1,297回、平均 6.27ms、合計 8.1秒、DB 時間の 9.5% (#12732: 1,024回 / 7.51ms / 7.4%)
- 原因箇所: `go/user_read_api.go:82`
- 対策案: `pg_trgm` の GIN インデックスを `search_username` / `search_display_name` に付ける。先に `EXPLAIN` で実行計画を見る
- 期待効果: 小〜中
- リスク: インデックスの作成が `/initialize` と追試の再起動後も保たれることを確認する

### 5. export のクエリ (DB)
- 根拠: `SELECT p.id, p.author_id, ..., w.ended_at AS window_ended_at FROM posts p JOIN ...` が 170回、平均 26.86ms、合計 4.6秒、DB 時間の 5.3%。`GET /api/me/export` も 170回で、p50 0.816s、p99 8.499s。export は1件20点
- 原因箇所: `go/export_api.go` (件数が一致するので export のクエリと考えられる。呼び出し元の確認は推測)
- 対策案: ZIP のストリーミングと、画像の読み込みの見直し (#11801 の4番と同じ)。DB のクエリは平均 26.86ms で、遅さの主因は DB ではない
- 期待効果: 小
- リスク: レスポンスの並び順を変えない

## 確認したこと / していないこと

- 確認した
  - 4台の vmstat、3台の alp、asia1 の pprof、asia2 の slow.txt、timeline/report.txt
  - ベンチが PASS し、score 59497 が出たこと
  - #12732 との差が nginx の設定だけであること (`git diff` で `etc/nginx/` の2ファイルのみ)
- 確認していない
  - nginx の設定変更だけの効果。リージョンの比率が違うので、スコアの差 (+7,364) は切り分けられない
  - 以前の `worker_connections 768` で、`worker_connections are not enough` のエラーが出ていたか (nginx の error.log を計測していない)
  - ポータルの Stdout (リージョン・日ごとの人数、グランプリの人数、エラーメッセージ)
  - 同じブランチでもう1回ベンチを流したときのぶれ
  - export の遅さ (p99 8.5s) の内訳
