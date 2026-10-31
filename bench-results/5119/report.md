# ベンチ #5119 分析 (M1: SetMaxIdleConns(30))

- ブランチ: `tatsumack/makino-m1-db-idle-conns` (`84951c2`)。asia1 / europe1 / america1 にデプロイ。asia2 は変更なし
- 比較対象: main の最新ベンチ #3790 (score 17871、計測 `022455`、デプロイ `f1ee00b`)。`f1ee00b` と main (`009efc5`) の `go/` `etc/` の差分はなし。今回の差分は `db.SetMaxIdleConns(30)` の1行のみ
- 結果: **PASS / score 24558** (#3790 比 +37%)。ベンチ1回ずつの比較なので、ぶれを含む
- 計測時間: 今回150秒 (ベンチ全体を含む)、#3790 は70秒。回数 (COUNT) は直接比べられない。平均応答時間と割合で比べる

## 概況

- ボトルネックの所在: **asia1 のアプリCPU (画像署名)** と **DB のロック待ち**。asia1 の CPU は平均 us 27.5% / idle 62.4% (150秒平均、ベンチ外の時間を含む)。pprof の 47.99% が `computeImageSignature`
- 接続の作り直しはほぼなくなった
  - asia2 の `pg_stat_database.sessions`: ベンチ前 28813 → 後 28876 (**+63 本**)。この前の10分間 (別ブランチのベンチ #4627 とデプロイを含む) は 25768 → 28813 で +3045 本
  - ベンチ直後の接続数: asia1 31 / europe1 15 / america1 12 (変更前は各 2〜3 本)
  - asia1 の pprof: `pgconn.connectOne` / `scramAuth` / `pbkdf2.Key` が上位300件に出なくなった (#3790 では connectOne 11.3%、scramAuth 9.4%)
- europe / america の応答時間 (alp 平均)

| API | europe1 #3790 → #5119 | america1 #3790 → #5119 |
|---|---:|---:|
| `POST /api/posts` | 2.248s → **1.561s** (-31%) | 2.203s → **1.560s** (-29%) |
| `GET /api/feeds/:name` | 0.568s → **0.450s** (-21%) | 0.572s → **0.443s** (-23%) |
| `POST /api/auth/login` | 1.193s → 0.999s (-16%) | 1.154s → 0.972s (-16%) |

- login の最小値は変わらず約 0.708s (#3790 も 0.709〜0.721s)。login の遅さは接続確立ではなく、DB との往復回数によると考えられる (推測。往復回数は数えていない)

## ベンチマーカーの行動 (timeline/report.txt より)

- `POST /api/posts → GET /api/feeds/friends` が 435 回。投稿直後に友達フィードを読む流れは #2540 と同じ
- グランプリの人数: アジア 71 / ヨーロッパ 5 / アメリカ 5 (Stdout)。各日の人数は europe 7・america 6 のまま増えていない
- M2 の「期間内の完了率」はまだ集計していない

## ボトルネック上位

### 1. 画像署名の計算 (asia1 CPU)
- 根拠: asia1 pprof の 47.99% が `computeImageSignature`。`listMemories` 32.6%、その中の `loadMemoryInteractions` → `newRealMojiResponse` 21.2%
- 対策案: action-plan.md の施策3 / makino の M3 (署名の使い回し)
- 期待効果: 大。接続確立の分の CPU が空いたので、次に大きいのはここ

### 2. advisory lock 待ち
- 根拠: `pg_advisory_xact_lock` 174 回、平均 300.96ms、SQL 時間の 18.4% (#3790 は平均 229.84ms、10.8%)
- 対策案: 登録・投稿期間作成のロック範囲を見直す (action-plan.md 参照)
- 期待効果: 中

### 3. 2ユーザーの行ロック
- 根拠: `SELECT id FROM users WHERE id IN ($1, $2) ... FOR NO KEY UPDATE` 1717 回、平均 48.23ms、SQL 時間の 29.1%
- 期待効果: 中。ロック待ちが主なので、トランザクションを短くする必要がある

### 4. export のクエリ
- 根拠: 115 回、平均 381.41ms、15.4%。asia1 pprof でも `exportPostsHandler` が 15.9%

### 5. asia2 の I/O 待ち
- 根拠: vmstat の wa が平均 12.3% (#3790 は 6.0%)。`image_objects` の読み書きが多い (SELECT 5188 回、INSERT 463 回)

## 確認したこと / していないこと

- 確認した: PASS、score、接続数の差、pprof から接続確立が消えたこと、europe / america の平均応答時間
- 確認していない: 同じ条件でのベンチの再現性 (1回のみ)、M2 の完了率、`database/sql` の接続待ち (`sql.DB.Stats` の WaitCount は取っていない)。ベンチ後に asia1 から 31 本の接続があり、アプリの上限 30 に達していた可能性が高い (1本はアプリ以外のプロセスと思われる。未確認)。asia1 でプールが足りているかは次に確認する
