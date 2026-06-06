# â”€â”€ Stage 1: Build Go binary â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Dependencies first (layer cache)
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy source + embedded assets
COPY backend/ ./

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server .

# â”€â”€ Stage 2: Minimal runtime image â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
FROM alpine:3.19

# ca-certs needed for outbound HTTPS (MongoDB Atlas TLS)
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/server .

EXPOSE 8080

CMD ["./server"]