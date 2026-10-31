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

    out.close()
    print(f"{out_dir}/report.txt\n{out_dir}/timeline.tsv")


if __name__ == "__main__":
    main()
