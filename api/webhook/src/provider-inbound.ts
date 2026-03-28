import type { FastifyInstance } from "fastify";
import {
  connect,
  type NatsConnection,
  type JetStreamClient,
  StringCodec,
} from "nats";
import type { Logger } from "./logger.js";
import { createHash, randomUUID } from "node:crypto";
import { verifySignature } from "./signature.js";
import { signatureFailuresTotal } from "./metrics.js";

const sc = StringCodec();

/** Default header used to carry the HMAC-SHA256 signature from a provider. */
const DEFAULT_SIGNATURE_HEADER = "x-webhook-signature";

/**
 * NATS subject for raw inbound webhooks. The Go InboundWebhookWorker
 * normalizes these using the provider's registered WebhookNormalizer.
 */
const RAW_WEBHOOK_SUBJECT = "settla.provider.inbound.raw";

/**
 * NATS event type for raw inbound webhooks.
 */
const RAW_WEBHOOK_EVENT_TYPE = "provider.inbound.raw";

interface ProviderInboundConfig {
  natsUrl: string;
  /**
   * HMAC-SHA256 signing secrets keyed by providerSlug.
   * Webhooks with an invalid signature are rejected with 401.
   * Webhooks with no configured secret are rejected with 403.
   */
  signingSecrets?: Record<string, string>;
  /**
   * Optional per-provider override for the HTTP header that carries the
   * signature.  Defaults to "x-webhook-signature".
   */
  signatureHeaders?: Record<string, string>;
}

/**
 * Registers the POST /webhooks/providers/:providerSlug route on the Fastify
 * instance. This endpoint is a pure HTTP-to-NATS relay:
 *
 * 1. Verify HMAC-SHA256 signature using per-provider secret
 * 2. Forward the raw body + provider slug + metadata to NATS
 * 3. Normalization happens in Go (InboundWebhookWorker) using the provider's
 *    registered WebhookNormalizer
 */
export async function registerProviderInboundRoutes(
  server: FastifyInstance,
  config: ProviderInboundConfig,
  logger: Logger
): Promise<{ disconnect: () => Promise<void> }> {
  let nc: NatsConnection | null = null;
  let js: JetStreamClient | null = null;

  // Connect to NATS for publishing inbound webhook events
  try {
    nc = await connect({
      servers: config.natsUrl,
      name: "settla-provider-inbound",
      reconnect: true,
      maxReconnectAttempts: -1,
      reconnectTimeWait: 2000,
    });
    js = nc.jetstream();
    logger.info("provider-inbound: connected to NATS", { url: config.natsUrl });
  } catch (err) {
    logger.warn("provider-inbound: NATS connection failed, webhooks will be rejected", {
      error: err instanceof Error ? err.message : String(err),
    });
  }

  // Parse the body as a raw buffer so we can compute HMAC over the exact
  // bytes the provider sent.
  server.addContentTypeParser(
    "application/json",
    { parseAs: "buffer" },
    (_req, body, done) => {
      done(null, body as Buffer);
    }
  );

  server.post<{
    Params: { providerSlug: string };
    Body: Buffer;
  }>(
    "/webhooks/providers/:providerSlug",
    async (request, reply) => {
      const { providerSlug } = request.params;
      const rawBuffer = request.body;

      // Determine which header carries the signature for this provider
      const sigHeaderName =
        (config.signatureHeaders ?? {})[providerSlug] ?? DEFAULT_SIGNATURE_HEADER;
      const signature = request.headers[sigHeaderName] as string | undefined;

      logger.info("provider-inbound: received webhook", {
        provider: providerSlug,
        has_signature: !!signature,
        body_size: rawBuffer.length,
      });

      // ── Signature verification ────────────────────────────────────────
      const secret = (config.signingSecrets ?? {})[providerSlug];
      if (!secret) {
        logger.error("provider-inbound: no webhook secret configured for provider", { providerSlug });
        return reply.status(403).send({ error: "provider not configured" });
      }

      if (!signature) {
        logger.warn("provider-inbound: webhook received without signature", { providerSlug });
        return reply.status(401).send({ error: "missing signature" });
      }

      const rawBody = rawBuffer.toString("utf8");
      if (!verifySignature(rawBody, secret, signature)) {
        signatureFailuresTotal.inc({ provider: providerSlug });
        logger.warn("provider-inbound: invalid webhook signature", { providerSlug });
        return reply.status(401).send({ error: "invalid signature" });
      }

      logger.info("provider-inbound: webhook signature verified", {
        provider: providerSlug,
      });

      if (!js) {
        logger.error("provider-inbound: NATS not connected, rejecting webhook", {
          provider: providerSlug,
        });
        return reply.status(503).send({ error: "service unavailable" });
      }

      // Build idempotency key: provider slug + SHA-256 prefix of raw body.
      // This ensures the same payload from the same provider is deduplicated.
      const bodyHash = createHash("sha256").update(rawBuffer).digest("hex").slice(0, 32);
      const idempotencyKey = `${providerSlug}-${bodyHash}`;

      // Forward raw payload to NATS — normalization happens in Go.
      const event = {
        ID: randomUUID(),
        Type: RAW_WEBHOOK_EVENT_TYPE,
        Timestamp: new Date().toISOString(),
        Data: {
          provider_slug: providerSlug,
          raw_body: rawBuffer.toString("base64"),
          idempotency_key: idempotencyKey,
          http_headers: {
            "content-type": request.headers["content-type"] ?? "",
            "user-agent": request.headers["user-agent"] ?? "",
          },
          source_ip: request.ip,
        },
      };

      try {
        const data = sc.encode(JSON.stringify(event));
        await js.publish(RAW_WEBHOOK_SUBJECT, data, {
          msgID: idempotencyKey,
        });

        logger.info("provider-inbound: published raw webhook to NATS", {
          provider: providerSlug,
          subject: RAW_WEBHOOK_SUBJECT,
          idempotency_key: idempotencyKey,
        });

        return reply.status(200).send({ ok: true });
      } catch (err) {
        logger.error("provider-inbound: failed to publish to NATS", {
          provider: providerSlug,
          error: err instanceof Error ? err.message : String(err),
        });
        return reply.status(500).send({ error: "internal error" });
      }
    }
  );

  return {
    disconnect: async () => {
      if (nc) {
        await nc.drain();
        nc = null;
        js = null;
      }
    },
  };
}
