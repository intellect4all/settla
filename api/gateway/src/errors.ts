import * as grpc from "@grpc/grpc-js";
import type { FastifyReply, FastifyRequest } from "fastify";
import { config } from "./config.js";

export function assertTenantMatch(expected: string, actual: string | undefined, resource: string) {
  if (actual && actual !== expected) {
    throw new Error(`[TENANT_MISMATCH] ${resource} belongs to different tenant`);
  }
}

/**
 * Known domain error codes from the Go backend (domain/errors.go).
 * Used to validate extracted codes and provide fallback HTTP error codes.
 */
const DOMAIN_CODE_HTTP_MAP: Record<string, { httpStatus: number; fallbackError: string }> = {
  // 400 - Bad Request / Invalid Argument
  AMOUNT_TOO_LOW: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  AMOUNT_TOO_HIGH: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  CURRENCY_MISMATCH: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  LEDGER_IMBALANCE: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  PAYMENT_MISMATCH: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  CRYPTO_DISABLED: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  CHAIN_NOT_SUPPORTED: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  BANK_DEPOSITS_DISABLED: { httpStatus: 400, fallbackError: "BAD_REQUEST" },
  CURRENCY_NOT_SUPPORTED: { httpStatus: 400, fallbackError: "BAD_REQUEST" },

  // 401 - Unauthorized
  UNAUTHORIZED: { httpStatus: 401, fallbackError: "UNAUTHORIZED" },
  INVALID_CREDENTIALS: { httpStatus: 401, fallbackError: "UNAUTHORIZED" },

  // 403 - Forbidden
  TENANT_SUSPENDED: { httpStatus: 403, fallbackError: "FORBIDDEN" },
  TENANT_MISMATCH: { httpStatus: 403, fallbackError: "FORBIDDEN" },

  // 404 - Not Found
  TRANSFER_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },
  ACCOUNT_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },
  TENANT_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },
  DEPOSIT_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },
  BANK_DEPOSIT_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },
  PAYMENT_LINK_NOT_FOUND: { httpStatus: 404, fallbackError: "NOT_FOUND" },

  // 409 - Conflict
  IDEMPOTENCY_CONFLICT: { httpStatus: 409, fallbackError: "CONFLICT" },
  EMAIL_ALREADY_EXISTS: { httpStatus: 409, fallbackError: "CONFLICT" },
  SLUG_CONFLICT: { httpStatus: 409, fallbackError: "CONFLICT" },
  OPTIMISTIC_LOCK: { httpStatus: 409, fallbackError: "CONFLICT" },

  // 422 - Unprocessable Entity (precondition failures)
  QUOTE_EXPIRED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  INVALID_TRANSITION: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  POSITION_LOCKED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  CORRIDOR_DISABLED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  EMAIL_NOT_VERIFIED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  TOKEN_EXPIRED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  DEPOSIT_EXPIRED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  PAYMENT_LINK_EXPIRED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  PAYMENT_LINK_EXHAUSTED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },
  PAYMENT_LINK_DISABLED: { httpStatus: 422, fallbackError: "UNPROCESSABLE" },

  // 429 - Rate Limited / Resource Exhausted
  INSUFFICIENT_FUNDS: { httpStatus: 429, fallbackError: "RATE_LIMITED" },
  RESERVATION_FAILED: { httpStatus: 429, fallbackError: "RATE_LIMITED" },
  DAILY_LIMIT_EXCEEDED: { httpStatus: 429, fallbackError: "RATE_LIMITED" },
  RATE_LIMIT_EXCEEDED: { httpStatus: 429, fallbackError: "RATE_LIMITED" },

  // 500 - Internal
  BLOCKCHAIN_REORG: { httpStatus: 500, fallbackError: "INTERNAL" },
  COMPENSATION_FAILED: { httpStatus: 500, fallbackError: "INTERNAL" },

  // 503 - Unavailable
  PROVIDER_ERROR: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
  CHAIN_ERROR: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
  PROVIDER_UNAVAILABLE: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
  NETWORK_ERROR: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
  ADDRESS_POOL_EMPTY: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
  VIRTUAL_ACCOUNT_POOL_EMPTY: { httpStatus: 503, fallbackError: "UNAVAILABLE" },
};

