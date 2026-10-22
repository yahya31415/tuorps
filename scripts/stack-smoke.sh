#!/usr/bin/env bash
# stack-smoke.sh — end-to-end checks against a running stack. Exits non-zero
# if any check fails.
#
#   make dev-secrets up storage-init smoke                    # dev overlay (make up)
#   COMPOSE="docker compose -f compose.yaml" scripts/stack-smoke.sh   # production config
#
# The mode is detected from the stack: with the dev overlay, datastore ports
# must be published on loopback (and the overlay grants the datastores
# egress, so isolation is not asserted). Without it, the production config
# must publish no datastore ports and the datastores must have no route to
# the internet.
#
# The account flow signs up through the API. With the dev overlay it then
# reads the verification email from Mailpit and walks the whole tenancy
# path (verify, log in, create a brand, read the audit log, and a second
# account is refused the first workspace). The production config has no
# mail catcher, so it stops at "signed up but not yet verified".
#
# Network checks run a pinned BusyBox inside each service's own network
# namespace (--network container:<id>) and use TCP connects to IP literals,
# because Docker's embedded DNS does not answer containers that join another
# container's namespace.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib/net.sh
. "$root/scripts/lib/net.sh"
compose="${COMPOSE:-docker compose -f $root/compose.yaml -f $root/compose.dev.yaml}"
api="${API_URL:-http://127.0.0.1:8080}"
mailpit="${MAILPIT_URL:-http://127.0.0.1:8025}"
probe_image="busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
internet_ips="1.1.1.1 8.8.8.8" # any one reachable on port 80 or 53 means egress

