# ISUCON2026

ISUCONの競技用リポジトリ。サーバ上の `/home/isucon/webapp` をそのままリポジトリにしている。
アプリの概要・エンドポイント・テーブルは競技開始後に追記する。

## 構成
- サーバは4台。`~/.ssh/config` のHost名で入れる
  - `asia1` `europe1` `america1`: nginx + Goアプリ (`isureal-go.service`)。リージョンごとに固定
  - `asia2`: 共有PostgreSQL 16
- `go/`: Goの実装。他言語の実装はリポジトリで管理しない
- `etc/`: サーバの `/etc` からコピーしたミドルウェアの設定 (nginx, systemd, postgresql)。deploy.shでサーバに反映される
- `etc/env/<host>.sh`: ホストごとの `/home/isucon/env.sh`
- `deploy.sh`: `./deploy.sh <branch> [host...]` でデプロイする。host省略時は全台
- `bench-capture.sh`: ベンチ中の計測スクリプト。`/bench-analyze` スキルから使う
- `install-tools.sh`: 計測ツール (alp) をアプリサーバに入れる。各サーバで1回だけ実行する
- `docs/manual.md`: 当日マニュアル。`docs/regulation.md`: レギュレーション。当日マニュアルの内容がレギュレーションより優先される

## 進め方
- 1施策 = 1ブランチ。mainへは、ベンチが通ってスコアが下がらないことを確認してからマージする
- 着手前にチームのSlackに作業を宣言する (`[着手] isu1 / ブランチ名 / 内容`)。Claudeが作業を始めるときも、宣言済みか確認する
- ベンチはポータル (https://portal.isucon.net/contestant/benchmark_jobs) から実行する。ポータル以外から同様のリクエストを送って実行してもよい (運営に確認済み)
- 序盤にベンチマーカーの行動分析 (どのAPIをどの順番・条件で叩いているか) を行う。手順は `docs/benchmarker-analysis.md`

## 守ること
- `/initialize` の挙動を変えない。初期化処理で作ったキャッシュやテーブルは `/initialize` 内で必ず作り直す
- レスポンスのJSONの形 (フィールド名、型、null/空配列の扱い、並び順) を変えない
- キャッシュを入れるときは、どこで無効化・更新するかをコミットメッセージに書く
- 1つの変更で複数の施策を混ぜない。スコアの増減の理由がわからなくなるため
- 作業の報告では、確認したこと・確認していないことを分けて書く
- サーバ上で直接ファイルを編集しない。変更はリポジトリ経由でdeploy.shで反映する

## IP BAN (isureal-guard)
アプリサーバ3台で `isureal-guard.service` (`/opt/isureal-guard/bin/isureal-guard`、ソースは `/opt/isureal-guard/src`) が動いている。リポジトリでは管理していない。asia2 では停止している。
- `journalctl --follow --output json` でジャーナルを読み、`_SYSTEMD_UNIT` が `isureal-*.service` に一致する行の MESSAGE を、アプリのアクションログ (1行1つのJSON) として解釈する
- 次の条件で、その IP を DB の `ip_bans` に2分間登録する。アプリの `IPBanMiddleware` が `ip_bans` を200msごとに読み、全APIで `403 ip_banned` を返す。DB は共有なので、BAN は全リージョンに効く
  - `action:"login"` かつ `status:401` が、同じ `ip` から10秒以内に10回
  - `action:"put_profile_image"` かつ `status:400` かつ `abuse:"invalid_image"` が、5秒以内に2回
  - `action:"get_image"` かつ `status:403` かつ `abuse:"invalid_signature"` が、5秒以内に3回
- そのため、次を変えると BAN が効かなくなる
  - アプリの unit 名 (`isureal-*.service` に一致すること) と、アクションログを stdout に1行1つのJSONで出すこと
  - アクションログの `action` `status` `ip` `abuse` フィールドと、上の `action` 名・`abuse` の値。`ip` は `X-Client-IP` (`c.RealIP()`)
  - ハンドラの `c.Set("abuse", ...)`、ログイン・登録の `c.Set("username", ...)`
  - 上の3つのログのレベル。いずれも `info` (GET は `debug` だが、`invalid_signature` の 403 だけ `info` に上げている)
- ログレベル (`ISUREAL_LOG_LEVEL`、未設定なら `debug`) は `info` まで上げてよい。`error` にすると BAN が止まる
