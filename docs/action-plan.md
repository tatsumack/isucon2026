# ISUReal 改善の実行計画

Claude の分析、Codex の分析 (`docs/performance-improvement-analysis.md`)、初期ベンチ結果 (2677点) を突き合わせ、効果が高いと考えられる順に並べた。
順位はコードの静的解析と初期ベンチのログに基づく推測であり、実測 (alp / slow query / pprof) で入れ替える前提。

出典の表記: 【両方】= Claude と Codex の両方が指摘、【Claude】【Codex】= 片方のみ、【ベンチ】= 初期ベンチのログから判明

## 初期ベンチから分かったこと

- `POST /api/admin/posting-window` が3回タイムアウトし、asia の3・4日目、europe の3日目が丸ごと飛んでいる
- 最終日 (グランプリ、友達フィード検証が20点) の参加者が 0 人
- ユーザー数が asia 18 / europe 7 / america 6 から増えていない。応答が悪いため、ベンチマーカーが負荷を上げていないと推測される
- 「投稿が開いていない投稿期間に入りました」が出ている。期間の切り替えに失敗した影響と考えられる

→ **投稿期間が予定どおり開くようにすることが最優先。** ここが直らないと、他の改善をしても加点の機会そのものが増えない。

## 全施策に共通する注意

- 1施策 = 1ブランチ。着手前に Slack で宣言し (`[着手] isu1 / ブランチ名 / 内容`)、ベンチが通ってスコアが下がらないことを確認してから main へマージする
- `/api/initialize` は asia1 にしか届かない (国コード省略時は JP → asia1)。europe1 / america1 のプロセス内キャッシュは initialize では消せないため、**キャッシュを入れるときは DB 上の世代番号を見て無効化するなど、全サーバで初期化される仕組みを必ず用意する**【Claude】
- 書き込み後の読み取りへの反映は、リージョンをまたいでも必須。友達・ブロック・公開範囲に関わるキャッシュは FAIL の原因になりやすい【両方】
- 画像は DB (`image_objects`) を正とする。スキーマのコメントにあるとおり、ファイル配置に移すと別サーバで 404 になる
- キャッシュを入れたら、どこで無効化・更新するかをコミットメッセージに書く (CLAUDE.md のルール)

---

## 0. 計測の準備 (着手前に1回)

**目的:** 以降の順位を実測で確定し、各施策の効果を比較できるようにする。

1. asia2 の PostgreSQL で `pg_stat_statements` を有効にする (`etc/postgresql/16/main/conf.d/isucon.conf` に `shared_preload_libraries` と `track_io_timing` を追加)。スロークエリログ (`log_min_duration_statement`) も有効にする
2. deploy.sh で反映し、`CREATE EXTENSION IF NOT EXISTS pg_stat_statements;` を実行する
3. ポータルからベンチを1回実行し、`/bench-analyze` で asia1 と asia2 を計測する
4. 確認する項目:
   - 投稿期間開始 API の処理中に `pg_stat_activity` でロック待ち (`wait_event_type = 'Lock'`) が出ているか
   - 総実行時間の上位クエリ
   - アプリ側の DB 接続プール (30) が使い切られていないか

---

## 1. インデックスの追加 【両方】

**効果: 非常に大 / 難易度: 低 / リスク: 低**

認証が必要なほぼすべてのリクエストが `login_sessions.token_hash` を全件スキャンしている。DB 全体が重くなり、投稿期間開始のロック待ちを長引かせている原因の一つと考えられる。

**手順**

