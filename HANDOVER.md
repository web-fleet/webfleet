# Web Fleet handover

This is a living handover. Update it whenever implementation, architecture, product scope, repository state, verification requirements, or the next development checkpoint changes.

## Product

Web Fleet is a self-hosted website monitoring and analytics platform.

Its job is to answer, from one dashboard:

> What is happening across all of my websites, and what needs my attention?

Web Fleet is aimed at a single self-hoster or freelancer first, while keeping an architecture that can scale to agencies and enterprise web estates without replacing the product.

The product observes websites. It does not host them and should not become a general PaaS, DNS provider, CDN, or infrastructure manager.

## Product principles

1. **Useful in five minutes.** A user should be able to install one binary, create the first admin, add a URL, and see useful monitoring without deploying an agent or modifying the website.
2. **One site to thousands.** Small installations stay simple. Scale features must not make the small deployment miserable.
3. **Monitoring first.** Uptime, response behaviour, TLS, DNS, links, headers, performance, manually triggered browser-rendered audits and change history are the core product.
4. **Analytics is first-class but optional.** Public checks work without site changes. Analytics begins only after the operator installs the lightweight tracker.
5. **Privacy by default.** Do not collect visitor data merely because other analytics products do. Avoid persistent raw IP storage, fingerprinting and unnecessary cookies.
6. **Actionable dashboard.** Prefer "what needs attention" over walls of charts.
7. **Self-hosted ownership.** Configuration, backups, upgrades, rollback and data export must remain understandable.
8. **Simple default, scalable internals.** SQLite and an integrated process are valid defaults. PostgreSQL, split workers and larger ingestion paths can be added without changing the product model.
9. **No fake enterprise complexity.** RBAC, SSO, audit, retention and scale controls should appear when they solve real organizational requirements.
10. **Truthful public claims.** Documentation and the public website must distinguish shipped functionality from planned functionality throughout development.

## Initial technical direction

- Go application server and workers.
- SQLite default storage. Use `database/sql` with the cgo-free `modernc.org/sqlite` driver, matching Trestle's proven SQLite pattern. Keep SQLite at one owned connection, enable WAL, enforce foreign keys, use a five-second busy timeout, secure the data directory to `0700`, and make schema migrations transactional and fail-closed.
- PostgreSQL supported before broad production positioning.
- Nift-built HTML/CSS/JavaScript dashboard with vanilla JavaScript unless a concrete requirement justifies another frontend stack.
- Background scheduler for remote checks.
- Optional browser audit worker for manually triggered rendered performance/accessibility/best-practice/discoverability evaluations; basic monitoring must not require Chromium.
- HTTP/HTTPS checks performed agentlessly from the Web Fleet server.
- Analytics ingestion exposed as a separate HTTP endpoint inside the same application initially.
- Analytics subsystem kept architecturally separable from monitoring/crawling.
- One deployable binary for the normal installation.
- Optional process separation later for large installations.

Do not introduce Redis, Kafka, ClickHouse, Kubernetes, a separate frontend framework, or a service mesh into the default architecture without measured need.

## First-run database setup contract

Web Fleet supports SQLite and PostgreSQL, but database choice is part of **first-run setup**, not an environment-variable-only implementation detail.

Follow the Trestle-proven UX shape:

1. Before the first administrator exists, show the database choice.
2. SQLite is selected by default and requires no connection string.
3. Selecting PostgreSQL reveals the PostgreSQL URL and **Test and use PostgreSQL** action.
4. An empty URL keeps that action visibly disabled in every interaction state. A non-empty URL makes the enabled state visually clear.
5. Test the real connection before accepting PostgreSQL.
6. If applying PostgreSQL requires restarting Web Fleet, replace the setup form with a prominent restart-required message that tells the operator to restart Web Fleet and reload.
7. After restart, return to a page explicitly headed **Create the administrator account**. Do not make first-admin creation look like login.
8. Do not allow casual provider switching after the deployment contains administrator/application data.
9. `WEBFLEET_DATABASE_URL` remains available for non-interactive provisioning, but the ordinary-user dashboard flow must not require editing environment variables.

