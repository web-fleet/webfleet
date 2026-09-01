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

**CP30 correction:** the optional roles exist, but the scheduler has no claim/lease mechanism, so running two worker processes would both schedule the full fleet and duplicate checks, crawls and incidents. "PostgreSQL-backed coordination" is not yet implemented; it is a CP30 obligation and must be proven (or replaced with single-owner scheduling) before split-worker operation is claimed. `SCALE.md` previously implied PostgreSQL was the multi-process coordination path; that claim was ahead of the implementation.

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

Until those measurements justify a change, the decision is **no additional distributed infrastructure**.
