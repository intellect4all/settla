/**
 * Distributed per-tenant rate limiter with L1 local + L2 Redis.
 *
 * Hot path: local in-memory counter (~100ns).
 * On window rotation: sync distributed count from Redis.
 * Fallback: local-only if Redis is unavailable.
 */
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import fp from "fastify-plugin";
import type { Redis } from "ioredis";
import { rateLimitTotal } from "../metrics.js";

interface RateLimitEntry {
  count: number;
  windowStart: number;
}

const BYPASS_PREFIXES = ["/health", "/docs", "/documentation", "/metrics", "/webhooks/"];

function shouldBypass(url: string): boolean {
  for (const prefix of BYPASS_PREFIXES) {
    if (url === prefix || url.startsWith(prefix)) return true;
  }
  return false;
}

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

  /**
   * Check rate limit. Returns { allowed, remaining, resetMs }.
   */
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
      // On rotation, try to fetch distributed count from Redis
      let distributedCount = 0;
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const val = await this.redis.incr(key);
          // Set expiry only if this is the first increment (val === 1)
          if (val === 1) {
            await this.redis.expire(key, windowSec + 1);
          }
          distributedCount = val;
        } catch {
          // Redis unavailable — fall back to local
        }
      }

      entry = { count: Math.max(1, distributedCount), windowStart: now };
      this.local.set(tenantId, entry);

      const resetMs = entry.windowStart + this.windowMs;
      const remaining = Math.max(0, this.limit - entry.count);
      return { allowed: true, remaining, resetMs };
    }

    // Increment local counter
    entry.count++;

    // If local exceeds limit, optionally cross-check Redis
    if (entry.count > this.limit) {
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const distributed = await this.redis.get(key);
          if (distributed && Number(distributed) <= this.limit) {
            // Other instances haven't hit the limit — allow
            await this.redis.incr(key);
            const resetMs = entry.windowStart + this.windowMs;
            return { allowed: true, remaining: 0, resetMs };
          }
        } catch {
          // Redis unavailable — enforce local limit
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

    const resetMs = entry.windowStart + this.windowMs;
    const remaining = Math.max(0, this.limit - entry.count);
    return { allowed: true, remaining, resetMs };
  }

  close(): void {
    if (this.cleanupTimer) {
      clearInterval(this.cleanupTimer);
      this.cleanupTimer = null;
    }
  }
}

export interface RateLimitPluginOpts {
  limit: number;
  windowMs?: number;
  redis: Redis | null;
}

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

    fastify.addHook("onRequest", async (request: FastifyRequest, reply: FastifyReply) => {
      if (shouldBypass(request.url)) return;

      const tenantId = request.tenantAuth?.tenantId;
      if (!tenantId) return; // auth failed — let auth hook handle 401

      const { allowed, remaining, resetMs } = await limiter.check(tenantId);

      // Store for onSend hook
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
        const retryAfterSec = Math.max(1, Math.ceil((resetMs - Date.now()) / 1000));
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
    });

    fastify.addHook("onSend", async (request: FastifyRequest, reply: FastifyReply) => {
      const rl = (request as any)._rateLimit;
      if (!rl) return;

      reply.header("X-RateLimit-Limit", String(rl.limit));
      reply.header("X-RateLimit-Remaining", String(rl.remaining));
      reply.header("X-RateLimit-Reset", String(rl.reset));
    });
  },
  { name: "settla-rate-limit" },
);