/** Regex to extract domain error code from gRPC message: "[CODE] message" */
const DOMAIN_CODE_REGEX = /^\[([A-Z_]+)\]\s*/;

/**
 * Patterns that indicate internal details that should be stripped from
 * client-facing error messages.
 */
const INTERNAL_PATTERNS = [
  /\bpq:\s/i,                  // Postgres driver errors
  /\bpgx:\s/i,                 // pgx driver errors
  /\bsql:/i,                   // SQL errors
  /\bconstraint\b/i,           // constraint violation details
  /invalid input syntax/i,     // Postgres type-cast errors
  /violates.*constraint/i,     // Postgres constraint violations
  /column.*does not exist/i,   // Postgres schema leaks (missing column)
  /relation.*does not exist/i, // Postgres schema leaks (missing table)
  /duplicate key/i,            // Postgres unique-violation details
  /\bstack trace\b/i,          // stack traces
  /\bgoroutine\b/i,            // Go stack traces
  /\bpanic\b/i,                // Go panics
  /\bindex\s+\w+_idx\b/i,     // index names
  /\brelation\s+"\w+"/i,       // table names in errors
  /\bcolumn\s+"\w+"/i,         // column names in errors
  /\bERROR:\s/i,               // raw Postgres ERROR prefix
];

const isProduction = config.env === "production";

/**
 * Extract the domain error code from a gRPC message if it follows the
 * "[CODE] message" format set by the Go backend's mapDomainError.
 */
function extractDomainCode(message: string): { code: string | null; cleanMessage: string } {
  const match = message.match(DOMAIN_CODE_REGEX);
  if (match && match[1] in DOMAIN_CODE_HTTP_MAP) {
    return {
      code: match[1],
      cleanMessage: message.slice(match[0].length),
    };
  }
  return { code: null, cleanMessage: message };
}

/**
 * Sanitize an error message for client consumption:
 * - Strip the "settla-domain: " prefix (internal naming)
 * - Remove internal details (SQL, constraint names, etc.)
 * - In production, replace 500-level messages with generic text
 */
function sanitizeMessage(message: string, httpStatus: number): string {
  // Strip internal prefix
  let cleaned = message.replace(/^settla-domain:\s*/i, "");

  // Check for internal detail leakage
  const hasInternalDetails = INTERNAL_PATTERNS.some((p) => p.test(cleaned));

  // In production, never expose internal details or 500-level raw messages
  if (isProduction) {
    if (hasInternalDetails) {
      return "An internal error occurred. Please contact support if the issue persists.";
    }
    if (httpStatus >= 500) {
      return "An internal error occurred. Please try again later.";
    }
  }

  // Even in development, strip obvious internal patterns
  if (hasInternalDetails) {
    // Return only the portion before the internal detail
    const firstInternalMatch = INTERNAL_PATTERNS.reduce<number>((minIdx, pattern) => {
      const m = cleaned.match(pattern);
      if (m && m.index !== undefined && m.index < minIdx) {
        return m.index;
      }
      return minIdx;
    }, cleaned.length);

    if (firstInternalMatch > 0) {
      cleaned = cleaned.slice(0, firstInternalMatch).trim();
    }
    if (!cleaned) {
      cleaned = "An error occurred while processing the request";
    }
  }

  return cleaned;
}

