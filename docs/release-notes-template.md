# Web Fleet {{VERSION}}

Web Fleet {{VERSION}} is a {{RELEASE_KIND}}.

## What this release is

- One self-hosted Go executable with an embedded dashboard, SQLite or
  PostgreSQL storage, and a versioned HTTP boundary.
- Website monitoring (uptime, response behaviour, TLS, DNS, links, headers,
  performance), manual browser-rendered audits, analytics, incidents,
  notifications, RBAC, API tokens and OIDC sign-in.
- Checksum-verified release archives for Linux, macOS and Windows on amd64 and
  arm64, plus `checksums.txt` and GitHub build-provenance attestations.

## Operator responsibilities

- Back up before upgrading.
- Read the known limitations before relying on Web Fleet in production.
- Web Fleet is a public preview for evaluation and early self-hosting; it is
  not claimed to be production-proven or battle-proven.

## Installation

- https://webfleet.cv/install.sh (per-user)
- https://webfleet.cv/download.sh (download the archive)
- https://webfleet.cv/update.sh (upgrade an existing install)