Keep the database-selection state machine pure/testable so the empty URL, enabled URL, restart and post-restart transitions can be regression tested without a browser.

## Core domain model

Keep these concepts explicit even when the first UI hides unnecessary hierarchy:

- **Organization** - ownership and access boundary. A single-user install can use one implicit organization.
- **Group** - optional organization of related sites, such as a client, team, product or portfolio.
- **Site** - a website Web Fleet monitors.
- **Environment** - production, staging, preview or another deployment context where needed.
- **Domain** - hostnames associated with a site.
- **Monitor** - an individual check definition.
- **Check result** - one observed monitor execution.
- **Incident** - a meaningful period of degraded or failed service.
- **Analytics property** - opt-in event collection associated with a site.
- **Analytics event** - a privacy-limited pageview or custom event.
- **Deployment event** - observed release/deployment metadata, initially from external integrations rather than deployment control.

Do not collapse site, domain and monitor into one record. A site can have multiple domains and multiple checks.

## Initial site experience

The fleet overview is the product centrepiece. It should answer these questions without drilling into individual sites:

- How many sites are healthy, degraded, warning or down?
- What needs attention now?
- Which problems are new?
- Which sites changed recently?
- Are there fleet-wide TLS, DNS, link, performance, audit or analytics anomalies?

A site detail view should eventually expose:

- Overview
- Uptime
- Performance
- Audit
- Pages and links
- TLS
- DNS
- Headers
- Incidents
- Analytics
- Deployments
- History
- Settings

Do not implement empty tabs merely because this list exists. Add a section when the underlying capability is usable.

## Monitoring contract

The first monitoring loop should support:

- HTTP status and request success/failure;
- response latency;
- redirects and final URL;
- timeout configuration;
- expected status/range;
- basic response metadata;
- retained check history;
- consecutive-failure handling before opening an incident;
- recovery and incident close;
- scheduler jitter so large fleets do not stampede at the same instant.

Monitoring failures must distinguish DNS failure, connection failure, TLS failure, timeout and unexpected HTTP response where possible.

## Website health expansion

After basic availability, add website-specific observations rather than generic host telemetry:

- TLS certificate validity and expiry;
- DNS record observation and change history;
- security/HTTP headers;
- crawl-based internal link health;
- external-link checks with conservative rate limits;
- redirects and redirect chains;
- sitemap and robots.txt discovery/health;
- page response/performance history;
- optional browser-rendered audits covering performance, accessibility, best-practice/security hygiene and technical discoverability;
- manual single-site and bounded batch audit execution, including simple fleet filters and optional advanced/regex targeting;
- latest audit results by default, with audit history/category-score regression tracking explicitly opt-in;
- basic metadata/structured-data checks where useful;
- regression/change detection.

Browser-rendered audits are a separate, heavyweight **manual** workload. They may use Chromium or another suitable browser engine, but basic monitoring, TLS/DNS checks and analytics ingestion must remain usable without a browser runtime. Audit history is opt-in rather than the default. Batch audits must resolve an explicit site set from selections/search/groups/tags or optional advanced/regex predicates, preview that scope before execution, and run through a bounded queue so they cannot starve availability monitoring. Do not claim Google Lighthouse compatibility unless Web Fleet literally executes and reports Lighthouse with a maintained compatibility contract; the product-facing feature/tab name is simply **Audit**.

## Analytics boundary

Analytics is part of Web Fleet's product, but keep the subsystem separable.

Initial analytics should target web analytics, not broad product analytics:

- pageviews;
- privacy-preserving visitor estimates;
- top pages;
- referrers/sources;
- country-level geography where feasible without retaining precise location;
- device/browser/OS classes derived conservatively;
- custom events;
- goals;
- realtime/recent activity;
- date-range filtering.

Default privacy posture:

