import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { errorResponseSchema } from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

export async function paymentLinkRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;


  // POST /v1/payment-links — Create a new payment link
  app.post<{
    Body: {
      description?: string;
      redirect_url?: string;
      use_limit?: number;
      expires_at_unix?: number;
      amount: string;
      currency: string;
      chain: string;
      token: string;
      settlement_pref?: string;
      ttl_seconds?: number;
    };
  }>(
    "/v1/payment-links",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "Create a payment link",
        operationId: "createPaymentLink",
        body: {
          type: "object",
          properties: {
            description: { type: "string" },
            redirect_url: { type: "string" },
            use_limit: { type: "integer", minimum: 1 },
            expires_at_unix: { type: "integer" },
            amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            currency: { type: "string", minLength: 1 },
            chain: { type: "string", minLength: 1 },
            token: { type: "string", minLength: 1 },
            settlement_pref: { type: "string", enum: ["AUTO_CONVERT", "HOLD", "THRESHOLD"] },
            ttl_seconds: { type: "integer", minimum: 0 },
          },
          required: ["amount", "currency", "chain", "token"],
        },
        response: {
          201: {
            type: "object",
            properties: {
              link: { type: "object", additionalProperties: true },
            },
          },
          400: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.createPaymentLink({
          tenantId: tenantAuth.tenantId,
          description: request.body.description ?? "",
          redirectUrl: request.body.redirect_url ?? "",
          useLimit: request.body.use_limit ?? 0,
          expiresAtUnix: request.body.expires_at_unix ?? 0,
          amount: request.body.amount,
          currency: request.body.currency,
          chain: request.body.chain,
          token: request.body.token,
          settlementPref: request.body.settlement_pref ?? "",
          ttlSeconds: request.body.ttl_seconds ?? 0,
        }, request.id, request);
        return reply.status(201).send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/payment-links — List payment links (paginated)
  app.get<{
    Querystring: { page_size?: number; page_token?: string; limit?: number; offset?: number };
  }>(
    "/v1/payment-links",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "List payment links",
        operationId: "listPaymentLinks",
        querystring: {
          type: "object",
          properties: {
            page_size: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            page_token: { type: "string" },
            limit: { type: "integer", minimum: 1, maximum: 100, default: 20 },
            offset: { type: "integer", minimum: 0, default: 0 },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              links: { type: "array", items: { type: "object", additionalProperties: true } },
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
        const result = await grpc.listPaymentLinks({
          tenantId: tenantAuth.tenantId,
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

  // GET /v1/payment-links/:id — Get a payment link by ID
  app.get<{
    Params: { id: string };
  }>(
    "/v1/payment-links/:id",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "Get a payment link",
        operationId: "getPaymentLink",
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
              link: { type: "object", additionalProperties: true },
            },
          },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getPaymentLink({
          tenantId: tenantAuth.tenantId,
          linkId: request.params.id,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // DELETE /v1/payment-links/:id — Disable a payment link
  app.delete<{
    Params: { id: string };
  }>(
    "/v1/payment-links/:id",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "Disable a payment link",
        operationId: "disablePaymentLink",
        params: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
          },
          required: ["id"],
        },
        response: {
          204: { type: "null" },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        await grpc.disablePaymentLink({
          tenantId: tenantAuth.tenantId,
          linkId: request.params.id,
        }, request.id, request);
        return reply.status(204).send();
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );


  // GET /v1/payment-links/resolve/:code — Resolve a payment link by short code
  app.get<{
    Params: { code: string };
  }>(
    "/v1/payment-links/resolve/:code",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "Resolve a payment link by code",
        operationId: "resolvePaymentLink",
        params: {
          type: "object",
          properties: {
            code: { type: "string", minLength: 1 },
          },
          required: ["code"],
        },
        response: {
          200: {
            type: "object",
            properties: {
              link: { type: "object", additionalProperties: true },
            },
          },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.resolvePaymentLink({
          shortCode: request.params.code,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // POST /v1/payment-links/redeem/:code — Redeem a payment link (create deposit session)
  app.post<{
    Params: { code: string };
  }>(
    "/v1/payment-links/redeem/:code",
    {
      schema: {
        tags: ["Payment Links"],
        summary: "Redeem a payment link",
        operationId: "redeemPaymentLink",
        params: {
          type: "object",
          properties: {
            code: { type: "string", minLength: 1 },
          },
          required: ["code"],
        },
        response: {
          201: {
            type: "object",
            properties: {
              session: { type: "object", additionalProperties: true },
              link: { type: "object", additionalProperties: true },
            },
          },
          404: errorResponseSchema,
          409: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.redeemPaymentLink({
          shortCode: request.params.code,
        }, request.id, request);
        return reply.status(201).send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/deposits/:id/public-status — Get deposit session public status (limited fields)
  app.get<{
    Params: { id: string };
  }>(
    "/v1/deposits/:id/public-status",
    {
      schema: {
        tags: ["Crypto Deposits"],
        summary: "Get deposit public status",
        operationId: "getDepositPublicStatus",
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
              id: { type: "string" },
              status: { type: "string" },
              chain: { type: "string" },
              token: { type: "string" },
              deposit_address: { type: "string" },
              expected_amount: { type: "string" },
              received_amount: { type: "string" },
              expires_at: { type: "string" },
            },
          },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      try {
        // Use a special gRPC call that doesn't require tenant_id
        // For now, we pass a nil tenant and the gRPC service handles it
        const result = await grpc.getDepositPublicStatus({
          sessionId: request.params.id,
        }, request.id, request);
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
