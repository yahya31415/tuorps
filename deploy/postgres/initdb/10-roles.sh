#!/bin/sh
# Creates the two application roles on first database start (docker-entrypoint-initdb.d).
#
#   tuorps_migrator  owns schema "tuorps"; runs migrations; never used by the app at runtime.
#   tuorps_app       runtime role: DML only, NOSUPERUSER, NOBYPASSRLS, so row-level security applies.
#
# Passwords come from TUORPS_{APP,MIGRATOR}_PASSWORD or their _FILE variants (Docker secrets).
set -eu

secret() {
	eval "value=\${$1:-}"
	eval "file=\${$1_FILE:-}"
	if [ -n "$file" ]; then
		value="$(cat "$file")"
	fi
	if [ -z "$value" ]; then
		echo "10-roles.sh: $1 or $1_FILE is required" >&2
		exit 1
	fi
	printf '%s' "$value"
}

app_pw="$(secret TUORPS_APP_PASSWORD)"
migrator_pw="$(secret TUORPS_MIGRATOR_PASSWORD)"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v app_pw="$app_pw" -v migrator_pw="$migrator_pw" -v db="$POSTGRES_DB" <<'SQL'
CREATE ROLE tuorps_migrator LOGIN PASSWORD :'migrator_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE ROLE tuorps_app      LOGIN PASSWORD :'app_pw'      NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;

REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE :"db" TO tuorps_migrator, tuorps_app;
REVOKE ALL ON SCHEMA public FROM PUBLIC;

CREATE SCHEMA tuorps AUTHORIZATION tuorps_migrator;
GRANT USAGE ON SCHEMA tuorps TO tuorps_app;
ALTER ROLE tuorps_migrator IN DATABASE :"db" SET search_path = tuorps;
ALTER ROLE tuorps_app      IN DATABASE :"db" SET search_path = tuorps;

-- Objects the migrator creates are usable, not alterable, by the app.
ALTER DEFAULT PRIVILEGES FOR ROLE tuorps_migrator IN SCHEMA tuorps GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO tuorps_app;
ALTER DEFAULT PRIVILEGES FOR ROLE tuorps_migrator IN SCHEMA tuorps GRANT USAGE, SELECT ON SEQUENCES TO tuorps_app;
ALTER DEFAULT PRIVILEGES FOR ROLE tuorps_migrator IN SCHEMA tuorps GRANT EXECUTE ON FUNCTIONS TO tuorps_app;
SQL
