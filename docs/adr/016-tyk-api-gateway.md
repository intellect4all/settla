# ADR-016: Tyk API Gateway over Custom-Built Gateway

**Status:** Accepted
**Date:** 2026-03-09
**Authors:** Engineering Team

## Context

Settla's API layer serves fintech tenants (Lemfi, Fincra, Paystack) at 5,000 TPS peak. The original architecture used a Fastify gateway that handled everything: TLS termination, CORS, authentication, rate limiting, request validation, tenant resolution, gRPC calls to the Go backend, response transformation, and API analytics.

This created several problems at scale:

1. **Bloated codebase**: the Fastify gateway grew to 2,000+ lines mixing infrastructure concerns (auth, rate limiting, CORS, circuit breaking) with business logic (tenant resolution, fee calculation, request validation). Every infrastructure change risked breaking business logic and vice versa.

2. **Rate limiting at 5K TPS**: implementing per-tenant sliding window rate limiting in application code requires Redis round-trips on every request. At 5,000 TPS peak, this adds ~0.5ms per request and 2,500 Redis operations/second just for rate limiting — competing with the cache layer for Redis capacity.

3. **Auth overhead duplication**: every Fastify instance independently implemented API key hashing, tenant resolution, and auth caching. A bug in auth logic required redeploying the entire gateway fleet and risked authentication bypass.

4. **No centralized API analytics**: understanding per-tenant API usage patterns, error rates, and latency percentiles required scraping metrics from individual Fastify instances and aggregating them manually.

We evaluated three approaches:

| Approach | Auth/Rate-Limit | Analytics | Operational Cost | Customization |
|----------|----------------|-----------|-----------------|---------------|
| Custom Fastify (status quo) | Application code | DIY (Prometheus) | Low (no new infra) | Unlimited |
| Tyk Gateway | Declarative policy | Built-in dashboard | Medium (new component) | Plugin API |
| Kong Gateway | Declarative plugin | Kong Manager | Medium-High (Lua plugins) | Lua/Go plugins |
| Envoy + ext_authz | External auth service | Manual (access logs) | High (complex config) | WASM filters |

## Decision

We chose **Tyk API Gateway** as the edge gateway, reducing the Fastify application to a thin **Backend-for-Frontend (BFF)** that handles only business logic.

### Responsibility Split

**Tyk handles (infrastructure):**
- TLS termination and certificate management
- CORS policy enforcement
- API key authentication and tenant identification
- Per-tenant rate limiting (configurable policies: Lemfi 1,000 req/s, Fincra 500 req/s)
- Request size limits and basic validation
- Circuit breaking to backend services
- API analytics and usage dashboards
- DDoS protection and IP allowlisting

**Fastify BFF handles (business logic):**
- Tenant context resolution: Bearer token → SHA-256 → L1 local cache → L2 Redis → L3 gRPC/DB (full tenant details: fees, limits, status)
- Request validation against business rules (e.g., amount limits, corridor availability)
- gRPC connection pooling and calls to `settla-server`
- Response transformation (protobuf to JSON, error mapping)
- OpenAPI documentation serving (`/docs`)

### Request Flow

```
Client (fintech)
  │ HTTPS
  ▼
Tyk Gateway (:443)
  │ 1. TLS termination
  │ 2. API key presence check (key exists in Tyk)
  │ 3. Rate limit check (per-tenant policy)
  │ 4. CORS check
  │ HTTP (internal, Bearer token forwarded)
  ▼
Fastify BFF (:3000)
  │ 1. token → SHA-256 → L1 local cache (~100ns) → L2 Redis (~0.5ms) → L3 gRPC/DB
  │    (resolves full tenant context: id, fees, limits, status)
  │ 2. Validate request body (business rules)
  │ 3. gRPC call to settla-server (uses tenant_id from cache, never from request body)
  │ 4. Transform response
  │ JSON
  ▼
Client
```

> **Implementation note**: the BFF performs full tenant resolution (3-level cache) rather than trusting a Tyk-injected `X-Tenant-ID` header. This provides defence-in-depth and makes the BFF independently deployable without Tyk. Tyk validates API key existence and enforces rate limits; the BFF then resolves rich tenant context needed for business logic (fee schedules, daily limits, suspension status).

### Tyk Configuration

Per-tenant API policies are defined declaratively:

```json
{
  "name": "lemfi-production",
  "rate": 1000,
  "per": 1,
  "quota_max": 5000000,
  "quota_renewal_rate": 86400,
  "access_rights": {
    "settla-api": {
      "allowed_urls": ["/v1/quotes", "/v1/transfers", "/v1/treasury/*"]
    }
  }
}
```

Rate limit changes are policy updates — no code deployment required.

