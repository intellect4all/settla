import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  positionsResponseSchema,
  positionResponseSchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError, assertTenantMatch } from "../errors.js";

const positionTransactionSchema = {
  type: "object",
  properties: {
    id: { type: "string" },
    tenantId: { type: "string" },
    type: { type: "string" },
    currency: { type: "string" },
    location: { type: "string" },
    amount: { type: "string" },
    status: { type: "string" },
    method: { type: "string" },
    destination: { type: "string" },
    reference: { type: "string" },
    failureReason: { type: "string" },
    createdAt: { type: "string" },
    updatedAt: { type: "string" },
  },
} as const;

const positionEventSchema = {
  type: "object",
  properties: {
    id: { type: "string" },
    positionId: { type: "string" },
    tenantId: { type: "string" },
    eventType: { type: "string" },
    amount: { type: "string" },
    balanceAfter: { type: "string" },
    lockedAfter: { type: "string" },
    referenceId: { type: "string" },
    referenceType: { type: "string" },
    recordedAt: { type: "string" },
  },
} as const;

export async function treasuryRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.get(
    "/v1/treasury/positions",
    {
      schema: {
        tags: ["Treasury"],
        summary: "List treasury positions",
        operationId: "listPositions",
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
        }, request.id);
        for (const p of result.positions || []) {
          assertTenantMatch(tenantAuth.tenantId, p.tenantId, 'position');
        }
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { currency: string; location: string };
  }>(
    "/v1/treasury/positions/:currency/:location",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Get a treasury position",
        operationId: "getPosition",
        params: {
          type: "object",
          properties: {
            currency: { type: "string", pattern: "^[A-Z]{3,5}$" },
            location: { type: "string", minLength: 1 },
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
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get(
    "/v1/treasury/liquidity",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Get liquidity report",
        operationId: "getLiquidityReport",
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
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── Position Management (Top-Up / Withdraw) ───────────────────────────

  app.post<{
    Body: { currency: string; location: string; amount: string; method: string };
  }>(
    "/v1/treasury/topup",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Request a position top-up",
        operationId: "requestTopUp",
        body: {
          type: "object",
          required: ["currency", "location", "amount"],
          properties: {
            currency: { type: "string", pattern: "^[A-Z]{3,5}$" },
            location: { type: "string", minLength: 1 },
            amount: { type: "string", pattern: "^[0-9]+(\\.[0-9]+)?$" },
            method: { type: "string", enum: ["bank_transfer", "crypto", "internal"], default: "bank_transfer" },
          },
        },
        response: {
          200: { type: "object", properties: { transaction: positionTransactionSchema } },
          400: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.requestTopUp({
          tenantId: tenantAuth.tenantId,
          currency: request.body.currency,
          location: request.body.location,
          amount: request.body.amount,
          method: request.body.method || "bank_transfer",
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.post<{
    Body: { currency: string; location: string; amount: string; method: string; destination: string };
  }>(
    "/v1/treasury/withdraw",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Request a position withdrawal",
        operationId: "requestWithdrawal",
        body: {
          type: "object",
          required: ["currency", "location", "amount", "destination"],
          properties: {
            currency: { type: "string", pattern: "^[A-Z]{3,5}$" },
            location: { type: "string", minLength: 1 },
            amount: { type: "string", pattern: "^[0-9]+(\\.[0-9]+)?$" },
            method: { type: "string", enum: ["bank_transfer", "crypto"], default: "bank_transfer" },
            destination: { type: "string", minLength: 1 },
          },
        },
        response: {
          200: { type: "object", properties: { transaction: positionTransactionSchema } },
          400: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.requestWithdrawal({
          tenantId: tenantAuth.tenantId,
          currency: request.body.currency,
          location: request.body.location,
          amount: request.body.amount,
          method: request.body.method || "bank_transfer",
          destination: request.body.destination,
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Querystring: { limit?: number; offset?: number };
  }>(
    "/v1/treasury/transactions",
    {
      schema: {
        tags: ["Treasury"],
        summary: "List position transactions",
        operationId: "listPositionTransactions",
        querystring: {
          type: "object",
          properties: {
            limit: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            offset: { type: "integer", minimum: 0, default: 0 },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              transactions: { type: "array", items: positionTransactionSchema },
              totalCount: { type: "integer" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listPositionTransactions({
          tenantId: tenantAuth.tenantId,
          limit: request.query.limit || 20,
          offset: request.query.offset || 0,
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { id: string };
  }>(
    "/v1/treasury/transactions/:id",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Get a position transaction",
        operationId: "getPositionTransaction",
        params: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
          },
          required: ["id"],
        },
        response: {
          200: { type: "object", properties: { transaction: positionTransactionSchema } },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getPositionTransaction({
          tenantId: tenantAuth.tenantId,
          transactionId: request.params.id,
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { currency: string; location: string };
    Querystring: { from?: string; to?: string; limit?: number; offset?: number };
  }>(
    "/v1/treasury/positions/:currency/:location/events",
    {
      schema: {
        tags: ["Treasury"],
        summary: "Get position event history",
        operationId: "getPositionEventHistory",
        params: {
          type: "object",
          properties: {
            currency: { type: "string", pattern: "^[A-Z]{3,5}$" },
            location: { type: "string", minLength: 1 },
          },
          required: ["currency", "location"],
        },
        querystring: {
          type: "object",
          properties: {
            from: { type: "string", format: "date-time" },
            to: { type: "string", format: "date-time" },
            limit: { type: "integer", minimum: 1, maximum: 100, default: 50 },
            offset: { type: "integer", minimum: 0, default: 0 },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              events: { type: "array", items: positionEventSchema },
              totalCount: { type: "integer" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const fromTs = request.query.from
          ? { seconds: Math.floor(new Date(request.query.from).getTime() / 1000), nanos: 0 }
          : undefined;
        const toTs = request.query.to
          ? { seconds: Math.floor(new Date(request.query.to).getTime() / 1000), nanos: 0 }
          : undefined;

        const result = await grpc.getPositionEventHistory({
          tenantId: tenantAuth.tenantId,
          currency: request.params.currency,
          location: request.params.location,
          from: fromTs,
          to: toTs,
          limit: request.query.limit || 50,
          offset: request.query.offset || 0,
        }, request.id);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
