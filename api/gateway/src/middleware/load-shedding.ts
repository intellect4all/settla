/**
 * Adaptive load shedding middleware for Fastify.
 *
 * Tracks in-flight requests and rejects with 503 Service Unavailable when the
 * server is overloaded. Uses exponentially-weighted moving average (EWMA) of
 * response times to calculate an adaptive concurrency limit inspired by
 * Little's Law:
 *
 *   concurrency_limit ~= target_throughput * target_latency
 *
 * When observed latency exceeds the target, the limit decreases (multiplicative
 * decrease). When latency is healthy, the limit grows (additive increase).
 * This prevents slow requests from queuing up and cascading into timeouts.
 */

import { FastifyPluginAsync, FastifyReply, FastifyRequest } from "fastify";
import fp from "fastify-plugin";

export interface LoadSheddingOptions {
  /** Hard cap on concurrent requests. Default: 1000. */
  maxConcurrent?: number;
  /** Target p50 latency in milliseconds. Default: 50. */
  targetLatencyMs?: number;
  /** Minimum adaptive limit floor. Default: 10. */
  minLimit?: number;
  /** Initial concurrency limit. Default: 200. */
  initialLimit?: number;
}

const loadSheddingPlugin: FastifyPluginAsync<LoadSheddingOptions> = async (
  fastify,
  opts
) => {
  const maxConcurrent = opts.maxConcurrent ?? 1000;
  const targetLatencyMs = opts.targetLatencyMs ?? 50;
  const minLimit = opts.minLimit ?? 10;

  let limit = opts.initialLimit ?? 200;
  let inFlight = 0;
  let ewmaLatencyMs = 0;
  let sampleCount = 0;
  const ewmaAlpha = 0.1;

  // Metrics counters for logging / Prometheus scraping.
  let totalRejected = 0;

  fastify.addHook(
    "onRequest",
    async (request: FastifyRequest, reply: FastifyReply) => {
      inFlight++;

      if (inFlight > limit) {
        inFlight--;
        totalRejected++;

        request.log.warn(
          {
            in_flight: inFlight,
            limit,
            total_rejected: totalRejected,
          },
          "settla-gateway: load shedding request"
        );

        reply.code(503).header("Retry-After", "1").send({
          error: "Service Unavailable",
          message: "Server is overloaded, please retry shortly",
          code: "LOAD_SHEDDED",
        });
        return;
      }

      // Stash the start time for latency measurement.
      (request as any)._loadShedStart = process.hrtime.bigint();
    }
  );

  fastify.addHook(
    "onResponse",
    async (request: FastifyRequest, reply: FastifyReply) => {
      inFlight = Math.max(0, inFlight - 1);

      const start = (request as any)._loadShedStart as bigint | undefined;
      if (start === undefined) {
        return; // Request was shed, no start time recorded.
      }

      const elapsedNs = Number(process.hrtime.bigint() - start);
      const elapsedMs = elapsedNs / 1_000_000;

      // Update EWMA.
      sampleCount++;
      if (sampleCount === 1) {
        ewmaLatencyMs = elapsedMs;
      } else {
        ewmaLatencyMs = ewmaAlpha * elapsedMs + (1 - ewmaAlpha) * ewmaLatencyMs;
      }

      // Adjust limit every 20 samples.
      if (sampleCount % 20 === 0) {
        const success = reply.statusCode < 500;
        if (ewmaLatencyMs <= targetLatencyMs && success) {
          // Additive increase.
          limit = Math.min(maxConcurrent, limit + 5);
        } else {
          // Multiplicative decrease.
          limit = Math.max(minLimit, Math.ceil(limit * 0.9));
        }

        request.log.debug(
          {
            ewma_latency_ms: ewmaLatencyMs.toFixed(2),
            limit,
            in_flight: inFlight,
          },
          "settla-gateway: adaptive limit updated"
        );
      }
    }
  );

  // Expose metrics endpoint for observability.
  fastify.decorate("loadShedMetrics", () => ({
    inFlight,
    limit,
    totalRejected,
    ewmaLatencyMs,
  }));

  fastify.log.info(
    {
      max_concurrent: maxConcurrent,
      target_latency_ms: targetLatencyMs,
      initial_limit: limit,
      min_limit: minLimit,
    },
    "settla-gateway: load shedding middleware registered"
  );
};

/**
 * Exported as a Fastify plugin with fastify-plugin to ensure it runs in the
 * global scope (not encapsulated), so every route is protected.
 */
export const loadShedding = fp(loadSheddingPlugin, {
  name: "settla-load-shedding",
  fastify: ">=4.0.0",
});
