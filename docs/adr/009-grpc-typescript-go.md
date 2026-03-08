# ADR-009: gRPC Between TypeScript and Go

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

The Settla API gateway is a Fastify (TypeScript) application that proxies requests to the Go settlement server. At 5,000 TPS peak, the communication layer between these two processes is on the critical path for every single request.

We evaluated the performance characteristics:

- **JSON over HTTP/1.1**: At 5,000 TPS, JSON serialization/deserialization adds ~2ms per request (1ms encode + 1ms decode). That is 10 seconds of cumulative CPU time per second across all requests. For a transfer request with nested monetary amounts, currency codes, and metadata, JSON payloads average 800 bytes. The parsing overhead is proportional to payload size and becomes measurable under load.
- **Schema drift risk**: Without a shared schema, the Go server and TypeScript gateway can silently disagree on field names, types, or required fields. At 50M transactions/day, even a 0.01% schema mismatch rate means 5,000 malformed requests per day.
- **Connection overhead**: HTTP/1.1 with keep-alive still incurs per-request overhead. At 5,000 TPS with 4 gateway replicas, that is 1,250 requests/second per instance, each needing a connection to the Go server.

We needed to decide between:

1. **REST/JSON over HTTP** — simple, widely understood, easy to debug
2. **gRPC with Protocol Buffers** — binary serialization, schema-enforced contracts, HTTP/2 multiplexing
3. **Message queue (NATS request/reply)** — async with request/reply pattern

## Decision

We chose **gRPC with Protocol Buffers** for all synchronous communication between the TypeScript gateway and the Go server.

**Protocol Buffers** define the shared schema in `proto/settla/v1/`:
- `settlement_service.proto` — transfer lifecycle (create, get, list, cancel)
- `quote_service.proto` — quote request/response
- `treasury_service.proto` — position queries, reservation management
- `types.proto` — shared message types (Money, Transfer, Quote, etc.)

**Code generation** via `buf` produces:
- Go server stubs in `gen/settla/v1/`
- TypeScript client stubs in `api/gateway/src/gen/`

**Connection pooling**: The gateway maintains a pool of ~50 persistent gRPC connections to the Go server, using round-robin selection. Each connection multiplexes requests over HTTP/2, so 50 connections can handle 5,000+ concurrent RPCs without head-of-line blocking.

The gRPC server runs on port 9090, separate from the HTTP health/debug endpoint on port 8080.

## Consequences

### Benefits
- **Type-safe contracts**: Protocol Buffers enforce field names, types, and required fields at compile time in both languages. A breaking schema change fails the build, not production traffic.
- **Efficient binary serialization**: Protobuf encoding is ~5-10x smaller than JSON for the same payload and ~10x faster to serialize/deserialize. At 5,000 TPS, this saves ~8-10ms of cumulative CPU time per second versus JSON.
- **HTTP/2 multiplexing**: A single gRPC connection can carry thousands of concurrent RPCs. Our pool of 50 connections provides massive headroom without the connection management complexity of HTTP/1.1 keep-alive pools.
- **Streaming support**: gRPC supports server-streaming and bidirectional streaming. While we use unary RPCs today, streaming is available for future features like real-time position updates or event subscriptions without a protocol change.
- **Consistent error handling**: gRPC status codes map cleanly to HTTP status codes in the gateway, and structured error details (via `google.rpc.Status`) carry machine-readable error information.

### Trade-offs
- **Protobuf toolchain complexity**: The `buf` tool, `.proto` files, code generation steps, and generated code all add build complexity. Developers must run `make proto` after schema changes, and the generated code must be committed (or CI must generate it).
- **Debugging opacity**: Binary payloads are not human-readable on the wire. Unlike JSON APIs where you can `curl` an endpoint and read the response, gRPC requires tools like `grpcurl` or `grpc-web` for manual testing.
- **Generated code maintenance**: Generated files in `gen/` and `api/gateway/src/gen/` must stay in sync with proto definitions. Stale generated code causes subtle runtime failures.
- **Learning curve**: gRPC is less familiar than REST/JSON to most developers. New team members need to understand proto syntax, code generation, and gRPC interceptors.

### Mitigations
- **`buf` for schema management**: `buf lint` enforces proto style, `buf breaking` detects breaking changes, and `buf generate` handles all code generation. The `buf.gen.yaml` configuration makes generation reproducible.
- **`make proto` target**: A single command regenerates all Go and TypeScript code from proto definitions. CI runs this and fails if the generated code is stale.
- **OpenAPI at `/docs`**: The Fastify gateway exposes a full OpenAPI/Swagger UI for external consumers. gRPC is an internal implementation detail — external clients never see it.
- **gRPC reflection enabled in development**: The Go server enables gRPC reflection in non-production environments, allowing tools like `grpcurl` to discover and call services without proto files.

## References

- [gRPC Performance Best Practices](https://grpc.io/docs/guides/performance/) — gRPC documentation
- [Protocol Buffers Language Guide](https://protobuf.dev/programming-guides/proto3/) — Google
- [Buf documentation](https://buf.build/docs/) — Buf
