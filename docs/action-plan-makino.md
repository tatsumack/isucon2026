# ISUReal 改善の実行計画 (makino 担当分)

ベンチ #2540 (score 7427 / PASS) の生データ (`bench-results/2540/`) を読み直し、`docs/action-plan.md` に書かれていないことを施策にした。
順位は #2540 の計測とコードの静的解析に基づく推測であり、実測で入れ替える前提。

出典の表記: 【#2540】= ベンチ #2540 の生データ (pprof / pg_stat_statements / timeline)、【コード】= コードと設定の静的解析

施策の番号は着手順の管理に使っているため変えない。`docs/action-plan.md` の施策と区別するため、番号に `M` を付ける。後から見つかった施策は末尾に追加する。

## 進捗

| 施策 | 状態 |
|---|---|
| M1. DB 接続を使い回す | 未着手 |
| M2. 期間内の投稿〜フィード完了率を計測する | 未着手 |
| M3. Memories の画像署名の計算を減らす | 未着手 (action-plan.md の施策3 / PR #10 の後) |
| M4. ログイン失敗時に users 行をロックしない | 未着手 |
| M5. プロフィール画像の 400 / 403 を正常な挙動として確認する | 未着手 |

## 全施策に共通する注意

- `docs/action-plan.md` の「全施策に共通する注意」に従う
- 1施策 = 1ブランチ。着手前に Slack で宣言し (`[着手] isu1 / ブランチ名 / 内容`)、ベンチが通ってスコアが下がらないことを確認してから main へマージする
- 効果の確認には、alp の平均応答時間に加えて M2 の「期間内の完了率」を使う。スコアに直接つながる指標のため

---

## M1. DB 接続を使い回す (接続の作り直しをなくす) 【#2540】【コード】

**効果: 大 (特に europe / america) / 難易度: 低 / リスク: 低**

`go/server.go:34` は `SetMaxOpenConns(30)` だけを設定しており、`SetMaxIdleConns` は Go の初期値の **2** になっている。同時に3本目以上使った接続は、使い終わるたびに閉じられ、次のクエリで作り直される。

- 根拠【#2540】: asia1 の pprof で、CPU の **17.8% が pgx の接続確立** (`pgconn.connect` → `scramAuth` → `pbkdf2`)
- PostgreSQL は `ssl = on` (`etc/postgresql/16/main/postgresql.conf:108`)、認証は scram-sha-256。新しい接続1本ごとに TCP 接続・TLS ハンドシェイク・SCRAM 認証で **5〜6往復** かかる
- europe / america → asia2 の RTT は約100ms なので、**接続1本あたり 0.5〜0.6秒** と見積もれる (推測)。europe1 / america1 は CPU がほぼ空いているのに `POST /api/posts` が平均4.7秒かかっている原因の一部と考えられる

**手順**

1. 変更前の値を取る: ベンチの前後で asia2 の `SELECT sessions FROM pg_stat_database WHERE datname = 'isureal';` を記録し、ベンチ中に作られた接続の数を出す
2. `go/server.go` に `db.SetMaxIdleConns(30)` を追加する (`SetMaxOpenConns` と同じ値)。必要なら `SetConnMaxIdleTime` も設定する
3. ベンチを実行し、次を確認する
   - `pg_stat_database.sessions` の増え方が大きく減っている
   - asia1 の pprof から `scramAuth` / `pbkdf2` がほぼ消えている
   - europe1 / america1 の主要 API (`POST /api/posts`、`GET /api/feeds/friends`、`POST /api/auth/login`) の平均応答時間 (alp)
   - M2 の期間内の完了率
4. 効果を確認してから、別ブランチで DSN に `sslmode=disable` を付けて TLS の往復をなくすことを検討する。DB との通信は私設ネットワーク内だが、マニュアル・レギュレーション上問題ないかを先に確認する

**注意**

- アプリ3台 × 30接続 = 90 で、`max_connections` (100) の範囲内。接続を保持し続けるようになるため、上限を上げる場合は `max_connections` も見直す (action-plan.md の施策10)
- europe / america は1クエリに約100ms かかるため、接続1本あたりの処理能力が低い。接続を使い回すようにした後でプールが足りなくなっていないか (`database/sql` の待ち) も確認する

---

## M2. 期間内の投稿〜フィード完了率を計測する 【#2540】

**効果: 間接的 (施策の効果をスコアに結びつけて比べられる) / 難易度: 低 / リスク: なし**

「投稿後の友達フィードの検証通過」は1件10点 (最終日20点) で、投稿期間の終了前にフィードの検証まで終える必要がある。#2540 の timeline をリージョン別に集計すると、europe / america で多く取りこぼしている。

| リージョン | 投稿数 | 期間内にフィード取得まで完了 |
|---|---|---|
| asia | 103 | 103 (100%) |
| europe | 30 | **19 (63%)** |
| america | 18 | **12 (67%)** |
| 最後の期間 (3リージョン計) | 44 | 41 (93%) |

- `bench-timeline.py` の「投稿期間ごとの投稿〜友達フィード完了 (M2)」の値 (#2540 の基準値)。最後の期間は終了時刻がログに無いため別に数えている
- 期間の境界は admin の `POST /api/admin/posting-window` の完了時刻。このリクエストが最大3.4秒かかるため、開始時刻を境界にすると europe 43%・america 44% になる。実際の値はこの間にある (詳細は bench-results/2540/report.md)
- 同じリージョンの投稿期間は約9秒ごとに切り替わる。期間開始から投稿開始まで p50 で asia 1.5秒 / europe 3.2秒 / america 4.0秒、投稿開始からフィード完了まで asia 1.0秒 / europe 5.2秒 / america 4.7秒
- europe / america は倍率1.5倍なので、取りこぼしの影響が一番大きい
- 期間のリージョンはログに無い (admin のリクエストは全部 asia1 に来て、region は body にしか無い)。SSE (`GET /api/notifications`) が閉じた時刻から巡回の順番を推定している (asia → europe → america)。正確に取るには、ベンチ後に asia2 の `posting_windows` (region, starts_at, ended_at) を読む方法がある (未実装)

**手順**

1. `bench-timeline.py` に、リージョン別の「投稿数 / 期間内にフィード取得まで完了した数 / 期間開始から投稿開始までの秒数」を出す集計を追加する
2. 投稿期間のリージョンの割り当てを正確にする (リクエストの順番による推定ではなく、レスポンスやアプリのログから取れないか確認する)
3. `/bench-analyze` のレポートにこの表を載せる (`.claude/skills/bench-analyze/SKILL.md`)
4. #2540 の数値を基準として残し、以降の施策 (M1、action-plan.md の施策2・5・6・7) の前後で比べる

---

## M3. Memories の画像署名の計算を減らす 【#2540】

**効果: 中〜大 (asia1 の CPU) / 難易度: 低〜中 / リスク: 低〜中**

action-plan.md の施策3 (画像署名の使い回し、PR #10) への補足。施策3 ではフィードを例にしているが、#2540 で一番多く署名を作っているのは **Memories** だった。

- 根拠【#2540】: asia1 の CPU のうち `listMemories` が36%、その中の `loadMemoryInteractions` → `newRealMojiResponse` が25%。`imageSignature` 全体では55%
- `GET /api/memories?month` は3番目に多い API (545回)。1回3点

**手順**

1. PR #10 がマージされた後にベンチを実行し、pprof で `listMemories` 配下の `imageSignature` の割合を確認する
2. まだ大きければ、Memories で返す RealMoji の件数・画像の数を確認する。署名が必要な画像だけに絞れるか、同じ画像の署名を1リクエスト内で使い回せるかを検討する
3. レスポンスの JSON の形 (フィールド、並び順、件数) は変えない

---

## M4. ログイン失敗時に users 行をロックしない 【#2540】【コード】

**効果: 小〜中 / 難易度: 低 / リスク: 低〜中**

`go/user_api.go:136` のログインは、`SELECT ... FROM users WHERE username = $1 FOR NO KEY UPDATE` で行をロックしてから secret key を照合している。照合に失敗するログインでも、正規の利用者の行をロックする。

- 根拠【#2540】: `POST /api/auth/login` 1066回のうち 401 が559回 (52%)。失敗するとすぐにリトライされる。ログインだけを繰り返す host が13
- action-plan.md の施策12 は `createLoginSessionTx` (`go/session.go`) のロックが対象で、この箇所は含まれていない
- このクエリは pg_stat_statements の上位20件に入っていなかった。ログインの多くは asia1 (DB との RTT 約1ms) に来るため、ロックを持つ時間は短いと考えられる。効果は小さめの見込み

**手順**

1. ロックなしで users を取得して secret key を照合し、成功したときだけ本人の行をロックする (またはロックは `createLoginSessionTx` のものだけにする)
2. 照合からロックまでの間に退会された場合に備え、ロック後に `status = 'active'` を確認する (`createLoginSessionTx` はすでに確認している)
3. ログイン失敗時のレスポンス (401 `invalid_credentials`) と、IP BAN の判定に使われる `c.Set("username", ...)` は変えない (action-plan.md の施策14)

---

## M5. プロフィール画像の 400 / 403 を正常な挙動として確認する 【#2540】

**効果: 間接的 (action-plan.md の施策13 を閉じられる) / 難易度: 低 (調査) / リスク: なし**

action-plan.md の施策13 (プロフィール画像の変更が失敗している) への補足。#2540 の timeline では、失敗はベンチマーカーの検査によるものと読める。

- 根拠【#2540】: `PUT /api/me/profile-image` は 400 が78件、403 が23件、成功 (204) が12件
- 400 の後は 0.5秒ちょうどの間隔でリトライされ、その後 403 (`ip_banned` と思われる) になる。「登録 → ログイン → プロフィール画像を3回送って止まる」利用者が22人いる
- 不正な画像を送る検査と、その後の BAN が正しく働いていると考えられる

**手順**

1. nginx のアクセスログで、400 を返した host が後で 403 を受けていることを確認する
2. 403 のレスポンス本文が `ip_banned` であることを、アプリのログで確認する
3. 正規の利用者 (成功している host) が 403 を受けていないことを確認する
4. 正常な挙動と確認できたら、action-plan.md の施策13 を「対応不要」として進捗表を更新する

---

## 着手順の目安 (2026-10-31 時点)

番号は変えず、着手の順番だけを示す。実測で入れ替える。

1. M2 の手順1 (`bench-timeline.py` に集計を追加する)。M1 の効果を比べるため、先に基準を取れるようにする
2. M1 (DB 接続の使い回し)
3. M5 (調査のみ。他の作業のベンチ結果を使って並行して進められる)
4. M3 (PR #10 のマージ後)
5. M4
