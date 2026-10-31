#!/usr/bin/env python3
"""ベンチマーカーの行動分析 (docs/benchmarker-analysis.md の手順2・3)

使い方: bench-timeline.py <出力ディレクトリ> <region>=<access.log> [...]
  例: bench-timeline.py bench-results/1530 asia=asia1.log europe=europe1.log america=america1.log
  手元で実行する。access.logは各アプリサーバから取ってくる (サーバ上では動かさない)
  出力: <出力ディレクトリ>/report.txt (集計) と timeline.tsv (ユーザー別の時系列)

ユーザーはX-Client-IP (ログのhost) で識別する。ベンチマーカーはユーザーごとに別のIPを使うため、
ログイン前のリクエストもまとめられる。user_id (X-User-Id) との対応も確認する。
"""
import re
import sys
from collections import Counter, defaultdict
from itertools import permutations

UUID = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
DATE = re.compile(r"^\d{4}-\d{2}(-\d{2})?$")
NUM = re.compile(r"^\d+$")
LONG_ID = re.compile(r"^[0-9A-Za-z_-]{20,}$")
# 各集計で表示する件数
TOP = 40


def normalize(uri):
    path, _, query = uri.partition("?")
    segs = []
    for s in path.split("/"):
        if UUID.match(s) or NUM.match(s) or LONG_ID.match(s):
            s = ":id"
        elif DATE.match(s):
            s = ":date"
        segs.append(s)
    path = "/".join(segs)
    if query:
        keys = sorted({kv.split("=", 1)[0] for kv in query.split("&") if kv})
        path += "?" + "&".join(keys)
    return path


def parse(region, filename):
    rows = []
    with open(filename, encoding="utf-8", errors="replace") as f:
        for line in f:
            f_ = {}
            for kv in line.rstrip("\n").split("\t"):
                k, _, v = kv.partition(":")
                f_[k] = v
            if "msec" not in f_:
                continue
            try:
                end = float(f_["msec"])
                reqtime = float(f_.get("reqtime") or 0)
            except ValueError:
                continue
            uid = f_.get("user_id", "")
            rows.append({
                # msecはレスポンス完了時刻。SSE (/api/notifications) などで順序が崩れないよう開始時刻で並べる
                "start": end - reqtime,
                "end": end,
                "reqtime": reqtime,
                "region": region,
                "host": f_.get("host", "") or "-",
                "uid": "" if uid in ("", "-") else uid,
                "method": f_.get("method", ""),
                "uri": f_.get("uri", ""),
                "ep": f_.get("method", "") + " " + normalize(f_.get("uri", "")),
                "status": f_.get("status", ""),
                "ua": f_.get("ua", ""),
                "country": f_.get("country", ""),
            })
    return rows


def pct(values, p):
    if not values:
        return 0
    values = sorted(values)
    return values[min(len(values) - 1, int(len(values) * p))]


