# Web Fleet scale and HA decision record

This document is a living evidence record for CP25. Web Fleet does not add distributed infrastructure because an enterprise feature list says it should.

## Current architecture decision

Keep the default deployment integrated:

```text
webfleet
├── dashboard/API
├── scheduler/check workers
├── crawler
├── analytics ingestion
└── SQLite or PostgreSQL
```

CP24 also exposes optional `webfleet serve`, `webfleet worker`, and `webfleet analytics-ingest` process roles. These are operational separation points, not mandatory microservices.

**CP30 status:** the optional roles exist and the scheduler now uses a fenced database due/claim model (`scheduler_claims`, migration 27): persistent per-kind/per-site `next_due_at`, unique owner, generation fencing token and `lease_until`. A unit is claimed atomically only when due and unclaimed/expired; renewal and completion are owner+generation qualified, so a stale worker cannot renew or complete after ownership moves and its in-flight work is cancelled. Completion advances `next_due_at`, so a configured interval longer than any lease TTL cannot create duplicate due work, and crash recovery is at-least-once. Semantics are proven equivalent on SQLite and PostgreSQL. The campaign must still measure single-owner scheduling and duplicate-execution behaviour at 100/1,000/10,000 sites.

## Evidence threshold

The current implementation has:

- bounded HTTP check concurrency;
- separately bounded manual Audit concurrency;
- bounded crawler concurrency;
- server-side paginated/searchable site inventory;
- a 1,000-site inventory regression;
- PostgreSQL as the multi-process coordination/storage path.

Do **not** add Redis, Kafka, ClickHouse, a queue service, Kubernetes coordination, or bespoke HA consensus yet.

Add another component only after a repeatable workload demonstrates one of:

1. PostgreSQL-backed workers cannot claim/schedule work without unacceptable duplicate execution or lock contention;
2. analytics ingestion exceeds the measured sustainable PostgreSQL/in-process buffering rate;
3. dashboard/report queries materially interfere with ingestion/check latency after indexing and query fixes;
4. a documented availability requirement demands active-active application nodes and cannot be met by ordinary reverse-proxy/process redundancy.

## Next measurement campaign

CP30 public-preview hardening must run repeatable fleet tests at 100, 1,000 and 10,000 sites where practical, record check throughput, scheduler lag, crawl throughput, analytics ingest rate, database size/growth and dashboard query latency, and retain the raw commands/results. The campaign must also prove that split-worker operation schedules each site exactly once (no duplicate checks/crawls/incidents) before the split path is presented as coordinated.

## CP30 scale report (SQLite, single machine; PG where noted)

Run with `WEBFLEET_SCALE=1 go test ./internal/sites/ -run TestScaleReport -v` (optionally `WEBFLEET_TEST_POSTGRES_URL=...` for the PG comparison).

| Measurement | 100 | 1,000 | 10,000 |
|---|---|---|---|
| seed inserts | 2 ms | 17 ms | 172 ms |
| list page (100/page) | 0.7 ms | 1.1 ms | 3.9 ms |
| search page | 0.3 ms | 0.9 ms | 7.7 ms |
| fleet summary | 0.7 ms | 1.2 ms | 5.6 ms |
| tag filter (no tags) | 0.1 ms | 0.5 ms | 4.1 ms |
| scheduler claim loop (all sites) | 2.9 ms | 27 ms | 265 ms |

PostgreSQL (1,000 sites): scheduler claim loop 43 ms (~43 µs/claim, atomic upsert), search page 2.5 ms — comparable to SQLite, confirming the fenced claim's per-site overhead is bounded and does not require a queue.

**Decision (unchanged):** no additional distributed infrastructure is justified. All measured paths stay comfortably interactive at 10,000 sites; the fenced scheduler claim adds ~26 µs/site on SQLite. If any future measurement shows PostgreSQL-backed workers cannot claim without unacceptable contention, or dashboard queries materially interfere with ingestion, `SCALE.md`'s four evidence thresholds re-apply.

Until those measurements justify a change, the decision is **no additional distributed infrastructure**.
