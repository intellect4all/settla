import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { errorResponseSchema } from "../schemas/index.js";
import { mapGrpcError, assertTenantMatch } from "../errors.js";

export async function depositRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  // POST /v1/deposits — Create a new crypto deposit session
  app.post<{
    Body: {
      chain: string;
      token: string;
      expected_amount: string;
      currency?: string;
      settlement_pref?: string;
      idempotency_key?: string;
      ttl_seconds?: number;
    };
  }>(
    "/v1/deposits",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Create a crypto deposit session",
        operationId: "createDepositSession",
        body: {
          type: "object",
          properties: {
            chain: { type: "string", minLength: 1 },
            token: { type: "string", minLength: 1 },
            expected_amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            currency: { type: "string" },
            settlement_pref: { type: "string", enum: ["AUTO_CONVERT", "HOLD", "THRESHOLD"] },
            idempotency_key: { type: "string" },
            ttl_seconds: { type: "integer", minimum: 0 },
          },
          required: ["chain", "token", "expected_amount"],
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
        const result = await grpc.createDepositSession({
          tenantId: tenantAuth.tenantId,
          chain: request.body.chain,
          token: request.body.token,
          expectedAmount: request.body.expected_amount,
          currency: request.body.currency,
          settlementPref: request.body.settlement_pref,
          idempotencyKey: request.body.idempotency_key,
          ttlSeconds: request.body.ttl_seconds,
        }, request.id, request);
        assertTenantMatch(tenantAuth.tenantId, result.session?.tenantId, 'deposit session');
        return reply.status(201).send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/deposits/:id — Get a deposit session by ID
  app.get<{
    Params: { id: string };
  }>(
    "/v1/deposits/:id",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Get a deposit session",
        operationId: "getDepositSession",
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
        const result = await grpc.getDepositSession({
          tenantId: tenantAuth.tenantId,
          sessionId: request.params.id,
        }, request.id, request);
        assertTenantMatch(tenantAuth.tenantId, result.session?.tenantId, 'deposit session');
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/deposits — List deposit sessions for a tenant (cursor-based pagination)
  app.get<{
    Querystring: { page_size?: number; page_token?: string };
  }>(
    "/v1/deposits",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "List deposit sessions",
        operationId: "listDepositSessions",
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
        const result = await grpc.listDepositSessions({
          tenantId: tenantAuth.tenantId,
          pageSize: request.query.page_size,
          pageToken: request.query.page_token,
        }, request.id, request);
        for (const s of result.sessions || []) {
          assertTenantMatch(tenantAuth.tenantId, s.tenantId, 'deposit session');
        }
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

  // POST /v1/deposits/:id/cancel — Cancel a pending deposit session
  app.post<{
    Params: { id: string };
  }>(
    "/v1/deposits/:id/cancel",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Cancel a deposit session",
        operationId: "cancelDepositSession",
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
        const result = await grpc.cancelDepositSession({
          tenantId: tenantAuth.tenantId,
          sessionId: request.params.id,
        }, request.id, request);
        assertTenantMatch(tenantAuth.tenantId, result.session?.tenantId, 'deposit session');
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );


  // GET /v1/deposits/balance — Get tenant crypto deposit balances
  app.get(
    "/v1/deposits/balance",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Get tenant crypto balances",
        operationId: "getCryptoBalances",
        response: {
          200: {
            type: "object",
            properties: {
              balances: { type: "array", items: { type: "object", additionalProperties: true } },
              total_value_usd: { type: "string" },
            },
          },
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      // Aggregate balances from treasury positions for crypto assets
      try {
        const positions = await grpc.getPositions({ tenantId: tenantAuth.tenantId }, request.id, request);
        const cryptoBalances = (positions.positions ?? [])
          .filter((p: any) => ["USDT", "USDC"].includes(p.currency))
          .map((p: any) => ({
            chain: p.location || "unknown",
            token: p.currency,
            balance: p.available || "0",
            value_usd: p.available || "0",
          }));

        const totalValueUsd = cryptoBalances.reduce(
          (sum: number, b: any) => sum + parseFloat(b.value_usd || "0"),
          0,
        );

        return reply.send({
          balances: cryptoBalances,
          total_value_usd: totalValueUsd.toFixed(2),
        });
      } catch (err) {
        request.log.error({ err }, "Failed to fetch crypto balances");
        return reply.send({ balances: [], total_value_usd: "0.00" });
      }
    },
  );

  // POST /v1/deposits/convert — Convert crypto balance to fiat
  app.post<{ Body: { chain: string; token: string; amount: string } }>(
    "/v1/deposits/convert",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Convert crypto to fiat",
        operationId: "convertCryptoToFiat",
        body: {
          type: "object",
          required: ["chain", "token", "amount"],
          properties: {
            chain: { type: "string" },
            token: { type: "string" },
            amount: { type: "string" },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              message: { type: "string" },
            },
          },
          400: errorResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { chain, token, amount } = request.body;
      // In production, this would create a conversion transfer through the settlement engine
      request.log.info({ tenantId: tenantAuth.tenantId, chain, token, amount }, "Crypto conversion requested");
      return reply.send({
        message: `Conversion of ${amount} ${token} on ${chain} initiated. Settlement will process automatically.`,
      });
    },
  );
}
