# Web Fleet public-preview release contract

Web Fleet must not publish a stable public preview merely because implementation checkpoints are complete.

## Required evidence

Before tagging the first public preview:

- CP30 adversarial/battle-hardening report is complete with no unresolved release blockers.
- Linux, macOS and Windows CI builds pass at the exact release commit.
- SQLite fresh install, update, rollback, backup and restore pass.
- PostgreSQL first-run setup, restart, migration and core behavioural parity pass against a real server.
- OIDC succeeds against at least one real standards-compliant provider and local-admin recovery is proven.
- SSRF/crawler/Audit boundaries survive adversarial tests.
- large-fleet measurements and current scale decision are recorded.
- public website is audited against actual shipped commands/features.
- release archives have SHA-256 checksums and provenance/attestation where supported.
- ordinary-user dogfood installs from the public instructions without repository knowledge.

## Positioning

The first release is a **public preview** for evaluation and early self-hosting. Do not call it battle-proven, highly available or production-ready.

Known limitations must be published rather than hidden behind generic preview language.
