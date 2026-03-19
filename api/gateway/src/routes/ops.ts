import crypto from "node:crypto";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import { config } from "../config.js";

const OPS_API_KEY = process.env.SETTLA_OPS_API_KEY;

/** Upstream request timeout for all ops proxy calls. */
const OPS_PROXY_TIMEOUT_MS = 5_000;

function requireOpsAuth(request: FastifyRequest, reply: FastifyReply): boolean {
  const key = request.headers["x-ops-api-key"];
  if (!OPS_API_KEY || typeof key !== "string") {
    reply.code(403).send({ error: "forbidden" });
    return false;
  }
  const keyBuf = Buffer.from(key);
  const expectedBuf = Buffer.from(OPS_API_KEY);
  const match =
    keyBuf.length === expectedBuf.length &&
    crypto.timingSafeEqual(keyBuf, expectedBuf);
  if (!match) {
    reply.code(403).send({ error: "forbidden" });
    return false;
  }
  return true;
}

/**
 * Fetch with a hard 5-second timeout using AbortController.
 * Prevents a hanging upstream from tying up the gateway connection indefinitely.
 */
async function fetchWithTimeout(
  url: string,
  init?: RequestInit,
  timeoutMs = OPS_PROXY_TIMEOUT_MS,
): Promise<Response> {
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), timeoutMs);
  try {
    return await fetch(url, { ...init, signal: ac.signal });
  } finally {
    clearTimeout(timer);
  }
}

/** UUID format regex for validating :id params before proxying. */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Body size limit for ops proxy POST requests (1 MiB). */
const OPS_BODY_LIMIT = 1_048_576;

function isValidUUID(id: string): boolean {
  return UUID_RE.test(id);
}

/**
 * Ops routes — proxies /v1/ops/* to the Go HTTP server at /internal/ops/*.
 *
 * These endpoints are used exclusively by the Settla ops dashboard for:
 *   - Manual review management (list, approve, reject)
 *   - Reconciliation reports (latest, trigger run)
 *   - Settlement reports (view, mark-paid)
 *
 * No gRPC: pure HTTP proxy to settla-server:8080/internal/ops/.
 * All upstream fetch calls use a 5-second AbortController timeout.
 * All :id params validated as UUIDs before interpolation into proxy URL.
 */