1. 追加するインデックス (優先度順)
   ```sql
   CREATE INDEX IF NOT EXISTS login_sessions_token_hash ON login_sessions(token_hash) WHERE active;
   CREATE INDEX IF NOT EXISTS users_username ON users(username);
   CREATE INDEX IF NOT EXISTS administrator_sessions_token_hash ON administrator_sessions(token_hash) WHERE active;
   CREATE INDEX IF NOT EXISTS friendships_high_low ON friendships(user_high_id, user_low_id);
   CREATE INDEX IF NOT EXISTS block_relations_blocked ON block_relations(blocked_id, blocker_id);
   CREATE INDEX IF NOT EXISTS follows_followee ON follows(followee_id, follower_id);
   CREATE INDEX IF NOT EXISTS posts_image_id ON posts(image_id) WHERE deleted_at IS NULL;
   CREATE INDEX IF NOT EXISTS posts_author_window ON posts(author_id, posting_window_id);
   CREATE INDEX IF NOT EXISTS realmojis_image_id ON realmojis(image_id) WHERE replaced_at IS NULL AND deleted_at IS NULL;
   CREATE INDEX IF NOT EXISTS posts_posted_at ON posts(posted_at DESC, id DESC) WHERE deleted_at IS NULL;
   CREATE INDEX IF NOT EXISTS friend_requests_recipient ON friend_requests(recipient_id, status, updated_at DESC, id DESC);
   CREATE INDEX IF NOT EXISTS friend_requests_requester ON friend_requests(requester_id, status, updated_at DESC, id DESC);
   ```
   - `notifications(user_id, ...)` は既存の UNIQUE `(user_id, posting_window_id)` があるため後回し
   - `posts_posted_at` はフィードの SQL を書き換える施策4とあわせて効く
2. `sql/002-indexes.sql` に置く。`sql/init.sh` で `reset.sql` の前に実行し、環境を作り直しても必ず作られるようにする。`IF NOT EXISTS` なので2回目以降のコストはほぼない
3. asia2 で上位クエリの `EXPLAIN (ANALYZE, BUFFERS)` を取り、インデックスが使われていることを確かめる
4. ベンチを実行し、投稿期間開始のタイムアウトが消えたか、日程が飛ばなくなったかを確認する

**確認ポイント:** タイムアウトがこれだけで消えるなら、施策2は後回しにしてよい。

---

## 2. 投稿期間開始 API のタイムアウト解消 【ベンチ】【Claude】

**効果: 非常に大 (日程の欠落と最終日0人を直接解消) / 難易度: 中 / リスク: 中**

`admin_posting_window_api.go:46` で `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE` を取っている。このロックは users への INSERT / UPDATE とぶつかる。

- 新規登録 (こちらも同じ `LOCK TABLE` を取る)
- プロフィールの更新 (表示名・自己紹介・リージョン)
- プロフィール画像の変更・削除
- 退会

さらに、ロックを持ったまま `UPDATE posting_windows` と、リージョン内全ユーザー分の通知 INSERT を行っている。また、投稿作成が `posting_windows` の行を `FOR UPDATE` で持っていると (施策5)、`UPDATE posting_windows SET ended_at` はその完了を待つ。

**手順**

1. 施策0の計測で、何を待っているかを特定する (`pg_locks` / `pg_stat_activity`)
2. このロックが守っている性質を確認する: 「期間開始の瞬間にそのリージョンにいる active ユーザー全員に通知を作る」こと。登録やリージョン変更と同時に起きたときに、通知の漏れや重複が出ないことが必要
3. 対策案 (上から順に検討する)
   - a. users を更新する他のトランザクションを短くする (施策1の効果で解消する可能性がある)
   - b. `LOCK TABLE users` を、対象リージョンだけを直列化する `pg_advisory_xact_lock` に置き換える。登録・リージョン変更の側でも、変更前後のリージョンについて同じ advisory lock を取る
   - c. 新規登録の `LOCK TABLE users` は、`users(username)` の UNIQUE 制約と `ON CONFLICT DO NOTHING` に置き換える (施策10 と同じ。こちらを先にやってもよい)
4. 期間の切り替えと、登録・リージョン変更・投稿作成を同時に流しても、通知とフィードが整合していることを確かめる

---

## 3. 画像署名 (HMAC 1000回) の計算結果を使い回す 【Claude】

**効果: 大 (アプリサーバの CPU) / 難易度: 低〜中 / リスク: 低〜中**

`image_signature.go` は、署名1つにつき HMAC-SHA256 を1000回繰り返す。フィード1回 (50件 × 投稿画像とプロフィール画像) で約10万回になる。画像 GET の検証でも毎回1000回計算している。フィードの SQL を直しても、返す件数が同じならこの計算は減らない。

**手順**

1. 署名を作る側: `(imageKey, imageID, exp)` をキーにメモ化する。`exp` を分単位に丸めれば (常に「現在 + 23時間59分」以上を保つ)、同じ1分間は同じ URL を返せるのでキャッシュが当たる
   - 署名の形式と検証結果は変えない (マニュアルの禁止事項)。`exp` を何にするかはサーバ側の自由と解釈しているが、ベンチで「exp がほぼ24時間後か」を見ている可能性があるため、通過することを必ず確認する
