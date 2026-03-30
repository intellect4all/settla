# Chapter 6.3: The REST Gateway (BFF Pattern)

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why Settla uses a TypeScript gateway between Tyk and the Go backend
2. Trace a REST request through the Fastify gateway to gRPC and back
3. Describe how response transformation maps between gRPC camelCase and REST snake_case
4. Identify how error sanitization prevents internal detail leakage to API consumers

---

## Why a TypeScript Gateway?

Settla's API layer has three tiers:

```
  Internet
     |
  +------+     Rate limits, TLS termination, access logging,
  | Tyk  |     API key existence check, CORS, analytics
  +------+
     |
  +----------+  Tenant resolution, business validation,
  | Fastify  |  REST<->gRPC transformation, idempotency,
  | (BFF)    |  rate limiting (per-tenant), load shedding
  +----------+
     |  gRPC (~50 persistent connections)
  +----------+
  | settla-  |  Domain logic, state machine, outbox
  | server   |
  +----------+
```

The TypeScript gateway is a Backend-For-Frontend (BFF). It exists for three reasons:

**1. REST API conventions differ from gRPC conventions.** REST uses snake_case JSON fields, HTTP status codes, URL path parameters, and query strings. gRPC uses camelCase, its own status codes, and all fields in the request body. The gateway bridges this impedance mismatch.

**2. Auth context injection.** The gateway extracts the tenant ID from the authenticated Bearer token and injects it into every gRPC request. The REST API never accepts `tenant_id` in the request body. This prevents tenant impersonation.

**3. OpenAPI documentation.** The gateway generates OpenAPI/Swagger docs at `/docs` from Fastify route schemas, giving tenants self-service API documentation.

---

## The Gateway Entrypoint: index.ts

The `buildApp` function in `api/gateway/src/index.ts` assembles the entire gateway. Here is the structure with annotations:

```typescript
export async function buildApp(deps?: {
  grpc?: SettlaGrpcClient;
  redis?: Redis | null;
  resolveTenant?: (keyHash: string) => Promise<any>;
  skipGrpcPool?: boolean;
  natsUrl?: string;
}) {
  const server = Fastify({
    bodyLimit: 1_048_576,   // 1 MiB -- prevents oversized payloads
    logger: {
      level: config.logLevel,
      serializers: {
        req(request) {
          return {
            method: request.method,
            url: request.url,
            hostname: request.hostname,
          };
        },
        res(reply) {
          return { statusCode: reply.statusCode };
        },
      },
    },
    genReqId: () => crypto.randomUUID(),
    disableRequestLogging: config.env === "production",
  });
```

Key decisions in the Fastify configuration:

- **`bodyLimit: 1_048_576`** -- A 1 MiB cap prevents clients from sending multi-gigabyte payloads that exhaust memory. Transfer requests are typically under 2 KB.
- **`genReqId: () => crypto.randomUUID()`** -- Every request gets a UUID as its `request_id`, propagated to gRPC via metadata and included in error responses for tracing.
- **`disableRequestLogging` in production** -- Tyk handles access logging. Logging every request in the BFF too would double log volume at 5,000 TPS.

### Plugin Registration Order

The registration order matters because Fastify processes hooks in registration order:

