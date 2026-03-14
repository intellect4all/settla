import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  transferResponseSchema,
  createTransferBodySchema,
  listTransfersResponseSchema,
  listTransfersQuerySchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

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
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

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
        return reply.status(201).send(mapTransfer(result.transfer));
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
        return reply.send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Querystring: { page_size?: number; page_token?: string };
  }>(
    "/v1/transfers",
    {
      schema: {
        querystring: listTransfersQuerySchema,
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
        }, request.id);
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

  app.post<{
    Params: { transferId: string };
    Body: { reason?: string };
  }>(
    "/v1/transfers/:transferId/cancel",
    {
      schema: {
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
        return reply.send(mapTransfer(result.transfer));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
