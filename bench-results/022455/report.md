# Benchmark report: PR #24 deployed

## Capture

- Current: `022455` (Europe/America: `022454`)
- Comparison baseline: PR #20 capture `021710`
- Deployed revision on all four hosts: `f1ee00b` (`codex/integrate-remaining-prs`)
- Capture duration: 70 seconds
- Benchmark score / PASS status: not included in the captured host metrics

PR #24 integrates the write-path round-trip reduction and SQL-side user-search filtering. The current run reached a substantially higher load than the baseline, so latency changes must be interpreted together with completed request counts and resource saturation.

## Summary

PR #24 is effective. User search became dramatically cheaper, and write APIs that cross regions improved most strongly. The faster paths allowed the benchmark to progress further: Asia post count rose from 206 to 354 and feed count from 1,316 to 2,404. Consequently, Asia application CPU reached saturation and DB utilization increased. The higher latency of some unrelated endpoints is consistent with this higher offered load rather than an isolated regression in PR #24.

Correctness and the official score still need confirmation from the benchmark PASS/FAIL and score output.

## User search

The old query fetched thousands of candidate rows per request and filtered them in Go. The new query filters normalized search columns, blocked users, and the result limit in PostgreSQL.

### SQL

| Metric | PR #20 baseline | PR #24 | Change |
|---|---:|---:|---:|
| Calls | 190 | 348 | +83% |
| Total DB time | 18.37 s | 6.42 s | -65% |
| Mean DB time | 96.68 ms | 18.44 ms | -81% |
| Rows | 939,173 | 368 | -99.96% |
| Rows per call | 4,943 | 1.06 | -99.98% |

Despite almost twice as many searches, total database time fell by about two thirds.

### Endpoint latency

| Region | PR #20 baseline | PR #24 | Change |
|---|---:|---:|---:|
| Asia | 0.261 s (151 calls) | 0.161 s (317 calls) | avg -38%; calls +110% |
| Europe | 0.732 s (18 calls) | 0.331 s (14 calls) | avg -55% |
| America | 0.814 s (20 calls) | 0.336 s (17 calls) | avg -59% |

## Write-path round-trip reduction

### Friend-request creation

| Region | PR #20 baseline | PR #24 | Change |
|---|---:|---:|---:|
| Asia | 0.307 s (368 calls) | 0.242 s (661 calls) | avg -21%; calls +80% |
| Europe | 1.512 s (38 calls) | 0.866 s (24 calls) | avg -43% |
| America | 1.605 s (32 calls) | 0.843 s (28 calls) | avg -47% |

### Friend-request actions

| Region | PR #20 baseline | PR #24 | Change |
|---|---:|---:|---:|
| Asia | 0.264 s (162 calls) | 0.210 s (320 calls) | avg -20%; calls +98% |
| Europe | 1.998 s (9 calls) | 1.281 s (30 calls) | avg -36%; calls +233% |
| America | 2.236 s (6 calls) | 1.137 s (36 calls) | avg -49%; calls +500% |

### Other affected writes

- Asia comment creation: 0.382 s over 14 calls -> 0.197 s over 100 calls (avg -48%, calls +614%).
- Asia follow creation: 0.327 s -> 0.273 s (avg -17%).
- Asia block creation: 0.445 s over 6 calls -> 0.081 s over 8 calls (avg -82%).
- Asia RealMoji creation stayed approximately flat at 0.273-0.274 s while calls rose from 52 to 74.
- Remote comment, RealMoji, follow, and block samples were too sparse for a reliable per-endpoint comparison.

The remote-region gains are consistent with the intended reduction in database round trips.

## Throughput and saturation

### Asia endpoint volume

| Endpoint | PR #20 baseline | PR #24 | Change in calls | Mean latency change |
|---|---:|---:|---:|---:|
| Posts | 206 | 354 | +72% | 0.681 s -> 1.071 s |
| Feeds | 1,316 | 2,404 | +83% | 0.139 s -> 0.178 s |
| Images | 1,997 | 3,330 | +67% | unchanged at 0.136 s |
| Memories | 440 | 948 | +115% | 0.236 s -> 0.266 s |
| Login | 1,004 | 1,549 | +54% | 0.097 s -> 0.152 s |
| Register | 77 | 115 | +49% | 0.229 s -> 0.514 s |

The benchmark produced much more work. Posts, posting-window reads, registration, and other unrelated endpoints became slower after the application and DB reached a higher utilization level. This run therefore should not be interpreted as a same-load latency regression.

## Resource usage

### Application CPU

| Host | PR #20 baseline | PR #24 |
|---|---:|---:|
| Asia | 69.73% | 101.77% |
| Europe | 3.46% | 3.80% |
| America | 2.63% | 3.13% |

Asia is now CPU-saturated. Its 70-second CPU profile contained 70.24 seconds of samples. The largest application costs were:

- image signature / SHA-256: about 23 seconds cumulative;
- JPEG decode: about 24.8 seconds cumulative;
- memory interaction loading and response construction;
- export processing and database calls.

These costs rose with the much larger number of completed posts, images, and feed/memory requests.

### Database CPU

| Metric | PR #20 baseline | PR #24 |
|---|---:|---:|
| User CPU | 31.2% | 43.4% |
| System CPU | 14.8% | 19.8% |
| Idle CPU | 45.1% | 27.0% |
| I/O wait | 5.3% | 6.0% |

The DB has less headroom, but is not yet continuously CPU-saturated. I/O wait remains visible.

## SQL findings

1. The new dominant query is the combined two-user row lock:
   - 1,258 calls, 60.37 s total, 47.99 ms mean, 20.9% of measured SQL time.
   - It returns roughly two rows per call and replaces separate lock round trips.
   - Although it is now the largest SQL bucket, the remote endpoint improvements show that combining the round trips is beneficial.

2. Export is the second-largest SQL cost:
   - 85 calls, 48.43 s total, 569.72 ms mean, 16.8%.
   - Its cost grew with export volume and remains a strong optimization candidate.

3. Advisory-lock waiting increased:
   - 136 calls, 31.26 s total, 229.84 ms mean, 10.8%.
   - This affects registration and admin posting-window serialization and contributes to their higher latency under the increased load.

4. Image storage and delivery remain important:
   - Image-data fetch: 2,799 calls, 26.87 s total, 9.60 ms mean.
   - Image insert: 427 calls, 21.44 s total, 50.21 ms mean.
   - Image authorization: 3,542 calls, 17.44 s total, 4.92 ms mean.

5. Notification fan-out remains expensive:
   - 18 calls, 11.85 s total, 658.24 ms mean, 29,671 inserted rows.

6. User search is no longer a primary bottleneck:
   - It fell to 2.2% of SQL time while serving almost twice as many calls.

## Assessment

Subject to a successful benchmark result, PR #24 is a material improvement. It removes the pathological user-search row volume and reduces cross-region write latency by consolidating database round trips. The benchmark then advances far enough to saturate the Asia application and place substantially more load on PostgreSQL.

The next optimization priorities exposed by this run are:

1. Asia application CPU, especially image signature hashing and JPEG decode.
2. The export query and response construction.
3. Contention around advisory locks used by registration and posting-window creation.
4. The combined user-row lock path, if its critical section can be shortened without weakening consistency.
5. Notification fan-out and image insert/I/O costs.
