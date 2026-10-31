# ISUCON2026 / ISUReal Go実装 — サービス構造から狙う大きな改善案

**チーム内の実装・検証引き継ぎ用。競技中はチーム外へ公開・共有しないこと。**

- **分析対象**: upstream **`tatsumack/isucon2026`** の Go 実装。構造読解の基準は取得済みコードのcommit **`614854ef0bfef8e681ea5156cb33c69d0888df62`**。ソースリンクはupstreamへ統一し、根拠の行番号を保持するため基準commitへ固定する。
- **情報時点**: 2026年10月31日。**11:17 JST（02:17 UTC）、利用者がupstreamの最新mainへ同期したと申告したフォークの`main`を経由して、`74c5abfae3faa12be8c2957a5f9ec282df8a1cc3`までの差分を確認した。** upstreamへの直接照会は404となり、upstream HEAD自体は直接検証できていない。確認範囲は下記に明示する。採用前にはさらに現在のコードとの差分を確認する。
- **目的**: SQLの細かな書換え、接続数の調整等とは別に、活動の進み方・一斉投稿・地域間通信・データの更新頻度を使い、計算や転送そのものを減らす大きな変更を比較する。
- **状態**: ソースの静的読解と小さい抽象モデルの検証まで。**本書の作成過程では競技アプリの改修、実SQL・EXPLAIN・アプリ起動・競合試験・ベンチマーク・サーバ操作は実施していない。本書の提案の性能効果・改善倍率は未計測。** Gitの変更・commit・pushも行っていない。
- **このファイルだけで読める構成**: 競技前提、配点、現行挙動、5つの基本構造案、3つの深掘り案、整合性条件、実装段階、反証テスト、停止判断、再現用モデルを含む。リンク先は根拠の確認用であり、別の分析文書を読む必要はない。過去大会の事例集は対象外。

## 目次