### BFF Simplification

The Fastify BFF was reduced from 2,000+ lines to <1,000 lines by removing:
- Auth middleware (200+ lines) — replaced by trusting Tyk's `X-Tenant-ID` header
- Rate limiting middleware (150+ lines) — handled by Tyk
- CORS configuration (50+ lines) — handled by Tyk
- Circuit breaker logic (100+ lines) — handled by Tyk
- TLS setup (30+ lines) — handled by Tyk

The BFF uses `fastify-plugin` for a global `authPlugin` that resolves tenant context via the 3-level cache, and standard Fastify routes for business logic.

## Consequences

### Benefits
- **Separation of concerns**: infrastructure policies (auth, rate limiting, CORS) are managed declaratively in Tyk, while business logic lives in the BFF. Changes to rate limits do not require code deployments.
- **Reduced BFF attack surface**: the BFF only accepts connections from Tyk (internal network), not from the public internet. Authentication happens before traffic reaches the BFF.
- **Centralized API analytics**: Tyk provides per-tenant, per-endpoint analytics (request counts, latency percentiles, error rates) without custom instrumentation. Operations can identify abusive tenants or degraded endpoints from a single dashboard.
- **Per-tenant policies without code**: adding a new tenant with custom rate limits, IP allowlists, or endpoint restrictions is a Tyk policy change — no BFF deployment required.
- **Simpler BFF testing**: with auth and rate limiting removed, BFF tests focus purely on business logic (validation, gRPC calls, response transformation). No need to mock Redis for rate limiting or simulate auth flows.

### Trade-offs
- **Additional infrastructure component**: Tyk is a new dependency in the deployment topology. It must be deployed, monitored, and maintained. Tyk cluster failure means complete API unavailability.
- **Learning curve**: the team must learn Tyk's configuration model, plugin system, and operational characteristics. This is a one-time cost but delays initial productivity.
- **Request latency overhead**: Tyk adds ~1-3ms per request for auth + rate limiting. At 5,000 TPS, this is 5-15 seconds of cumulative latency per second. This is comparable to the application-level auth overhead it replaces, so net latency impact is near zero.
- **Vendor dependency**: Tyk is open-source (MPL-2.0) with a commercial offering. The open-source version covers our requirements. However, advanced features (RBAC dashboard, SLA monitoring) require the commercial tier.
- **Header trust model**: the BFF trusts Tyk's `X-Tenant-ID` header without re-validating. If Tyk is misconfigured or bypassed, tenant isolation breaks. This is mitigated by network-level enforcement (BFF only accepts connections from Tyk's internal IP range).

### Mitigations
- **Tyk high availability**: Tyk is deployed as a cluster (3+ nodes) behind a load balancer. Individual node failures do not cause API unavailability. Tyk uses Redis for distributed rate limiting state, which is the same Redis cluster used by the cache layer.
- **Fallback auth in BFF**: the BFF retains a lightweight auth validation (verify `X-Tenant-ID` header is present and is a valid UUID). This prevents processing unauthenticated requests if Tyk is misconfigured, though it does not replace full auth.
- **Network isolation**: the BFF listens only on the internal network interface. Kubernetes NetworkPolicy (or Docker network isolation in development) ensures no direct external access to the BFF.
- **Configuration as code**: Tyk API definitions and policies are stored in the repository (`deploy/tyk/`) and applied via CI/CD, not through the Tyk dashboard. This ensures reproducibility and review.

## Threshold Triggers for Revisiting

- **Tyk becomes a throughput bottleneck at >10,000 TPS**: if Tyk's per-node capacity cannot scale to handle traffic growth. Migration path: Kong (higher raw throughput on equivalent hardware) or Envoy with ext_authz (C++ performance, WASM extensibility).
- **Tyk licensing costs become prohibitive**: if the commercial tier becomes necessary and costs exceed the value. Migration path: Kong (Apache 2.0), Envoy (Apache 2.0), or a custom Go gateway (leveraging existing auth and rate limiting code from the cache module).
- **Complex request transformation requirements**: if business logic needs to run before Tyk's auth layer (e.g., request body inspection for routing). Migration path: Tyk custom Go plugins, or move transformation logic into a pre-auth middleware layer.

## References

- [Tyk API Gateway Documentation](https://tyk.io/docs/)
- [API Gateway Pattern](https://microservices.io/patterns/apigateway.html) — Chris Richardson
- [Backend for Frontend Pattern](https://samnewman.io/patterns/architectural/bff/) — Sam Newman
- ADR-006 (Two-Level Cache) — the cache layer that Tyk's Redis shares
- ADR-009 (gRPC Between TypeScript and Go) — the BFF-to-backend communication protocol