```typescript
  // 1. Load shedding (rejects when overloaded)
  await server.register(loadShedding, {
    maxConcurrent: 1000,
    targetLatencyMs: 50,
    initialLimit: 200,
  });

  // 2. Graceful drain (rejects during shutdown)
  await server.register(gracefulDrain);

  // 3. CORS
  await server.register(fastifyCors, { origin: config.corsOrigin });

  // 4. Security headers
  server.addHook("onSend", async (_request, reply) => {
    reply.header("X-Content-Type-Options", "nosniff");
    reply.header("X-Frame-Options", "DENY");
    reply.header("X-XSS-Protection", "0");
    reply.header("Referrer-Policy", "strict-origin-when-cross-origin");
    if (config.env === "production") {
      reply.header("Strict-Transport-Security",
        "max-age=31536000; includeSubDomains");
    }
  });

  // 5. OpenAPI docs
  await server.register(fastifySwagger, { ... });
  await server.register(fastifySwaggerUi, { routePrefix: "/docs" });

  // 6. Infrastructure (Redis, gRPC pool)
  // ... redis connection, grpc pool setup ...

  // 7. Auth plugin (tenant resolution)
  await server.register(authPlugin, { cache: authCache, resolveTenant, redis });

  // 8. Metrics
  await server.register(metricsPlugin);

  // 9. Rate limiting (after auth, so tenant_id is available)
  await server.register(rateLimitPlugin, { limit: config.rateLimitPerTenant, redis });

  // 10. Routes -- one registration per domain boundary
  await server.register(healthRoutes, { grpcPool, redis });
  await server.register(quoteRoutes, { grpc: grpcClient });
  await server.register(transferRoutes, { grpc: grpcClient, redis });
  await server.register(treasuryRoutes, { grpc: grpcClient });
  await server.register(treasurySseRoutes, { grpc: grpcClient });
  await server.register(ledgerRoutes, { grpc: grpcClient });
  await server.register(routeRoutes, { grpc: grpcClient });
  await server.register(tenantPortalRoutes, { grpc: grpcClient });
  await server.register(authRoutes, { grpc: grpcClient, redis });
  await server.register(depositRoutes, { grpc: grpcClient });
  await server.register(bankDepositRoutes, { grpc: grpcClient });
  await server.register(paymentLinkRoutes, { grpc: grpcClient });
  await server.register(analyticsRoutes, { grpc: grpcClient });
  await server.register(verifyRoutes, { grpc: grpcClient });
  await server.register(webhookRoutes, { redis, natsUrl: config.natsUrl });
  await server.register(bankWebhookRoutes, { redis, natsUrl: config.natsUrl, grpc: grpcClient });
  await server.register(opsRoutes);
```

The route registrations reveal the full API surface area. Seventeen route modules, each mapping to a domain boundary. Several routes have extra dependencies beyond gRPC: `transferRoutes` needs Redis for idempotency, `webhookRoutes` needs NATS for publishing inbound events, and `healthRoutes` needs direct pool/Redis access for readiness probes.

Load shedding fires first (before any processing), then graceful drain, then auth, then rate limiting. Rate limiting must come after auth because it needs the tenant ID.

### Dependency Injection for Testing

The `deps` parameter lets tests inject mock dependencies:

```typescript
// In production: no deps, everything is created from config
const server = await buildApp();

// In tests: inject mock gRPC client, skip Redis
const server = await buildApp({
  grpc: mockGrpcClient,
  redis: null,
  resolveTenant: async (hash) => ({ tenantId: "test-tenant", ... }),
});
```

The `if (!process.env.VITEST)` guard at the bottom prevents the server from starting when imported by the test runner:

```typescript
if (!process.env.VITEST) {
  start();
}
```

---

## Transfer Routes: REST to gRPC Translation

The transfer routes in `api/gateway/src/routes/transfers.ts` show the core translation pattern.

### POST /v1/transfers -- Create a Transfer

```typescript
export async function transferRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.post<{
    Body: {
      idempotency_key: string;
      external_ref?: string;
      source_currency: string;
      source_amount: string;
      dest_currency: string;
      sender?: { id?: string; name?: string; email?: string; country?: string };
      recipient: {
        name: string;
        account_number?: string;
        sort_code?: string;
        bank_name?: string;
        country: string;
        iban?: string;
      };
      quote_id?: string;
    };
  }>(
    "/v1/transfers",
    {
      schema: {
        body: createTransferBodySchema,
        response: {
          201: transferResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
          409: errorResponseSchema,
          429: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const body = request.body;

      try {
        const result = await grpc.createTransfer({
          tenantId: tenantAuth.tenantId,   // FROM AUTH, not from body
          idempotencyKey: body.idempotency_key,
          externalRef: body.external_ref,
          sourceCurrency: body.source_currency,
          sourceAmount: body.source_amount,
          destCurrency: body.dest_currency,
          sender: body.sender,
          recipient: body.recipient,
          quoteId: body.quote_id,
        }, request.id);
        return reply.status(201).send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
```

