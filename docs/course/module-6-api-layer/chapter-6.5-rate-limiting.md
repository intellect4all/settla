# Chapter 6.5: Rate Limiting and Load Shedding

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain how Settla's distributed rate limiter works with local counters and Redis synchronization
2. Describe the adaptive load shedding algorithm based on Little's Law
3. Trace the graceful SIGTERM drain sequence for zero-downtime deployments
4. Calculate the trade-offs between local accuracy and Redis round-trip cost

---

## Why Per-Tenant Rate Limiting?

Settla is multi-tenant infrastructure. Without rate limiting, one tenant's traffic spike (or bug) can starve all other tenants:

```
  Without rate limiting:
  Tenant A (buggy client): 10,000 req/s  --->  Gateway saturated
  Tenant B (normal):          100 req/s  --->  503 Service Unavailable
  Tenant C (normal):           50 req/s  --->  503 Service Unavailable

  With per-tenant rate limiting (1,000/sec):
  Tenant A: 10,000 req/s  ---> 1,000 allowed, 9,000 rejected (429)
  Tenant B:    100 req/s  ---> all allowed
  Tenant C:     50 req/s  ---> all allowed
```

The default limit is 1,000 requests per second per tenant, configured via `SETTLA_RATE_LIMIT_PER_TENANT`.

---

## The Distributed Rate Limiter: rate-limit.ts

The rate limiter uses a hybrid approach: local in-memory counters for speed, with periodic Redis synchronization for distributed accuracy.

```typescript
export class DistributedRateLimiter {
  private local = new Map<string, RateLimitEntry>();
  private readonly limit: number;
  private readonly windowMs: number;
  private readonly redis: Redis | null;
  private cleanupTimer: ReturnType<typeof setInterval> | null = null;

  constructor(limit: number, windowMs: number, redis: Redis | null) {
    this.limit = limit;
    this.windowMs = windowMs;
    this.redis = redis;

    // Evict stale entries every 60s
    this.cleanupTimer = setInterval(() => {
      const now = Date.now();
      const staleThreshold = 10_000;
      for (const [key, entry] of this.local) {
        if (now - entry.windowStart > staleThreshold) {
          this.local.delete(key);
        }
      }
    }, 60_000);
    this.cleanupTimer.unref();
  }
```

The `unref()` on the cleanup timer is important. Without it, the timer would keep the Node.js process alive even after `server.close()`, preventing clean shutdowns.

### The Check Method: Window-Based Counting

```typescript
  async check(tenantId: string): Promise<{
    allowed: boolean;
    remaining: number;
    resetMs: number;
  }> {
    const now = Date.now();
    const windowSec = Math.max(1, Math.floor(this.windowMs / 1000));
    const windowEpoch = Math.floor(now / 1000 / windowSec) * windowSec;
    let entry = this.local.get(tenantId);

    // Window rotation: start new window
    if (!entry || now - entry.windowStart >= this.windowMs) {
      let distributedCount = 0;
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const val = await this.redis.incr(key);
          if (val === 1) {
            await this.redis.expire(key, windowSec + 1);
          }
          distributedCount = val;
        } catch {
          // Redis unavailable -- fall back to local
        }
      }

      entry = { count: Math.max(1, distributedCount), windowStart: now };
      this.local.set(tenantId, entry);

      const resetMs = entry.windowStart + this.windowMs;
      const remaining = Math.max(0, this.limit - entry.count);
      return { allowed: true, remaining, resetMs };
    }
```

The window rotation logic is the key insight. When a new time window starts:

1. Increment the Redis counter for this window epoch (atomic `INCR`)
2. If this is the first increment (`val === 1`), set an expiry on the key
3. Initialize the local counter with the distributed count

This means that at the start of each window, the local counter synchronizes with the global state. Within the window, the local counter tracks requests independently.