def window_pass_rate(rows, out):
    """投稿期間ごとの「投稿 → 友達フィード取得」の完了率 (docs/action-plan-makino.md の M2)

    加点条件 (manual.md): 投稿が受理され、その投稿期間が終了する前に友達フィードの検証に成功すること。
    - 期間: POST /api/admin/posting-window (201) の完了時刻から、同じリージョンの次の期間の開始まで。
      期間の開始時刻 (startsAt = 前の期間の ended_at) はロック待ちの後にトランザクション内で決まり、
      #2540 では admin のリクエストが最大3.4秒かかったため、リクエストの開始ではなく完了を境界にする
      各リージョンの最後の期間は終了時刻がログに無いので別に集計する (最終日で1件20点)
    - リージョン: 管理者のリクエストはすべて asia1 に来て、region はリクエストの body にしか無い。
      SSE (GET /api/notifications) は自分のリージョンの通知を1件送ると閉じるので、リージョンを巡回する順番
      (6通り) のうち、期間開始から3秒以内に閉じた SSE が一番多い順番で割り当てる (推定。同数なら asia → europe → america)。
      期間は3秒ごとに開くので、通知が3秒以上遅れると次の期間 (別リージョン) に数えられ、1位と2位の差が縮む
    - 投稿: 期間内に始まった POST /api/posts (201)。host (ユーザー) × 期間ごとに最初の1件
    - 完了: 同じ host・同じリージョンの GET /api/feeds/friends (200) が投稿の後に始まり、期間の終了前に終わったもの
    """
    print("\n## 投稿期間ごとの投稿〜友達フィード完了 (M2)", file=out)
    admins = sorted((r for r in rows if r["ep"] == "POST /api/admin/posting-window" and r["status"] == "201"),
                    key=lambda r: r["t"])
    starts = [r["t"] + r["reqtime"] for r in admins]
    if not starts:
        print("POST /api/admin/posting-window (201) が無い", file=out)
        return
    regions = [g for g in ("asia", "europe", "america") if any(r["region"] == g for r in rows)]
    t_last = max(r["t"] + r["reqtime"] for r in rows)

    # SSE が通知を受け取って閉じた時刻。60秒で切れたもの (通知なし) は除く
    sse = defaultdict(list)
    for r in rows:
        if r["ep"] == "GET /api/notifications" and r["status"] == "200" and r["reqtime"] < 59:
            sse[(r["region"], r["host"])].append(r["t"] + r["reqtime"])
    sse_by_region = defaultdict(list)
    for (g, _), ends in sse.items():
        sse_by_region[g] += ends

    def score(order):
        return sum(1 for i, t in enumerate(starts) for e in sse_by_region[order[i % len(order)]] if t <= e < t + 3)
    scores = sorted(((score(o), o) for o in permutations(regions)), key=lambda x: -x[0])
    order = max(scores, key=lambda x: (x[0], x[1] == tuple(regions)))[1]
    print("期間の割り当て: 巡回の順番ごとの、期間開始から3秒以内に閉じた SSE の数 (多いものを採用)", file=out)
    for n, o in scores:
        print(f"  {n:5d}  {' → '.join(o)}{'  ← 採用' if o == order else ''}", file=out)
    windows = [{"region": order[i % len(order)], "start": t, "admin": r["reqtime"]}
               for i, (t, r) in enumerate(zip(starts, admins))]
    for w in windows:
        nxt = [v["start"] for v in windows if v["region"] == w["region"] and v["start"] > w["start"]]
        w["end"] = min(nxt) if nxt else None

    by_host = defaultdict(list)
    for r in rows:
        by_host[(r["region"], r["host"])].append(r)
    posts = []
    for (g, host), rs in by_host.items():
        for w in windows:
            if w["region"] != g:
                continue
            end = w["end"] if w["end"] is not None else t_last
            p = next((r for r in rs if r["ep"] == "POST /api/posts" and r["status"] == "201"
                      and w["start"] <= r["t"] < end), None)
            if p is None:
                continue
            p_end = p["t"] + p["reqtime"]
            feed = next((r for r in rs if r["ep"] == "GET /api/feeds/friends" and r["status"] == "200"
                         and r["t"] >= p_end and r["t"] + r["reqtime"] < end), None)
            notified = [e for e in sse.get((g, host), []) if w["start"] <= e <= p["t"]]
            posts.append({
                "window": w, "post_start": p["t"], "post_end": p_end,
                "feed_end": feed["t"] + feed["reqtime"] if feed else None,
                "notified": min(notified) if notified else None,
            })

    print("admin = POST /api/admin/posting-window の reqtime、lag = 期間開始からそのリージョンで最初に SSE が閉じるまで", file=out)
    print(f"{'start':>7} {'end':>7}  {'region':<8} {'admin':>5} {'lag':>5}  {'posts':>5} {'done':>5}", file=out)
    suspicious = 0
    for w in windows:
        lag = min((e - w["start"] for e in sse_by_region[w["region"]]
                   if w["start"] <= e < (w["end"] if w["end"] is not None else t_last)), default=None)
        if lag is None or lag > 6:
            suspicious += 1
        ps = [p for p in posts if p["window"] is w]
        end = f"{w['end']:7.2f}" if w["end"] is not None else "   last"
        lag_s = f"{lag:5.2f}" if lag is not None else "    -"
        print(f"{w['start']:7.2f} {end}  {w['region']:<8} {w['admin']:5.2f} {lag_s}  {len(ps):5d} {sum(1 for p in ps if p['feed_end'] is not None):5d}", file=out)
    print(f"lag が無い・6秒超の期間 (割り当てが疑わしい): {suspicious} / {len(windows)}", file=out)

    def row(label, ps):
        done = [p for p in ps if p["feed_end"] is not None]
        rate = f"{100 * len(done) / len(ps):3.0f}%" if ps else "   -"
        notify = [p["notified"] - p["window"]["start"] for p in ps if p["notified"] is not None]
        think = [p["post_start"] - p["notified"] for p in ps if p["notified"] is not None]
        to_post = [p["post_start"] - p["window"]["start"] for p in ps]
        to_feed = [p["feed_end"] - p["post_start"] for p in done]
        q = lambda v: f"{pct(v, .5):5.2f}/{pct(v, .9):5.2f}" if v else "    -/    -"
        print(f"{label:<14} {len(ps):5d} {len(done):5d} {rate}  {q(notify)}  {q(think)}  {q(to_post)}  {q(to_feed)}", file=out)

    print("\n### リージョン別 (秒は p50/p90。通知 = 期間開始→本人の SSE が閉じる、反応 = SSE→投稿開始、"
          "開始→投稿 = 期間開始→投稿開始、投稿→フィード = 投稿開始→友達フィード完了)", file=out)
    print(f"{'region':<14} {'posts':>5} {'done':>5} {'rate':>4}  {'通知':>11}  {'反応':>11}  {'開始→投稿':>11}  {'投稿→フィード':>11}", file=out)
    for g in regions:
        row(g, [p for p in posts if p["window"]["region"] == g and p["window"]["end"] is not None])
    row("(all)", [p for p in posts if p["window"]["end"] is not None])
    print(f"最後の期間 (終了時刻不明。ログの最後 t={t_last:.2f} までに完了したもの)", file=out)
    for g in regions:
        row(f"{g} last", [p for p in posts if p["window"]["region"] == g and p["window"]["end"] is None])


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    out_dir = sys.argv[1]
    rows = []
    for arg in sys.argv[2:]:
        region, _, filename = arg.partition("=")
        rows += parse(region, filename)
    if not rows:
        print("no rows (log_formatにmsecが無い?)")
        sys.exit(1)
    rows.sort(key=lambda r: r["start"])
    t0 = rows[0]["start"]
    init = [r["start"] for r in rows if r["ep"] == "POST /api/initialize"]
    t_init = init[-1] if init else t0
    for r in rows:
        r["t"] = r["start"] - t_init

    # ユーザー = host (X-Client-IP)
    by_user = defaultdict(list)
    for r in rows:
        by_user[r["host"]].append(r)

    with open(f"{out_dir}/timeline.tsv", "w") as f:
        f.write("host\tuser_id\tt\tregion\tmethod\turi\tstatus\treqtime\n")
        for host in sorted(by_user, key=lambda h: by_user[h][0]["start"]):
            for r in by_user[host]:
                f.write(f"{host}\t{r['uid']}\t{r['t']:.3f}\t{r['region']}\t{r['method']}\t{r['uri']}\t{r['status']}\t{r['reqtime']:.3f}\n")

    out = open(f"{out_dir}/report.txt", "w")

    def section(title):
        print(f"\n## {title}", file=out)

    print(f"# ベンチマーカー行動分析\nrequests={len(rows)} users(host)={len(by_user)} "
          f"t=0 は最後の POST /api/initialize (見つかった数: {len(init)})", file=out)

    section("リージョン別リクエスト数 / 国コード / UA")
    print(Counter(r["region"] for r in rows).most_common(), file=out)
    print(Counter(r["country"] for r in rows).most_common(10), file=out)
    for ua, n in Counter(r["ua"] for r in rows).most_common(10):
        print(f"{n:8d}  {ua}", file=out)

    section("host と user_id の対応")
    host_uids = {h: {r["uid"] for r in rs if r["uid"]} for h, rs in by_user.items()}
    uid_hosts = defaultdict(set)
    for h, uids in host_uids.items():
        for u in uids:
            uid_hosts[u].add(h)
    print(f"user_idが付かないhost: {sum(1 for u in host_uids.values() if not u)}", file=out)
    print(f"複数user_idを持つhost: {sum(1 for u in host_uids.values() if len(u) > 1)}", file=out)
    print(f"複数hostから来るuser_id: {sum(1 for h in uid_hosts.values() if len(h) > 1)}", file=out)
    multi_region = sum(1 for rs in by_user.values() if len({r['region'] for r in rs}) > 1)
    print(f"複数リージョンにアクセスするhost: {multi_region}", file=out)

    section("フェーズ別のエンドポイント (初期化前 / 初期化後10秒 / 負荷走行)")
    phases = [("before_init", lambda t: t < 0), ("init+10s", lambda t: 0 <= t < 10), ("load", lambda t: t >= 10)]
    for name, cond in phases:
        c = Counter(r["ep"] for r in rows if cond(r["t"]))
        print(f"### {name}: {sum(c.values())} req", file=out)
        for ep, n in c.most_common(15):
            print(f"{n:8d}  {ep}", file=out)

    section("エンドポイント別 件数・ステータス・reqtime")
    by_ep = defaultdict(list)
    for r in rows:
        by_ep[r["ep"]].append(r)
    print(f"{'count':>8} {'users':>6} {'p50':>7} {'p99':>7}  statuses  endpoint", file=out)
    for ep, rs in sorted(by_ep.items(), key=lambda x: -len(x[1])):
        st = ",".join(f"{s}:{n}" for s, n in Counter(r["status"] for r in rs).most_common(4))
        rt = [r["reqtime"] for r in rs]
        print(f"{len(rs):8d} {len({r['host'] for r in rs}):6d} {pct(rt, .5):7.3f} {pct(rt, .99):7.3f}  {st}  {ep}", file=out)

    section("最初の5リクエスト (ユーザーの入口)")
    c = Counter(" > ".join(r["ep"] for r in rs[:5]) for rs in by_user.values())
    for seq, n in c.most_common(15):
        print(f"{n:6d}  {seq}", file=out)

    section(f"遷移 (同一ユーザーで A の次に B) 上位{TOP}")
    bigram = Counter()
    for rs in by_user.values():
        for a, b in zip(rs, rs[1:]):
            bigram[(a["ep"], b["ep"])] += 1
    for (a, b), n in bigram.most_common(TOP):
        print(f"{n:8d}  {a}  ->  {b}", file=out)

    section("A の次に来るもの (件数の多いエンドポイント上位15について、後続の上位5)")
    nexts = defaultdict(Counter)
    for (a, b), n in bigram.items():
        nexts[a][b] += n
    for ep, _ in sorted(by_ep.items(), key=lambda x: -len(x[1]))[:15]:
        total = sum(nexts[ep].values()) or 1
        print(f"### {ep}", file=out)
        for b, n in nexts[ep].most_common(5):
            print(f"  {100 * n / total:5.1f}%  {b}", file=out)

    section("同一ユーザー・同一エンドポイントのアクセス間隔 (秒) → ポーリング周期")
    gaps = defaultdict(list)
    for rs in by_user.values():
        last = {}
        for r in rs:
            if r["ep"] in last:
                gaps[r["ep"]].append(r["start"] - last[r["ep"]])
            last[r["ep"]] = r["start"]
    print(f"{'n':>8} {'p10':>7} {'p50':>7} {'p90':>7}  endpoint", file=out)
    for ep, g in sorted(gaps.items(), key=lambda x: -len(x[1]))[:TOP]:
        print(f"{len(g):8d} {pct(g, .1):7.2f} {pct(g, .5):7.2f} {pct(g, .9):7.2f}  {ep}", file=out)

    section("書き込み直後の読み取り (POST/PUT/PATCH/DELETE の次の GET と間隔)")
    after_write = defaultdict(Counter)
    for rs in by_user.values():
        for a, b in zip(rs, rs[1:]):
            if a["method"] != "GET" and b["method"] == "GET":
                after_write[a["ep"]][(b["ep"], b["start"] - a["end"] < 1)] += 1
    for ep, c in sorted(after_write.items(), key=lambda x: -sum(x[1].values()))[:15]:
        print(f"### {ep}", file=out)
        for (b, within1s), n in c.most_common(4):
            print(f"  {n:6d}  {'<1s ' if within1s else '>=1s'}  {b}", file=out)

    section("エラー (4xx/5xx/499) の後の行動")
    after_err = defaultdict(Counter)
    for rs in by_user.values():
        for i, r in enumerate(rs):
            if r["status"] >= "400":
                nxt = rs[i + 1] if i + 1 < len(rs) else None
                key = "(end)" if nxt is None else ("same(retry)" if nxt["ep"] == r["ep"] else nxt["ep"])
                after_err[f"{r['status']} {r['ep']}"][key] += 1
    for k, c in sorted(after_err.items(), key=lambda x: -sum(x[1].values()))[:20]:
        print(f"### {sum(c.values())}  {k}", file=out)
        for b, n in c.most_common(4):
            print(f"  {n:6d}  {b}", file=out)

    section("ユーザーごとのリクエスト数・セッション長 (秒)・最後のリクエスト")
    counts = [len(rs) for rs in by_user.values()]
    spans = [rs[-1]["end"] - rs[0]["start"] for rs in by_user.values()]
    print(f"requests/user p10={pct(counts, .1)} p50={pct(counts, .5)} p90={pct(counts, .9)} max={max(counts)}", file=out)
    print(f"span(s)       p10={pct(spans, .1):.1f} p50={pct(spans, .5):.1f} p90={pct(spans, .9):.1f}", file=out)
    t_end = rows[-1]["end"]
    early = [rs for rs in by_user.values() if t_end - rs[-1]["end"] > 10]
    print(f"終了10秒以上前に止まったユーザー: {len(early)} / {len(by_user)}", file=out)
    for k, n in Counter(f"{rs[-1]['status']} {rs[-1]['ep']}" for rs in early).most_common(15):
        print(f"{n:6d}  {k}", file=out)

    section("時間帯ごとのアクティブユーザー数・リクエスト数 (5秒ごと、t=初期化からの秒)")
    active = defaultdict(set)
    reqs = Counter()
    writes = defaultdict(Counter)
    for r in rows:
        b = int(r["t"] // 5) * 5
        active[b].add(r["host"])
        reqs[b] += 1
        if r["method"] != "GET":
            writes[b][r["ep"]] += 1
    for b in sorted(active):
        top = ", ".join(f"{ep.split(' ', 1)[1]}:{n}" for ep, n in writes[b].most_common(3))
        print(f"t={b:5d}  users={len(active[b]):5d}  req={reqs[b]:6d}  writes: {top}", file=out)

    section("管理者のリクエスト (投稿期間の開始タイミング)")
    for r in rows:
        if r["uid"].startswith("admin:") or "/api/admin/" in r["uri"]:
            print(f"t={r['t']:8.2f}  {r['region']}  {r['method']} {r['uri']}  {r['status']}", file=out)

    window_pass_rate(rows, out)

    out.close()
    print(f"{out_dir}/report.txt\n{out_dir}/timeline.tsv")


if __name__ == "__main__":
    main()
