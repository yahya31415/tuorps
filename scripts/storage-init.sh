#!/usr/bin/env bash
# storage-init.sh — one-time Garage bootstrap for the local stack: assign the
# single node a layout, import the app's access key and create the media
# bucket. Safe to rerun.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
secrets="$root/secrets"
bucket="${MEDIA_BUCKET:-tuorps-media}"
compose="${COMPOSE:-docker compose -f $root/compose.yaml -f $root/compose.dev.yaml}"
# shellcheck disable=SC2086 # $compose is a command line
garage() { $compose exec -T garage /garage -c /etc/garage.toml "$@"; }

node="$(garage node id -q | cut -d@ -f1)"
if garage layout show | grep -q "${node:0:16}"; then
	echo "layout: node already assigned"
else
	garage layout assign -z dc1 -c 10G "$node"
	garage layout apply --version 1
fi

key_id="$(cat "$secrets/garage_access_key_id")"
if garage key info "$key_id" >/dev/null 2>&1; then
	echo "key: already imported"
else
	garage key import --yes -n tuorps-app "$key_id" "$(cat "$secrets/garage_secret_key")" >/dev/null
	echo "key: imported tuorps-app"
fi

if garage bucket info "$bucket" >/dev/null 2>&1; then
	echo "bucket: $bucket exists"
else
	garage bucket create "$bucket"
fi
garage bucket allow --read --write --owner "$bucket" --key tuorps-app >/dev/null
echo "storage ready: bucket $bucket, key tuorps-app"
