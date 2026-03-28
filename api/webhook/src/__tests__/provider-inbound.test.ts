/**
 * Tests for provider inbound webhook relay (signature verification + raw forwarding).
 *
 * Covers:
 *   1. Valid signature -> 200, raw payload published to NATS
 *   2. Invalid signature -> 401 rejected
 *   3. Missing signature -> 401 rejected
 *   4. No secret configured -> 403 rejected
 *   5. Custom signature header override -> respected
 *   6. NATS unavailable -> 503
 *   7. Published NATS payload is raw (base64 body + metadata, no normalization)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import Fastify, { type FastifyInstance } from "fastify";
import { computeSignature } from "../signature.js";
import { registerProviderInboundRoutes } from "../provider-inbound.js";
import type { Logger } from "../logger.js";

// ── Mock NATS so tests do not need a running NATS server ─────────────────────

const mockPublish = vi.fn().mockResolvedValue(undefined);
const mockJetstream = vi.fn().mockReturnValue({ publish: mockPublish });
const mockDrain = vi.fn().mockResolvedValue(undefined);
const mockConnect = vi.fn().mockResolvedValue({
  jetstream: mockJetstream,
  drain: mockDrain,
});

vi.mock("nats", () => ({
  connect: (...args: unknown[]) => mockConnect(...args),
  StringCodec: () => ({
    encode: (s: string) => Buffer.from(s),
    decode: (b: Buffer) => b.toString(),
  }),
}));

// ── Helpers ──────────────────────────────────────────────────────────────────

function buildLogger(): Logger & { warnings: string[]; errors: string[] } {
  const warnings: string[] = [];
  const errors: string[] = [];
  return {
    warnings,
    errors,
    info: vi.fn(),
    warn: (msg: string) => {
      warnings.push(msg);
    },
    error: (msg: string) => {
      errors.push(msg);
    },
    deliveryAttempt: vi.fn(),
    deadLetter: vi.fn(),
  };
}

const PROVIDER_SLUG = "test-provider";
const SECRET = "whsec_test_signing_secret";

/** Sample webhook body — format doesn't matter since TS no longer normalizes. */
const validBody = {
  event: "charge.success",
  data: { reference: "ref-001", amount: 5000 },
};

async function buildServer(opts: {
  signingSecrets?: Record<string, string>;
  signatureHeaders?: Record<string, string>;
}): Promise<{
  server: FastifyInstance;
  logger: ReturnType<typeof buildLogger>;
  teardown: () => Promise<void>;
}> {
  const server = Fastify({ logger: false });
  const logger = buildLogger();

  const { disconnect } = await registerProviderInboundRoutes(
    server,
    {
      natsUrl: "nats://localhost:4222",
      signingSecrets: opts.signingSecrets,
      signatureHeaders: opts.signatureHeaders,
    },
    logger
  );

  await server.ready();

  return {
    server,
    logger,
    teardown: async () => {
      await disconnect();
      await server.close();
    },
  };
}

// ── Tests ────────────────────────────────────────────────────────────────────

