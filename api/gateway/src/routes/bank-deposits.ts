import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { errorResponseSchema } from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

export async function bankDepositRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  // POST /v1/bank-deposits — Create a new bank deposit session
  app.post<{
    Body: {
      currency: string;
      banking_partner_id?: string;
      account_type?: string;
      expected_amount: string;
      min_amount?: string;
      max_amount?: string;
      mismatch_policy?: string;
      settlement_pref?: string;
      idempotency_key?: string;
      ttl_seconds?: number;
    };
  }>(
    "/v1/bank-deposits",
    {
      schema: {
        tags: ["Bank Deposits"],
        summary: "Create a bank deposit session",
        operationId: "createBankDepositSession",
        body: {
          type: "object",
          properties: {
            currency: { type: "string", minLength: 1 },
            banking_partner_id: { type: "string" },
            account_type: { type: "string", enum: ["PERMANENT", "TEMPORARY"] },
            expected_amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            min_amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            max_amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            mismatch_policy: { type: "string", enum: ["ACCEPT", "REJECT"] },
            settlement_pref: { type: "string", enum: ["AUTO_CONVERT", "HOLD", "THRESHOLD"] },
            idempotency_key: { type: "string" },
            ttl_seconds: { type: "integer", minimum: 0 },
          },
          required: ["currency", "expected_amount"],
        },
        response: {
          201: {
            type: "object",
            properties: {
              session: { type: "object", additionalProperties: true },
            },
          },
          400: errorResponseSchema,
          409: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.createBankDepositSession({
          tenantId: tenantAuth.tenantId,
          currency: request.body.currency,
          bankingPartnerId: request.body.banking_partner_id,
          accountType: request.body.account_type,
          expectedAmount: request.body.expected_amount,
          minAmount: request.body.min_amount,
          maxAmount: request.body.max_amount,
          mismatchPolicy: request.body.mismatch_policy,
          settlementPref: request.body.settlement_pref,
          idempotencyKey: request.body.idempotency_key,
          ttlSeconds: request.body.ttl_seconds,
        }, request.id, request);
        return reply.status(201).send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/bank-deposits/:id — Get a bank deposit session by ID
  app.get<{
    Params: { id: string };
  }>(
    "/v1/bank-deposits/:id",
    {
      schema: {
        tags: ["Bank Deposits"],
        summary: "Get a bank deposit session",
        operationId: "getBankDepositSession",
        params: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
          },
          required: ["id"],
        },
        response: {
          200: {
            type: "object",
            properties: {
              session: { type: "object", additionalProperties: true },
            },
          },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getBankDepositSession({
          tenantId: tenantAuth.tenantId,
          sessionId: request.params.id,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/bank-deposits — List bank deposit sessions (cursor-based pagination)
  app.get<{
    Querystring: { page_size?: number; page_token?: string };
  }>(
    "/v1/bank-deposits",
    {
      schema: {
        tags: ["Bank Deposits"],
        summary: "List bank deposit sessions",
        operationId: "listBankDepositSessions",
        querystring: {
          type: "object",
          properties: {
            page_size: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            page_token: { type: "string" },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              sessions: { type: "array", items: { type: "object", additionalProperties: true } },
              next_page_token: { type: "string" },
              total_count: { type: "integer" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listBankDepositSessions({
          tenantId: tenantAuth.tenantId,
          pageSize: request.query.page_size,
          pageToken: request.query.page_token,
        }, request.id, request);
        return reply.send({
          sessions: result.sessions,
          next_page_token: result.nextPageToken || "",
          total_count: result.totalCount || 0,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/bank-deposits/:id/cancel — Cancel a pending bank deposit session
  app.post<{
    Params: { id: string };
  }>(
    "/v1/bank-deposits/:id/cancel",
    {
      schema: {
        tags: ["Bank Deposits"],
        summary: "Cancel a bank deposit session",
        operationId: "cancelBankDepositSession",
        params: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
          },
          required: ["id"],
        },
        response: {
          200: {
            type: "object",
            properties: {
              session: { type: "object", additionalProperties: true },
            },
          },
          404: errorResponseSchema,
          409: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.cancelBankDepositSession({
          tenantId: tenantAuth.tenantId,
          sessionId: request.params.id,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/bank-deposits/accounts — List virtual accounts for a tenant
  app.get<{
    Querystring: { page_size?: number; page_token?: string; limit?: number; offset?: number; currency?: string; account_type?: string };
  }>(
    "/v1/bank-deposits/accounts",
    {
      schema: {
        tags: ["Bank Deposits"],
        summary: "List virtual accounts",
        operationId: "listVirtualAccounts",
        querystring: {
          type: "object",
          properties: {
            page_size: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            page_token: { type: "string" },
            limit: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            offset: { type: "integer", minimum: 0, default: 0 },
            currency: { type: "string" },
            account_type: { type: "string", enum: ["PERMANENT", "TEMPORARY"] },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              accounts: { type: "array", items: { type: "object", additionalProperties: true } },
              total: { type: "integer" },
              nextPageToken: { type: "string" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listVirtualAccounts({
          tenantId: tenantAuth.tenantId,
          currency: request.query.currency,
          accountType: request.query.account_type,
          pageSize: request.query.page_size || request.query.limit,
          pageToken: request.query.page_token || "",
          limit: request.query.limit,
          offset: request.query.offset,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
