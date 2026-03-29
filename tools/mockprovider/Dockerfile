FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /bin/mockprovider ./tools/mockprovider/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget
RUN addgroup -S settla && adduser -S settla -G settla

COPY --from=builder /bin/mockprovider /bin/mockprovider

USER settla
EXPOSE 9095

ENTRYPOINT ["/bin/mockprovider"]
