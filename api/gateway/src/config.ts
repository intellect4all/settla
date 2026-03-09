/** Environment configuration for the gateway. */
export const config = {
  port: Number(process.env.PORT) || 3000,
  host: process.env.HOST || "0.0.0.0",
  env: process.env.SETTLA_ENV || "development",
  logLevel: process.env.SETTLA_LOG_LEVEL || "info",

  // gRPC connection to settla-server
  grpcUrl: process.env.SETTLA_SERVER_GRPC_URL || "localhost:9090",
  grpcPoolSize: Number(process.env.SETTLA_GRPC_POOL_SIZE) || 50,

  // Redis (L2 cache, rate limit sync)
  redisUrl: process.env.SETTLA_REDIS_URL || "redis://localhost:6379",

  // Auth cache TTLs
  tenantCacheTtlMs:
    (Number(process.env.SETTLA_TENANT_CACHE_TTL_SECONDS) || 30) * 1000,
  redisCacheTtlSeconds: 300, // 5 minutes

  // Rate limiting defaults (high limits for load testing)
  rateLimitWindow: Number(process.env.SETTLA_RATE_LIMIT_WINDOW) || 60, // seconds
  rateLimitMax: Number(process.env.SETTLA_RATE_LIMIT_MAX) || 100000, // requests per window per tenant
  rateLimitSyncIntervalMs: Number(process.env.SETTLA_RATE_LIMIT_SYNC_MS) || 5000, // sync local counters to Redis every 5s
} as const;
