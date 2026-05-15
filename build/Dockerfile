# Multi-stage build that produces a distroless container per cmd/* binary.
# Build args:
#   BINARY  — name of the cmd subdirectory (e.g. "gateway", "ws")
#   VERSION — version string baked in via -ldflags
#
# Two stages:
#   builder  — fetch deps, compile static binary
#   runtime  — distroless static, nonroot, just the binary
#
# Per the §11 production hardening checklist: distroless static, no
# shell, no libc, USER 65532, only the binary in the image. Trivy
# blocks HIGH/CRITICAL CVEs in CI before the image is published.

# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-alpine AS builder
ARG BINARY
ARG VERSION=dev
WORKDIR /src

# Cache deps separately from source so a pure-source change reuses the
# layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/${BINARY} \
      ./cmd/${BINARY}

# Runtime: distroless static, nonroot. Only the binary, no shell.
FROM gcr.io/distroless/static-debian12:nonroot
ARG BINARY
COPY --from=builder /out/${BINARY} /app
USER 65532:65532
ENTRYPOINT ["/app"]
