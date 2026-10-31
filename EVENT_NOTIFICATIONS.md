# 世代履歴による通知

## 導入手順

初期値は `ISUREAL_EVENT_ONLY_NOTIFICATIONS=false`。`true` の完全一致だけが新規windowのevent方式を有効化する。履歴追跡と既存event読取はflagに関係なく続ける。

1. 通常trafficを止め、全ノードの進行中リクエスト・transactionを完了させる。競技サーバへの適用とベンチ実行は運用者が別途行う。
2. `sql/005-event-notifications.sql` を適用する（新binaryの起動時にも同一DDLを適用）。既存windowは `cut_epoch=NULL` のlegacyのままで、起動だけではbaselineを作らない。
3. **全readerと全writerを新binaryへ更新する**。旧binary混在は不可。旧writerはmembership履歴を記録せず、旧readerはevent通知を読めない。
4. 全ノードの通常trafficを停止し進行中リクエストをdrainしたまま `/api/initialize` を1回実行する。従来reset・画像resetを維持し、DB reset transaction内で世代0・active membership seed・ready状態・generation更新を一体でcommitする。現在所属から初期windowの通知は再生成しない。
5. 初期化成功を確認後、開くwindowをevent方式にするノードでflagを `true` にして再起動する。baseline未作成時のevent開始は拒否される。通常trafficを再開する。

`init.sh` はmigrationをresetより前に実行する。`001-schema.sql` で初期DBを作り直した場合もreadyはfalseで、initializeが必要。migration単独では既存populationを走査しない。

## 整合性・リセット

membershipは `(user_id,region,epoch)` の最終stateを保存する。退会後もfalse履歴を残す。region_stateへのFKを付けず、ユーザー更新をregion行ロックで直列化しない。開始側はexclusive advisory、所属更新側はuser row lock → 固定順shared advisory → **READ COMMITTEDの別statementでepoch読取**（lock取得後のINSERT SELECTに統合し、履歴追加は1往復）の順。flag=falseで開始したlegacy windowでもepoch境界は進める。

table排他だけでは既にepochを読んだ待機writerの排除にはならないため、停止＋drainは必須。resetはusers/windows/history/stateをtable排他し、世代とcutを別々に戻さない。既存のinitialize同様、通常trafficと並行した運用はサポートしない。初期化前後はローカルSSEを破棄し、他ノードの古い購読は次回pollでDB generationを照合し無効化する。ローカルResetはqueue済み通知をDisconnectに置換し、退役した購読への進行中pollの配送も拒否する。ただし既にhandlerが受領してwireへ送信中の通知は撤回できないため、traffic停止とdrainの前提は必要。履歴GCは実装しない。

## 切戻し

まずflagをfalseにし、新binaryのまま運用する。以後のwindowは従来fanoutになるが、過去event windowの読取・履歴記録・epoch更新は継続するため再有効化できる。**flag切戻しはbinary切戻しではない**。

旧binaryへ戻す場合は全traffic停止 → 全event windowを含む状態のinitialize/reset → 旧binaryへ全ノードを揃える。event windowや履歴が残る状態で旧reader/writerを混ぜない。新schemaは付加的で旧binaryへ戻す際に削除する必要はない。旧binary運用後に再導入するときは、全binary更新後のinitializeを再実施する（ready値が残っていても信頼しない）。

## 検証上の区別

Goテスト・SQL fixtureで仕様整合性を確認するが、実際の同時接続によるlock待機/transaction境界と競技スコアは別の検証である。未接続ユーザーを含めた対象集合を保持しつつ、開始transactionから人口依存のrecipient scanを除くことが目的で、得点増を保証しない。

## 通知ID・読取計算量

新通知IDはstarts_atの48bitミリ秒prefix、UUID version 7、RFC4122 variantを保ち、namespace・user ID・window IDのSHA-256から残り74bitを決定する。PostgreSQL組込みsha256を使い拡張は追加しない。再接続・再起動でも同じ値で、legacyレコードが同じuser/windowにあるときはそのIDとcreated_atを優先する。旧random IDとのbit単位一致は求めない。

IDは従来のrandom UUIDと同様に確率的な一意性であり、74bit切詰めのため衝突可能性はゼロではない。同じミリ秒prefix内でn件の場合の偶発衝突確率上界は概ねn(n-1)/2^75。永続notification行を作らないためunique制約による再抽選はしない（再抽選すると安定IDを壊す）。現APIでは識別にuser/windowも含み、購読ごと一件で終了する。ID単独で世界的な厳密一意性が必要な契約へ拡張するならこの方式は再設計が必要。

開始はregion境界1件とwindow1件だけで、人口分の走査・JSONB構築・通知INSERTをしない。受信は全regionのactive window（最大3件）を一度取り、購読者ごとに履歴のpredecessorを主キーで引く。履歴総量は初期population＋membership変更数で、開始window数×全populationには比例しない。

## ローカルテスト

`cd go && go test ./... && go vet ./...`。`go test -race ./...` でGo側の競合検出も実行できる。

`ISUREAL_TEST_POSTGRES_DSN` に**専用テストDB**の接続文字列を指定すると `TestNotificationHubPostgres` が実際のhub SQLをTEMP表に対して検査する。未指定時は明示skip（実DB PASSとは数えない）。SHA-256の独立Go参照、v7形式、他regionの過去対象、false predecessor、legacy優先、strict時刻境界を含む。単一接続なので並行MVCC検証ではない。
