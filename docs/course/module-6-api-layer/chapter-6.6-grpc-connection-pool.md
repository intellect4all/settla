# Chapter 6.6: gRPC Connection Pooling

**Estimated reading time:** 20 minutes

## Learning Objectives

- Understand why per-request gRPC connections are prohibitively expensive at 5K TPS
- Implement a persistent connection pool with round-robin selection
- Add circuit breaker integration to the pool layer
- Configure pool size based on throughput math
- Handle connection health checking and automatic reconnection

---

## The Per-Request Connection Problem

Without connection pooling, every API request creates a new gRPC connection:

```
Per-request connection cost:
  TCP handshake:           ~0.5ms (local network)
  TLS negotiation:         ~2-5ms (if TLS enabled)
  HTTP/2 SETTINGS frame:   ~0.1ms
  ─────────────────────────────
  Total overhead:          ~3-6ms per connection

At 5,000 TPS:
  5,000 × 3ms = 15,000ms of connection time per second
  = 15 seconds of work to create connections per second of wall time
  = IMPOSSIBLE (more setup time than available time)
```

Even without TLS, TCP connection establishment at 5K TPS wastes significant resources and creates file descriptor pressure.

> **Security note:** TLS is enabled in production via `SETTLA_GRPC_TLS=true` with optional mTLS certificates (`SETTLA_GRPC_CA_CERT`, `SETTLA_GRPC_CERT`, `SETTLA_GRPC_KEY`). The pool negotiates TLS once per persistent connection, amortizing the 2-5ms handshake across thousands of requests.

---

## Settla's Connection Pool

The gateway maintains a pool of persistent gRPC connections:

```typescript
// From api/gateway/src/grpc/client.ts
export class SettlaGrpcClient {
  private pool: GrpcPool;
  private SettlementServiceCtor: any;
  private TreasuryServiceCtor: any;
  private AuthServiceCtor: any;
  // ... 7 more service constructors

  constructor(pool: GrpcPool) {
    this.pool = pool;
    const p = loadProto();
    this.SettlementServiceCtor = p.settla.v1.SettlementService;
    this.TreasuryServiceCtor = p.settla.v1.TreasuryService;
    this.AuthServiceCtor = p.settla.v1.AuthService;
    // ... initialize all service constructors
  }
}
```

### Pool Architecture

```
┌─────────────────────────────────────────────────────┐
│                  gRPC CONNECTION POOL                 │
├─────────────────────────────────────────────────────┤
│                                                      │
│  Gateway Instance (one of 4+ replicas)               │
│  ┌────────────────────────────────────────────┐     │
│  │ GrpcPool                                    │     │
│  │ ┌──────────┐ ┌──────────┐ ┌──────────┐    │     │
│  │ │ Conn #1  │ │ Conn #2  │ │ Conn #3  │... │     │
│  │ │ (idle)   │ │ (active) │ │ (active) │    │     │
│  │ └──────────┘ └──────────┘ └──────────┘    │     │
│  │      │            │            │           │     │
│  │      └────────────┼────────────┘           │     │
│  │           Round-robin selection             │     │
│  └────────────────────┬───────────────────────┘     │
│                       │                              │
│                       ▼                              │
│          ┌────────────────────────┐                  │
│          │   settla-server:9090   │                  │
│          │   (6+ replicas, LB)   │                  │
│          └────────────────────────┘                  │
│                                                      │
│  Pool size: ~50 persistent connections               │
│  Selection: round-robin                              │
│  Health check: automatic (gRPC keepalive)            │
│  Reconnection: automatic on failure                  │
└─────────────────────────────────────────────────────┘
```

### Round-Robin Selection

Each request gets the next connection in sequence:

```
Request 1 → Conn #1
Request 2 → Conn #2
Request 3 → Conn #3
...
Request 50 → Conn #50
Request 51 → Conn #1 (wraps around)
```

This distributes load evenly across connections. HTTP/2 multiplexing means each connection can handle multiple concurrent streams, so 50 connections can support thousands of concurrent requests.

---

## Why 50 Connections

The pool size is derived from throughput math:

```
HTTP/2 max concurrent streams per connection: 100 (default)
Target peak throughput: 5,000 requests/sec
Average request duration: ~10ms (gRPC round trip)

Concurrent requests at peak:
  5,000 req/sec × 0.010 sec/req = 50 concurrent requests

Connections needed (with headroom):
  50 concurrent / 100 streams per conn = 0.5 connections minimum
  With 10x headroom for bursts: 5 connections minimum
  With operational margin: ~50 connections

  50 connections × 100 streams = 5,000 concurrent capacity
  This matches peak TPS exactly, with burst absorption.
```

