# ベンチ #19113 (asia1 のディスクの読み書きをプロセス別に測る)

- ブランチ: `measure/pidstat-disk` (`23baef2`、PR #53)。main (`d041437`、#45 通知の行を作らない・#52 nginx 設定変更を含む) に、`bench-capture.sh` の `pidstat -d` を足しただけ。4台にデプロイ
- 計測: 05:45:50 UTC からの150秒 (4台)。結果ディレクトリ: asia1 `054550`、europe1 / america1 `054551`、asia2 `054552`
- 結果: **score 92613**。main 相当の #17256 は 94360、#18039 は 89583 で、ぶれの範囲
- 目的: asia1 の書き込みの主因が、nginx の一時ファイル・画像の fsync・ログ (journald) のどれかを切り分ける

## 結論

- **nginx の一時ファイルは無くなっている** (nginx の書き込みは150秒で 11MB)。#52 が効いている
- **書き込みの主因は、新しく投稿された画像の保存** (isureal 1,292MB、全体の75%)。次が journald (314MB、18%)
- **書き込みより読み込みの方が大きい**。isureal が 2,720MB を読んでいて、ほぼすべてが t=40〜90 に集中する。初期データの画像 (16GB、183万ファイル) がメモリ (3.8GB、ページキャッシュ約1.8GB) に収まらず、画像を返すたびにディスクから読んでいる

## asia1 のプロセス別ディスク I/O (pidstat -d)

| プロセス | 読み込み (150秒) | 書き込み (150秒) | 書き込み (t=40〜90) |
|---|---|---|---|
| isureal | 2,720MB | 1,292MB | 1,257MB |
| systemd-journal | 88MB | 314MB | 301MB |
| jbd2 (ext4 のジャーナル) | 0 | 90MB | 74MB |
| nginx | 1MB | 12MB | 11MB |
| rsyslogd | 0 | 10MB | 9MB |
| 合計 | 3,199MB | 1,719MB | 1,653MB |

- vmstat の合計は bi 2,964MB / bo 2,018MB。t=40〜90 の平均は sy 37%、idle 8%、iowait 11%
- isureal の書き込みは、`/home/isucon/images/new` の中身 (2,983ファイル、1.3GB、平均約440KB) とほぼ同じ量。fsync をやめても書く量は変わらない。変わるのは、投稿の応答が書き込みの完了を待つかどうか
- journald は t=60 と t=80〜85 に5秒で 70〜96MB の山がある。アプリのログ (debug、1リクエストごと) と考えているが、山になる理由は確かめていない (推測)
- europe1 (176MB) と america1 (37MB) の書き込みは小さい。europe1 の go / vet / link はデプロイ時のビルドが計測の始めに重なったもの。asia2 は postgres が読み込み 935MB、書き込み 192MB

## 読み込みの出どころ (asia1 の alp と pprof)

- `GET /api/images/:id`: 10,388回、1,279MB を返している (asia の利用者の分)
- `GET /api/me/export`: 193回、352MB を返している
- europe1 / america1 から `/internal/images/:id` への分は nginx を通らないので、alp に出ない
- pprof: `readImageFile` が 8.9% (4.61s)、`exportPostsHandler` が 9.5%

## 対策の候補 (未検証)

1. 初期データの画像の読み込みを減らす。案: よく読まれる画像をメモリに置く (`imageDataCache` の大きさと当たり率を見る)、export の画像の読み方を見直す
2. journald への書き込みを減らす。アプリのログレベルの変更は施策14 (BAN の仕組み) が終わるまで触らない決まりなので、journald 側の設定 (`Storage`、`RateLimit`) で済むかを見る
3. 画像の fsync をやめる (`perf/image-save-no-fsync`): 書く量は変わらないが、投稿の応答が書き込みを待たなくなる。追試の再起動ではデータは残る。異常停止で直近の画像が消えうる

## 確認したこと / していないこと

- 確認した
  - 4台の `pidstat_disk.txt` (1秒ごと) を、全体と t=40〜90 で集計した
  - asia1 の `/home/isucon/images` の大きさとファイル数、メモリの量 (計測後に読むだけ)
  - asia1 の vmstat、alp、pprof の該当する行
- 確認していない
  - timeline/report.txt (行動分析) と、DB の slow.txt。今回の目的から外れるので読んでいない
  - journald の山の原因
  - `imageDataCache` の当たり率
  - ポータルの Stdout
