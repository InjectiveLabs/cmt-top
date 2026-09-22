# Protocol capacity harness: run with --network none. The synthetic source,
# real app process and load driver communicate exclusively over loopback.
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/cmt-top ./cmd/cmt-top \
 && CGO_ENABLED=0 go build -o /out/cmt-top-replay ./cmd/cmt-top-replay \
 && CGO_ENABLED=0 go build -o /out/cmt-top-load ./cmd/cmt-top-load
FROM alpine:3.20
RUN apk add --no-cache bash python3 \
 && addgroup -S -g 65532 app && adduser -S -u 65532 -G app app
COPY --from=builder /out/ /app/bin/
COPY deploy/capacity/smoke.sh /app/deploy/capacity/smoke.sh
USER app
WORKDIR /app
ENTRYPOINT ["bash", "/app/deploy/capacity/smoke.sh"]
