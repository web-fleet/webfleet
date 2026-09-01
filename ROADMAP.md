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

### CP28 - Accessibility, mobile and large-fleet UX

- Keyboard navigation and screen-reader review.
- Responsive/mobile fleet management.
- Search/filter/tag/group workflows proven at large site counts.
- No page-level dashboard overflow regressions.

### CP29 - Hosting and deployment documentation

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

### CP30 - Public-preview hardening

- Threat model and SSRF review.
- Fuzz/property tests for URL/parser/security boundaries.
- Fresh install and recovery rehearsal.
- SQLite and PostgreSQL matrix.
- Cross-platform application builds.
- Release provenance/checksums.
- Documentation/public website audit against actual shipped behaviour.

### CP31 - Stable public preview

- Signed/tagged release.
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
