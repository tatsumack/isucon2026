# #11801 の報告に出ていないボトルネック

`bench-results/11801/report.md` (score 54926、画像本体を asia1 のファイルへ移した構成) の計測データを見直し、報告の「ボトルネック上位」に入っていないものを洗い出した。

結論: 大きめのものは2つ。DB は idle 73% で、上位のクエリも DB 時間の 12% 以下なので、DB 側に大きな見落としはない。

| # | 内容 | 推定効果 | 対策の種類 |
|---|---|---|---|
| 1 | asia1 のディスクの読み書きが上限近い (書き込み 2.8GB、iowait 20〜43%) | 中〜大 | nginx の設定 |
| 2 | europe1 / america1 のログイン・登録・プロフィール更新が遅い (ログイン平均 1.05s) | 中 | asia1 への転送の対象を広げる |
| 3 | DB との通信が TLS になっている | 小 | DSN に `sslmode=disable` |
| 4 | プロフィール画像の変更 (大きな画像の読み込み) | 小 | アプリ |
| 5 | ログの処理 (journald) | 小 | 施策14 が終わるまで触らない |

## 1. asia1 のディスクの読み書きが上限近い

- 根拠
  - vmstat の合計 (150秒): asia1 は読み込み 2,373MB、**書き込み 2,786MB**。europe1 の書き込みは 162MB、america1 は 86MB、asia2 は 840MB
  - CPU が張り付いた区間 (計測開始から70〜90秒) は、読み書きの合計が毎秒 130〜190MB、iowait は 20〜43%
  - `bench-results/11801/report.md` の3番は読み込み (ページキャッシュに収まらない初期データの画像) だけを扱っていて、この書き込みには触れていない
- 書き込みの出どころ (推測)
  - 投稿画像の保存: 約1,080件を1件ずつ fsync している (`go/image_store.go` の `writeFileAtomic`)。数百MB と見る
  - 残りの大半は **nginx の一時ファイル** と考えている。nginx の設定 (`etc/nginx/sites-available/isureal.conf`) はバッファまわりが既定値のままで、大きいリクエスト・レスポンスはディスクに書いてから送る
    - asia1 の nginx が返したもの: 画像 393MB、export 199MB (alp の SUM(BODY))
    - 投稿とプロフィール画像のアップロード (asia のユーザーの分) も一時ファイルを通る
  - asia1 の `/var/lib/nginx/proxy` と `/var/lib/nginx/body` が使われていることは確認した (ディレクトリの更新時刻)。書いた量は測れていない
- 対策案
  - `/api/images` と export の場所で `proxy_buffering off`、または `proxy_buffers` を大きくして一時ファイルを使わないようにする (`proxy_max_temp_file_size 0` も候補)
  - `client_body_buffer_size` を大きくする
  - nginx の設定だけで、アプリの動作とレスポンスは変わらない
- 先に確かめること: ベンチ中にどのプロセスがディスクに書いているか。`bench-capture.sh` の pidstat に `-d` を足すか、iostat を取る
- リスク: `proxy_buffering off` にすると、遅いクライアントの間はアプリのコネクションが空かない。asia1 のメモリ (使えるメモリは約3GB) との兼ね合いを見る

## 2. europe1 / america1 のログイン・登録・プロフィール更新が遅い

これらの API は asia1 へ転送されず (`go/server.go:62-69`)、europe1 / america1 から DB (asia2) と何度も往復している。

| API | asia1 | europe1 | america1 |
|---|---|---|---|
| `POST /api/auth/login` | 0.117s | **1.053s** (509回、合計 536秒) | 0.610s (266回) |
| `POST /api/auth/register` | 0.160s | 0.751s | 0.609s |
| `PATCH /api/me` | 0.257s | 1.166s | 0.677s |
| `PUT /api/me/profile-image` | 1.032s | 1.530s | 0.882s |