2. 検証する側: 一度検証に成功した `(imageID, exp, sig)` を覚えておき、2回目からは HMAC を省略する。削除状態の確認は省略しない
3. pprof で CPU 時間に占める `imageSignature` の割合が下がったことを確認する

---

## 4. フィードの絞り込みを SQL 側に移す 【両方】

**効果: 非常に大 / 難易度: 中〜高 / リスク: 高 (公開範囲を誤ると FAIL)**

現状は、進行中の投稿期間にある全リージョンの他人の投稿を LIMIT なしで取得し、Go 側で権限と feed 種別を判定して50件に切っている。friends-of-friends では、さらに全候補の関係情報・RealMoji・コメントを読んでからソートしている。

**手順**

1. 書き換える前に、初期データ上の数ユーザー × 4種類の feed のレスポンスを保存しておく (比較用)
2. feed 種別ごとに SQL を分け、`canView` と `matchesFeed` の条件を WHERE に移す
   - friends: 友達の ID 集合に `author_id` が含まれる
   - following: `follows` を JOIN する
   - public: `visibility = 'public'`
   - 共通: ブロックの除外、`HasPostedToday` (false なら自分以外の投稿は空)、期間が終了していない
3. `(posted_at, id) < カーソル` と `LIMIT 51` を DB 側で適用する
4. friends-of-friends はスコア順のため、候補を SQL で絞ってからスコアを計算する (候補を全件読まない)。共通の友達数などを SQL で集計することも検討する
5. 手順1で保存したレスポンスと一致することを確かめてからベンチを実行する

---

## 5. 投稿作成時の posting_windows 行ロックをやめる 【Codex】

**効果: 大 / 難易度: 中 / リスク: 中**

`post_api.go` の投稿作成は、現在の `posting_windows` 行を `FOR UPDATE` でロックする。同じリージョンの投稿がすべて直列になり、期間開始の `UPDATE` もこれを待つ (施策2と関連)。

**手順**

1. `FOR UPDATE` を `FOR SHARE` (投稿同士はぶつからず、期間終了の UPDATE とだけぶつかる) にするか、ロックなしで読んでから `INSERT ... SELECT ... WHERE ended_at IS NULL` で期間が終わっていないことを確かめる形に変える
2. 期間の切り替えと同時に投稿しても、終了した期間に投稿が入らないことを確かめる (ベンチで出た「開いていない投稿期間に入りました」が再発しないこと)

---

## 6. SSE 通知のポーリングをまとめる 【両方】

**効果: 大 / 難易度: 中 / リスク: 中**

接続1つにつき、0.5秒おきに `getUser` と通知検索の2クエリを発行している。europe / america の場合は、これがすべてリージョンをまたぐ。

**手順**

1. サーバごとに1つの goroutine が、0.5秒おきに「自リージョンで新しく作られた通知」をまとめて取得し、接続中の各ユーザーに配る
   - または PostgreSQL の `LISTEN/NOTIFY` で、期間開始時にだけ各サーバを起こす (ルーター経由の DB 接続で動く)
2. 通知に含める内容 (id / postingWindowId / region / createdAt) と、接続した時刻より後の通知だけを送るという条件は変えない
3. ユーザーのリージョン変更と退会で接続を終了する処理は残す

---

## 7. europe / america から DB への往復を減らす (viewer context を含む) 【両方】

**効果: 非常に大 (得点1.5倍のリージョン) / 難易度: 高 / リスク: 中〜高**

1リクエストの中で、セッション → ユーザー → 今日投稿したか → ブロック → 友達 → 友達の友達 → 投稿、と逐次クエリを発行している。往復ごとにリージョン間の遅延がかかる。`canViewPost` と `canComment` は viewer context (5クエリ以上) を毎回作り直す。

**手順**

1. 施策0の計測で、europe1 / america1 の各 API の応答時間と、リクエストあたりのクエリ数を確認する
2. 安全なものからまとめる
   - セッションとユーザーを1クエリで取る (現在もほぼ1本。`getUser` の再取得をなくす)
   - `loadViewerContext` の4〜5本を CTE で1本にする
   - RealMoji / コメント系で、`getPost` → `canViewPost` (内部で再度 post を取得) の重複をなくす
