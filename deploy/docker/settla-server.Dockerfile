FROM golang:1.24-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/settla-server ./cmd/settla-server

# ── Runtime ──────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk --no-cache add ca-certificates wget
COPY --from=builder /bin/settla-server /bin/settla-server

EXPOSE 8080 9090 6060
ENTRYPOINT ["/bin/settla-server"]
