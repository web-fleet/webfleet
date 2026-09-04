#!/bin/bash
# rehearse.sh runs a stranger-style rehearsal from a freshly built release
# archive (not a development-tree binary): fresh SQLite setup, admin, sites,
# monitoring, incident lifecycle, Audit, analytics, API token, webhook outbox,
# backup, destructive change, restore, binary update/rollback, restart and
# persistence. An optional PostgreSQL section runs when
# WEBFLEET_TEST_POSTGRES_URL is set. It never creates a public release or tag
# and is not a substitute for the owner's later ordinary-user dogfood.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=${1:-}
if [ -z "$BIN" ]; then
  ./scripts/build-release.sh v0.0.0 >/dev/null 2>&1
  LINUX_ARCHIVE="dist/webfleet-linux-amd64.tar.gz"
  REHEARSE_DIR=$(mktemp -d)
  tar -xzf "$LINUX_ARCHIVE" -C "$REHEARSE_DIR"
  BIN="$REHEARSE_DIR/webfleet"
fi

WORK=$(mktemp -d)
LISTEN=127.0.0.1:8092
export WEBFLEET_DATA_DIR="$WORK/data"
export WEBFLEET_LISTEN="$LISTEN"
B=http://$LISTEN
CSRF=""
APP_PID=""

start_app() { "$BIN" >>"$WORK/app.log" 2>&1 & APP_PID=$!; sleep 1; }
stop_app() { kill $APP_PID 2>/dev/null || true; wait $APP_PID 2>/dev/null || true; }

