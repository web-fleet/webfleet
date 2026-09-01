# Web Fleet public-preview release contract

Web Fleet must not publish a stable public preview merely because implementation checkpoints are complete.

## Required evidence

Before tagging the first public preview:

- CP30 adversarial/battle-hardening report is complete with no unresolved release blockers.
- `go mod verify` passes and a clean-checkout `go test ./...` succeeds without modifying `go.mod`/`go.sum` or using `-mod=mod`.
- Linux, macOS and Windows CI builds pass at the exact release commit with no cgo dependency (password hashing is the pure-Go `golang.org/x/crypto/argon2` path).
- SQLite fresh install, update, rollback, backup and restore pass.
- PostgreSQL first-run setup, restart, migration and core behavioural parity pass against a real server (`WEBFLEET_TEST_POSTGRES_URL`), and backup/restore follow the configured provider (`pg_dump`/`pg_restore` via `PGPASSWORD`).
- OIDC succeeds against at least one real standards-compliant provider, works behind TLS-terminating reverse proxies with a canonical `WEBFLEET_PUBLIC_URL`, and local-admin recovery is proven.
- SSRF/crawler/Audit boundaries survive adversarial tests; Audit never launches an unsandboxed browser or bypasses the public-network guard.
- RBAC enforcement is proven against the route inventory (`docs/hardening/route-inventory.json`) with adversarial viewer/operator/admin/owner and cross-organization tests.
- systemd install creates the service account, ownership and data directory idempotently on a clean host.
- large-fleet measurements and current scale decision are recorded, including single-owner split-worker scheduling via the database lease.
- public website is audited against actual shipped commands/features.
- release archives have SHA-256 checksums and provenance/attestation where supported, built and verified with `scripts/release.sh` / `scripts/verify-release.sh` (release building and verification are separate responsibilities; the verifier is read-only).
- ordinary-user dogfood installs from the public instructions without repository knowledge.

## Positioning

The first release is a **public preview** for evaluation and early self-hosting. Do not call it battle-proven, highly available or production-ready.

Known limitations must be published rather than hidden behind generic preview language.
