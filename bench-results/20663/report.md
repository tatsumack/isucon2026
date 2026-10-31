# ベンチ #20663 分析 (PR #57 画像アップロードのコピーを減らす)

- ブランチ: `tatsumack/画像アップロードのコピーを減らす` (`cb8b355`) = main (`7f03ee2`) + #57。4台にデプロイ
- 計測: 06:04:18 UTC からの150秒 (4台、`060418` / asia2 は `060419`)
- 結果: **PASS / score 105922** (減点 0)。main の #20097 (93801) から **+12.9%**、これまでの最高 #17256 (94360) から +12.3%。ベンチ1回ずつの比較なので、ぶれを含む
- 計測の失敗は無い (pprof_error.txt なし、空のファイルなし)

## 人数 (ポータルの Stdout)

| | 2日目 | 3日目 | 4日目 | 5日目 | グランプリ |
|---|---|---|---|---|---|
| asia #20097 | 48 | 138 | 394 | 709 | 683 |
| asia **#20663** | 49 | 142 | 406 | **873** | **555** (最終日 872) |
| europe #20097 | 18 | 43 | 98 | 99 | 92 |
| europe **#20663** | 17 | 48 | 114 | **114** | 111 |
| america #20097 | 14 | 35 | 62 | 62 | 55 |
| america **#20663** | 15 | 41 | 96 | **96** | 95 |

- 3リージョンとも人数が増えた。europe / america は5日目に止まる形は同じだが、止まる人数が増えた (europe 99 → 114、america 62 → 96)
- グランプリは asia 555人 / europe 111人 / america 95人

## 概況

- ボトルネックの所在: **負荷の山 (t=55〜90秒) で asia1 のアプリ CPU と asia2 の DB の両方が張り付いている**
  - asia1: us 37.6 / sy 34.9 / idle 6.4 / wa 15.7 (150秒の平均は idle 60%)
  - asia2: us 69.0 / sy 13.0 / idle 10.9
  - europe1 / america1: idle 83〜84%。ほとんど空いている
- 前回比 (asia1、#20097 → #20663)
  - POST /api/posts: 1949 → 2303回 (+18%)、feeds: 6394 → 6923回、memories: 3300 → 3320回
  - 投稿の解析 (Bind + ReadAll → bindCreatePostRequest): **4.11s → 1.93s**。PR #57 の狙いどおり
  - アプリ全体の memclrNoHeapPointers: 4.86s (9.8%) → 3.66s (6.9%)
  - **プロフィール画像は改善していない**: ReadAll 3.52s → readUpload 3.86s (件数 537 → 636)。先に確保したはずのバッファが `bytes.(*Buffer).grow` で伸び直している (memclr 2.38s のほとんど)。投稿の経路では起きていない

## ベンチマーカーの行動 (timeline/report.txt より)

- 投稿の流れは前回と同じ: 通知 (SSE) → posting-window/current (2984回) → POST /api/posts → 友達フィード (2871回)
- 投稿期間は約3秒ずつリージョンを巡回し、各リージョン約9秒。admin の reqtime は最大 1.01s (asia 5日目の開始)
- ユーザーの入口で一番多いのは register → login → PUT /api/me/profile-image → GET /api/me → 画像 (582人)。プロフィール画像の 4xx は 137回 (400 が 107、403 が 30)
- 加点されないリクエスト: ログイン失敗 401 が 558回、画像の 403 が 478回、login → login の連続が 630回
- 通知 (SSE) は p50 11.4s。499 (クライアントが切断) が 1105回。期間の終わりにまとめて閉じる

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)

