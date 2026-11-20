#!/usr/bin/env bash
# Install, reconfigure, purge and reinstall the .deb (ADR 0031) on a machine
# with systemd (the CI runner). Destructive: only on a throwaway machine.
# Usage: scripts/deb-smoke.sh dist/netdeploy_<version>_amd64.deb
set -euo pipefail
deb="$(realpath "$1")"
data=/var/lib/netdeploy
base="https://127.0.0.1:8443/api/v1"
origin="https://127.0.0.1:8443"
jar="$(mktemp)"
fail() { echo "FAIL: $*" >&2; sudo journalctl -u netdeploy -n 50 --no-pager >&2 || true; exit 1; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {'d': d}))" "$1"; }
wait_up() {
  for _ in $(seq 1 50); do curl -sk "$base/auth/session" >/dev/null 2>&1 && return 0; sleep 0.2; done
  fail "admin API not answering"
}
install() { sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "$deb" >/tmp/deb-install.log 2>&1 || { cat /tmp/deb-install.log; fail "install"; }; }

echo "--- install"
install
grep -q "setup code" /tmp/deb-install.log || fail "postinst didn't print the setup code"
systemctl is-active --quiet netdeploy || fail "service not active"
[ "$(sudo stat -c '%a %U' $data)" = "700 netdeploy" ] || fail "data directory $(sudo stat -c '%a %U' $data)"
wait_up
# All networks: Go listens dual-stack, so ss shows *:8443 or [::]:8443 (or 0.0.0.0:8443).
ss -ltnH 'sport = :8443' | awk '{print $4}' | grep -Eq '^(\*|0\.0\.0\.0|\[::\]):8443$' ||
  fail "admin not on all networks (the debconf default): $(ss -ltnH 'sport = :8443' | awk '{print $4}')"

code="$(sudo sed -n 2p $data/setup-code.txt)"
curl -sk -c "$jar" -H 'Content-Type: application/json' -H "Origin: $origin" \
  --data "{\"setupCode\":\"$code\",\"username\":\"ci\",\"password\":\"correct horse battery\"}" "$base/auth/setup" >/dev/null
token="$(curl -sk -b "$jar" -H 'Content-Type: application/json' -H "Origin: $origin" --data '{"name":"ci"}' "$base/tokens" | json "d['secret']")"
auth=(-H "Authorization: Bearer $token")

caps="$(curl -sk "${auth[@]}" "$base/system/capabilities")"
echo "capabilities: $caps"
[ "$(echo "$caps" | json "d['privileges']['lowPorts']")" = True ] || fail "the unit's CAP_NET_BIND_SERVICE"
[ "$(echo "$caps" | json "d['privileges']['icmp']")" = raw ] || fail "the unit's CAP_NET_RAW"
[ "$(echo "$caps" | json "[b['available'] for b in d['builders'] if b['name']=='wimlib'][0]")" = True ] || fail "wimtools (a dependency)"
[ "$(echo "$caps" | json "sorted(d['httpBoot']['architectures'])")" = "['arm64', 'x64']" ] || fail "the iPXE chain in /usr/lib/netdeploy/ipxe"
health="$(curl -sk "${auth[@]}" "$base/system/health")"
[ -n "$(echo "$health" | json "d.get('version', '')")" ] || fail "health has no version"
echo "$health" | grep -q data_dir_permissions && fail "data directory reported open"
[ "$(sudo stat -c %a $data/netdeploy.db)" = 600 ] || fail "netdeploy.db mode"

echo "--- reconfigure: this computer only, port 9443"
echo "netdeploy netdeploy/admin-access select this computer only" | sudo debconf-set-selections
echo "netdeploy netdeploy/admin-port string 9443" | sudo debconf-set-selections
sudo DEBIAN_FRONTEND=noninteractive dpkg-reconfigure netdeploy >/dev/null
for _ in $(seq 1 50); do ss -ltnH 'sport = :9443' | grep -q '127.0.0.1:9443' && break; sleep 0.2; done
ss -ltnH 'sport = :9443' | awk '{print $4}' | grep -qx '127.0.0.1:9443' ||
  fail "admin not moved to 127.0.0.1:9443: $(ss -ltnH | awk '{print $4}' | grep -E ':(8443|9443)$')"
ss -ltnH 'sport = :8443' | grep -q . && fail "still listening on 8443 after the move"

echo "--- purge"
sudo DEBIAN_FRONTEND=noninteractive apt-get purge -y netdeploy >/tmp/deb-purge.log 2>&1 || { cat /tmp/deb-purge.log; fail "purge"; }
[ ! -e $data ] || fail "data directory still there after purge"
kept="$(ls -d $data.purged-* | head -1)"
[ -n "$kept" ] && sudo test -e "$kept/netdeploy.db" || fail "data not moved aside"
grep -q "moved to $kept" /tmp/deb-purge.log || fail "purge didn't say where the data went"

echo "--- reinstall: starts fresh"
echo "netdeploy netdeploy/admin-access select all networks" | sudo debconf-set-selections
echo "netdeploy netdeploy/admin-port string 8443" | sudo debconf-set-selections
install
wait_up
[ "$(curl -sk "$base/auth/session" | json "d['setupRequired']")" = True ] || fail "reinstall didn't start fresh"
echo "deb smoke test passed."
