import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { errorResponseSchema } from "../schemas/index.js";
import { mapGrpcError, assertTenantMatch } from "../errors.js";

export async function ledgerRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  // GET /v1/accounts — List ledger accounts for the tenant
  app.get(
    "/v1/accounts",
    {
      schema: {
        tags: ["Ledger"],
        summary: "List ledger accounts",
        operationId: "listAccounts",
        querystring: {
          type: "object",
          properties: {
            page_size: { type: "string" },
            page_token: { type: "string" },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              accounts: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    id: { type: "string" },
                    tenantId: { type: "string" },
                    code: { type: "string" },
                    name: { type: "string" },
                    type: { type: "string" },
                    currency: { type: "string" },
                    isActive: { type: "boolean" },
                    createdAt: { type: "string" },
                    updatedAt: { type: "string" },
                  },
                },
              },
              nextPageToken: { type: "string" },
              totalCount: { type: "number" },
            },
          },
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { page_size, page_token } = request.query as {
        page_size?: string;
        page_token?: string;
      };
      try {
        const result = await grpc.getAccounts(
          {
            tenantId: tenantAuth.tenantId,
            pageSize: page_size ? Number(page_size) : undefined,
            pageToken: page_token,
          },
          request.id,
        );
        for (const a of result.accounts || []) {
          assertTenantMatch(tenantAuth.tenantId, a.tenantId, 'account');
        }
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/accounts/:code/balance — Get account balance
  app.get<{ Params: { code: string } }>(
    "/v1/accounts/:code/balance",
    {
      schema: {
        tags: ["Ledger"],
        summary: "Get account balance",
        operationId: "getAccountBalance",
        params: {
          type: "object",
          required: ["code"],
          properties: {
            code: { type: "string", minLength: 1 },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              accountBalance: {
                type: "object",
                properties: {
                  accountCode: { type: "string" },
                  balance: { type: "string" },
                  currency: { type: "string" },
                },
              },
            },
          },
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { code } = request.params;
      const accountCode = decodeURIComponent(code);
      // Validate account code format: alphanumeric, colons, dots, hyphens, underscores only
      if (!/^[a-zA-Z0-9:._-]+$/.test(accountCode)) {
        return reply.status(400).send({ error: "INVALID_ACCOUNT_CODE", message: "Account code contains invalid characters" });
      }
      try {
        const result = await grpc.getAccountBalance(
          {
            tenantId: tenantAuth.tenantId,
            accountCode,
          },
          request.id,
        );
        return reply.send(result);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // GET /v1/accounts/:code/transactions — List account transactions
  app.get<{
    Params: { code: string };
    Querystring: {
      from?: string;
      to?: string;
      page_size?: string;
      page_token?: string;
    };
  }>(
    "/v1/accounts/:code/transactions",
    {
      schema: {
        tags: ["Ledger"],
        summary: "List account transactions",
        operationId: "listAccountTransactions",
        params: {
          type: "object",
          required: ["code"],
          properties: {
            code: { type: "string", minLength: 1 },
          },
        },
        querystring: {
          type: "object",
          properties: {
            from: { type: "string" },
            to: { type: "string" },
            page_size: { type: "string" },
            page_token: { type: "string" },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              entries: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    id: { type: "string" },
                    accountId: { type: "string" },
                    accountCode: { type: "string" },
                    entryType: { type: "string" },
                    amount: { type: "string" },
                    currency: { type: "string" },
                    description: { type: "string" },
                  },
                },
              },
              nextPageToken: { type: "string" },
              totalCount: { type: "number" },
            },
          },
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { code } = request.params;
      const accountCode = decodeURIComponent(code);
      // Validate account code format: alphanumeric, colons, dots, hyphens, underscores only
      if (!/^[a-zA-Z0-9:._-]+$/.test(accountCode)) {
        return reply.status(400).send({ error: "INVALID_ACCOUNT_CODE", message: "Account code contains invalid characters" });
      }
      const { from, to, page_size, page_token } = request.query;
      try {
        const result = await grpc.getTransactions(
          {
            tenantId: tenantAuth.tenantId,
            accountCode,
            from: from
              ? { seconds: Math.floor(new Date(from).getTime() / 1000) }
              : undefined,
            to: to
              ? { seconds: Math.floor(new Date(to).getTime() / 1000) }
              : undefined,
            pageSize: page_size ? Number(page_size) : undefined,
            pageToken: page_token,
          },
          request.id,
        );
        return reply.send({
          entries: result.entries ?? [],
          nextPageToken: result.nextPageToken,
          totalCount: result.totalCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
