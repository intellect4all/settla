import type { DeliveryResult, DeadLetterEntry } from "./types.js";

export interface Logger {
  info(msg: string, fields?: Record<string, unknown>): void;
  error(msg: string, fields?: Record<string, unknown>): void;
  warn(msg: string, fields?: Record<string, unknown>): void;
  deliveryAttempt(result: DeliveryResult): void;
  deadLetter(entry: DeadLetterEntry): void;
}

/**
 * Structured JSON logger matching the slog convention used in Go services.
 */
export function createLogger(service: string): Logger {
  function log(
    level: string,
    msg: string,
    fields?: Record<string, unknown>
  ): void {
    const entry = {
      time: new Date().toISOString(),
      level,
      msg,
      service,
      ...fields,
    };
    if (level === "ERROR") {
      console.error(JSON.stringify(entry));
    } else if (level === "WARN") {
      console.warn(JSON.stringify(entry));
    } else {
      console.log(JSON.stringify(entry));
    }
  }

  return {
    info: (msg, fields) => log("INFO", msg, fields),
    error: (msg, fields) => log("ERROR", msg, fields),
    warn: (msg, fields) => log("WARN", msg, fields),

    deliveryAttempt(result: DeliveryResult): void {
      log(result.success ? "INFO" : "WARN", "webhook delivery attempt", {
        event_id: result.eventId,
        webhook_id: result.webhookId,
        attempt: result.attempt,
        status_code: result.statusCode,
        duration_ms: result.durationMs,
        success: result.success,
        error: result.error,
      });
    },

    deadLetter(entry: DeadLetterEntry): void {
      log("ERROR", "webhook dead-lettered", {
        event_id: entry.eventId,
        webhook_id: entry.webhookId,
        tenant_id: entry.tenantId,
        last_attempt: entry.lastAttempt,
        last_error: entry.lastError,
      });
    },
  };
}
