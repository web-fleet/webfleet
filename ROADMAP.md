# Web Fleet development roadmap

This is a living implementation plan. Update checkpoint status, evidence and follow-up work throughout development.

## Phase 1 - prove the monitoring loop

### CP1 - Application foundation ✅

- Establish Go module and package layout.
- Embed/build the Nift dashboard frontend through the normal repository workflow.
- Add configuration loading with explicit data/listen settings.
- Open SQLite database and version schema migrations.
- Add graceful startup/shutdown and structured application logging.
- Add `/healthz` and a minimal dashboard shell.
- Add unit tests for configuration and migration behaviour.

**Exit:** one binary starts from a fresh directory, initializes its database, serves the dashboard and exits cleanly.

**Completed:** Go application foundation, versioned SQLite schema, Nift-built embedded dashboard, configuration, structured logging, graceful shutdown, `/healthz`, and unit/integration coverage. SQLite now follows the same storage pattern as Trestle: `database/sql`, the cgo-free `modernc.org/sqlite` driver, one owned SQLite connection, WAL, foreign-key enforcement, a five-second busy timeout, secured data-directory permissions, validated migration history and transactional migrations.

### CP2 - First-admin authentication ✅

- Fresh-install setup flow.
- First admin creation.
- Modern password hashing.
- Login/logout/session lifecycle.
- CSRF protection and secure cookie settings.
- Minimum password length consistent with the project policy.
- Authentication audit events.

**Exit:** unauthenticated users cannot access the dashboard and session/security regression tests pass.

**Completed:** first-run admin creation, Argon2id password hashing, strict session cookies, hashed session tokens, CSRF enforcement, logout/session expiry, authentication audit records, and setup/login UI. The current Argon2 binding uses the system `libargon2.so.1` because external Go modules are unavailable in this build environment; portable dependency packaging remains a later release-hardening obligation.

### CP3 - Site model and CRUD ✅

- Organization foundation with a simple single-organization default UX.
- Site records with name, primary URL, enabled state and optional tags/group.
- URL canonicalization and validation.
- Site create/edit/archive/delete UX.
- Empty state and validation errors that ordinary users can understand.
- Search/filter foundation for future large fleets.

**Exit:** a user can add and manage several sites without monitoring yet.

**Completed:** persisted groups and sites, canonical HTTP/HTTPS URLs, create/edit/archive/delete flows, server-backed search/group filtering/page-number pagination, large-fleet inventory UX, and a stable SPA site-detail route. Search/pagination/grouping were deliberately promoted from the later large-fleet UX checkpoint because they are core inventory behaviour, while CP27 now focuses on accessibility and scale hardening rather than first implementation.

### CP4 - HTTP monitor engine ✅

- Monitor definitions and check-result persistence.
- HTTP/HTTPS request execution with timeout and redirect policy.
- Status/latency/final URL capture.
- Error classification.
- SSRF protections for private/reserved targets, redirect hops and DNS resolution.
- Unit and integration tests with controlled local HTTP fixtures.

**Exit:** checks are correct, security-bounded and queryable from storage.

**Completed:** persisted HTTP monitor definitions/results, timeout and redirect bounds, expected-status evaluation, latency/final-URL capture, structured DNS/connection/TLS/timeout/redirect/status error classes, and SSRF-safe DNS/dial/redirect validation. Private/reserved targets are blocked at resolved-address and dial time; controlled local fixtures use an explicit test-only private-network override.

### CP5 - Scheduler and fleet health ✅

- Background scheduler with bounded concurrency and jitter.
- Manual "check now" action.
- Consecutive failure/recovery state machine.
- Fleet health derivation.
- Fleet overview showing healthy/degraded/warning/down counts and "needs attention".
- Site overview with recent history.

**Exit:** adding a URL results in periodic checks and an immediately useful fleet dashboard.

**Completed:** bounded-concurrency scheduler with interval jitter, manual Check now, persisted healthy/degraded/warning/down/unknown state, consecutive failure and recovery transitions, fleet health counts, attention queue, health-aware site inventory, and recent check history on the individual site page.

