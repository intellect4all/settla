/**
 * Environment configuration for the gateway BFF.
 * Note: Rate limiting and CORS are handled by Tyk — no config needed here.
 */

function parseJsonEnv(envVar: string, defaultValue: string): Record<string, string> {
  const raw = process.env[envVar] || defaultValue;
  try {
    return JSON.parse(raw) as Record<string, string>;
  } catch {
    throw new Error(`Invalid JSON in ${envVar}: ${raw.slice(0, 100)}`);
  }
}

export const config = {
  port: Number(process.env.PORT) || 3000,
  host: process.env.HOST || "0.0.0.0",
  env: process.env.SETTLA_ENV || "development",
  logLevel: process.env.SETTLA_LOG_LEVEL || "info",

  // gRPC connection to settla-server
  grpcUrl: process.env.SETTLA_SERVER_GRPC_URL || "localhost:9090",
  grpcPoolSize: Number(process.env.SETTLA_GRPC_POOL_SIZE) || 50,
  grpcTls: process.env.SETTLA_GRPC_TLS === "true",
  grpcCaCertPath: process.env.SETTLA_GRPC_CA_CERT || "",
  grpcCertPath: process.env.SETTLA_GRPC_CERT || "",
  grpcKeyPath: process.env.SETTLA_GRPC_KEY || "",

  // Per-service gRPC deadlines (milliseconds)
  grpcDeadlineMs: {
    settlement: Number(process.env.SETTLA_GRPC_DEADLINE_SETTLEMENT_MS) || 5000,
    treasury: Number(process.env.SETTLA_GRPC_DEADLINE_TREASURY_MS) || 5000,
    auth: Number(process.env.SETTLA_GRPC_DEADLINE_AUTH_MS) || 5000,
    portal: Number(process.env.SETTLA_GRPC_DEADLINE_PORTAL_MS) || 5000,
    ledger: Number(process.env.SETTLA_GRPC_DEADLINE_LEDGER_MS) || 5000,
  },

  // Redis (L2 cache, idempotency, webhook dedup)
  // In production, Sentinel is used for HA.  When SETTLA_REDIS_SENTINEL_ADDRS is
  // set, the gateway connects via Sentinel rather than a plain Redis URL.
  redisUrl: process.env.SETTLA_REDIS_URL || "redis://localhost:6379",
  // Comma-separated sentinel endpoints, e.g.:
  //   "sentinel-0:26379,sentinel-1:26379,sentinel-2:26379"
  // Unset in development (falls back to redisUrl).
  redisSentinelAddrs: process.env.SETTLA_REDIS_SENTINEL_ADDRS || "",
  // Logical master name as configured in sentinel.conf (sentinel monitor <name>).
  redisSentinelMasterName: process.env.SETTLA_REDIS_SENTINEL_MASTER_NAME || "settla-redis",

  // Auth cache TTLs
  tenantCacheTtlMs:
    (Number(process.env.SETTLA_TENANT_CACHE_TTL_SECONDS) || 30) * 1000,
  redisCacheTtlSeconds: 300, // 5 minutes

  // Max entries in the L1 (in-process) tenant auth cache.
  // Default 500K supports up to ~1M concurrent tenants with reasonable hit rates.
  tenantCacheMaxLocal: Number(process.env.SETTLA_TENANT_CACHE_MAX_LOCAL) || 500_000,

  // NATS for publishing inbound webhook events
  natsUrl: process.env.SETTLA_NATS_URL || "nats://localhost:4222",

  // Internal Go server HTTP URL (for ops endpoints)
  serverHttpUrl: process.env.SETTLA_SERVER_HTTP_URL || "http://localhost:8080",

  // Internal settla-node HTTP URL (for DLQ ops endpoints)
  nodeHttpUrl: process.env.SETTLA_NODE_HTTP_URL || "http://localhost:9091",

  // Webhook HMAC secrets per provider (JSON map: { "provider-id": "secret" })
  // WARNING: In production, use a secret manager (Vault, AWS Secrets Manager)
  // instead of environment variables for webhook secrets.
  webhookSecrets: parseJsonEnv("SETTLA_WEBHOOK_SECRETS", "{}"),

  // Webhook dedup TTL in seconds (72 hours)
  webhookDedupTtlSeconds: Number(process.env.SETTLA_WEBHOOK_DEDUP_TTL) || 259200,

  // Per-tenant rate limit: max requests per second per tenant
  rateLimitPerTenant: Number(process.env.SETTLA_RATE_LIMIT_PER_TENANT) || 5000,

  // Per-provider webhook rate limit: max requests per second per provider
  webhookRateLimitPerProvider: Number(process.env.SETTLA_WEBHOOK_RATE_LIMIT_PER_PROVIDER) || 200,

  // Per-IP webhook rate limit: max requests per second per source IP
  webhookIpRateLimit: Number(process.env.SETTLA_WEBHOOK_IP_RATE_LIMIT) || 100,

  // Global webhook rate limit: max requests per second across all providers
  webhookGlobalRateLimit: Number(process.env.SETTLA_WEBHOOK_GLOBAL_RATE_LIMIT) || 10_000,

  // CORS origin — permissive in dev, MUST be configured in production via env var.
  // In production, if SETTLA_CORS_ORIGIN is unset, default to rejecting cross-origin (false).
  corsOrigin: process.env.SETTLA_CORS_ORIGIN
    || ((process.env.SETTLA_ENV || "development") === "production" ? false as const : "*"),
} as const;
