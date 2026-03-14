import type { WebhookNormalizer } from "../types.js";
import { settlaTestnetNormalizer } from "./settla-testnet.js";

/** Registry of per-provider webhook normalizers. */
const normalizers: Record<string, WebhookNormalizer> = {
  "settla-testnet": settlaTestnetNormalizer,
};

/**
 * Get the normalizer for a given provider ID.
 * Returns undefined if no normalizer is registered for the provider.
 */
export function getNormalizer(providerId: string): WebhookNormalizer | undefined {
  return normalizers[providerId];
}

/** Register a custom normalizer (useful for tests). */
export function registerNormalizer(providerId: string, normalizer: WebhookNormalizer): void {
  normalizers[providerId] = normalizer;
}