### CP6 - Incident history and alerts foundation ✅

- Open/close incidents from monitor state transitions.
- Incident timeline and acknowledgement metadata.
- Alert policy model.
- First notification transport, kept deliberately simple.
- Deduplication and recovery notification behaviour.

**Exit:** an outage creates one coherent incident and recovery closes it without alert storms.

**Completed:** one persisted incident per unhealthy period, state escalation without duplicate incidents, recovery closure, acknowledgement metadata, alert policy model, and deduplicated in-app open/recovery delivery history. The site-detail view exposes incident history; external transports remain intentionally deferred to the integrations checkpoint.

## Phase 2 - understand websites as websites

### CP7 - TLS health ✅

- Certificate chain/hostname/expiry inspection.
- Expiry thresholds and fleet warnings.
- TLS failure classification and history.
- Dashboard/site-detail presentation.

**Completed:** shared public-target network guard reused by HTTP/TLS, certificate handshake/hostname validation, chain leaf metadata and expiry persistence, 30-day fleet warning query, manual inspection, 12-hour scheduler refresh, and site-detail TLS presentation.

### CP8 - DNS observation ✅

- Observe relevant A/AAAA/CNAME records.
- Record resolved values and meaningful changes.
- Distinguish transient resolution failures from changed configuration.
- DNS history UI.

**Completed:** A/AAAA/CNAME observation with normalized record sets, private/reserved-answer rejection, successful-state comparison, explicit error observations that do not masquerade as configuration changes, one-hour scheduler refresh, manual observation, history API, and site-detail presentation.

### CP9 - Headers and redirects ✅

- Security/header observations.
- Redirect chain recording.
- Configurable expectations without turning the feature into a generic rule engine.
- Regression/change history.

**Completed:** complete bounded redirect-chain capture, selected HTTP/security header observations, per-site configurable required-header expectations, missing-header evaluation, change detection against the prior observation, history API, and site-detail summary.

### CP10 - Website crawler and link health ✅

- Same-site crawler with explicit limits.
- robots/sitemap awareness where appropriate.
- Internal link graph and broken-link detection.
- Conservative external-link checking.
- Crawl schedule independent from high-frequency uptime checks.
- Per-page and fleet-level regressions.

**Completed:** bounded same-origin crawl queue (50 pages, depth 3, 200 links/page), robots.txt and sitemap awareness, persisted page/link graph, internal broken-link detection, conservative capped external checks with HEAD-to-GET fallback, new-broken comparison against the prior completed crawl, six-hour independent crawl scheduling, manual crawl API/UI, site-detail link health, and fleet-level regression surfacing. All crawler requests and redirects use the shared public-target network guard.

**Ordering review after CP10:** CP11 remains next. Performance history should establish honest server-side baselines before browser-rendered audits are added. CP12 is now a first-class Site Audits checkpoint rather than a vague optional browser-check note. CP13-CP16 then form the coherent privacy-first analytics slice. CP17 backup/restore remains a release gate and may be pulled ahead of analytics only if monitoring-only dogfooding begins before the analytics phase is ready.

### CP11 - Performance history [COMPLETE]

- Stable server-side timing metrics first.
- Response-size and transfer observations.
- Regression thresholds/baselines.
- Avoid presenting synthetic server timing as browser Core Web Vitals.
- Keep this checkpoint independent of a browser runtime so ordinary monitoring remains lightweight.

### CP12 - Audit [COMPLETE]

