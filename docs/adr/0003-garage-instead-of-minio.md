# ADR 0003: Garage instead of MinIO for local S3-compatible storage

- Status: accepted
- Date: 2026-10-19

## Context

The stack called for MinIO in Compose. As of October 2026, MinIO is
effectively unavailable:

- `minio/minio` returns 404 on Docker Hub.
- `quay.io/minio/minio` denies pulls.
- The GitHub repository was archived in April 2026.

A pinned, unmaintained image would never receive security fixes.

## Decision

Use Garage (`dxflrs/garage:v2.4.1`, pinned by digest), a maintained
S3-compatible server, in Compose:

- Configuration: `deploy/garage/garage.toml`.
- Bootstrap: `scripts/storage-init.sh` (`make storage-init`) assigns the
  single-node layout, imports the app key from Docker secrets and creates the
  `tuorps-media` bucket. It is idempotent.

The application only speaks the S3 API, so production can use any
S3-compatible store in an India region.

## Consequences

- One-time bootstrap step after the first `make up`.
- Garage's scratch image has no non-root user that owns the named volumes, so
  it runs as uid 0. It does so with every capability dropped,
  `no-new-privileges`, a read-only rootfs and no network route out.
- M4 must still choose how to sign S3 requests: stdlib SigV4 or an SDK. That
  choice needs its own ADR, because the allowed-dependency list has no S3
  client.
