# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.26.2 AS builder

WORKDIR /build

# Install CA certificates (for copying to scratch)
RUN apt-get update && apt-get install -y ca-certificates && update-ca-certificates

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully-static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -extldflags=-static" -o slackfiler .

# ── Runtime stage ───────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

COPY --from=builder /build/slackfiler /slackfiler

ENTRYPOINT ["/slackfiler"]
