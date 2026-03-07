import { describe, it, expect, vi, beforeEach, afterEach, afterAll } from "vitest";
import { createServer, type Server, type IncomingMessage, type ServerResponse } from "node:http";
import { shouldRetry, getRetryDelay, buildWebhookEvent, deliverWebhook } from "../delivery.js";
import { computeSignature } from "../signature.js";
import type { DeliveryResult, DeliveryTask, WebhookRegistration } from "../types.js";
import type { Config } from "../config.js";

// --- Unit tests for retry logic ---

describe("shouldRetry", () => {
  const maxRetries = 5;

  it("does not retry successful deliveries", () => {
    expect(shouldRetry({ success: true, attempt: 1 } as DeliveryResult, maxRetries)).toBe(false);
  });

  it("does not retry when max retries reached", () => {
    expect(
      shouldRetry({ success: false, attempt: 5, statusCode: 500 } as DeliveryResult, maxRetries)
    ).toBe(false);
  });

  it("does not retry 4xx client errors", () => {
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: 400 } as DeliveryResult, maxRetries)
    ).toBe(false);
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: 404 } as DeliveryResult, maxRetries)
    ).toBe(false);
  });

  it("retries 429 rate-limited responses", () => {
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: 429 } as DeliveryResult, maxRetries)
    ).toBe(true);
  });

  it("retries 5xx server errors", () => {
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: 500 } as DeliveryResult, maxRetries)
    ).toBe(true);
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: 503 } as DeliveryResult, maxRetries)
    ).toBe(true);
  });

  it("retries timeouts (null status code)", () => {
    expect(
      shouldRetry({ success: false, attempt: 1, statusCode: null } as DeliveryResult, maxRetries)
    ).toBe(true);
  });
});

describe("getRetryDelay", () => {
  const delays = [0, 30_000, 120_000, 900_000, 3_600_000];

  it("returns correct delay for each attempt", () => {
    expect(getRetryDelay({ attempt: 1 } as DeliveryResult, delays)).toBe(30_000);
    expect(getRetryDelay({ attempt: 2 } as DeliveryResult, delays)).toBe(120_000);
    expect(getRetryDelay({ attempt: 3 } as DeliveryResult, delays)).toBe(900_000);
    expect(getRetryDelay({ attempt: 4 } as DeliveryResult, delays)).toBe(3_600_000);
  });

  it("clamps to last delay when attempt exceeds array length", () => {
    expect(getRetryDelay({ attempt: 10 } as DeliveryResult, delays)).toBe(3_600_000);
  });

  it("uses Retry-After when present", () => {
    expect(
      getRetryDelay({ attempt: 1, retryAfterMs: 60_000 } as DeliveryResult, delays)
    ).toBe(60_000);
  });
});

describe("buildWebhookEvent", () => {
  it("builds correct event payload", () => {
    const event = buildWebhookEvent(
      "550e8400-e29b-41d4-a716-446655440000",
      "transfer.completed",
      "2025-02-06T17:15:32Z",
      { status: "COMPLETED" }
    );
    expect(event.event).toBe("transfer.completed");
    expect(event.event_id).toBe("evt_550e8400e29b");
    expect(event.timestamp).toBe("2025-02-06T17:15:32Z");
    expect(event.data).toEqual({ status: "COMPLETED" });
  });
});

// --- Integration tests with real HTTP server ---

