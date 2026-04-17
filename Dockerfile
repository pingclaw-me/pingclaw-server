# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Cache the module download independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static build — pure Go (pgx is pure Go, no CGO needed).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags='-s -w' \
    -o /out/pingclaw-server ./cmd/server

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S pingclaw && \
    adduser -S -G pingclaw -h /app pingclaw

WORKDIR /app

# Binary + static web assets + markdown content + schema snapshot.
# Anything the running server reads at runtime needs to be in the image.
COPY --from=builder /out/pingclaw-server ./pingclaw-server
COPY --chown=pingclaw:pingclaw web/  ./web/
COPY --chown=pingclaw:pingclaw db/   ./db/

USER pingclaw

EXPOSE 8080

# DATABASE_URL is required at runtime — supply via env or docker-compose.
CMD ["./pingclaw-server"]