describe("provider-inbound webhook relay", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockConnect.mockResolvedValue({
      jetstream: mockJetstream,
      drain: mockDrain,
    });
    mockPublish.mockResolvedValue(undefined);
  });

  describe("signature verification", () => {
    it("accepts a request with a valid signature and returns 200", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const body = JSON.stringify(validBody);
        const sig = computeSignature(body, SECRET);

        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": sig,
          },
          payload: body,
        });

        expect(response.statusCode).toBe(200);
        expect(JSON.parse(response.body)).toMatchObject({ ok: true });
      } finally {
        await teardown();
      }
    });

    it("rejects a request with an invalid signature with 401", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const body = JSON.stringify(validBody);
        const wrongSig = computeSignature(body, "wrong-secret");

        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": wrongSig,
          },
          payload: body,
        });

        expect(response.statusCode).toBe(401);
        expect(JSON.parse(response.body)).toMatchObject({
          error: "invalid signature",
        });
      } finally {
        await teardown();
      }
    });

    it("rejects with 401 when signature header is absent", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: { "content-type": "application/json" },
          payload: JSON.stringify(validBody),
        });

        expect(response.statusCode).toBe(401);
        expect(JSON.parse(response.body)).toMatchObject({
          error: "missing signature",
        });
      } finally {
        await teardown();
      }
    });

    it("rejects with 403 when no secret is configured for provider", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: {},
      });

      try {
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: { "content-type": "application/json" },
          payload: JSON.stringify(validBody),
        });

        expect(response.statusCode).toBe(403);
      } finally {
        await teardown();
      }
    });

    it("rejects a tampered body even if signature was valid for original", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const originalBody = JSON.stringify(validBody);
        const sig = computeSignature(originalBody, SECRET);
        const tamperedBody = JSON.stringify({ ...validBody, event: "charge.failed" });

        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": sig,
          },
          payload: tamperedBody,
        });

        expect(response.statusCode).toBe(401);
      } finally {
        await teardown();
      }
    });
  });

  describe("custom signature header", () => {
    const customHeader = "x-provider-hmac";

    it("reads signature from configured custom header", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
        signatureHeaders: { [PROVIDER_SLUG]: customHeader },
      });

      try {
        const body = JSON.stringify(validBody);
        const sig = computeSignature(body, SECRET);

        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            [customHeader]: sig,
          },
          payload: body,
        });

        expect(response.statusCode).toBe(200);
      } finally {
        await teardown();
      }
    });
  });

  describe("raw payload forwarding to NATS", () => {
    it("publishes raw body as base64 with provider slug and metadata", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const body = JSON.stringify(validBody);
        const sig = computeSignature(body, SECRET);

        await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": sig,
          },
          payload: body,
        });

        expect(mockPublish).toHaveBeenCalledTimes(1);

        // Parse the published NATS message
        const [subject, data, opts] = mockPublish.mock.calls[0];
        expect(subject).toBe("settla.provider.inbound.raw");

        const event = JSON.parse(data.toString());
        expect(event.Type).toBe("provider.inbound.raw");
        expect(event.Data.provider_slug).toBe(PROVIDER_SLUG);
        expect(event.Data.idempotency_key).toMatch(new RegExp(`^${PROVIDER_SLUG}-[a-f0-9]{32}$`));
        expect(event.Data.source_ip).toBeDefined();

        // raw_body is base64 encoded
        const decodedBody = Buffer.from(event.Data.raw_body, "base64").toString();
        expect(JSON.parse(decodedBody)).toEqual(validBody);

        // NATS msgID matches idempotency key
        expect(opts.msgID).toBe(event.Data.idempotency_key);
      } finally {
        await teardown();
      }
    });
  });

  describe("NATS unavailable", () => {
    it("returns 503 when NATS is unavailable", async () => {
      mockConnect.mockRejectedValueOnce(new Error("connection refused"));

      const server = Fastify({ logger: false });
      const logger = buildLogger();

      const { disconnect } = await registerProviderInboundRoutes(
        server,
        {
          natsUrl: "nats://localhost:4222",
          signingSecrets: { [PROVIDER_SLUG]: SECRET },
        },
        logger
      );
      await server.ready();

      try {
        const body = JSON.stringify(validBody);
        const sig = computeSignature(body, SECRET);

        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": sig,
          },
          payload: body,
        });

        expect(response.statusCode).toBe(503);
      } finally {
        await disconnect();
        await server.close();
      }
    });
  });
});

// ── Config loader: provider secret env var parsing ────────────────────────────

describe("loadConfig providerSigningSecrets", () => {
  it("parses PROVIDER_{SLUG}_WEBHOOK_SECRET env vars into hyphen-slug-keyed map", async () => {
    const savedEnv: Record<string, string | undefined> = {};
    const testKeys = [
      "PROVIDER_YELLOW_CARD_WEBHOOK_SECRET",
      "PROVIDER_KOTANI_PAY_WEBHOOK_SECRET",
      "PROVIDER_YELLOW_CARD_SIGNATURE_HEADER",
    ];

    for (const k of testKeys) {
      savedEnv[k] = process.env[k];
    }
    process.env.PROVIDER_YELLOW_CARD_WEBHOOK_SECRET = "secret-yc";
    process.env.PROVIDER_KOTANI_PAY_WEBHOOK_SECRET = "secret-kp";
    process.env.PROVIDER_YELLOW_CARD_SIGNATURE_HEADER = "x-yc-signature";

    vi.resetModules();
    const { loadConfig } = await import("../config.js");

    const cfg = loadConfig();

    expect(cfg.providerSigningSecrets["yellow-card"]).toBe("secret-yc");
    expect(cfg.providerSigningSecrets["kotani-pay"]).toBe("secret-kp");
    expect(cfg.providerSignatureHeaders["yellow-card"]).toBe("x-yc-signature");

    for (const k of testKeys) {
      if (savedEnv[k] === undefined) {
        delete process.env[k];
      } else {
        process.env[k] = savedEnv[k];
      }
    }
  });
});
