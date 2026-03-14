import type { FastifyInstance } from "fastify";
import {
  connect,
  type NatsConnection,
  type JetStreamClient,
  StringCodec,
} from "nats";
import type { Logger } from "./logger.js";
import { randomUUID } from "node:crypto";
import { verifySignature } from "./signature.js";
import { signatureFailuresTotal } from "./metrics.js";

const sc = StringCodec();

/**
 * Normalized provider webhook payload published to NATS for processing
 * by the InboundWebhookWorker (Go).
 */
export interface ProviderWebhookPayload {
  transfer_id: string;
  tenant_id: string;
  provider_id: string;
  provider_ref: string;
  status: string; // "completed" | "failed"
  tx_hash?: string;
  error?: string;
  error_code?: string;
  tx_type: string; // "onramp" | "offramp"
}

/**
 * Raw inbound webhook body from a provider. Each provider sends a different
 * format; the normalizer converts it to ProviderWebhookPayload.
 */
interface RawProviderWebhook {
  // Common fields most providers include
  reference?: string;
  external_id?: string;
  status?: string;
  tx_hash?: string;
  error?: string;
  error_code?: string;
  type?: string; // "onramp" | "offramp"
  transfer_id?: string;
  tenant_id?: string;
  [key: string]: unknown;
}

/** Default header used to carry the HMAC-SHA256 signature from a provider. */
const DEFAULT_SIGNATURE_HEADER = "x-webhook-signature";

interface ProviderInboundConfig {
  natsUrl: string;
  /**
   * HMAC-SHA256 signing secrets keyed by providerSlug.
   * When a secret is configured for a provider, any webhook that carries a
   * signature header will be verified.  Webhooks with an invalid signature
   * are rejected with 401.  Webhooks with no signature header log a warning
   * but proceed (backward-compatibility).
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
 * instance. This endpoint receives raw webhooks from payment providers,
 * normalizes the payload, and publishes to the SETTLA_PROVIDER_WEBHOOKS
 * NATS stream for async processing by InboundWebhookWorker.
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
  // bytes the provider sent, then also JSON-parse for normalization.
  server.addContentTypeParser(
    "application/json",
    { parseAs: "buffer" },
    (_req, body, done) => {
      try {
        done(null, { raw: body as Buffer, parsed: JSON.parse((body as Buffer).toString()) });
      } catch (err) {
        done(err as Error, undefined);
      }
    }
  );

  server.post<{
    Params: { providerSlug: string };
    Body: { raw: Buffer; parsed: RawProviderWebhook };
  }>(
    "/webhooks/providers/:providerSlug",
    async (request, reply) => {
      const { providerSlug } = request.params;

      // Determine which header carries the signature for this provider
      const sigHeaderName =
        (config.signatureHeaders ?? {})[providerSlug] ?? DEFAULT_SIGNATURE_HEADER;
      const signature = request.headers[sigHeaderName] as string | undefined;

      logger.info("provider-inbound: received webhook", {
        provider: providerSlug,
        has_signature: !!signature,
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

      const rawBody = request.body.raw.toString("utf8");
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

      const body = request.body?.parsed;
      if (!body || typeof body !== "object") {
        return reply.status(400).send({ error: "invalid request body" });
      }

      // Normalize the provider-specific payload into canonical format
      const normalized = normalizePayload(providerSlug, body);
      if (!normalized) {
        logger.warn("provider-inbound: could not normalize webhook payload", {
          provider: providerSlug,
        });
        return reply.status(400).send({ error: "could not normalize webhook payload" });
      }

      // Determine NATS subject based on tx_type
      const eventType =
        normalized.tx_type === "offramp"
          ? "provider.inbound.offramp.webhook"
          : "provider.inbound.onramp.webhook";

      const natsSubject = `settla.provider.inbound.${eventType.split("provider.inbound.")[1]}`;

      const event = {
        ID: randomUUID(),
        TenantID: normalized.tenant_id,
        Type: eventType,
        Timestamp: new Date().toISOString(),
        Data: normalized,
      };

      try {
        const data = sc.encode(JSON.stringify(event));
        await js.publish(natsSubject, data, {
          msgID: `webhook-${providerSlug}-${normalized.transfer_id}-${normalized.status}`,
        });

        logger.info("provider-inbound: published to NATS", {
          provider: providerSlug,
          subject: natsSubject,
          transfer_id: normalized.transfer_id,
          status: normalized.status,
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

/**
 * Normalizes a raw provider webhook into the canonical ProviderWebhookPayload.
 * Returns null if required fields are missing.
 */
function normalizePayload(
  providerSlug: string,
  raw: RawProviderWebhook
): ProviderWebhookPayload | null {
  const transferId = raw.transfer_id ?? raw.reference;
  const tenantId = raw.tenant_id;
  const status = raw.status;

  if (!transferId || !tenantId || !status) {
    return null;
  }

  // Map provider-reported status to our canonical statuses
  const normalizedStatus = normalizeStatus(status);
  if (!normalizedStatus) {
    return null;
  }

  return {
    transfer_id: transferId,
    tenant_id: tenantId,
    provider_id: providerSlug,
    provider_ref: raw.external_id ?? "",
    status: normalizedStatus,
    tx_hash: raw.tx_hash,
    error: raw.error,
    error_code: raw.error_code,
    tx_type: raw.type ?? "onramp",
  };
}

/**
 * Maps various provider status strings to our canonical "completed" or "failed".
 */
function normalizeStatus(status: string): string | null {
  const s = status.toLowerCase();
  switch (s) {
    case "completed":
    case "success":
    case "successful":
    case "confirmed":
      return "completed";
    case "failed":
    case "failure":
    case "error":
    case "rejected":
    case "declined":
      return "failed";
    default:
      // Unknown status — could be "pending" or provider-specific.
      // We only process terminal statuses.
      return null;
  }
}