- Add an optional browser-rendered audit runner for representative pages.
- Present the feature in the site UI simply as **Audit**, not as Google Lighthouse compatibility.
- Audits are **manual by default**. Web Fleet must not automatically launch browser audits merely because a site is being monitored.
- Evaluate performance, accessibility, best-practice/security hygiene and technical discoverability/SEO-style signals with clearly documented scoring rules.
- Capture browser-derived metrics separately from CP11 server timings; never relabel synthetic HTTP timings as Core Web Vitals.
- Show the latest audit result without requiring history.
- Make **audit history opt-in** per site/property. With history disabled, a successful new run replaces the stored current result rather than accumulating historical runs.
- When history is enabled, retain category scores, individual findings and evidence so changes/regressions can be compared over time. Detailed retention controls belong with the later retention-policy work rather than complicating CP12.
- Allow per-site audit configuration, representative-page selection and a manual **Run audit** action.
- Add a **Batch audit** workflow for intentionally running audits across many sites.
- Reuse the normal fleet-selection model for batch targeting: selected sites, current search results, group/category, tags and other simple filters should be available without requiring advanced syntax.
- Provide an optional **Advanced** matcher for operators who need it, including name/URL regular expressions and useful predicates such as last-audited age or current audit state.
- Before starting an advanced/regex batch, show the resolved site count/list so a broad expression cannot unexpectedly launch audits across the whole fleet.
- Queue and bound batch work. Never launch one browser per matched site; audit concurrency must be configurable/limited independently from uptime monitoring.
- Surface current audit problems fleet-wide. Surface **regressions** only where history is enabled and sufficient historical evidence exists.
- Keep the browser runtime optional so basic self-hosting and all automatic monitoring remain usable without Chromium.
- Keep the audit engine internally abstract enough that a browser extension or remote audit worker can be explored later without changing the site/audit data model.

**Exit:** an operator can manually audit one site or deliberately batch-audit an understandable filtered set, inspect the latest browser-based quality evaluation, optionally retain/compare history, and do so without Web Fleet launching heavyweight browser work automatically or confusing the feature with Google Lighthouse itself.

**Phase 2 exit:** Web Fleet understands failures and regressions specific to websites, including browser-rendered quality regressions, not merely hosts.

## Phase 3 - privacy-first analytics

### CP13 - Analytics property and tracker [COMPLETE]

- Optional analytics property per site.
- Tiny cacheable tracker script.
- Ingestion endpoint with origin/property validation and rate limits.
- Pageview event contract.
- No-cookie default.
- Privacy review of every collected field.

### CP14 - Analytics storage and rollups [COMPLETE]

- Raw-event retention policy.
- Privacy-preserving visitor estimation.
- Hour/day aggregate tables.
- Top pages, sources/referrers and coarse client/geography dimensions.
- Bot filtering.
- Performance/load tests for SQLite-scale deployments.

### CP15 - Analytics dashboard [COMPLETE]

- Today/7d/30d/custom ranges.
- Visitors, pageviews, top pages, sources, countries and device/browser classes.
- Recent activity.
- Fleet-wide traffic overview.
- Clear "analytics not installed" onboarding for sites without the tracker.

### CP16 - Events and goals [COMPLETE]

- Custom event API.
- Goal definitions.
- Event/goal dashboards.
- Keep arbitrary user profiling out of scope.

**Phase 3 exit:** Web Fleet offers useful privacy-first analytics to static sites, including GitHub Pages, without needing to host those sites.

## Phase 4 - self-hosting and operational reliability

### CP17 - Backup and restore [COMPLETE]

- Consistent SQLite backup.
- Restore workflow with safety checks.
- Configuration/data export where appropriate.
- Documented disaster-recovery test.

### CP18 - Service install/update/rollback [COMPLETE]

- Linux systemd install path with clear privilege/ownership model.
- Release artifact verification.
- Update and rollback workflow.
- Fresh-install, upgrade and rollback tests.
- Keep non-Linux application builds compiling even if service installation is platform-specific.

### CP19 - PostgreSQL [COMPLETE]

The storage path and Trestle-style first-run database-selection experience are implemented. Live PostgreSQL behavioural/parity execution is intentionally retained for the CP30 battle-hardening campaign.

**Post-Campaign-5 truth:** a real-PostgreSQL parity suite (guarded by `WEBFLEET_TEST_POSTGRES_URL`) now runs the product's storage paths against a live server, catching and fixing two dialect bugs (bool-to-INTEGER binding and an unqualified conflict-update RHS). Fresh setup, restart idempotency, future-schema refusal and provider-aware backup/restore are proven on real PostgreSQL.

