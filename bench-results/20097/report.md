# ベンチ #20097 分析 (PR #55 アクションログは isureal-guard が使う行とエラーだけを出す)

- ブランチ: `perf/action-log-guard-only` (`c1bf97b`) = main (`d041437`) + #55。4台にデプロイ
- 計測: 05:57:54 UTC からの150秒 (4台、`055754`)。asia1 は `iostat` と `free` も取った。ベンチは 05:58:20〜05:59:21 (Stdout)
- 結果: **PASS / score 93801**。main の #17256 (94360、#52 nginx を含まない) から **-0.6%**、#18039 (89583、#52 + #51) から +4.7%。ベンチ1回ずつの比較なので、ぶれを含む
- 計測開始から約20秒 (ベンチ前) に asia1 で `find` が CPU 約28% を使っていた (チームの誰かの調査と思われる)。負荷の山には重なっていない

## 人数 (ポータルの Stdout)

| | 2日目 | 3日目 | 4日目 | 5日目 | グランプリ |
|---|---|---|---|---|---|
| asia #17256 | 48 | 137 | 397 | 481 | 69 |
| asia **#20097** | 48 | 138 | 394 | **709** | **683** |
| europe #17256 | 18 | 46 | 121 | 198 | 196 |
| europe **#20097** | 18 | 43 | 98 | **99** | 92 |
| america #17256 | 14 | 38 | 82 | 135 | 134 |
| america **#20097** | 14 | 35 | 62 | **62** | 55 |

- asia は5日目に 709人まで増え、グランプリも 683人 (これまでで最多)
- europe / america は4日目以降に人数が止まった。#18039 と同じ形

## 概況

- **ログの処理が asia1 から消えた** (負荷の山、1コア=100%)
  - #17256: journalctl 9%、systemd-journal 4% (#18039 では合計 26%)
  - #20097: systemd-journal 0%。journalctl・rsyslogd は上位に出ない
- **guard の BAN は続いている**: ベンチ中 185件 (login 74、invalid_signature 82、invalid_image 29)
- **asia1 は CPU で張り付いたまま**: t=55〜90秒で us 36.3 / sy 42.1 / idle 8.6 / wa 13.3。isureal 93%、nginx 20%、kswapd + kcompactd 16%
  - 空いた分は asia の処理の増加で使われた: asia1 の投稿 1274 → 1949回、feeds 3856 → 6394回、memories 2153 → 3300回
- ディスクは詰まっていない: 読み 73MB/s・書き 36MB/s、待ち時間 1.4〜1.7ms (#19579 は約10ms)
- asia1 の空きメモリの最小は 124MB。kswapd / kcompactd は #17256 (7%) より多い 16%
- europe / america から asia1 へ転送される API が遅い: europe1 feeds 0.252s → 0.573s、memories 0.330s → 0.785s、posting-window/current 0.254s → 0.613s

## 評価

- ログの変更でスコアは下がっていない (-0.6% はぶれの範囲と考える)
- 狙った asia1 の CPU (ログの処理、約0.26コア) は空いた。ただし asia の人数が増えて、その分を使った
- 残る課題は前回と同じ: asia1 に asia の処理が集中すると、europe / america (得点1.5倍) の転送された API が遅くなり、人数が止まる

## 確認したこと / していないこと

- 確認した: 4台の vmstat、asia1 の pidstat・iostat・free、3台の alp、guard の BAN のログ、ポータルの Stdout
- 確認していない: pprof の詳細、asia2 の slow.txt、timeline と M2
