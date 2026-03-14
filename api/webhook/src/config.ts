export interface Config {
  port: number;
  host: string;
  natsUrl: string;
  streamName: string;
  subjectPrefix: string;
  numPartitions: number;
  workerPoolSize: number;
  deliveryTimeoutMs: number;
  maxRetries: number;
  retryDelaysMs: number[];
  /**
   * HMAC-SHA256 signing secrets for inbound provider webhooks.
   * Map of providerSlug → secret. Read from PROVIDER_{SLUG_UPPER}_WEBHOOK_SECRET.
   */
  providerSigningSecrets: Record<string, string>;
  /**
   * Optional override for the signature header name per provider.
   * Map of providerSlug → header name (lower-cased).
   * Defaults to "x-webhook-signature" when not specified.
   * Read from PROVIDER_{SLUG_UPPER}_SIGNATURE_HEADER.
   */
  providerSignatureHeaders: Record<string, string>;
}

/**
 * Scans environment variables for provider webhook secrets and header overrides.
 *
 * Pattern: PROVIDER_{SLUG_UPPER}_WEBHOOK_SECRET=<secret>
 * Pattern: PROVIDER_{SLUG_UPPER}_SIGNATURE_HEADER=<header-name>
 *
 * Examples:
 *   PROVIDER_YELLOW_CARD_WEBHOOK_SECRET=whsec_...
 *   PROVIDER_YELLOW_CARD_SIGNATURE_HEADER=x-yc-signature
 */
function loadProviderSecrets(): {
  secrets: Record<string, string>;
  headers: Record<string, string>;
} {
  const secrets: Record<string, string> = {};
  const headers: Record<string, string> = {};

  const secretPrefix = "PROVIDER_";
  const secretSuffix = "_WEBHOOK_SECRET";
  const headerSuffix = "_SIGNATURE_HEADER";

  for (const [key, value] of Object.entries(process.env)) {
    if (!value) continue;

    if (key.startsWith(secretPrefix) && key.endsWith(secretSuffix)) {
      // e.g. PROVIDER_YELLOW_CARD_WEBHOOK_SECRET → yellow-card
      const slug = key
        .slice(secretPrefix.length, -secretSuffix.length)
        .toLowerCase()
        .replace(/_/g, "-");
      secrets[slug] = value;
    } else if (key.startsWith(secretPrefix) && key.endsWith(headerSuffix)) {
      // e.g. PROVIDER_YELLOW_CARD_SIGNATURE_HEADER → yellow-card
      const slug = key
        .slice(secretPrefix.length, -headerSuffix.length)
        .toLowerCase()
        .replace(/_/g, "-");
      headers[slug] = value.toLowerCase();
    }
  }

  return { secrets, headers };
}

export function loadConfig(): Config {
  const { secrets, headers } = loadProviderSecrets();

  return {
    port: Number(process.env.PORT) || 3001,
    host: process.env.HOST || "0.0.0.0",
    natsUrl: process.env.NATS_URL || "nats://localhost:4222",
    streamName: process.env.NATS_STREAM || "SETTLA_TRANSFERS",
    subjectPrefix: process.env.NATS_SUBJECT_PREFIX || "settla.transfer",
    numPartitions: Number(process.env.NUM_PARTITIONS) || 8,
    workerPoolSize: Number(process.env.WORKER_POOL_SIZE) || 20,
    deliveryTimeoutMs: Number(process.env.DELIVERY_TIMEOUT_MS) || 30_000,
    maxRetries: Number(process.env.MAX_RETRIES) || 5,
    retryDelaysMs: [0, 30_000, 120_000, 900_000, 3_600_000],
    providerSigningSecrets: secrets,
    providerSignatureHeaders: headers,
  };
}
