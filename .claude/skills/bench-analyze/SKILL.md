---
name: bench-analyze
description: ISUCONでベンチマーク実行中のサーバを計測し、終了後にボトルネックを分析して報告する。「ベンチ分析して」「/bench-analyze」などで使う。
---

# ベンチマーク計測・分析

引数: 計測秒数 (省略時150秒)。ベンチは開始から終了まで約2分半かかる (#2540 で 148秒)。70秒では負荷走行の後半を取り逃す。
ベンチはルーター経由で3台のアプリサーバすべてに来るので、全台を同時に計測する。

| ホスト | 役割 | bench-capture.shの第2引数 |
|---|---|---|
| asia1, europe1, america1 | nginx + Goアプリ | `app` |
| asia2 | PostgreSQL | `db` |

## 前提
- `bench-capture.sh` がdeploy.shで全台に配布されていること
- アプリサーバに alp が入っていること (`./install-tools.sh`)
- PostgreSQLで pg_stat_statements が有効なこと (`etc/postgresql/16/main/conf.d/isucon.conf`)

## 手順

### 1. 計測を開始する
次の4コマンドをそれぞれ **`run_in_background: true` で** 同時に実行し、すぐにユーザーへ「計測を開始しました。ベンチを開始してください」と伝える。

```
ssh asia1 /home/isucon/webapp/bench-capture.sh 150 app
ssh europe1 /home/isucon/webapp/bench-capture.sh 150 app
ssh america1 /home/isucon/webapp/bench-capture.sh 150 app
ssh asia2 /home/isucon/webapp/bench-capture.sh 150 db
```

- ベンチ時間が違う場合はユーザーの指示に合わせる
- 完了通知が来るまで待つ。sleepやポーリングはしない
- 最終行に出力される結果ディレクトリ (例: `/home/isucon/bench/153012`) を控える (ディレクトリ名はUTCの時刻で、ベンチIDではない)

### 2. 結果を取得する
`<ベンチID>` はポータルのジョブ番号 (`#6150` なら `6150`) にする。bench-capture.sh の結果ディレクトリ名 (`153012` のような時刻) は使わない。
ジョブ番号はユーザーに聞くか、ポータルのジョブ一覧 (https://portal.isucon.net/contestant/benchmark_jobs) で計測開始時刻に最も近い開始時刻のジョブを探す。結果ディレクトリ名はUTC、ポータルの時刻はJSTなので9時間ずらして比べる。特定できなければユーザーに確認する。

```
mkdir -p bench-results/<ベンチID>
scp -r <host>:<結果ディレクトリ> bench-results/<ベンチID>/<host>
```

ベンチマーカーの行動分析 (`docs/benchmarker-analysis.md`) 用に、3台の access.log も取得して集計する。
access.log は計測が終わった後も書き込まれ続けるので、**ポータルでベンチの終了 (FINISHED) を確認してから** 取得する。

```
D=bench-results/<ベンチID>/timeline
mkdir -p $D
for h in asia1 europe1 america1; do ssh $h sudo cat /var/log/nginx/access.log > $D/$h.log; done
./bench-timeline.py $D asia=$D/asia1.log europe=$D/europe1.log america=$D/america1.log
```

- `$D/report.txt` (集計) と `$D/timeline.tsv` (ユーザー別の時系列) ができる。生ログと timeline.tsv は gitignore されている
- ログに `msec` が無いブランチ (計測用のログ項目を外した後など) では `no rows` で終わる。その場合は行動分析を飛ばし、報告にそう書く

ポータルのジョブ詳細から、スコアと Stdout (リージョン・日ごとの人数、グランプリの人数、エラーメッセージ) を `bench-results/<ベンチID>/stdout.txt` に保存する。ブラウザで読めなければユーザーに貼ってもらう。
スコアを上げる観点の分析 (手順3の6) はこれが無いとできない。取れなかった場合は報告の冒頭にそう書く。

### 3. 分析する
次の順で読む。生ログ (access.log) は読まない。

1. 各ホストの `pidstat.txt` / `vmstat.txt` — どのホストのどのプロセス (postgres / isureal / nginx) がCPUを使っているか、CPUが張り付いているか、iowaitがあるか
2. 各アプリサーバの `alp.txt` — SUM (合計時間) が大きいエンドポイント上位。COUNTとAVGも見て「遅い」のか「多い」のかを区別する。リージョン間の差も見る
3. asia2の `slow.txt` — pg_stat_statementsの total_ms 上位のクエリ。rows / calls が大きいもの、mean_msが大きいものはインデックス不足を疑う
4. `pprof_cum.txt` / `pprof_flat.txt` — アプリ内で時間を使っている関数。`isureal.` で始まるものに注目する
5. `timeline/report.txt` — ベンチマーカーの行動。生ログの代わりにこれを読む。特に次を見る
   - 投稿の流れ (通知 → posting-window/current → POST /api/posts → 友達フィード) と、投稿期間の開始間隔 (管理者のリクエスト)
   - 「投稿期間ごとの投稿〜友達フィード完了 (M2)」: リージョン別の期間内の完了率 (done / posts)。「投稿後の友達フィードの検証通過」(1件10点、最後の期間は20点) に直結する。施策の効果はこの率で比べる
     - 期間のリージョンはログに無いので、SSE が閉じた時刻から巡回の順番を推定している。採用した順番のスコアが2位とほぼ同じ、または「割り当てが疑わしい」期間がある場合は、数値を推定扱いにして報告に書く
     - 期間の境界は admin のリクエストの完了時刻。admin の reqtime が長いと境界がぶれる (#2540 では最大3.4秒。開始時刻を境界にすると europe 63% → 43%)。reqtime も報告に書く
     - 遅い場合は、通知 (期間開始 → SSE が閉じる) / 反応 (SSE → 投稿開始) / 投稿→フィード のどこで時間を使っているかを見る
   - エラーの後の行動: 即リトライされる 4xx (失敗ログイン、不正な署名の画像など) は加点されない。安く返せる候補
   - 書き込み直後の読み取り: キャッシュを入れる場合に無効化が必要な組み合わせ
   - 時間帯ごとのアクティブユーザー数: 負荷が増えるタイミング
   - ベンチの Stdout (ポータル) に出るリージョン・日ごとの人数、グランプリの人数
6. **スコアを上げるには** — 「どこが遅いか」ではなく「どこで点を取りこぼしたか」を見る。配点は `docs/manual.md` の「スコアの計算」
   - スコアは人数でほぼ決まる (`docs/user-growth-analysis.md`)。人数が増えなかったリージョン・日 (前日比 約1倍) を Stdout から挙げ、その日の admin の reqtime、CPU の張り付き、M2 の完了率と並べる
   - 取りこぼしを点数で見積もる。例: 最後の期間で完了しなかった件数 × 20点 × 倍率、通常の期間で完了しなかった件数 × 10点 × 倍率、人数が増えなかった日の人数差 × (1人あたりの点数の目安)。1人あたりの点数は、スコア ÷ 延べ人数で概算し、概算であることを書く
   - europe / america は倍率1.5。asia の負荷が europe / america の人数を止めていないか (#18039 のように asia1 が張り付くと europe / america の転送も遅くなる) を見る
   - 配点の大きい活動 (エクスポート 20点、RealMoji 5点、コメント 3点、プロフィール画像 3点、Memories 月単位 3点) のエンドポイントで、エラーや遅さで失っている件数がないかを alp のステータス別件数と timeline から見る
   - 加点の上限 (投稿期間ごとにフィード20回など) に達している活動は、速くしても点が増えない。上限まで達していない活動を優先する
   - 速くしても点が増えない処理 (加点されない 4xx、採点しない初期検証など) は、CPUを空けるために安く返す候補にする

その後、上位のエンドポイント・クエリ・関数についてアプリのコードを読み、原因箇所を `file:line` で特定する。
必要なら `ssh <host> "go tool pprof -list 'isureal\.<関数名>' /home/isucon/webapp/go/isureal <結果ディレクトリ>/cpu.pb.gz"` で行単位の内訳を見る (PATHに `/home/isucon/local/go/bin` を足す)。

`bench-results/` に前回の結果がある場合は比較し、改善・悪化したものを示す。`timeline/report.txt` も前回と比べ、ベンチマーカーの行動が変わったか (エラー後のリトライ、離脱するユーザー、グランプリの人数など) を見る。

### 4. 報告する
次の形式で `bench-results/<ベンチID>/report.md` に保存し、同じ内容をユーザーに出力する。

```
## 概況
- ボトルネックの所在: DB / アプリCPU / nginx / 外部待ち など (根拠の数値つき)
- 前回比: (前回がある場合)

## ベンチマーカーの行動 (timeline/report.txt より)
- 典型的な流れ、投稿期間の間隔、加点されないリクエスト、前回からの変化を、数値つきで箇条書きにする

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)
| リージョン | 投稿数 | 期間内に完了 | 前回 | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | | (%) | | | |
| europe | | (%) | | | |
| america | | (%) | | | |
| 最後の期間 (3リージョン計) | | (%) | | | |
- 期間の割り当て: 採用した順番と、2位とのスコア差
- 前回から下がったリージョンがあれば、通知・反応・投稿→フィードのどれが伸びたかを書く

## スコアを上げるには
| 取りこぼし | リージョン | 件数・人数 | 見積もり (点) | 原因 (根拠) |
|---|---|---|---|---|
| 人数が増えなかった日 | | | | |
| 最後の期間で未完了 | | | | |
| 通常の期間で未完了 | | | | |
| 配点の大きい活動の失敗 | | | | |
- 見積もりの計算式と、推測の部分を書く
- 次に打つべき施策を1つ挙げ、どの取りこぼしを何点減らす見込みかを書く

## ボトルネック上位 (最大5件、スコアへの効果が大きい順)
### 1. <短いタイトル>
- 根拠: alp/slow/pprofの数値
- 原因箇所: file:line
- 対策案: 具体的な変更内容
- 期待効果: 大/中/小 とその理由。「スコアを上げるには」のどの取りこぼしが減るか
- リスク: 仕様を壊す可能性、整合性の注意点 (キャッシュの無効化タイミングなど)
```

### 5. Slackに共有する
`report.md` をSlackの `#isucon2026` (チャンネルID `C0CDHTMH8MU`) にファイルとして添付して送る。本文に全文は貼らない。

1. `wc -c < report.md` でバイト数を測り、`slack_get_file_upload_url` を呼ぶ (filename: `bench-<ベンチID>-report.md`、snippet_type: `markdown`)
2. 返ってきたURLをそのまま変数に入れ、`curl -X POST -H 'Content-Type: text/markdown' --data-binary @report.md "$URL"` でアップロードする
3. `slack_complete_file_upload` で `#isucon2026` に共有する (title: `ベンチ #<ベンチID> 分析 (score <スコア>)`、initial_comment: `ベンチ #<ベンチID> の分析結果 (mdファイル)`)
4. 返ってきたファイルのリンクをユーザーに伝える

### 6. 結果をコミットする
`bench-results/<ベンチID>/` (計測結果と `report.md`、`timeline/report.txt`) をコミットして main に push する。データのみの変更なのでブランチとPRは作らない。
手元の作業ツリーは別の施策のブランチで、未コミットの変更があることが多い。触らないよう、一時的な worktree で行う。

```
W=<スクラッチパッド>/wt-bench-<ベンチID>
git fetch -q origin
git worktree add -q --detach $W origin/main
cp -R bench-results/<ベンチID> $W/bench-results/
git -C $W add bench-results/<ベンチID>
git -C $W commit -m "ベンチ #<ベンチID> の計測結果と分析を追加 (score <スコア>)"
git -C $W push origin HEAD:main
git worktree remove $W
```

- コミット対象は `bench-results/<ベンチID>/` だけにする。他のファイルを含めない
- 生ログ (`timeline/*.log`) と `timeline/timeline.tsv` はコミットしない (gitignore 済み。`git -C $W status` で入っていないことを確認する)
- push が拒否されたら (main が先に進んだ場合)、`git -C $W pull --rebase origin main` してから push し直す
- コミットのハッシュをユーザーに伝える

## 守ること
- **分析だけ行い、コードの変更・デプロイはしない。** 対策をどれから入れるかは人間が決める
- サーバ上でサービスの再起動、設定変更、`/initialize` の実行をしない
- 数値の根拠がない推測は「推測」と明記する
- 計測が失敗したもの (pprof_error.txt がある、ファイルが空など) は報告の冒頭で伝える