- ログインは、ベンチの利用者が最初に必ず通る API。ログインの後は Memories と画像の取得が続く (timeline/report.txt の「A の次に来るもの」)。europe / america は得点の倍率が 1.5
- 原因箇所: ログインは、ユーザーの取得 → BEGIN → advisory lock → セッションの更新 → COMMIT で、DB と約5往復する (`go/user_api.go:119`、`go/session.go:139`)。リージョン間の往復は約100ms
- 対策案: ほかの API と同じく、asia1 へ転送する対象 (`delegate`) に入れる。往復は1回になる
- 対応: `perf/delegate-auth-and-me` で実装した。ログアウト (europe1 平均 0.878s、68回) と `DELETE /api/me/profile-image` も合わせて転送する
- リスク
  - `isureal-guard` は、アプリのログを読んで不正なアクセスを BAN していると推測されている (`docs/action-plan.md` の施策14)。転送すると、ログイン失敗のログが asia1 側に出るようになる。BAN の判定に影響しないかを確かめる
  - 転送元 (europe1 / america1) の行動ログでは、ログイン・登録の username が null になる (ハンドラーが転送先で動くため)。username つきの行は asia1 に出る
  - 転送は今までどおりルーター経由で行う
- 参考: europe1 の `GET /api/images` も平均 0.456s (1,924回、合計 877秒で europe1 の最大)。1回ごとに配信してよいかを DB に問い合わせている。削除後5秒の決まりがあるため、キャッシュする場合は有効期間を5秒より十分短くする必要がある

## 3. DB との通信が TLS になっている

- 根拠: DSN に `sslmode` の指定がなく (`go/config.go:42`)、PostgreSQL は `ssl = on`。asia1 の pprof の約3% が `crypto/tls` (`Conn.Write` 1.08s、`readRecordOrCCS` 0.94s)
- `docs/action-plan-makino.md` の M1 手順4 で「未着手」のまま
- 対策案: DSN に `sslmode=disable` を付ける。マニュアル・レギュレーション上の問題がないかは先に確認する
- 期待効果: 小。asia1 と asia2 の CPU が少し空く

## 4. プロフィール画像の変更

- 根拠: asia1 の `PUT /api/me/profile-image` は 207回、平均 1.032s、最大 13.5s。400番台が106件、502 が19件。pprof の 4.4% (3.07s) を使い、ほとんどが `io.ReadAll` の中の `memclrNoHeapPointers` (1.69s)。大きな画像が送られていると推測する
- 原因箇所: `go/profile_image_api.go` で、本文を大きさの見当なしに `io.ReadAll` で読む
- 対策案: `Content-Length` で先にバッファを確保する。不正な画像を早く 400 で返せないかを見る
- 期待効果: 小

## 5. ログの処理

- 根拠: asia1 で `journalctl` と `systemd-journal` が合わせて約3.7% (pidstat の平均。isureal 本体は 48%)。アプリのログは debug で、リクエストごとに出している
- ログは `isureal-guard` の判定に使われている可能性があるため、施策14 が終わるまで触らない (`docs/action-plan.md` の表の「アプリのログレベル (debug) を下げる」と同じ判断)

## 大きくないと判断したもの

- DB のクエリ: 上位は、画像を配信してよいかの確認 (7,873回、平均 1.95ms、DB 時間の 12.1%)、ユーザー検索 (1,140回、10.69ms、9.6%)、友達申請の2ユーザーの行ロック (3,930回、2.98ms、9.2%)。asia2 は idle 73% で、待ちの原因になっていない
- `GET /api/posting-window/current` (投稿の直前に呼ばれる): asia1 で p99 2.5s。転送されて asia1 で処理されるので、asia1 の CPU の問題 (報告の1番) に含まれる
- europe1 / america1 の CPU: idle 96〜98%

## 確認したこと / していないこと

- 確認した
  - #11801 の4台の vmstat と pidstat、3台の alp、asia1 の pprof (署名を除いた内訳も)、DB の slow.txt、timeline/report.txt
  - ログインとプロフィール画像の処理のコード、nginx の設定、チームの資料 (施策11・14、M1)
  - asia1 で読み取りだけ: nginx の error.log (エラー以外を記録しない設定で、一時ファイルの記録は無かった)、`nginx -T`、`/var/lib/nginx` の中身、メモリとディスクの量
- 確認していない
  - ディスクに書いているプロセスの内訳。nginx の一時ファイルの量は推測
  - asia1 のディスク (EBS) のスループットの上限
  - ログイン・登録を転送したときの、BAN の仕組みへの影響
  - ポータルの Stdout。europe / america で利用者が増えなかった理由との関係
