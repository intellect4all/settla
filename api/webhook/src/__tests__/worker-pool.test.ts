import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createServer, type Server, type IncomingMessage, type ServerResponse } from "node:http";
import { WorkerPool } from "../worker-pool.js";
import { buildWebhookEvent } from "../delivery.js";
import type { Config } from "../config.js";
import type { WebhookRegistration, DeadLetterEntry } from "../types.js";

describe("WorkerPool", () => {
  let testServer: Server;
  let serverPort: number;
  let responseHandler: (req: IncomingMessage, res: ServerResponse) => void;
  let requestCount: number;

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
      workerPoolSize: 5,
      deliveryTimeoutMs: 2000,
      maxRetries: 3,
      retryDelaysMs: [0, 0, 0], // No delays for testing
      providerSigningSecrets: {},
      providerSignatureHeaders: {},
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

  beforeEach(async () => {
    requestCount = 0;
    responseHandler = (_req, res) => {
      requestCount++;
      res.writeHead(200);
      res.end("OK");
    };

    testServer = createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => (body += chunk));
      req.on("end", () => {
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

  it("delivers a single webhook successfully", async () => {
    const config = makeConfig();
    const pool = new WorkerPool(config, mockLogger);
    const event = buildWebhookEvent("id-1", "transfer.completed", "2025-01-01T00:00:00Z", {});

    pool.enqueue({ registration: makeRegistration(), event, attempt: 1 });

    // Wait for delivery
    await new Promise((r) => setTimeout(r, 200));
    await pool.shutdown();

    expect(requestCount).toBe(1);
    const stats = pool.getStats();
    expect(stats.delivered).toBe(1);
    expect(stats.failed).toBe(0);
  });

  it("retries on 5xx and then succeeds", async () => {
    let callNum = 0;
    responseHandler = (_req, res) => {
      callNum++;
      requestCount++;
      if (callNum <= 2) {
        res.writeHead(500);
        res.end("error");
      } else {
        res.writeHead(200);
        res.end("OK");
      }
    };

    const config = makeConfig();
    const pool = new WorkerPool(config, mockLogger);
    const event = buildWebhookEvent("id-2", "transfer.failed", "2025-01-01T00:00:00Z", {});

    pool.enqueue({ registration: makeRegistration(), event, attempt: 1 });

    await new Promise((r) => setTimeout(r, 500));
    await pool.shutdown();

    expect(requestCount).toBe(3); // 2 failures + 1 success
    const stats = pool.getStats();
    expect(stats.delivered).toBe(1);
    expect(stats.retried).toBe(2);
  });

  it("does not retry 4xx client errors", async () => {
    responseHandler = (_req, res) => {
      requestCount++;
      res.writeHead(400);
      res.end("Bad Request");
    };

    const config = makeConfig();
    const pool = new WorkerPool(config, mockLogger);
    const event = buildWebhookEvent("id-3", "transfer.completed", "2025-01-01T00:00:00Z", {});

    pool.enqueue({ registration: makeRegistration(), event, attempt: 1 });

    await new Promise((r) => setTimeout(r, 200));
    await pool.shutdown();

    expect(requestCount).toBe(1);
    const stats = pool.getStats();
    expect(stats.failed).toBe(1);
    expect(stats.retried).toBe(0);
  });

  it("dead-letters after max retries exceeded", async () => {
    responseHandler = (_req, res) => {
      requestCount++;
      res.writeHead(500);
      res.end("error");
    };

    const deadLettered: DeadLetterEntry[] = [];
    const config = makeConfig({ maxRetries: 3 });
    const pool = new WorkerPool(config, mockLogger, (entry) => deadLettered.push(entry));
    const event = buildWebhookEvent("id-4", "transfer.completed", "2025-01-01T00:00:00Z", {});

    pool.enqueue({ registration: makeRegistration(), event, attempt: 1 });

    await new Promise((r) => setTimeout(r, 500));
    await pool.shutdown();

    expect(requestCount).toBe(3); // attempts 1, 2, 3
    expect(deadLettered).toHaveLength(1);
    expect(deadLettered[0].eventId).toBe(event.event_id);
    expect(deadLettered[0].lastAttempt).toBe(3);
    const stats = pool.getStats();
    expect(stats.deadLettered).toBe(1);
  });

  it("delivers to multiple webhooks for the same event", async () => {
    const config = makeConfig();
    const pool = new WorkerPool(config, mockLogger);
    const event = buildWebhookEvent("id-5", "transfer.completed", "2025-01-01T00:00:00Z", {});

    pool.enqueue({ registration: makeRegistration({ id: "wh_1" }), event, attempt: 1 });
    pool.enqueue({ registration: makeRegistration({ id: "wh_2" }), event, attempt: 1 });
    pool.enqueue({ registration: makeRegistration({ id: "wh_3" }), event, attempt: 1 });

    await new Promise((r) => setTimeout(r, 300));
    await pool.shutdown();

    expect(requestCount).toBe(3);
    expect(pool.getStats().delivered).toBe(3);
  });

  it("respects worker pool concurrency limit", async () => {
    let concurrent = 0;
    let maxConcurrent = 0;

    responseHandler = (_req, res) => {
      concurrent++;
      maxConcurrent = Math.max(maxConcurrent, concurrent);
      requestCount++;
      setTimeout(() => {
        concurrent--;
        res.writeHead(200);
        res.end("OK");
      }, 50);
    };

    const config = makeConfig({ workerPoolSize: 3 });
    const pool = new WorkerPool(config, mockLogger);
    const event = buildWebhookEvent("id-6", "transfer.completed", "2025-01-01T00:00:00Z", {});

    // Enqueue more tasks than pool size
    for (let i = 0; i < 10; i++) {
      pool.enqueue({ registration: makeRegistration({ id: `wh_${i}` }), event, attempt: 1 });
    }

    await new Promise((r) => setTimeout(r, 1000));
    await pool.shutdown();

    expect(requestCount).toBe(10);
    expect(maxConcurrent).toBeLessThanOrEqual(3);
  });
});
