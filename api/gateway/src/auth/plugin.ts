import { createHash, createHmac } from "node:crypto";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import type { Redis } from "ioredis";
import type { TenantAuth, TenantAuthCache } from "./cache.js";
import { verifyJwt } from "./jwt.js";
import type { JwtPayload } from "./jwt.js";

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

/**
 * Check if a URL matches a public route that skips auth.
 * These routes are unauthenticated and must have stricter rate limits.
 */
function isPublicRoute(url: string): boolean {
  return (
    url.startsWith("/v1/payment-links/resolve/") ||
    url.startsWith("/v1/payment-links/redeem/") ||
    /^\/v1\/deposits\/[^/]+\/public-status/.test(url)
  );
}

/**
 * Strict per-IP rate limiter for public (unauthenticated) endpoints.
 * These endpoints (payment link resolve/redeem, deposit public status) are
 * exposed without auth and are vulnerable to scraping / enumeration attacks.
 * 20 req/s per IP is intentionally low — legitimate users hitting these
 * endpoints are end-users clicking a payment link, not automated systems.
 */
const PUBLIC_ROUTE_RATE_LIMIT = 20; // 20 req/s per IP
const PUBLIC_ROUTE_WINDOW_MS = 1_000;

interface PublicRateLimitEntry {
  count: number;
  windowStart: number;
}
const publicRouteRateLimitMap = new Map<string, PublicRateLimitEntry>();

// Cleanup stale public rate-limit entries every 30s
const publicRateLimitCleanupTimer = setInterval(() => {
  const cutoff = Date.now() - 60_000;
  for (const [key, entry] of publicRouteRateLimitMap) {
    if (entry.windowStart < cutoff) {
      publicRouteRateLimitMap.delete(key);
    }
  }
}, 30_000);
publicRateLimitCleanupTimer.unref();

/**
 * Per-IP rate limiter for failed authentication attempts.
 * Limits to 10 failures per 60-second window per source IP to prevent
 * API key brute-force enumeration attacks.
 */
const AUTH_FAILURE_RATE_LIMIT = 10;
const AUTH_FAILURE_WINDOW_MS = 60_000;

interface AuthFailureEntry {
  count: number;
  windowStart: number;
}
const authFailureRateLimitMap = new Map<string, AuthFailureEntry>();

// Cleanup stale auth failure entries every 60s
const authFailureCleanupTimer = setInterval(() => {
  const cutoff = Date.now() - 120_000;
  for (const [key, entry] of authFailureRateLimitMap) {
    if (entry.windowStart < cutoff) {
      authFailureRateLimitMap.delete(key);
    }
  }
}, 60_000);
authFailureCleanupTimer.unref();

function checkAuthFailureRateLimit(ip: string): boolean {
  const now = Date.now();
  let entry = authFailureRateLimitMap.get(ip);
  if (!entry || now - entry.windowStart >= AUTH_FAILURE_WINDOW_MS) {
    return false; // no failures in current window
  }
  return entry.count >= AUTH_FAILURE_RATE_LIMIT;
}

function recordAuthFailure(ip: string): void {
  const now = Date.now();
  let entry = authFailureRateLimitMap.get(ip);
  if (!entry || now - entry.windowStart >= AUTH_FAILURE_WINDOW_MS) {
    entry = { count: 1, windowStart: now };
    authFailureRateLimitMap.set(ip, entry);
  } else {
    entry.count++;
  }
}

