#!/bin/sh
# Builds the server and the extension, starts the server on a fresh data
# directory, and tests the extension in Firefox.
# First use: (cd e2e && npm install && needs Firefox installed)
set -e
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'kill $pid 2>/dev/null; rm -rf "$tmp"' EXIT
(cd .. && go build -o "$tmp/codemail" ./cmd/codemail)
(cd ../extension && node build.mjs --dev >/dev/null)
port=${E2E_PORT:-8080}
"$tmp/codemail" serve -dev -dev-trust-senders -http ":$port" -data "$tmp/data" 2>"$tmp/serve.log" &
pid=$!
sleep 1.5
url=$(grep -o "http://localhost:$port/#/setup/[A-Za-z0-9_-]*" "$tmp/serve.log")
rm -rf shots && mkdir shots
node firefox.mjs "$url" | tee "$tmp/out.log"
! grep -q '^FAIL' "$tmp/out.log"
