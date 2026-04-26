# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

COPY main.go .

# Build a static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o webhook .

# ── Runtime stage (distroless for minimal attack surface) ─────────────────────
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /app/webhook /webhook

USER nonroot:nonroot
ENTRYPOINT ["/webhook"]