- Storage abstraction only where necessary.
- PostgreSQL migrations and integration tests.
- Behavioural equivalence for core monitoring/auth/analytics paths.
- Migration/import story from SQLite where feasible.
- **First-run database choice happens before first-admin registration.**
- Default to SQLite and explain that it is the simplest self-hosted choice.
- Offer PostgreSQL as an explicit alternative during first-run setup.
- When PostgreSQL is selected, show a connection URL field plus **Test and use PostgreSQL**. The action must remain visibly disabled, including hover state, while the URL is empty; once the URL is non-empty the enabled/hover styling must be unambiguous.
- Validate the PostgreSQL connection and schema compatibility before persisting the choice. Do not create the administrator until the database choice is settled.
- If switching to PostgreSQL requires a process restart, show a prominent **Restart required** state with an exact next action: stop/start Web Fleet, reload the page, then create the administrator account.
- After restart/reload, the auth gate must explicitly say **Create the administrator account** rather than looking like an ordinary sign-in form.
- Once a deployment contains application/admin data, database-provider switching must not remain casually available from the browser setup flow. Later migration tooling is a separate deliberate operation.
- Support non-interactive/server-managed configuration through `WEBFLEET_DATABASE_URL` for operators who provision configuration before first launch.
- Add a pure setup-state contract/regression suite, similar in spirit to Trestle's database setup state machine, covering SQLite, PostgreSQL URL empty/non-empty, persisted provider, restart-required and post-restart administrator-creation transitions.
- Run the full PostgreSQL behavioural/integration suite against a real PostgreSQL server during CP30 public-preview hardening, proving fresh setup, restart, migration, backup/recovery expectations and SQLite/PostgreSQL behavioural parity.

**Exit:** an ordinary user can choose SQLite or PostgreSQL before creating the first administrator, cannot accidentally proceed with an untested PostgreSQL URL, receives unmistakable restart instructions when required, and lands back on an unmistakable administrator-creation screen after restart.

### CP20 - Retention and maintenance [COMPLETE]

- Check-history retention/compaction.
- Analytics raw-event retention.
- Database maintenance jobs.
- Disk-usage visibility and guardrails.

**Phase 4 exit:** ordinary self-hosters can install, update, back up, restore and operate Web Fleet confidently.

## CP11-CP20 ordering review

The sequence remains sound after implementation. Performance and Audit needed to precede analytics so browser-derived and server-derived measurements stayed distinct. Analytics property/storage/dashboard/events formed one coherent block. Backup/restore and the Linux service lifecycle correctly preceded the larger-database path. PostgreSQL storage and first-run database selection are complete before enterprise identity work; CP21+ preserves both storage modes. Live PostgreSQL parity remains a CP30 release-hardening gate. CP20 retention belongs before multi-user scale because disk growth is already a concern for monitoring and analytics.

No checkpoint is being promoted ahead of CP21. The next block should start with users/organizations/RBAC, then API tokens and OIDC. CP19 is complete at the implementation checkpoint level. Live PostgreSQL integration/parity execution remains explicitly required by CP30 and must not be waived.

## Phase 5 - multi-user, agency and enterprise scale

### CP21 - Users, organizations and RBAC [COMPLETE]

- Multiple users.
- Organization membership.
- Roles/permissions scoped to organizations/groups/sites.
- Agency/client-friendly grouping.
- Audit logs for privileged actions.

### CP22 - API and tokens [COMPLETE]

- Documented API for site/monitor/reporting workflows.
- Scoped API tokens.
- Rotation/revocation.
- Rate limiting and auditability.

**CP30 wiring review:** scoped API tokens are persisted, hashed and revocable, but no HTTP route accepts token authentication yet (`apitokens.Authenticate` is exercised only by its unit test). Rate limiting is not implemented. CP22 reached its implementation checkpoint; wiring a deliberate token-authenticated API surface and proving scope enforcement, revocation, organization isolation and rate limiting remain CP30 obligations.