pass=0
fail=0
ok() { echo "ok    $1"; pass=$((pass + 1)); }
bad() {
	echo "FAIL  $1"
	[ -s "$out" ] && sed 's/^/      | /' "$out" | tail -n 8
	fail=$((fail + 1))
}
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
# check NAME CMD...: passes when CMD succeeds.
check() {
	local name="$1"
	shift
	if "$@" >"$out" 2>&1; then ok "$name"; else bad "$name"; fi
}
# refute NAME CMD...: passes when CMD fails.
refute() {
	local name="$1"
	shift
	if "$@" >"$out" 2>&1; then bad "$name"; else ok "$name"; fi
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
# has_egress SVC: true if SVC can open a TCP connection to the internet.
has_egress() {
	local ip port
	for ip in $internet_ips; do
		for port in 80 53; do
			in_netns "$1" nc -w 5 "$ip" "$port" </dev/null && return 0
		done
	done
	return 1
}
json_404() { curl -s -D - -o /dev/null "$api/nope" | grep -qi '^content-type: application/json'; }

# call METHOD PATH [JSON] [COOKIE]: prints "<status> <body>" from the API.
call() {
	local args=(-s -X "$1" -w '\n%{http_code}' "$api$2")
	[ -n "${3:-}" ] && args+=(-H 'Content-Type: application/json' --data "$3")
	[ -n "${4:-}" ] && args+=(-H "Cookie: $4")
	local res
	res="$(curl "${args[@]}")" || return 1
	printf '%s %s\n' "${res##*$'\n'}" "${res%$'\n'*}"
}
# expect WANT METHOD PATH [JSON] [COOKIE]: the response status is WANT.
expect() {
	local want="$1"
	shift
	local res
	res="$(call "$@")" || return 1
	echo "$res"
	[ "${res%% *}" = "$want" ]
}
# signup EMAIL WORKSPACE
signup() { expect 202 POST /v1/auth/signup "$(jq -nc --arg e "$1" --arg w "$2" '{email:$e,password:"smoke test passphrase 1",name:"Smoke",workspace_name:$w}')"; }
# login EMAIL: prints the session cookie ("name=value").
login() {
	curl -s -D - -o /dev/null -H 'Content-Type: application/json' \
		--data "$(jq -nc --arg e "$1" '{email:$e,password:"smoke test passphrase 1"}')" "$api/v1/auth/login" |
		tr -d '\r' | sed -n 's/^[Ss]et-[Cc]ookie: \(__Host-tuorps_session=[^;]*\).*/\1/p'
}
# mail_token EMAIL: waits for the newest email to EMAIL in Mailpit and
# prints the token from its link.
mail_token() {
	local id tok
	for _ in $(seq 1 30); do
		id="$(curl -s "$mailpit/api/v1/search?query=$(jq -rn --arg q "to:\"$1\"" '$q|@uri')" | jq -r '.messages[0].ID // empty')"
		if [ -n "$id" ]; then
			tok="$(curl -s "$mailpit/api/v1/message/$id" | jq -r '.Text' | grep -o 'token=[A-Za-z0-9_-]*' | head -n1)"
			[ -n "$tok" ] && echo "${tok#token=}" && return 0
		fi
		sleep 1
	done
	return 1
}
published() { [ -n "$(docker inspect -f '{{range $p, $b := .HostConfig.PortBindings}}{{$p}} {{end}}' "$(cid "$1")")" ]; }

docker pull -q "$probe_image" >/dev/null
if published postgres; then mode=dev; else mode=prod; fi
echo "== mode: $mode"

echo "== probes"
check "api /healthz is 200" is_status "$api/healthz" 200
check "api /readyz is 200 (reaches postgres and redis by service name)" is_status "$api/readyz" 200
check "worker healthcheck passes" dc exec -T worker /app/tuorps healthcheck
check "unknown route answers JSON 404" json_404

echo "== migrations"
check "migrate container exited 0" test "$(docker inspect -f '{{.State.ExitCode}}' "$(cid migrate)")" = 0
check "00001 is applied" bash -c "$compose run --rm -T migrate migrate status | grep -q 'applied.*00001_tenant_context.sql'"
check "00003 is applied" bash -c "$compose run --rm -T migrate migrate status | grep -q 'applied.*00003_connections.sql'"

echo "== networking"
check "api can reach the internet (egress network)" has_egress api
check "worker can reach the internet (egress network)" has_egress worker
check "api is published on loopback" tcp_open 127.0.0.1 8080
if [ "$mode" = prod ]; then
	for svc in postgres redis garage; do
		refute "$svc cannot reach the internet (internal network)" has_egress "$svc"
	done
	refute "postgres is not published" published postgres
	refute "redis is not published" published redis
	refute "garage is not published" published garage
else
	echo "skip  datastore isolation: the dev overlay attaches datastores to a routable network to publish ports"
	check "dev overlay publishes postgres on loopback" tcp_open 127.0.0.1 5432
	check "dev overlay publishes redis on loopback" tcp_open 127.0.0.1 6379
	check "dev overlay publishes garage S3 on loopback" tcp_open 127.0.0.1 3900
fi

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
garage_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$(cid garage)" | awk '{print $1}')"
check "garage refuses anonymous S3 access (from the api's network)" \
	bash -c "docker run --rm --network container:$(cid api) $probe_image wget -S -q -O /dev/null http://$garage_ip:3900/tuorps-media/ 2>&1 | grep -q '403'"

echo "== account flow"
run_id="$(date +%s)-$$"
owner="smoke-owner-$run_id@example.com"
outsider="smoke-outsider-$run_id@example.com"
check "signup is accepted (202)" signup "$owner" "Smoke $run_id"
check "login before verification is refused (403 email_not_verified)" \
	bash -c "$(declare -f call expect); api='$api'; expect 403 POST /v1/auth/login '{\"email\":\"$owner\",\"password\":\"smoke test passphrase 1\"}' | grep -q email_not_verified"
check "the API needs a session (401 without one)" expect 401 GET /v1/workspaces
if [ "$mode" = dev ]; then
	token="$(mail_token "$owner")"
	check "the worker delivered the verification email to Mailpit" test -n "$token"
	check "the emailed token verifies the address (204)" expect 204 POST /v1/auth/verify-email "$(jq -nc --arg t "$token" '{token:$t}')"
	cookie="$(login "$owner")"
	check "verified owner logs in (session cookie set)" test -n "$cookie"
	ws="$(call GET /v1/workspaces "" "$cookie" | cut -d' ' -f2- | jq -r '.items[0].id // empty')"
	check "signup created the owner's workspace" test -n "$ws"
	check "owner creates a brand (201)" expect 201 POST "/v1/workspaces/$ws/brands" '{"name":"Smoke Brand"}' "$cookie"
	check "audit log records the brand creation" \
		bash -c "$(declare -f call); api='$api'; call GET '/v1/workspaces/$ws/audit-log' '' '$cookie' | cut -d' ' -f2- | jq -e '.items[0].action == \"brand.create\"'"
	check "second signup is accepted (202)" signup "$outsider" "Outsider $run_id"
	token="$(mail_token "$outsider")"
	check "second account verifies" expect 204 POST /v1/auth/verify-email "$(jq -nc --arg t "$token" '{token:$t}')"
	other="$(login "$outsider")"
	check "second account logs in" test -n "$other"
	check "owner can read the workspace (200)" expect 200 GET "/v1/workspaces/$ws" "" "$cookie"
	check "second account cannot see the first workspace (404)" expect 404 GET "/v1/workspaces/$ws" "" "$other"
	check "second account cannot list the first workspace's brands (404)" expect 404 GET "/v1/workspaces/$ws/brands" "" "$other"
	check "api and worker logs contain no email addresses" bash -c "! $compose logs api worker 2>&1 | grep -qF -e '$owner' -e '$outsider'"
	check "logout ends the session (then 401)" bash -c "$(declare -f call expect); api='$api'; expect 204 POST /v1/auth/logout '' '$cookie' && expect 401 GET /v1/workspaces '' '$cookie'"
else
	echo "skip  email delivery and the verified flow: the production config has no mail catcher"
fi

echo "== connections and webhooks"
# Compose configures no platform credentials by default, so this checks the
# routes' fail-safe behaviour; the flows themselves are covered by
# internal/connections/http and internal/webhooks/http tests.
nil_id="00000000-0000-0000-0000-000000000000"
# callback_fails_safe: the callback answers 303 to the app's error page.
callback_fails_safe() {
	local r
	r="$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "$api/v1/oauth/meta/callback?code=zz-smoke-code-123&state=zz-smoke-state")"
	echo "$r"
	case "$r" in '303 '*'/connections/error#reason=signed_out') ;; *) return 1 ;; esac
}
check "OAuth callback without a session redirects to the app's error page" callback_fails_safe
check "the connection list needs a session (401)" expect 401 GET "/v1/workspaces/$nil_id/brands/$nil_id/connections"
check "an unconfigured platform's webhook is not exposed (404)" expect 404 POST /v1/webhooks/meta '{}'
check "an unknown data-deletion code is 404" expect 404 GET /v1/data-deletion/zz-unknown-code
check "OAuth codes and states never reach the logs" bash -c "! $compose logs api 2>&1 | grep -qF -e zz-smoke-code-123 -e zz-smoke-state"

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

echo "== $mode: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