Three critical details:

**1. Tenant ID comes from auth, never from the body.** The line `tenantId: tenantAuth.tenantId` is the enforcement of Critical Invariant #7. Even if a malicious client sends a `tenant_id` field in the JSON body, it is ignored. The auth plugin resolved the tenant from the Bearer token, and that is the only tenant ID that reaches gRPC.

**2. Field name translation.** The REST body uses `snake_case` (`idempotency_key`, `source_currency`), but the gRPC call uses `camelCase` (`idempotencyKey`, `sourceCurrency`). This is the impedance mismatch the BFF resolves.

**3. Request ID propagation.** The `request.id` (a UUID generated by Fastify) is passed to `grpc.createTransfer` as the second argument. The gRPC client attaches it as `x-request-id` metadata, allowing end-to-end request tracing from the REST response through to the Go server logs.

### Response Transformation: mapTransfer

The gRPC response comes back in camelCase. The REST API must return snake_case:

```typescript
function mapTransfer(t: any): any {
  if (!t) return {};
  return {
    id: t.id,
    tenant_id: t.tenantId,
    external_ref: t.externalRef,
    idempotency_key: t.idempotencyKey,
    status: t.status,
    version: t.version,
    source_currency: t.sourceCurrency,
    source_amount: t.sourceAmount,
    dest_currency: t.destCurrency,
    dest_amount: t.destAmount,
    stable_coin: t.stableCoin,
    stable_amount: t.stableAmount,
    chain: t.chain,
    fx_rate: t.fxRate,
    fees: t.fees
      ? {
          on_ramp_fee: t.fees.onRampFee,
          network_fee: t.fees.networkFee,
          off_ramp_fee: t.fees.offRampFee,
          total_fee_usd: t.fees.totalFeeUsd,
        }
      : undefined,
    sender: t.sender
      ? {
          id: t.sender.id,
          name: t.sender.name,
          email: t.sender.email,
          country: t.sender.country,
        }
      : undefined,
    recipient: t.recipient
      ? {
          name: t.recipient.name,
          account_number: t.recipient.accountNumber,
          sort_code: t.recipient.sortCode,
          bank_name: t.recipient.bankName,
          country: t.recipient.country,
          iban: t.recipient.iban,
        }
      : undefined,
    quote_id: t.quoteId,
    created_at: t.createdAt,
    updated_at: t.updatedAt,
    funded_at: t.fundedAt,
    completed_at: t.completedAt,
    failed_at: t.failedAt,
    failure_reason: t.failureReason,
    failure_code: t.failureCode,
  };
}
```

This is tedious but necessary. An automatic camelCase-to-snake_case converter would break on fields like `fxRate` (which should become `fx_rate`, not `f_x_rate`). Explicit mapping prevents surprises.

### GET /v1/transfers/:transferId -- Single Transfer

```typescript
  app.get<{ Params: { transferId: string } }>(
    "/v1/transfers/:transferId",
    {
      schema: {
        params: {
          type: "object",
          properties: { transferId: { type: "string", format: "uuid" } },
          required: ["transferId"],
        },
        response: {
          200: transferResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getTransfer({
          tenantId: tenantAuth.tenantId,
          transferId: request.params.transferId,
        }, request.id);
        return reply.send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
```

The URL path parameter `:transferId` becomes `request.params.transferId`. The schema validates it is a UUID format string before the handler runs. If the format is wrong, Fastify returns a 400 automatically without reaching the handler.

### GET /v1/transfers -- List with Pagination

