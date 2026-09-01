#!/bin/bash
# rehearse.sh runs a stranger-style rehearsal from a freshly built release
# archive (not a development-tree binary): unpack, fresh SQLite setup, admin
# creation, sites/groups/tags, monitoring check, fleet, Audit, analytics,
# API token, webhook, backup, restart and persistence. It never creates a
# public release or tag.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=${1:-}
if [ -z "$BIN" ]; then
  echo "building release archive..." >/dev/null
  ./scripts/release.sh >/dev/null 2>&1
  LINUX_ARCHIVE=$(ls dist/webfleet_*_linux_amd64.tar.gz | head -1)
  REHEARSE_DIR=$(mktemp -d)
  tar -xzf "$LINUX_ARCHIVE" -C "$REHEARSE_DIR"
  BIN="$REHEARSE_DIR"/$(basename "$LINUX_ARCHIVE" .tar.gz)/webfleet
fi

WORK=$(mktemp -d)
LISTEN=127.0.0.1:8092
export WEBFLEET_DATA_DIR="$WORK/data"
export WEBFLEET_LISTEN="$LISTEN"

echo "== starting from a fresh data dir: $WORK"
"$BIN" >"$WORK/app.log" 2>&1 &
APP_PID=$!
trap 'kill $APP_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT
sleep 1

B=http://$LISTEN
J='-sS'
CSRF=""

setup() {
  local out
  out=$(curl $J -c "$WORK/cookies" -H 'Content-Type: application/json' -d '{"email":"admin@example.com","password":"secret7"}' "$B/api/setup")
  CSRF=$(echo "$out" | jq -r '.csrf')
  echo "setup: $(echo "$out" | jq -r '.email // .error') (csrf ${CSRF:0:6}…)"
}
login() {
  local out
  out=$(curl $J -b "$WORK/cookies" -c "$WORK/cookies" -H 'Content-Type: application/json' -d '{"email":"admin@example.com","password":"secret7"}' "$B/api/login")
  CSRF=$(echo "$out" | jq -r '.csrf')
  echo "login: $(echo "$out" | jq -r '.email // .error')"
}
mut() {
  curl $J -b "$WORK/cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -X POST -d "$2" "$B$1"
}
mutp() { # PUT
  curl $J -b "$WORK/cookies" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -X PUT -d "$2" "$B$1"
}
get() { curl $J -b "$WORK/cookies" "$B$1"; }

setup
login
SITE=$(mut /api/sites '{"name":"Example","primary_url":"https://example.com"}')
SITE_ID=$(echo "$SITE" | jq -r '.id')
echo "site created: id=$SITE_ID"
GROUP=$(mut /api/groups '{"name":"Clients"}')
GID=$(echo "$GROUP" | jq -r '.id')
mutp "/api/sites/$SITE_ID" "{\"name\":\"Example\",\"primary_url\":\"https://example.com\",\"group_id\":$GID,\"enabled\":true}" >/dev/null
mutp "/api/sites/$SITE_ID/tags" '{"tags":["prod"]}' >/dev/null
echo "group/tags set"
echo "list: $(get '/api/sites?tag=prod' | jq -r '.total') site(s) tagged prod"
echo "fleet: $(get /api/fleet | jq -r '.total') site(s)"
echo "check now: $(mut "/api/sites/$SITE_ID/check" '' | jq -r '.status_code // .error_class')"
echo "incidents: $(get "/api/sites/$SITE_ID/incidents" | jq -r '.incidents|length')"
echo "audit: $(mut "/api/sites/$SITE_ID/audit" '' | jq -r '.status // .error')"
PROP=$(mut "/api/sites/$SITE_ID/analytics" '')
KEY=$(echo "$PROP" | jq -r '.public_key')
echo "analytics key: ${KEY:0:8}…"
curl $J -H 'Content-Type: application/json' -H 'Origin: https://example.com' -d "{\"key\":\"$KEY\",\"path\":\"/\"}" "$B/api/analytics/event" -o /dev/null -w "analytics event: %{http_code}\n"
echo "analytics summary: $(get "/api/sites/$SITE_ID/analytics/summary?days=7" | jq -r '.pageviews') pageview(s)"
TOK=$(mut /api/tokens '{"name":"ci","scopes":["sites:read"]}')
echo "token: $(echo "$TOK" | jq -r '.prefix')…"
WH=$(mut /api/notifications/webhooks '{"name":"hook","url":"https://8.8.8.8/hook"}')
echo "webhook: $(echo "$WH" | jq -r '.webhook.name // .error')"
"$BIN" backup "$WORK/backup.db" >/dev/null 2>&1 && echo "backup: ok ($(du -h "$WORK/backup.db" | cut -f1))"

echo "== restarting and verifying persistence"
kill $APP_PID; wait $APP_PID 2>/dev/null || true
"$BIN" >"$WORK/app2.log" 2>&1 &
APP_PID=$!
sleep 1
echo "session after restart: $(curl $J -b "$WORK/cookies" "$B/api/session" | jq -r '.email // .error')"
echo "sites after restart: $(curl $J -b "$WORK/cookies" "$B/api/sites" | jq -r '.total')"
echo "REHEARSAL COMPLETE"