export interface AuthPluginOpts {
  cache: TenantAuthCache;
  /** Resolve tenant from DB via gRPC. Returns null if key is invalid. */
  resolveTenant: (keyHash: string) => Promise<TenantAuth | null>;
  /**
   * Resolve tenant context for a JWT-authenticated user.
   * Called when a portal JWT is used instead of an API key.
   * Returns null if the tenant cannot be resolved.
   */
  resolveJwtTenant?: (payload: JwtPayload) => Promise<TenantAuth | null>;
  /** JWT secret for verifying portal tokens. */
  jwtSecret?: string;
  /**
   * Redis client used for pub/sub invalidation broadcast.
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
 * On revocation, `app.invalidateAuthCache(keyHash)` flushes L1+L2 and
 * publishes to `settla:auth:invalidate` so all peer gateway instances also evict
 * the key within milliseconds (no wait for the 30s / 5min TTL).
 */
/**
 * Interval (ms) between lightweight DB revalidation checks for
 * keys served from L2 (Redis) cache. See the inline comment in the API key
 * flow for the full trade-off analysis.
 */
const DB_REVALIDATION_INTERVAL_MS = Number(process.env.SETTLA_DB_REVALIDATION_INTERVAL_MS) || 10_000;

/** Tracks the last time each key hash was revalidated against the DB. */
const dbRevalidationTimestamps = new Map<string, number>();

// Cleanup stale DB-revalidation timestamps every 60s
const dbRevalidationCleanupTimer = setInterval(() => {
  const cutoff = Date.now() - 120_000; // entries older than 2 minutes
  for (const [key, ts] of dbRevalidationTimestamps) {
    if (ts < cutoff) dbRevalidationTimestamps.delete(key);
  }
}, 60_000);
dbRevalidationCleanupTimer.unref();

export const authPlugin = fp(async function authPluginInner(
  app: FastifyInstance,
  opts: AuthPluginOpts,
): Promise<void> {
  const { cache, resolveTenant, redis } = opts;

  app.decorateRequest("tenantAuth", undefined as unknown as TenantAuth);

  // ── Cross-instance invalidation via Redis pub/sub ────────────────
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

  const { resolveJwtTenant } = opts;
  // Support JWT secret rotation: SETTLA_JWT_SECRET=new_secret,old_secret
  // Split by comma and filter empty strings so verifyJwt tries each key.
  const jwtSecrets: string[] | undefined = opts.jwtSecret
    ? opts.jwtSecret.split(",").map((s) => s.trim()).filter(Boolean)
    : undefined;

  app.addHook(
    "onRequest",
    async (request: FastifyRequest, reply: FastifyReply) => {
      // Skip auth for health/docs/metrics/webhook endpoints and public auth routes
      if (
        request.url === "/health" ||
        request.url.startsWith("/docs") ||
        request.url.startsWith("/documentation") ||
        request.url === "/openapi.json" ||
        request.url === "/metrics" ||
        request.url.startsWith("/webhooks/") ||
        request.url.startsWith("/v1/auth/") ||
        request.url.startsWith("/v1/ops/") ||
        request.url.startsWith("/v1/payment-links/resolve/") ||
        request.url.startsWith("/v1/payment-links/redeem/") ||
        (request.url.match(/^\/v1\/deposits\/[^/]+\/public-status/) !== null)
      ) {
        // Public payment-link and deposit routes skip auth but still
        // need strict per-IP rate limiting to prevent scraping and enumeration.
        // 20 req/s per IP — legitimate users are humans clicking payment links.
        if (isPublicRoute(request.url)) {
          const ip = request.ip;
          const now = Date.now();
          let entry = publicRouteRateLimitMap.get(ip);
          if (!entry || now - entry.windowStart >= PUBLIC_ROUTE_WINDOW_MS) {
            entry = { count: 1, windowStart: now };
            publicRouteRateLimitMap.set(ip, entry);
          } else {
            entry.count++;
            if (entry.count > PUBLIC_ROUTE_RATE_LIMIT) {
              app.log.warn(
                { ip, url: request.url, count: entry.count, limit: PUBLIC_ROUTE_RATE_LIMIT },
                "Public route rate limit exceeded",
              );
              return reply
                .status(429)
                .header("Retry-After", "1")
                .header("X-RateLimit-Limit", String(PUBLIC_ROUTE_RATE_LIMIT))
                .header("X-RateLimit-Remaining", "0")
                .send({
                  error: "rate_limit_exceeded",
                  message: "Too many requests to public endpoint",
                });
            }
          }
        }
        return;
      }

      // Check per-IP auth failure rate limit before processing credentials.
      if (checkAuthFailureRateLimit(request.ip)) {
        app.log.warn(
          { ip: request.ip, path: request.url },
          "auth: too many failed auth attempts from IP",
        );
        return reply
          .status(429)
          .header("Retry-After", "60")
          .send({
            error: "TOO_MANY_AUTH_FAILURES",
            message: "Too many failed authentication attempts. Try again later.",
          });
      }

      const authHeader = request.headers.authorization;

      if (!authHeader || (!authHeader.startsWith("Bearer ") && !authHeader.startsWith("bearer "))) {
        recordAuthFailure(request.ip);
        app.log.warn(
          { ip: request.ip, userAgent: request.headers["user-agent"], path: request.url },
          "auth: missing or invalid Authorization header",
        );
        reply.code(401).send({
          error: "UNAUTHORIZED",
          message: "Missing or invalid Authorization header",
        });
        return;
      }

      const token = authHeader.slice(7);
      if (!token) {
        app.log.warn(
          { ip: request.ip, userAgent: request.headers["user-agent"], path: request.url },
          "auth: empty bearer token",
        );
        reply.code(401).send({
          error: "UNAUTHORIZED",
          message: "Empty bearer token",
        });
        return;
      }

      // Dual auth: API keys start with sk_live_ or sk_test_, everything else is JWT
      const isApiKey = token.startsWith("sk_live_") || token.startsWith("sk_test_");

      if (isApiKey) {
        // ── API key flow (existing) ──────────────────────────────────────
        const keyHash = hashApiKey(token);

        // Track where the cache entry came from for periodic revalidation.
        // "l1" = local cache, "l2" = Redis, "l3" = DB (source of truth).
        let cacheSource: "l1" | "l2" | "l3" = "l3";

        let auth = cache.getLocal(keyHash);
        if (auth) {
          cacheSource = "l1";
        }
        if (!auth) {
          auth = await cache.getRedis(keyHash);
          if (auth) cacheSource = "l2";
        }
        if (!auth) {
          const resolved = await resolveTenant(keyHash);
          if (!resolved) {
            recordAuthFailure(request.ip);
            app.log.warn(
              { ip: request.ip, userAgent: request.headers["user-agent"], path: request.url },
              "auth: invalid API key",
            );
            reply.code(401).send({
              error: "UNAUTHORIZED",
              message: "Invalid API key",
            });
            return;
          }
          auth = resolved;
          cacheSource = "l3";
          await cache.set(keyHash, auth);
        }

        // ── Periodic DB revalidation to reduce revocation window ──
        // Trade-off: The L1 cache (30s TTL) and L2 cache (5min TTL) mean a
        // revoked API key could remain valid for up to 5 minutes if the pub/sub
        // invalidation misses a gateway instance (e.g., network partition).
        //
        // To mitigate this, when a key was served from L2 (Redis) — meaning it
        // wasn't in the hot L1 cache — we do a lightweight DB check every 30s.
        // This uses a separate timestamp map to avoid re-checking on every request.
        // The DB check only verifies the key's status, not the full tenant context.
        //
        // This reduces the worst-case revocation window from 5 minutes to ~30s
        // at the cost of one extra DB round-trip per key per 30s window rotation.
        // Keys served from L1 are not rechecked (they expire in 30s anyway).
        // Keys just fetched from L3 (DB) are already fresh.
        if (cacheSource === "l2") {
          const lastDbCheckKey = `dbcheck:${keyHash}`;
          const lastCheck = dbRevalidationTimestamps.get(lastDbCheckKey) ?? 0;
          const now = Date.now();
          if (now - lastCheck > DB_REVALIDATION_INTERVAL_MS) {
            dbRevalidationTimestamps.set(lastDbCheckKey, now);
            // Fire-and-forget lightweight DB check — don't block the request
            // unless the key is actually revoked.
            try {
              const freshAuth = await resolveTenant(keyHash);
              if (!freshAuth) {
                // Key was revoked — evict from all caches and reject
                await cache.invalidate(keyHash);
                dbRevalidationTimestamps.delete(lastDbCheckKey);
                reply.code(401).send({
                  error: "UNAUTHORIZED",
                  message: "API key has been revoked",
                });
                return;
              }
              // Refresh cache with fresh data from DB
              auth = freshAuth;
              await cache.set(keyHash, auth);
            } catch (err) {
              // DB check failed — allow request through with cached data.
              // Better to serve a potentially-stale auth than to reject all
              // traffic when the DB is momentarily unreachable.
              app.log.warn(
                { err, keyHash: keyHash.slice(0, 8) + "..." },
                "auth: DB revalidation failed, using cached auth",
              );
            }
          }
        }

        if (auth.status === "SUSPENDED") {
          reply.code(403).send({
            error: "FORBIDDEN",
            message: "Tenant is suspended",
          });
          return;
        }

        request.tenantAuth = auth;
      } else {
        // ── JWT flow (portal) ────────────────────────────────────────────
        if (!jwtSecrets || jwtSecrets.length === 0) {
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "JWT authentication not configured",
          });
          return;
        }

        const payload = verifyJwt(token, jwtSecrets);
        if (!payload) {
          app.log.warn(
            { ip: request.ip, userAgent: request.headers["user-agent"], path: request.url },
            "auth: invalid or expired JWT token",
          );
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "Invalid or expired token",
          });
          return;
        }

        if (payload.type !== "access") {
          reply.code(401).send({
            error: "UNAUTHORIZED",
            message: "Token is not an access token",
          });
          return;
        }

        // Resolve full tenant context for this JWT user
        let auth: TenantAuth | null = null;
        if (resolveJwtTenant) {
          auth = await resolveJwtTenant(payload);
        }

        if (!auth) {
          // Minimal TenantAuth from JWT claims — sufficient for portal routes
          auth = {
            tenantId: payload.tid,
            slug: "",
            status: "ACTIVE",
            feeSchedule: { onRampBps: 0, offRampBps: 0, minFeeUsd: "0", maxFeeUsd: "0" },
            dailyLimitUsd: "0",
            perTransferLimit: "0",
            userId: payload.sub,
            userRole: payload.role,
          };
        }

        // ONBOARDING tenants are allowed through JWT auth (they need portal access)
        if (auth.status === "SUSPENDED") {
          reply.code(403).send({
            error: "FORBIDDEN",
            message: "Tenant is suspended",
          });
          return;
        }

        request.tenantAuth = auth;
      }
    },
  );
});

/**
 * Hash an API key for storage/lookup. Uses HMAC-SHA256 when
 * SETTLA_API_KEY_HMAC_SECRET is set, plain SHA-256 otherwise.
 * Keys are never stored raw.
 */
const API_KEY_HMAC_SECRET = process.env.SETTLA_API_KEY_HMAC_SECRET || "";

// Warn at startup if HMAC secret is not configured — API key hashes will use
// plain SHA-256 which is weaker against database breach + rainbow table attacks.
if (!API_KEY_HMAC_SECRET) {
  const env = process.env.SETTLA_ENV || process.env.NODE_ENV || "development";
  if (env === "production") {
    console.error(
      "FATAL: SETTLA_API_KEY_HMAC_SECRET is required in production — refusing to start with plain SHA-256 API key hashing",
    );
    process.exit(1);
  } else {
    console.warn(
      "WARNING: SETTLA_API_KEY_HMAC_SECRET is not set — API keys hashed with plain SHA-256 (not safe for production)",
    );
  }
}

export function hashApiKey(key: string): string {
  if (API_KEY_HMAC_SECRET) {
    return createHmac("sha256", API_KEY_HMAC_SECRET).update(key).digest("hex");
  }
  return createHash("sha256").update(key).digest("hex");
}