```typescript
  app.get<{
    Querystring: { page_size?: number; page_token?: string };
  }>(
    "/v1/transfers",
    {
      schema: {
        querystring: listTransfersQuerySchema,
        response: { 200: listTransfersResponseSchema },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listTransfers({
          tenantId: tenantAuth.tenantId,
          pageSize: request.query.page_size,
          pageToken: request.query.page_token,
        }, request.id);
        return reply.send({
          transfers: (result.transfers || []).map(mapTransfer),
          next_page_token: result.nextPageToken,
          total_count: result.totalCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
```

Pagination parameters come from the query string (`?page_size=50&page_token=abc`). The response wraps the array in an object with `next_page_token`, following the standard REST pagination pattern.

---

## Error Translation: gRPC to HTTP

When a gRPC call fails, the gateway translates the error to an appropriate HTTP response. The `mapGrpcError` function in `api/gateway/src/errors.ts` handles this:

```typescript
export function mapGrpcError(
  request: FastifyRequest,
  reply: FastifyReply,
  err: unknown,
): FastifyReply {
  const grpcErr = err as grpc.ServiceError;
  const request_id = request.id;

  // Extract domain code from "[CODE] message" format
  const { code: domainCode, cleanMessage } = extractDomainCode(rawMessage);

  // If we have a known domain code, use its mapping
  if (domainCode) {
    const mapping = DOMAIN_CODE_HTTP_MAP[domainCode];
    const message = sanitizeMessage(cleanMessage, mapping.httpStatus);
    return reply.status(mapping.httpStatus).send({
      error: domainCode,
      code: domainCode,
      message,
      request_id,
    });
  }

  // Fall back to gRPC status code mapping
  switch (grpcCode) {
    case grpc.status.NOT_FOUND:
      return reply.status(404).send({ ... });
    case grpc.status.ALREADY_EXISTS:
      return reply.status(409).send({ ... });
    case grpc.status.INVALID_ARGUMENT:
      return reply.status(400).send({ ... });
    // ... other cases ...
  }
}
```

The two-level mapping works as follows:

```
  Go Backend                    gRPC Wire                   TypeScript Gateway
  +-----------------------+     +-------------------+       +-------------------+
  | domain.CodeTransfer   | --> | codes.NotFound    |  -->  | 404 Not Found     |
  |   NotFound            |     | "[TRANSFER_NOT_   |       | { error:          |
  |                       |     |  FOUND] transfer  |       |   "TRANSFER_NOT_  |
  |                       |     |  not found"       |       |    FOUND",        |
  |                       |     +-------------------+       |   code: "...",    |
  +-----------------------+                                  |   message: "...", |
                                                             |   request_id: "." |
                                                             +-------------------+
```

### Error Message Sanitization

The `sanitizeMessage` function prevents internal details from reaching the client:

```typescript
const INTERNAL_PATTERNS = [
  /\bpq:\s/i,               // Postgres driver errors
  /\bsql:/i,                // SQL errors
  /\bconstraint\b/i,        // constraint violation details
  /\bstack trace\b/i,       // stack traces
  /\bgoroutine\b/i,         // Go stack traces
  /\bpanic\b/i,             // Go panics
  /\bindex\s+\w+_idx\b/i,   // index names
  /\brelation\s+"\w+"/i,    // table names in errors
];
```

In production, if an error message matches any of these patterns, the client sees a generic message instead of database internals. This is a security measure: leaked table names and constraint names help attackers understand the schema.

> **Key Insight: Defense in Depth for Error Messages**
>
> Error sanitization happens at two levels. The Go server's `mapDomainError` wraps domain errors in gRPC status codes. The TypeScript gateway's `sanitizeMessage` strips any infrastructure details that leaked through. Even if a Go developer accidentally returns a raw Postgres error, the gateway will catch and sanitize it before the client sees it.

---

## OpenAPI Documentation

The gateway auto-generates OpenAPI docs from route schemas:

