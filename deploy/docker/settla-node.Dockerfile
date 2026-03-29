FROM golang:1.25-alpine AS builder

RUN apk --no-cache add build-base
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /bin/settla-node ./cmd/settla-node

# ── Runtime ──────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk --no-cache add ca-certificates
COPY --from=builder /bin/settla-node /bin/settla-node

RUN addgroup -g 1000 settla && adduser -u 1000 -G settla -D settla
USER settla

ENTRYPOINT ["/bin/settla-node"]
