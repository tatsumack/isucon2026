---
name: bench-analyze
description: ISUCONでベンチマーク実行中のサーバを計測し、終了後にボトルネックを分析して報告する。「ベンチ分析して」「/bench-analyze」などで使う。
---

# ベンチマーク計測・分析

引数: 計測秒数 (省略時70秒)。
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
ssh asia1 /home/isucon/webapp/bench-capture.sh 70 app
ssh europe1 /home/isucon/webapp/bench-capture.sh 70 app
ssh america1 /home/isucon/webapp/bench-capture.sh 70 app
ssh asia2 /home/isucon/webapp/bench-capture.sh 70 db
```

- ベンチ時間が違う場合はユーザーの指示に合わせる
- 完了通知が来るまで待つ。sleepやポーリングはしない
- 最終行に出力される結果ディレクトリ (例: `/home/isucon/bench/153012`) を控える

### 2. 結果を取得する
```
mkdir -p bench-results/<ベンチID>
scp -r <host>:<結果ディレクトリ> bench-results/<ベンチID>/<host>
```

### 3. 分析する
次の順で読む。生ログ (access.log) は読まない。

1. 各ホストの `pidstat.txt` / `vmstat.txt` — どのホストのどのプロセス (postgres / isureal / nginx) がCPUを使っているか、CPUが張り付いているか、iowaitがあるか
2. 各アプリサーバの `alp.txt` — SUM (合計時間) が大きいエンドポイント上位。COUNTとAVGも見て「遅い」のか「多い」のかを区別する。リージョン間の差も見る
3. asia2の `slow.txt` — pg_stat_statementsの total_ms 上位のクエリ。rows / calls が大きいもの、mean_msが大きいものはインデックス不足を疑う
4. `pprof_cum.txt` / `pprof_flat.txt` — アプリ内で時間を使っている関数。`isureal.` で始まるものに注目する

その後、上位のエンドポイント・クエリ・関数についてアプリのコードを読み、原因箇所を `file:line` で特定する。
必要なら `ssh <host> "go tool pprof -list 'isureal\.<関数名>' /home/isucon/webapp/go/isureal <結果ディレクトリ>/cpu.pb.gz"` で行単位の内訳を見る (PATHに `/home/isucon/local/go/bin` を足す)。

`bench-results/` に前回の結果がある場合は比較し、改善・悪化したものを示す。

### 4. 報告する
次の形式で `bench-results/<ベンチID>/report.md` に保存し、同じ内容をユーザーに出力する。

```
## 概況
- ボトルネックの所在: DB / アプリCPU / nginx / 外部待ち など (根拠の数値つき)
- 前回比: (前回がある場合)

## ボトルネック上位 (最大5件、効果が大きい順)
### 1. <短いタイトル>
- 根拠: alp/slow/pprofの数値
- 原因箇所: file:line
- 対策案: 具体的な変更内容
- 期待効果: 大/中/小 とその理由
- リスク: 仕様を壊す可能性、整合性の注意点 (キャッシュの無効化タイミングなど)
```

### 5. Slackに共有する
`report.md` をSlackの `#isucon2026` (チャンネルID `C0CDHTMH8MU`) にファイルとして添付して送る。本文に全文は貼らない。

1. `wc -c < report.md` でバイト数を測り、`slack_get_file_upload_url` を呼ぶ (filename: `bench-<ベンチID>-report.md`、snippet_type: `markdown`)
2. 返ってきたURLをそのまま変数に入れ、`curl -X POST -H 'Content-Type: text/markdown' --data-binary @report.md "$URL"` でアップロードする
3. `slack_complete_file_upload` で `#isucon2026` に共有する (title: `ベンチ #<ベンチID> 分析 (score <スコア>)`、initial_comment: `ベンチ #<ベンチID> の分析結果 (mdファイル)`)
4. 返ってきたファイルのリンクをユーザーに伝える

## 守ること
- **分析だけ行い、コードの変更・デプロイはしない。** 対策をどれから入れるかは人間が決める
- サーバ上でサービスの再起動、設定変更、`/initialize` の実行をしない
- 数値の根拠がない推測は「推測」と明記する
- 計測が失敗したもの (pprof_error.txt がある、ファイルが空など) は報告の冒頭で伝える
