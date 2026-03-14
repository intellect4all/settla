/**
 * Graceful drain middleware for Fastify.
 *
 * When SIGTERM is received, rejects new requests with 503 + Connection: close
 * while allowing in-flight requests to complete within the drain timeout.
 *
 * Usage:
 *   import { gracefulDrain } from './middleware/graceful-drain.js';
 *   await app.register(gracefulDrain, { drainTimeoutMs: 15000 });
 */
import { FastifyPluginAsync, FastifyRequest, FastifyReply } from "fastify";
import fp from "fastify-plugin";

interface GracefulDrainOptions {
  /** Maximum time (ms) to wait for in-flight requests during drain. Default: 15000 */
  drainTimeoutMs?: number;
}

interface DrainState {
  draining: boolean;
  inFlight: number;
  rejected: number;
}

const gracefulDrainPlugin: FastifyPluginAsync<GracefulDrainOptions> = async (
  fastify,
  opts
) => {
  const drainTimeoutMs = opts.drainTimeoutMs ?? 15000;

  const state: DrainState = {
    draining: false,
    inFlight: 0,
    rejected: 0,
  };

  let drainResolve: (() => void) | null = null;

  // Decorate fastify with drain state for health checks / metrics.
  fastify.decorate("drainState", state);

  // Hook into every request to track in-flight and reject during drain.
  fastify.addHook(
    "onRequest",
    async (_request: FastifyRequest, reply: FastifyReply) => {
      if (state.draining) {
        state.rejected++;
        reply.header("Connection", "close");
        reply.code(503).send({
          error: "server is shutting down",
          code: "SERVICE_UNAVAILABLE",
        });
        return;
      }
      state.inFlight++;
    }
  );

  // Decrement in-flight after response is sent.
  fastify.addHook("onResponse", async () => {
    state.inFlight--;
    if (state.draining && state.inFlight <= 0 && drainResolve) {
      drainResolve();
    }
  });

  // Also handle errors that bypass onResponse.
  fastify.addHook("onError", async () => {
    state.inFlight--;
    if (state.draining && state.inFlight <= 0 && drainResolve) {
      drainResolve();
    }
  });

  const startDrain = async (): Promise<void> => {
    if (state.draining) return;

    state.draining = true;
    fastify.log.info(
      { inFlight: state.inFlight, timeoutMs: drainTimeoutMs },
      "settla-drain: starting graceful drain"
    );

    if (state.inFlight <= 0) {
      fastify.log.info("settla-drain: no in-flight requests, drain complete");
      return;
    }

    // Wait for in-flight requests to complete or timeout.
    await Promise.race([
      new Promise<void>((resolve) => {
        drainResolve = resolve;
      }),
      new Promise<void>((resolve) => {
        setTimeout(() => {
          fastify.log.warn(
            { remaining: state.inFlight },
            "settla-drain: timeout reached with requests still in-flight"
          );
          resolve();
        }, drainTimeoutMs);
      }),
    ]);

    fastify.log.info(
      { remaining: state.inFlight, rejected: state.rejected },
      "settla-drain: drain phase complete"
    );
  };

  // Listen for SIGTERM to initiate drain + shutdown.
  const shutdown = async (signal: string) => {
    fastify.log.info({ signal }, "settla-drain: received shutdown signal");
    await startDrain();
    await fastify.close();
    process.exit(0);
  };

  process.on("SIGTERM", () => void shutdown("SIGTERM"));
  process.on("SIGINT", () => void shutdown("SIGINT"));

  // Expose drain function for programmatic use (e.g. in tests).
  fastify.decorate("startDrain", startDrain);
};

export const gracefulDrain = fp(gracefulDrainPlugin, {
  fastify: ">=4.x",
  name: "settla-graceful-drain",
});