export const opsRoutes = fp(async function opsRoutesInner(app: FastifyInstance) {
  const baseUrl = config.serverHttpUrl;

  // ── Tenants ────────────────────────────────────────────────────────────

  app.get<{ Querystring: { limit?: string; offset?: string } }>(
    "/v1/ops/tenants",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const limit = request.query.limit ?? "50";
      const offset = request.query.offset ?? "0";
      const target = `${baseUrl}/internal/ops/tenants?limit=${encodeURIComponent(limit)}&offset=${encodeURIComponent(offset)}`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET tenants");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.get<{ Params: { id: string } }>(
    "/v1/ops/tenants/:id",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      const target = `${baseUrl}/internal/ops/tenants/${encodeURIComponent(id)}`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy GET tenant");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{ Params: { id: string }; Body: { status: string } }>(
    "/v1/ops/tenants/:id/status",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      const target = `${baseUrl}/internal/ops/tenants/${encodeURIComponent(id)}/status`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST tenant status");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{ Params: { id: string }; Body: { kyb_status: string } }>(
    "/v1/ops/tenants/:id/kyb",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      const target = `${baseUrl}/internal/ops/tenants/${encodeURIComponent(id)}/kyb`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST tenant KYB");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{
    Params: { id: string };
    Body: { on_ramp_bps: number; off_ramp_bps: number; min_fee_usd: string; max_fee_usd: string };
  }>(
    "/v1/ops/tenants/:id/fees",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      const target = `${baseUrl}/internal/ops/tenants/${encodeURIComponent(id)}/fees`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST tenant fees");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{
    Params: { id: string };
    Body: { daily_limit_usd: string; per_transfer_limit: string };
  }>(
    "/v1/ops/tenants/:id/limits",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      const target = `${baseUrl}/internal/ops/tenants/${encodeURIComponent(id)}/limits`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) return reply.status(res.status).send(body);
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST tenant limits");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  // ── Manual Reviews ──────────────────────────────────────────────────────

  app.get<{ Querystring: { status?: string } }>(
    "/v1/ops/manual-reviews",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const status = request.query.status ?? "";
      const target = `${baseUrl}/internal/ops/manual-reviews?status=${encodeURIComponent(status)}`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET manual-reviews");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{ Params: { id: string }; Body: { notes?: string } }>(
    "/v1/ops/manual-reviews/:id/approve",
    { bodyLimit: OPS_BODY_LIMIT },
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      if (!isValidUUID(id)) return reply.status(400).send({ error: "Invalid ID format" });
      const target = `${baseUrl}/internal/ops/manual-reviews/${id}/approve`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST manual-reviews approve");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{ Params: { id: string }; Body: { notes?: string } }>(
    "/v1/ops/manual-reviews/:id/reject",
    { bodyLimit: OPS_BODY_LIMIT },
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      if (!isValidUUID(id)) return reply.status(400).send({ error: "Invalid ID format" });
      const target = `${baseUrl}/internal/ops/manual-reviews/${id}/reject`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST manual-reviews reject");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  // ── Reconciliation ──────────────────────────────────────────────────────

  app.get(
    "/v1/ops/reconciliation/latest",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const target = `${baseUrl}/internal/ops/reconciliation/latest`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET reconciliation/latest");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post(
    "/v1/ops/reconciliation/run",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const target = `${baseUrl}/internal/ops/reconciliation/run`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
        });
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy POST reconciliation/run");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  // ── DLQ (Dead Letter Queue) ─────────────────────────────────────────────
  // DLQ endpoints are served by settla-node (not settla-server).

  app.get<{ Querystring: { limit?: string } }>(
    "/v1/ops/dlq/stats",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const target = `${config.nodeHttpUrl}/internal/ops/dlq/stats`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET dlq/stats");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.get<{ Querystring: { limit?: string } }>(
    "/v1/ops/dlq/messages",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const limit = request.query.limit ?? "50";
      const target = `${config.nodeHttpUrl}/internal/ops/dlq/messages?limit=${encodeURIComponent(limit)}`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET dlq/messages");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{ Params: { id: string } }>(
    "/v1/ops/dlq/messages/:id/replay",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { id } = request.params;
      if (!isValidUUID(id)) return reply.status(400).send({ error: "Invalid ID format" });
      const target = `${config.nodeHttpUrl}/internal/ops/dlq/messages/${id}/replay`;
      try {
        const res = await fetchWithTimeout(target, { method: "POST" });
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, id }, "ops: failed to proxy POST dlq/messages replay");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  // ── Settlements ─────────────────────────────────────────────────────────

  app.get<{ Querystring: { period?: string } }>(
    "/v1/ops/settlements/report",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const period = request.query.period ?? "";
      const target = `${baseUrl}/internal/ops/settlements/report?period=${encodeURIComponent(period)}`;
      try {
        const res = await fetchWithTimeout(target);
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err }, "ops: failed to proxy GET settlements/report");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );

  app.post<{
    Params: { tenantId: string };
    Body: { payment_ref?: string };
  }>(
    "/v1/ops/settlements/:tenantId/mark-paid",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return reply;
      const { tenantId } = request.params;
      if (!isValidUUID(tenantId)) return reply.status(400).send({ error: "Invalid tenant ID format" });
      const target = `${baseUrl}/internal/ops/settlements/${tenantId}/mark-paid`;
      try {
        const res = await fetchWithTimeout(target, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request.body ?? {}),
        });
        const body = await res.json();
        if (!res.ok) {
          return reply.status(res.status).send(body);
        }
        return reply.send(body);
      } catch (err) {
        app.log.error({ err, tenantId }, "ops: failed to proxy POST settlements mark-paid");
        return reply.status(502).send({ error: "upstream unavailable" });
      }
    },
  );
});