**Post-Campaign-4 truth:** a deliberate API-token surface is now wired. Routes declare token scopes in `docs/hardening/route-inventory.json`; `Authorization: Bearer` authentication grants exactly the token's scopes with the token's organization as the acting boundary; unknown/revoked/malformed tokens and missing scopes are denied; tokens cannot reach session-only routes; secrets are hashed at rest and returned only at creation; `last_used_at` is tracked. Rate limiting for login/setup landed in Campaign 3. See `docs/hardening/integrations.json`.

### CP23 - SSO/OIDC [COMPLETE]

- OIDC integration.
- Safe account linking/provisioning policy.
- Local-admin recovery path.
- Enterprise configuration documentation.

### CP24 - Worker separation and scale tests [COMPLETE]

- Optional scheduler/check worker processes.
- Optional independent analytics ingestion process.
- PostgreSQL-backed coordination.
- Load/stress tests for hundreds/thousands of sites.
- Preserve the one-binary integrated deployment as the default.

**CP30 wiring review:** the optional `serve`/`worker`/`analytics-ingest` process roles exist, but the scheduler has no claim/lease mechanism, so two worker processes would both schedule the full fleet and produce duplicate checks, crawls and incidents. "PostgreSQL-backed coordination" is not yet implemented; proving single-owner scheduling (or adding a claim/lease) is a CP30 concurrency obligation, not current behaviour.

**Post-Campaign-5 truth:** the scheduler now uses a database-backed claim/lease (`job_leases`, migration 26) with a unique owner identity, expiry and reclamation, proven equivalent on SQLite and PostgreSQL and exercised by two-worker adversarial tests.

### CP25 - High availability and larger ingestion decision gate [COMPLETE]

Measure real workload first. Only add queues, specialized analytics storage or HA coordination when evidence shows PostgreSQL/in-process buffering is insufficient.

**Exit:** document the measured limit that justified each added component. Current decision: no additional distributed component is justified; `SCALE.md` records the thresholds and CP30 measurement campaign.

## CP19/CP21-CP25 ordering review

Completing CP19 before enterprise identity was correct: the first-run database contract now exists before organizations/RBAC expand the data model. CP21 RBAC then establishes the authorization boundary used by CP22 API tokens and CP23 OIDC. CP24 process separation follows identity/storage so split processes do not invent a second product model. CP25 deliberately ends this block with a **no new infrastructure yet** decision rather than adding queues/HA without measurements.

The next checkpoint remains CP26 deployment observations. CP30 is the appropriate battle-hardening campaign for live PostgreSQL parity, OIDC provider interoperability and measured 100/1,000/10,000-site scale evidence.

## Phase 6 - integrations and release readiness

### CP26 - Deployment observations [COMPLETE]

- GitHub/webhook/API ingestion of external deployment events.
- Correlate deployments with uptime/performance/link/traffic changes.
- Web Fleet observes deployments initially; it does not become the deployment platform.

### CP27 - Notifications and integrations [COMPLETE]

- Additional notification transports based on user demand.
- Webhooks.
- Clear retry/delivery history.
- Secret handling and redaction tests.

**CP30 wiring review:** webhook CRUD and a delivery primitive exist, but no product event invokes delivery yet (`notifications.Deliver` has no callers). Webhooks will not fire until incident/check transitions are wired to delivery, and retry/signature/redaction behaviour needs end-to-end tests. CP27 reached its implementation checkpoint; wiring and proof remain CP30 obligations.

**Post-Campaign-4 truth:** webhook delivery is wired product behavior. Incident open/recover transitions write outbox rows atomically with the incident state; a background worker delivers with guarded outbound networking (public-only, dial-time re-check, no redirects), HMAC signatures, bounded retries/timeouts/output and a concurrency limit; disabled destinations never receive events; delivery history is visible. See `docs/hardening/integrations.json`.

### CP28 - Accessibility, mobile and large-fleet UX [COMPLETE]

- Keyboard navigation and screen-reader review.
- Responsive/mobile fleet management.
- Search/filter/tag/group workflows proven at large site counts.
- No page-level dashboard overflow regressions.

