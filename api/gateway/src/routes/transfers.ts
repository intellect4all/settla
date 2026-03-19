import type { FastifyInstance } from "fastify";
import type { Redis } from "ioredis";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  transferResponseSchema,
  createTransferBodySchema,
  listTransfersResponseSchema,
  listTransfersQuerySchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError, assertTenantMatch } from "../errors.js";

/** TTL for idempotency cache entries (default 2 hours, configurable via env). */
const IDEMPOTENCY_CACHE_TTL_SECONDS = Number(process.env.SETTLA_IDEMPOTENCY_TTL_SECONDS) || 7200;

function mapTransfer(t: any): any {
  if (!t) return {};
  return {
    id: t.id,
    tenant_id: t.tenantId,
    external_ref: t.externalRef,
    idempotency_key: t.idempotencyKey,
    status: t.status,
    version: t.version,
    source_currency: t.sourceCurrency,
    source_amount: t.sourceAmount,
    dest_currency: t.destCurrency,
    dest_amount: t.destAmount,
    stable_coin: t.stableCoin,
    stable_amount: t.stableAmount,
    chain: t.chain,
    fx_rate: t.fxRate,
    fees: t.fees
      ? {
          on_ramp_fee: t.fees.onRampFee,
          network_fee: t.fees.networkFee,
          off_ramp_fee: t.fees.offRampFee,
          total_fee_usd: t.fees.totalFeeUsd,
        }
      : undefined,
    sender: t.sender
      ? {
          id: t.sender.id,
          name: t.sender.name,
          email: t.sender.email,
          country: t.sender.country,
        }
      : undefined,
    recipient: t.recipient
      ? {
          name: t.recipient.name,
          account_number: t.recipient.accountNumber,
          sort_code: t.recipient.sortCode,
          bank_name: t.recipient.bankName,
          country: t.recipient.country,
          iban: t.recipient.iban,
        }
      : undefined,
    quote_id: t.quoteId,
    blockchain_transactions: (t.blockchainTransactions || []).map((tx: any) => ({
      chain: tx.chain,
      type: tx.type,
      tx_hash: tx.txHash,
      explorer_url: tx.explorerUrl,
      status: tx.status,
    })),
    created_at: t.createdAt,
    updated_at: t.updatedAt,
    funded_at: t.fundedAt,
    completed_at: t.completedAt,
    failed_at: t.failedAt,
    failure_reason: t.failureReason,
    failure_code: t.failureCode,
  };
}

