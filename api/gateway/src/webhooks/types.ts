/** Normalized inbound webhook from any provider. */
export interface ProviderWebhook {
  provider: string;
  externalEventId: string;
  transferRef: string;
  step: "onramp" | "offramp" | "blockchain";
  status: "completed" | "failed" | "pending";
  metadata: Record<string, string>;
}

/** A normalizer transforms a raw provider payload into a ProviderWebhook. */
export interface WebhookNormalizer {
  normalize(providerId: string, payload: unknown): ProviderWebhook | null;
}
