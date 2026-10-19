#!/usr/bin/env bash
# dev-secrets.sh — generate local Docker secrets under secrets/ (gitignored).
# Existing files are kept so running stacks keep working; pass --force to
# regenerate everything (then `docker compose down -v` to reinitialise data).
set -euo pipefail

dir="${SECRETS_DIR:-$(dirname "$0")/../secrets}"
force=0
[[ "${1:-}" == "--force" ]] && force=1

hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

umask 077
mkdir -p "$dir"
chmod 700 "$dir"

put() {
	local name="$1" value="$2"
	if [[ -f "$dir/$name" && $force -eq 0 ]]; then
		return
	fi
	printf '%s' "$value" >"$dir/$name"
	# Readable by the non-root users inside containers; the directory stays 0700.
	chmod 644 "$dir/$name"
	echo "wrote secrets/$name"
}

get() { cat "$dir/$1"; }

put postgres_password "$(hex 24)"
put pg_app_password "$(hex 24)"
put pg_migrator_password "$(hex 24)"
put redis_password "$(hex 24)"
put garage_rpc_secret "$(hex 32)"
put garage_admin_token "$(hex 32)"
put garage_access_key_id "GK$(hex 12)"
put garage_secret_key "$(hex 32)"

# Derived values (rewritten when their inputs were).
put database_url "postgres://tuorps_app:$(get pg_app_password)@postgres:5432/tuorps?sslmode=disable"
put migrate_database_url "postgres://tuorps_migrator:$(get pg_migrator_password)@postgres:5432/tuorps?sslmode=disable"
put redis_url "redis://tuorps:$(get redis_password)@redis:6379/0"
put redis_acl "user default off
user healthcheck on nopass -@all +ping
user tuorps on >$(get redis_password) ~* &* +@all -@dangerous
"
