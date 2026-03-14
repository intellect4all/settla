import { createHash } from "node:crypto";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import type { Redis } from "ioredis";
import type { TenantAuth, TenantAuthCache } from "./cache.js";

/** Redis pub/sub channel used to broadcast revocations across gateway instances. */
const AUTH_INVALIDATE_CHANNEL = "settla:auth:invalidate";

declare module "fastify" {
  interface FastifyInstance {
    /**
     * Invalidate a revoked API key from L1 (local) and L2 (Redis) caches,
     * and broadcast the revocation to all other gateway instances via Redis pub/sub.
     * Call this immediately after a successful revokeAPIKey gRPC call.
     */
    invalidateAuthCache(keyHash: string): Promise<void>;
  }
  interface FastifyRequest {
    tenantAuth: TenantAuth;
  }
}

export interface AuthPluginOpts {
  cache: TenantAuthCache;
  /** Resolve tenant from DB via gRPC. Returns null if key is invalid. */
  resolveTenant: (keyHash: string) => Promise<TenantAuth | null>;
  /**
   * Redis client used for pub/sub invalidation broadcast (SEC-2).
   * When provided, revocations are published to all gateway replicas.
   * Passing null disables cross-instance invalidation (single-instance dev).
   */
  redis?: Redis | null;
}

/**
 * Fastify plugin for tenant resolution from Bearer token.
 * Tyk validates that the API key exists; this plugin resolves the full tenant
 * context (fees, limits, status) needed for business logic.
 * Flow: token → SHA-256 → L1 local cache → L2 Redis → L3 gRPC/DB
 *
 * SEC-2: On revocation, `app.invalidateAuthCache(keyHash)` flushes L1+L2 and
 * publishes to `settla:auth:invalidate` so all peer gateway instances also evict
 * the key within milliseconds (no wait for the 30s / 5min TTL).
 */
export const authPlugin = fp(async function authPluginInner(
  app: FastifyInstance,
  opts: AuthPluginOpts,
): Promise<void> {
  const { cache, resolveTenant, redis } = opts;

  app.decorateRequest("tenantAuth", undefined as unknown as TenantAuth);

  // ── SEC-2: Cross-instance invalidation via Redis pub/sub ────────────────
  // We need a dedicated subscriber connection — a Redis client in subscribe mode
  // cannot issue regular commands, so we duplicate the connection.
  let subscriber: Redis | null = null;
  if (redis) {
    try {
      subscriber = redis.duplicate();
      await subscriber.subscribe(AUTH_INVALIDATE_CHANNEL);
      subscriber.on("message", (_channel: string, keyHash: string) => {
        // Evict from L1 only — the publisher already deleted from L2 (Redis).
        cache.deleteLocal(keyHash);
        app.log.debug({ keyHash: keyHash.slice(0, 8) + "..." }, "auth: evicted key via pub/sub");
      });
      subscriber.on("error", (err: Error) => {
        app.log.warn({ err }, "auth: subscriber error");
      });
      app.log.info("auth: subscribed to Redis invalidation channel");
    } catch (err) {
      app.log.warn({ err }, "auth: failed to subscribe to Redis invalidation channel — cross-instance revocation disabled");
      subscriber = null;
    }
  }

  app.addHook("onClose", async () => {
    if (subscriber) {
      try {
        await subscriber.unsubscribe(AUTH_INVALIDATE_CHANNEL);
        subscriber.disconnect();
      } catch {
        // best-effort
      }
    }
  });

  /**
   * Invalidate one key: delete from L1, delete from L2 (Redis), then broadcast
   * to all peer gateway instances via Redis pub/sub so they drop their L1 copy.
   */
  app.decorate("invalidateAuthCache", async (keyHash: string): Promise<void> => {
    // L1 + L2 eviction (L2 DEL is inside TenantAuthCache.invalidate)
    await cache.invalidate(keyHash);

    // Broadcast to peers — they will evict from their L1 on receipt
    if (redis) {
      try {
        await redis.publish(AUTH_INVALIDATE_CHANNEL, keyHash);
      } catch (err) {
        app.log.warn(
          { err, keyHash: keyHash.slice(0, 8) + "..." },
          "auth: failed to publish invalidation — peer gateways may serve revoked key until TTL expires",
        );
      }
    }
  });

  app.addHook(
    "onRequest",
    async (request: FastifyRequest, reply: FastifyReply) => {
      // Skip auth for health/docs/metrics/webhook endpoints
      if (
        request.url === "/health" ||
        request.url.startsWith("/docs") ||
        request.url.startsWith("/documentation") ||
        request.url === "/metrics" ||
        request.url.startsWith("/webhooks/")
      ) {
        return;
      }

      const authHeader = request.headers.authorization;

      if (!authHeader || (!authHeader.startsWith("Bearer ") && !authHeader.startsWith("bearer "))) {
        reply.code(401).send({
          error: "UNAUTHORIZED",
          message: "Missing or invalid Authorization header",
        });
        return;
      }

      const token = authHeader.slice(7);
      if (!token) {
        reply.code(401).send({
          error: "UNAUTHORIZED",
          message: "Empty bearer token",
        });
        return;
      }

      const keyHash = hashApiKey(token);

      // L1: local in-process cache (~100ns)
      let auth = cache.getLocal(keyHash);

      // L2: Redis (~0.5ms)
      if (!auth) {
        auth = await cache.getRedis(keyHash);
      }

      // L3: DB via gRPC (source of truth)
      if (!auth) {
        const resolved = await resolveTenant(keyHash);
        if (!resolved) {
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "Invalid API key",
          });
          return;
        }
        auth = resolved;
        // Populate caches for next hit
        await cache.set(keyHash, auth);
      }

      // Check tenant status
      if (auth.status !== "ACTIVE") {
        reply.code(403).send({
          error: "FORBIDDEN",
          message: "Tenant is suspended",
        });
        return;
      }

      request.tenantAuth = auth;
    },
  );
});

/** SHA-256 hash of an API key. Keys are never stored raw. */
export function hashApiKey(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}