```
  Time:    0s                    1s                    2s
           |---- window 1 ------|---- window 2 ------|
           |                    |                    |
  Gateway1: sync from Redis     sync from Redis
           |  local count ++    |  local count ++
           |  local count ++    |
           |                    |
  Gateway2: sync from Redis     sync from Redis
           |  local count ++    |  local count ++
```

### Within-Window Counting and Redis Cross-Check

```typescript
    // Increment local counter
    entry.count++;

    // If local exceeds limit, optionally cross-check Redis
    if (entry.count > this.limit) {
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const distributed = await this.redis.get(key);
          if (distributed && Number(distributed) <= this.limit) {
            // Other instances haven't hit the limit -- allow
            await this.redis.incr(key);
            const resetMs = entry.windowStart + this.windowMs;
            return { allowed: true, remaining: 0, resetMs };
          }
        } catch {
          // Redis unavailable -- enforce local limit
        }
      }

      const resetMs = entry.windowStart + this.windowMs;
      return { allowed: false, remaining: 0, resetMs };
    }

    // Periodically sync to Redis (every 10 requests to reduce round-trips)
    if (this.redis && entry.count % 10 === 0) {
      const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
      try {
        await this.redis.incrby(key, 10);
      } catch {
        // Best-effort sync
      }
    }
```

This is where the hybrid approach pays off:

**Normal case (within limit):** The local counter increments with zero network calls. Every 10 requests, it syncs the batch to Redis. This reduces Redis round-trips by 10x.

**Limit reached locally:** Before rejecting, the limiter cross-checks Redis. If the distributed count is still under the limit (because other gateway instances have not used their share), it allows the request. This prevents a single gateway instance from consuming the entire limit.

**Redis unavailable:** The limiter falls back to local-only counting. Each gateway instance enforces the limit independently. With 4 gateway instances, the effective global limit becomes `4 * 1000 = 4000` in the worst case, which is acceptable as a degradation mode.

```
  4 Gateway instances, limit = 1000/sec per tenant:

  With Redis (normal):
    Total allowed: ~1000/sec (globally synchronized)

  Without Redis (degraded):
    Total allowed: ~4000/sec (each instance allows 1000)
    Acceptable? Yes -- better to over-allow than reject legitimate traffic
```

---

## The Fastify Plugin: Hook Integration

The rate limiter is wrapped in a Fastify plugin:

```typescript
export const rateLimitPlugin = fp(
  async (fastify: FastifyInstance, opts: RateLimitPluginOpts) => {
    const limiter = new DistributedRateLimiter(
      opts.limit,
      opts.windowMs ?? 1_000,
      opts.redis,
    );

    fastify.addHook("onClose", async () => {
      limiter.close();
    });

    fastify.addHook("onRequest",
      async (request: FastifyRequest, reply: FastifyReply) => {
        if (shouldBypass(request.url)) return;

        const tenantId = request.tenantAuth?.tenantId;
        if (!tenantId) {
          // Public route -- rate limit by IP
          const ipKey = `public:${request.ip}`;
          const { allowed, remaining, resetMs } = await limiter.check(ipKey);
          // ... handle public rate limiting ...
          return;
        }

        const { allowed, remaining, resetMs } = await limiter.check(tenantId);

        (request as any)._rateLimit = {
          limit: opts.limit,
          remaining,
          reset: Math.ceil(resetMs / 1000),
        };

        if (!allowed) {
          rateLimitTotal.inc({ tenant: tenantId, result: "rejected" });
          request.log.warn(
            { tenant_id: tenantId, limit: opts.limit },
            "rate_limit_exceeded",
          );
          const retryAfterSec = Math.max(1,
            Math.ceil((resetMs - Date.now()) / 1000));
          return reply
            .header("Retry-After", String(retryAfterSec))
            .status(429)
            .send({
              error: "rate_limit_exceeded",
              tenant_id: tenantId,
              request_id: request.id,
            });
        }

        rateLimitTotal.inc({ tenant: tenantId, result: "allowed" });
      }
    );
```

Two important details:

