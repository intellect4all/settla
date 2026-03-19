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

  // ── Redis-distributed rate limiting ──────────────────────────────────────
  // All rate limits use Redis sliding-window counters so they work correctly
  // across 4+ gateway instances. On Redis unavailability, we degrade to allow.
  const PROVIDER_RATE_LIMIT = config.webhookRateLimitPerProvider;
  const GLOBAL_WEBHOOK_RATE_LIMIT = config.webhookGlobalRateLimit;
  const WEBHOOK_IP_RATE_LIMIT = config.webhookIpRateLimit;

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
        tags: ["Webhooks"],
        summary: "Receive provider webhook",
        operationId: "receiveProviderWebhook",
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

      // ── 0a. Global webhook rate limit (Redis) ──────────────
      // Prevents aggregate abuse via provider ID enumeration. Distributed
      // across all gateway instances via Redis sliding-window counter.
      const epochSec = Math.floor(Date.now() / 1000);
      if (redis) {
        try {
          const globalKey = `webhook:rate:global:${epochSec}`;
          const globalCount = await redis.incr(globalKey);
          if (globalCount === 1) await redis.expire(globalKey, 2);
          if (globalCount > GLOBAL_WEBHOOK_RATE_LIMIT) {
            webhookRateLimitTotal.inc({ provider: "global" });
            app.log.warn(
              { count: globalCount, limit: GLOBAL_WEBHOOK_RATE_LIMIT },
              "Webhook global rate limit exceeded",
            );
            return reply
              .status(429)
              .header("Retry-After", "1")
              .send({ error: "rate_limit_exceeded" });
          }
        } catch {
          // Redis unavailable — degrade to allow (log warning)
          app.log.warn("webhook rate limit: Redis unavailable for global check, degrading to allow");
        }
      }

      // ── 0b. Per-source-IP rate limit (Redis) ───────────────
      // Catches enumeration attacks from a single source IP.
      const sourceIp = request.ip;
      if (redis) {
        try {
          const ipKey = `webhook:rate:ip:${sourceIp}:${epochSec}`;
          const ipCount = await redis.incr(ipKey);
          if (ipCount === 1) await redis.expire(ipKey, 2);
          if (ipCount > WEBHOOK_IP_RATE_LIMIT) {
            webhookRateLimitTotal.inc({ provider: "ip:" + providerId });
            app.log.warn(
              { sourceIp, providerId, count: ipCount, limit: WEBHOOK_IP_RATE_LIMIT },
              "Webhook IP rate limit exceeded",
            );
            return reply
              .status(429)
              .header("Retry-After", "1")
              .send({ error: "rate_limit_exceeded" });
          }
        } catch {
          // Redis unavailable — degrade to allow (log warning)
          app.log.warn({ sourceIp }, "webhook rate limit: Redis unavailable for IP check, degrading to allow");
        }
      }

      // ── 0c. Per-provider rate limiting (Redis) ────────────────────────
      if (redis) {
        try {
          const providerKey = `webhook:rate:provider:${providerId}:${epochSec}`;
          const providerCount = await redis.incr(providerKey);
          if (providerCount === 1) await redis.expire(providerKey, 2);
          if (providerCount > PROVIDER_RATE_LIMIT) {
            webhookRateLimitTotal.inc({ provider: providerId });
            app.log.warn(
              { providerId, count: providerCount, limit: PROVIDER_RATE_LIMIT },
              "Webhook provider rate limit exceeded",
            );
            return reply
              .status(429)
              .header("Retry-After", "1")
              .send({ error: "rate_limit_exceeded" });
          }
        } catch {
          // Redis unavailable — degrade to allow (log warning)
          app.log.warn({ providerId }, "webhook rate limit: Redis unavailable for provider check, degrading to allow");
        }
      }

      // ── 1. Validate HMAC signature ────────────────────────────────────
      const secret = config.webhookSecrets[providerId];
      if (!secret) {
        app.log.error({ providerId, ip: request.ip }, "Webhook provider not configured");
        return reply.status(403).send({ error: "provider not configured" });
      }

      const signatureHeader =
        request.headers["x-webhook-signature"] as string | undefined;
      if (!signatureHeader) {
        app.log.warn({ providerId, ip: request.ip, signaturePresent: false }, "Webhook missing signature header");
        return reply.status(401).send({ error: "missing signature" });
      }

      // ── Timestamp replay protection ──────────────────────────
      const MAX_WEBHOOK_AGE_MS = 5 * 60 * 1000; // 5 minutes
      const timestampHeader = request.headers["x-webhook-timestamp"] as string | undefined;
      const webhookTimestamp = timestampHeader
        ? Number(timestampHeader)
        : (request.body.parsed?.timestamp as number | undefined);

      if (webhookTimestamp) {
        const ageMs = Date.now() - webhookTimestamp * 1000;
        if (ageMs > MAX_WEBHOOK_AGE_MS) {
          app.log.warn(
            { providerId, timestamp: webhookTimestamp, ageMs },
            "Webhook rejected: timestamp too old (replay protection)",
          );
          return reply.status(401).send({ error: "timestamp_expired" });
        }
        if (ageMs < -60_000) {
          // Timestamp is more than 60s in the future — likely clock skew or spoofing
          app.log.warn(
            { providerId, timestamp: webhookTimestamp, ageMs },
            "Webhook rejected: timestamp in the future",
          );
          return reply.status(401).send({ error: "invalid_timestamp" });
        }
      }

      const rawBody = request.body.raw;
      // Include timestamp in HMAC computation when available for replay protection
      const hmacPayload = webhookTimestamp
        ? Buffer.concat([rawBody, Buffer.from(`.${webhookTimestamp}`)])
        : rawBody;
      const expected = createHmac("sha256", secret)
        .update(hmacPayload)
        .digest("hex");

      const sigBuffer = Buffer.from(signatureHeader, "hex");
      const expectedBuffer = Buffer.from(expected, "hex");

      if (
        sigBuffer.length !== expectedBuffer.length ||
        !timingSafeEqual(sigBuffer, expectedBuffer)
      ) {
        app.log.warn({ providerId, ip: request.ip, signaturePresent: true }, "Webhook HMAC signature mismatch");
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
      // Downstream workers enforce idempotency via CHECK-BEFORE-CALL; duplicate delivery is safe.
      const dedupKey = `webhook:dedup:${providerId}:${normalized.externalEventId}`;
      let dedupBypassed = false;
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
          // Redis is unavailable — degrade to allow and proceed without dedup.
          // Downstream workers enforce idempotency via CHECK-BEFORE-CALL; duplicate delivery is safe.
          app.log.warn(
            { err, providerId, externalEventId: normalized.externalEventId },
            "Redis dedup unavailable — proceeding without dedup (workers enforce idempotency)",
          );
          dedupBypassed = true;
        }
      } else {
        // Redis not configured — proceed without dedup.
        // Downstream workers enforce idempotency via CHECK-BEFORE-CALL; duplicate delivery is safe.
        app.log.warn(
          { providerId, externalEventId: normalized.externalEventId },
          "Redis not configured — proceeding without dedup (workers enforce idempotency)",
        );
        dedupBypassed = true;
      }

      // ── 4. Publish to NATS ────────────────────────────────────────────
      try {
        const nc = await getNatsConnection();
        if (nc) {
          const nats = await import("nats");
          const sc = nats.StringCodec();
          const js = nc.jetstream();
          await js.publish(NATS_SUBJECT, sc.encode(JSON.stringify(normalized)), {
            msgID: normalized.externalEventId || request.id,
          });
          app.log.info(
            {
              providerId,
              externalEventId: normalized.externalEventId,
              transferRef: normalized.transferRef,
            },
            "Webhook published to NATS JetStream",
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