describe("deliverWebhook", () => {
  let testServer: Server;
  let serverPort: number;
  let lastRequest: { body: string; headers: Record<string, string | string[] | undefined> } | null;

  const mockLogger = {
    info: vi.fn(),
    error: vi.fn(),
    warn: vi.fn(),
    deliveryAttempt: vi.fn(),
    deadLetter: vi.fn(),
  };

  function makeConfig(overrides: Partial<Config> = {}): Config {
    return {
      port: 3001,
      host: "0.0.0.0",
      natsUrl: "nats://localhost:4222",
      streamName: "SETTLA_TRANSFERS",
      subjectPrefix: "settla.transfer",
      numPartitions: 8,
      workerPoolSize: 20,
      deliveryTimeoutMs: 5000,
      maxRetries: 5,
      retryDelaysMs: [0, 30_000, 120_000, 900_000, 3_600_000],
      ...overrides,
    };
  }

  function makeRegistration(overrides: Partial<WebhookRegistration> = {}): WebhookRegistration {
    return {
      id: "wh_test",
      tenantId: "tenant_a",
      url: `http://localhost:${serverPort}/webhook`,
      secret: "test_secret",
      events: [],
      isActive: true,
      ...overrides,
    };
  }

  let responseHandler: (req: IncomingMessage, res: ServerResponse) => void;

  beforeEach(async () => {
    lastRequest = null;
    responseHandler = (_req, res) => {
      res.writeHead(200);
      res.end("OK");
    };

    testServer = createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => (body += chunk));
      req.on("end", () => {
        lastRequest = { body, headers: req.headers };
        responseHandler(req, res);
      });
    });

    await new Promise<void>((resolve) => {
      testServer.listen(0, () => {
        const addr = testServer.address();
        serverPort = typeof addr === "object" && addr ? addr.port : 0;
        resolve();
      });
    });
  });

  afterEach(async () => {
    await new Promise<void>((resolve) => testServer.close(() => resolve()));
    vi.clearAllMocks();
  });

  it("delivers webhook with correct HMAC signature", async () => {
    const reg = makeRegistration();
    const event = buildWebhookEvent("id-1", "transfer.completed", "2025-01-01T00:00:00Z", {
      transfer_id: "t_1",
    });
    const task: DeliveryTask = { registration: reg, event, attempt: 1 };

    const result = await deliverWebhook(task, makeConfig(), mockLogger);

    expect(result.success).toBe(true);
    expect(result.statusCode).toBe(200);
    expect(lastRequest).not.toBeNull();

    // Verify signature
    const expectedSig = computeSignature(lastRequest!.body, reg.secret);
    expect(lastRequest!.headers["x-webhook-signature"]).toBe(expectedSig);
    expect(lastRequest!.headers["x-webhook-id"]).toBe(event.event_id);
    expect(lastRequest!.headers["x-webhook-timestamp"]).toBe(event.timestamp);
  });

  it("returns failure on 5xx response", async () => {
    responseHandler = (_req, res) => {
      res.writeHead(500);
      res.end("Internal Server Error");
    };

    const task: DeliveryTask = {
      registration: makeRegistration(),
      event: buildWebhookEvent("id-2", "transfer.failed", "2025-01-01T00:00:00Z", {}),
      attempt: 1,
    };

    const result = await deliverWebhook(task, makeConfig(), mockLogger);
    expect(result.success).toBe(false);
    expect(result.statusCode).toBe(500);
  });

  it("returns failure on 4xx response", async () => {
    responseHandler = (_req, res) => {
      res.writeHead(404);
      res.end("Not Found");
    };

    const task: DeliveryTask = {
      registration: makeRegistration(),
      event: buildWebhookEvent("id-3", "transfer.completed", "2025-01-01T00:00:00Z", {}),
      attempt: 1,
    };

    const result = await deliverWebhook(task, makeConfig(), mockLogger);
    expect(result.success).toBe(false);
    expect(result.statusCode).toBe(404);
  });

  it("handles 429 with Retry-After header", async () => {
    responseHandler = (_req, res) => {
      res.writeHead(429, { "Retry-After": "60" });
      res.end("Too Many Requests");
    };

    const task: DeliveryTask = {
      registration: makeRegistration(),
      event: buildWebhookEvent("id-4", "transfer.completed", "2025-01-01T00:00:00Z", {}),
      attempt: 1,
    };

    const result = await deliverWebhook(task, makeConfig(), mockLogger);
    expect(result.success).toBe(false);
    expect(result.statusCode).toBe(429);
    expect(result.retryAfterMs).toBe(60_000);
  });

  it("handles timeout", async () => {
    responseHandler = (_req, _res) => {
      // Never respond — let the timeout fire
    };

    const task: DeliveryTask = {
      registration: makeRegistration(),
      event: buildWebhookEvent("id-5", "transfer.completed", "2025-01-01T00:00:00Z", {}),
      attempt: 1,
    };

    const result = await deliverWebhook(task, makeConfig({ deliveryTimeoutMs: 200 }), mockLogger);
    expect(result.success).toBe(false);
    expect(result.statusCode).toBeNull();
    expect(result.error).toBe("timeout");
  });

  it("logs every delivery attempt", async () => {
    const task: DeliveryTask = {
      registration: makeRegistration(),
      event: buildWebhookEvent("id-6", "transfer.completed", "2025-01-01T00:00:00Z", {}),
      attempt: 1,
    };

    await deliverWebhook(task, makeConfig(), mockLogger);
    expect(mockLogger.deliveryAttempt).toHaveBeenCalledTimes(1);
    expect(mockLogger.deliveryAttempt).toHaveBeenCalledWith(
      expect.objectContaining({ eventId: task.event.event_id, webhookId: "wh_test", attempt: 1 })
    );
  });
});
