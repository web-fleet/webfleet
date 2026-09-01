#!/bin/sh
# scale.sh runs the CP30 large-fleet measurements and prints them.
set -eu
cd "$(dirname "$0")/.."
WEBFLEET_SCALE=1 go test ./internal/sites/ -run 'TestScaleReport$' -count=1 -v "$@"
if [ -n "${WEBFLEET_TEST_POSTGRES_URL:-}" ]; then
  echo "running PostgreSQL scale comparison against WEBFLEET_TEST_POSTGRES_URL..."
  WEBFLEET_SCALE=1 go test ./internal/sites/ -run TestScaleReportPostgres -count=1 -v
fi