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

## Current enforcement status (CP30 audit, Campaign 1)

The boundaries below were implemented in earlier checkpoints but were not yet enforced or wired when CP30 began. Each is a live obligation; the route/permission inventory in `docs/hardening/route-inventory.json` and the roadmap track their repair.

- RBAC role checks were consulted by only four handlers; most authenticated routes were session-only. Authorization is being enforced per route from the inventory, with viewer/operator/admin/owner boundaries proven by server-level tests.
- The fresh-install first administrator had no organization membership. First-admin user creation and owner membership are now a single atomic transaction with a race guard.
- Organization ids were hard-coded as `1` in user-facing queries. Site/group/list/fleet queries now filter by the acting organization derived from the session membership.
- API tokens were hashed at rest but no HTTP route accepted them; no endpoint rate limiting existed.
- Webhook delivery was never invoked by product events (`notifications.Deliver` had no callers).
- Audit launched Chromium with `--no-sandbox` without the public-network guard (CP30 Campaign 2 blocker).
- Cookie `Secure` and OIDC redirect schemes derived from `r.TLS`, so TLS-terminating reverse proxies produced non-secure cookies and `http` redirect URIs (CP30 Campaign 3 blocker).
- `webfleet backup`/`restore` always addressed the SQLite path regardless of the configured provider (CP30 Campaign 5/6 blocker).
