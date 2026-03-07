import type { Logger } from "./logger.js";
import type { Config } from "./config.js";
import type { DeliveryResult, DeliveryTask, WebhookEvent } from "./types.js";
import { computeSignature } from "./signature.js";

/**
 * Deliver a webhook event to a registered endpoint.
 * Returns the delivery result (success/failure, status code, duration).
 */
export async function deliverWebhook(
  task: DeliveryTask,
  config: Config,
  logger: Logger
): Promise<DeliveryResult> {
  const { registration, event, attempt } = task;
  const body = JSON.stringify(event);
  const signature = computeSignature(body, registration.secret);
  const startTime = Date.now();

  const controller = new AbortController();
  const timeout = setTimeout(
    () => controller.abort(),
    config.deliveryTimeoutMs
  );

  try {
    const response = await fetch(registration.url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Webhook-Signature": signature,
        "X-Webhook-ID": event.event_id,
        "X-Webhook-Timestamp": event.timestamp,
        "User-Agent": "Settla-Webhook/1.0",
      },
      body,
      signal: controller.signal,
    });

    const durationMs = Date.now() - startTime;
    const result: DeliveryResult = {
      webhookId: registration.id,
      eventId: event.event_id,
      attempt,
      statusCode: response.status,
      success: response.status >= 200 && response.status < 300,
      durationMs,
    };

    // Parse Retry-After header for 429 responses
    if (response.status === 429) {
      const retryAfter = response.headers.get("Retry-After");
      if (retryAfter) {
        const seconds = parseInt(retryAfter, 10);
        if (!isNaN(seconds)) {
          result.retryAfterMs = seconds * 1000;
        }
      }
    }

    logger.deliveryAttempt(result);
    return result;
  } catch (err) {
    const durationMs = Date.now() - startTime;
    const isTimeout =
      err instanceof Error && err.name === "AbortError";
    const result: DeliveryResult = {
      webhookId: registration.id,
      eventId: event.event_id,
      attempt,
      statusCode: null,
      success: false,
      durationMs,
      error: isTimeout
        ? "timeout"
        : err instanceof Error
          ? err.message
          : "unknown error",
    };
    logger.deliveryAttempt(result);
    return result;
  } finally {
    clearTimeout(timeout);
  }
}

/**
 * Determine whether a failed delivery should be retried.
 */
export function shouldRetry(result: DeliveryResult, maxRetries: number): boolean {
  if (result.success) return false;
  if (result.attempt >= maxRetries) return false;

  // Client errors (except 429) are not retried
  if (
    result.statusCode !== null &&
    result.statusCode >= 400 &&
    result.statusCode < 500 &&
    result.statusCode !== 429
  ) {
    return false;
  }

  // 429, 5xx, timeout, network error → retry
  return true;
}

/**
 * Get the delay before the next retry attempt.
 */
export function getRetryDelay(
  result: DeliveryResult,
  retryDelaysMs: number[]
): number {
  // 429 with Retry-After takes precedence
  if (result.retryAfterMs && result.retryAfterMs > 0) {
    return result.retryAfterMs;
  }
  const index = Math.min(result.attempt, retryDelaysMs.length - 1);
  return retryDelaysMs[index];
}

/**
 * Build a webhook event payload from a NATS event.
 */
export function buildWebhookEvent(
  eventId: string,
  eventType: string,
  timestamp: string,
  data: Record<string, unknown>
): WebhookEvent {
  return {
    event: eventType,
    event_id: `evt_${eventId.replace(/-/g, "").substring(0, 12)}`,
    timestamp,
    data,
  };
}