**1. Bypass for health/docs/metrics.** These endpoints must never be rate-limited. A load balancer checking `/health` should not be throttled by a tenant's traffic.

**2. Public routes rate-limit by IP.** Unauthenticated endpoints (like `/v1/auth/login`) cannot be rate-limited by tenant. They fall back to IP-based limiting.

### Rate Limit Headers

The `onSend` hook attaches standard rate limit headers to every response:

```typescript
    fastify.addHook("onSend",
      async (request: FastifyRequest, reply: FastifyReply) => {
        const rl = (request as any)._rateLimit;
        if (!rl) return;

        reply.header("X-RateLimit-Limit", String(rl.limit));
        reply.header("X-RateLimit-Remaining", String(rl.remaining));
        reply.header("X-RateLimit-Reset", String(rl.reset));
      }
    );
```

This lets clients implement proactive backoff before hitting the limit. A well-behaved client watches `X-RateLimit-Remaining` and reduces request rate as it approaches zero.

---

## Adaptive Load Shedding

Rate limiting prevents individual tenants from abusing the system. Load shedding protects the system as a whole when total traffic exceeds capacity. It lives in `api/gateway/src/middleware/load-shedding.ts`.

### The Algorithm: AIMD with Little's Law

```typescript
const loadSheddingPlugin: FastifyPluginAsync<LoadSheddingOptions> = async (
  fastify, opts
) => {
  const maxConcurrent = opts.maxConcurrent ?? 1000;
  const targetLatencyMs = opts.targetLatencyMs ?? 50;
  const minLimit = opts.minLimit ?? 10;

  let limit = opts.initialLimit ?? 200;
  let inFlight = 0;
  let ewmaLatencyMs = 0;
  let sampleCount = 0;
  const ewmaAlpha = 0.1;
  let totalRejected = 0;
```

The load shedder tracks three metrics:
- **`inFlight`** -- current number of concurrent requests
- **`ewmaLatencyMs`** -- exponentially-weighted moving average of response times
- **`limit`** -- the current concurrency limit (adapts over time)

### Request Admission

```typescript
  fastify.addHook("onRequest",
    async (request: FastifyRequest, reply: FastifyReply) => {
      inFlight++;

      if (inFlight > limit) {
        inFlight--;
        totalRejected++;

        request.log.warn({
          in_flight: inFlight, limit, total_rejected: totalRejected,
        }, "settla-gateway: load shedding request");

        reply.code(503).header("Retry-After", "1").send({
          error: "Service Unavailable",
          message: "Server is overloaded, please retry shortly",
          code: "LOAD_SHEDDED",
        });
        return;
      }

      (request as any)._loadShedStart = process.hrtime.bigint();
    }
  );
```

If `inFlight` exceeds the current `limit`, the request is immediately rejected with 503. The `Retry-After: 1` header tells the client to retry in 1 second. The high-resolution timer (`process.hrtime.bigint()`) measures latency in nanoseconds for precise EWMA calculation.

### Adaptive Limit Adjustment

```typescript
  fastify.addHook("onResponse",
    async (request: FastifyRequest, reply: FastifyReply) => {
      inFlight = Math.max(0, inFlight - 1);

      const start = (request as any)._loadShedStart as bigint | undefined;
      if (start === undefined) return;

      const elapsedNs = Number(process.hrtime.bigint() - start);
      const elapsedMs = elapsedNs / 1_000_000;

      // Update EWMA
      sampleCount++;
      if (sampleCount === 1) {
        ewmaLatencyMs = elapsedMs;
      } else {
        ewmaLatencyMs = ewmaAlpha * elapsedMs + (1 - ewmaAlpha) * ewmaLatencyMs;
      }

      // Adjust limit every 20 samples
      if (sampleCount % 20 === 0) {
        const success = reply.statusCode < 500;
        if (ewmaLatencyMs <= targetLatencyMs && success) {
          // Additive increase
          limit = Math.min(maxConcurrent, limit + 5);
        } else {
          // Multiplicative decrease
          limit = Math.max(minLimit, Math.ceil(limit * 0.9));
        }
      }
    }
  );
```