```typescript
  await server.register(fastifySwagger, {
    openapi: {
      info: {
        title: "Settla API",
        description: "B2B stablecoin settlement infrastructure (BFF behind Tyk)",
        version: "1.0.0",
      },
      servers: [{ url: `http://localhost:${config.port}` }],
      components: {
        securitySchemes: {
          bearerAuth: { type: "http", scheme: "bearer" },
        },
      },
      security: [{ bearerAuth: [] }],
    },
  });

  await server.register(fastifySwaggerUi, { routePrefix: "/docs" });
```

Every route's `schema` property (body, params, querystring, response) is compiled into the OpenAPI spec. Navigating to `/docs` shows an interactive Swagger UI where tenants can explore the API, see example requests and responses, and try out endpoints.

---

## The Full Route Map

The gateway exposes routes across all domain boundaries. Here is the complete REST API surface:

```
  /health                          GET     Liveness probe
  /ready                           GET     Readiness probe (checks gRPC + Redis)
  /docs                            GET     Interactive API reference (Scalar)
  /openapi.json                    GET     Raw OpenAPI spec

  /v1/quotes                       POST    Create a real-time FX quote
  /v1/quotes/:quoteId              GET     Retrieve a quote by ID

  /v1/transfers                    POST    Create a transfer
  /v1/transfers                    GET     List transfers (paginated, filterable)
  /v1/transfers/:transferId        GET     Get a single transfer
  /v1/transfers/:transferId/cancel POST    Cancel a transfer
  /v1/transfers/:transferId/events GET     Get transfer lifecycle events

  /v1/treasury/positions           GET     List all positions
  /v1/treasury/positions/:currency GET     Get a specific position
  /v1/treasury/liquidity-report    GET     Get liquidity report with alerts
  /v1/treasury/topup               POST    Request a position top-up
  /v1/treasury/withdrawal          POST    Request a position withdrawal
  /v1/treasury/transactions        GET     List position transactions
  /v1/treasury/transactions/:id    GET     Get a position transaction
  /v1/treasury/position-events     GET     Get position event audit trail
  /v1/treasury/stream              GET     SSE: real-time position updates

  /v1/ledger/accounts              GET     List ledger accounts
  /v1/ledger/accounts/:code/balance GET   Get account balance
  /v1/ledger/accounts/:code/transactions GET List account transactions

  /v1/deposits                     POST    Create crypto deposit session
  /v1/deposits                     GET     List deposit sessions
  /v1/deposits/:id                 GET     Get deposit session
  /v1/deposits/:id/cancel          POST    Cancel deposit session
  /v1/deposits/:id/public-status   GET     Public deposit status (no auth)
  /v1/deposits/balances            GET     Get crypto balances
  /v1/deposits/:id/convert         POST    Convert crypto to fiat

  /v1/bank-deposits                POST    Create bank deposit session
  /v1/bank-deposits                GET     List bank deposit sessions
  /v1/bank-deposits/:id            GET     Get bank deposit session
  /v1/bank-deposits/:id/cancel     POST    Cancel bank deposit session
  /v1/bank-deposits/virtual-accounts GET   List virtual accounts

  /v1/payment-links                POST    Create payment link
  /v1/payment-links                GET     List payment links
  /v1/payment-links/:id            GET     Get payment link
  /v1/payment-links/:id/disable    POST    Disable payment link
  /v1/payment-links/resolve/:code  GET     Resolve by short code (public)
  /v1/payment-links/redeem/:code   POST    Redeem payment link (public)

  /v1/analytics/transfers          GET     Transfer analytics
  /v1/analytics/fees               GET     Fee breakdown analytics
  /v1/analytics/providers          GET     Provider performance analytics
  /v1/analytics/deposits           GET     Deposit analytics
  /v1/analytics/exports            POST    Create export job
  /v1/analytics/exports/:id        GET     Get export job status

  /v1/routes                       GET     Get routing options with scoring

  /v1/auth/register                POST    Tenant registration
  /v1/auth/login                   POST    Portal login
  /v1/auth/verify-email            POST    Email verification
  /v1/auth/refresh                 POST    Token refresh

  /v1/account/me                   GET     Get tenant profile
  /v1/account/api-keys             GET     List API keys
  /v1/account/api-keys             POST    Create API key
  /v1/account/api-keys/:id/revoke  POST    Revoke API key
  /v1/account/webhooks             PUT     Update webhook config
  /v1/account/metrics              GET     Dashboard metrics