3. トランザクションの中の `SELECT ... FOR NO KEY UPDATE` (17箇所) のうち、不要なものを外す (往復が1回ずつ減る)
4. キャッシュは最後の手段とする。入れる場合は DB の世代番号で無効化し、initialize でも世代を進める

---

## 8. 画像取得を1クエリにし、画像データをメモリにキャッシュする 【両方】

**効果: 大 (1件2点で高頻度、europe / america の帯域) / 難易度: 中 / リスク: 中**

現状は、所有者の SecretKey 取得と、有効性の確認 + `bytea` 取得の2クエリ。`image_objects` は `id` だけで検索しているため、約35個のパーティションをインデックスでそれぞれ探している。

**手順**

1. SecretKey の取得と有効性の確認を1クエリにまとめる (署名の検証は画像本体を取る前に行う)
2. 有効性の確認 (削除されていないか) は毎回 DB で行い、画像本体 (`data`) だけを ID をキーにメモリへキャッシュする。画像の中身は ID ごとに変わらないため、無効化は不要
3. `posts.created_at` と `image_objects.created_at` は投稿時に同じ値を入れているので、パーティションの絞り込みに使えるか検討する
4. 削除後5秒以内に 403 になることを確認する

---

## 9. ユーザー検索 【両方】

**効果: 中 (1件2点) / 難易度: 中 / リスク: 中**

有効なユーザーを全件読み、Go 側で `NFC(casefold(NFC(x)))` の部分一致を判定している。

**手順**

1. 正規化済みの列 (username / display_name) を追加し、登録時と更新時に Go 側で同じ関数を使って値を入れる。初期データの分は一度だけ埋める (reset.sql でも作り直す)
2. `pg_trgm` + GIN インデックスで `LIKE '%q%'` を使う
3. 既存の実装と結果 (並び順とカーソル) が一致することを確認する

---

## 10. その他 (それぞれ小さく、別ブランチで)

| 内容 | 出典 | 備考 |
|---|---|---|
| 新規登録の `LOCK TABLE users` を UNIQUE 制約に置き換える | Claude | 施策2とも関連する |
| PostgreSQL の `max_connections` (100) と `shared_buffers` (128MB) を見直す | Claude | アプリ3台 × 30接続 = 90。asia2 でもアプリが動いていれば上限を超える |
| Memories の RealMoji / コメントを SQL のウィンドウ関数で50件に絞る | Codex | |
| エクスポートを ZIP のストリーミングにする | Codex | 1回20点。メモリ使用量の対策 |
| nginx: `worker_connections`、静的ファイルのキャッシュ、gzip の対象 | Codex | SSE は接続を張り続けるので `worker_connections` に注意 |
| アプリのログレベル (debug) を下げる | 両方 | `ip_bans` に書き込むコードが Go 側にない。外部のプロセスがログを読んで BAN している可能性があるため、何が書き込んでいるかを確認してから行う |

---

## Claude と Codex の分析で異なる点

- **投稿期間開始のタイムアウト:** Codex の分析には含まれていない (初期ベンチの前に書かれたため)。ベンチで実害が出ているので最上位に置いた
- **画像署名の HMAC 1000回:** Codex は指摘していない。実装が軽く、どの施策とも独立して CPU を減らせるため、3番目に置いた
- **フィードと投稿ロックの順:** Codex はフィード (2位) → 投稿ロック (3位)。この計画では、施策2とつながる投稿ロックを先に検討できるよう近くに置いたが、効果はフィードの方が大きいと見ている。実測で決める
- **インデックスの範囲:** 両者でほぼ一致。`users(username)` は Claude のみ、`friend_requests` と `administrator_sessions` は Codex のみ

## 確認済み・未確認

**確認済み**
- マニュアルの採点、FAIL 条件、初期化、追試
- Go の主要 API と SQL、スキーマ、各種設定
- 初期ベンチのログ (投稿期間開始のタイムアウトと日程の欠落)

**未確認**
- 実際のボトルネック (alp / slow query / pprof を取っていない)
- 投稿期間開始 API が何を待っているか (ロック待ちは推測)
- 初期データの件数とテーブルサイズ
- リージョン間の遅延と帯域
- asia2 でアプリが動いているか
- `ip_bans` に書き込んでいるもの
