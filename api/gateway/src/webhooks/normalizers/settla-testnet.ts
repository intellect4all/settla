import type { ProviderWebhook, WebhookNormalizer } from "../types.js";

/**
 * Normalizer for Settla testnet provider webhook format.
 *
 * Expected payload shape:
 * {
 *   event_id: string,
 *   transfer_ref: string,
 *   event_type: "onramp.completed" | "offramp.completed" | "blockchain.confirmed" | ...
 *   status: "completed" | "failed" | "pending",
 *   data: Record<string, string>
 * }
 */
export const settlaTestnetNormalizer: WebhookNormalizer = {
  normalize(providerId: string, payload: unknown): ProviderWebhook | null {
    const p = payload as Record<string, any>;
    if (!p || typeof p !== "object") return null;

    const eventId = p.event_id;
    const transferRef = p.transfer_ref;
    const eventType = p.event_type as string | undefined;
    const status = p.status as string | undefined;

    if (!eventId || !transferRef || !eventType || !status) return null;

    // Parse step from event_type (e.g. "onramp.completed" → "onramp")
    const stepStr = eventType.split(".")[0];
    const step = parseStep(stepStr);
    if (!step) return null;

    const normalizedStatus = parseStatus(status);
    if (!normalizedStatus) return null;

    return {
      provider: providerId,
      externalEventId: String(eventId),
      transferRef: String(transferRef),
      step,
      status: normalizedStatus,
      metadata: p.data && typeof p.data === "object"
        ? Object.fromEntries(
            Object.entries(p.data).map(([k, v]) => [k, String(v)]),
          )
        : {},
    };
  },
};

function parseStep(s: string): "onramp" | "offramp" | "blockchain" | null {
  if (s === "onramp") return "onramp";
  if (s === "offramp") return "offramp";
  if (s === "blockchain") return "blockchain";
  return null;
}

function parseStatus(s: string): "completed" | "failed" | "pending" | null {
  if (s === "completed") return "completed";
  if (s === "failed") return "failed";
  if (s === "pending") return "pending";
  return null;
}