| リージョン | 投稿数 | 期間内に完了 | 前回 (#17256) | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 1375 | 1226 (89%) | 849/902 (94%) | 5.02s | 1.08s |
| europe | 268 | 257 (96%) | 248/284 (87%) | 2.59s | 1.44s |
| america | 168 | 152 (90%) | 172/199 (86%) | 7.71s | 1.46s |
| 最後の期間 (3リージョン計) | 1035 | 996 (96%) | 428/641 (67%) | — | — |

- 前回の比較は #17256 を使った。#20097 では timeline を取っていない
- 期間の割り当て: 採用した順番は america → europe → asia (1253)。2位の asia → europe → america (1160) との差は 93 (約8%)。lag が無いか6秒を超える期間が 5/18 ある。**期間ごとの数値は推定扱い**
- asia は件数が 1.5倍になって、率は 94% → 89% に下がった。下がったのは 41〜60秒の期間 (293/337、376/449、372/404)。開始→投稿 p50 が 2.69s → 5.02s に伸びた。内訳は通知 (期間開始 → SSE が閉じる) が p50 0.10s → 3.76s。この通知の値は、期間の割り当てがずれている影響を受けている可能性がある (推定)
- asia の最後の期間は 34% → 95% と大きく上がった (最後の期間は1件20点)

## ボトルネック上位 (効果が大きい順)

### 1. ユーザー検索の SQL が DB 時間の 26% を使っている
- 根拠: asia2 slow.txt の1位。2912回、合計 57.3s、平均 19.7ms、1回あたり約1行。asia2 は負荷の山で idle 11%
- 原因箇所: `go/user_read_api.go:80-87`。`search_username LIKE '%q%' OR search_display_name LIKE '%q%'` にはインデックスが効かない。`ORDER BY username, id LIMIT 51` を `users_active_username_id` でたどりながら全員の文字列を比べている (ヒットはほぼ1件なので、ほぼ全件を見る)
- 対策案: (a) `pg_trgm` の GIN インデックスを `search_username` / `search_display_name` に張る (asia2 で拡張が入るかは未確認)。(b) 検索列をアプリのメモリに持ち、Go で部分一致をとってから ID で引く。初期化・登録・PATCH /api/me・アカウント削除で更新する
- 期待効果: 大。DB の 1/4 が空き、DB 待ちの API がすべて速くなる
- リスク: (b) は表示名の変更と即時反映 (リージョンをまたぐ)。検索は asia1 に転送されているので、asia1 だけで持てばよい

### 2. 投稿のレスポンスで画像の署名 (HMAC 1000回) を asia1 で計算している
- 根拠: asia1 で `computeImageSignature` 4.90s (9.2%)。そのうち newPostResponse → signedImageURL が 3.15s。投稿1回あたり約1.4ms
- 原因箇所: `go/post_api.go` の `newPostResponse` → `go/image_signature.go:116` `signedImageURL`。新しい画像なのでキャッシュには当たらない
- 対策案: 署名の形式は変えられない (manual.md:472)。Memories / export と同じく、空いている europe1 / america1 (`ISUREAL_SIGNING_URLS`) に計算させる。往復の分だけ投稿が遅くなるので、トランザクションと並行して投げる。もう1案は、プロフィール画像の署名 (newGetMeResponse 0.82s) も含めて、署名をトランザクションの間に別の goroutine で計算すること。これは CPU は減らず、遅延だけが減る
- 期待効果: 中。asia1 の CPU が約9%空く。asia1 は負荷の山で張り付いている
- リスク: 署名サーバへの往復で投稿が遅くなると、M2 (期間内の完了率) が下がる

### 3. エクスポートが画像を毎回ディスクから読む
- 根拠: `ImageStore.load` 5.66s のうち exportPostsHandler から 3.86s。syscall (os.ReadFile / Open) が中心。export は 281回、p50 0.91s。1件20点
- 原因箇所: export は `ImageStore.load` を直接呼び、imageDataCache を使っていない
- 対策案: export で読んだ画像を imageDataCache に入れるか、キャッシュを先に見る。キャッシュは 256MB 上限
- 期待効果: 中。export が速くなれば得点が直接増える (20点)
- リスク: キャッシュを export の大量の画像で埋めると、通常の画像配信のヒット率が下がる。上限と追い出しの方針を確かめる

### 4. プロフィール画像の読み込みでバッファが伸び直している
- 根拠: readUpload 3.86s。`bytes.(*Buffer).grow` → memclr 2.38s / memmove 1.08s は、すべて putProfileImageHandler の経路
- 原因箇所: `go/upload_buffer.go` の `readUpload`。Content-Length で先に確保しているはずなのに伸びている。Content-Length が -1 (chunked) か、8MB を超える本文 (不正な画像の 400 が 107回) の可能性がある。**未確認**
- 対策案: ベンチ中の Content-Length と本文の大きさをログで確かめてから直す
- 期待効果: 小〜中。asia1 の CPU で約4〜6%
- リスク: 小さい

### 5. 投稿の画像の保存 (fsync) が 2.3s
- 根拠: `ImageStore.Save` → `writeFileAtomic` 2.30s。syscall の待ちが中心。負荷の山で wa 15.7%
- 原因箇所: `go/image_store.go` の `writeFileAtomic` (ファイルとディレクトリの fsync)
- 対策案: 追試で再起動後に画像が残る必要がある。正常な再起動なら fsync しなくてもページキャッシュは書き出される。fsync を外すか、まとめて行う案はある。ただしクラッシュ時に失われるリスクの判断が要る
- 期待効果: 小〜中 (投稿の遅延)
- リスク: 追試の条件 (再起動後にデータが残ること) を満たすかを確かめる必要がある

## 確認したこと / していないこと

- 確認した: 4台の vmstat、asia1 の pidstat、3台の alp、asia1 の pprof (peek / traces)、asia2 の slow.txt、timeline と M2、ポータルの Stdout
- 確認していない: europe1 / america1 の pprof の詳細、iostat / free (今回は取っていない)、プロフィール画像の Content-Length の実際の値、スコアの伸びのうち PR #57 によるものの切り分け (ベンチ1回)
