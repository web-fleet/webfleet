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
3. **Monitoring first.** Uptime, response behaviour, TLS, DNS, links, headers, performance and change history are the core product.
4. **Analytics is first-class but optional.** Public checks work without site changes. Analytics begins only after the operator installs the lightweight tracker.
5. **Privacy by default.** Do not collect visitor data merely because other analytics products do. Avoid persistent raw IP storage, fingerprinting and unnecessary cookies.
6. **Actionable dashboard.** Prefer "what needs attention" over walls of charts.
7. **Self-hosted ownership.** Configuration, backups, upgrades, rollback and data export must remain understandable.
8. **Simple default, scalable internals.** SQLite and an integrated process are valid defaults. PostgreSQL, split workers and larger ingestion paths can be added without changing the product model.
9. **No fake enterprise complexity.** RBAC, SSO, audit, retention and scale controls should appear when they solve real organizational requirements.
10. **Truthful public claims.** Documentation and the public website must distinguish shipped functionality from planned functionality throughout development.

## Initial technical direction

- Go application server and workers.
- SQLite default storage.
- PostgreSQL supported before broad production positioning.
- Nift-built HTML/CSS/JavaScript dashboard with vanilla JavaScript unless a concrete requirement justifies another frontend stack.
- Background scheduler for remote checks.
- HTTP/HTTPS checks performed agentlessly from the Web Fleet server.
- Analytics ingestion exposed as a separate HTTP endpoint inside the same application initially.
- Analytics subsystem kept architecturally separable from monitoring/crawling.
- One deployable binary for the normal installation.
- Optional process separation later for large installations.

Do not introduce Redis, Kafka, ClickHouse, Kubernetes, a separate frontend framework, or a service mesh into the default architecture without measured need.

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
- Are there fleet-wide TLS, DNS, link, performance or analytics anomalies?

A site detail view should eventually expose:

- Overview
- Availability
- Performance
- Pages and links
- TLS
- DNS
- Headers
- Analytics
- Deployments
- Incidents
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
- basic metadata/structured-data checks where useful;
- regression/change detection.

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
- Before declaring a checkpoint complete, run the relevant tests plus `git diff --check` and report the exact commit.

## Nift usage

Nift is the build-time templating/dependency layer for Web Fleet's frontend and public website. Use it for composition, tracked relationships and generated static assets where it helps. Use normal Go/JavaScript/CSS for application behaviour.

Use `@pathto` for internal tracked-page and local-asset relationships, and `@input` for shared reusable template fragments. Do not hand-maintain generated output.

## Current state

As of this handover revision:

- GitHub organization: `web-fleet`.
- Main application repository: `web-fleet/webfleet`.
- CP1-CP5 are complete: application/auth/inventory plus SSRF-bounded monitoring, scheduling and fleet/site health views are implemented.
- The public website is being established in the companion `web-fleet.github.io` source/generated workspace.
- The next implementation checkpoint is CP6 in `ROADMAP.md`.

## Immediate next step

Continue with CP6 incident history and alert foundations. Preserve one incident across a failure period, close it on recovery, and keep notification transport deliberately simple.
