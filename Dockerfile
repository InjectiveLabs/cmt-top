# syntax=docker/dockerfile:1.7

# ---- Stage 1: build the Svelte SPA -------------------------------------------
# Pinned digests for supply-chain reproducibility. Refresh periodically:
#   docker pull <image>:<tag> && docker inspect --format '{{index .RepoDigests 0}}' <image>:<tag>
FROM node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS web-builder

RUN corepack enable && corepack prepare pnpm@10.8.1 --activate

WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY web/ ./
# vite.config.ts is configured to emit into ../internal/web/dist
RUN pnpm build

# ---- Stage 2: build the Go binary --------------------------------------------
FROM golang:1.25-alpine@sha256:5caaf1cca9dc351e13deafbc3879fd4754801acba8653fa9540cea125d01a71f AS go-builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Pull in the freshly built SPA so //go:embed picks it up.
COPY --from=web-builder /src/internal/web/dist /src/internal/web/dist

ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/cmt-top ./cmd/cmt-top

# ---- Stage 3: runtime --------------------------------------------------------
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

RUN apk add --no-cache ca-certificates tzdata curl \
 && addgroup -S -g 65532 app \
 && adduser  -S -u 65532 -G app -h /app app

COPY --from=go-builder /out/cmt-top /usr/local/bin/cmt-top

USER app
WORKDIR /app

# Web UI / REST + WS
EXPOSE 8080
# Prometheus
EXPOSE 9091

ENV CMTOP_MODE=web \
    CMTOP_WEB_LISTEN=0.0.0.0:8080 \
    CMTOP_METRICS_LISTEN=0.0.0.0:9091 \
    CMTOP_LOG_LEVEL=info

# Loopback inside the container is useless; we bind 0.0.0.0 by default. That
# requires a token unless the operator explicitly uses --web-token "" via
# command override. The healthcheck hits /healthz which is unauthenticated.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/cmt-top"]
