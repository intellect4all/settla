import crypto from "node:crypto";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import { config } from "../config.js";

const OPS_API_KEY = process.env.SETTLA_OPS_API_KEY;

/** Upstream request timeout for all ops proxy calls (SEC-5). */
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
 * SEC-5: Fetch with a hard 5-second timeout using AbortController.
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

/**
 * Ops routes — proxies /v1/ops/* to the Go HTTP server at /internal/ops/*.
 *
 * These endpoints are used exclusively by the Settla ops dashboard for:
 *   - Manual review management (list, approve, reject)
 *   - Reconciliation reports (latest, trigger run)
 *   - Settlement reports (view, mark-paid)
 *
 * No gRPC: pure HTTP proxy to settla-server:8080/internal/ops/.
 * SEC-5: All upstream fetch calls use a 5-second AbortController timeout.
 */
export const opsRoutes = fp(async function opsRoutesInner(app: FastifyInstance) {
  const baseUrl = config.serverHttpUrl;

  // ── Manual Reviews ──────────────────────────────────────────────────────

  app.get<{ Querystring: { status?: string } }>(
    "/v1/ops/manual-reviews",
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return;
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
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return;
      const { id } = request.params;
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
    async (request, reply) => {
      if (!requireOpsAuth(request, reply)) return;
      const { id } = request.params;
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
      if (!requireOpsAuth(request, reply)) return;
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
      if (!requireOpsAuth(request, reply)) return;
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
      if (!requireOpsAuth(request, reply)) return;
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
      if (!requireOpsAuth(request, reply)) return;
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
      if (!requireOpsAuth(request, reply)) return;
      const { id } = request.params;
      const target = `${config.nodeHttpUrl}/internal/ops/dlq/messages/${encodeURIComponent(id)}/replay`;
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
      if (!requireOpsAuth(request, reply)) return;
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
      if (!requireOpsAuth(request, reply)) return;
      const { tenantId } = request.params;
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
