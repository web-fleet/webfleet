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

CP30 public-preview hardening must run repeatable fleet tests at 100, 1,000 and 10,000 sites where practical, record check throughput, scheduler lag, crawl throughput, analytics ingest rate, database size/growth and dashboard query latency, and retain the raw commands/results.

Until those measurements justify a change, the decision is **no additional distributed infrastructure**.