**CP30 wiring review:** search, group filter, pagination and the mobile navigation work, but the tag-filter input is not wired to the server (`sites.ListByTag` exists; the dashboard tag field has no handler and its state is never initialised). Native `<dialog>` markup provides baseline focus handling, but no keyboard/screen-reader audit evidence has been recorded. CP28 reached its implementation checkpoint; tag-filter wiring and an accessibility audit remain CP30 obligations.

### CP29 - Hosting and deployment documentation [COMPLETE]

- Publish substantial hosting/deployment documentation alongside the application.
- Document direct VPS/systemd deployment, reverse proxies, TLS termination, firewall/listen-address expectations, upgrades, rollback, backup/restore and PostgreSQL deployment considerations.
- Provide dedicated Caddy and nginx examples.
- Document the recommended company subdomain pattern, for example `webfleet.company.com`, including DNS, reverse proxy and HTTPS setup.
- Include a step-by-step **Set up Web Fleet on a subdomain** walkthrough starting from ownership of `company.com`: create the DNS record, configure `webfleet.company.com`, reverse proxy it to Web Fleet's private listen address, obtain/verify HTTPS, test `/healthz`, then load first-run setup.
- Show both Caddy and nginx variants for the subdomain walkthrough and explain common DNS/TLS propagation failures.
- Explain that related self-hosted tools can live independently on sibling subdomains such as `trestle.company.com`, `cortex.company.com` and `watchpost.company.com`; do not imply that Web Fleet manages or requires those products.
- Cover analytics tracker origin/CORS implications when the monitored website and Web Fleet live on different domains.
- Cover Chromium/browser requirements separately for manual Audit so ordinary monitoring deployments do not accidentally install heavyweight browser dependencies.
- Include deployment troubleshooting for 502/504 errors, TLS/certificate problems, DNS propagation, proxy headers, database connectivity and service permissions.

**Exit:** an operator can go from a fresh server and company domain to a correctly proxied HTTPS Web Fleet deployment using only the public documentation.

### CP30 - Public-preview hardening [READY FOR DEEPSEEK/CORTEX]

- Threat model and SSRF review. `SECURITY.md` defines the adversarial handoff.
- Fuzz/property tests for URL/parser/security boundaries. Initial netguard fuzz target is present; adversarial expansion remains part of the handoff.
- Fresh install and recovery rehearsal.
- SQLite and PostgreSQL matrix.
- Cross-platform application builds.
- Release provenance/checksums.
- Documentation/public website audit against actual shipped behaviour.

**Campaign 1 (complete):** project-truth correction and the identity/RBAC foundation. CP30 confirmed that RBAC was decorative (only four handlers consulted it), the fresh-install first administrator had no organization membership, organization ids were hard-coded in user-facing queries, and the first-admin setup path was not race-atomic. Campaign 1 repaired all of these: first-admin user + owner membership are now created atomically with a concurrency guard; route-level RBAC is enforced from one route table; site/group/list/fleet and audit-batch data paths filter by the acting organization resolved from membership; and the first-run restart-required state survives reload. The route/permission inventory lives in `docs/hardening/route-inventory.json`, is enforced by a route-inventory contract test that runs in isolation, and records the policy decision that operators may archive sites but only admin/owner may permanently delete them.

**Campaign 2 (complete):** the Audit/SSRF boundary. The previous browser Audit launched Chromium with `--no-sandbox` against an unguarded URL, giving Chromium a DNS/network path the Go-side guard never saw. Campaign 2 replaced that with a guarded-proxy architecture: Chromium is pinned to an in-process forward proxy (`--proxy-server`, `--proxy-bypass-list=<-loopback>`) that applies the public-network guard to every CONNECT and HTTP request, re-resolves at dial time (blocking DNS rebinding, including mixed public/private answer sets), and never follows redirects (each hop is re-validated). The browser sandbox is required by default with `--no-sandbox` only via explicit `WEBFLEET_AUDIT_SANDBOX=allow-no-sandbox`; per-audit timeout, bounded DOM output and a global concurrency semaphore bound resource use. The same fail-closed dial invariant hardens the shared monitor/crawler/TLS dialer. Evidence: `docs/hardening/audit-boundary.json`, `internal/audit/proxy_test.go`, `internal/audit/browser_test.go` (including a real-Chromium test proving traffic flows through the proxy).

