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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/tuorps /app/tuorps
USER nonroot:nonroot
ENTRYPOINT ["/app/tuorps"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 CMD ["/app/tuorps", "healthcheck"]
