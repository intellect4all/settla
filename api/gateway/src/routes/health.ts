import type { FastifyInstance } from "fastify";
import type { Redis } from "ioredis";
import type { GrpcPool } from "../grpc/pool.js";
import * as grpc from "@grpc/grpc-js";

interface HealthDeps {
  grpcPool?: GrpcPool | null;
  redis?: Redis | null;
}

export async function healthRoutes(
  app: FastifyInstance,
  opts: HealthDeps,
): Promise<void> {
  // Liveness probe — always returns ok. Used by Kubernetes to determine if the
  // process is alive (not deadlocked). No dependency checks.
  app.get(
    "/health",
    {
      schema: {
        response: {
          200: {
            type: "object",
            properties: {
              status: { type: "string" },
            },
          },
        },
      },
    },
    async () => {
      return { status: "ok" };
    },
  );

  // Readiness probe — checks downstream dependencies before accepting traffic.
  // Returns 503 if any critical dependency is unavailable.
  app.get(
    "/ready",
    {
      schema: {
        response: {
          200: {
            type: "object",
            properties: {
              status: { type: "string" },
              checks: { type: "object", additionalProperties: true },
            },
          },
          503: {
            type: "object",
            properties: {
              status: { type: "string" },
              checks: { type: "object", additionalProperties: true },
            },
          },
        },
      },
    },
    async (_request, reply) => {
      const checks: Record<string, { status: string; detail?: string }> = {};
      let allOk = true;

      // Check gRPC pool connectivity
      if (opts.grpcPool) {
        try {
          const ch = opts.grpcPool.getChannel();
          const state = ch.getConnectivityState(false);
          const ok =
            state === grpc.connectivityState.READY ||
            state === grpc.connectivityState.IDLE;
          checks.grpc = {
            status: ok ? "ok" : "degraded",
            detail: grpc.connectivityState[state],
          };
          if (!ok) allOk = false;
        } catch (err) {
          checks.grpc = { status: "error", detail: String(err) };
          allOk = false;
        }
      }

      // Check Redis connectivity
      if (opts.redis) {
        try {
          const pong = await Promise.race([
            opts.redis.ping(),
            new Promise<never>((_, reject) =>
              setTimeout(() => reject(new Error("redis ping timeout")), 2000),
            ),
          ]);
          checks.redis = { status: pong === "PONG" ? "ok" : "error" };
        } catch (err) {
          checks.redis = { status: "error", detail: String(err) };
          allOk = false;
        }
      }

      const statusCode = allOk ? 200 : 503;
      return reply.status(statusCode).send({
        status: allOk ? "ok" : "not_ready",
        checks,
      });
    },
  );
}
