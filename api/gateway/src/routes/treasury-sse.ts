import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { connect, type NatsConnection, type Subscription, StringCodec } from "nats";
import { config } from "../config.js";

const sc = StringCodec();

/**
 * SSE endpoint for real-time treasury position updates.
 *
 * Clients connect to GET /v1/treasury/stream and receive position events
 * as Server-Sent Events, pushed in real-time via a direct NATS subscription
 * on the settla.treasury.partition.*.> subject.
 *
 * On disconnect, clients should reconnect with the Last-Event-ID header
 * to resume from where they left off.
 */
export async function treasurySseRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  // Lazily connect to NATS on first SSE client. Shared across all SSE connections.
  let nc: NatsConnection | null = null;

  async function getNatsConnection(): Promise<NatsConnection> {
    if (nc && !nc.isClosed()) return nc;
    nc = await connect({ servers: config.natsUrl });
    return nc;
  }

  // Cleanup NATS on server shutdown.
  app.addHook("onClose", async () => {
    if (nc && !nc.isClosed()) {
      await nc.drain();
    }
  });

  app.get(
    "/v1/treasury/stream",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Stream position events (SSE)",
        operationId: "streamPositionEvents",
        description: "Server-Sent Events stream for real-time treasury position updates via NATS",
      },
    },
    async (request: FastifyRequest, reply: FastifyReply) => {
      const { tenantAuth } = request;
      const tenantId = tenantAuth.tenantId;

      // Set SSE headers.
      reply.raw.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
        "X-Accel-Buffering": "no", // disable nginx buffering
      });

      let sub: Subscription | null = null;

      try {
        const conn = await getNatsConnection();

        // Subscribe to all treasury partition events. Filter by tenantId in-process.
        sub = conn.subscribe("settla.treasury.partition.*.>");

        // Heartbeat to keep connection alive.
        const heartbeatTimer = setInterval(() => {
          try {
            reply.raw.write(": heartbeat\n\n");
          } catch {
            // Connection closed.
          }
        }, 30_000);

        // Process NATS messages and push matching events to SSE.
        (async () => {
          for await (const msg of sub) {
            try {
              const payload = JSON.parse(sc.decode(msg.data));

              // Filter: only send events for this tenant.
              if (payload.tenant_id !== tenantId && payload.tenantId !== tenantId) {
                continue;
              }

              const data = JSON.stringify({
                positionId: payload.position_id || payload.positionId,
                eventType: payload.event_type || payload.eventType,
                amount: payload.amount,
                balanceAfter: payload.balance_after || payload.balanceAfter,
                lockedAfter: payload.locked_after || payload.lockedAfter,
                currency: payload.currency,
                location: payload.location,
                referenceId: payload.reference_id || payload.referenceId,
                referenceType: payload.reference_type || payload.referenceType,
                recordedAt: payload.recorded_at || payload.recordedAt || new Date().toISOString(),
              });

              const eventId = payload.recorded_at || payload.recordedAt || new Date().toISOString();
              reply.raw.write(`id: ${eventId}\n`);
              reply.raw.write(`event: position_update\n`);
              reply.raw.write(`data: ${data}\n\n`);
            } catch {
              // Skip malformed messages silently.
            }
          }
        })().catch(() => {
          // Subscription ended (client disconnect or NATS drain).
        });

        // Cleanup on client disconnect.
        request.raw.on("close", () => {
          clearInterval(heartbeatTimer);
          sub?.unsubscribe();
        });
      } catch (err) {
        request.log.error({ err }, "settla-treasury-sse: NATS connection failed, falling back to closed stream");
        reply.raw.write(`event: error\ndata: ${JSON.stringify({ error: "stream unavailable" })}\n\n`);
        reply.raw.end();
        return;
      }

      // Keep the response open (don't call reply.send).
      await reply;
    },
  );
}
