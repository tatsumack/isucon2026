# ベンチ #15626 (画像署名の計算を asia1 から europe1 / america1 へ移す、1回目)

- ブランチ: `tatsumack/画像asiaから外す` (`9bee65d`)。アプリ3台にデプロイ
- 結果: **PASS / score 75413**。メッセージ: `america の投稿期間を開けません: POST /api/admin/posting-window: 応答がタイムアウトしました`。グランプリ: アジア10人、ヨーロッパ125人、アメリカ0人
- 計測の欠け: 計測 (05:02:24 開始) がベンチ (05:04:40 開始) とずれた
  - pprof / alp / pidstat / slow.txt は、ベンチの最初の約14秒分しか含まない
  - ベンチ全体を含むもの: 各ホストの `vmstat_full.txt` (開始時刻は `vmstat_full_start.txt`)、asia2 の `slow_cumulative.txt` (05:02:24 以降の累計)、`timeline/report.txt`
- 分析は同じブランチの2回目 #15916 (`bench-results/15916/report.md`) を参照
