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

The boundaries below were implemented in earlier checkpoints but were not yet enforced or wired when CP30 began. Repaired items are marked `[repaired in CP30 Campaign 1]`; the route/permission inventory in `docs/hardening/route-inventory.json` and the roadmap track the rest.

- RBAC role checks were consulted by only four handlers; most authenticated routes were session-only. `[repaired]` Every authenticated route now declares a permission and CSRF posture in one route table and is enforced by the central `authorize` guard, with viewer/operator/admin/owner boundaries proven by server-level adversarial tests. No route is owner-only; the owner-only `organization.delete` action is deliberately not exposed over HTTP.
- The fresh-install first administrator had no organization membership. `[repaired]` First-admin user creation and owner membership are now a single atomic transaction with a concurrency guard.
- Organization ids were hard-coded as `1` in user-facing queries. `[repaired]` Site/group/list/fleet and audit-batch queries now filter by the acting organization resolved from the session membership; site-scoped handlers reject cross-organization access.
- API tokens were hashed at rest but no HTTP route accepted them; no endpoint rate limiting existed. `[repaired in CP30 Campaign 3/4]` Login and first-run setup are throttled per resolved client address with a bounded-memory fixed window; a deliberately defined API-token surface now accepts `Authorization: Bearer` tokens on routes that declare a scope (`sites:read`, `sites:write`, `fleet:read`, `analytics:read`, `audit:run`), with the acting organization fixed to the token's own organization, generic errors for unknown/revoked tokens, hashed storage, secret returned only at creation, no CSRF for token-authenticated mutations, single-JSON error responses, and failed-Bearer throttling per trusted-proxy-resolved client address (spoofed `X-Forwarded-For` cannot bypass it; rate-limit keys are never token material). See `docs/hardening/integrations.json`.
- Webhook delivery was never invoked by product events (`notifications.Deliver` had no callers). `[repaired in CP30 Campaign 4]` Incident open/recover transitions now write webhook outbox rows atomically with the incident state **scoped to the site's own organization** (an incident in organization A can never queue to organization B's webhooks); a background worker performs guarded, bounded, retried delivery with HMAC signatures and a stable `event_id` deduplication identity, so monitoring never depends on an external webhook. Destinations must be HTTPS and public at creation and are re-checked at dial time (DNS rebinding and mixed public/private answers fail closed); redirects are never followed. The obsolete synchronous `Service.Deliver` was removed so the guarded worker is the only delivery boundary. Webhook secrets are stored plaintext by design because they must be recoverable to sign (documented threat-model decision), returned only at creation, and redacted from errors.
- Audit launched Chromium with `--no-sandbox` and no public-network guard. `[repaired in CP30 Campaign 2]` Audit now pins Chromium to an in-process guarded proxy so every connection (redirects and subresources included) is validated and dialed by the Go process; Chromium never resolves or dials the target itself. DNS rebinding is blocked at dial time, the browser sandbox is required by default (`WEBFLEET_AUDIT_SANDBOX` strict) with `--no-sandbox` only on explicit opt-in, browser output and concurrency are bounded, and Audit remains manual with opt-in history. See `docs/hardening/audit-boundary.json`.
- Cookie `Secure` and OIDC redirect schemes derived from `r.TLS`, so TLS-terminating reverse proxies produced non-secure cookies and `http` redirect URIs. `[repaired in CP30 Campaign 3]` Web Fleet now has an explicit trusted-proxy model (`WEBFLEET_TRUSTED_PROXIES`): forwarded headers are honored only from trusted peer addresses and are ignored from untrusted peers, so spoofed `X-Forwarded-Proto`/`X-Forwarded-For` cannot upgrade a plaintext request, change client identity, alter OIDC redirect URIs or make cookies `Secure`.
- OIDC validated neither the ID token nor the nonce and derived redirect URIs from `r.TLS`. `[repaired in CP30 Campaign 3]` OIDC now uses `github.com/coreos/go-oidc` for standards-compliant discovery, keyset retrieval and ID-token verification (signature, issuer, audience, expiry), enforces one-time state consumption and nonce equality, requires a verified email (with userinfo fallback only when the ID token omits email), and keeps local password login as an independent recovery path. `[Campaign 3/4 closure]` Each authorization transaction is additionally bound to the initiating browser with a short-lived HttpOnly SameSite=Lax cookie (follows the trusted-proxy Secure decision), so one browser cannot consume another browser's valid state/code pair; the OIDC callback origin is canonical via `WEBFLEET_PUBLIC_URL` when configured, and `X-Forwarded-Host`/`Forwarded` are never trusted. Real-provider interoperability remains an external CP30 release gate; a local standards-shaped provider simulation covers the adversarial cases.
- `webfleet backup`/`restore` always addressed the SQLite path regardless of the configured provider (CP30 Campaign 5/6 blocker).

## Role policy decisions (CP30 Campaign 1)

- **Operators may archive sites but may not permanently delete them.** `site.archive` is reversible and available to operators; `site.delete` is admin/owner only because permanent deletion cascades through check history, analytics, audit and deployments. This is enforced by `rbac.Can` and covered by the rbac matrix and server-level denial/allowance tests.
- **Admin and owner share the shipped API surface.** The only owner-only action, `organization.delete`, is reserved and not routed. The admin/owner distinction is proven at the `rbac` matrix boundary rather than inventing an endpoint solely for the test.
