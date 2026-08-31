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

**Completed:** Go application foundation, versioned SQLite schema, Nift-built embedded dashboard, configuration, structured logging, graceful shutdown, `/healthz`, and unit/integration coverage. SQLite now follows the same storage pattern as Trestle: `database/sql`, the cgo-free `modernc.org/sqlite` driver, one owned SQLite connection, WAL, foreign-key enforcement, a five-second busy timeout, secured data-directory permissions, validated migration history and transactional migrations. A `sandbox_sqlite`-tagged system-SQLite driver exists only so the network-isolated development sandbox can exercise the same `database/sql` contract when module downloads are unavailable; it is not the normal build path.

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

**Ordering review after CP10:** CP11 remains next. Completing performance history closes the core website-health phase before analytics begins. CP12-CP15 remain together because tracker ingestion, storage/rollups, dashboarding, then events/goals form one coherent privacy-first analytics slice. CP16 backup/restore remains a release gate and may be pulled ahead of analytics only if monitoring-only dogfooding begins before the analytics phase is ready.

### CP11 - Performance history

- Stable server-side timing metrics first.
- Response-size and transfer observations.
- Regression thresholds/baselines.
- Avoid presenting synthetic server timing as browser Core Web Vitals.
- Evaluate optional browser-based checks only after the base feature is useful.

**Phase 2 exit:** Web Fleet understands failures and regressions specific to websites, not merely hosts.

## Phase 3 - privacy-first analytics

### CP12 - Analytics property and tracker

- Optional analytics property per site.
- Tiny cacheable tracker script.
- Ingestion endpoint with origin/property validation and rate limits.
- Pageview event contract.
- No-cookie default.
- Privacy review of every collected field.

### CP13 - Analytics storage and rollups

- Raw-event retention policy.
- Privacy-preserving visitor estimation.
- Hour/day aggregate tables.
- Top pages, sources/referrers and coarse client/geography dimensions.
- Bot filtering.
- Performance/load tests for SQLite-scale deployments.

### CP14 - Analytics dashboard

- Today/7d/30d/custom ranges.
- Visitors, pageviews, top pages, sources, countries and device/browser classes.
- Recent activity.
- Fleet-wide traffic overview.
- Clear "analytics not installed" onboarding for sites without the tracker.

### CP15 - Events and goals

- Custom event API.
- Goal definitions.
- Event/goal dashboards.
- Keep arbitrary user profiling out of scope.

**Phase 3 exit:** Web Fleet offers useful privacy-first analytics to static sites, including GitHub Pages, without needing to host those sites.

## Phase 4 - self-hosting and operational reliability

### CP16 - Backup and restore

- Consistent SQLite backup.
- Restore workflow with safety checks.
- Configuration/data export where appropriate.
- Documented disaster-recovery test.

### CP17 - Service install/update/rollback

- Linux systemd install path with clear privilege/ownership model.
- Release artifact verification.
- Update and rollback workflow.
- Fresh-install, upgrade and rollback tests.
- Keep non-Linux application builds compiling even if service installation is platform-specific.

### CP18 - PostgreSQL

- Storage abstraction only where necessary.
- PostgreSQL migrations and integration tests.
- Behavioural equivalence for core monitoring/auth/analytics paths.
- Migration/import story from SQLite where feasible.

### CP19 - Retention and maintenance

- Check-history retention/compaction.
- Analytics raw-event retention.
- Database maintenance jobs.
- Disk-usage visibility and guardrails.

**Phase 4 exit:** ordinary self-hosters can install, update, back up, restore and operate Web Fleet confidently.

## Phase 5 - multi-user, agency and enterprise scale

### CP20 - Users, organizations and RBAC

- Multiple users.
- Organization membership.
- Roles/permissions scoped to organizations/groups/sites.
- Agency/client-friendly grouping.
- Audit logs for privileged actions.

### CP21 - API and tokens

- Documented API for site/monitor/reporting workflows.
- Scoped API tokens.
- Rotation/revocation.
- Rate limiting and auditability.

### CP22 - SSO/OIDC

- OIDC integration.
- Safe account linking/provisioning policy.
- Local-admin recovery path.
- Enterprise configuration documentation.

### CP23 - Worker separation and scale tests

- Optional scheduler/check worker processes.
- Optional independent analytics ingestion process.
- PostgreSQL-backed coordination.
- Load/stress tests for hundreds/thousands of sites.
- Preserve the one-binary integrated deployment as the default.

### CP24 - High availability and larger ingestion decision gate

Measure real workload first. Only add queues, specialized analytics storage or HA coordination when evidence shows PostgreSQL/in-process buffering is insufficient.

**Exit:** document the measured limit that justified each added component.

## Phase 6 - integrations and release readiness

### CP25 - Deployment observations

- GitHub/webhook/API ingestion of external deployment events.
- Correlate deployments with uptime/performance/link/traffic changes.
- Web Fleet observes deployments initially; it does not become the deployment platform.

### CP26 - Notifications and integrations

- Additional notification transports based on user demand.
- Webhooks.
- Clear retry/delivery history.
- Secret handling and redaction tests.

### CP27 - Accessibility, mobile and large-fleet UX

- Keyboard navigation and screen-reader review.
- Responsive/mobile fleet management.
- Search/filter/tag/group workflows proven at large site counts.
- No page-level dashboard overflow regressions.

### CP28 - Public-preview hardening

- Threat model and SSRF review.
- Fuzz/property tests for URL/parser/security boundaries.
- Fresh install and recovery rehearsal.
- SQLite and PostgreSQL matrix.
- Cross-platform application builds.
- Release provenance/checksums.
- Documentation/public website audit against actual shipped behaviour.

### CP29 - Stable public preview

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
