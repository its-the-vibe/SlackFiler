# ── Build stage ────────────────────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully-static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o slackfiler .

# ── Runtime stage (distroless) ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /build/slackfiler /slackfiler

USER nonroot:nonroot

ENTRYPOINT ["/slackfiler"]
