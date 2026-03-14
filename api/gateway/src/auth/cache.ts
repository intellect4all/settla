/**
 * TenantAuthCache is a two-level cache for tenant authentication:
 *   L1: in-process Map with TTL (30s default, ~100ns lookup)
 *   L2: Redis (5min TTL, ~0.5ms lookup)
 *   L3: DB via gRPC (source of truth)
 *
 * At 5,000 TPS, a Redis round-trip per request adds 2.5 seconds of aggregate
 * latency per second. The local cache eliminates this for repeat lookups.
 */

import type { Redis } from "ioredis";

export interface TenantAuth {
  tenantId: string;
  slug: string;
  status: string;
  feeSchedule: {
    onRampBps: number;
    offRampBps: number;
    minFeeUsd: string;
    maxFeeUsd: string;
  };
  dailyLimitUsd: string;
  perTransferLimit: string;
}

interface CacheEntry {
  value: TenantAuth;
  expiresAt: number;
}

export class TenantAuthCache {
  private local: Map<string, CacheEntry> = new Map();
  private redis: Redis | null;
  private localTtlMs: number;
  private redisTtlSeconds: number;
  private maxLocalSize = 10_000; // Cap to prevent unbounded memory growth

  constructor(
    redis: Redis | null,
    localTtlMs: number,
    redisTtlSeconds: number,
  ) {
    this.redis = redis;
    this.localTtlMs = localTtlMs;
    this.redisTtlSeconds = redisTtlSeconds;
  }

  /** Get from L1 (local). Returns undefined on miss or expiry. */
  getLocal(keyHash: string): TenantAuth | undefined {
    const entry = this.local.get(keyHash);
    if (!entry) return undefined;
    if (Date.now() > entry.expiresAt) {
      this.local.delete(keyHash);
      return undefined;
    }
    // LRU promotion: delete + re-insert moves entry to end of Map iteration order,
    // so Map.keys().next() always returns the least-recently-used key.
    this.local.delete(keyHash);
    this.local.set(keyHash, entry);
    return entry.value;
  }

  /** Get from L2 (Redis). Returns undefined on miss. */
  async getRedis(keyHash: string): Promise<TenantAuth | undefined> {
    if (!this.redis) return undefined;
    const raw = await this.redis.get(`tenant:auth:${keyHash}`);
    if (!raw) return undefined;
    try {
      const auth = JSON.parse(raw) as TenantAuth;
      // Promote to L1
      this.setLocal(keyHash, auth);
      return auth;
    } catch {
      return undefined;
    }
  }

  /** Set in both L1 and L2. */
  async set(keyHash: string, auth: TenantAuth): Promise<void> {
    this.setLocal(keyHash, auth);
    if (this.redis) {
      await this.redis.setex(
        `tenant:auth:${keyHash}`,
        this.redisTtlSeconds,
        JSON.stringify(auth),
      );
    }
  }

  /** Invalidate a key in both levels. */
  async invalidate(keyHash: string): Promise<void> {
    this.local.delete(keyHash);
    if (this.redis) {
      await this.redis.del(`tenant:auth:${keyHash}`);
    }
  }

  /**
   * Evict a key from L1 only (local map).
   * Used by the pub/sub subscriber when a peer gateway has already deleted L2.
   */
  deleteLocal(keyHash: string): void {
    this.local.delete(keyHash);
  }

  /** Current L1 cache size (for metrics). */
  get localSize(): number {
    return this.local.size;
  }

  private setLocal(keyHash: string, auth: TenantAuth): void {
    // Evict oldest entries if at capacity
    if (this.local.size >= this.maxLocalSize) {
      const firstKey = this.local.keys().next().value;
      if (firstKey) this.local.delete(firstKey);
    }
    this.local.set(keyHash, {
      value: auth,
      expiresAt: Date.now() + this.localTtlMs,
    });
  }
}
