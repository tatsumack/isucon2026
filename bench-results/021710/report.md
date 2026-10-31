# Benchmark report: PR #20 deployed

## Capture

- Current: `021710` (Asia 1: `021709`, Europe 1: `021712`, America 1: `021708`)
- Comparison baseline: `021022`
- Deployed revision on all four hosts: `9fda8db` (`codex/integrate-open-prs`)
- Capture duration: 70 seconds
- Benchmark score / PASS status: not included in the captured host metrics

The baseline ran revision `a81e21a`, before the PR #20 integration. Because PR #20 combines several changes, this comparison verifies the integrated result; it cannot independently attribute every change to one PR.

## Summary

PR #20 shows a clear improvement under substantially higher request volume. The largest verified changes are:

- Posting-window reads changed from a heavily contended `FOR UPDATE` to `FOR SHARE`: mean DB time fell from 776.29 ms to 10.75 ms (-98.6%), despite calls increasing from 152 to 263.
- The registration path's `LOCK TABLE users` disappeared completely. It previously consumed 35.95 seconds over 51 calls.
- Asia post latency fell by 41%, while completed posts nearly doubled.
- Feed latency improved in every region; the remote regions improved by about 62%.
- Image-signing CPU was roughly halved while image traffic increased by 65%.
- App and DB CPU both gained headroom.

Correctness and the official result still need to be confirmed from the benchmark score and PASS/FAIL output.

## Endpoint comparison

### Asia

| Endpoint | Baseline | PR #20 | Change |
|---|---:|---:|---:|
| Posts | 105 calls, avg 1.155 s, p95 2.300 s | 206 calls, avg 0.681 s, p95 1.354 s | calls +96%; avg -41%; p95 -41% |
| Feeds | 812 calls, avg 0.192 s | 1,316 calls, avg 0.139 s | calls +62%; avg -28% |
| Images | 1,212 calls, avg 0.180 s | 1,997 calls, avg 0.136 s | calls +65%; avg -24% |
| Register | 30 calls, avg 0.482 s | 77 calls, avg 0.229 s | calls +157%; avg -52% |
| Export | 19 calls, avg 1.188 s | 33 calls, avg 0.747 s | avg -37% |
| Admin posting window | 15 calls, avg 1.112 s, p95 2.858 s | 14 calls, avg 0.907 s, p95 3.955 s | avg -18%; tail worsened |

Friend requests, memories, login, posting-window reads, and user search processed much more traffic. Their mean latency was flat to moderately higher, so they remain worth watching, but throughput increased enough that the overall result is positive.

### Europe

| Endpoint | Baseline | PR #20 | Change |
|---|---:|---:|---:|
| Posts | 24 calls, avg 4.026 s, p95 7.394 s | 29 calls, avg 2.237 s, p95 3.052 s | avg -44%; p95 -59% |
| Feeds | 30 calls, avg 1.412 s | 59 calls, avg 0.537 s | calls +97%; avg -62% |
| Register | 5 calls, avg 3.921 s | 5 calls, avg 2.556 s | avg -35% |
| Images | 60 calls, avg 0.821 s | 99 calls, avg 0.766 s | calls +65%; avg -7% |

### America

| Endpoint | Baseline | PR #20 | Change |
|---|---:|---:|---:|
| Posts | 15 calls, avg 2.934 s, p95 5.070 s | 22 calls, avg 2.056 s, p95 2.513 s | avg -30%; p95 -50% |
| Feeds | 13 calls, avg 1.473 s | 29 calls, avg 0.550 s | calls +123%; avg -63% |
| Register | avg 4.619 s | avg 2.137 s | avg -54% |
| Images | avg 0.706 s | avg 0.717 s | approximately flat |

The remote-region improvements are especially meaningful: the feed improvement is not merely an Asia-local CPU effect.

## Resource usage

### Asia application

- `isureal` CPU: 93.33% -> 69.73% (-25%)
- Host idle CPU: 48.4% -> 59.7%
- Go CPU profile samples: 64.64 s -> 48.06 s
- Image-signing cumulative CPU: 31.99 s -> 12.80 s
- SHA work fell from 24.25 s flat to 12.61 s flat, even though image requests rose from 1,212 to 1,997.

JPEG decoding is now one of the largest application CPU consumers (14.77 s cumulative), which is consistent with completing 206 posts rather than 105.

### Remote applications

- Europe app CPU: 4.50% -> 3.46%
- America app CPU: 4.73% -> 2.63%

### Database

- User CPU: 38.0% -> 32.9%
- System CPU: 19.7% -> 15.6%
- Idle CPU: 40.3% -> 46.4%
- I/O wait: 2.5% -> 5.6%

The DB has more CPU headroom, but the increased I/O-wait ratio should be watched as throughput rises.

## SQL findings

### Verified improvements

1. Posting-window lookup
   - Baseline `FOR UPDATE`: 152 calls, 117.996 s total, 776.29 ms mean.
   - PR #20 `FOR SHARE`: 263 calls, 2.826 s total, 10.75 ms mean.
   - Mean improved 98.6%; total time improved 97.6% despite 73% more calls.

2. Registration locking
   - `LOCK TABLE users` was 51 calls / 35.95 s in the baseline.
   - It is absent after PR #20.
   - Registration latency improved 35-54% in all regions while Asia completed 2.6 times as many registrations.

3. Feed query
   - Endpoint latency improved in every region, with Europe and America about 62% faster.

4. Image delivery
   - The image authorization query averaged 5.02 ms.
   - Image data fetch averaged 9.70 ms and ran for 1,744 of 2,172 authorization calls.
   - The observed combined DB work is about 14.7 ms per miss, versus 31.5 ms for the former single query. The 80% miss ratio includes cache warm-up and many distinct images.

## Remaining bottlenecks

1. User row locking is now the dominant DB cost.
   - `SELECT ... FROM users ... FOR NO KEY UPDATE`: 2,078 calls, 99.98 s total, 48.11 ms mean, 40.7% of measured SQL time.
   - Mean latency is 41% better than the baseline, but volume nearly doubled, so it remains the primary serialization point.

2. User search scans too many rows.
   - 190 calls returned/scanned 939,173 rows, taking 18.37 s total and 96.68 ms mean.
   - Mean improved 37%, but row volume remains high.

3. Notification bulk insert has a long tail.
   - 14 calls, 8.79 s total, 627.72 ms mean, 24,622 rows.
   - Baseline mean was 435.39 ms. This may be contention or run-to-run variance and corresponds with the worse p95 for admin posting-window updates.

4. Advisory-lock waits are visible.
   - 101 calls, 14.56 s total, 144.12 ms mean.
   - The call count exactly matches the observed registrations plus admin posting-window operations, so this appears to be intentional serialization rather than the removed table lock.

5. Export, JPEG decode, and DB I/O are the next scaling limits.
   - Export improved but still averaged 0.747 s in Asia.
   - JPEG decode has become prominent as post throughput increased.
   - DB I/O wait rose from 2.5% to 5.6%.

## Assessment

Subject to a successful benchmark result, PR #20 is a material performance improvement over the baseline. The integrated changes remove the previously dominant posting-window and table-lock stalls and improve both local and cross-region throughput. The next optimization should target the `users ... FOR NO KEY UPDATE` path; it now accounts for more than twice the SQL time of any other individual query.

