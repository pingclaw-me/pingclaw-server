# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS TARGETARCH

WORKDIR /src

# 1. Cache module download (only re-runs when go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# 2. Copy only Go source — changes to web/, db/, docs/ won't
#    invalidate the expensive `go build` layer.
COPY cmd/    ./cmd/
COPY internal/ ./internal/

# 3. Build with a cache mount so the Go compiler cache persists
#    across rebuilds. TARGETOS/TARGETARCH use the host's native
#    arch (no QEMU emulation on Apple Silicon).
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -ldflags='-s -w' \
    -o /out/pingclaw-server ./cmd/pingclaw-server

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S pingclaw && \
    adduser -S -G pingclaw -h /app pingclaw

WORKDIR /app

# Binary from the build stage.
COPY --from=builder /out/pingclaw-server ./pingclaw-server

# Static assets copied directly from the build context (not from the
# builder stage) so Go source changes don't invalidate these layers.
COPY --chown=pingclaw:pingclaw web/ ./web/
COPY --chown=pingclaw:pingclaw db/  ./db/

USER pingclaw

EXPOSE 8080

CMD ["./pingclaw-server"]
