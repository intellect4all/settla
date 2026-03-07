import { createHash } from "node:crypto";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import type { TenantAuth, TenantAuthCache } from "./cache.js";

declare module "fastify" {
  interface FastifyRequest {
    tenantAuth: TenantAuth;
  }
}

export interface AuthPluginOpts {
  cache: TenantAuthCache;
  /** Resolve tenant from DB via gRPC. Returns null if key is invalid. */
  resolveTenant: (keyHash: string) => Promise<TenantAuth | null>;
}

/**
 * Fastify plugin that authenticates requests via Bearer token.
 * Flow: token → SHA-256 → L1 local cache → L2 Redis → L3 gRPC/DB
 */
export const authPlugin = fp(async function authPluginInner(
  app: FastifyInstance,
  opts: AuthPluginOpts,
): Promise<void> {
  const { cache, resolveTenant } = opts;

  app.decorateRequest("tenantAuth", undefined as unknown as TenantAuth);

  app.addHook(
    "onRequest",
    async (request: FastifyRequest, reply: FastifyReply) => {
      // Skip auth for health/docs endpoints
      if (
        request.url === "/health" ||
        request.url.startsWith("/docs") ||
        request.url.startsWith("/documentation")
      ) {
        return;
      }

      const authHeader = request.headers.authorization;

      if (!authHeader || !authHeader.startsWith("Bearer ") || !authHeader.startsWith("bearer ")) {
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
