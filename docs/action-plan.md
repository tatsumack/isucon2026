# ISUReal 改善の実行計画

Claude の分析、Codex の分析 (`docs/performance-improvement-analysis.md`)、初期ベンチ結果 (2677点) を突き合わせ、効果が高いと考えられる順に並べた。
順位はコードの静的解析と初期ベンチのログに基づく推測であり、実測 (alp / slow query / pprof) で入れ替える前提。
その後、ベンチ #940 の計測レポートと Dots の構造分析を各施策に追記し、施策11・12を追加した。

出典の表記: 【両方】= Claude と Codex の両方が指摘、【Claude】【Codex】= 片方のみ、【ベンチ】= 初期ベンチのログから判明、【#940】= ベンチ #940 の計測レポート、【Dots】= Dots による構造分析

施策の番号は着手順の管理に使っているため変えない。後から見つかった施策は末尾に追加する。

## 進捗

| 施策 | 状態 |
|---|---|
| 1. インデックスの追加 | main にマージ済み ([tatsumack/isucon2026#5](https://github.com/tatsumack/isucon2026/pull/5)、`sql/002-performance-indexes.sql`) |
| 2. 投稿期間開始のタイムアウト解消 | PR [tatsumack/isucon2026#7](https://github.com/tatsumack/isucon2026/pull/7) (ベンチ未確認) |
| 3. 画像署名の使い回し | PR [tatsumack/isucon2026#10](https://github.com/tatsumack/isucon2026/pull/10) (ベンチ未確認) |

## 追加の分析で分かったこと (ベンチ #940、Dots)

ベンチ #940 は `setup/measurement` ブランチで実行 (スコア 4129 / PASS)。施策1〜3は**入っていない**状態の計測。

- **europe1 / america1 → asia2 (DB) の RTT は約100ms** (asia1 → asia2 は約1ms)【#940】
  - europe / america の API は 1〜4秒かかっている。例: `POST /api/posts` の平均は asia1 1.48秒、europe1 3.83秒、america1 3.71秒
  - 遅さはほぼ「クエリ数 × 100ms」で説明できる。CPU はどのサーバも張り付いていない (asia1 アプリ約41%、asia2 DB約50%、europe1 / america1 は約5%)
  - グランプリの参加者は asia 16人、europe 0人、america 3人。遅延で投稿が間に合っていない可能性がある (推測)
- **DB 時間の 36.6% が画像取得の有効性確認** (`posts.image_id` / `realmojis.image_id` のフルスキャン。posts 約62.5万行、realmojis 約120万行)【#940】→ 施策1のインデックスで対応済み
- **ロック待ちが大きい**【#940】
  - `posting_windows ... FOR UPDATE` (投稿作成): 150回、平均 902ms → 施策5
  - `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`: 44回、平均 2159ms (投稿期間開始と新規登録) → 施策2、施策10 の新規登録の行
  - `SELECT id FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE` (ログイン): 821回、平均 203ms → 施策12 (新規)
- **SSE 通知**: `GET /api/notifications` は asia1 で 111回、合計 1096秒 (1接続約10秒、その間 0.5秒ごとにポーリング)【#940】→ 施策6
- 計測の失敗: pprof は直前の計測と重なって取れなかった (`cpu profiling already in use`)【#940】→ 施策0に注意を追記

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
5. 【#940】pprof は同時に1つしか取れない。前の計測が終わる前に次のベンチを計測すると `cpu profiling already in use` で失敗するので、前の計測の終了を待ってから始める
6. 施策ごとに、europe1 / america1 の主要 API の平均応答時間 (alp) を記録しておく。施策7・11 のどちらを選ぶかの判断材料になる

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

**追記 (Dots の分析)**
- 方針は「全投稿から除外していく」ではなく、「関係する作者 (友達・友達の友達・フォロー先) から投稿を逆引きする」。投稿が増えても、候補はその人の関係の大きさにしか比例しない
- **新着順に51件を先に切ってから権限や順位で絞ってはいけない。** 権限で除外される投稿や、friends-of-friends のスコア順で上に来る投稿を取りこぼし、正しい結果にならない。権限と feed 種別の条件を満たすものだけを対象にしてから LIMIT をかける
- friends-of-friends の推薦スコアは「関係グラフから決まる点 (共通の友達数、作者のつながり)」と「友達の反応による点 (RealMoji・コメント)」に分けて計算できる。前者は投稿に依存しないので、閲覧者ごとに一度だけ計算すればよい
- 施策1で `posts(posted_at DESC, id DESC)` と `posts(author_id, posting_window_id)` のインデックスが入っている

---

## 5. 投稿作成時の posting_windows 行ロックをやめる 【Codex】

**効果: 大 / 難易度: 中 / リスク: 中**

`post_api.go` の投稿作成は、現在の `posting_windows` 行を `FOR UPDATE` でロックする。同じリージョンの投稿がすべて直列になり、期間開始の `UPDATE` もこれを待つ (施策2と関連)。

**手順**

1. `FOR UPDATE` を `FOR SHARE` (投稿同士はぶつからず、期間終了の UPDATE とだけぶつかる) にするか、ロックなしで読んでから `INSERT ... SELECT ... WHERE ended_at IS NULL` で期間が終わっていないことを確かめる形に変える
2. 期間の切り替えと同時に投稿しても、終了した期間に投稿が入らないことを確かめる (ベンチで出た「開いていない投稿期間に入りました」が再発しないこと)

**追記 (ベンチ #940、Dots の分析)**

計測で裏付けが取れたため、優先度を上げる。施策2・3の次に着手する。

- 根拠【#940】: `posting_windows ... FOR UPDATE` は 150回で平均 902ms。`POST /api/posts` の平均は asia1 1.48秒、europe1 3.83秒、america1 3.71秒
- 通知で一斉に投稿させるサービスなので、リージョン内の全投稿が1本の待ち行列になる。N+1 を潰してもこの直列化は残る【Dots】
- ロックを持ったまま、画像の DB 保存・投稿の INSERT・取得確認・commit まで行っている。europe / america ではその間に DB 往復 (約100ms ずつ) と画像の転送が重なるため、ロックを持つ時間が特に長い【Dots】

設計の方針【Dots】: 単純にロックを削るのではなく、**投稿同士は並行に通し、投稿期間の切り替えとは排他を保つ**。

1. `FOR UPDATE` を `FOR SHARE` に変える
   - `FOR SHARE` 同士はぶつからないので、投稿は並行に進む。期間終了の `UPDATE posting_windows SET ended_at` とはぶつかるので、切り替えとの排他は保たれる
   - **`FOR KEY SHARE` は不可。** `ended_at` の更新はキー列の更新ではないため、`FOR KEY SHARE` では期間終了の UPDATE を止められない
2. 本人の `users` 行ロック (`FOR NO KEY UPDATE`) は残す。1人1日1投稿 (削除は2回まで) の判定が同時に走らないようにするため
3. 切り替え時の再取得 (`ErrNoRows` のときにもう一度 `posting_windows` を引く処理) は残す。ロック待ちの間に期間が終了した場合、WHERE 条件を満たさなくなって行が返らないため、新しい期間を取り直す必要がある
4. ロックを持つ時間を短くする: JPEG のデコードと検証はすでにトランザクションの前にある。可能であれば、画像のバイト列の送信 (`INSERT INTO image_objects`) をロック取得より前に移す
5. 効果の確認: 投稿時のロック待ち (`pg_stat_activity` / `pg_locks`)、ロックを持っている時間、リージョン別の投稿完了数を、変更の前後で比べる

---

## 6. SSE 通知のポーリングをまとめる 【両方】

**効果: 大 / 難易度: 中 / リスク: 中**

接続1つにつき、0.5秒おきに `getUser` と通知検索の2クエリを発行している。europe / america の場合は、これがすべてリージョンをまたぐ。

**手順**

1. サーバごとに1つの goroutine が、0.5秒おきに「自リージョンで新しく作られた通知」をまとめて取得し、接続中の各ユーザーに配る
   - または PostgreSQL の `LISTEN/NOTIFY` で、期間開始時にだけ各サーバを起こす (ルーター経由の DB 接続で動く)
2. 通知に含める内容 (id / postingWindowId / region / createdAt) と、接続した時刻より後の通知だけを送るという条件は変えない
3. ユーザーのリージョン変更と退会で接続を終了する処理は残す

**追記 (ベンチ #940、Dots の分析)**
- 根拠【#940】: `GET /api/notifications` は asia1 で 111回、合計 1096秒。1接続あたり約10秒、その間 0.5秒ごとに2クエリを発行している。N 接続で概ね毎秒 4N クエリ【Dots】
- `notifications(user_id, created_at, id)` のインデックスは施策1で追加済み。残るのは、接続ごとのポーリングをサーバ単位の配送にまとめること
- 方針【Dots】: 「接続ごとの監視」から「リージョン単位の共有配送」にする。まだ投稿していない利用者が待っている間ずっと DB を消費し続ける構造をなくす
- 第一段階で守ること: 通知は DB に永続化したまま使う (配送だけを共有化する)。接続した時刻より後の通知だけを送る、宛先、リージョン変更、退会、取り逃しがないこと
- 通知が遅れると時間内投稿 (`lateSeconds = 0`) に間に合わなくなるため、配送の遅延は 0.5秒以内を保つ

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

**追記 (ベンチ #940)**
- RTT は約100ms と実測できた。europe / america の API の遅さは、ほぼ「クエリ数 × 100ms」で説明できる。例: `POST /api/friend-requests` europe1 平均 3.17秒、`PUT /api/follows/:id` 3.9〜4.8秒
- 1リクエストのクエリを1本減らすごとに、europe / america では約100ms 速くなる。まずクエリ数の多い API (投稿、友達申請、フォロー、フィード) から数える
- クエリをまとめても往復待ちが支配的なままなら、施策11 (asia 側へ処理をまとめて委譲する) と比べる
- リージョンごとに DB を分ける構成や、非同期の複製は勧めない【Dots】。書き込み直後の別リージョンからの読み取りや、ユーザーのリージョン移動に対応する必要があり、整合性を作り直すことになる

---

## 8. 画像取得を1クエリにし、画像データをメモリにキャッシュする 【両方】

**効果: 大 (1件2点で高頻度、europe / america の帯域) / 難易度: 中 / リスク: 中**

現状は、所有者の SecretKey 取得と、有効性の確認 + `bytea` 取得の2クエリ。`image_objects` は `id` だけで検索しているため、約35個のパーティションをインデックスでそれぞれ探している。

**手順**

1. SecretKey の取得と有効性の確認を1クエリにまとめる (署名の検証は画像本体を取る前に行う)
2. 有効性の確認 (削除されていないか) は毎回 DB で行い、画像本体 (`data`) だけを ID をキーにメモリへキャッシュする。画像の中身は ID ごとに変わらないため、無効化は不要
3. `posts.created_at` と `image_objects.created_at` は投稿時に同じ値を入れているので、パーティションの絞り込みに使えるか検討する
4. 削除後5秒以内に 403 になることを確認する

**追記 (ベンチ #940、Dots の分析)**
- #940 では、画像取得の有効性確認が DB 時間の 36.6% を占めていた (1回平均 436ms)。原因の `posts.image_id` / `realmojis.image_id` のフルスキャンは、施策1のインデックスで解消済みのはず。施策1の後に計測し直して、この施策の優先度を決める
- 方針【Dots】: 画像の「変わらない中身 (バイト列)」と、「署名・期限・今配信してよいか」を分けて扱う。正本は DB のまま、繰り返し見られる画像のバイト列だけを各リージョンに置く。配信してよいかは毎回 DB で確認するので、削除や退会を無視してしまう TTL キャッシュとは違う
- europe / america で帯域が詰まっている場合に優先度が上がる。施策0の計測で、europe1 / america1 の画像取得の応答時間と転送量を見て決める

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
| 新規登録の `LOCK TABLE users` を UNIQUE 制約に置き換える | Claude | 施策2とも関連する。#940 では `LOCK TABLE users` が44回で平均 2159ms (投稿期間開始の分を含む)。施策2の後もこの待ちが残っていれば、この行を優先して切り出す |
| PostgreSQL の `max_connections` (100) と `shared_buffers` (128MB) を見直す | Claude | アプリ3台 × 30接続 = 90。asia2 でもアプリが動いていれば上限を超える |
| Memories の RealMoji / コメントを SQL のウィンドウ関数で50件に絞る | Codex | |
| エクスポートを ZIP のストリーミングにする | Codex | 1回20点。メモリ使用量の対策 |
| nginx: `worker_connections`、静的ファイルのキャッシュ、gzip の対象 | Codex | SSE は接続を張り続けるので `worker_connections` に注意 |
| アプリのログレベル (debug) を下げる | 両方 | `ip_bans` に書き込むコードが Go 側にない。外部のプロセスがログを読んで BAN している可能性があるため、何が書き込んでいるかを確認してから行う |

---

## 11. europe / america の処理を asia 側へまとめて委譲する (内部 RPC) 【Dots】

**効果: 大 (往復待ちが支配的な場合) / 難易度: 高 / リスク: 高**

施策7 (クエリをまとめる) の比較対象。europe1 / america1 で SQL を1本ずつ往復させる代わりに、一連の処理を asia 側 (DB の近く) で実行し、結果だけを受け取る。リージョンごとのキャッシュに強い整合性を新しく作るより、仕組みは単純になる。

**前提と注意**
- 公開の入口 (europe1 / america1) は各リージョンに残す。利用者の地域判定もそのまま維持する
- サーバ間の通信は**必ずルーターを経由する**。ルーターを迂回する通信はマニュアルで禁止されている。宛先は `X-Country-Code` で asia1 を指定する
- asia の既存ハンドラにそのまま転送すると、`RegionMiddleware` のリージョン確認で `region_mismatch` (403) になる。内部用のエンドポイントを別に用意し、元のリージョンを引き継ぐ必要がある
- 内部用のエンドポイントは外部から呼べないようにする (公開範囲や認証を回避できる口を作らない)
- asia 側に CPU が集中する。画像のように大きなデータを運ぶ処理は、ルーターの帯域制限で逆に遅くなる可能性がある

**手順**
1. 施策7でクエリをまとめた後も、europe / america の応答時間の大半が DB 往復の待ちかを確認する
2. クエリ数が多く、扱うデータが小さい API から試す (友達申請、フォロー、ブロックなど)
3. 1つの API だけを委譲して、europe1 / america1 の応答時間と asia1 の CPU を比べてから広げる

---

## 12. ログイン時の users 行ロックの待ちを減らす 【#940】

**効果: 中 / 難易度: 中 / リスク: 中**

ログイン (`createLoginSessionTx`、`go/session.go`) は、古いセッションを無効化する前に `SELECT id FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE` で本人の行をロックする。#940 では 821回、平均 203ms だった。ロックそのものは1人分なので、待っている相手は同じユーザーの別のトランザクション (投稿・コメント・RealMoji など、本人の行をロックしたまま処理するもの) と考えられる (推測)。

**手順**
1. 施策1・5の後に計測し直す。他のトランザクションが短くなれば、この待ちも自然に減る可能性がある
2. まだ残っていれば、何を待っているかを `pg_locks` で特定する
3. このロックが守っているのは「有効なセッションは1ユーザー1つ」という性質。外す場合は、`login_sessions(user_id) WHERE active` のユニークインデックスなど、同じ性質を保てる別の方法と組み合わせる

---

## 着手順の目安 (2026-10-31 時点)

番号は変えず、着手の順番だけを示す。実測で入れ替える。

1. 施策2・3 のベンチ確認とマージ
2. 施策5 (投稿のロック): #940 で裏付けがあり、変更が小さい
3. 施策6 (SSE の共有配送)
4. 施策4 (フィード)
5. 施策7 (往復の削減)。足りなければ施策11 と比較する
6. 施策8 (画像のバイト列のキャッシュ): 施策1の後の計測で優先度を決める
7. 施策10 の新規登録の行、施策12、施策9、その他

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
- ベンチ #940 の計測 (alp / vmstat / pidstat / pg_stat_statements): europe / america → DB の RTT 約100ms、上位クエリ、ロック待ちの回数と平均時間
- posts 約62.5万行、realmojis 約120万行 (#940 のレポートより)

**未確認**
- pprof (#940 では取得に失敗)
- 施策1〜3 を入れた後のボトルネック
- ロック待ちの相手 (誰が何を待っているか)。`pg_locks` では見ていない
- リージョン間の帯域 (RTT は計測済み)
- Dots の分析の添付資料 `START_HERE.md` (成立条件・反証テスト・根拠) は受け取っておらず、本文の要約だけを反映した
- asia2 でアプリが動いているか
- `ip_bans` に書き込んでいるもの