This is the AIMD (Additive Increase, Multiplicative Decrease) algorithm, the same principle behind TCP congestion control:

```
  Latency OK & no 5xx:  limit += 5     (cautious increase)
  Latency high or 5xx:  limit *= 0.9   (aggressive decrease)

  limit floor: 10 (minLimit)
  limit ceiling: 1000 (maxConcurrent)
```

The rationale from Little's Law: `L = lambda * W` (concurrency = throughput * latency). If the target latency is 50ms and we want 5,000 TPS, the theoretical concurrency limit is `5000 * 0.05 = 250`. The initial limit of 200 is just below this, allowing the AIMD algorithm to find the optimal point.

```
  Healthy state:
  +-----+
  | 250 | limit (after additive increases)
  +-----+
  EWMA latency: 30ms (below 50ms target)
  limit grows by 5 every 20 requests

  Overloaded state:
  +-----+
  | 250 | -> 225 -> 202 -> 182 (multiplicative decreases)
  +-----+
  EWMA latency: 120ms (above 50ms target)
  limit drops by 10% every 20 requests

  Recovery:
  +-----+
  | 182 | -> 187 -> 192 -> 197 -> ... -> 250 (slow recovery)
  +-----+
  EWMA latency: 40ms (back below target)
```

> **Key Insight: Why Multiplicative Decrease?**
>
> When a system is overloaded, halving the concurrency does not halve the load -- it allows the existing requests to complete faster, creating a positive feedback loop. Additive decrease would be too slow: the system stays in the overloaded state for too long, accumulating timeouts and cascade failures. The 10% decrease every 20 requests is aggressive enough to recover quickly but gentle enough to avoid oscillation.

---

## Graceful SIGTERM Drain

During deployments, Kubernetes sends SIGTERM to the gateway pod. The graceful drain middleware ensures zero dropped requests:

```typescript
const gracefulDrainPlugin: FastifyPluginAsync<GracefulDrainOptions> = async (
  fastify, opts
) => {
  const drainTimeoutMs = opts.drainTimeoutMs ?? 15000;

  const state: DrainState = {
    draining: false,
    inFlight: 0,
    rejected: 0,
  };

  fastify.addHook("onRequest",
    async (_request: FastifyRequest, reply: FastifyReply) => {
      if (state.draining) {
        state.rejected++;
        reply.header("Connection", "close");
        reply.code(503).send({
          error: "server is shutting down",
          code: "SERVICE_UNAVAILABLE",
        });
        return;
      }
      state.inFlight++;
    }
  );

  fastify.addHook("onResponse", async () => {
    state.inFlight--;
    if (state.draining && state.inFlight <= 0 && drainResolve) {
      drainResolve();
    }
  });
```

The shutdown sequence:

```
  1. SIGTERM received
     |
  2. state.draining = true
     New requests get 503 + Connection: close
     |
  3. Wait for in-flight requests (up to 15s)
     |
     +-- All complete?  --> 4. fastify.close()  --> 5. process.exit(0)
     |
     +-- Timeout (15s)? --> log warning --> 4. fastify.close() --> 5. process.exit(0)
```

```typescript
  const startDrain = async (): Promise<void> => {
    if (state.draining) return;
    state.draining = true;

    fastify.log.info(
      { inFlight: state.inFlight, timeoutMs: drainTimeoutMs },
      "settla-drain: starting graceful drain"
    );

    if (state.inFlight <= 0) {
      fastify.log.info("settla-drain: no in-flight requests, drain complete");
      return;
    }

    await Promise.race([
      new Promise<void>((resolve) => { drainResolve = resolve; }),
      new Promise<void>((resolve) => {
        setTimeout(() => {
          fastify.log.warn(
            { remaining: state.inFlight },
            "settla-drain: timeout reached with requests still in-flight"
          );
          resolve();
        }, drainTimeoutMs);
      }),
    ]);
  };

  process.on("SIGTERM", () => void shutdown("SIGTERM"));
  process.on("SIGINT", () => void shutdown("SIGINT"));
```

