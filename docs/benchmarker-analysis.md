# ベンチマーカーの行動分析

参考: 白金動物園の戦略。ベンチマーカーが「どのエンドポイントを、どの順番で、どういう条件で叩いているか」をユーザー単位で突き止める。

## 目的

`alp` 等のエンドポイント別集計では「どこが重いか」しか分からない。ユーザー単位の時系列で見て、以下を把握する。

- **典型シナリオ**: ログイン → 一覧 → 詳細 → 投稿 のような流れと、その分岐条件
- **ポーリング**: 同一ユーザーが同一 GET を何秒おきに叩くか → キャッシュ / 304 / 間引きの候補
- **書き込み後の整合性チェック**: POST 直後に GET で確認しているか → キャッシュの古さの許容度
- **離脱・負荷増加の条件**: 遅いとユーザーが去るのか、エラー時にリトライするのか、何をきっかけに並列度が上がるのか
- **スコアに効かないリクエスト**: 叩かれているが加点にならない処理

## 手順

### 1. ユーザー識別子をログに出す

nginx のログは alp で集計する都合で LTSV。既存の `log_format` に次のフィールドを追加する（alp は未知のキーを無視するので、`bench-capture.sh` はそのまま動く）。

```nginx
# 既存の LTSV フォーマットの末尾に追加する
    "\tmsec:$msec"
    "\tsession:$cookie_SESSION_NAME"
    "\tauth:$http_authorization"
    "\tuser_id:$sent_http_x_user_id";
```

- `SESSION_NAME` は実際のセッション Cookie 名に置き換える
- `msec` は時刻順に並べるため（`time` は秒単位なので同一秒内の順序が分からない）

セッションから user_id が直接取れない場合は、Go 側でレスポンスヘッダ `X-User-Id` を付けて、nginx の `$sent_http_x_user_id` で拾う。ヘッダはレスポンスを書き出す前にセットする必要があるので、ハンドラの後ではなく、認証処理で user_id を解決した直後にセットする。

```go
// 例: セッションから user_id を解決している関数の中で
c.Response().Header().Set("X-User-Id", strconv.FormatInt(userID, 10))
```

### 2. ユーザー別タイムラインを作る

user_id（無ければ session）ごとにグループ化し、時刻順に `method uri status reqtime` を並べる。

```bash
awk -F'\t' '{
  delete f
  for (i = 1; i <= NF; i++) { k = substr($i, 1, index($i, ":") - 1); f[k] = substr($i, length(k) + 2) }
  u = (f["user_id"] != "" && f["user_id"] != "-") ? f["user_id"] : f["session"]
  print u "\t" f["msec"] "\t" f["method"] "\t" f["uri"] "\t" f["status"] "\t" f["reqtime"]
}' /var/log/nginx/access.log | sort -t$'\t' -k1,1 -k2,2n > timeline.tsv
```

キー名（`method` `uri` `status` `reqtime`）は実際の `log_format` に合わせる。

URI 中の ID は正規化（`/items/123` → `/items/:id`）してから集計する。
ログイン前のリクエストは user_id が空なので session でまとめられる。同じユーザーのログイン前後をつなげたい場合は session でまとめる。

### 3. 集計する

- **遷移（bigram）**: 「A の次に B が来た回数」を数える → 典型シナリオが浮かび上がる
- **アクセス間隔**: 同一ユーザー・同一パスの間隔分布 → ポーリング周期
- **ユーザーごとのリクエスト数・セッション長** → 離脱の有無、途中で止まるユーザーの最後のリクエスト
- **ステータス別の後続行動**: 4xx/5xx やタイムアウトの後に何を叩くか
- **時間帯ごとのアクティブユーザー数**: 負荷が増えるタイミングと、その直前の出来事

### 4. マニュアルと突き合わせる

スコア計算式・加点 / 減点条件と照らし合わせて、「どのリクエストを速く・多く捌けば得か」「どこまで手を抜いてよいか」を決める。

## 注意

- ログ出力自体が負荷になる。分析が終わったら、最終ベンチの前に access log とヘッダ付与を切る。
- `auth` には認証トークンが入る。ログや分析結果をリポジトリにコミットしない。
- 初期化（`POST /initialize` 等）直後の挙動と、負荷走行中の挙動は分けて見る。

## このアプリでの実施方法 (isureal)

ブランチ `analysis/benchmarker-timeline` で、上の手順1・2・3を次のように実装している。

- **ユーザーの識別**: 認証は Cookie ではなく `Authorization: Bearer`。ベンチマーカーはユーザーごとに別の `X-Client-IP` を付ける (マニュアル「クライアントのIPアドレス」) ので、ログの `host` でまとめる。ログイン前のリクエストも同じユーザーにまとまる。`auth` (トークン) はログに出さない
- **user_id**: `SessionMiddleware` / `AdminSessionMiddleware` が `X-User-Id` を付け、nginx が `$upstream_http_x_user_id` でログに出す。`proxy_hide_header` でクライアントには返さない。管理者は `admin:<id>`
- **追加したログのフィールド**: `msec` `country` (`X-Country-Code`) `user_id`
- **集計**: `bench-timeline.py` を手元で実行する。並べる時刻は `msec - reqtime` (リクエストの開始時刻)。`/api/notifications` は SSE で reqtime が長いため

```bash
# ベンチ後、各アプリサーバのログを手元に持ってくる (deploy.sh / bench-capture.sh でログが退避・truncateされる前に)
D=bench-results/timeline-$(date +%H%M); mkdir -p $D
for h in asia1 europe1 america1; do ssh $h sudo cat /var/log/nginx/access.log > $D/$h.log; done
./bench-timeline.py $D asia=$D/asia1.log europe=$D/europe1.log america=$D/america1.log
```

生ログ (`*.log`) と `timeline.tsv` は gitignore している。`report.txt` はコミットしてよい。最終ベンチの前に、`X-User-Id` の付与と追加したログのフィールドを外す。