1. [結論と証拠の強さ](#overview)
2. [競技・サービスの前提と配点](#premises)
3. [候補比較と選別順](#choices)
4. [B1: 同一地域の投稿を並行化](#b1)
5. [B2: SSEの接続別pollを共有配送へ](#b2)
6. [B3: feedを関係起点・厳密top51へ](#b3)
7. [B4: 業務処理をDB近傍RPCへ](#b4)
8. [B5: 画像の不変bytesだけ地域cache](#b5)
9. [D1: 画像専用の小さい整合モデルとblob分離](#d1)
10. [D2: 通知の全員分書込みを仮想化](#d2)
11. [D3: 疎な推薦状態・hot viewer index](#d3)
12. [横断条件: 失効、世代、応答時fence、初期化](#consistency)
13. [測定・段階導入・採否判定](#validation)
14. [ソース索引](#sources)
15. [再現付録: 抽象モデル3本と実行結果](#appendix)


<a id="overview"></a>

## 1. 結論と証拠の強さ

### 最初に押さえる結論

**一番大きい可能性があるのは、単に1リクエストを速くすることではなく、投稿期間内に「通知→投稿→友達feed検証→閲覧・反応」へ到達する人数を増やすことである。**

1. 投稿は同じ地域の現役window行を排他ロックし、画像保存まで保持する。期間に全員が集まるサービスなのに、地域内投稿を1本ずつ処理する形になっている。**B1は変更境界が比較的小さく、最初の反証実験に向く。**
2. SSEは待っているだけの接続ごとに500ms周期で2SQLを発行する。**B2で待機人数比例の継続pollを除ける**。通知を受けるための待機が、投稿・feed用DB資源を消費する構造を変える。
3. feedは全地域の現役投稿を受け取った後に閲覧条件を判定する。**B3で「全投稿→絞込み」から「関係上の作者→投稿」へ探索方向を変える**。B1/B2で投稿が増えるほど重要になる。
4. 欧米appの各SQLがasia DBを往復する。**B4は固定地域入口を残したまま、SQL列を1つの業務RPCへ集約する**。地域判定・認証・元IP・ルーター経路は変えない。
5. 画像bytesは不変、配信可否は可変。**B5でこの2つを分離し、同じ画像の地域間再転送を避ける**。画像GET自体はfriend graphに依存しないため、さらにD1の画像専用小モデルへ進める余地がある。

高難度のD1/D2/D3は、基本案の後で残る律速に応じて選ぶ。全面地域レプリカ、全viewerランキング物化、全画像eager複製を最初から同時に導入しない。


### 最新コード・既存計測報告との関係（11:17 JST、同期済フォーク経由で確認）

前回確認した[基準commitから`d6f6064`までの差分](https://github.com/tatsumack/isucon2026/compare/614854ef0bfef8e681ea5156cb33c69d0888df62...d6f606408f4561b609ae7d6d23d83d19cb3cba40)では、業務Go handlerは基準commitから変更されておらず、Goの実行コード差分は`go/cmd/isureal/main.go`のpprof追加。`sql/002-performance-indexes.sql`の追加と初期化への適用、計測設定・文書の追加もある。**したがって本書の共有window lock、接続別SSE poll、全active投稿取得、画像参照predicate等の構造根拠は残る。ただし索引を「まだ存在しない」とは仮定しない。** 本書の索引列は必要なアクセス方向の例であり、最新の実定義とplanを照合してから追加・統合する。

その後の[`d6f6064`から`74c5abf`への差分](https://github.com/tatsumack/isucon2026/compare/d6f606408f4561b609ae7d6d23d83d19cb3cba40...74c5abfae3faa12be8c2957a5f9ec282df8a1cc3)には、`bench-results/2540/`・`bench-results/940/`等のベンチ関連成果物、文書・計測関連の追加がある。Goの差分は`go/admin_session.go`と`go/session.go`へのログ相関用`X-User-Id`ヘッダ追加で、業務の共有window lock・feed・画像・SSE本体の変更は確認していない。**構造分析とモデル本文はこの差分によって変更していない。追加ベンチ成果物の存在は確認したが、本書では内容・測定条件・原因・提案効果を詳細再検証していない。** フォークのparent/source情報でupstream所有者の綴りは`tatsumack`と確認しているが、これらのupstream向けURLやHEADの直接取得成功を意味しない。

前回確認した`d6f6064`時点の[既存作業文書](https://github.com/tatsumack/isucon2026/blob/d6f606408f4561b609ae7d6d23d83d19cb3cba40/docs/action-plan.md)には、初期ベンチスコア2,677点、投稿期間開始3回のtimeout、最終日の参加者0、users件数asia=18/europe=7/america=6と報告されている。**これは既存文書からの二次報告であり、本書の作成過程で生ログ・測定条件・実ベンチ結果を取得して再検証した値ではない。** この報告はwindow開始から活動進行までを優先して観測する動機になるが、timeoutの原因を通知INSERT・共有投稿lock・WANのどれかに確定する証拠でも、本書の新施策の効果測定でもない。

公式規則と固定commitの挙動を優先し、作業文書中の「画像はDB固定」「SSE候補は現在regionだけ」といった解釈を本書の制約にはしない。保存方式は変更可能であり、現行通知queryの候補は全3regionにまたがる。また、同文書の時刻列に関する説明をblob対応付けの根拠にしない。新規投稿経路で画像の`created_at`へ渡される値はpostの`posted_at`と同じ`postedAt`であり、postの`created_at`はschemaの既定値である。RealMoji・profile画像・初期画像まで時刻一致を一般化できない。object IDと明示的な参照/locatorを用いる。

### 証拠の区分

| 区分 | この文書で確認したこと | 確認していないこと |
|---|---|---|
| 競技条件 | 提供された当日マニュアル・レギュレーションの仕様、配点、禁止事項 | 提供後の公式告知変更。採用直前に確認が必要 |
| コード事実 | 固定commitの対象44ソースファイルについてGit blob hash一致を確認し、処理・参照経路を静的に読解 | 実行時の件数・plan・ロック待ち・通信実効値 |
| 数学・抽象検証 | 画像predicateの4,096+32ケース、cache raceの5順序、graphの10,840辺遷移・6,000 engagement遷移・3,000 top51ケース | PostgreSQL/NULL/実API/署名/並行性/分散障害/永続化/性能 |
| 設計仮説 | どの仕事を消せるか、必要な整合性、費用、反例、段階導入 | 採用による増点、速度倍率、実装完成、競技検証通過 |

基準commitで確認した`docs/benchmarker-analysis.md`は計測方法の文書であり、それ自体をベンチ本体や観測結果の根拠にはしない。新たに追加されたベンチ成果物は本書で詳細再検証していない。負荷増加条件、実際のユーザー遷移、頻度、離脱条件を推測で埋めない。「通知が来なければベンチが必ず無期限に待つ」といった断言もしない。


<a id="premises"></a>

## 2. 競技・サービスの前提と配点

### 2.1 固定構成と許される変更

| サーバ | 固定region | 初期の役割 |
|---|---|---|
| asia1 | asia | nginx + Go app |
| europe1 | europe | nginx + Go app |
| america1 | america | nginx + Go app |
| asia2 | asia | 全appが使う共有PostgreSQL 16 |
| 運営ルーター | 対象外 | 国別転送、地域間遅延、帯域制限 |

ユーザー通信・サーバ間通信は指定ルーター経由。asia1とasia2は同地域扱い。固定地域・公開入口・遅延・帯域制限を変えず、ルーターへログインしたり通信を迂回したりしない。各appのDB接続上限は対象コードで30だが、これを増やすだけで共有行ロックやWAN往復は消えない。

ソフトウェア、DBスキーマ、索引、保存方式、言語、内部cacheの変更は許される。**画像を必ずDBに置くという競技制約はない。** 一方、別地域取得、初期画像、再起動後の永続性を壊してよいわけではない。リポジトリ内の作業向け記述やSQLコメントは、公式の競技規則と同じ扱いにしない。

処理を担う資源は認められた競技サーバだけ。外部CDN・外部計算サーバへ本番処理を移す案は対象外。開発・テスト・監視で許された外部資源の利用と、本番得点を上げる処理委譲は区別する。チーム外への競技内容共有を行わない。

### 2.2 絶対に残す外部仕様

- URI/メソッド/HTTP status/JSON・HTMLの意味、配布静的ファイル、ブラウザ利用を維持する。
- 認証、ユーザーregionと入口regionの整合、公開範囲、両方向block、friendship、削除、画像署名の形式・検証を維持する。
- **書込み成功後に開始する読取りには、別ユーザー・別regionでも結果を反映する。** 書いた本人だけprimaryへ寄せるread-your-writesでは不足する。
- 5秒の猶予は投稿削除後の画像取得不可化だけ。公開範囲、友達関係、block、退会、プロフィール画像変更、RealMoji置換等に一般化しない。安全な初期設計は即時失効とする。
- 画像署名不正、期限切れ、無効画像は403。画像GETにはsession/region middlewareがなく、署名と参照状態で判断する。最適化のために新しいログイン要件を足すことも仕様変更になる。
- ベンチマーカーはredirectを追わない。503は再試行対象だが、エラー率・応答時間計測から除外されるわけではない。意図的な間引きで検証を回避しない。
- 新しいcache、派生表、複製先、ファイル正本をすべてinitialize対象にする。初期履歴・画像が復元され、全サーバ再起動後も書込み済みデータを取得できること。
- initializeの成功レスポンスは空でない`language`を返し、Go実装なら`go`を維持する。`lang`ではない。追加申告項目も公式指示に従う。
- 運営アカウント、SSH公開鍵、登録サービス、追試手順を妨げない。競技終了時は4台とも起動状態を保ち、終了後の無許可操作を行わない。

初期化・整合性検証失敗は走行FAILで0点。権限のない投稿の取得・feed混入・操作受理、初期履歴/画像の不一致、削除猶予超過等はクリティカルエラー。最終採用は「どこかの回で最高だった値」でなく最後の走行結果であり、再起動を含む追試にも耐える必要がある。追試はFAIL又は最終スコアの75%以下を再判定し、所定回数で解消しなければ失格になる。

### 2.3 サービスの時間構造

地域ごとに現役posting windowは最大1件、全地域で最大3件。同地域の次window開始が旧windowを終了させる。`localDate`は日付を表し、時間内判定は`onTimeDeadline`。deadline超過後もwindow終了までは投稿可能で、`lateSeconds`は秒未満切捨て。

他人の投稿を見るには、viewerの**現在regionの現役windowのlocal_dateと一致する日付**に、自分の未削除投稿が必要。自分の投稿windowが今も現役である必要も、今のregionと同じである必要もない。他人側の投稿windowは現役でなければならない。地域移動があるため、単なる`posted[windowID]`や`author=1 active post`への置換は誤り。

```text
window開始
  ├─ 開始処理が通知対象を確定
  └─ 通知受信 → 投稿成功 → 期間終了前の友達feed検証
                         └─ feed / 画像 / 詳細 / 反応 / コメント
```

この連鎖を速めると、未投稿のため閉じていた閲覧活動を開ける可能性がある。これはサービスと配点からの仮説で、実ベンチの遷移・待機時間の実測ではない。

### 2.4 配点と上限

`score = floor(Σ 採点件数 × 活動配点 × 地域倍率)`。地域倍率は加点時点の利用者regionでasia=1、europe=1.5、america=1.5。HTTP成功件数と採点件数は異なる。

| 活動 | 点/件 | 特に重要な制限 |
|---|---:|---|
| 投稿後の友達feed検証 | 通常10 / 最終20 | user×windowで1回、投稿受理後かつそのwindow終了前に成功 |
| feed閲覧 | 1 | 閲覧中、user×windowで20回 |
| 画像取得 | 2 | 閲覧中4回。投稿直後の確認は別枠 |
| 投稿詳細 | 1 | 閲覧中8回。投稿直後の確認は別枠 |
| RealMoji・comment一覧 | 1 | 閲覧中、合計8回 |
| profile閲覧 | 1 | 閲覧中4回 |
| RealMoji送信 | 5 | user×windowで5回 |
| comment投稿 | 3 | user×windowで5回 |
| 通知受信 / Memoriesで前回投稿確認 | 各1 | 成功を確認した活動数 |
| RealMoji・comment削除 | 1 | 同上 |
| ユーザー検索 / 友達申請送信 / 承認 | 各2 | 同上 |
| 友達申請拒否・取り下げ / 友達解除 | 各1 | 同上 |
| 友達・follow・follower一覧 | 1 | 同上 |
| follow / unfollow / block / unblock | 各1 | 同上 |
| 表示名・自己紹介変更 / 投稿caption変更 | 各1 | 同上 |
| profile画像変更・削除 | 3 | 同上 |
| 退会 | 5 | 同上 |
| Memories月単位 / 日付単位 | 3 / 2 | 投稿件数ではなく一連の確認成功数 |
| export | 20 | 画像件数ではなく一連の確認成功数 |
| 過去投稿画像取得 | 2 | 成功を確認した活動数 |

閲覧中の列挙した活動群は`20 + 4×2 + 8 + 8 + 4 = 48点`、反応・commentは`5×5 + 5×3 = 40点`。合計88点に投稿後検証を加えると98/108点の活動余地になる（地域倍率前）。**これは全活動の絶対上限ではない**。通知、投稿直後の別枠確認、Memories等は別である。

したがって、すでに上限到達した少数の利用者のGETだけを速めるより、期間内検証へ到達できない利用者を進める方が増点につながる可能性がある。ただし「exportが20点だから最優先」「欧米倍率が高いからasiaを捨てる」「エラー直接減点なしだから503で間引く」とは判断しない。頻度・費用・後続活動・FAIL条件を合わせて測る。

根拠: [当日マニュアル（提供内容の出典）](https://gist.github.com/whywaita/17f3ab1a8c1b7585c571f4f8afe5f531)、[公式レギュレーション](https://isucon.net/archives/59966826.html)。本節は提供時点の内容の要約であり、公式の追加告知があればそちらを優先する。


<a id="choices"></a>

## 3. 候補比較と選別順

### 3.1 まず比較する5案

最新の索引追加済みcommitも確認済みだが、実サーバへどの変更が適用されたか、実plan・効果は未確認。本表は索引の重複提案ではなく、処理単位・探索集合・転送の構造を比較する。

| ID | 構造変更 | 消せる仕事・待ち | 最小の判断材料 | 主な正しさリスク | 初期の実装負担 |
|---|---|---|---|---|---|
| B1 | window共有lock＋利用者単位排他 | 同region投稿の全面直列待ち | row lock保持/待ち、地域別投稿完了 | 期間切替、同日制約、切替待ち | 小〜中 |
| B2 | SSE共有dispatcher | 接続数×500msのDB poll | idle SSE数、SQL/s、通知tail | 接続境界、全3地域候補、移動/退会 | 中 |
| B3 | 関係作者起点＋厳密top51 | 全active投稿転送、不要hydration、非友達engagement | 候補/返却比、rows、bytes、CPU | filter前LIMIT、rank/cursor、status | 中〜大 |
| B4 | DB近傍の粗粒度RPC | 1業務内の逐次WAN往復 | SQL実行とRTT/pool待ちの分離 | 入口region、認証/IP、二重write | 中 |
| B5 | bytesだけ地域cache | 同じ画像の繰返しWAN転送 | 地域別再利用率、画像bytes、hit率 | 期限/失効省略、古いfill、全地域GET | 中 |

**これは実測前の調査順で、改善倍率順の断定ではない。** 画像が帯域を占有していればB5を先にする。海外RTTが大半ならB4、idle SSEがpoolを埋めていればB2を先にする。B1とB4はlock保持時間を通じて効果が重なるため、組合せだけでなく単独差分を測る。

### 3.2 残る律速に対する3つの深案

| ID | 基本案からさらに変えるもの | 上限が高くなる条件 | 追加費用 | 進まない条件 |
|---|---|---|---|---|
| D1 | origin durable blob＋画像専用同期projection | upload bytes/画像GET RTTが支配、画像再利用が多い | file durability、export resolver、全提供先ACK | 中央小RPCで十分、hit低、write同期tail大 |
| D2 | 全recipient通知rowをevent＋membership履歴へ | window開始の大量INSERT/users lockが支配 | snapshot意味、履歴、stable ID、全mutation対応 | 配送集約だけで解消、Uが小さい |
| D3 | presence＋局所graph cache＋hot viewer順序index | 同じviewerのranked readが多くgraph安定 | write fanout、memory、dirty/build protocol | ほぼ毎readでdirty、hub更新が支配 |

全面SNSメタデータの地域複製は独立した第4の深案として急いで採用せず、D1の小さい対象やB4で足りない根拠が出てから検討する。readを速めるためwrite成功を最遅地域へ依存させる費用がある。

### 3.3 律速の移動を見越した順序

```text
B1/B2で投稿する人数が増える
    → B3の全投稿scanが増える
    → feed/画像が増える
    → B4/B5のRTT・bytes削減が効く可能性
    → なお開始write/画像RTT/ranked計算が残る場合にD2/D1/D3
```

同時に全部入れて得た差は単一施策の効果量ではない。1案ごとに同初期状態で比較し、上流の改善で負荷自体が増えた場合は「下流p95悪化」だけで失敗とせず、完了した活動人数・総スコアとの両方を見る。


<a id="b1"></a>

## 4. B1 — 同一地域の投稿を並行化する

### コード根拠

- [`go/post_api.go:89–107`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L89-L107): transaction開始→本人行 `FOR NO KEY UPDATE`→ユーザー取得→当該regionの現役windowを `FOR UPDATE`。
- `:121–150`: windowロック後に同日投稿数確認、画像bytea INSERT、post INSERT、post取得、commit。ロック後最低5回の逐次DB往復があり、大画像転送を含む。
- [`sql/001-schema.sql:152`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L152): regionごとに現役windowは1行。従って同regionユーザーの投稿は全て同一排他行で待つ。
- [`go/admin_posting_window_api.go:60–80`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L60-L80): 旧windowの終了UPDATE→新window INSERT→対象全利用者への通知作成→commit。

### 作用機序・候補設計

現ロック保持時間を H 秒とすると、そのregionの投稿は概ね `1/H` 件/秒を超えられない（他の制約を無視した上限）。EU/USから共有DBへの往復・画像転送でHが伸びる。接続プール増量だけではこの上限は消えない。

本人行の排他は残し、現役windowのロックを **FOR SHARE** とする候補。投稿同士は共有し、期間終了UPDATEだけは投稿commitまで待つ。これは単なるSQL短縮ではなく、サービスの一斉投稿を並列可能にする変更。

PostgreSQL 16公式で FOR SHARE は同じ共有ロックと共存し、UPDATE / NO KEY UPDATE と競合する。**FOR KEY SHAREでは ended_at の非キー更新を防げないため代用しない。** [公式ロック仕様](https://www.postgresql.org/docs/16/explicit-locking.html)

### 整合性・期間切替の要点

- ユーザー単位 `FOR NO KEY UPDATE` を残す。二重投稿、同日の削除回数上限、同日での地域移動後再投稿を守る。単純なunique(author,window)だけではlocal_date跨regionの制約を代替しない。
- 新windowへの切替が先行し、SELECTが旧window行を待った場合、終了済み行はWHERE条件から外れ得る。現実装 [`post_api.go:108–110`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L108-L110) の再SELECTを削除しない。READ COMMITTEDなら新しいstatementで新windowを見る。隔離レベル変更時は別途設計。
- 投稿が先に共有ロック取得: 終了UPDATEは待機し、投稿commit後に切替。切替が先行: 新windowを再取得してから投稿。終了後の旧windowへ投稿をcommitする穴を作らない。
- ロック順は本人→windowを保つ。新たにwindow→本人の排他経路を追加しない。adminのusers表ロックはSHARE ROW EXCLUSIVE、SELECT FOR NO KEY UPDATEによる表ROW SHAREとは共存する。一方ユーザーUPDATE/INSERTはその表ロックと競合する。
- adminのグローバルusers表ロックを同時に外すなら、通知対象を決める時点の登録/移動/退会との整合性も別に守る必要がある。最初は触らない。
- 共有ロックを短くする目的で画像INSERTをwindow取得前に移す追加案もあるが、rollback時の画像孤児、投稿時刻、期間境界を再検証する。まずロックmodeだけの変更で効果切り分けがよい。

### 最小検証（実施前の設計。まだ未実施）

1. 同region異なる利用者2名で、Aをwindow取得直後に止める。Bが同じ箇所を通過できること（現状は待つ）。
2. A/B投稿中に期間開始を実行し、旧期間終了は投稿commitまで待つこと、終了成功後の新投稿は新windowを使うこと。
3. 切替が先行して旧window待ちとなった投稿が、新windowへ再取得できること。
4. 同一人物の並列投稿/削除再投稿/地域移動、初期化、再起動永続性を確認。
5. 同時投稿数に対する完了件数、window行wait、DB pool wait、投稿p95、期間内友達feed検証到達率を比較。

### リスク

並列化で画像転送帯域・WAL・DB pool・feedが次の律速になる。長い共有lockを大量に保持すれば期間切替が遅れるため、「投稿速いが次期間が開かない」を観測する。効果倍率は現在のロック待ち割合なしには言えない。


**採否/停止条件**: 同region別userが同時通過せず、あるいはwindow切替・同日投稿制約が崩れたら採用しない。正しくてもlock待ちが元から小さいなら優先度を下げる。共有lockが増えて開始APIのtailが悪化し、期間内活動完了が増えない場合は撤回又はtransaction短縮を別実験にする。


<a id="b2"></a>

## 5. B2 — SSEの接続別pollを共有配送へ変える

### コード根拠・機序

[`go/notification_api.go:26–37`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/notification_api.go#L26-L37): DB時刻取得後にSSE header flush、500ms ticker。`:40–56`: 各ループでactive user取得と通知SELECT。`:54`: `created_at > connectedAt AND window.ended_at IS NULL`。`:63–67`: 1件送信して接続終了。

DBが速ければ待機接続Nに対して約 **4N SQL/秒**（2問い合わせ×2回）。遅い地域では直列2 RTTに律速され頻度が下がるが、pool滞留と通知遅延が増す。これは単なるSSEの1点ではなく、全投稿活動に先行する負荷。

### 候補設計

**段階1:** 永続notificationsは維持。プロセス/region単位dispatcherが新window/通知をまとめて読み、登録されたSSE waiterへfanout。DB NOTIFY等はあくまでwake-up、durable通知テーブルを真実として取り逃しを回収。1接続ごとの継続pollをやめる。接続時の検証・通知時の対象検証は残す。

**段階2（高リスク）:** [`admin_posting_window_api.go:70–73`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L70-L73) の全active user分の行生成を、windowイベント＋開始時点recipient集合/履歴に置換。登録・移動・退会履歴から開始時点の資格を再現し、安定した通知IDを生成する必要がある。単に「今購読中の人だけ」に絞る、現在のregionだけから通知を捏造するのは意味が違う。最初の突破口としては段階1優先。

### 整合性・最小検証

- 接続登録とevent公開の競合に注意。登録→durable catch-upの順やcursorで取り逃しを防ぐ。connectedAtは現仕様のDB時刻を基準にし、**通知のcreated_at（実際にはwindow.startsAt）が接続より古ければ送らない**（commitが後でも同様）。
- 終了windowを配信しない。接続中のactive状態と現在regionを確認し、現在regionが接続時のsession.Regionと不一致なら終了する。地域変更イベントを見た瞬間に必ず切る設計とは同義でなく、poll間の移動→復帰を区別する。multi-process dispatcherの再接続、再起動、初期化epochを扱う。
- 実通知送信でSSEを終了する現仕様を維持。bufferingとflush、遅いclientによる共有dispatcher全体のhead-of-line blockingを避ける。
- N待機接続でDB QPSがO(N/0.5s)から外れること、commit→最後の受信のtailを測る。接続前後/切替直前/移動/退会/再接続を競合試験。


### 配送プロセスのregionと、通知候補のregionを混同しない

**dispatcherはregionごとに置けるが、候補通知は全3regionの現役windowから選ぶ。** 現在の通知SELECTには`window.region = current user.region`条件がない。現在activeか・現在regionが接続時regionと一致するかは終了判定であり、通知作成時点のrecipient判定とは別である。

反例: asiaで接続→europeへ移動→europeのwindow開始→asiaへ復帰→次poll。この場合、現行はeuropeで作られた本人宛て通知を返し得る。asiaイベントだけを配送すれば取り逃す。通知は`created_at > connectedAt`、window未終了を満たし、`created_at,id`順の先頭1件を返す。同時刻の場合のID順も残す。

### 配送経路を分離する理由と停止条件

すべてのSSEをasiaへ終端するだけでは海外streamと接続待機が残る。地域配送によりWANを「接続数×poll」から「変更イベント＋まとめた照会」へ寄せる。利用者ごとの終了判定を単純になくしてよいわけではなく、その確認を共有・batch化する対象にする。

通知漏れ・IDの不安定・対象外への通知・終了済window配送が1件でも出れば正本pollへ戻す。SQL/sが下がっても受信tail又は期間内投稿/検証人数が悪化するならqueue/batch待ちを疑う。全員へのINSERTがまだ開始処理を支配する場合にだけD2へ進む。


<a id="b3"></a>

## 6. B3 — feedを関係起点・厳密top51へ変える

**入口**: 未投稿viewerのfeedはselfを除外するため必ず空である。`HasPostedToday=false`を確定したら、不要なgraph展開・全投稿読込を行わない。ただしregion/dateに基づく投稿済判定を単純windowフラグへ変えない。

### 現状と正確な意味


- `feed_api.go:51,72–85`: 毎回viewer graphをloadし、全regionの `w.ended_at IS NULL` の他人の投稿をSQLで取得→GoでcanView/matchesFeed。SQLにLIMITはない。[`post.go:65–80`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L65-L80) は候補全件のauthor情報も取得・画像key設定する。
- [`post.go:109–117`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L109-L117): 投稿済判定は **viewerの現在regionのactive windowのlocal_dateと、自分の非削除投稿が属したwindowのlocal_dateの一致**。自分の投稿windowがactiveである必要も、同regionである必要もない。
- [`post.go:123–157,198–213`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L123-L157): friend集合F、二段展開の共通友達数Mを毎回構築。中間userのstatusはfilterしない。M(viewer)=0として扱う。
- [`feed_api.go:140–155,183–184`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L140-L155): C(a)=sum_{b∈N(a)} M(b)。score(p)=10 E(p)+3 M(author)+min(C(author),30)。Cは単なる共通友達数ではなく、Mを重みとしたもう一段の隣接和（viewer自身のMを0にする補正付き3-hop walk）。
- [`feed_api.go:157–180`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L157-L180): E(p)=activeなviewer友達のうち、その投稿に有効RealMoji又は非削除commentを1つ以上持つ**人の数**。UNIONは(post,user)単位で重複排除する。同じ人の複数commentも、comment+RealMojiも1。
- [`feed_api.go:91–126`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L91-L126): 最初のSQLがposted_at DESC,id DESC、stable sortでscore DESCを追加。したがって完全順序は(score DESC,posted_at DESC,id DESC)。cursorはその厳密な辞書順下側。51件目はHasMore用。
- [`post.go:161–175`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L161-L175) / [`feed_api.go:189–200`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L189-L200): FOF feedは直接友達を除外し、FOF作者の public/friends_of_friends 投稿だけ。friends visibility投稿はFOFに流れない。public feedにはpublic投稿だけ。followingはフォローだけでvisibilityを迂回しない。
- 退会 ([`account_deletion_api.go:28–34`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/account_deletion_api.go#L28-L34)) はstatus変更だけ、friendships/engagementを物理削除しない。**退会者をグラフ中間点から除く最適化は現行と不一致**。投稿作者とengagement actorはactive filterが必要。

### A. active-window × 作者集合を起点にする

#### 構造

`friendships(low,high)` に対し両向きの adjacency `(src,dst)` を提供する。初手はlow/high各索引を使う2枝UNION ALL、必要なら同一transaction更新する2行/edgeのadjacency表。全friendshipsを毎回展開・materializeするCTEは避ける（実planで確認）。

- friends: F(v)→`posts(author_id, posting_window_id, posted_at DESC,id DESC) WHERE deleted_at IS NULL`。
- FOF: F(v)→adjacency→GROUP BY dstでMを作り、self/direct friendを除外した作者→投稿。単なるDISTINCTでは後段score用Mが失われる。
- following: follows(follower_id,followee_id)→作者→投稿。canView条件を適用。
- public: active window IDs (最大3: schema `posting_windows_one_active_region`, [`001-schema.sql:152`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L152)) それぞれに `posts(posting_window_id,posted_at DESC,id DESC) WHERE deleted_at IS NULL AND visibility='public'`。作者active/self除外/両方向block/cursorを**各枝LIMIT前に**評価してtop51を取り、最大153件からglobal top51。各枝の52番目以降には同じ枝に先行する51件がありglobal51位以内には入れない、という保証。
- 作者情報/profile URL等のhydrationはID選抜後の最大51件に遅延。ただし作者active判定は選抜前に必須。postのvisibility/window/cursorは同一snapshotで確定。

#### 計算量・成立条件

P=全active投稿数、d=viewer次数、W2=Σ_{f∈F}deg(f)、A=候補作者数、C=適格候補投稿数、L=51。
現状は少なくともO(P+W2)、rankedはさらに候補近傍edgeとengagementを読みO(C log C) sort。作者起点は概ねO(W2 + A·index lookup + C)でPから局所近傍へ縮小（friendsはW2不要）。publicは可視な51件までのscan量に依存、block/退会密度が低ければ少数で止まるが最悪はP。

current windowは全regionを対象にする。viewer.regionのwindowだけに絞るのは誤り。1作者1投稿とは決めつけない: [`post_api.go:121–130`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L121-L130) はlocal_dateごと制約。region間でactive local_dateが異なる時にregion移動した作者は複数active投稿を持ち得る。作者IDは候補生成で重複排除し、投稿は複数保持。

#### 最小反証テスト

- 最新51投稿がすべてblocked又はvisibility不適格、52番目が適格: 空にしてはいけない。
- 50/51/52適格投稿、同時刻・異なるid、次pageの境界: HasMoreと重複/欠落確認。
- FOF authorのfriends visibility、following先の非公開: 無条件で流してはいけない。
- 別regionのactive投稿が見える。region移動し同日投稿済判定が維持される例/別日で失われる例。
- 2本のactive windowに同じ作者の異なるlocal_date投稿: 両方保持。

実装コスト: 中。非rankedから段階導入可能。SQL planがhash join全scanを選べば期待量にはならないため、索引・LATERAL/枝別topK・EXPLAINで確認が必要。

### B. 推薦のクエリ方向を反転し、疎なengagement集合にする

候補post→全engagement→Goでfriend filter ではなく、**F(v)中active user→有効engagement→候補post/active window** にする。

索引候補: `realmojis(reactor_id,post_id) WHERE replaced_at IS NULL AND deleted_at IS NULL`、`comments(author_id,post_id) WHERE deleted_at IS NULL`。両枝は必ず `UNION(post_id,user_id)` 又は `(post_id,user_id)` でGROUP BY、その後post別count。UNION ALLしてCOUNT(*)は不可。候補集合が小さいならpost起点＋friend semi-joinの方が良いので、固定的な逆転ではなく件数で選ぶ余地あり。

さらに `engagement_presence(post_id,user_id,comment_count,realmoji_present)` をtransaction内更新すれば、comment数に依存せず集合サイズで処理できる。actor状態はread時joinする方が退会fanoutを避けやすい。active window専用 `(window_id,user_id,post_id)` 構造なら友達の過去全履歴scanも避ける。window IDが無いactor索引だけでは、多年のcomment履歴を毎回読む可能性が残る。

計算量: E_all=候補全投稿の全actor engagement量から、E_friend=viewer友達のactive候補への有効engagement量へ。presenceを持てば重複comment件数からdistinct pair数へ。人気投稿に非友達が大量反応する分布ほど成立しやすい。友達が極端に多い/多産なら逆起点が不利になり得る。

反証テスト: 同じ友達のcomment2個+RealMoji→E=1; comment1個deleteしてもE=1; 最後のcommentdeleteでもRealMojiがあれば1; RealMoji置換はscore不変; 全engagement消失で0; 友達の退会で0（グラフM/Cは変わらない）。同時comment作成/deleteでpresence countがlost updateしないこと。

実装コスト: クエリ反転は中、presence増分表は中〜大（全write経路・初期化・並行性が対象）。

### C. graph baselineを再利用、top51を疎な差分で厳密に選ぶ

B_v(a)=3M_v(a)+min(C_v(a),30)。Bはwindowや投稿内容、engagementでは変わらずfriendship graphに依存する。viewerごと必要時にM/Bを計算しgraph version付きで保存する。毎requestの全graph展開を「graphが変わった時だけ」にする。ただしglobal graph epoch方式は簡単でも無関係edge1個で全viewer cacheが無効化され、writeが多いと効果が消える。

#### 正確な2分割topK

1. 候補postでE>0の集合S+をB案で完全に列挙し、実scoreを計算、cursorとvisibilityを適用してtop51。
2. E=0の候補はscore=B(author)。**S+を除いた**baseline順の適格候補から、cursor適用後top51を得る。
3. 両枝最大102件を(score,time,id)順mergeしてtop51。分割枝それぞれのtop51でglobal top51が得られる。

baseline順を活かすにはviewer-author B索引＋authorごとのactive投稿stream/heap、又はviewer別ranked-post index等が必要。Bをcacheしただけでは候補C件scanが残る。S+が少なくgraphが安定しbaselineを再利用できる場合、requestはS+の処理＋baselineから必要な51件探索に寄せられる。全viewer×全postの物化はO(VP)やwrite fanoutになりやすく、必要viewerのみon-demandが現実的候補。

軽い中間案: C(author)の計算を `0<=min(C,30)<=30` の上界で遅延。既知のE/Mで `10E+3M+30` がheap51番目scoreより厳密に小さい作者はC詳細を省略できる（同点はtime/id判定必要）。C計算中は和が30以上なら停止できる。**ただし全候補のE/M又は安全な上界は必要で、最新51のみの候補打切りには使えない。**

反証テスト: 最古の投稿に大量友達反応を付けて1位にする; 同score/same timestampをidで跨いでpage; 2page取得前にengagementを付けscoreをcursorより上に移す（現行同様、毎requestの現scoreで判定。旧ランキングsnapshotを固定して返さない）; E>0だった投稿の最後のengagement削除でbaseline枝へ移す。

実装コスト: B cache+heapは中、baseline順の索引と疎な差分mergeは大。実測なしに採用断定しない。

### D. 増分更新・失効境界（省略すると正確性が壊れる）

| 更新 | 維持すべき依存関係 |
|---|---|
| friendship追加/削除 | F、M、C、FOF候補、Eの友達判定。Mは2-hop、Cは3-hop依存のため当事者2人だけのcache失効では足りない。edgeの旧/新graphで影響viewerを列挙するか、まずglobal epochで保守的失効。[`relationship.go:56–58`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/relationship.go#L56-L58), [`relationship_api.go:27`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/relationship_api.go#L27) |
| block | block自体の双方向可視性失効に加えてfriendshipを削除、両方向follow削除 ([`relationship_api.go:138–145`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/relationship_api.go#L138-L145))。上記graph/engagement依存も更新。unblockはfriendshipを復元しない |
| 退会 | author投稿除外、actor engagement除外。**グラフedge/M/Cは削除しない**。statusをread時joinすればgraph再構築不要。作者表示cacheは失効 |
| region変更 | viewer.region→current local_date→HasPostedToday再判定。既存post.windowや作者所属windowを移し替えない ([`user_api.go:220–222`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L220-L222)) |
| window開始 | regionの旧windowをclose、新windowをopen ([`admin_posting_window_api.go:60–67`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L60-L67))。active集合とviewer gateを更新。graph baselineは不変。window世代keyで旧候補を除外すれば巨大delete/fanout不要 |
| 投稿作成/削除 | candidate streamとHasPostedToday更新。本人の非削除postが無くなれば全feedが閉じる。削除後再投稿可; author単位1件固定cacheは不可 |
| comment/RealMoji | presenceの0↔1遷移だけがEを変える。RealMoji置換は同一transaction内で旧終了＋新追加 ([`realmoji_api.go:90–99`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L90-L99))。post/user別comment reference countが必要 |
| profile更新・認定 | 最終hydrationに任せれば全feed cache fanout不要。完成JSONをcacheするならdisplay name/bio/image/account_typeも失効対象 |
| initialize | 補助表/cache/graph epoch/window世代すべてreset又は再構築。古いprocess cacheを残さない |

厳密な増分Cを選ぶ場合、raw Cを保持して出力時min30、又は削除時再計算する。clamped=30だけを保持しedge削除で1減らすのは誤り（真のC=100→99でも出力30）。複数app process間のローカルcacheだけで同期を仮定しない。DB transaction内派生表更新、又はcommitに対応する共有version+再検証が必要。非同期更新だけでは、write成功後の直後readで旧順位/権限を返す。

### 評価手順（提案、未実施）

比較する量は単なるSQL回数でなく、feed種別ごとのP/d/W2/A/C、engagement total vs friend-only distinct pair、graph change/read比、rows visited/returned、hydration件数、p95/p99時間、補助更新write量とlock wait。基準実装とランダム小graph・状態変更列で返却ID/score/HasMoreを差分比較し、特に上の最小反証を通してからベンチ測定。大きいhub、高block密度、無投稿viewer、別region/local_date、退会中間点、同時刻paginationを必ず含める。


<a id="b4"></a>

## 7. B4 — SQLごとのWAN往復をDB近傍の業務RPCへまとめる

### 根拠

認証ですでにSQL往復 ([`session.go:50–55`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/session.go#L50-L55))。投稿はBegin→ユーザーロック→ユーザー読取→windowロック→件数→画像INSERT→投稿INSERT→読取→Commit ([`post_api.go:89–150`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L89-L150))。特に **同じregionのactive window一行をFOR UPDATEし、画像bytea転送を含む残り処理まで保持** (:107–150)。同時投稿が地域内で直列化され、長いWAN RTTと画像転送が他ユーザーの待機に増幅される可能性がある。全地域で一つの行ではなく、regionごとの行である。

フィードは全current投稿をDBから受けてGoで閲覧判定後50件に絞る ([`feed_api.go:72–114`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L72-L114))。SQL改善を別担当が行っても、業務処理の位置によってWAN転送の無駄が残るかは比較価値がある。

### 案

- 公開入口は各固定地域app。内部の信頼済みRPCへ、入口地域、認証、入力、元のクライアントIPを渡す。
- asia1などDB近傍のワーカーで一連のSQLを行い、最終レスポンスに必要なデータだけ戻す。DBホストasia2に載せる案はDBとのCPU競合を測ってから。全appのConfig.Regionをasiaに変える案ではない。
- 一気に全機能移植するより、投稿の業務トランザクションやフィードなどの粗粒度RPCから比較。代案はDB内の関数に業務処理をまとめること。ただし仕様・テストの実装重複とDB CPU増が代償。
- 投稿画像の受信・JPEG検証を終えてから短いDBトランザクションを開始する現在の境界 (:70–89) は保つ。RPC受信中にwindowロックを持たない。

### 成立条件 / 反例

- [`region.go:16–17`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/region.go#L16-L17) は入口server regionとuser regionの一致を要求。asiaへ普通にreverse proxyするだけでは海外ユーザーを拒否する。**入口地域をサーバ側で確定し、内部専用のcontextとして保持**。外部が任意の内部地域headerを注入できないようにする。
- [`user_api.go:220–235`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L220-L235) ではuserのregion変更が可能。regionは長期固定ユーザー属性ではない。認証/region判定を古いlocal cacheで済ませない。
- [`ip_ban.go:61–65`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/ip_ban.go#L61-L65) のX-Client-IP意味、認証エラー順序、multipart、status、SSE flush、キャンセルを保つ。認証middlewareだけ海外に残すと少なくとも一往復が残る。
- CPUボトルネックをasia1へ集中させると悪化。巨大画像はRPCでも一度WANを渡り、帯域制約そのものは消えない。DB SQL処理自体が支配的なら効果は限定的。
- 失敗時の自動RPC再試行は投稿・コメント等の二重書込になり得る。commit済/応答喪失を区別するか、再試行しない。既存APIにない冪等性を勝手に仮定しない。

### 最小測定

地域別に主要経路の (1) SQL直列往復数と所要時間 (2) DB実行時間 (3) pool待ち (4) window rowのロック取得〜commit時間 (5) WAN送受信bytes を分ける。単発と同地域同時投稿を比較し、地域RPCの同等処理とp50/p95・成功活動数を比較。概算は `現状 ≈ n×RTT + SQL + transfer + queue`、RPC後 `≈ RTT + SQL + 最終データtransfer + 新queue`。速度倍率は未確定。


### 境界の追加条件

内部RPCを既存public handlerへそのまま通すとIPBan・action logが二重評価/二重記録され得る。入口処理と業務contextを分け、禁止IP・元の`X-Client-IP`・認証エラー順序・応答分類・ログの意味を維持する。信頼済み入口regionを一般クライアントが注入できないようにする。内部endpointも指定ルーター経由であり、外部から自由に呼べる経路を作らない。

**採否/停止条件**: 同じ業務で削れた逐次RTTと、増えたRPC待ち・asia CPU待ちを比較する。SQL CPU/帯域が主因ならRPCだけでは効かない。海外だけ改善してasiaを含む全体の期間内活動数が下がる場合、中央queueの優先制御や対象APIの限定を検討し、全面移行を止める。


<a id="b5"></a>

## 8. B5 — 不変の画像bytesだけを地域cacheに置く

### 根拠

画像本体はDBのbytea ([`sql/001-schema.sql:22–30`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L22-L30))、投稿時INSERT ([`post_api.go:134`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L134))。画像GETはownerの有効性と署名鍵を読む後、本体と参照の有効性を読む ([`image_api.go:45–86`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_api.go#L45-L86))。画像URLは相対URLで地域入口へ行く ([`image_signature.go:35–41`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_signature.go#L35-L41))、画像endpointはsession/region middlewareなし ([`server.go:74`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/server.go#L74))。したがってどの地域入口でも任意の有効画像を取得できる必要がある。

### 案

- 正本は当面DBのまま、imageID→不変bytes/media typeを各地域のローカルdisk/上限付きmemoryにread-through保存。キャッシュmissは既存正本へfallback。キャッシュhitも署名、期限、owner active、現在の参照状態は別の最新メタデータで検証する。
- メタデータ検証をAの小さいRPCにまとめれば、hitでWANに画像bytesを流さずに済む。さらにメタデータを地域へ同期配信する案はC相当の整合性設計が必要。
- 既存履歴画像はread-throughだけで十分なら大量prewarmしない。投稿受信地域のbytesをcommit後cacheへ昇格できるが、commit前の画像を配信しない。
- 「全3地域へ画像を常時同期複製」は、実際には一地域でしか見ない画像もWAN転送する。需要に応じたcacheか、全地域の再利用が多いと分かった画像だけ先行複製する。

### 成立条件 / 反例

- キャッシュは**バイト列のみ**。署名期限切れや不正sigをcached 200で返すのは不可。URL全体のproxy cacheも不適切。`image_api.go:25,30–40,60–63,93–96` の検証を維持。
- 投稿削除の5秒猶予を、退会・プロフィール画像変更削除・RealMoji置換削除などの全invalid状態へ拡大しない。安全側は配信可否を都度正本参照、後から対象限定の失効設計を行う。
- 投稿削除stateをTTLで持つ場合も、削除前に始まった古い読み取りが削除後cache fillするraceに注意。単にfill時刻+5秒では削除成功+5秒を越え得る。tombstone/versionとinflight fillの排除、または検証開始時刻基準の保守的期限が必要。
- 本体をDBから除去しローカルdiskを正本化する大変更は、global locatorとmiss proxy、成功前の永続化、初期画像移行、全地域アクセス、再起動を新たに保証する必要。最初はcacheに留める方が安全。

### 最小測定

imageID単位の地域別再取得率、size分布、hit率、DB→海外bytes、local disk/memory容量、miss時p95。画像bytesの内容一致、別地域GET、期限/署名・削除・退会・置換後GETの境界テスト。hit率が低くDB/WAN bytesも少なければ優先度を下げる。


### メタデータとログまで含めて同値にする

画像の完全な配信条件はD1で定義する。owner activeだけでも、post deletedだけでも足りず、Objectの存在、profile参照、post参照、RealMoji参照、RealMojiの対象post作者のactive状態を保つ。署名不正時は現実装が`abuse=invalid_signature`を設定し、GETでも403をinfoとしてaction logへ記録する。画像RPC/地域判定へ移してもstatusだけを返して分類を失わず、元IPとログの意味を保つ。ログを読む外部処理の有無は未確認なので、不要とは断定しない。

**採否/停止条件**: hit率、WAN bytes、DB bytes、全地域p95の改善が小さいならmetadata同期へ拡大しない。署名・失効・初期化・再起動の境界が未解決ならbytes cacheを正本化しない。blob uploadやGETの1RTTがなお支配的ならD1の段階案へ進む。


<a id="d1"></a>

## 9. D1 — 画像を「大きい不変データ」と「小さい即時整合状態」に分割する

### 独立させられる画像経路


**全SNSの強整合レプリカを作らなくても、画像配信だけWANゼロのhot pathにできる可能性がある。**

[`image_api.go:45–86`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_api.go#L45-L86) の配信可否は、閲覧者ID・友達・公開範囲ではなく、画像ownerのactiveと「profile/current post/current RealMojiのどれかから生きた参照があるか」で決まる。署名付きURLがcapabilityに相当する。フィード/投稿詳細でURLを渡す権限判定と、画像GETの配信可否は異なる。この違いを保てば、画像専用の小さいread modelだけを同期化し、複雑なfriend-of-friendやランキングは中央に残せる。

また、配信可否を画像ごとのboolへ全展開する必要はない。**owner.active、parentPost.live、parentAuthor.activeを参照する形に因子分解**すれば、退会や投稿削除で画像の数だけ失効イベントを出さずに済む。退会一件をowner flag一件として、各地域で画像GET時に評価できる。

### 1. 画像read modelの厳密な形

保持する候補:

- `Object[imageID] = {exists, ownerID, mediaType, digest, length, originServer, generation}`
- `Owner[userID] = {active, imageSigningKey, profileImageID}`
- `Post[postID] = {authorID, imageID, live}`
- `Reaction[reactionID] = {reactorID, parentPostID, imageID, current, live}`
- 逆参照 `imageID → post IDs / reaction IDs`。通常生成経路は一画像一用途だが、初期データも同じとは未検証なので「一用途」を無条件に仮定しない。現行SQLのOR/EXISTSの意味を保つ。

配信条件（期限・sigの字句検証は省略せず従来どおり）:

```
Object.exists && Owner[Object.ownerID].active && validSignature
&& (
   Owner.profileImageID == imageID
   || exists p: p.imageID == imageID && p.authorID == Object.ownerID && p.live
   || exists r: r.imageID == imageID && r.reactorID == Object.ownerID
                && r.current && r.live
                && Post[r.parentPostID].live
                && Owner[Post[r.parentPostID].authorID].active
)
```

出典: [`image_api.go:30–40,45–86,93–96`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_api.go#L30-L40)。**post branchとreaction branchの条件差も保持する**。期限は送信直前にも確認する現状を維持。`Cache-Control: no-store`を変える必要はなく、内部bytes cacheと両立する。

#### 全mutation経路への対応表

| 現API/処理 | 正本変更 | 画像projectionに必要な変更 | 同期が必要な理由 |
|---|---|---|---|
| POST posts ([`post_api.go:133–150`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L133-L150)) | object＋post挿入 | Object、Post、必要Owner | 成功直後の別地域GETで新画像を取れる |
| DELETE post ([`post_api.go:367–370`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L367-L370)) | post.deleted_at | Post.live=false | 投稿画像とそのpostのRealMoji画像を無効化。最初は5秒猶予に依存せず同期 |
| PUT profile-image ([`profile_image_api.go:47–59`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/profile_image_api.go#L47-L59)) | new object、owner.profileImageID、old object削除 | new Object＋Owner.profileImageID＋old Object.exists=falseをatomic apply | 新旧の逆転や旧画像復活を防ぐ |
| DELETE profile-image ([`profile_image_api.go:86–92`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/profile_image_api.go#L86-L92)) | pointer=NULL、object削除 | 同じ2状態をatomic apply | 投稿削除の5秒猶予は使わない |
| POST realmoji ([`realmoji_api.go:90–109`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L90-L109)) | old replaced_at、新object/new reaction | 旧Reaction.current=false＋new Object/Reactionをatomic apply | replacement成功後の旧URLを止め、新URLを許す |
| DELETE realmoji ([`realmoji_api.go:185–198`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L185-L198)) | reaction.deleted_at | Reaction.live=false | reactor以外にpost authorも削除できる |
| account-deletion ([`account_deletion_api.go:28–34`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/account_deletion_api.go#L28-L34)) | owner.status、sessions無効 | Owner.active=false | 所有画像すべて、および当人のpostに付いた他人のRealMoji画像を一つのflagから遮断 |
| caption/visibility PATCH、friend/block変更 | post/graph変更 | 画像projectionに不要（現GET条件にない） | feed/詳細のURL発行権限は中央で即時反映を維持 |
| region/profile表示名/bio変更 ([`user_api.go:210–232`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L210-L232)) | user列変更 | bytes locatorを変える必要なし。region認証等は中央の最新状態 | originとuserの現在regionを混同しない |
| initialize ([`initialize.go:18–30`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/initialize.go#L18-L30), [`sql/reset.sql:5–32`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/reset.sql#L5-L32)) | 初期状態へ復元 | 全projection/cache世代変更＋再構築 | 退会flag等を戻すため単調tombstoneだけでは不十分 |

秘密鍵hashは内部だけへ配る。公開APIへkeyを含めない。初期データobjectの重複ID/複数参照・サイズ分布は未測定で、projection構築時に検査する。

### 2. 競合する3つの実行プラン

#### プランA: DBを正本のまま、中央判定RPC＋地域bytes cache

```
client → 固定地域edge → AuthorizeImage(imageID, exp, sig) at asia
                     ← permit + {digest,mediaType,length,locator}
                     → local bytes if hit / trusted object fetch if miss
```

- 中央で署名とlive判定を一つの業務RPCにまとめる。signatureをedgeで検証するならkeyの安全な配布が必要なので初期prototypeでは中央検証が簡単。
- 正本はDB。metadata RPCは絶対に`i.data`をSELECTしない。cache miss専用GetBytesはbytesのみ、一般クライアントから不可。permitは当該inflight requestにだけ使用し、将来GETへ再利用しない。
- full APIをasiaへ転送する案との違い: **JSON/権限判断だけ中央、画像の大部分はedgeから返す**。1 WAN RTTは残るが、何度も同じ大画像を転送しない。
- DB近傍API用RPC境界は `Execute{ingressRegion, clientIP, auth, method/path/query, validated body}`。認証とRegionMiddleware相当も中央に含め、外部からingressRegionを偽装不能にする。画像RPCはsession不要という現仕様を保つ。SSEは別の地域イベント配送に分ける。
- commit後の業務応答喪失による自動write再試行を行わない。画像readは安全に再試行可能。

**強み**: 最小移行、metadata整合性の新しい分散protocolなし。弱み: 全画像GETにWAN RTT、DB bytes upload/WAL/storageは残る。中央permit後のbytes待機中に削除が起きるinflight readは現実装にも同類の競合がある。少なくともwrite成功後に開始したreadが古いpermitを再利用しないことを保証し、長時間待ったpermitは送信前再検証する保守的運用が可能。

#### プランB: origin地域に永続immutable object、中央判定RPC

Aのbytes正本をDBから分離。既存の3画像upload APIは最初のJPEG検証後にedgeでimmutable fileを完成させる。

```
1. upload受信/JPEG検証
2. temp file→必要なdurability処理→atomic publication（request-private staging）
3. 中央のmetadata transaction: Object(locator,digest,length,...)とpost/profile/reaction更新
4. commit成功→公開response。失敗ならstaged orphanは後でGC
```

- 新規bytesをWANでasiaへ送る必要がない。固定originServerは投稿受付serverを指し、user region移動でも変えない。別地域は中央locatorからoriginへserver-side fetchし、地域cacheへ保存。redirectを外へ返さない。全通信は指定router経由。
- DB整合性はmetadataと既存業務制約を一つのtransactionに残す。画像をDB transactionで転送しなくなるので、投稿windowやuser/relationship lock保持時間からblob転送を除去できる。
- 同一DB transactionへfile writeを無理にatomic commitしようとせず、**bytesを先にdurable化、metadataを後にpublish**。安全に残るのはorphanであり、成功済みmetadataからmissing fileを参照させない。staging GCはcommit不明の操作を先に消さず正本照合する。
- 初期画像はDB locatorのままでよい。Object backendを`db`/`file`二種類にして、既存履歴の全面コピーを移行前提にしない。
- DB schemaのdata NOT NULL ([`sql/001-schema.sql:25`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L25)) を保ったまま空bytes代入で済ませない。別catalog導入かschema変更が必要。**exportが直接i.dataを読む ([`export_api.go:30–37`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/export_api.go#L30-L37))**ため、GET imagesだけ置換するとexportが壊れる。exportもmanifest取得とobject resolverへ分割し、地域側にZIP生成を移す候補。`zip.Store` (:47) なので既存画像bytes圧縮による節約はない。
- **exportのmanifest取得とbytes取得を分離する競合**も処理する。現行は単一SQLでmetadataとbytesを一緒に読むが、分離後はmanifest取得→post削除→GC→bytes取得の順で実行中exportが欠落し得る。manifest生成とGCに対するblob pin登録を一体に扱い、読取り完了までretention rootへ加える、又は同等の読取り一貫性を保つ再取得を行う。GC graceだけを使うなら、進行中readの長さと再検証を含めた十分な条件が必要で、根拠のない固定秒数では保証にならない。bytesの保持は配信許可の延長ではないので、通常の画像GETの署名・失効検証は別に維持する。
- データ永続性はorigin diskに依存。初期規則の「全server再起動後に取得」を満たすdurable originは必要。server/disk永久損失へのHAを保証する設計ではない。追加複製するならwrite前replica完成がWAN節約を減らすことを費用に入れる。

**強み**: upload時WANとDBのblob/WAL負担を除去、readは需要のある地域だけ転送。弱み: 初回他地域readは中央判定＋origin fetchで2段階、origin障害/GC/復旧の管理、export改修。

#### プランC: B＋画像専用の同期projection

各edgeが本節の「画像read modelの厳密な形」のprojectionとbytesを持つ。署名・live判定をlocalで実行でき、hot hitはWANゼロ。全SNS metadataや全APIのlocalizationは不要。

**単純かつ証明しやすいprotocol候補**:

1. DBの同一transactionに業務状態更新と永続change batchを記録。
2. DB commit完了後、committed change batchを全3 serving edgeへ配信。
3. edgeはbatch内の複数表変更をatomicにapplyし、適用済みversionをACK。
4. writerは全serving edgeの適用ACK後にのみHTTP成功を返す。
5. 新規画像のbytes未配置地域はlocatorからfetchする。metadata同期はbytes全複製ではない。

apply順序は**DBの実際のcommit順または同値な競合キーごとのversion順**を保証すること。transaction開始時の連番だけではcommit順にならない。順序不明/欠落のedgeはstale stateを使わず中央判定へfallbackする。commit→publishのcrash gapはdurable change batchをreplay。ACK前の成功を禁止。

再起動edgeはlocal projectionを無条件にserveしない。新processのserving generationを発行し、snapshot＋変更追随完了まで中央判定モード。初期化世代も別にして旧batchの遅延配送を拒否する。writer/dispatcher再起動で未完了batchを飛ばさない。

**故障でACKを返せないedgeがある場合の選択**:

- 最初のprototypeでは安全側にwrite成功を待つ/失敗させる。無応答だからmemberから除く、はそのedgeが古い状態をserveし続けるため不可。
- 可用性が必要ならedgeのlocal判定に期限付きserving leaseを与える。未応答edgeのlease失効を保証してからwriteを成功させる。期限後edgeは中央判定/fail closed。これは時刻誤差・停止再開・lease再付与競合の証明が必要な別拡張であり、初手に混ぜない。
- より小さい同期対象として「当該画像/owner/post stateをcacheしているedgeだけ」ACKを待つdirectory方式もあるが、新subscriber登録とwrite/fillのraceが追加される。3地域しかないので最初は全地域ACKの方が単純。

**強み**: 画像hot pathでRTTもDB readも除去。退会/投稿削除はflag少数の同期で済む。弱み: 画像状態を変えるwriteに最遅edgeの伝播時間、DB commit後HTTP応答前の不確定区間、順序/atomic apply/復旧の複雑性。キャプション/友情/コメントなどprojectionに関係ないwriteまで同期barrierを課さない。

### 残るlocal CPU費用

画像をWANゼロにしても、署名処理は消えない。対象の[`image_signature.go:14–32`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_signature.go#L14-L32)は`imageSignatureIterations=1000`としてHMACを反復しているため、署名生成・検証のCPUを別に計測する。D1の`c_L`をゼロとみなさない。署名方式・検証結果・期限の意味を変える案ではなく、expiryの丸めを公式に許されたものとも扱わない。計算再利用を別途行う場合も、期限・失効の確認は省略しない。

### 3. 定量cost model（未知量は記号、倍率の断言なし）

`r`=request地域、`a`=asia authority、`o`=object origin。`L_xy`=RTT、`B_xy`=実効bytes/s、`S`=画像bytes、`m`=判定RPC bytes、`δ`=change batch bytes。`U_o`=地域oでの新規upload数/s、`G_r`=地域rの画像GET数/s、`h_r`=bytes hit率、`I`=画像projectionを変えるwrite数/s、`c_A`=中央判定CPU/SQL時間、`c_L`=local判定時間。外部client↔edgeの共通費用は省略。

| | A: DB bytes＋中央判定 | B: origin bytes＋中央判定 | C: origin bytes＋同期projection |
|---|---|---|---|
| hot image GET latency | `L_ra+c_A+localIO` | Aと同じ | `c_L+localIO` |
| miss latency | hot＋`L_ra+S/B_ra`（別fetchの場合） | hot＋`L_ro+S/B_ro` | local判定＋`L_ro+S/B_ro` |
| 海外upload WAN bytes/s | `Σ(o≠a) U_o S` | metadataのみ | metadata＋同期batchのみ |
| image read WAN bytes/s | `Σ G_r[m+(1-h_r)S]`（海外だけ） | `Σ G_r m + Σ remote misses S` | `Σ remote misses S + change replication` |
| 画像write追加遅延 | DB transaction | file durable化＋metadata tx | B＋`max_j(propagation_j+apply_j+ACK_j)` |
| 中央image認可QPS | `ΣG_r` | `ΣG_r` | 正常warm時ほぼ0、fallbackのみ |
| metadata複製量 | なし | なし | 約`I × 2δ`（asiaを既存正本側とみなし海外2地域） |

mは失敗系も含めた平均、δはowner/postの因子分解で小さくできる。miss時Aは判定と本体同時返却など1 RPC化余地があるため表は分離prototypeの保守値。B/Cでorigin==rならmissにもWANは要らない。地域リンクの共有queueにより単純な独立RTT加算より悪化し得る。

#### 選別式

- Bのupload局所化がWAN総量で得になる条件（比較対象AのDBキャッシュが同じhit率の場合の概念式）:
  `省ける海外upload S + 省けるasia→origin地域の初回read S > 新たに発生するorigin→他地域fetch bytes + 追加複製bytes`。
  originが海外でreadも同地域に偏るほど有利。全地域が一度ずつ見る場合は総bytesの改善が小さく、分散I/O効果が主になることもある。
- CはreadからwriteへWAN往復を移す。集計待ち時間の粗い判断は `ΣG_r L_ra > I × ΔwriteLatency`。ただし投稿期間内の行動連鎖・write優先度が異なるため、これはスコア等価式ではない。
- Cのmetadata bandwidthの有利条件は `I × 2δ < ΣG_r m`。RTT削減が目的ならこの不等式が成立しなくても有益になり得る。
- bytes eager全地域複製は、新規1画像あたり少なくとも海外2本分Sを先払い。需要地域数/再利用が少ないならread-throughより不利。原点が違うので各リンク別に比較する。
- 中央API化はDB逐次往復数nに対して概ね `(n-1)L_ra`を除去するが、Cとの組合せでは画像だけedgeに残す。全requestsをasiaへ寄せて地域CPU/帯域を捨てない。

### 4. 最小prototypeと段階移行

#### P0: read modelの意味だけ検証（network不要のローカルテスト設計）

実装担当向け: 現在のimage SQL結果をoracleにし、本節の「画像read modelの厳密な形」のprojection evaluatorと同じinputで比較する。profile replace/delete、post delete、reaction replace/delete、reactor退会、post author退会、初期化復活、複数参照を含む状態遷移列。署名字句/expiryは既存test相当を保つ。**まず許可/拒否の同値を証明し、同期protocolは後**。

#### P1: Aの一経路だけ

`GET /api/images/:imageID`に内部AuthorizeImage＋bytes cacheを試作。metadata bytes、image size/hit率、中央CPU、地域p95を測る。既存SQL/DB blobをfallbackに残し、全他APIは変更しない。成功してもまだDB正本なのでrollback容易。

#### P2: Bの新規post画像だけ

old imagesはDB backend、新postのみfile backend。metadata transactionへのblobLocator導入、export resolver対応、初期化世代とorphan管理を含める。profile/RealMojiはDBのまま開始して、同じobject interfaceで後から移行。writeの画像転送がwindow lockから消えることを確認。別地域read・region移動・再起動後readを必須とする。

#### P3: Cのshadow projection

全image mutationをoutboxへ載せ、3地域shadow evaluatorと中央の結果を比較（応答はA/Bの中央判定を使う）。差分ゼロだけで十分ではなく、故意の配送遅延/重複/順序逆転・再起動・reset競合を検査。

#### P4: Cのauthoritative化

全ACK barrier付きwriteと、世代チェック済みlocal判定を同時に有効化する。「projectionだけ先に読み取り利用」は不可。切戻しは中央判定モードにしてからbarrierを外す。新規bytesはまだ正本として必要なので旧実装への無条件rollbackではなくresolver互換を保つ。

### 5. 破綻しやすい具体ケース

1. 投稿削除時に投稿画像だけ失効し、そのpostへの他人のRealMoji画像を残す。parentPost.live参照なら防げる。
2. post author退会時にown imagesだけ消す。他人が反応した画像も現SQLでは不可になるためparentAuthor.activeが必要。
3. old profile画像は参照変更だけでなくObject自体が削除される。別参照が偶然あってもObject.exists=falseを尊重。
4. UUID→originをuser.regionから計算し、region移動で以前の画像を404にする。originはobject固有の不変locator。
5. bytes cacheから直接200を返して署名期限とlive検証を飛ばす。BにもCにも当てはまる。
6. full bool画像projectionを作り退会時に過去全画像を同期走査。因子分解モデルならO(1) owner flag、read側O(参照数)。反面regionごとのowner/post mapは必要。
7. DB commit後にchange batchを書き出してcrashで失う。batchは元transaction内へ。単なる非永続pubsubでは代替不可。
8. auto incrementの最大event IDを適用watermarkとみなし、先行未commit transactionを飛ばす。commit順の保証またはgap/競合version処理が必要。
9. bytesのGCを「現在live参照なし」だけで行い、初期化で復活する初期画像を消す。初期snapshot参照もretention rootに含める。
10. 全地域metadata同期はできてもexportだけi.data直読のままで、file-backed imageが欠落する。exportは必須の移行面。

### 判断

基本改善後も海外RTT/画像bytesが支配的なら、**Aで観測→Bでupload/DB blob分離→Cで画像hot pathのWANゼロ化**が段階的に試せる。Cだけを急いで全面SNS複製へ膨らませない。read-write比が低い/画像hit率が低い/最遅地域applyが遅いならA/Bで止める方がよい。現在は実測なしで、採用推奨ではなくprototypeで反証可能な高上限設計である。

### 実施した小さい抽象検証

ローカルPythonで、object存在・owner active・profile一致・post画像一致/author一致/live・reaction画像一致/reactor一致/current/live・parent post live・parent author activeの12 boolについて、現SQLのOR/EXISTS条件を直接表した式と、逆参照index＋factor flags式を全4,096組合せ比較し一致。複数参照ORの32組合せも一致。再現コードと出力は本書の付録Aに全文を収録した。

これは同じ仕様の2つの抽象表現の比較であり、実PostgreSQL/NULL/実データ生成、署名、同時実行、通信、disk durability、競技実装の検証ではない。**parent author退会による別reactor画像失効もbool軸に含めた**。次に必要なのは実データのmulti-reference/index構築とmutation replayの同値性試験。

### 同期設計の必須補足

cacheへの古いfill拒否だけでは足りない。同じ古い結果をinflight requestへ直接返す経路もepoch/versionで検証し、長いbytes取得後は期限・失効を再確認する。画像キー単独ではなくowner/対象post作者等の依存状態の一貫したsnapshotが必要。全serving edgeのACKは地域代表1プロセスのACKと同義ではなく、全worker/旧deployプロセスの提供状態まで含めて保証する。DBcommit後ACK失敗の500は更新取消ではないためoutbox再送を継続する。

画像経路を外出しする場合、`image_api.go` の不正署名時のabuse分類と、`action_log.go` の対応するログ意味も保持する。単に403だけ返すRPCでは分類が欠落する。IP banの書込み側は取得済みGo実装では未確認で、ログが不要とは判断しない。元X-Client-IPと既存の認証・署名エラー意味を保つ。

### 画像移送で落としやすい運用上の同値性

署名不正時の`abuse=invalid_signature`、403 status、action logのinfo分類、元の`X-Client-IP`もRPC結果又は同等の入口contextへ戻す。`ip_bans`を更新する処理は読解対象のGo/SQLでは確認していないが、読取側だけを根拠に分類ログを無意味とは判断しない。認証関連値・署名鍵を公開レスポンスや測定ログへ含めない。


<a id="d2"></a>

## 10. D2 — 通知fanoutの全員分書込みを仮想化する

### 構造を変えるポイント


- **全員への通知row書込みを、windowイベント1件＋必要な受信者の遅延生成へ変える余地がある。** snapshot内でnotificationsを読むAPIはSSEだけ。過去通知一覧・通知ID取得・既読・Last-Event-ID再送処理はない。ただし任意の利用者に同じ通知機能を提供し、対象者を減らしてはいけない。
- **主問題はID生成でなく、期間開始時点のactive/regionを後から再現すること。** 現在のusers参照やupdated_at比較では誤る。
- **users全表lockの代替として、append-only membership履歴＋イベント時点のMVCC snapshot保存**を提案。実データ履歴を残した上で、どの変更がイベント開始時にcommit済みだったかを復元する。DBの古いtupleに依存するMVCC time travelではない。
- **予想外の互換性注意:** 現SSEの通知queryにはregion絞込みがない。現在所属regionだけでfanoutすると、一時的な地域移動と復帰の競合で取り逃しが起きる。候補windowは全3regionの現役分。

### 1. API/永続データの全参照確認

`rg notifications` をGo/SQL全体に実施。作成は [`go/admin_posting_window_api.go:70–73`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L70-L73)、読取は [`go/notification_api.go:51–56`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/notification_api.go#L51-L56)、初期化削除は [`sql/reset.sql:7`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/reset.sql#L7)。ルート一覧 [`go/server.go:47–88`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/server.go#L47-L88) に他の通知操作なし。export (`go/export_api.go`) はpost/画像のみ。schema `sql/001-schema.sql` の notifications はPK(id)、UNIQUE(user_id,posting_window_id)、user/windowへのFK。

#### 通知の外部に見える意味

1. 開始APIはusers表SHARE ROW EXCLUSIVEロック後、過去/同日conflict判定、DB clock_timestampをstartsAtとして旧window終了、新window作成、当該regionのactive全員にuuid通知を作りcommit ([`admin_posting_window_api.go:46–80`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L46-L80))。
2. SSEは認証時に現在user.regionをsession.Regionとして固定 ([`session.go:50–55`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/session.go#L50-L55))。接続時刻はhandler内のDB clock_timestamp ([`notification_api.go:26–28`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/notification_api.go#L26-L28))。
3. ループごとに現在active userを読み、消失/退会または現在region≠接続時regionなら終了 (`:40–48`)。
4. 自分宛てで `n.created_at > connectedAt`、所属windowが未終了の通知から `created_at,id` 順で最初の1件 (`:51–56`)。**現在regionとの一致条件はない。**
5. `data: {id,postingWindowId,region,createdAt}` を送ってflushし、接続終了 (`:58–67`)。SSE `id:` フィールド、既読更新、Last-Event-ID読取はなし。
6. 通知createdAtは生成処理時刻でなくwindow.startsAt。複数の同一user SSEがあれば同じ通知IDを返す。再起動してもIDは変えない。

#### APIから言えること/言えないこと

過去通知のquery APIは存在しないが、これを理由に対象者を購読中userに限定するのは不可。任意userに対して仮想通知が存在し、条件が合えば同じ結果を返せる必要がある。通知IDは外部JSONなので安定性/一意性/既存初期値は維持。新規IDが暗号学的ランダムUUIDでなければならない明文化までは見つからないが、安全な段階では既存と同じUUIDv4を遅延生成・永続化する。

### 2. なぜ現在usersだけでは再現できないか

| 誤った簡略化 | 反証例 |
|---|---|
| 通知送信時のregionだけ判定 | event開始時asia、送信前europe移動: asia通知のrecipientだった事実が消える |
| created_at < event.startsAtだけで登録判定 | 登録INSERTのtimestampは早いがcommitがevent開始後。開始時点で未登録なのに通知してしまう |
| updated_atを履歴の代わりにする | 表示名更新でもupdated_atが変化。移動して戻った履歴も失われる |
| membership変更の壁時計timestampを比較 | transaction内の変更時刻とcommit順は違う。rollbackもある |
| current/session regionのeventのみ | asiaで接続→europeへ移動→europe開始→asiaへ復帰、次poll。現実装はeurope通知を返し得る |
| region変更eventで必ずSSE強制切断 | 現実装はpollで不一致を観測した場合のみ終了。poll間で往復した移動を一律検出して切ると同一挙動ではない |
| expired windowもqueueから配信 | 開始後に次期間が始まったのに古い通知を返す。現queryはended_at IS NULL |
| commit後の接続にも新eventをpush | startsAt < connectedAt < event commitなら通知対象外。厳密なcreatedAt > connectedAtが必要 |
| IDを送信ごとにuuid生成 | 2本の同時接続が同じ通知について異なるIDを見る |

現在SSEのユーザー確認と通知SELECTは別statementなので競合中の挙動に幅はある。設計時はAPI操作の実時間順序と整合する線形化点を明示し、競合中だけの既存偶然のraceを仕様改善の名目で勝手に利用しない。

### 3. 推奨する段階的モデル

#### Phase A: 配送の集約のみ

既存notificationsの行生成を全く変えず、共有dispatcher/batch問い合わせでSSEの2N/500ms pollingを除く。具体的な配送条件と移行は本書B2のとおり。配送は全region eventを見て、受信者条件を別途検証。リスク最小で、fanout書込みが本当に次の律速か測る。

#### Phase B: append-only履歴をshadowで作る（元動作は維持）

例:

```
user_membership_versions(user_id, revision, region, status, top_xid xid8, epoch)
window_events(window_id, starts_at, recipient_snapshot pg_snapshot, epoch)
notification_ids(user_id, window_id, id UUID, created_at) UNIQUE(user_id,window_id)
```

- 初期userはbaseline revision 0。移行/初期化時に全userを一度読んで構築する。baselineを永続化し、既存初期notificationsはそのまま保持。
- registerはuser行とrevision 1を同transactionでINSERT。region変更/退会は本人行の排他下でrevisionを増し、変更後region/statusを追記。プロフィール等だけの更新はmembership revisionを増やさなくてよい。
- 変更経路は [`user_api.go:104`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L104) 登録、`:221` region、[`account_deletion_api.go:28`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/account_deletion_api.go#L28) 退会。初期化 [`reset.sql:24–32`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/reset.sql#L24-L32) は別epochでbaseline再構築。将来のadmin変更等にも追記漏れがないようDB trigger等で全経路を保証する設計がよい。
- `top_xid` は `pg_current_xact_id()` で取得したtop-level xid8。userごとrevision順に並べる。**transaction ID大小順はcommit順でもuser revision順でもないので順序として使わない。** rollbackした履歴行は永続化されない。
- 現users表freezeの内側でevent snapshotを保存し、旧通知row集合と仮想集合を比較するshadow検証から始める。

#### Phase C: eventのsnapshot cutでrecipientを仮想化

イベント開始transactionで同一regionの開始をserialize（region状態行の排他/advisory transaction lock等）。その後、**新しいstatement snapshot** を取得してrecipient_snapshotとして保存し、DB startsAtも記録。READ COMMITTEDでlock待ち前の古いstatement snapshotを再利用しない。旧window終了・新window・eventは一括commit。

recipient(user,event) は、保存済membership versionのうち `pg_visible_in_snapshot(top_xid,event.recipient_snapshot)` がtrueである最大revision（または初期baseline）を選び、region一致かつactiveか確認する。

PostgreSQL 16の`pg_snapshot`はその時点のtop-level transaction可視性を保持し、`pg_visible_in_snapshot(xid8,snapshot)`で判定できる。subtransaction IDは不可。snapshotはcommit/abortを直接区別しないが、ここでは同一transactionで書いて現時点で読める**commit済み履歴行だけ**を判定するためabort行を候補に入れない。[PostgreSQL公式](https://www.postgresql.org/docs/16/functions-info.html#FUNCTIONS-PG-SNAPSHOT)

この方式はsnapshotをDB値として保存するだけで、長時間transaction/exported snapshotを維持しない。VACUUMによる過去tuple削除に依存しない。全membership履歴をアプリケーションの永続データとして残すのが重要。物理DB再作成/論理移行ではxid namespaceが変わるため、そのまま持ち越せない。epoch切替時に旧eventのrecipientをmaterializeするか旧判定結果を保存する必要。

#### Phase D: UUID mappingを遅延生成

SSEが取得条件を満たす仮想通知を見つけたら、`notification_ids` にgen_random_uuidによるIDをINSERT ON CONFLICTして、commit済みの既存/新規IDを取得して送信。created_atはevent.startsAt。別region/processや同時接続でも同じIDに収束する。window終了前に仮想通知を選んだ後のID生成待ちには再確認を入れる（少なくとも開始成功後の新requestに終了済みeventを返さない）。

これで開始時はO(U)書込みからevent O(1)（snapshotサイズ除く）、実際の通知観測はO(D)ID行となる。DがUに近い場合は総書込み削減より**全員分の書込みを開始critical pathから外す**効果。ID保存のためのDB往復が受信tailに足されるためbatch/同transaction化が必要。

さらに強い完全仮想ID（persistent namespace＋user/windowから決定的UUID）ならD書込みも消せる。しかしIDのversion/衝突/namespaceの永続・初期化・初期既存IDoverrideを検証する負担がある。最初はUUIDv4 mappingの遅延生成が安全。

### 4. users表lockを外しても整合する理由と残るlock

現実装の全表lockは、登録・region変更・退会だけでなく、bio/画像/認定更新まで開始処理中に止める。それを外す代わりに「開始中の全user状態freeze」ではなく「開始の一点のmembership snapshot」を選ぶ。

- membership更新成功が開始呼出前: snapshotに必ず含む。
- 開始成功後にmembership更新開始: snapshotに含まない。
- 両APIが重なる: snapshot cut前にcommitした変更だけ含む。これは相互に重なる操作の線形化順として扱える。返答前のreadは同じ中央DBを使い、書込み後即反映を崩さない。
- 期間同士のserializeは依然必要。元のusers表lockは全regionの開始までserializeしているが、新方式はregion単位で十分。ただし同regionの日付conflict判定を排他取得**後の新statement**で行う。unique(region,local_date)＋one-active partial indexも維持する。
- 投稿のwindow共有lockとの競合は維持する（本書B1）。event生成は期間切替のcommitより先に配送しない。
- register自身にもusers表lock ([`user_api.go:93`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L93)) がある。通知側lockを消しても登録の全表lockは残る。これを消すにはusername UNIQUE等で別途同等の一意性を保証する必要があり、本案だけで「users表lock全廃」とは言わない。

#### MVCC案より単純な代案

regionごとepoch行を設け、membership変更は関係regionの共有lock、開始は排他lockを取得。所属履歴にregion epochを刻み、開始はepochを進める。移動は旧/新regionを固定順でlockし、本人行のlock順も全経路で統一する。これならsnapshot型に依存しないが、変更transactionを開始が待ち、期間切替中の変更も止まる。全表lockより小さいが完全非同期ではない。国をまたぐ移動のdeadlock設計負担があるため、厳密なlock順を検証せず導入しない。

### 5. コスト比較と撤退条件

U=regionの全active users、W=window数、D=通知を実際に観測したdistinct(user,window)、M=membership変更件数、S=event時に進行中のtop-level transactions。

| 方式 | 開始critical path | 追加永続量 | 配送時 |
|---|---|---|---|
| 現状 | U行INSERT/WAL/index + users表freeze | O(UW) | 通知index lookup、接続別poll |
| 配送集約 | 同じ | 同じ | shared/batch lookup |
| membership + lazy ID | event/snapshot O(S)、window更新 | baseline O(users)+O(M)+O(WS)+O(D) | history判定＋ID UPSERT |
| membership + virtual ID | event/snapshot O(S)、window更新 | baseline+M+WS | history判定＋ID計算 |

全userが毎期受信するならD≃UWでID行の総量は減らない。Uが小さい/通知INSERTが軽いなら履歴管理の複雑性が勝つ。Sが非常に大きければsnapshot自体が膨張。1userのmembership変更が多いと履歴lookupが重いので(user_id,revision DESC)索引と上限のない正しい探索を用いる。固定K履歴切捨ては不可。

### 6. 必須の反証テスト（まだ実行していない）

1. 元実装の永続通知集合と仮想recipient集合を、全user×各windowで比較。無接続ユーザーも含める。
2. register/移動/退会をcommit直前で止め、event snapshot前/後にcommit・rollbackし、全組合せのrecipient一致を確認。
3. user行lock取得順とxid取得順を逆にした複数変更で、xidの大小でなくrevision選択が正しいこと。
4. asia接続→europe移動→europe開始→asia復帰→配送。全region候補の必要性を確認。
5. startsAt前/同時/後の接続、event commit前の接続、旧window終了、2本SSE、dispatcher切断/再起動、初期化中の古いwaiter。
6. 同region2開始の順序/同日conflict、異region同時開始、投稿共有lockで開始が待つ場合を確認。
7. UUID mapping生成途中でprocess crash、commit前/後で再試行しID安定性。初期通知IDの保持。
8. 実ベンチでは開始API時間、users lock wait、通知commit→受信p95/p99、期間内投稿/検証人数、WAL量を比較。全体効果が出なければPhase Aに留める。

### 判断

有望なのは「全登録者分の事前書込みをなくす」ことで、単に通知1点を速くする話ではない。期間開始を小さなtransactionにして、プロフィール等の無関係な書込み停止と一斉投稿開始の遅れを除く。ただし本案は高難度。まず配送集約→membership shadow一致→lazy ID→必要ならvirtual IDの順が妥当。snapshot/履歴方式の正しさは理論設計段階で、検証なしの実装投入は勧めない。

### 補足: sequenceのcommit順trapと配送scope

- `nextval()`/bigserialでmembership変更とeventに通番を振り、`membership.seq < event.seq` とするだけでは不可。T1がseq=10を取得して未commit、T2(event)がseq=11を取得してcommit、その後T1がcommitすると、event時点では未反映だった変更が後からrecipient判定に混入する。transaction開始順やtimestampにも同じ問題。通番方式を使うなら通番取得からcommitまで共通lockでserializeしてcutを作る必要があり、全体ボトルネックが復活する。保存MVCC snapshotは「event時点でin-progressだったT1」を後から除外できる点が本質。
- 「region単位dispatcher」は**配送プロセスをregionごとに持てる**という意味に限定する。候補イベントは全3regionの現役window、現在user.regionと接続時session.Regionの一致は別の終了判定、作成時点recipientは不変。この3条件を混ぜない。一時移動→復帰はコードから構成した反証例で実測結果ではない。
- `ORDER BY created_at,id` も保持する。複数regionの通知が同一created_atになる可能性を切り捨てず、該当最古timestampの候補に対して安定IDを確定してからid順に選ぶ。lazy方式で「先に適当に1event選び、その後ID生成」はtie時の元の並びと一致しない。候補は各region現役windowなので最大3件。初期既存IDは常にoverrideする。


<a id="d3"></a>

## 11. D3 — 推薦を疎なgraph行・actor集合差分・hot viewer indexへ変える

### 設計の要点


1. 現行のconnectionsは一般に `C=A³−DA`（Dは次数対角行列）だが、**FOF候補は非直接友達なのでC=A³そのもの**。共通友達数Mは `A²−D`。これで「何が変わるか」をedge単位で厳密に扱える。
2. 単一friendship edge変更でMに影響するviewerは端点から最大1-hop、Cに影響するviewerは最大2-hop。単に当事者2人のcacheを失効するのは不足、全viewer失効は過剰。
3. 全員分のA³を増分物化するのも危険。差分に隣接ベクトル外積 `AΔA` があり、hub同士のedge変更1件が `deg(x)deg(y)` 規模のpair更新を生む。**hot viewerのみ・dirty再構築・小さいfanoutのみ増分**が現実的な折衷。
4. clamp30は、生のCを保存する以外に**30本の現存3-hop pathを証明として保持**できる。無関係edge削除では飽和値30が崩れない。削除が証明を壊した場合だけ補充。ただしこれはMや候補資格の更新を代替しない。
5. B cacheだけではtop51クエリはO(51)にならない。baseline同点bucket・未投稿作者・cursorの深いpageが残る。本当にqueryを `O(log C+51)` に寄せるにはhot viewerの**全適格active候補のordered index**か、それに相当する厳密な探索構造が必要。top51だけのcacheでは削除・深いpage・score移動を正しく扱えない。

推奨は「まずactor-presenceを共有で維持→必要viewerだけgraph行をcache→高read/低writeのviewerだけordered indexを昇格」。全ユーザー向けランキングtableを最初から作る案は推奨しない。

### 1. 現行意味を代数化する

[`post.go:123–157,198–213`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L123-L157) と [`feed_api.go:140–184`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L140-L184) が根拠。

A: 全friendshipsの無向0/1隣接行列、自己loopなし。利用者が退会してもedgeは残る。[`account_deletion_api.go:28–34`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/account_deletion_api.go#L28-L34) はstatus変更のみであるため、Aの中間頂点にactive filterを入れない。

- `d_v=Σ_a A_va`, `D=diag(d)`
- `M=A²−D`: `M_va` は共通友達数、M_vv=0。
- `C=MA=A³−DA`: 現行connections。行viewer・列author。
- `eligible_graph(v,a) := v≠a ∧ A_va=0 ∧ M_va>0`
- `B_v(a)=3M_va+min(C_va,30)`
- `H(p,u)=1[comment_count(p,u)>0 OR current_realmoji_exists(p,u)]`
- `E_v(p)=Σ_u A_vu · active(u) · H(p,u)`
- `S_v(p)=B_v(author(p))+10E_v(p)`

FOF候補には `A_va=0` なので `C_va=(A³)_va`。**これを他feed/general viewerContextにそのまま拡張しない**: 直接友達の場合は `d_v A_va` の補正が必要。M自体は対称、一般C=A³−DAは次数差により対称でない。FOFの非隣接pairではCが対称になる。

engagementは同じ人のcomment複数件・commentとRealMojiの両方を1人にまとめる。active actorだけを数えることと、graphの退会中間点を残すことは別の条件。

### 2. 1 edge更新の厳密な差分

x–yの追加ならs=+1、削除ならs=−1。旧Aを固定し `Δ=s(e_x e_yᵀ+e_y e_xᵀ)`, `A'=A+Δ`。存在しないedgeの削除や既存edgeの追加は対象外。

```
Δ(A²) = AΔ + ΔA + Δ²
Δ(A³) = A²Δ + AΔA + ΔA²
        + Δ²A + ΔAΔ + AΔ² + Δ³
ΔD    = s · diag(1 at x,y)
ΔM    = Δ(A²) − ΔD
ΔC    = Δ(A³) − DΔ − ΔD·A − ΔD·Δ
```

`Δ²=diag(1 at x,y)`。削除でも二次項は正なので「追加の変化量を単に負にする」近似は不可。二次・三次項やdegree補正を省略しない。Mの対角は厳密に0へ戻る。

#### 2.1 影響viewerの範囲と実務的な失効

3-hop walkに変更edgeを含むなら、viewerからそのedgeの近い端点まで0/1/2歩しかない。そのためCの変更行は端点2-hop集合内。Mでは最大1-hop。

- M/F/FOF候補集合: `{x,y} ∪ N(x) ∪ N(y)` にいるviewerを保守的に対象にする。
- B（C含む）: `{x,y} ∪ N(x) ∪ N(y) ∪ N²(x) ∪ N²(y)` にいるviewerを対象にする。
- 旧/新graphの集合のunionを使えば明快。単一edge更新では旧端点2-hop集合だけでも新たに近くなったviewerをカバーできるが、batch更新は**各edgeを順に評価するか旧/新の両方を使い、batch全体の経路を検討**する。
- Eの「友達になった/外れたactor」の変更はviewer=x,yだけ。それ以外のviewerのEはこのedgeだけでは変わらない。
- blockはedge削除＋双方向可視性変更＋follow削除の複合write。graph失効だけでは足りない。

実装初期はこの範囲にいる**現在hotなviewerだけdirty**にする。非hot viewerには何も物化しない。既にdirtyなら以後の細かな差分を蓄積せず次readで最新graphから再構築してよい。

#### 2.2 なぜ完全増分が書込爆発するか

`AΔA = s[(A e_x)(e_yᵀ A)+(A e_y)(e_xᵀ A)]` はN(x)×N(y)とN(y)×N(x)の外積。候補資格のfilter前では最大 `2 d_x d_y` pairに寄与。加えて `A²Δ/ΔA²` は端点の2-hop集合に作用する。一般の全行物化では1edgeの処理量が概ね `d_x d_y + Σ_{z∈N(x)}d_z + Σ_{z∈N(y)}d_z` まで増え得る（重複・candidate filterで減る場合あり）。

A²の非零pair数自体が最悪O(V²)、A³も疎とは限らない。画像SNSの疎graphであってもhub周囲は密になるため、「edge数が少ないから全rankpairも少ない」とは言えない。

**切替規則案**: fanout見積が小さくhot cacheがcleanなら差分適用、大きい場合はhot影響viewerをdirty化。dirty化も大規模ならgraph epochを上げてlazyに再構築する。全体epochはfallbackであり通常経路ではない。

### 3. 持つデータを3層に分ける

#### Layer 0: 全viewerで共有する基礎状態

- adjacency: `(src,dst)` 双方向。既存friendshipsと同transactionで維持、又は両端索引から読取。Aに退会中間点を残す。
- presence: `(post_id,actor_id,comment_count,current_realmoji_present)`。HはOR。索引はactor→active window→postとpost→actorの両方向を検討。
- active post registry: window別 `(author_id → ordered post IDs)` と `(posted_at,id)`順。最大3active windowsだが、作者がregion移動した場合を考え作者1投稿固定にしない。
- users status、block、viewer現在region/投稿日gateはsource of truthと同期。window世代を含めて旧window候補を切る。

#### Layer 1: 読まれたviewerだけのgraph baseline

cache entry:

```
viewer_id, graph_generation, state(clean/dirty)
F_v                           // 直接友達
M_v[a] > 0                    // 共通友達数; self除外
candidate author set          // M>0 minus F minus self
B_v[a] or (M_v[a], raw C_v[a]) // 完全又は需要時評価
optional saturation witnesses
```

cold viewerは同期build。LRU/容量上限/idle evictionでhot viewer数Hを制御。M mapはFOF作者だけでなく直接友達を含む場合がある点に注意: Cを `Σ_b M_v[b]A_ba` で作る時はMの直接友達成分も必要。

Mはviewer友達の隣接リストの和、Cは候補作者の隣接リストとMの積、又はMの非零supportから逆展開する。どちらが小さいかで選ぶ。C出力だけなら30到達で停止できる。B cacheを全candidate author分構築せず、postがある作者から評価するlazy方式も可能（未計算authorが後から投稿した時に評価）。

#### Layer 2: 特にhotなviewerだけordered candidate index

`(score DESC,posted_at DESC,id DESC)` をキーに、**可視な全active候補post**を保持する。全viewerではなく昇格条件を満たすviewerだけ。indexはpageごとのtop51 cacheではない。

- request: source-of-truthの投稿済gate確認、必要な更新barrierを満たす、cursorでseek→51件→profile等hydrate。
- score変更: 古いkeyをremove、新しいkeyをinsert。
- 投稿追加/削除: 作者の購読hot viewer集合へinsert/remove。
- graph変更: 小fanoutなら差分、その他はentry dirty→次request build。
- window close: window別subtreeに分けて旧windowを切り離し、新windowへ切替。3本のwindow別順序列の各top51をmergeしても厳密。

memoryは `O(Σ_{v∈hot} A_v + Σ_{v∈promoted} C_v)`。C_vは候補投稿数。Hが小さくてもhubのC_vは大きいのでviewer単位/全体の容量制限が必要。超過viewerはLayer1のonline queryにfallbackし、近似結果にはしない。

逆購読者表はhotのみ: `author→candidate viewers`, `actor→friend viewers`。新たにFOFになった作者やfriend変更で購読更新が必要。graph entryがdirtyなら古い購読に依存して正しい順位を保証しない; 次read時に完全再構築する。cold/evicted viewerへはfanoutしない。

### 4. clamp30を保持する3方式

#### 4.1 exact raw C（最も明快）

rawCを保持し、出力だけmin(rawC,30)。削除でも正しい。`30`だけを保持し「edge削除なので29」は誤り: 真のC=100→99ならまだ30。rawCは3-hop path本数なので十分な整数型とoverflow条件を確認する。

#### 4.2 dirty recompute（最初の実装候補）

Cの飽和値しか持たず、影響edge変更でdirty。必要になった作者だけ、Mを参照し隣接和が30になるまで再計算。低頻度graph writeなら実装・同期負担が小さい。飽和が多いほど早期停止が効く可能性はあるが測定が必要。

#### 4.3 30 path証明書（探索的な次段）

FOF候補ならC=A³なので、異なる30本の有効walk `v–f–b–a` を保存すればC≥30の証明になる。非隣接v,a・自己loopなしの条件下ではこの長さ3walkに端点への即時戻りを含められず、通常の3edge pathとして扱える。

- 30本が存在する限りCの出力は30。証明に使っていないedgeが削除されても再計算不要。
- 証明edgeの削除で失ったpathを除き、現graphから30本まで補充。補充できなければ残りの全walkを数えてunsaturatedの正確値へ移行。
- 追加edgeは既存証明を壊さない。ただしMは増え得る、v-aが直接友達になれば候補資格が消えるため、その更新は別に行う。
- pathは重複なし。M(b)の数字だけを「証明」として保存するなら、そのMの有効性と具体的経路/世代を確認する必要がある。
- 逆依存 `edge→certificate IDs` を持つか、読取時に最大90edgeの有効性を検証する。前者はwrite/memory、後者はreadが増える。全pairに30pathを持つと膨らむのでhotな飽和候補だけ対象。

これはrawCの大きさにかかわらず小さな証拠で「30以上」を維持する案であり、未計算作者の上位可能性を勝手に排除する仕組みではない。M/candidateの失効と併用必須。抽象モデルでは下記の行列代数まで検証済みだが、この証明書更新アルゴリズムは未実装・未検証。

### 5. distinct actor countをincrementalにする

[`feed_api.go:157–180`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L157-L180) のUNION意味を維持する状態機械:

```
old_present = comment_count>0 OR realmoji_present
apply one logical mutation atomically
new_present = comment_count>0 OR realmoji_present
if old_present != new_present:
    ΔH = +1 or -1
    for cached viewer v with A_vu=1 and active(u):
        E_v(p) += ΔH
        if p is an eligible candidate in v's index: rekey by 10*ΔH
```

comment作成/deleteはreference countを増減。最後のcommentを消してもRealMojiが残ればΔH=0。RealMoji置換は同transactionで旧終了＋新追加 ([`realmoji_api.go:90–99`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L90-L99)) なので、**論理的にはpresence不変**。旧行delete相当・新行insert相当の各イベントを外へ流して中間状態を見せない。

- friendship x-y追加/削除: E_xにはactive yのpresence集合を±、E_yにはactive xのpresence集合を±。他viewerのEは不変。候補資格変化はgraph側で処理。
- actor退会: そのactorのactive window presenceを現在の友達viewerに−。Aは消さずM/Cを保持。statusをpresenceに焼き付けない方式ならread時active joinでもよいが、Layer2のscoreには失効/再計算が必要。
- post作者退会: その作者の候補postsを全該当hot indexから除外する（又はdirty）。actorとしてのscore変更とは別経路。
- block: friendship削除分、view可視性、followingの各変更。block actorによる過去engagementを一律deleteする規則は現行にはない。Eは「いま友達か」とactor.activeで決める。
- window close: presenceの履歴は消す必要がなく、active window indexから外すだけ。過去履歴全scanを避ける。

ロック・競合: `(post,actor)` のcount更新はatomic increment/row lock等でlost updateを防ぎ、1→0/0→1判定とtransaction境界を一体化する。friendship変更/退会と同時のengagementには、同じsnapshot又は順序付きイベント適用が必要。抽象モデルの順次遷移が通っただけでは並行安全性を保証しない。

### 6. top51/cursorを近似せず高速化する

[`feed_api.go:91–126`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L91-L126) の完全順序Kは `(S,posted_at,id)` の辞書順DESC。ranked cursor適格は `K < (beforeScore,beforePostedAt,beforeId)`。**scoreだけ先にfilterしてtime/idを後で扱う、postedAtだけで先に51件切る、50件しか選ばない**のはいずれも誤り。

#### 6.1 Layer1向け: 正確な2集合分割

- P+ = 適格候補でE>0のpost集合。friend actor→presenceで完全列挙できる。
- P0 = 適格候補からP+を除いた集合。score=B(author)。
- 各枝で全可視性・cursorを適用しtop51。両枝最大102件からtop51。

証明: ある枝で52位以下の要素には同じ枝に先行する適格51件が存在するためglobal top51には入れない。枝はpost IDで排他的。**P0からP+を除外しないと、boost後cursor外に移った投稿をbaseline scoreで誤って返す**。

B順author listからP0を得る場合、同じBの作者すべてがtime/id tie-breakに参加する。bucket先頭51作者だけの取得は不正。未投稿作者が多いとB順scanは長くなる。作者毎active投稿streamをheap化すれば厳密だが初期化にO(A_v)が必要。これがLayer2を必要とする理由であり、「B cacheで全scan解消」とは断定しない。

#### 6.2 Layer2向け: ordered index

最新状態の全候補を順序木に保持できれば `seek(cursor)+next(51)`。queryは概ねO(log C_v+51)、writeは影響post当たりO(log C_v)。HasMoreは51件目の存在、返却は50件。適格性の最終filterが別途あるなら51件揃うまで読み進める。scoreを変えるactor退会等は単なる最後のfilterでは処理できずrekey/dirtyが必要。

次pageまでにscoreが変わった投稿は**現時点score**でcursor判定する。現実装はpage間のランキングsnapshotを固定していない。ユーザーが渡した古いcursorに対して最新indexでseekする。変化によるpage間重複/取りこぼしを独自snapshotで「修正」すると意味が変わる。

### 7. 一貫性: cacheを速くするほど必須になる境界

- 元テーブルwriteと派生presence/graph世代更新を同transactionにするのが最も明快。
- ローカルprocess cacheはwrite成功後の次readで最新状態を見られる仕組みが必要。単なるTTL/非同期pubsubだけでは不十分。
- outbox/event方式ならcommit済状態とのbarrierを読取側に置き、barrierまで適用できないcacheはsource-of-truth再構築にfallbackする。**発番sequenceの大小をcommit順序とみなしてはいけない**（先に発番したtransactionが遅くcommitし得る）。未commit gapを飛ばして処理済とする設計は不正。
- 共有graph世代を毎edgeで更新する実装は簡単だが、その行をglobal lock bottleneckにし得る。通常は局所dirty、fallbackにglobal epochを使う。
- 再構築中にwriteが進む場合はsnapshot versionを取得→build→version再確認、又はsnapshot以降の差分を適用してpublish。初期buildとlive updateの間の取りこぼしを防ぐ。
- gateは[`post.go:109–117`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L109-L117)どおり現在regionのactive local_dateと自分の非削除投稿の日付で判断。windowの閉鎖・region移動・自己投稿削除で変化する。本人が未投稿ならindex内容に関係なく空。
- profile/画像URLをindex値に焼かず、最終50件をhydrateすることでprofile更新の全rankindex失効を避ける。

### 8. online queryとincremental writeの損益

定義:
- q_v: viewer vのranked read頻度
- λ_v: vのgraph cacheをdirtyにするedge更新頻度
- G_v: graph baseline再構築コスト
- J_v: Layer1のpresence取得＋候補探索/top51コスト
- C_v: Layer2のactive候補数
- r_p: active投稿insert/delete頻度、r_h: presence 0↔1頻度
- f_p/f_h: 各イベントの平均hot購読viewer fanout

比較モデル（実測値ではない）:

```
毎回計算:     Σ_v q_v (G_v + J_v)
Layer1:       Σ_v [ rebuild_rate_v G_v + q_v J_v ] + dirty通知コスト
Layer2:       Σ_v q_v (log C_v + 51)
              + Σ_p r_p f_p log C
              + Σ_h r_h f_h log C
              + graph dirty再構築 + status/block/window処理
```

read/writeを独立Poisson、dirty中のwriteをまとめて次readで1回rebuildする単純モデルでは、rebuild rateは `q_v λ_v/(q_v+λ_v)`（dirtyとcleanの2状態）。λ≪qならcache有利、λ≫qならほぼ毎read再構築となり保存の旨味が消える。これは計画用モデルであり実負荷分布・burst・競合を測定する必要がある。

昇格基準案: 最近の `read_count × saved_query_cost` が、予測 `write_fanout × rekey_cost + rebuild + memory` を上回るviewerのみLayer2。graphが激変するviewer、巨大候補集合、更新fanoutの高い人気actor周りはLayer1へ降格。閾値はベンチ/実測で決め、ここで数値を固定しない。

presence 0↔1だけをfanoutすれば、同じ人がcommentを何度追加してもscore updateは1回で済む。これがraw engagementイベント全件fanoutとの構造的な差。逆に、多数の友達が初回反応する人気投稿ではfanoutは依然大きい。

### 9. 実装段階と撤退点

| 段階 | 変更 | 得られるもの | 次段へ進む条件/撤退点 |
|---|---|---|---|
| 0 | 現行と同意味の純粋計算をreference化、差分test | 正しさの基準 | cursor/status/region/削除ケースが通らないなら進まない |
| 1 | actor→active presence逆引き、候補author起点query | 全人気投稿engagement読込を避ける | 友達履歴scanが支配ならwindow索引; presence writeが重ければUNION queryを残す |
| 2 | on-demand M/B cache、global epochから局所dirtyへ | repeated graph計算を減らす | q/λが小さい・hubでbuild高騰ならcache対象を限定 |
| 3 | presence refcountをtransaction維持、hot逆購読 | DISTINCT重複計算と無駄fanoutを減らす | count競合・初期化整合が未解決なら導入しない |
| 4 | read-heavy viewerのみ全candidate ordered index | 深いcursorを含むtop51 seek | write/memory増幅がquery節約を超えたら降格 |
| 5 | 小fanout graph代数差分、clamp証明書 | dirty/rebuildを減らす余地 | 式とevent順序の複雑さに比べ利益が小さければdirty方式維持 |

全段階でfallbackは正確なonline計算。近似rank、top51固定cache、eventual visibilityをfallbackにしない。

### 10. 抽象検証: 実施内容と限界

再現用モデルの全文は本書の付録Cに収録。seed `20261031`。標準Pythonの小さな独立モデル、競技コードimportなし・ネットワーク/DB/サーバ接続なし。

実行結果:

```
graph_edge_transitions_verified = 10840
engagement_transitions_verified = 6000
partition_cursor_cases_verified = 3000
```

- 5頂点の全1024無向単純graph×全10edge toggle=10240遷移と、9頂点ランダムgraph150個×4toggle=600遷移。
- 現行ループ相当のM/C直接計算 vs A²−D/A³−DAを比較。
- 完全な二次/三次差分式をedge追加/削除のfull recomputeと比較。
- Cが変わるviewerが端点旧/新2-hop集合内であること、FOF候補のC=A³を確認。
- comment refcount/RealMoji presence/actor status/friendshipを6000回順次変更し、incremental Eと全再計算Eを比較。status変更時にgraph edgeは消していない。
- score/time/id同点を含む3000ランダム候補集合で、cursor適用後のP+/P0各top51のmergeが全候補sort top51と一致。

**未検証**: PostgreSQL plan、API仕様/benchmark checkerとの完全一致、並行transaction、複数app cache同期、実clock/id照合順、region/window gateの実装、certificate更新、性能/メモリ量。モデル成功を「実装検証済」又は「高速化確定」と表現してはいけない。

#### 実装時に追加すべきランダム差分検証

基準handlerと候補実装へ同じ操作列を与え、毎操作後/ランダムpageで返却ID・score・HasMoreを比較:

- relationship追加/削除、block/unblock、退会（中間点/作者/actorそれぞれ）、follow更新。
- comment複数件・最後のcomment削除・RealMoji置換/delete、同時操作のtransaction順序。
- active windowsの日付差、region移動、自己投稿delete/repost、window世代切替。
- scoreがcursor上下に跨ぐ更新、全件同時刻、同score bucket>51、candidate削除で52位が繰上がる、深いpage。
- warm cache作成中write、eviction後write再warm、event重複/遅延/並べ替え、process再起動、initialize。
- graph形状はchain/star/triangle/clique/bipartite/hub–hub edgeを含め、C=29/30/31/100近辺を意図的に作る。

最小反例: C=100のpairでpathを1本消す→clampは30のまま。E>0投稿がscore増でcursor外へ移動→P0で旧Bのまま拾ってはいけない。viewerから2-hop先のedge削除で3-hop Cだけが変わる→当事者/1-hopだけのcache失効は不足。この3件を先に固定回帰testへ入れる。


<a id="consistency"></a>

## 12. 横断条件 — cache失効・世代・応答時fence・初期化

### 12.1 全データ同期ではなく失効を同期する


画像本体などの不変データは近くに置き、可変メタデータだけ中央で確定する。write後に全データを全地域へ複製する代わりに、影響キーの失効が全ての読み取り提供プロセスに反映されたことを確認してから成功応答する案。失効後missは中央の最新状態を取得する。ヒットのWAN往復をなくすが、writeは最遠地域の失効ACKを待つ。

前提はDB更新と同じtransactionで単調な世代とdurable outboxを確定し、その変更対象を正確に特定できること。全SNSのfriend graph等は影響が広いため難しい。画像専用のowner/post/reaction/profileの小さいモデルが有望な理由は、この影響キーを狭くできること。

### 単にdeleteしてACKするだけでは不十分

旧値fetch開始 → 新値commit → cache削除とACK → 旧fetch完了でcacheへ復活 → write成功 → 新readerが旧値を読む、という順序が可能。

対策候補はキーごとの最低許可世代を保持し、fetchの値と世代を同じDB snapshotから取り、cacheへのpublishを世代比較と一体のatomic操作で行うこと。invalidationはその最低世代を上げ、古いfetchが後から完了してもpublishさせない。invalidateだけ残して世代を捨てると同じ穴が戻る。

### 成功応答の条件

1. 正本の変更、依存キー世代、outboxを同じDB transactionでcommit。
2. 読み取りを提供する全プロセスへキーと新世代を通知。
3. 各プロセスは世代fenceを適用し旧cacheを外してからACK。
4. 全ての提供先のACK後にwrite成功を返す。停止/到達不能プロセスをACK対象から外すなら、そのプロセスがstale readを提供できないよう経路/起動状態を制御する必要がある。固定公開入口を勝手に無くす方法は使わない。
5. 再起動プロセスはempty cacheで開始し、epoch/version整合が確認できるまでcache serveしない。initializeは全プロセスのepochを切り替え、古いinflight fetchを拒否。

これは完全実装ではない。複数キーを別々に適用した中間状態の整合性、複数writerの順序、outbox欠落、ACK紛失、commit後500応答と再試行、プロセス会員管理、versionGCなどが残る。必要な一連のmetadataはatomicな読取りsnapshot/適用単位にする。

### 費用と棄却条件

read rate R、write rate W、正本メタデータ取得時間 L、cache hit率 h、同期失効時間 A とすると、粗い比較は節約できるread待ち R*h*L と追加write待ち W*A。ただしこれは累積待ち時間の目安でありスコア予測ではない。無効化対象数・miss再増加・同時性・帯域・tailを別に測る。

投稿/反応/プロフィール変更が多く失効ばかりなら不利。中央小RPCで十分なら採用しない。キャッシュ失効を簡単にするために関係・公開範囲・画像の検証を省略してはいけない。

### 実施した抽象検証

付録Bのcache fenceモデルは、単一key、1write、1旧fetch、atomic cache操作、確実な失効ACKを仮定したイベント列挙。制約を満たす5順序中、単純deleteでは2順序でwrite成功後のreaderが旧値を読み、世代fence版では0。反例はFCIPWQ（F旧fetch、Ccommit、Iinvalidate、P旧fill、W成功応答、Qread）。

この検証は、上記の一般的なraceを示すだけで、実アプリの正しさ・PostgreSQL隔離・分散障害・性能を実証するものではない。

### 応答提供まで含めた必須条件

- fenceはcacheへの格納だけでなく、取得結果をHTTP応答へ使う直前にも必要。旧fetchのpublishを拒否したのに同じ旧値を直接返してはいけない。長いbytes待ちの後には失効・期限を再確認する。上の5順序モデルはHTTP送信中の競合を検査していない。
- imageIDの世代だけではowner退会・parent post author退会などの依存キー更新を拾えない。因子化projectionの関連状態をatomic snapshotとして読むか、完全な依存世代を検証する。影響を受ける画像全件の世代更新を選ぶとfanoutが戻る。
- 全ACK集合には同じ地域の全worker/processと旧deploy中のプロセスも含む。再加入時のserving epoch、遅延ACK拒否、deny cache/tombstone復活を設計する。
- commit後ACK失敗はDB更新の取消ではない。500や通信切断後もdurable outboxの配信を継続し、二重write再試行を勝手に行わない。

### 12.2 全面SNSメタデータ複製を急がない理由


#### 案と必要条件

単一writerにmetadataを集約し、各地域へ変更ストリームを配る。成功応答は**すべての読み取り提供先が当該変更を実際に適用した後**に返すか、read側が中央最新versionとlocal watermarkを照合して待機/fallbackする。ログ受領だけのACK、writer自身の更新だけ、同一sessionのsticky routingでは足りない。クライアントに新しいversion tokenの送付を要求する方式は既存clientに使えない。

- 常時local readを許す方式では、書込成功待ちが最も遅い地域に引っ張られる。障害時はstale replicaを読み取り提供から外すか中央へfallbackする。
- 競合中のcache fill、commitと通知の間の欠落、通知重複/順序逆転、subscriber再接続を処理。DB commitとdurable outboxを同じtransactionにするなど、失われない変更履歴が必要。
- sessions、users、graph、posts、windows、comments、realmojisのいずれかだけ遅れると、権限・ランキング・表示内容が混在する。関連変更のatomic applyとreset世代管理が必要。
- region変更 ([`user_api.go:220–235`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L220-L235)) があるためowner-region shardを固定できない。ユーザー間の友情/ブロックと全地域公開投稿も跨る。region毎DB分割は単なるWHERE分割ではなく、関係グラフとユーザー移動のプロトコルになる。

#### 反例

readが非常に多ければwriteの全地域同期コストを償却できるが、投稿波・関係変更が多いとwrite遅延と複製負荷で損をする。現状のデータ量が大きい場合、各地域のメモリ/ストレージ、初期化時間が上限になる。read/write活動比や実効RTTについては、追加されたベンチ成果物も含め本書で詳細再検証していない。

#### 最小測定

上位A/B後の残存WAN待ち、テーブル別変更頻度とbytes、全地域apply latencyのp95/p99、読み取り/書込み比、初期化復元時間。正しさ試験はA地域write成功の直後にB/Cから別ユーザーでreadし、ブロック/解除、公開範囲、友達解除、再ログインで旧session失効、region移動、退会、投稿期間切替を検査。障害・遅延を入れ、未適用replicaがstale 200を返さないことを確認。


### 12.3 初期化を「全提供先の世代切替」として設計する

現行initializeはshell resetを呼ぶだけである。RPC worker、画像bytes cache、image projection、通知waiter/dispatcher、membership履歴、graph/presence/indexを追加するなら、それらも初期状態へ戻す。reset前のinflight request、古いfill、旧deployのprocess、遅延outbox batchがreset後に再登録されるのをepochで拒否する。部分初期化を成功としない。

永続データとcacheは分ける。bytes cacheは消してよいが、file-backed正本はinitializeで復活する初期画像も保持する。live参照のない画像だけをGC条件にすると、初期化復活用画像を消す。初期snapshotをretention rootに含める。初期化・再起動後のprojection再構築が終わるまでは中央の正本判定へfallbackする。

### 12.4 認証・権限にTTLを持ち込まない

sessions、user.active、現在region、block、friendship、公開範囲、window、投稿済gateのどれかだけ古くても権限や応答が混在する。再ログインで旧sessionが失効すること、logout、退会も即時反映対象。内部cacheのために署名期限を延ばしたり、失効検証を省略したりしない。成功後readの保証が実装できなければ中央判定を残す。


<a id="validation"></a>

## 13. 測定・段階導入・採否判定

### 13.1 まず確立する基準

現在の対象commitと初期状態を記録し、地域別に同じ条件で測る。変更前走行を基準として残す。未知のベンチ内部ロジックを推測する代わりに、観測できる成功活動・待ち・転送を分解する。

| 観測対象 | 必要な量 | 何を区別するか |
|---|---|---|
| 活動連鎖 | region/user/windowごとの通知受信、投稿成功、friend feed検証時刻と人数 | HTTP成功だけ速いのか、期間内の採点活動が増えたのか |
| 投稿 | window lock取得/保持/待ち、同region別userの同時通過、commit時間 | 行直列化、WAN、blob転送、WAL、pool待ち |
| SSE | 同時待機数、idle SQL/s、queue、commit→受信p95/p99/max | 継続poll除去と受信tailの悪化を分離 |
| feed | feed種別、P/d/W2/A/C、rows visited/returned、hydration件数 | 索引lookupへ変わったか、単に全scanがDBへ移っただけか |
| 推薦 | 全engagement/友達distinct pair、graph read/write比、dirty率、fanout | Layer1/2のread節約とwrite増幅 |
| 画像 | imageID別size・地域再利用率・hit率・miss p95・WAN bytes | cacheに価値があるか、upload/GET RTTのどちらが残るか |
| RPC/DB | 逐次SQL回数、実DB時間、RTT、pool WaitCount/WaitDuration、中央CPU | CPU低下でもqueue/RTTで止まっていないか |
| 同期複製 | change bytes、全提供先apply/ACK p95/p99、fallback率 | write tailをread節約で償却できるか |
| 運用 | initialize時間、再起動復元、cache warm、正本画像取得 | 追試で崩れないか |

ログ相関は認証済user IDの安全な識別子、region、window ID、正規化path、所要時間、bytesを使う。Authorization、Cookie、SecretKey、署名付きURL全文を生のまま計測ログへ出す必要はない。既存のabuse分類と元IPの意味は保つが、機密値の追加収集はしない。

### 13.2 共通の差分試験表

| ID | 入れる反例・競合 | 期待する性質 |
|---|---|---|
| T1 | 同region異なるuserの投稿2本、同じuserの投稿2本 | 別userは並行、同userの同日制約は維持 |
| T2 | window lock前後で開始/投稿を交差、旧row待機 | 終了後旧windowへの新投稿なし、新window再取得 |
| T3 | SSE接続とstartsAt/commitを前後、移動→復帰、2本SSE | 全3地域候補、時刻境界、安定ID、1件で終了 |
| T4 | 上位51件不適格、52件目だけ適格、50/51/52件 | filter後top51、正しいHasMore |
| T5 | score/time同点、深いcursor、page間score移動 | 完全な(score,time,id)順と現時点score |
| T6 | 退会を中間頂点/投稿作者/反応actorそれぞれに | graph中間点は残る、作者/actor active条件は反映 |
| T7 | comment複数＋RealMoji、最後のcomment削除、置換 | actorごと0/1のOR、count lost updateなし |
| T8 | region移動でlocal_date同じ/違う、複数active投稿 | gateは日付条件、作者1投稿固定にしない |
| T9 | 画像期限/不正署名、profile置換、post削除、反応削除、対象post作者退会 | predicate同値、失効、403とabuse分類維持 |
| T10 | old fetch→write→invalidate ACK→old fill→成功後read | cache格納と応答提供の両方でstale拒否 |
| T11 | event重複/遅延/順序逆転、ACK喪失、process再起動 | durable replay、順序、全提供先fence、stale 200なし |
| T12 | initialize中のinflight、古いepoch、初期画像GC | 古い状態が復活せず、初期画像は正しく復活 |
| T13 | Aでwrite成功直後、B/Cで別user read | friendship/block/visibility/session/画像等が最新 |
| T14 | 全サーバ再起動、origin bytes、export、Memories | 初期/新規の履歴と画像の内容一致・永続性 |

各候補章のより細かな反証も合わせて実施する。抽象モデルの成功はこの試験表の代替ではない。

### 13.3 実装を進めるゲート

1. **意味のゲート**: 純粋計算/既存SQLのoracleと候補結果が一致する。まだ同期や性能を混ぜない。
2. **競合のゲート**: 意図的な停止点でwrite/read/初期化/再起動を交差させる。取り逃し、旧値復活、二重write、cursor誤りが出たら先へ進まない。
3. **局所性能のゲート**: 対象の仕事量が減ったことを示す。B1なら行待ち、B2ならN比例poll、B3なら全active転送、B4なら逐次RTT、B5なら再送bytes。
4. **全体効果のゲート**: 同じ初期状態でスコア、地域別活動完了、期間内検証人数、エラー/tailを比較する。偶然の1回だけで改善確定としない。測定回数は変動・競技残時間に応じて決める。
5. **復元のゲート**: initialize・全サーバ再起動・初期履歴・新規blob・exportが通る。ここまで通る前に正本移行や旧経路削除を確定しない。

### 13.4 変更別の最小導入と戻し方

| 変更 | 最小導入 | 戻す時の条件 |
|---|---|---|
| B1 | window lock modeだけ変更、本人lock/再SELECTを維持 | 旧modeへ。既存スキーマ・データ互換を保つ |
| B2 | 通知rowを残して配送だけ共有化 | durable通知pollへ。waiterの二重配送を抑止 |
| B3 | 非ranked/単一feedから、ID選抜後hydrate | 正確な既存online queryへ。近似cacheへ戻さない |
| B4 | 1業務だけRPC、元regionを信頼contextで保持 | 当該業務だけ元経路へ。commit不明writeは自動再試行しない |
| B5 | DB正本のread-through bytes cache | cacheを無効化してDB取得へ。可変判定は常時維持 |
| D1 | DB/file混在resolver→shadow projection→ACK barrier+local判定 | 先に中央判定へ切替え、次にbarrierを外す。file正本resolverは残す |
| D2 | 既存通知と履歴をshadow比較→lazy ID | 初期/生成済IDを維持。既に仮想化したwindowのrecipientを失わない |
| D3 | presence逆引き→on-demand graph→hot順序index | entryをdirty/降格し正確なonline queryへ |

### 13.5 停止判断

- 権限漏れ、画像失効漏れ、履歴/画像欠落、地域不一致、通知対象違い、再起動で正本消失があれば、速度に関係なくその段階は不採用。
- correctnessを保つfallbackが実装できない場合、非同期cacheやprojectionをread正本に昇格しない。
- 狙った費用が元から小さい、あるいは追加write/中央queue/初期化費用が大きく全体活動が増えない場合、深い次段へ進まず基本案で止める。
- 抽象的には有望でも、競技残時間で実API差分・競合・再起動試験まで終えられない構造変更は採用しない。時間を使ったことを理由に取り返しのつかない正本移行を確定しない。
- 最終的な推奨順は実測で入れ替える。現時点で確定しているのは「安全な採否の条件」と「反証すべき構造」であり、増点幅ではない。


<a id="sources"></a>

## 14. ソース索引

全リンクは分析対象commitへ固定している。本文のファイル名・行番号も同commitに対するもの。GitHubへのアクセス権が必要な場合でも、設計の条件は本文内に記載した。

| ソース | 主な根拠 |
|---|---|
| [`go/post_api.go:89–150`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L89-L150) | 投稿transaction、本人/現役window lock、blob保存 |
| [`go/post_api.go:347–374`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post_api.go#L347-L374) | 投稿削除 |
| [`go/admin_posting_window_api.go:40–80`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/admin_posting_window_api.go#L40-L80) | users表lock、期間切替、通知全員生成 |
| [`go/notification_api.go:20–79`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/notification_api.go#L20-L79) | SSEの接続時刻、user確認、全地域通知候補、500ms poll |
| [`go/post.go:65–80`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L65-L80) | 投稿候補と作者hydration |
| [`go/post.go:109–175`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L109-L175) | local_date投稿済gate、graphと閲覧判定 |
| [`go/post.go:198–213`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/post.go#L198-L213) | 直接友達ID一覧 |
| [`go/feed_api.go:35–126`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L35-L126) | feed選別、sort、cursor、50/51件 |
| [`go/feed_api.go:129–200`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/feed_api.go#L129-L200) | rank関数、distinct actor、feed種別条件 |
| [`go/image_api.go:25–98`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_api.go#L25-L98) | 署名、ownerと参照状態、bytes、送信前期限 |
| [`go/image_signature.go:25–42`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/image_signature.go#L25-L42) | 画像URL生成 |
| [`go/profile_image_api.go:34–96`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/profile_image_api.go#L34-L96) | profile画像置換/削除 |
| [`go/realmoji_api.go:70–110`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L70-L110) | RealMoji置換、画像生成 |
| [`go/realmoji_api.go:150–202`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/realmoji_api.go#L150-L202) | RealMoji削除 |
| [`go/account_deletion_api.go:20–39`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/account_deletion_api.go#L20-L39) | 退会statusとsession無効化 |
| [`go/user_api.go:87–116`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L87-L116) | 登録経路 |
| [`go/user_api.go:210–235`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/user_api.go#L210-L235) | profile/region変更 |
| [`go/relationship_api.go:125–150`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/relationship_api.go#L125-L150) | blockによるfriendship/follow削除 |
| [`go/relationship.go:43–64`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/relationship.go#L43-L64) | friendship更新 |
| [`go/export_api.go:25–60`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/export_api.go#L25-L60) | exportのi.data直読、ZIP Store |
| [`go/session.go:40–78`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/session.go#L40-L78) | 認証時のactive/region取得 |
| [`go/region.go:8–22`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/region.go#L8-L22) | 入口region照合 |
| [`go/ip_ban.go:45–68`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/ip_ban.go#L45-L68) | IP判定とX-Client-IP |
| [`go/action_log.go:57–111`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/action_log.go#L57-L111) | abuse分類とaction log |
| [`go/server.go:30–88`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/server.go#L30-L88) | DB pool、ルート、画像middleware境界 |
| [`go/initialize.go:18–30`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/go/initialize.go#L18-L30) | 初期化shell呼出 |
| [`sql/001-schema.sql:21–30`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L21-L30) | image_objectsのblob格納 |
| [`sql/001-schema.sql:135–167`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L135-L167) | window/post構造 |
| [`sql/001-schema.sql:204–210`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/001-schema.sql#L204-L210) | notifications構造 |
| [`sql/reset.sql:5–32`](https://github.com/tatsumack/isucon2026/blob/614854ef0bfef8e681ea5156cb33c69d0888df62/sql/reset.sql#L5-L32) | 初期状態復元 |

外部仕様の補助根拠:

- [PostgreSQL 16 — 明示的ロック](https://www.postgresql.org/docs/16/explicit-locking.html): FOR SHAREと更新lockの競合、FOR KEY SHAREとの差。
- [PostgreSQL 16 — snapshot情報関数](https://www.postgresql.org/docs/16/functions-info.html#FUNCTIONS-PG-SNAPSHOT): `pg_snapshot`、`pg_current_xact_id()`、`pg_visible_in_snapshot()`。通知の履歴設計はこれらの仕様を使う提案であり、実SQL試験は未実施。
- [当日マニュアル](https://gist.github.com/whywaita/17f3ab1a8c1b7585c571f4f8afe5f531)、[公式レギュレーション](https://isucon.net/archives/59966826.html): 本文の競技前提は提供された内容の要約。更新があれば公式を再確認する。


<a id="appendix"></a>

## 15. 再現付録 — 抽象モデルと実行結果

以下はPython標準ライブラリだけで動く独立モデルである。各コードブロックを記載名のファイルへ保存し、`python3 ファイル名.py`で実行できる。競技ソース、実DB、ネットワーク、認証情報は不要。**出力一致は抽象的な式・順次状態・限定イベント順序の一致であって、実装や競技ベンチの検証ではない。**

### 付録A: 画像predicateの因子分解

保存名: `image_predicate_model.py`

```python
"""Abstract image authorization model equivalence check.

Not a PostgreSQL/application/signature/concurrency/network/durability benchmark.
Source predicate: snapshot 614854e, go/image_api.go:45-86.
Run: python3 image_predicate_model.py
"""
from itertools import product

names = [
    "object_exists", "owner_active", "profile_match",
    "post_image_match", "post_owner_match", "post_live",
    "reaction_image_match", "reaction_owner_match", "reaction_current",
    "reaction_live", "parent_post_live", "parent_author_active",
]
checked = 0
for values in product([False, True], repeat=len(names)):
    s = dict(zip(names, values))
    # Direct abstract expression of the SQL OR/EXISTS predicate.
    sql = s["object_exists"] and s["owner_active"] and (
        s["profile_match"]
        or (s["post_image_match"] and s["post_owner_match"] and s["post_live"])
        or (
            s["reaction_image_match"] and s["reaction_owner_match"]
            and s["reaction_current"] and s["reaction_live"]
            and s["parent_post_live"] and s["parent_author_active"]
        )
    )
    # Construct reverse-index membership, then evaluate factorized flags.
    post_refs = (
        [{"live": s["post_live"]}]
        if s["post_image_match"] and s["post_owner_match"] else []
    )
    reaction_refs = (
        [{
            "current": s["reaction_current"],
            "live": s["reaction_live"],
            "parent_live": s["parent_post_live"],
            "parent_author_active": s["parent_author_active"],
        }]
        if s["reaction_image_match"] and s["reaction_owner_match"] else []
    )
    live_ref = (
        s["profile_match"]
        or any(p["live"] for p in post_refs)
        or any(
            r["current"] and r["live"] and r["parent_live"]
            and r["parent_author_active"]
            for r in reaction_refs
        )
    )
    model = s["object_exists"] and s["owner_active"] and live_ref
    assert sql == model, s
    checked += 1

# Multiple references preserve existential OR, not last-value overwrite.
multi_checked = 0
for profile, p1, p2, r1, r2 in product([False, True], repeat=5):
    assert (profile or p1 or p2 or r1 or r2) == (
        profile or any([p1, p2]) or any([r1, r2])
    )
    multi_checked += 1

print(
    f"PASS: {checked} single-reference boolean combinations + "
    f"{multi_checked} multi-reference OR combinations."
)
print(
    "Abstract expressions only; no real PostgreSQL, application code, "
    "NULL semantics, signatures, concurrency, network, disk durability "
    "or benchmark executed."
)
```

実行結果:

```text
PASS: 4096 single-reference boolean combinations + 32 multi-reference OR combinations.
Abstract expressions only; no real PostgreSQL, application code, NULL semantics, signatures, concurrency, network, disk durability or benchmark executed.
```

**限界**: boolのOR/EXISTSを別表現で比較しただけで、SQLのNULL、実初期データ、署名、mutation replay、同時実行、disk durabilityは含まない。

### 付録B: 失効後の古いfill復活

保存名: `cache_fence_model.py`

```python
"""Abstract single-key event model, not application/performance validation."""
import itertools,json
valid=naive_bad=fenced_bad=0; example=None
for order in itertools.permutations('FCIPWQ'):
 at={e:order.index(e) for e in order}
 # F: snapshot old fetched; C: commit new; I: invalidate ack;
 # P: old fill published; W: successful writer response; Q: new reader.
 if not(at['F']<at['C']<at['I']<at['W']<at['Q'] and at['F']<at['P']): continue
 valid+=1; db=0; cache=0; fence=0; fc=0
 for e in order:
  if e=='C': db=1
  elif e=='I': cache=None;fc=None;fence=1
  elif e=='P':
   cache=0
   if 0>=fence:fc=0
  elif e=='Q':
   n=db if cache is None else cache
   f=db if fc is None else fc
   if n!=1:naive_bad+=1;example=example or ''.join(order)
   if f!=1:fenced_bad+=1
assert naive_bad>0 and fenced_bad==0
print(json.dumps({'valid_event_orders':valid,'naive_stale_read_orders':naive_bad,'fenced_stale_read_orders':fenced_bad,'counterexample':example,'scope':'single key, one write, one old fetch, atomic cache operations, reliable invalidate ACK; not app/DB/network validation'},indent=2))
```

実行結果:

```text
{
  "valid_event_orders": 5,
  "naive_stale_read_orders": 2,
  "fenced_stale_read_orders": 0,
  "counterexample": "FCIPWQ",
  "scope": "single key, one write, one old fetch, atomic cache operations, reliable invalidate ACK; not app/DB/network validation"
}
```

**限界**: 単一key・1write・1旧fetch・atomic cache操作・確実なACKを仮定。HTTP送信中や複数依存keyの整合は検査しない。格納fenceだけでなく、旧fetch結果を応答へ直接使う経路にもfenceが必要。

### 付録C: graph差分・presence・top51分割

保存名: `graph_model.py`

```python
"""Abstract mathematical differential model only; not application/performance test."""
import random, json
R=random.Random(20261031)
def zeros(n):return [[0]*n for _ in range(n)]
def mul(a,b):
 n=len(a);return [[sum(a[i][k]*b[k][j] for k in range(n)) for j in range(n)] for i in range(n)]
def add(*aa):return [[sum(a[i][j] for a in aa) for j in range(len(aa[0]))] for i in range(len(aa[0]))]
def neg(a):return [[-x for x in row] for row in a]
def ref(a):
 n=len(a);m=zeros(n);c=zeros(n)
 for v in range(n):
  for f in range(n):
   if a[v][f]:
    for z in range(n):
     if z!=v:m[v][z]+=a[f][z]
  for z in range(n):c[v][z]=sum(a[z][b]*m[v][b] for b in range(n))
 return m,c
checks=0
for n,graphs in [(5,range(1<<10)),(9,range(150))]:
 edges=[(x,y) for x in range(n) for y in range(x+1,n)]
 for mask in graphs:
  a=zeros(n)
  for bit,(x,y) in enumerate(edges):a[x][y]=a[y][x]=(mask>>bit)&1 if n==5 else int(R.random()<.35)
  a2=mul(a,a);a3=mul(a2,a);m,c=ref(a)
  d=zeros(n)
  for v in range(n):d[v][v]=sum(a[v])
  assert m==add(a2,neg(d)) and c==add(a3,neg(mul(d,a)))
  pairs=edges if n==5 else R.sample(edges,4)
  for x,y in pairs:
   s=1 if not a[x][y] else -1;delta=zeros(n);delta[x][y]=delta[y][x]=s
   b=add(a,delta);dd=zeros(n);dd[x][x]=dd[y][y]=s
   de2=mul(delta,delta)
   da2=add(mul(a,delta),mul(delta,a),de2)
   da3=add(mul(a2,delta),mul(mul(a,delta),a),mul(delta,a2),mul(de2,a),mul(mul(delta,a),delta),mul(a,de2),mul(de2,delta))
   assert add(a2,da2)==mul(b,b)
   assert add(a3,da3)==mul(mul(b,b),b)
   mb,cb=ref(b)
   dc=add(da3,neg(mul(d,delta)),neg(mul(dd,a)),neg(mul(dd,delta)))
   assert add(c,dc)==cb
   radius={x,y}
   for _ in range(2):radius|={j for i in list(radius) for j in range(n) if a[i][j] or b[i][j]}
   for v in range(n):
    if c[v]!=cb[v]:assert v in radius
    for z in range(n):
     if z!=v and not b[v][z] and mb[v][z]>0:assert cb[v][z]==mul(mul(b,b),b)[v][z]
   checks+=1
# Incremental E: per pair presence uses comment refcount OR current realmoji;
# status and graph friendship use current membership, no edge removal on status change.
n=9;posts=17;counts=[[0]*n for _ in range(posts)];reactions=[[False]*n for _ in range(posts)];active=[True]*n
friends=[set() for _ in range(n)];ee=[[0]*posts for _ in range(n)]
def full_e():return [[sum(active[u] and u in friends[v] and (counts[p][u]>0 or reactions[p][u]) for u in range(n)) for p in range(posts)] for v in range(n)]
for _ in range(6000):
 op=R.randrange(5);u=R.randrange(n);p=R.randrange(posts)
 if op<3:
  old=counts[p][u]>0 or reactions[p][u]
  if op==0:counts[p][u]+=1
  elif op==1:counts[p][u]=max(0,counts[p][u]-1)
  else:reactions[p][u]=not reactions[p][u]
  new=counts[p][u]>0 or reactions[p][u]
  if old!=new and active[u]:
   for v in range(n):
    if u in friends[v]:ee[v][p]+=int(new)-int(old)
 elif op==3:
  active[u]=not active[u];sgn=1 if active[u] else -1
  for v in range(n):
   if u in friends[v]:
    for pp in range(posts):ee[v][pp]+=sgn*int(counts[pp][u]>0 or reactions[pp][u])
 else:
  v=R.choice([x for x in range(n) if x!=u]);sgn=-1 if v in friends[u] else 1
  for aa,bb in [(u,v),(v,u)]:
   if sgn==1:friends[aa].add(bb)
   else:friends[aa].remove(bb)
   for pp in range(posts):ee[aa][pp]+=sgn*int(active[bb] and (counts[pp][bb]>0 or reactions[pp][bb]))
 assert ee==full_e()
# Partition top51 proof for E=0 / E>0 branches under full three-key cursor.
for _ in range(3000):
 rows=[(R.randrange(35),R.randrange(6),i,R.randrange(4),bool(R.randrange(4))) for i in range(R.randrange(250))]
 # score=baseline+10*E, time, id; last field models final visibility.
 scored=[((b+10*e,t,i),e) for b,t,i,e,visible in rows if visible]
 cursor=R.choice(scored)[0] if scored and R.random()<.8 else (1000,1000,1000)
 eligible=[(k,e) for k,e in scored if k<cursor]
 expected=sorted((k for k,e in eligible),reverse=True)[:51]
 branches=[sorted((k for k,e in eligible if bool(e)==positive),reverse=True)[:51] for positive in (False,True)]
 assert sorted(branches[0]+branches[1],reverse=True)[:51]==expected
print(json.dumps({'seed':20261031,'graph_edge_transitions_verified':checks,'engagement_transitions_verified':6000,'partition_cursor_cases_verified':3000,'scope':'abstract algebra and discrete model only; no application, database, benchmark, timing or concurrency test'},ensure_ascii=False))
```

実行結果:

```text
{"seed": 20261031, "graph_edge_transitions_verified": 10840, "engagement_transitions_verified": 6000, "partition_cursor_cases_verified": 3000, "scope": "abstract algebra and discrete model only; no application, database, benchmark, timing or concurrency test"}
```

**限界**: 行列式・離散的な順次更新・完全なcursor順の比較。DB plan、文字列ID/時刻の実照合、region/date gate、並行transaction、cache同期、30path証明書の更新、性能/メモリは検査しない。
