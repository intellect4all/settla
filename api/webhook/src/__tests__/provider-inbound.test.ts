/**
 * Tests for provider inbound webhook signature verification.
 *
 * Covers:
 *   1. Valid signature -> 200 accepted
 *   2. Invalid signature -> 401 rejected
 *   3. Missing signature (secret configured) -> warning logged, 200 accepted
 *   4. No secret configured (no sig) -> warning logged, 200 accepted
 *   5. No secret configured (sig present) -> 200 accepted (no verification)
 *   6. Custom signature header override -> respected
 *   7. NATS unavailable -> 503 (unchanged behaviour)
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

/** Canonical valid payload that normalizePayload can handle */
const validBody = {
  transfer_id: "transfer-uuid-001",
  tenant_id: "tenant-uuid-001",
  status: "completed",
  type: "onramp",
  external_id: "ext-ref-001",
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

describe("provider-inbound signature verification", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockConnect.mockResolvedValue({
      jetstream: mockJetstream,
      drain: mockDrain,
    });
    mockPublish.mockResolvedValue(undefined);
  });

  describe("when a signing secret IS configured", () => {
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
      const { server, logger, teardown } = await buildServer({
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
        expect(logger.errors).toContain(
          "provider-inbound: webhook signature verification failed"
        );
      } finally {
        await teardown();
      }
    });

    it("logs a warning and proceeds (200) when signature header is absent", async () => {
      const { server, logger, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: { "content-type": "application/json" },
          payload: JSON.stringify(validBody),
        });

        // backward-compat: missing sig is not a hard rejection
        expect(response.statusCode).toBe(200);
        expect(logger.warnings).toContain(
          "provider-inbound: webhook received without signature"
        );
      } finally {
        await teardown();
      }
    });

    it("rejects a tampered body even if the signature was valid for original body", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
      });

      try {
        const originalBody = JSON.stringify(validBody);
        const sig = computeSignature(originalBody, SECRET);

        // Send a different body but the signature of the original
        const tamperedBody = JSON.stringify({ ...validBody, status: "failed" });

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

  describe("when NO signing secret is configured for the provider", () => {
    it("accepts the request and logs a warning when no signature present", async () => {
      const { server, logger, teardown } = await buildServer({
        signingSecrets: {}, // no secret for PROVIDER_SLUG
      });

      try {
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: { "content-type": "application/json" },
          payload: JSON.stringify(validBody),
        });

        expect(response.statusCode).toBe(200);
        expect(logger.warnings).toContain(
          "provider-inbound: webhook received without signature"
        );
      } finally {
        await teardown();
      }
    });

    it("accepts the request even when a signature header is present (no verification performed)", async () => {
      const { server, teardown } = await buildServer({
        signingSecrets: {},
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
      } finally {
        await teardown();
      }
    });
  });

  describe("custom signature header override", () => {
    const customHeader = "x-provider-hmac";

    it("reads the signature from the configured custom header and accepts valid request", async () => {
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
        expect(JSON.parse(response.body)).toMatchObject({ ok: true });
      } finally {
        await teardown();
      }
    });

    it("treats default header as missing when a custom header is configured", async () => {
      const { server, logger, teardown } = await buildServer({
        signingSecrets: { [PROVIDER_SLUG]: SECRET },
        signatureHeaders: { [PROVIDER_SLUG]: customHeader },
      });

      try {
        const body = JSON.stringify(validBody);
        const sig = computeSignature(body, SECRET);

        // Signature in the wrong header — treated as absent
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: {
            "content-type": "application/json",
            "x-webhook-signature": sig,
          },
          payload: body,
        });

        // Missing sig -> warning + proceed
        expect(response.statusCode).toBe(200);
        expect(logger.warnings).toContain(
          "provider-inbound: webhook received without signature"
        );
      } finally {
        await teardown();
      }
    });
  });

  describe("downstream error handling (unchanged behaviour)", () => {
    it("returns 503 when NATS is unavailable", async () => {
      mockConnect.mockRejectedValueOnce(new Error("connection refused"));

      const server = Fastify({ logger: false });
      const logger = buildLogger();

      const { disconnect } = await registerProviderInboundRoutes(
        server,
        { natsUrl: "nats://localhost:4222" },
        logger
      );
      await server.ready();

      try {
        const response = await server.inject({
          method: "POST",
          url: `/webhooks/providers/${PROVIDER_SLUG}`,
          headers: { "content-type": "application/json" },
          payload: JSON.stringify(validBody),
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

    // Save originals and set test values
    for (const k of testKeys) {
      savedEnv[k] = process.env[k];
    }
    process.env.PROVIDER_YELLOW_CARD_WEBHOOK_SECRET = "secret-yc";
    process.env.PROVIDER_KOTANI_PAY_WEBHOOK_SECRET = "secret-kp";
    process.env.PROVIDER_YELLOW_CARD_SIGNATURE_HEADER = "x-yc-signature";

    // Reset module registry so the re-imported config.js re-evaluates
    // loadConfig() against the updated process.env.
    vi.resetModules();
    const { loadConfig } = await import("../config.js");

    const cfg = loadConfig();

    expect(cfg.providerSigningSecrets["yellow-card"]).toBe("secret-yc");
    expect(cfg.providerSigningSecrets["kotani-pay"]).toBe("secret-kp");
    expect(cfg.providerSignatureHeaders["yellow-card"]).toBe("x-yc-signature");

    // Restore env
    for (const k of testKeys) {
      if (savedEnv[k] === undefined) {
        delete process.env[k];
      } else {
        process.env[k] = savedEnv[k];
      }
    }
  });
});