/** Map gRPC status codes to HTTP status codes and send structured error response. */
export function mapGrpcError(request: FastifyRequest, reply: FastifyReply, err: unknown): FastifyReply {
  const grpcErr = err as grpc.ServiceError;
  const request_id = request.id;

  if (!grpcErr || grpcErr.code === undefined) {
    return reply.status(500).send({
      error: "INTERNAL",
      code: "INTERNAL_ERROR",
      message: isProduction
        ? "An unexpected error occurred"
        : "An unexpected error occurred (non-gRPC error)",
      request_id,
    });
  }

  const { code: grpcCode, details } = grpcErr;
  const rawMessage = details || "An error occurred";

  // Try to extract domain code from the "[CODE] message" format
  const { code: domainCode, cleanMessage } = extractDomainCode(rawMessage);

  // If we have a known domain code, use its mapping
  if (domainCode) {
    const mapping = DOMAIN_CODE_HTTP_MAP[domainCode];
    const message = sanitizeMessage(cleanMessage, mapping.httpStatus);
    return reply.status(mapping.httpStatus).send({
      error: domainCode,
      code: domainCode,
      message,
      request_id,
    });
  }

  // Log unmapped domain codes for visibility — helps detect new codes added
  // to the Go backend that haven't been mapped in the gateway yet.
  const unmappedMatch = rawMessage.match(DOMAIN_CODE_REGEX);
  if (unmappedMatch && !(unmappedMatch[1] in DOMAIN_CODE_HTTP_MAP)) {
    request.log.warn(
      { unmapped_code: unmappedMatch[1], grpc_code: grpcCode, request_id },
      "gateway: unmapped domain error code — add to DOMAIN_CODE_HTTP_MAP",
    );
    request.log.warn(
      { code: unmappedMatch[1] },
      "UNMAPPED_DOMAIN_ERROR_CODE",
    );
    return reply.status(500).send({
      error: "Internal Server Error",
      code: unmappedMatch[1],
      message: "An unexpected error occurred",
      request_id,
    });
  }

  // Fall back to gRPC status code mapping (for non-domain errors)
  switch (grpcCode) {
    case grpc.status.NOT_FOUND:
      return reply.status(404).send({
        error: "NOT_FOUND",
        code: "NOT_FOUND",
        message: sanitizeMessage(rawMessage, 404),
        request_id,
      });
    case grpc.status.ALREADY_EXISTS:
      return reply.status(409).send({
        error: "CONFLICT",
        code: "CONFLICT",
        message: sanitizeMessage(rawMessage, 409),
        request_id,
      });
    case grpc.status.INVALID_ARGUMENT:
      return reply.status(400).send({
        error: "BAD_REQUEST",
        code: "BAD_REQUEST",
        message: sanitizeMessage(rawMessage, 400),
        request_id,
      });
    case grpc.status.FAILED_PRECONDITION:
      return reply.status(422).send({
        error: "UNPROCESSABLE",
        code: "UNPROCESSABLE",
        message: sanitizeMessage(rawMessage, 422),
        request_id,
      });
    case grpc.status.PERMISSION_DENIED:
      return reply.status(403).send({
        error: "FORBIDDEN",
        code: "FORBIDDEN",
        message: sanitizeMessage(rawMessage, 403),
        request_id,
      });
    case grpc.status.UNAUTHENTICATED:
      return reply.status(401).send({
        error: "UNAUTHORIZED",
        code: "UNAUTHORIZED",
        message: sanitizeMessage(rawMessage, 401),
        request_id,
      });
    case grpc.status.RESOURCE_EXHAUSTED:
      return reply.status(429).send({
        error: "RATE_LIMITED",
        code: "RATE_LIMITED",
        message: sanitizeMessage(rawMessage, 429),
        request_id,
      });
    case grpc.status.UNAVAILABLE:
      return reply.status(503).send({
        error: "UNAVAILABLE",
        code: "UNAVAILABLE",
        message: sanitizeMessage(rawMessage, 503),
        request_id,
      });
    case grpc.status.DEADLINE_EXCEEDED:
      return reply.status(504).send({
        error: "TIMEOUT",
        code: "TIMEOUT",
        message: sanitizeMessage(rawMessage, 504),
        request_id,
      });
    default:
      return reply.status(500).send({
        error: "INTERNAL",
        code: "INTERNAL_ERROR",
        message: sanitizeMessage(rawMessage, 500),
        request_id,
      });
  }
}