export async function transferRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient; redis?: Redis | null },
): Promise<void> {
  const { grpc, redis } = opts;

  app.post<{
    Body: {
      idempotency_key: string;
      external_ref?: string;
      source_currency: string;
      source_amount: string;
      dest_currency: string;
      sender?: {
        id?: string;
        name?: string;
        email?: string;
        country?: string;
      };
      recipient: {
        name: string;
        account_number?: string;
        sort_code?: string;
        bank_name?: string;
        country: string;
        iban?: string;
      };
      quote_id?: string;
    };
  }>(
    "/v1/transfers",
    {
      schema: {
        tags: ["Transfers"],
        summary: "Create a transfer",
        operationId: "createTransfer",
        body: createTransferBodySchema,
        response: {
          201: transferResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
          409: errorResponseSchema,
          429: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const body = request.body;

      // sort_code and iban are mutually exclusive — a recipient uses one scheme or the other.
      if (body.recipient.sort_code && body.recipient.iban) {
        return reply.status(400).send({
          error: "BAD_REQUEST",
          code: "BAD_REQUEST",
          message: "sort_code and iban are mutually exclusive",
          request_id: request.id,
        });
      }

      // Amount range validation
      const amount = Number(body.source_amount);
      if (Number.isNaN(amount) || amount < 0.01 || amount > 10_000_000) {
        return reply.status(400).send({
          error: "BAD_REQUEST",
          code: "AMOUNT_OUT_OF_RANGE",
          message: "source_amount must be between 0.01 and 10,000,000",
          request_id: request.id,
        });
      }

      // UK recipient validation — GB requires sort_code or iban
      if (
        body.recipient.country === "GB" &&
        !body.recipient.sort_code &&
        !body.recipient.iban
      ) {
        return reply.status(400).send({
          error: "BAD_REQUEST",
          code: "MISSING_PAYMENT_DETAILS",
          message: "GB recipients require either sort_code or iban",
          request_id: request.id,
        });
      }

      // Redis idempotency cache — return cached response on duplicate
      const idempKey = `idem:${tenantAuth.tenantId}:${body.idempotency_key}`;
      if (redis) {
        try {
          const cached = await redis.get(idempKey);
          if (cached) {
            request.log.info(
              { idempotency_key: body.idempotency_key },
              "transfer: returning cached idempotent response",
            );
            return reply.status(201).send(JSON.parse(cached));
          }
        } catch {
          // Redis unavailable — proceed without cache
        }
      }

      try {
        const result = await grpc.createTransfer({
          tenantId: tenantAuth.tenantId,
          idempotencyKey: body.idempotency_key,
          externalRef: body.external_ref,
          sourceCurrency: body.source_currency,
          sourceAmount: body.source_amount,
          destCurrency: body.dest_currency,
          sender: body.sender,
          recipient: body.recipient,
          quoteId: body.quote_id,
        }, request.id);
        assertTenantMatch(tenantAuth.tenantId, result.transfer?.tenantId, 'transfer');
        const mapped = mapTransfer(result.transfer);

        // Cache successful response for idempotency (1hr TTL)
        if (redis) {
          try {
            await redis.set(idempKey, JSON.stringify(mapped), "EX", IDEMPOTENCY_CACHE_TTL_SECONDS);
          } catch {
            // Best-effort cache write
          }
        }

        return reply.status(201).send(mapped);
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { transferId: string };
  }>(
    "/v1/transfers/:transferId",
    {
      schema: {
        tags: ["Transfers"],
        summary: "Get a transfer by ID",
        operationId: "getTransfer",
        params: {
          type: "object",
          properties: { transferId: { type: "string", format: "uuid" } },
          required: ["transferId"],
        },
        response: {
          200: transferResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getTransfer({
          tenantId: tenantAuth.tenantId,
          transferId: request.params.transferId,
        }, request.id);
        assertTenantMatch(tenantAuth.tenantId, result.transfer?.tenantId, 'transfer');
        return reply.send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Querystring: { page_size?: number; page_token?: string; status?: string; search?: string };
  }>(
    "/v1/transfers",
    {
      schema: {
        tags: ["Transfers"],
        summary: "List transfers",
        operationId: "listTransfers",
        querystring: {
          ...listTransfersQuerySchema,
          properties: {
            ...(listTransfersQuerySchema as any).properties,
            status: { type: "string", description: "Filter by exact status (e.g. COMPLETED, FAILED)" },
            search: { type: "string", description: "Substring search on id, external_ref, or idempotency_key" },
          },
        },
        response: {
          200: listTransfersResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listTransfers({
          tenantId: tenantAuth.tenantId,
          pageSize: request.query.page_size,
          pageToken: request.query.page_token,
          statusFilter: request.query.status || "",
          searchQuery: request.query.search || "",
        }, request.id);
        for (const t of result.transfers || []) {
          assertTenantMatch(tenantAuth.tenantId, t.tenantId, 'transfer');
        }
        return reply.send({
          transfers: (result.transfers || []).map(mapTransfer),
          next_page_token: result.nextPageToken,
          total_count: result.totalCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{ Params: { transferId: string } }>(
    "/v1/transfers/:transferId/events",
    {
      schema: {
        tags: ["Transfers"],
        summary: "Get transfer events",
        operationId: "getTransferEvents",
        params: {
          type: "object",
          required: ["transferId"],
          properties: { transferId: { type: "string", format: "uuid" } },
        },
        response: {
          200: {
            type: "object",
            properties: {
              events: { type: "array", items: { type: "object", additionalProperties: true } },
            },
          },
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listTransferEvents(
          { tenantId: tenantAuth.tenantId, transferId: request.params.transferId },
          request.id,
        );
        return reply.send({ events: result.events ?? [] });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.post<{
    Params: { transferId: string };
    Body: { reason?: string };
  }>(
    "/v1/transfers/:transferId/cancel",
    {
      schema: {
        tags: ["Transfers"],
        summary: "Cancel a transfer",
        operationId: "cancelTransfer",
        params: {
          type: "object",
          properties: { transferId: { type: "string", format: "uuid" } },
          required: ["transferId"],
        },
        body: {
          type: "object",
          properties: { reason: { type: "string" } },
        },
        response: {
          200: transferResponseSchema,
          400: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.cancelTransfer({
          tenantId: tenantAuth.tenantId,
          transferId: request.params.transferId,
          reason: request.body.reason,
        }, request.id);
        assertTenantMatch(tenantAuth.tenantId, result.transfer?.tenantId, 'transfer');
        return reply.send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
