import client from "prom-client";

// Collect default Node.js metrics.
client.collectDefaultMetrics({ prefix: "settla_webhook_" });

// ── Webhook delivery metrics ────────────────────────────────────────────
export const deliveriesTotal = new client.Counter({
  name: "settla_webhook_deliveries_total",
  help: "Total webhook deliveries by tenant, event type, and status.",
  labelNames: ["tenant", "event_type", "status"] as const,
});

export const deliveryDuration = new client.Histogram({
  name: "settla_webhook_delivery_duration_seconds",
  help: "Webhook delivery duration.",
  buckets: [0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30],
});

export const deadLettersTotal = new client.Counter({
  name: "settla_webhook_dead_letters_total",
  help: "Total webhook deliveries moved to dead letter queue.",
  labelNames: ["tenant", "event_type"] as const,
});

export const activeWorkers = new client.Gauge({
  name: "settla_webhook_active_workers",
  help: "Number of active webhook delivery workers.",
});

/** Returns Prometheus metrics as string. */
export async function getMetrics(): Promise<string> {
  return client.register.metrics();
}

/** Content type for metrics response. */
export const metricsContentType = client.register.contentType;