```

Routes marked "(public)" skip authentication -- they are used by end-users clicking payment links or checking deposit status. These routes have stricter per-IP rate limiting (20 req/s) to prevent enumeration attacks.

---

## Server-Sent Events: Treasury Position Stream

The SSE endpoint at `/v1/treasury/stream` (in `api/gateway/src/routes/treasury-sse.ts`) is the only non-request/response route in the gateway. It provides real-time treasury position updates:

```typescript
export async function treasurySseRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.get(
    "/v1/treasury/stream",
    { /* schema */ },
    async (request: FastifyRequest, reply: FastifyReply) => {
      const { tenantAuth } = request;

      // Set SSE headers.
      reply.raw.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
        "X-Accel-Buffering": "no", // disable nginx buffering
      });

      // Determine start time from Last-Event-ID or default to now.
      const lastEventId = request.headers["last-event-id"] as string | undefined;
      let cursor = lastEventId
        ? new Date(lastEventId)
        : new Date();

      const heartbeatMs = 30_000;
      const pollMs = 2_000;

      // Heartbeat to keep connection alive.
      const heartbeatTimer = setInterval(() => {
        try {
          reply.raw.write(": heartbeat\n\n");
        } catch {
          // Connection closed
        }
      }, heartbeatMs);

      // Poll for new events every 2 seconds.
      const pollTimer = setInterval(async () => {
        try {
          const positionsResult = await grpc.getPositions({
            tenantId: tenantAuth.tenantId,
          }, request.id);

          for (const pos of positionsResult.positions || []) {
            const eventsResult = await grpc.getPositionEventHistory({
              tenantId: tenantAuth.tenantId,
              currency: pos.currency,
              location: pos.location,
              from: { seconds: Math.floor(cursor.getTime() / 1000), nanos: 0 },
              to: { seconds: Math.floor(Date.now() / 1000), nanos: 0 },
              limit: 100, offset: 0,
            }, request.id);

            for (const event of eventsResult.events || []) {
              const data = JSON.stringify({ /* event fields */ });
              reply.raw.write(`id: ${event.recordedAt}\n`);
              reply.raw.write(`event: position_update\n`);
              reply.raw.write(`data: ${data}\n\n`);

              // Advance cursor to latest event time.
              const eventTime = new Date(event.recordedAt);
              if (eventTime > cursor) cursor = eventTime;
            }
          }
        } catch (err) {
          request.log.warn({ err }, "settla-treasury-sse: poll error");
        }
      }, pollMs);

      // Cleanup on disconnect.
      request.raw.on("close", () => {
        clearInterval(heartbeatTimer);
        clearInterval(pollTimer);
      });

      await reply; // Keep response open
    },
  );
}
```

The SSE implementation has five key design decisions:

```
  Client                                          Gateway                              gRPC
    |                                                |                                   |
    | GET /v1/treasury/stream                        |                                   |
    | Last-Event-ID: 2026-03-15T12:30:00Z            |                                   |
    |----------------------------------------------->|                                   |
    |                                                | Set cursor = Last-Event-ID         |
    |                                                |                                   |
    |                              (every 2 seconds) |                                   |
    |                                                |-- getPositions() ----------------->|
    |                                                |<- positions -----------------------|
    |                                                |-- getPositionEventHistory() ------>|
    |                                                |<- events since cursor -------------|
    |                                                |                                   |
    | id: 2026-03-15T12:30:05Z                       |                                   |
    | event: position_update                         |                                   |
    | data: {"eventType":"CREDIT",...}               |                                   |
    |<-----------------------------------------------|                                   |
    |                                                |                                   |
    |                             (every 30 seconds) |                                   |
    | : heartbeat                                    |                                   |
    |<-----------------------------------------------|                                   |
