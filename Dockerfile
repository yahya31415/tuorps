# syntax=docker/dockerfile:1
# One image definition for both binaries: --build-arg CMD=api|worker.
# Final stage is distroless static (no shell, no package manager), non-root.

FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
ARG CMD=api
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags='-s -w' -o /out/tuorps ./cmd/${CMD}

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/tuorps /app/tuorps
USER nonroot:nonroot
ENTRYPOINT ["/app/tuorps"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 CMD ["/app/tuorps", "healthcheck"]
