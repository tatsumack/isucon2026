# 投稿期間通知の compact 正本（既定無効）

## 採点上の狙いと未確認事項

従来は投稿期間の開始トランザクション内で、地域の所属変更を排他しながら全 active
ユーザー分の通知 UUID を生成し、通知行・インデックス・外部キーを更新していた。
その間にも startsAt からの期限が進む。本変更は全員分の通知行生成を公開の前提から外し、
正確な受信対象集合を 1 行の永続データに置き換える。投稿可能時間を増やし、その後の
友達フィード検証・エクスポート等の得点につなげる、サービスのデータ表現の変更である。

集合の走査・JSONB 集約・TOAST 処理のコストは残る。開始処理が O(1) になるわけではない。
競技サーバへの適用・ベンチマークは実施しておらず、スコア向上は未確認。

## 仕様を維持する仕組み

- 既存の地域 exclusive advisory lock の下で、開始時点のその地域の **全 active
  ユーザー**を保存する。接続中のユーザーだけを対象にしない。JSONB object のキーで、
  UUID 以外・大小文字・引用符・日本語を含む既存 varchar ID もそのまま扱う。
- 旧期間の終了・新期間の作成・受信対象集合の保存を同一トランザクションで commit。
  commit 時点で対象者全員の通知が論理的に存在し、非同期生成を待つ隙間がない。
- hub は **1 回の SQL** 内で既存通知と virtual 通知を UNION ALL する。
  `(user, window)` の既存通知行があれば virtual 側から除外し、初期通知を含む既存 ID を
  優先する。全候補を `created_at, id` 順に並べて 1 件を返す。
- `created_at > connectedAt` は厳密な不等号のまま。virtual 通知の createdAt は startsAt。
  終了済み期間は除外し、候補は **全 3 地域**から読む。接続地域で期間を絞らないため、
  移動後に元地域へ戻ったユーザーの元の通知も残る。
- 現在のユーザー status / region も同じ SQL snapshot で取得し、既存どおり退会・移動を
  検出して切断する。別 statement 間で所属・通知候補が食い違う競合がない。
- reader は初回通知を含め常に読み取り 1 往復。通知 ID の lazy INSERT、キャッシュ、
  バックグラウンド生成、process epoch、再試行状態は不要。

## virtual 通知 ID

`compact_notification_id(user_id, window_id)` は、固定・バージョン付き namespace と
userID の UTF-8 byte 長、区切り、userID、windowID を PostgreSQL 組込み SHA-256 で hash
する。長さ prefix によって任意の既存 ID でも組の連結に曖昧さがない。
122 bit を保持し、version 8 / RFC variant を設定して、従来と同じ 36 文字の
ハイフン付き 16 進 UUID 形式にする。

**新しい compact 通知の ID は random UUIDv4 から決定的な UUIDv8 に変わる。**
既存通知 ID は書き換えない。これは認証情報ではなく識別子。衝突は理論上あり得るが、
保持する hash 空間は random UUIDv4 と同じ 122 bit であり、数学的な一意性は主張しない。
公開済み ID を変えないため、namespace / encoding / 関数を後から無断で変更しない。
複数プロセス・同時読み取り・再起動でも同じ ID になる。Go の UUID parser で形式を
確認済みだが、ベンチマーカーによる新 ID の受け入れ確認は未実施。

## initialize・再起動

受信対象集合は通常の WAL 対象 PostgreSQL データであり、プロセス内の正本を持たない。
ランダムな window ID が世代を区別する。reset は既存トランザクション内で最初に集合を
TRUNCATE してから生成済み通知・期間を削除する。writer は期間を変更する前に集合
テーブルのロックを取り、reset と lock 順を合わせる。reader にアプリ側の無効化は不要。
既存の初期通知は従来の reset 境界で保持する。初期 posting_windows の ended_at を
復元しない既存 reset の挙動は本変更の対象外で、初期期間の集合は新規作成しない。
アプリ全体の `/initialize` 実行は未検証。

## 適用・有効化・切り戻し手順（未実施）

1. **先に**共有 DB へ `sql/005-compact-notifications.sql` を適用する。通常起動は自動
   migration しない。新規構築の `001-schema.sql` はこれを include し、`init.sh` は
   migration → reset の順に実行する。
2. **全 3 地域の app を新 reader に更新**し、まず
   `ISUREAL_COMPACT_NOTIFICATIONS=false`（既定値）にする。
   旧 reader は virtual 通知を読めないため、compact writer を有効にした状態で混在させない。
3. writer の `ISUREAL_COMPACT_NOTIFICATIONS=true` で有効化し、ベンチ比較する。
   false のままでは従来どおり全員分の通知行を生成する。
   **新版同士**なら地域ごとの flag 値が異なっていても両形式を読める。
4. 将来の期間だけを従来生成へ戻すには flag を false にし、**新 reader・テーブル・
   関数は維持**する。既存 compact 期間も読み取れる。旧 binary への復帰やテーブル・
   関数の削除には、compact データが不要かを確認して状態整理を別途計画する必要がある。
   この手順はデータ消去・initialize・デプロイの実行許可を意味しない。

## 検証結果と残るリスク

- ローカル `go test ./...` / `go test -race ./...` / `go vet ./...` は PASS。
  opt-in の Go / DB integration **4 件は SKIP**（DSN 未設定）。通常テスト成功を DB 検証
  成功と読み替えない。UUID parser と既定無効の flag は DB なしで検証する。
- PostgreSQL 16.10 standalone backend で実 migration と hub の単一 SQL を実行して
  PASS。任意文字列の exact membership・空集合・同時刻の全地域候補順・厳密な接続時刻境界・
  後から登録したユーザーの除外・既存 ID 優先・集合削除後の phantom 不在を確認した。
  SHA-256 の独立 Python 期待値 7 組とも一致。これは複数 session の競合検証ではない。
- opt-in Go テストは、開始時に未接続の受信者、後登録除外、退会、移動・戻り、並び順、
  既存 ID、再生成した hub での安定性、reset、rollback、parallel reader、任意 ID、
  従来生成との共存を扱う。**専用ローカル DB** に対して実行する:

  ```sh
  cd go
  ISUREAL_TEST_DATABASE_URL='postgres://.../dedicated_test_db?...' go test -race -run 'Compact.*Integration' ./...
  ```

  テスト専用 schema を作成・削除する。今回の環境では PostgreSQL socket bind が sandbox
  に拒否され、正規の権限昇格も環境準備で失敗したため live Go / DB テストは未実施。
  実際の process kill / restart、HTTP 全体、競合、およびベンチマークは未確認。
- JSONB 集約は開始処理に残り、subscriber ごとの membership 判定では大きい object の
  detoast が繰り返される可能性がある。CPU / I/O とスコアは有効化前後で実測する。
  ベンチマーカーや特定ユーザーだけを識別する特別扱いは入れない。