**Campaign 3 (complete):** authentication, trusted proxy and OIDC. An explicit trusted-proxy model (`WEBFLEET_TRUSTED_PROXIES`) makes secure cookies, OIDC redirect URIs, externally generated URLs and client identity honor `X-Forwarded-Proto`/`X-Forwarded-For` only from configured trusted peers and ignore them from untrusted peers. Login and first-run setup are throttled per resolved client address with a bounded-memory fixed window. OIDC now uses `github.com/coreos/go-oidc` for standards-compliant discovery, keyset retrieval and ID-token verification (signature, issuer, audience, expiry), enforces one-time state consumption and nonce equality, requires a verified email, and keeps local password login as an independent recovery path. `[closure]` Each OIDC transaction is browser-bound via a short-lived HttpOnly SameSite=Lax cookie consumed atomically with the state (`DELETE ... WHERE state AND browser`), so a different browser can neither log in nor destroy the legitimate browser's transaction; the callback origin is canonical and fail-closed, coming only from `WEBFLEET_PUBLIC_URL` (required to enable/use OIDC). A standards-shaped local provider simulation covers the adversarial cases; real-provider interoperability remains an external CP30 gate.

**Campaign 4 (complete and reviewed):** API tokens and integrations. A deliberate API-token surface is wired through the route inventory (`tokenScopes`): Bearer authentication grants exactly the token's scopes with the token's organization as the acting boundary, tokens cannot reach session-only routes, secrets are hashed and returned only at creation, and unknown/revoked/missing-scope cases are denied; failed Bearer auth is throttled per trusted-proxy-resolved client address and error responses are single-JSON. Webhook delivery is real product behavior: incident open/recover transitions write an outbox atomically with the incident state **scoped to the site's own organization**, and a background worker delivers with guarded outbound networking, HMAC signatures with a stable `event_id`, bounded retries/timeouts/output and a concurrency limit; disabled destinations never receive events; the obsolete synchronous `Service.Deliver` was removed so the guarded worker is the only delivery boundary. See `docs/hardening/integrations.json`.

**Campaign 5 (implemented, pending review):** PostgreSQL parity, provider-aware backup and scheduler claim/lease. A real-PostgreSQL integration suite runs the product's normal storage paths against a live server (guarded by `WEBFLEET_TEST_POSTGRES_URL`) and caught two genuine dialect bugs (Go bools bound to INTEGER columns, and an unqualified conflict-update RHS); both are fixed at the shared database layer. Backup/restore is provider-aware: SQLite keeps its native path, PostgreSQL uses pg_dump/pg_restore with credentials passed via `PGPASSWORD` (never argv), temp-file security, bounded subprocesses, clean tool-absence detection and destructive-change rehearsals for both providers. The scheduler now uses a database claim/lease (`job_leases`) so two workers cannot perform the same due work, with unique owners, expiry/reclamation, and equivalent SQLite/PostgreSQL semantics proven by two-worker tests. Evidence: `docs/hardening/database.json`. CP30 remains blocked on Campaigns 6-8.

### CP31 - Stable public preview [BLOCKED ON CP30]

- Signed/tagged release. Do not create it until every `RELEASE.md` evidence gate passes.
- Install/update instructions tested by an ordinary-user dogfood pass.
- Public website points only at real release artifacts and truthful features.
- Known limitations published.

## Explicitly deferred

Do not pull these forward without a strong product reason:

- website hosting;
- DNS management;
- CDN/proxy service;
- deployment execution/PaaS;
- session replay;
- heatmaps;
- invasive visitor fingerprinting;
- arbitrary product-analytics cohorts;
- advertising attribution stack;
- host CPU/RAM/disk monitoring (Watchpost territory);
- mandatory agents;
- mandatory distributed infrastructure for small installs.
