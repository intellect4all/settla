import type { FastifyRequest } from "fastify";
import * as grpc from "@grpc/grpc-js";

/**
 * Injects W3C Trace Context headers from the inbound HTTP request into
 * gRPC metadata for downstream trace propagation.
 *
 * If the inbound request carries a `traceparent` header (e.g., from an
 * external load balancer or observability proxy), it is forwarded to the
 * gRPC backend so that spans are linked end-to-end.
 */
export function injectTraceContext(
  meta: grpc.Metadata,
  request?: FastifyRequest
): void {
  if (!request) return;

  const traceparent = request.headers["traceparent"];
  if (traceparent && typeof traceparent === "string") {
    meta.add("traceparent", traceparent);
  }

  const tracestate = request.headers["tracestate"];
  if (tracestate && typeof tracestate === "string") {
    meta.add("tracestate", tracestate);
  }
}
