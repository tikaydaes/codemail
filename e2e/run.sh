#!/bin/sh
# Builds the server, starts it on a fresh data directory in dev mode and
# drives the web app in a headless browser with a virtual passkey.
# First use: (cd e2e && npm install && npx playwright install chromium)
set -e
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'kill $pid 2>/dev/null; rm -rf "$tmp"' EXIT
(cd .. && go build -o "$tmp/codemail" ./cmd/codemail)
"$tmp/codemail" serve -dev -data "$tmp/data" 2>"$tmp/serve.log" &
pid=$!
sleep 1.5
url=$(grep -o 'http://localhost:8080/#/setup/[A-Za-z0-9_-]*' "$tmp/serve.log")
rm -rf shots && mkdir shots
node run.mjs "$url" | tee "$tmp/out.log"
! grep -q '^FAIL' "$tmp/out.log"
