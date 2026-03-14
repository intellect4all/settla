import { createHmac, timingSafeEqual } from "node:crypto";
import type { FastifyInstance } from "fastify";
import type { Redis } from "ioredis";
import { getNormalizer } from "../webhooks/normalizers/index.js";
import { config } from "../config.js";
import { webhookReceivedTotal, webhookDedupTotal, webhookRateLimitTotal } from "../metrics.js";

/** Webhook dedup TTL: 72 hours in seconds. */
const DEDUP_TTL_SECONDS = config.webhookDedupTtlSeconds;

/** NATS subject for inbound webhook events. */
const NATS_SUBJECT = "settla.inbound.webhook.received";

interface WebhookRouteOpts {
  redis: Redis | null;
  natsUrl: string;
}

/**
 * Inbound provider webhook routes.
 *
 * POST /webhooks/providers/:providerId
 *   1. Validate HMAC signature (provider-specific secret)
 *   2. Dedup by provider + external_event_id (Redis, 72h TTL)
 *   3. Normalize payload via per-provider normalizer
 *   4. Publish to NATS: settla.inbound.webhook.received
 *   5. Return 200 immediately (never block the provider)
 */
export async function webhookRoutes(
  app: FastifyInstance,
  opts: WebhookRouteOpts,
): Promise<void> {
  const { redis, natsUrl } = opts;

  // ── Per-provider sliding-window rate limiting ───────────────────────────
  const PROVIDER_RATE_LIMIT = config.webhookRateLimitPerProvider;
  const PROVIDER_WINDOW_MS = 1_000;

  interface ProviderRateEntry {
    count: number;
    windowStart: number;
  }
  const providerRateLimitMap = new Map<string, ProviderRateEntry>();

  // Lazy NATS connection — initialized on first webhook
  let natsConn: any = null;
  let natsConnecting: Promise<any> | null = null;

  async function getNatsConnection() {
    if (natsConn) return natsConn;
    if (natsConnecting) return natsConnecting;

    natsConnecting = (async () => {
      try {
        const nats = await import("nats");
        natsConn = await nats.connect({ servers: natsUrl });
        app.log.info("NATS connected for webhook publishing");
        return natsConn;
      } catch (err) {
        app.log.error({ err }, "Failed to connect to NATS for webhooks");
        natsConnecting = null;
        return null;
      }
    })();

    return natsConnecting;
  }

  app.addHook("onClose", async () => {
    if (natsConn) {
      try {
        await natsConn.drain();
      } catch {
        // best-effort cleanup
      }
    }
  });

  // Tell Fastify to give us the raw body for HMAC verification
  app.addContentTypeParser(
    "application/json",
    { parseAs: "buffer" },
    (_req, body, done) => {
      try {
        done(null, { raw: body, parsed: JSON.parse(body.toString()) });
      } catch (err) {
        done(err as Error, undefined);
      }
    },
  );

  app.post<{
    Params: { providerId: string };
    Body: { raw: Buffer; parsed: any };
  }>(
    "/webhooks/providers/:providerId",
    {
      schema: {
        params: {
          type: "object",
          properties: { providerId: { type: "string" } },
          required: ["providerId"],
        },
        response: {
          200: {
            type: "object",
            properties: {
              status: { type: "string" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { providerId } = request.params;
      webhookReceivedTotal.inc({ provider: providerId });

      // ── 0. Per-provider rate limiting ─────────────────────────────────
      const now = Date.now();
      let rlEntry = providerRateLimitMap.get(providerId);
      if (!rlEntry || now - rlEntry.windowStart >= PROVIDER_WINDOW_MS) {
        rlEntry = { count: 1, windowStart: now };
        providerRateLimitMap.set(providerId, rlEntry);
      } else {
        rlEntry.count++;
        if (rlEntry.count > PROVIDER_RATE_LIMIT) {
          webhookRateLimitTotal.inc({ provider: providerId });
          app.log.warn(
            { providerId, count: rlEntry.count, limit: PROVIDER_RATE_LIMIT },
            "Webhook provider rate limit exceeded",
          );
          return reply
            .status(429)
            .header("Retry-After", "1")
            .send({ error: "rate_limit_exceeded" });
        }
      }

      // ── 1. Validate HMAC signature ────────────────────────────────────
      const secret = config.webhookSecrets[providerId];
      if (!secret) {
        app.log.error({ providerId }, "Webhook provider not configured");
        return reply.status(403).send({ error: "provider not configured" });
      }

      const signatureHeader =
        request.headers["x-webhook-signature"] as string | undefined;
      if (!signatureHeader) {
        app.log.warn({ providerId }, "Webhook missing signature header");
        return reply.status(401).send({ error: "missing signature" });
      }

      const rawBody = request.body.raw;
      const expected = createHmac("sha256", secret)
        .update(rawBody)
        .digest("hex");

      const sigBuffer = Buffer.from(signatureHeader, "hex");
      const expectedBuffer = Buffer.from(expected, "hex");

      if (
        sigBuffer.length !== expectedBuffer.length ||
        !timingSafeEqual(sigBuffer, expectedBuffer)
      ) {
        app.log.warn({ providerId }, "Webhook HMAC signature mismatch");
        return reply.status(401).send({ error: "invalid signature" });
      }

      // ── 2. Normalize payload ──────────────────────────────────────────
      const normalizer = getNormalizer(providerId);
      if (!normalizer) {
        app.log.warn({ providerId }, "No normalizer registered for provider");
        return reply.status(400).send({ error: "unsupported_provider" });
      }

      const payload = request.body.parsed;
      const normalized = normalizer.normalize(providerId, payload);
      if (!normalized) {
        app.log.warn(
          { providerId },
          "Webhook payload could not be normalized",
        );
        return reply.status(400).send({ error: "invalid_payload" });
      }

      // ── 3. Dedup by provider + external_event_id ──────────────────────
      // SEC-6: Redis dedup is a hard gate — if Redis is unavailable we return
      // 503 rather than silently proceeding.  Silently proceeding would allow
      // duplicate webhook events to fire on any Redis outage/restart, violating
      // the exactly-once guarantee that downstream workers rely on.
      // Providers are expected to retry on 5xx; the gateway will recover once
      // Redis is available again.
      const dedupKey = `webhook:dedup:${providerId}:${normalized.externalEventId}`;
      if (redis) {
        try {
          const wasSet = await redis.set(
            dedupKey,
            "1",
            "EX",
            DEDUP_TTL_SECONDS,
            "NX",
          );
          if (!wasSet) {
            // Duplicate — already processed
            webhookDedupTotal.inc({ provider: providerId });
            app.log.info(
              { providerId, externalEventId: normalized.externalEventId },
              "Webhook deduplicated",
            );
            return reply.status(200).send({ status: "duplicate" });
          }
        } catch (err) {
          // Redis is unavailable — hard-fail so the provider retries later.
          // Silently proceeding would allow duplicate events through.
          app.log.error(
            { err, providerId, externalEventId: normalized.externalEventId },
            "Redis dedup unavailable — returning 503 to prevent duplicate event processing",
          );
          return reply.status(503).send({ error: "dedup_unavailable" });
        }
      } else {
        // Redis not configured — hard-fail for the same reason.
        // In production Redis is required; this prevents silent duplicates in
        // misconfigured deployments.
        app.log.error(
          { providerId, externalEventId: normalized.externalEventId },
          "Redis not configured — returning 503 to prevent duplicate event processing",
        );
        return reply.status(503).send({ error: "dedup_unavailable" });
      }

      // ── 4. Publish to NATS ────────────────────────────────────────────
      try {
        const nc = await getNatsConnection();
        if (nc) {
          const nats = await import("nats");
          const sc = nats.StringCodec();
          nc.publish(NATS_SUBJECT, sc.encode(JSON.stringify(normalized)));
          app.log.info(
            {
              providerId,
              externalEventId: normalized.externalEventId,
              transferRef: normalized.transferRef,
            },
            "Webhook published to NATS",
          );
        } else {
          // NATS unavailable — roll back dedup key so provider retry is accepted
          app.log.error(
            { providerId, externalEventId: normalized.externalEventId },
            "NATS unavailable, webhook event not published",
          );
          if (redis) {
            try { await redis.del(dedupKey); } catch { /* best-effort */ }
          }
          return reply.status(503).send({ error: "event_bus_unavailable" });
        }
      } catch (err) {
        // NATS publish failure — roll back dedup key so provider retry is accepted
        app.log.error(
          { err, providerId, externalEventId: normalized.externalEventId },
          "Failed to publish webhook to NATS",
        );
        if (redis) {
          try { await redis.del(dedupKey); } catch { /* best-effort */ }
        }
        return reply.status(503).send({ error: "event_bus_unavailable" });
      }

      // ── 5. Return 200 immediately ────────────────────────────────────
      return reply.status(200).send({ status: "accepted" });
    },
  );
}