```

1. **Polling-based, not push-based.** The gateway polls the gRPC service every 2 seconds rather than subscribing to NATS directly. This avoids adding a NATS dependency to the gateway process and keeps the SSE implementation simple.

2. **`Last-Event-ID` for resumption.** The SSE `id` field is set to the event's `recordedAt` timestamp. When a client reconnects, the browser automatically sends `Last-Event-ID` with the last received timestamp, and the gateway resumes from that point. No events are lost across reconnects.

3. **30-second heartbeat.** SSE connections are long-lived. Without periodic data, proxies and load balancers may timeout the connection. The heartbeat (a comment line `: heartbeat\n\n`) keeps the connection alive without sending actual event data.

4. **`X-Accel-Buffering: no`.** Nginx buffers responses by default, which would delay SSE events until the buffer fills. This header disables buffering for the SSE response.

5. **Cleanup on disconnect.** When the client closes the connection, the `close` event handler clears both timers, preventing leaked intervals.

---

## Security Headers

The gateway sets security headers on every response:

```typescript
  server.addHook("onSend", async (_request, reply) => {
    reply.header("X-Content-Type-Options", "nosniff");
    reply.header("X-Frame-Options", "DENY");
    reply.header("X-XSS-Protection", "0");
    reply.header("Referrer-Policy", "strict-origin-when-cross-origin");
    if (config.env === "production") {
      reply.header("Strict-Transport-Security",
        "max-age=31536000; includeSubDomains");
    }
  });
```

`X-XSS-Protection: 0` is intentionally set to zero, not one. Modern browsers' built-in XSS filters can themselves introduce vulnerabilities. The OWASP recommendation is to disable them and rely on Content Security Policy instead. HSTS is only set in production because local dev uses HTTP without TLS.

---

## Common Mistakes

1. **Accepting tenant_id from the request body.** This is the most dangerous mistake. The gateway must always take the tenant ID from the authenticated token, never from the client. The entire tenant isolation model depends on this.

2. **Forgetting to map field names.** If a gRPC response field `fxRate` is passed through to the REST response without mapping to `fx_rate`, the API contract is inconsistent. Clients parsing `snake_case` will miss the field.

3. **Leaking gRPC errors to the client.** Returning the raw gRPC error object exposes internal details (server address, stack traces). Always use `mapGrpcError`.

4. **Registering rate limiting before auth.** Rate limiting needs the tenant ID. If it runs before the auth plugin, it cannot rate-limit per tenant and falls back to per-IP, which is wrong for the multi-tenant model.

5. **Starting the server during tests.** Without the `if (!process.env.VITEST)` guard, importing the module starts the HTTP listener, causing port conflicts in parallel tests.

---

## Exercises

1. **Add a new route.** Implement `GET /v1/transfers/:transferId/events` that calls `grpc.listTransferEvents` and returns the events array. Include the schema definition with proper response types.

2. **Trace a 409.** A client sends a `CreateTransfer` with a duplicate `idempotency_key`. Trace the error from `core.Engine` through `mapDomainError` in Go, across gRPC, through `mapGrpcError` in TypeScript, to the final HTTP response. What does the client see?

3. **Schema validation.** The `createTransferBodySchema` validates the request body. What happens if a client sends `source_amount: 123` (number) instead of `source_amount: "123"` (string)? At which layer is this caught?

4. **CORS configuration.** In production, `config.corsOrigin` defaults to `false` if `SETTLA_CORS_ORIGIN` is not set. Explain why this is more secure than defaulting to `"*"`.

---

## What's Next

In Chapter 6.4, we will examine the authentication and caching system in detail: how the three-level cache (local LRU, Redis, DB) achieves 100-nanosecond auth lookups at 5,000 TPS, and how cross-instance cache invalidation works via Redis pub/sub.
