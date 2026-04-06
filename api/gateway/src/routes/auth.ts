import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import type { Redis } from "ioredis";
import { mapGrpcError } from "../errors.js";

/**
 * Per-IP login rate limiter using Redis sliding window with in-memory fallback.
 * - 10 attempts per minute per IP
 * - 100 attempts per hour per IP
 */
const loginRateLimitLocal = new Map<
  string,
  { count: number; windowStart: number }
>();

// Cleanup stale local rate-limit entries every 5 minutes
const loginRlCleanup = setInterval(() => {
  const cutoff = Date.now() - 5 * 60_000;
  for (const [key, entry] of loginRateLimitLocal) {
    if (entry.windowStart < cutoff) {
      loginRateLimitLocal.delete(key);
    }
  }
}, 5 * 60_000);
loginRlCleanup.unref();

async function checkLoginRateLimit(
  redis: Redis | null,
  ip: string,
): Promise<{ allowed: true } | { allowed: false; retryAfterSeconds: number }> {
  if (!redis) {
    // In-memory fallback: 10 req/60s per IP
    const now = Date.now();
    const windowMs = 60_000;
    let entry = loginRateLimitLocal.get(ip);
    if (!entry || now - entry.windowStart >= windowMs) {
      entry = { count: 1, windowStart: now };
      loginRateLimitLocal.set(ip, entry);
      return { allowed: true };
    }
    entry.count++;
    if (entry.count > 10) {
      const retryAfterSeconds = Math.max(
        1,
        Math.ceil((entry.windowStart + windowMs - now) / 1000),
      );
      return { allowed: false, retryAfterSeconds };
    }
    return { allowed: true };
  }

  const minuteKey = `login:rl:min:${ip}`;
  const hourKey = `login:rl:hr:${ip}`;

  // Check per-minute limit (10 / 60s)
  const minuteCount = await redis.incr(minuteKey);
  if (minuteCount === 1) {
    await redis.expire(minuteKey, 60);
  }
  if (minuteCount > 10) {
    const ttl = await redis.ttl(minuteKey);
    return { allowed: false, retryAfterSeconds: ttl > 0 ? ttl : 60 };
  }

  // Check per-hour limit (100 / 3600s)
  const hourCount = await redis.incr(hourKey);
  if (hourCount === 1) {
    await redis.expire(hourKey, 3600);
  }
  if (hourCount > 100) {
    const ttl = await redis.ttl(hourKey);
    return { allowed: false, retryAfterSeconds: ttl > 0 ? ttl : 3600 };
  }

  return { allowed: true };
}

