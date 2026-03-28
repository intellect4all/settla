import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";

/**
 * SSE endpoint for real-time treasury position updates.
 *
 * Clients connect to GET /v1/treasury/stream and receive position events
 * (CREDIT, DEBIT, RESERVE, RELEASE, COMMIT, CONSUME) as Server-Sent Events.
 *
 * The endpoint polls the gRPC service for position events newer than the
 * client's last-seen timestamp. This is a simple polling-based SSE
 * implementation that avoids direct NATS dependency in the gateway.
 *
 * On disconnect, clients should reconnect with the Last-Event-ID header
 * to resume from where they left off. If no Last-Event-ID is provided,
 * the stream starts from the current time.
 */
export async function treasurySseRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.get(
    "/v1/treasury/stream",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Stream position events (SSE)",
        operationId: "streamPositionEvents",
        description: "Server-Sent Events stream for real-time treasury position updates",
      },
    },
    async (request: FastifyRequest, reply: FastifyReply) => {
      const { tenantAuth } = request;

      // Set SSE headers.
      reply.raw.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
        "X-Accel-Buffering": "no", // disable nginx buffering
      });

      // Determine start time from Last-Event-ID or default to now.
      const lastEventId = request.headers["last-event-id"] as string | undefined;
      let cursor = lastEventId
        ? new Date(lastEventId)
        : new Date();

      // Heartbeat interval to keep connection alive.
      const heartbeatMs = 30_000;
      const pollMs = 2_000;

      const heartbeatTimer = setInterval(() => {
        try {
          reply.raw.write(": heartbeat\n\n");
        } catch {
          // Connection closed — cleanup will happen via close handler.
        }
      }, heartbeatMs);

      // Poll for new events.
      const pollTimer = setInterval(async () => {
        try {
          // Fetch all positions for the tenant, then get events for each.
          const positionsResult = await grpc.getPositions({
            tenantId: tenantAuth.tenantId,
          }, request.id);

          for (const pos of positionsResult.positions || []) {
            const fromTs = { seconds: Math.floor(cursor.getTime() / 1000), nanos: 0 };
            const toTs = { seconds: Math.floor(Date.now() / 1000), nanos: 0 };

            const eventsResult = await grpc.getPositionEventHistory({
              tenantId: tenantAuth.tenantId,
              currency: pos.currency,
              location: pos.location,
              from: fromTs,
              to: toTs,
              limit: 100,
              offset: 0,
            }, request.id);

            for (const event of eventsResult.events || []) {
              const data = JSON.stringify({
                positionId: event.positionId,
                eventType: event.eventType,
                amount: event.amount,
                balanceAfter: event.balanceAfter,
                lockedAfter: event.lockedAfter,
                currency: pos.currency,
                location: pos.location,
                referenceId: event.referenceId,
                referenceType: event.referenceType,
                recordedAt: event.recordedAt,
              });

              reply.raw.write(`id: ${event.recordedAt}\n`);
              reply.raw.write(`event: position_update\n`);
              reply.raw.write(`data: ${data}\n\n`);

              // Update cursor to the latest event time.
              const eventTime = new Date(event.recordedAt);
              if (eventTime > cursor) {
                cursor = eventTime;
              }
            }
          }
        } catch (err) {
          // Log but don't crash the stream — transient gRPC errors are expected.
          request.log.warn({ err }, "settla-treasury-sse: poll error");
        }
      }, pollMs);

      // Cleanup on disconnect.
      request.raw.on("close", () => {
        clearInterval(heartbeatTimer);
        clearInterval(pollTimer);
      });

      // Keep the response open (don't call reply.send).
      await reply;
    },
  );
}
