# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.24 AS builder

WORKDIR /build

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully-static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -extldflags=-static" -o slackfiler .

# ── Runtime stage ───────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /build/slackfiler /slackfiler

ENTRYPOINT ["/slackfiler"]
