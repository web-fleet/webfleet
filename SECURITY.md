# Web Fleet security model

## Trust boundaries

Web Fleet accepts untrusted website URLs, HTML/link targets, analytics events, OIDC responses, deployment metadata and administrator configuration.

The most important boundaries are:

- monitoring/crawling URLs must pass the public-address guard at resolution and again for redirects;
- browser Audit is manual, bounded and must not become an SSRF bypass around the normal URL guard;
- analytics property keys grant ingestion only, never dashboard access;
- API tokens are hashed at rest and scope-limited;
- OIDC requires HTTPS discovery and verified email; local admin authentication remains recovery;
- webhook destinations require HTTPS and secrets are returned only at creation;
- reverse-proxy forwarding headers are not authorization credentials;
- database/provider selection locks after administrator creation.

## CP30 battle-hardening handoff

The repository implementation campaign is complete, but public preview remains blocked on adversarial evidence. DeepSeek/Cortex should attempt to break:

1. URL parsing, redirects, DNS rebinding/private ranges, crawler links and Audit targets.
2. CSRF/session cookie behavior and cross-origin analytics ingestion.
3. RBAC privilege boundaries, API token scopes/revocation and organization isolation.
4. OIDC state/nonce/account-linking/provider interoperability.
5. webhook retries, signatures, secret redaction and malicious endpoints.
6. SQLite/PostgreSQL parity, migration failure/restart/recovery and backup/restore.
7. worker separation, duplicate scheduling and analytics/check contention.
8. 100/1,000/10,000-site workloads and large-fleet browser UX.
9. clean install/update/rollback on supported release platforms.
10. accessibility, mobile layout and ordinary-user setup confusion.

Do not waive a failed gate because the feature worked in the implementation environment.
