#!/usr/bin/env bash
# Smoke test of the Linux binary (ADR 0030): start it on a fresh data
# directory as an ordinary user, create the first account with the setup
# code, make a token, and check what it reports about this host.
# Usage: scripts/linux-smoke.sh [dist/netdeployd-linux-amd64]
# NETDEPLOY_SMOKE_NO_TOOLS=1 skips the wimlib and cabextract checks (a host
# without wimtools and cabextract installed).
set -euo pipefail
bin="${1:-dist/netdeployd-linux-amd64}"
data="$(mktemp -d)"
base="https://127.0.0.1:18443/api/v1"
origin="https://127.0.0.1:18443"
jar="$data/cookies"
log="$data/server.log"
fail() { echo "FAIL: $*" >&2; echo "--- server log" >&2; cat "$log" >&2; exit 1; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {'d': d}))" "$1"; }

"$bin" -data "$data" -admin-addr 127.0.0.1:18443 >"$log" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null || true; rm -rf "$data"' EXIT
for _ in $(seq 1 50); do
  curl -sk "$base/auth/session" >/dev/null 2>&1 && break
  sleep 0.2
done

[ "$(stat -c %a "$data")" = 700 ] || fail "data directory mode $(stat -c %a "$data")"
[ "$(stat -c %a "$data/setup-code.txt")" = 600 ] || fail "setup-code.txt mode"
code="$(sed -n 2p "$data/setup-code.txt")"
grep -q "setup_code=$code" "$log" || fail "setup code not in the log"

[ "$(curl -sk "$base/auth/session" | json "d['setupRequired']")" = True ] || fail "setupRequired"
[ "$(curl -sk -o /dev/null -w '%{http_code}' "$base/clients")" = 401 ] || fail "clients without a session"

curl -sk -c "$jar" -H 'Content-Type: application/json' -H "Origin: $origin" \
  --data "{\"setupCode\":\"$code\",\"username\":\"ci\",\"password\":\"correct horse battery\"}" "$base/auth/setup" >/dev/null
[ ! -e "$data/setup-code.txt" ] || fail "setup-code.txt left behind"
token="$(curl -sk -b "$jar" -H 'Content-Type: application/json' -H "Origin: $origin" --data '{"name":"ci"}' "$base/tokens" | json "d['secret']")"
auth=(-H "Authorization: Bearer $token")

caps="$(curl -sk "${auth[@]}" "$base/system/capabilities")"
echo "capabilities: $caps"
[ "$(echo "$caps" | json "d['hostOs']")" = linux ] || fail "hostOs"
if [ -z "${NETDEPLOY_SMOKE_NO_TOOLS:-}" ]; then
  [ "$(echo "$caps" | json "[b['available'] for b in d['builders'] if b['name']=='wimlib'][0]")" = True ] || fail "wimlib not available"
  [ "$(echo "$caps" | json "d['cabLzx']['available']")" = True ] || fail "cabextract not found"
fi
[ "$(echo "$caps" | json "[b['available'] for b in d['builders'] if b['name']=='dism'][0]")" = False ] || fail "DISM on Linux"
[ "$(echo "$caps" | json "d['privileges']['lowPorts']")" = False ] || fail "an ordinary user may bind low ports?"
echo "$caps" | json "d['privileges']['missing']" | grep -q CAP_NET_BIND_SERVICE || fail "CAP_NET_BIND_SERVICE not reported missing"

health="$(curl -sk "${auth[@]}" "$base/system/health")"
echo "health: $health"
[ "$(echo "$health" | json "d['status']")" = ok ] || fail "health status"
echo "$health" | grep -q data_dir_permissions && fail "data directory reported open"
echo "Linux smoke test passed."
