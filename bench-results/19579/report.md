# ベンチ #19579 分析 (PR #54 asia1 の CPU を空ける: ログの削減、nginx の本文バッファ、画像の受信)

- ブランチ: `perf/asia1-cpu-relief` (`b4354f0`) = main (`d041437`) + #54。4台にデプロイ
  - `ISUREAL_LOG_LEVEL=info` と、guard が使う行・エラーだけを出す変更 (後に #55 として切り出し、main にマージ)
  - nginx の `client_body_buffer_size` 32m → 2m
  - 投稿・RealMoji・プロフィール画像の受信で、大きさの分を先に確保して読む (`readAllSized`)
- 計測: 05:51:31 UTC からの150秒 (4台、`055131`)。asia1 は `iostat` と `free` も取った。ベンチは 05:52:01〜05:53:02 (Stdout)
- 結果: **PASS / score 55636**。main の #17256 (94360) から **-41%**
  - `asia の投稿期間を開けません: POST /api/admin/posting-window: 応答がタイムアウトしました`。Stdout に asia の5日目の行が無い

## 人数 (ポータルの Stdout)

| | 2日目 | 3日目 | 4日目 | 5日目 | グランプリ |
|---|---|---|---|---|---|
| asia | 49 | 141 | 405 | (期間を開けず) | 35 |
| europe | 14 | 36 | 86 | 87 | 13 |
| america | 17 | 40 | 91 | 92 | 18 |

## 概況

- **asia1 がディスク待ちで止まった** (負荷の山 t=50〜95秒)
  - vmstat: us 18.7 / sy 31.4 / idle 1.6 / **wa 48.3**、読み 67MB/s・書き 50MB/s
  - iostat: 読み 1984回/s・63.7MB/s、書き 604回/s・50.0MB/s、待ち時間 **約10ms** (#18039 は約3ms)、aqu 22.8
  - 読み書きの合計 約114MB/s。EBS の標準の上限 (125MB/s) に近い (推測。ボリュームの設定は見ていない)
  - プロセス別の CPU (1コア=100%): isureal 39%、nginx 12% (wait 11%)、kswapd 9%、kcompactd 3%
- **nginx が本文を一時ファイルに書いていた**: `/var/lib/nginx/body` の更新時刻がベンチ中 (05:53:05)
  - 2MB を超える本文がディスクに書かれた。guard の BAN に `invalid_image` が多いことから、不正な大きいプロフィール画像と考えられる (推測。本文の大きさはログに無い)
- **ログの処理は消えた**: journald / journalctl / guard / rsyslog は CPU の上位に出ない。guard の BAN (login、invalid_image) はベンチ中も続いていた
- asia1 の空きメモリの最小は 271MB (#18039 は 135MB)

## 評価

- スコアの低下は nginx の `client_body_buffer_size 2m` によるディスクの書き込みの増加と考えられる (推測。変更ごとに切り分けたベンチはしていない)
- ログの変更はディスクの書き込みを増やさないので、#55 として切り出した。#55 だけの #20097 は 93801 で、下がっていない
- `readAllSized` はメモリの確保の仕方だけの変更で、ディスクには関係しないと考えている。単体のベンチはしていない

## 確認したこと / していないこと

- 確認した: 4台の vmstat、asia1 の pidstat・iostat・free、`/var/lib/nginx/body` の更新時刻、guard の BAN のログ、ポータルの Stdout
- 確認していない: pprof の詳細、alp、asia2 の slow.txt、本文の大きさの分布、nginx の値だけを戻したベンチ