- no cookies required;
- do not persist raw IP addresses by default;
- no browser fingerprinting;
- strip or allowlist sensitive query parameters;
- configurable raw-event retention;
- bot filtering;
- transparent documentation of collected fields.

Do not build session replay, heatmaps, arbitrary user profiling, ad-tech attribution or a PostHog replacement into the initial product.

## Scale path

The normal self-hosted deployment should remain:

```text
webfleet
  + SQLite
```

A larger installation may later become:

```text
webfleet serve
webfleet worker
webfleet analytics-ingest
  + PostgreSQL
```

The split must be an operational option, not a different product.

## Security baseline

Before public preview:

- password hashing with a modern memory-hard password KDF;
- secure session cookies;
- CSRF protection for state-changing browser requests;
- strict origin/CORS handling for analytics ingestion;
- site/property tokens that cannot grant dashboard access;
- request body and rate limits;
- SSRF protection for monitors and crawlers, including redirect revalidation;
- safe URL normalization;
- audit records for security-sensitive actions;
- backup/restore documentation and tests;
- least-privilege service installation.

Monitoring arbitrary URLs is an SSRF-sensitive feature. Treat private/reserved address handling, DNS rebinding and redirect destinations as security boundaries from the first implementation.

## Repository discipline

- Keep this file current after every substantial checkpoint.
- Keep `ROADMAP.md` current. Checkpoints may be split/merged as evidence changes, but do not silently delete unfinished obligations.
- Keep public documentation truthful about current maturity.
- Keep source and generated website repositories clean after website work.
- Do not commit secrets, analytics signing keys, test credentials or operator data.
- Prefer focused commits with tests/evidence for the behaviour changed.
- Treat database lifecycle as a contract: restart persistence, PRAGMA enforcement, future-schema refusal, migration-history integrity and migration rollback must remain covered by tests.
- Before declaring a checkpoint complete, run the relevant tests plus `git diff --check` and report the exact commit.

## Nift usage

Nift is the build-time templating/dependency layer for Web Fleet's frontend and public website. Use it for composition, tracked relationships and generated static assets where it helps. Use normal Go/JavaScript/CSS for application behaviour.

Use `@pathto` for internal tracked-page and local-asset relationships, and `@input` for shared reusable template fragments. Do not hand-maintain generated output.

## Current state

As of this handover revision:

