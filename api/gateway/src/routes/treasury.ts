import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  positionsResponseSchema,
  positionResponseSchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

export async function treasuryRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.get(
    "/v1/treasury/positions",
    {
      schema: {
        response: {
          200: positionsResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getPositions({
          tenantId: tenantAuth.tenantId,
        });
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(reply, err);
      }
    },
  );

  app.get<{
    Params: { currency: string; location: string };
  }>(
    "/v1/treasury/positions/:currency/:location",
    {
      schema: {
        params: {
          type: "object",
          properties: {
            currency: { type: "string" },
            location: { type: "string" },
          },
          required: ["currency", "location"],
        },
        response: {
          200: positionResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getPosition({
          tenantId: tenantAuth.tenantId,
          currency: request.params.currency,
          location: request.params.location,
        });
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(reply, err);
      }
    },
  );

  app.get(
    "/v1/treasury/liquidity",
    {
      schema: {
        response: {
          200: {
            type: "object",
            properties: {
              tenant_id: { type: "string" },
              positions: { type: "array", items: positionResponseSchema },
              total_available: { type: "object", additionalProperties: { type: "string" } },
              alert_positions: { type: "array", items: positionResponseSchema },
              generated_at: { type: "string" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getLiquidityReport({
          tenantId: tenantAuth.tenantId,
        });
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(reply, err);
      }
    },
  );
}
