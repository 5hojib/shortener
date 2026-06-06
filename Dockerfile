# â”€â”€ Stage 1: Build â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY backend/go.mod ./
# go.sum is generated inside the container â€” no need to commit it
RUN go mod download

COPY backend/ ./

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server .

# â”€â”€ Stage 2: Runtime â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]