export async function authRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient; redis?: Redis | null },
): Promise<void> {
  const { grpc, redis = null } = opts;

  // POST /v1/auth/register — public
  app.post<{
    Body: {
      company_name: string;
      email: string;
      password: string;
      display_name?: string;
    };
  }>(
    "/v1/auth/register",
    {
      schema: {
        tags: ["Auth"],
        summary: "Register a new tenant",
        operationId: "register",
        body: {
          type: "object",
          required: ["company_name", "email", "password"],
          properties: {
            company_name: { type: "string", minLength: 1 },
            email: { type: "string", format: "email" },
            password: { type: "string", minLength: 8 },
            display_name: { type: "string" },
          },
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.register(
          {
            companyName: request.body.company_name,
            email: request.body.email,
            password: request.body.password,
            displayName: request.body.display_name,
          },
          request.id,
          request,
        );
        return reply.status(201).send({
          tenant_id: result.tenantId,
          user_id: result.userId,
          email: result.email,
          message: result.message,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/auth/login — public
  app.post<{
    Body: {
      email: string;
      password: string;
    };
  }>(
    "/v1/auth/login",
    {
      schema: {
        tags: ["Auth"],
        summary: "Login",
        operationId: "login",
        body: {
          type: "object",
          required: ["email", "password"],
          properties: {
            email: { type: "string", format: "email" },
            password: { type: "string", minLength: 1 },
          },
        },
      },
    },
    async (request, reply) => {
      // SEC: Per-IP sliding window rate limiting on login
      const clientIp = request.ip;
      const rlResult = await checkLoginRateLimit(redis, clientIp);
      if (!rlResult.allowed) {
        return reply
          .status(429)
          .header("Retry-After", String(rlResult.retryAfterSeconds))
          .send({
            error: "TOO_MANY_REQUESTS",
            message: "Too many login attempts. Please try again later.",
          });
      }

      try {
        const result = await grpc.login(
          {
            email: request.body.email,
            password: request.body.password,
          },
          request.id,
          request,
        );
        return reply.send({
          access_token: result.accessToken,
          refresh_token: result.refreshToken,
          expires_in: Number(result.expiresIn),
          user: result.user
            ? {
                id: result.user.id,
                email: result.user.email,
                display_name: result.user.displayName,
                role: result.user.role,
                tenant_id: result.user.tenantId,
                tenant_name: result.user.tenantName,
                tenant_slug: result.user.tenantSlug,
                tenant_status: result.user.tenantStatus,
                kyb_status: result.user.kybStatus,
              }
            : undefined,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/auth/verify-email — public
  app.post<{
    Body: { token: string };
  }>(
    "/v1/auth/verify-email",
    {
      schema: {
        tags: ["Auth"],
        summary: "Verify email address",
        operationId: "verifyEmail",
        body: {
          type: "object",
          required: ["token"],
          properties: {
            token: { type: "string", minLength: 1 },
          },
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.verifyEmail(
          { token: request.body.token },
          request.id,
          request,
        );
        return reply.send({ message: result.message });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/auth/refresh — public
  app.post<{
    Body: { refresh_token: string };
  }>(
    "/v1/auth/refresh",
    {
      schema: {
        tags: ["Auth"],
        summary: "Refresh access token",
        operationId: "refreshToken",
        body: {
          type: "object",
          required: ["refresh_token"],
          properties: {
            refresh_token: { type: "string", minLength: 1 },
          },
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.refreshToken(
          { refreshToken: request.body.refresh_token },
          request.id,
          request,
        );
        return reply.send({
          access_token: result.accessToken,
          expires_in: Number(result.expiresIn),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/me/kyb — requires JWT auth
  app.post<{
    Body: {
      company_registration_number: string;
      country: string;
      business_type: string;
      contact_name: string;
      contact_email: string;
      contact_phone?: string;
    };
  }>(
    "/v1/me/kyb",
    {
      schema: {
        tags: ["Auth"],
        summary: "Submit KYB verification",
        operationId: "submitKyb",
        body: {
          type: "object",
          required: [
            "company_registration_number",
            "country",
            "business_type",
            "contact_name",
            "contact_email",
          ],
          properties: {
            company_registration_number: { type: "string", minLength: 1 },
            country: { type: "string", minLength: 2, maxLength: 2 },
            business_type: {
              type: "string",
              enum: ["fintech", "bank", "payment_processor", "other"],
            },
            contact_name: { type: "string", minLength: 1 },
            contact_email: { type: "string", format: "email" },
            contact_phone: { type: "string" },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.submitKYB(
          {
            tenantId: tenantAuth.tenantId,
            companyRegistrationNumber:
              request.body.company_registration_number,
            country: request.body.country,
            businessType: request.body.business_type,
            contactName: request.body.contact_name,
            contactEmail: request.body.contact_email,
            contactPhone: request.body.contact_phone,
          },
          request.id,
          request,
        );
        return reply.send({
          message: result.message,
          kyb_status: result.kybStatus,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/admin/tenants/:tenantId/approve-kyb — admin only
  app.post<{
    Params: { tenantId: string };
  }>(
    "/v1/admin/tenants/:tenantId/approve-kyb",
    {
      schema: {
        tags: ["Auth"],
        summary: "Approve tenant KYB",
        operationId: "approveKyb",
        params: {
          type: "object",
          properties: { tenantId: { type: "string", format: "uuid" } },
          required: ["tenantId"],
        },
      },
    },
    async (request, reply) => {
      // SEC: Require admin-level JWT — reject if not authenticated via JWT with admin role
      const { tenantAuth } = request;
      if (!tenantAuth?.userRole || tenantAuth.userRole.toLowerCase() !== "admin") {
        return reply.status(403).send({
          error: "FORBIDDEN",
          message: "Admin role required to approve KYB",
        });
      }

      try {
        const result = await grpc.approveKYB(
          { tenantId: request.params.tenantId },
          request.id,
          request,
        );
        return reply.send({
          message: result.message,
          tenant_status: result.tenantStatus,
          kyb_status: result.kybStatus,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
