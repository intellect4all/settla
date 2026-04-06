import { createHmac, timingSafeEqual } from "node:crypto";
import type { FastifyInstance } from "fastify";
import type { Redis } from "ioredis";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { config } from "../config.js";

/** Webhook dedup TTL: 72 hours in seconds. */
const DEDUP_TTL_SECONDS = config.webhookDedupTtlSeconds;

/** NATS subject for inbound bank credit events. */
const NATS_SUBJECT = "settla.inbound.bank.credit.received";

interface BankWebhookRouteOpts {
  redis: Redis | null;
  natsUrl: string;
  grpc: SettlaGrpcClient | null;
}

/**
 * Inbound bank webhook routes.
 *
 * POST /webhooks/bank/:partner_id
 *   1. Validate HMAC signature (partner-specific secret)
 *   2. Dedup by partner + bank_reference (Redis, 72h TTL)
 *   3. Normalize payload to IncomingBankCredit
 *   4. Publish to NATS: settla.inbound.bank.credit.received
 *   5. Return 200 immediately (never block the banking partner)
 */
export async function bankWebhookRoutes(
  app: FastifyInstance,
  opts: BankWebhookRouteOpts,
): Promise<void> {
  const { redis, natsUrl, grpc } = opts;

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
        app.log.info("NATS connected for bank webhook publishing");
        return natsConn;
      } catch (err) {
        app.log.error({ err }, "Failed to connect to NATS for bank webhooks");
        natsConnecting = null;
        return null;
      }
    })();

    return natsConnecting;
  }

  async function getPartnerSecret(partnerId: string, request?: any): Promise<string | null> {
    // 1. Redis cache
    if (redis) {
      try {
        const cached = await redis.get(`banking-partner:secret:${partnerId}`);
        if (cached) return cached;
      } catch { /* fall through */ }
    }

    // 2. gRPC lookup
    if (grpc) {
      try {
        const partner = await grpc.getBankingPartner({ partnerId }, undefined, request);
        if (partner?.webhookSecret) {
          if (redis) {
            try { await redis.set(`banking-partner:secret:${partnerId}`, partner.webhookSecret, "EX", 300); } catch { /* best-effort */ }
          }
          return partner.webhookSecret;
        }
      } catch { /* fall through */ }
    }

    // 3. Static fallback
    return config.webhookSecrets[partnerId] || null;
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

  // Tell Fastify to give us the raw body bytes for HMAC verification.
  // Re-serializing via JSON.stringify can change key order / whitespace,
  // causing HMAC mismatches with the sender's signature.
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
    Params: { partner_id: string };
    Body: { raw: Buffer; parsed: any };
  }>(
    "/webhooks/bank/:partner_id",
    {
      bodyLimit: 100 * 1024,
      schema: {
        params: {
          type: "object",
          properties: { partner_id: { type: "string" } },
          required: ["partner_id"],
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
      const { partner_id: partnerId } = request.params;

      const secret = await getPartnerSecret(partnerId, request);
      if (!secret) {
        app.log.error({ partnerId }, "Bank webhook partner not configured");
        return reply.status(403).send({ error: "partner not configured" });
      }

      const signatureHeader =
        request.headers["x-webhook-signature"] as string | undefined;
      if (!signatureHeader) {
        app.log.warn({ partnerId }, "Bank webhook missing signature header");
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
        app.log.warn({ partnerId }, "Bank webhook HMAC signature mismatch");
        return reply.status(401).send({ error: "invalid signature" });
      }

      const body = request.body.parsed as Record<string, any>;
      const bankReference = body.bank_reference || body.reference || body.id;
      if (!bankReference) {
        return reply.status(400).send({ error: "missing bank_reference" });
      }

      const normalized = {
        partnerId,
        accountNumber: body.account_number || body.destination_account,
        amount: body.amount,
        currency: body.currency,
        payerName: body.payer_name || body.sender_name || "",
        payerAccountNumber: body.payer_account_number || body.sender_account || "",
        payerReference: body.payer_reference || body.reference || "",
        bankReference,
        receivedAt: body.received_at || body.timestamp || new Date().toISOString(),
      };

      const dedupKey = `bank-webhook:dedup:${partnerId}:${bankReference}`;
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
            app.log.info(
              { partnerId, bankReference },
              "Bank webhook deduplicated",
            );
            return reply.status(200).send({ status: "duplicate" });
          }
        } catch (err) {
          app.log.error(
            { err, partnerId, bankReference },
            "Redis dedup unavailable — returning 503",
          );
          return reply.status(503).send({ error: "dedup_unavailable" });
        }
      } else {
        app.log.error(
          { partnerId, bankReference },
          "Redis not configured — returning 503",
        );
        return reply.status(503).send({ error: "dedup_unavailable" });
      }

      try {
        const nc = await getNatsConnection();
        if (nc) {
          const nats = await import("nats");
          const sc = nats.StringCodec();
          nc.publish(NATS_SUBJECT, sc.encode(JSON.stringify(normalized)));
          app.log.info(
            { partnerId, bankReference, accountNumber: normalized.accountNumber },
            "Bank credit webhook published to NATS",
          );
        } else {
          app.log.error(
            { partnerId, bankReference },
            "NATS unavailable, bank credit event not published",
          );
          if (redis) {
            try { await redis.del(dedupKey); } catch { /* best-effort */ }
          }
          return reply.status(503).send({ error: "event_bus_unavailable" });
        }
      } catch (err) {
        app.log.error(
          { err, partnerId, bankReference },
          "Failed to publish bank credit to NATS",
        );
        if (redis) {
          try { await redis.del(dedupKey); } catch { /* best-effort */ }
        }
        return reply.status(503).send({ error: "event_bus_unavailable" });
      }

      return reply.status(200).send({ status: "accepted" });
    },
  );
}
