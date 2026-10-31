# ベンチ分析 #14832 (europe1 / america1 のログイン・登録・プロフィール更新を asia1 へ転送)

- ブランチ: `perf/delegate-auth-and-me` (`d7eaae2`、PR #46)。main (`b9209de`、PR #43 を含む) に、ログイン・登録・ログアウト・`PATCH /api/me`・プロフィール画像の転送だけを足したもの。4台にデプロイ
- 計測: 04:54:44 UTC からの150秒 (4台)。結果ディレクトリ: asia1 / europe1 `045444`、america1 `045445`、asia2 `045447`
- 結果: **PASS / score 55227**。#12732 (main と同じ構成) は 52133 (**+5.9%**)、#11801 は 54926 (+0.5%)。ベンチ1回ずつの比較なので、ぶれを含む
- 計測の欠け: なし (4台とも pprof / alp / vmstat / pidstat / slow.txt あり)

## 概況

- **狙いの API は europe1 / america1 で半分以下になった** (平均。#12732 → #14832)

  | API | europe1 | america1 |
  |---|---|---|
  | `POST /api/auth/login` | 0.586s → **0.379s** (230 → 297回) | 0.613s → **0.393s** (130 → 170回) |
  | `POST /api/auth/register` | 0.565s → **0.275s** | 0.627s → **0.253s** |
  | `POST /api/auth/logout` | 0.664s → **0.390s** | 0.674s → **0.441s** |
  | `PATCH /api/me` | 0.653s → **0.293s** | 0.665s → **0.345s** |
  | `PUT /api/me/profile-image` | 0.858s → **0.359s** | 0.940s → **0.272s** |

  - ログインに 0.38s 残るのは、asia1 との往復 (約100ms) と asia1 側の処理 (asia1 のログインは平均 0.214s。CPU が張り付いた時間帯に伸びる) のため (推測)
- **asia1 の CPU はほぼ増えていない**
  - asia1 の pprof で、転送してきた処理の合計: `loginHandler` 0.74s (0.92%、#12732 は 0.68s)、`putProfileImageHandler` 3.25s (4.04%、#12732 は 3.83s)。ほかは上位に出ない
  - isureal の CPU (pidstat 平均): 53.3% → 55.0%。idle 5% 未満の秒数: 42秒 → 48秒。asia1 の総リクエスト (nginx) は 27,731 → 28,625 で、負荷の量の差の範囲と考えられる
- europe / america の利用者のリクエストが増えた: europe1 4,788 → 5,134 (+7%)、america1 2,752 → 2,959 (+8%)。asia1 は +3%。ログインが速くなった効果か、ぶれかは切り分けられない
- ボトルネックの所在は前回と同じ。asia1 の CPU (画像署名が pprof の 62.5%) と、DB の上位クエリ

## ベンチマーカーの行動 (timeline/report.txt より)

- 流れは前回と同じ。通知 (SSE) → `GET /api/posting-window/current` → `POST /api/posts` → `GET /api/feeds/friends`
- 投稿期間は 3秒ごとに asia → europe → america の順に開く。最後の期間は3リージョンを同時に開く (t=67.45)
- 投稿期間の作成 (admin の reqtime): 最大 1.42s (asia、t=44.88)。#12732 は最大 1.71s
- 加点されない 4xx は前回と同じ傾向: `POST /api/auth/login` の 401 が 783件 (asia1 のみ。europe1 / america1 の 4xx は 0件)、`PUT /api/me/profile-image` の 400 が 106件と 502 が 2件 (asia1)
- 転送に変えたことで、ベンチマーカーの行動に変化は見えない (ログインの後は Memories と画像の取得)

## 期間内の投稿〜友達フィード完了率 (timeline/report.txt の M2 より)

| リージョン | 投稿数 | 期間内に完了 | 前回 (#12732) | 開始→投稿 p50 | 投稿→フィード p50 |
|---|---|---|---|---|---|
| asia | 654 | 630 (96%) | 604 / 639 (95%) | 2.39s | 1.00s |
| europe | 171 | 166 (97%) | 140 / 146 (96%) | 2.30s | 1.30s |
| america | 112 | 107 (96%) | 102 / 104 (98%) | 2.69s | 1.27s |
| 最後の期間 (3リージョン計) | 360 | 360 (100%) | 336 / 339 (99%) | - | - |
| (全体、最後の期間を除く) | 937 | 903 (96%) | 846 / 889 (95%) | 2.40s | 1.08s |

- 期間の割り当て: asia → europe → america (1001)。2位の asia → america → europe は 825 で、差は十分ある
- 割り当てが疑わしい期間: 18期間中1つ (america の最後の期間。reqtime 1.27s)
- europe / america の投稿数は 146 → 171、104 → 112 に増えた。america の完了率は 98% → 96% に下がったが、件数が少なく (未完了 2 → 5件) ぶれの範囲と考える。反応 (SSE → 投稿開始) の p50 が 1.57s → 2.11s に伸びている

## ボトルネック上位

前回 (#12732) から順位は変わらない。

### 1. asia1 の CPU (画像署名)
- 根拠: pprof の 62.5% が `computeImageSignature`。呼び出し元は `listMemories` (42.0%)、`newPostResponse` (35.5%)、`newRealMojiResponse` (26.6%)。idle 5% 未満が 48秒
- 原因箇所: `go/image_signature.go` の `computeImageSignature`
- 対策案: 署名の計算を europe1 / america1 に任せる (別担当が対応中)
- 期待効果: 大

### 2. 画像の閲覧権限の確認クエリ (DB)
- 根拠: 8,781回、平均 1.64ms、合計 14.4秒、DB 時間の 15.0% (#12732: 8,514回 / 1.86ms / 15.2%)
- 原因箇所: `go/image_api.go` の配信してよいかの確認
- 対策案: 削除後5秒の決まりの中で、短い有効期間のキャッシュを置く
- 期待効果: 小〜中。DB は idle 76% で待ちの原因になっていない

### 3. 友達申請の承認のロック (DB)
- 根拠: `SELECT id FROM users WHERE id IN ($1, $2) AND status=$3 ORDER BY id FOR NO KEY UPDATE` が 4,208回、平均 2.29ms、合計 9.6秒、DB 時間の 10.0% (#12732: 3,846回 / 2.77ms)
- 期待効果: 小

### 4. ユーザー検索 (DB)
- 根拠: 1,112回、平均 7.14ms、合計 7.9秒、DB 時間の 8.2% (#12732: 1,024回 / 7.51ms)
- 期待効果: 小

### 5. export のクエリ (DB)
- 根拠: 165回、平均 31.48ms、DB 時間の 5.4% (#12732: 151回 / 38.43ms)。asia1 の `GET /api/me/export` は平均 0.970s
- 期待効果: 小

## 確認したこと / していないこと

- 確認した
  - 4台の vmstat・pidstat、3台の alp、asia1 の pprof (転送した処理の CPU)、DB の slow.txt を #12732 と比較した
  - timeline/report.txt (M2、admin の reqtime、4xx)
- 確認していない
  - ポータルの Stdout (リージョン・日ごとの人数、グランプリの人数)
  - IP BAN の仕組み (`isureal-guard`) への影響。ログイン・登録の行動ログの username が、転送元では null になる。BAN が効いているか (`ip_bans` の件数) は見ていない
  - 同じブランチでもう1回ベンチを流したときのぶれ
