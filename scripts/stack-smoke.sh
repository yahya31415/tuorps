#!/usr/bin/env bash
# stack-smoke.sh — end-to-end checks against a running stack
# (make dev-secrets up storage-init). Exits non-zero if any check fails.
#
# Network checks run a pinned BusyBox inside each service's own network
# namespace (--network container:<id>), so they test the real container.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
compose="${COMPOSE:-docker compose -f $root/compose.yaml -f $root/compose.dev.yaml}"
api="${API_URL:-http://127.0.0.1:8080}"
probe_image="busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
outside="http://example.com/"

pass=0
fail=0
ok() { echo "ok    $1"; pass=$((pass + 1)); }
bad() { echo "FAIL  $1"; fail=$((fail + 1)); }
# check NAME CMD...: passes when CMD succeeds.
check() {
	local name="$1"
	shift
	if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi
}
# refute NAME CMD...: passes when CMD fails.
refute() {
	local name="$1"
	shift
	if "$@" >/dev/null 2>&1; then bad "$name"; else ok "$name"; fi
}

# shellcheck disable=SC2086 # $compose is a command line
dc() { $compose "$@"; }
cid() { dc ps -aq "$1"; }
status() { curl -s -o /dev/null -w '%{http_code}' "$1"; }
is_status() { [ "$(status "$1")" = "$2" ]; }
wait_status() {
	for _ in $(seq 1 60); do
		is_status "$1" "$2" && return 0
		sleep 1
	done
	return 1
}
in_netns() {
	local svc="$1"
	shift
	docker run --rm --network "container:$(cid "$svc")" "$probe_image" "$@"
}
tcp_open() { timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2"; }
json_404() { curl -s -D - -o /dev/null "$api/nope" | grep -qi '^content-type: application/json'; }

docker pull -q "$probe_image" >/dev/null

echo "== probes"
check "api /healthz is 200" is_status "$api/healthz" 200
check "api /readyz is 200" is_status "$api/readyz" 200
check "worker healthcheck passes" dc exec -T worker /app/tuorps healthcheck
check "unknown route answers JSON 404" json_404

echo "== migrations"
check "migrate container exited 0" test "$(docker inspect -f '{{.State.ExitCode}}' "$(cid migrate)")" = 0
check "00001 is applied" bash -c "$compose run --rm -T migrate migrate status | grep -q 'applied.*00001_tenant_context.sql'"

echo "== networking"
check "api resolves datastores by service name" in_netns api nslookup postgres
check "api can reach the internet (egress)" in_netns api wget -q -T 8 -O /dev/null "$outside"
for svc in postgres redis garage; do
	refute "$svc cannot reach the internet (internal network)" in_netns "$svc" wget -q -T 8 -O /dev/null "$outside"
done
check "dev overlay publishes postgres on loopback" tcp_open 127.0.0.1 5432
check "dev overlay publishes redis on loopback" tcp_open 127.0.0.1 6379
check "dev overlay publishes garage S3 on loopback" tcp_open 127.0.0.1 3900

echo "== hardening"
for svc in api worker; do
	check "$svc runs as nonroot" test "$(docker inspect -f '{{.Config.User}}' "$(cid "$svc")")" = "nonroot:nonroot"
	refute "$svc has no shell" dc exec -T "$svc" sh -c true
done
for svc in api worker postgres redis garage; do
	check "$svc has a read-only root filesystem" test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$(cid "$svc")")" = true
	check "$svc drops all capabilities" test "$(docker inspect -f '{{.HostConfig.CapDrop}}' "$(cid "$svc")")" = "[ALL]"
done
check "redis default user is disabled" bash -c "$compose exec -T redis redis-cli ping | grep -q NOAUTH"
check "redis app user cannot FLUSHALL" bash -c "$compose exec -T redis redis-cli --no-auth-warning --user tuorps --pass \"\$(cat '$root/secrets/redis_password')\" flushall | grep -q NOPERM"
check "garage refuses anonymous S3 access" is_status "http://127.0.0.1:3900/tuorps-media/" 403

echo "== readiness under a dependency outage"
dc stop redis >/dev/null 2>&1
check "/readyz is 503 while redis is down" wait_status "$api/readyz" 503
check "/healthz stays 200 while redis is down" is_status "$api/healthz" 200
dc start redis >/dev/null 2>&1
check "/readyz recovers once redis is back" wait_status "$api/readyz" 200

echo "== log hygiene"
leaks=0
for f in pg_app_password pg_migrator_password redis_password garage_secret_key; do
	if dc logs api worker migrate 2>&1 | grep -qF "$(cat "$root/secrets/$f")"; then leaks=$((leaks + 1)); fi
done
check "no secret values in api/worker/migrate logs" test "$leaks" = 0

echo "== graceful shutdown"
dc stop api worker >/dev/null 2>&1
for svc in api worker; do
	check "$svc exits 0 on SIGTERM" test "$(docker inspect -f '{{.State.ExitCode}}' "$(cid "$svc")")" = 0
done

echo "== $pass passed, $fail failed"
[ "$fail" -eq 0 ]
