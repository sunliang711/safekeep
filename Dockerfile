# Stage 1: Build React + Vite Admin UI
FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go single binary with embedded Web UI
FROM golang:1.26-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/safekeep ./cmd/safekeep

# Stage 3: Minimal runtime image
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=go-builder /out/safekeep /usr/local/bin/safekeep
COPY config.toml /app/config.toml

# Default container configuration (all can be overridden via docker run -e / compose environment)
ENV SAFEKEEP_CONFIG=/app/config.toml \
    SAFEKEEP_LISTEN=:8333 \
    SAFEKEEP_DATA_DIR=/data \
    SAFEKEEP_SPOOL_DIR=/data/spool \
    SAFEKEEP_PUBLIC_BASE_URL=http://localhost:8333

RUN mkdir -p /data/spool
VOLUME ["/data"]
EXPOSE 8333

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8333/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/safekeep"]