The `Connection: close` header on 503 responses during drain tells the client's HTTP library to stop reusing the connection. This forces the client to connect to a different gateway instance (which the load balancer will route to since this instance is being removed from the pool).

The `Promise.race` between the drain completion and the timeout ensures the process eventually exits even if a request is stuck. The 15-second timeout aligns with Kubernetes' default `terminationGracePeriodSeconds` of 30 seconds, giving the application 15 seconds to drain and another 15 seconds for Fastify to close listeners and clean up.

---

## How the Three Systems Interact

```
  Request arrives
       |
  +----v----+
  | Load    |  inFlight > limit?  --YES-->  503 (overloaded)
  | Shed    |
  +----+----+
       |
  +----v----+
  | Drain   |  state.draining?   --YES-->  503 (shutting down)
  |         |
  +----+----+
       |
  +----v----+
  | Auth    |  Valid API key?    --NO--->  401
  |         |
  +----+----+
       |
  +----v----+
  | Rate    |  Over tenant limit? --YES-->  429
  | Limit   |
  +----+----+
       |
  +----v----+
  | Route   |  Business logic
  | Handler |
  +----+----+
       |
  Response
```

The ordering is deliberate:

1. **Load shedding first** -- cheapest check (increment counter, compare), protects the entire process
2. **Drain check second** -- instant check of a boolean, prevents new work during shutdown
3. **Auth third** -- may require Redis/gRPC, but must happen before tenant-scoped rate limiting
4. **Rate limiting fourth** -- requires tenant ID from auth, per-tenant fairness enforcement
5. **Route handler last** -- the actual business logic

---

## Common Mistakes

1. **Rate limiting by IP instead of tenant.** In a B2B API, many requests come from the same IP (the tenant's server). IP-based limiting would throttle legitimate multi-tenant traffic from the same data center.

2. **Synchronizing every request to Redis.** At 5,000 TPS, that is 5,000 Redis round-trips per second just for rate limiting. The batch sync every 10 requests reduces this to 500, with negligible accuracy impact.

3. **Setting the load shed limit too high.** If `maxConcurrent` is 10,000 but the server can only handle 1,000 concurrent requests, load shedding never triggers. The initial limit should be below the theoretical maximum and allow AIMD to find the right level.

4. **Not handling SIGTERM.** Without graceful drain, a `kill` during deployment drops all in-flight requests. Users see 502 errors. The drain middleware eliminates this.

5. **Using `setInterval` without `unref`.** The cleanup timer in `DistributedRateLimiter` uses `unref()` to prevent it from keeping the process alive during shutdown. Without this, `process.exit()` would need to be called explicitly, potentially interrupting cleanup.

---

## Exercises

1. **Calculate accuracy.** With 4 gateway instances, a 1,000/sec limit, and batch sync every 10 requests: what is the worst-case overshoot before Redis sync detects the limit is reached? Under what conditions does this overshoot matter?

2. **Simulate AIMD.** Starting from limit=200, with EWMA latency oscillating between 30ms and 80ms every 100 requests: trace the limit value over 500 requests. Does it stabilize? At what value?

3. **Design a test.** Write a test for the graceful drain middleware that: starts the server, begins 5 long-running requests, sends SIGTERM, verifies new requests get 503, and verifies the 5 in-flight requests complete successfully.

4. **Evaluate trade-offs.** The current design syncs to Redis every 10 requests. What happens if you change this to every 1 request? Every 100 requests? Plot the trade-off between accuracy and Redis load.

---

## What's Next

In Chapter 6.6, we will examine the gRPC connection pool: how 50 persistent connections handle 5,000 TPS, the round-robin selection algorithm, health checking with auto-reconnection, and the circuit breaker that prevents cascade failures when the Go backend is down.