50 connections provides significant headroom while keeping the connection count manageable for the server side.

---

## Circuit Breaker Integration

The pool integrates circuit breaker logic to fast-fail when the server is down:

```typescript
// From api/gateway/src/grpc/client.ts
/** Check circuit breaker before dispatch, record result after. */
private async withCircuitBreaker<T>(fn: () => Promise<T>): Promise<T> {
  if (this.pool.isOpen()) {
    grpcCircuitBreakerRejections.inc();
    throw new Error("gRPC circuit breaker is open");
  }

  try {
    const result = await fn();
    this.pool.recordSuccess();
    return result;
  } catch (error) {
    this.pool.recordFailure();
    throw error;
  }
}
```

When the circuit breaker opens, the gateway returns 503 Service Unavailable immediately without attempting a gRPC call. This prevents connection pool exhaustion during server outages.

---

## Proto Loading

Protos are loaded once at startup and compiled to efficient serializers:

```typescript
// From api/gateway/src/grpc/client.ts
const packageDef = protoLoader.loadSync(
  [
    path.join(PROTO_DIR, "settla/v1/settlement.proto"),
    path.join(PROTO_DIR, "settla/v1/types.proto"),
    path.join(PROTO_DIR, "settla/v1/deposit.proto"),
    path.join(PROTO_DIR, "settla/v1/bank_deposit.proto"),
    path.join(PROTO_DIR, "settla/v1/payment_link.proto"),
    path.join(PROTO_DIR, "settla/v1/analytics.proto"),
  ],
  {
    keepCase: false,  // convert to camelCase for JS
    longs: String,    // CRITICAL: never use JS number for money
    enums: String,
    defaults: true,
    oneofs: true,
  },
);
```

> **Key Insight:** The `longs: String` option is critical for financial systems. JavaScript's `number` type is a 64-bit float (IEEE 754), which loses precision for integers above 2^53. Proto `int64` fields (used for monetary amounts in micro-units) must be represented as strings to preserve precision. This is the same principle as the `shopspring/decimal` rule in Go.

---

## Connection Health and Recovery

gRPC has built-in keepalive mechanisms:

```
Keepalive Configuration:
  keepalive_time:     30 seconds (send ping if idle)
  keepalive_timeout:  10 seconds (close if no pong)
  permit_without_stream: true (keepalive even when no active RPCs)

Reconnection:
  On connection drop → gRPC automatically reconnects
  Exponential backoff: 1s, 2s, 4s, 8s... up to 120s max
  Pool removes dead connections and creates replacements
```

---

## Common Mistakes

**Mistake 1: Creating connections per request**
At 5K TPS, per-request connections waste 15+ seconds of overhead per second. Always pool.

**Mistake 2: Pool too small**
With 5 connections and 100 concurrent requests, connections saturate and requests queue. Size the pool based on peak concurrent requests.

**Mistake 3: Pool too large**
500 connections wastes server-side resources (each connection has memory overhead). 50 with HTTP/2 multiplexing is plenty for 5K TPS.

**Mistake 4: Using JS `number` for proto int64**
JavaScript's `number` loses precision above 2^53. Financial amounts in micro-units (10^6 scale) can easily exceed this. Use `longs: String` in proto loader config.

---

## Exercises

### Exercise 1: Pool Sizing
Your system handles 2,000 TPS with 20ms average gRPC latency:
1. Calculate concurrent requests at peak
2. How many connections with 100 streams each?
3. What pool size would you choose (with headroom)?

### Exercise 2: Connection Failure Simulation
Design a test that:
1. Creates a pool of 5 connections
2. Kills the gRPC server
3. Verifies the circuit breaker opens within the threshold
4. Restarts the server
5. Verifies the circuit breaker closes and connections recover

### Exercise 3: Load Balancing
With 6 settla-server replicas behind a load balancer:
1. How does the pool interact with the LB?
2. Should connections be distributed across replicas?
3. What happens during a rolling deployment?

---

## What's Next

Module 6 is complete. You now understand Protocol Buffers, the gRPC server, the REST gateway, multi-level auth caching, rate limiting, and connection pooling. Module 7 covers operations — reconciliation, compensation, stuck-transfer recovery, and net settlement.
