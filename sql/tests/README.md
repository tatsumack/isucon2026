# Event-only 通知 SQL 回帰テスト

**専用の使い捨て PostgreSQL 16 DB 限定です。競技DB・本番DBで実行しないでください。**
`current_database()` が `isureal_event_test_` で始まらなければ、接続確認以外のSQLを拒否します。
固定schema `isureal_event_fixture` のみをsearch_pathへ設定し、publicは対象外です。
このschemaの既存テストデータは削除・再作成します。fixtureにはDDLとCOMMITがあるため、
「最後にROLLBACKするので安全」というテストではありません。既存サービスと接続しない
独立したtest DBを用意してください。DBの自動作成・削除はしません。

## 実行

Python 3・psql・PostgreSQL 16が必要です。reset SQLの
`SET session_replication_role` を実行できる専用テストロールを使用します。

```sh
# 先に、例えば isureal_event_test_local という専用DBを用意する。
# 接続URLはローカル環境で安全に設定する（ログやコミットに含めない）。
export ISUREAL_TEST_DATABASE_URL
python3 sql/tests/run.py
```

`ISUREAL_TEST_DATABASE_URL` の未設定・接続先DB名不一致は実行拒否です。
`PSQL` でpsql実行ファイルを指定できます。DSNは証跡へ出力しません。
証跡SQL・ログ・対象source SHA256は一時ディレクトリに保存し、最後にパスを表示します。
接続secretを含む環境変数やURLをリポジトリへ保存しないでください。

## 範囲

最新の実schema/reset、Goのmembership UPSERT、notificationPollQuery、startup migrationを抽出。
初期active seed、世代reset、同epoch A→B→A、false predecessor、cut後の登録/移動/退会、
region別epoch、登録/開始rollback、全3regionの同時刻候補、legacy ID/createdAt優先、
厳密connectedAt境界、現在user status/region、reset generationの投影、SHA256由来UUIDv7を
独立計算と照合します。adminの順序は直列fixtureで再現しています。

**Go経由DB接続・advisory lockの実待機・並行transaction・SSE wire・競技ベンチは対象外です。**
単一processでの順序列挙を、並行実行の証明とは扱いません。