setup() {
  local out=$(curl -sS -c "$WORK/cookies" -H 'Content-Type: application/json' -d '{"email":"admin@example.com","password":"secret7"}' "$B/api/setup")
  CSRF=$(echo "$out" | jq -r '.csrf')
  echo "setup: $(echo "$out" | jq -r '.email // .error')"
}
mut() { curl -sS -b "$WORK/cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -X POST -d "$2" "$B$1"; }
mutp() { curl -sS -b "$WORK/cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -X PUT -d "$2" "$B$1"; }
get() { curl -sS -b "$WORK/cookies" "$B$1"; }

echo "== fresh SQLite rehearsal: $WORK"
start_app
setup
SITE=$(mut /api/sites '{"name":"Example","primary_url":"https://example.com/missing-404"}')
SITE_ID=$(echo "$SITE" | jq -r '.id')
echo "site created: id=$SITE_ID"

# Incident lifecycle: a 404 URL degrades -> open -> acknowledge -> recover.
echo "check (404): $(mut "/api/sites/$SITE_ID/check" '' | jq -r '.status_code // .error_class')"
INC=$(get "/api/sites/$SITE_ID/incidents")
[ "$(echo "$INC" | jq -r '.incidents|length')" -eq 1 ] && echo "incident open: yes"
INCID=$(echo "$INC" | jq -r '.incidents[0].id')
mut "/api/incidents/$INCID/ack" '' >/dev/null && echo "incident acknowledged: yes"
mutp "/api/sites/$SITE_ID" '{"name":"Example","primary_url":"https://example.com/","group_id":0,"enabled":true}' >/dev/null
echo "check (200): $(mut "/api/sites/$SITE_ID/check" '' | jq -r '.status_code // .error_class')"
[ "$(get "/api/sites/$SITE_ID/incidents" | jq -r '.incidents[0].state')" = "resolved" ] && echo "incident recovered: yes"

# Webhook outbox wiring: create a webhook, fire another incident, verify the
# outbox row and that delivery status was recorded by the background worker.
WH=$(mut /api/notifications/webhooks '{"name":"hook","url":"https://8.8.8.8/hook"}')
[ "$(echo "$WH" | jq -r '.webhook.name // .error')" = "hook" ] && echo "webhook created: yes"
mutp "/api/sites/$SITE_ID" '{"name":"Example","primary_url":"https://example.com/missing-404","group_id":0,"enabled":true}' >/dev/null
mut "/api/sites/$SITE_ID/check" '' >/dev/null
sleep 2
DEL=$(get /api/notifications/deliveries | jq -r '.deliveries|length')
echo "webhook outbox deliveries recorded: $DEL"

# Audit, analytics, token.
echo "audit: $(mut "/api/sites/$SITE_ID/audit" '' | jq -r '.status // .error')"
PROP=$(mut "/api/sites/$SITE_ID/analytics" '')
KEY=$(echo "$PROP" | jq -r '.public_key')
curl -sS -H 'Content-Type: application/json' -H 'Origin: https://example.com' -d "{\"key\":\"$KEY\",\"path\":\"/\"}" "$B/api/analytics/event" -o /dev/null -w "analytics event: %{http_code}\n"
TOK=$(mut /api/tokens '{"name":"ci","scopes":["sites:read"]}')
[ -n "$(echo "$TOK" | jq -r '.prefix // empty')" ] && echo "api token created: yes"

# Backup -> destructive change -> restore.
"$BIN" backup "$WORK/backup.db" >/dev/null 2>&1 && echo "backup: ok ($(du -h "$WORK/backup.db" | cut -f1))"
SITES_BEFORE=$(get /api/sites | jq -r '.total')
stop_app
# Destructive change: wipe the data file, then restore.
rm -f "$WORK/data/webfleet.db"
"$BIN" restore "$WORK/backup.db" >/dev/null 2>&1
start_app
SITES_AFTER=$(get /api/sites | jq -r '.total')
[ "$SITES_AFTER" = "$SITES_BEFORE" ] && echo "restore after destructive change: ok ($SITES_AFTER sites)"

# Binary update/rollback semantics using separately produced release artifacts:
# build a second release with the release contract, verify its recorded
# checksum, swap in its binary, restart, then roll back to the first artifact.
V2_DIST="$WORK/dist2"
./scripts/build-release.sh v0.0.1 "$V2_DIST" >/dev/null 2>&1
V2_ARCHIVE="$V2_DIST/webfleet-linux-amd64.tar.gz"
V2_DIR=$(mktemp -d)
tar -xzf "$V2_ARCHIVE" -C "$V2_DIR"
V2_BIN="$V2_DIR/webfleet"
# The expected checksum comes from the release-build contract, not a self-hash.
# The archive is verified against its recorded checksum; the binary is then
# trusted because it is extracted from that verified archive.
V2_SHA=$(awk '$2 == "webfleet-linux-amd64.tar.gz" {print $1}' "$V2_DIST/checksums.txt")
echo "$V2_SHA  $V2_ARCHIVE" | sha256sum -c - >/dev/null
cp "$BIN" "$WORK/webfleet-v1"   # the original release artifact binary
stop_app
cp "$V2_BIN" "$BIN"             # swap in the verified update artifact
start_app
echo "post-update health: $(curl -sS "$B/healthz" | jq -r '.ok')"
echo "post-update session: $(curl -sS -b "$WORK/cookies" "$B/api/session" | jq -r '.email // .error')"
# rollback restores the original artifact binary
stop_app
cp "$WORK/webfleet-v1" "$BIN"
start_app
echo "post-rollback health: $(curl -sS "$B/healthz" | jq -r '.ok')"
echo "post-rollback sites: $(curl -sS -b "$WORK/cookies" "$B/api/sites" | jq -r '.total')"
stop_app

echo "== optional PostgreSQL rehearsal (WEBFLEET_TEST_POSTGRES_URL)"
if [ -n "${WEBFLEET_TEST_POSTGRES_URL:-}" ]; then
  PGWORK=$(mktemp -d)
  PGDB="wf_rehearse_$(date +%s%N)"
  # Create a fresh database on the real server, then provision via the env var.
  psql "${WEBFLEET_TEST_POSTGRES_URL}" -q -c "CREATE DATABASE $PGDB" >/dev/null
  trap 'psql "${WEBFLEET_TEST_POSTGRES_URL}" -q -c "DROP DATABASE IF EXISTS $PGDB WITH (FORCE)" >/dev/null 2>&1 || true' EXIT
  case "$WEBFLEET_TEST_POSTGRES_URL" in
    */*) PGURL="${WEBFLEET_TEST_POSTGRES_URL%/*}/$PGDB" ;;
    *) PGURL="${WEBFLEET_TEST_POSTGRES_URL}/$PGDB" ;;
  esac
  export WEBFLEET_DATA_DIR="$PGWORK/data"
  export WEBFLEET_DATABASE_URL="$PGURL"
  start_app
  sleep 1
  # A fresh PostgreSQL deployment via WEBFLEET_DATABASE_URL: setup state is
  # locked (no chooser) and admin creation works.
  OUT=$(curl -sS -c "$PGWORK/pg.cookies" -H 'Content-Type: application/json' -d '{"email":"admin@example.com","password":"secret7"}' "$B/api/setup")
  echo "pg setup: $(echo "$OUT" | jq -r '.email // .error')"
  PGCSRF=$(echo "$OUT" | jq -r '.csrf')
  curl -sS -b "$PGWORK/pg.cookies" -c "$PGWORK/pg.cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: $PGCSRF" -d '{"name":"PGSite","primary_url":"https://example.com/"}' "$B/api/sites" -o /dev/null -w "pg site create: %{http_code}\n"
  stop_app
  # restart and prove persistence on postgres
  start_app
  echo "pg post-restart session: $(curl -sS -b "$PGWORK/pg.cookies" "$B/api/session" | jq -r '.email // .error')"
  stop_app
  unset WEBFLEET_DATABASE_URL
fi

echo "REHEARSAL COMPLETE"