import Fastify from "fastify";
import { loadConfig } from "./config.js";
import { createLogger } from "./logger.js";
import { WebhookRegistry } from "./registry.js";
import { WorkerPool } from "./worker-pool.js";
import { WebhookConsumer } from "./consumer.js";
import type { DeadLetterEntry, WebhookRegistration } from "./types.js";
import { getMetrics, metricsContentType } from "./metrics.js";

const config = loadConfig();
const logger = createLogger("settla-webhook");

// --- Dead letter store (in-memory, future: database) ---
const deadLetters: DeadLetterEntry[] = [];

// --- Webhook registry with seed data ---
const registry = new WebhookRegistry();

function seedDefaultWebhooks(): void {
  // Seed test webhooks for the two dev tenants (from db/seed)
  // Lemfi: tenant_id = a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01
  // Fincra: tenant_id = b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a02
  const seedRegistrations: WebhookRegistration[] = [
    {
      id: "wh_lemfi_default",
      tenantId: "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a01",
      url: process.env.LEMFI_WEBHOOK_URL || "http://localhost:9999/webhook/lemfi",
      secret: process.env.LEMFI_WEBHOOK_SECRET || "whsec_lemfi_test_secret_key_001",
      events: [], // all events
      isActive: true,
    },
    {
      id: "wh_fincra_default",
      tenantId: "b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a02",
      url: process.env.FINCRA_WEBHOOK_URL || "http://localhost:9999/webhook/fincra",
      secret: process.env.FINCRA_WEBHOOK_SECRET || "whsec_fincra_test_secret_key_002",
      events: [], // all events
      isActive: true,
    },
  ];

  for (const reg of seedRegistrations) {
    registry.register(reg);
    logger.info("seeded webhook registration", {
      webhook_id: reg.id,
      tenant_id: reg.tenantId,
      url: reg.url,
    });
  }
}

// --- Worker pool ---
const pool = new WorkerPool(config, logger, (entry) => {
  deadLetters.push(entry);
});

// --- NATS consumer ---
const consumer = new WebhookConsumer(config, registry, pool, logger);

// --- Fastify HTTP server (health + internal admin API) ---
const server = Fastify({ logger: false });

server.get("/metrics", async (_request, reply) => {
  const metrics = await getMetrics();
  reply.header("Content-Type", metricsContentType);
  return metrics;
});

server.get("/health", async () => {
  const stats = pool.getStats();
  return {
    status: "ok",
    service: "settla-webhook-dispatcher",
    stats,
    registrations: registry.getAll().length,
    deadLetters: deadLetters.length,
  };
});

// --- Internal webhook management API ---

server.get<{ Querystring: { tenant_id?: string } }>(
  "/internal/webhooks",
  async (request) => {
    const tenantId = request.query.tenant_id;
    const all = registry.getAll();
    if (tenantId) {
      return all.filter((r) => r.tenantId === tenantId);
    }
    return all;
  }
);

server.post<{ Body: WebhookRegistration }>(
  "/internal/webhooks",
  async (request, reply) => {
    const reg = request.body;
    if (!reg.id || !reg.tenantId || !reg.url || !reg.secret) {
      return reply.status(400).send({ error: "missing required fields: id, tenantId, url, secret" });
    }
    registry.register({
      ...reg,
      events: reg.events || [],
      isActive: reg.isActive ?? true,
    });
    logger.info("webhook registered", {
      webhook_id: reg.id,
      tenant_id: reg.tenantId,
    });
    return reply.status(201).send(reg);
  }
);

server.delete<{ Params: { id: string } }>(
  "/internal/webhooks/:id",
  async (request, reply) => {
    const removed = registry.unregister(request.params.id);
    if (!removed) {
      return reply.status(404).send({ error: "webhook not found" });
    }
    logger.info("webhook unregistered", { webhook_id: request.params.id });
    return { ok: true };
  }
);

server.get("/internal/dead-letters", async () => {
  return deadLetters;
});

server.get("/internal/stats", async () => {
  return pool.getStats();
});

// --- Startup ---

async function start(): Promise<void> {
  seedDefaultWebhooks();

  // Start HTTP server
  await server.listen({ port: config.port, host: config.host });
  logger.info("webhook dispatcher HTTP server started", {
    port: config.port,
    host: config.host,
  });

  // Connect to NATS and subscribe
  try {
    await consumer.connect();
    await consumer.subscribe();
    logger.info("webhook dispatcher fully started", {
      partitions: config.numPartitions,
      worker_pool_size: config.workerPoolSize,
    });
  } catch (err) {
    logger.warn("NATS connection failed, running in HTTP-only mode", {
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

// --- Graceful shutdown ---

async function shutdown(): Promise<void> {
  logger.info("webhook dispatcher shutting down...");
  await consumer.disconnect();
  await pool.shutdown();
  await server.close();
  logger.info("webhook dispatcher stopped", { stats: pool.getStats() });
  process.exit(0);
}

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

start().catch((err) => {
  logger.error("fatal startup error", {
    error: err instanceof Error ? err.message : String(err),
  });
  process.exit(1);
});