- GitHub organization: `web-fleet`.
- Main application repository: `web-fleet/webfleet`.
- CP1-CP25 are complete in development. CP19 includes the Trestle-style first-run SQLite/PostgreSQL selection UX; live PostgreSQL parity testing remains a CP30 battle-hardening gate. CP21-CP23 add organizations/RBAC, scoped API tokens and OIDC; CP24 adds optional process roles; CP25 records the no-extra-distributed-infrastructure-yet decision.
- CP30 adversarial review is underway. It confirmed several implementation checkpoints were not yet wired/enforced when CP30 began: RBAC was consulted by only four handlers, the fresh-install first administrator had no organization membership, API-token authentication and webhook delivery had no callers, tag filtering was not wired in the dashboard, the scheduler had no claim/lease, and organization ids were hard-coded in user-facing queries. It also confirmed release blockers: an incomplete `go.sum`, a cgo `libargon2` password binding that breaks cross-platform builds, a systemd install that never creates the service account/ownership, and provider-agnostic backup/restore. These are recorded in `ROADMAP.md`, `SECURITY.md`, `SCALE.md`, `RELEASE.md` and the route inventory at `docs/hardening/route-inventory.json`.
- CP30 Campaign 1 (project-truth correction + identity/RBAC foundation) is **complete and reviewed**. It repaired: the fresh-install first-administrator membership bug, the first-admin setup race, and the reload-lost restart-required first-run state; and it enforced route-level RBAC plus organization isolation for the Campaign 1 API surface, backed by the machine-readable route/permission inventory at `docs/hardening/route-inventory.json` and adversarial viewer/operator/admin/owner server tests. Operators may archive sites but only admin/owner may permanently delete them.
- CP30 Campaign 2 (Audit/SSRF boundary) is **complete and reviewed**. The browser Audit no longer launches `--no-sandbox` Chromium at an unguarded URL. Chromium is pinned to an in-process guarded proxy that applies the public-network guard to every connection and re-checks the full resolution set at dial time (DNS rebinding blocked); the browser sandbox is the default with `--no-sandbox` only on explicit opt-in; execution timeout, bounded output and concurrency are enforced; Audit remains manual with opt-in history. Evidence: `docs/hardening/audit-boundary.json`.
- CP30 Campaign 3 (authentication, trusted proxy, OIDC) is **complete and reviewed**. An explicit `WEBFLEET_TRUSTED_PROXIES` model makes scheme, client identity, cookies and OIDC redirect URIs honor forwarded headers only from trusted peers; login/setup are throttled per resolved client address; OIDC verifies the ID token cryptographically (signature/issuer/audience/expiry) via `github.com/coreos/go-oidc`, enforces one-time state and nonce, requires verified email, and leaves local password login as the independent recovery path. `[closure]` OIDC transactions are browser-bound via a short-lived HttpOnly SameSite=Lax cookie consumed atomically with the state, and the callback origin is canonical and fail-closed, coming only from `WEBFLEET_PUBLIC_URL` (required to enable/use OIDC). Real-provider interoperability remains an external CP30 gate.
- CP30 Campaign 4 (API tokens and integrations) is **complete and reviewed**. A deliberate API-token surface is wired through the route inventory (`tokenScopes`): Bearer tokens grant exactly their scopes within their own organization, cannot reach session-only routes, are hashed at rest and returned only at creation, and deny unknown/revoked/missing-scope cases; failed Bearer auth is throttled per trusted-proxy-resolved client address and error responses are single-JSON. Webhook delivery is real product behavior: incident open/recover transitions write an outbox atomically with incident state **scoped to the site's own organization**, and a background worker delivers with guarded outbound networking, HMAC signatures with a stable `event_id`, bounded retries/timeouts/output, no redirect-following, and disabled-destination filtering; the obsolete synchronous `Service.Deliver` was removed so the guarded worker is the only delivery boundary. CP22 and CP27 have moved from "implemented but unwired" to their post-Campaign-4 truth.
- CP30 Campaign 5 (PostgreSQL parity, provider-aware backup, scheduler claim/lease) is **implemented, pending review**. A real-PostgreSQL suite (guarded by `WEBFLEET_TEST_POSTGRES_URL`) exercises the product's storage paths against a live server, catching and fixing two dialect bugs at the shared database layer. Backup/restore is provider-aware (SQLite native, PostgreSQL pg_dump/pg_restore with PGPASSWORD-only credentials, bounded subprocesses, tool-absence detection, rehearsals for both). The scheduler uses a database claim/lease (`job_leases`) with unique owners, expiry and reclamation, proven by two-worker adversarial tests on both providers. Evidence: `docs/hardening/database.json`. CP30 and CP31 remain blocked on the remaining Campaign 6-8 work.
- The companion `web-fleet.github.io` source/generated workspace documents the current development state.
- The next implementation checkpoint is CP26 in `ROADMAP.md`.

## Immediate next step

The implementation campaign is complete through CP29. CP30 is now explicitly handed to DeepSeek/Cortex for adversarial testing and battle hardening. Use `SECURITY.md`, `SCALE.md`, `RELEASE.md`, the roadmap and existing regressions as the attack/review contract.

CP30 Campaign 1 (project-truth correction + identity/RBAC foundation) is complete. Each campaign stops for review after an independently complete, committed repair with regression/adversarial tests. The route/permission inventory at `docs/hardening/route-inventory.json` is the authorization contract that drove the RBAC enforcement work and is enforced by the route-inventory contract test.

Do not tag or announce the public preview while CP31 is blocked on CP30. After the hardening campaign, repair every release blocker, rerun the exact release gates, then perform an ordinary-user dogfood install from the public website before deciding whether to tag the preview.
