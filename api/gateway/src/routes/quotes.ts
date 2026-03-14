import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  quoteResponseSchema,
  createQuoteBodySchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

function mapQuote(q: any): any {
  if (!q) return {};
  return {
    id: q.id,
    tenant_id: q.tenantId,
    source_currency: q.sourceCurrency,
    source_amount: q.sourceAmount,
    dest_currency: q.destCurrency,
    dest_amount: q.destAmount,
    fx_rate: q.fxRate,
    fees: q.fees
      ? {
          on_ramp_fee: q.fees.onRampFee,
          network_fee: q.fees.networkFee,
          off_ramp_fee: q.fees.offRampFee,
          total_fee_usd: q.fees.totalFeeUsd,
        }
      : undefined,
    route: q.route
      ? {
          chain: q.route.chain,
          stable_coin: q.route.stableCoin,
          estimated_time_min: q.route.estimatedTimeMin,
          on_ramp_provider: q.route.onRampProvider,
          off_ramp_provider: q.route.offRampProvider,
          explorer_url: q.route.explorerUrl,
        }
      : undefined,
    expires_at: q.expiresAt,
    created_at: q.createdAt,
  };
}

export async function quoteRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.post<{
    Body: {
      source_currency: string;
      source_amount: string;
      dest_currency: string;
      dest_country?: string;
    };
  }>(
    "/v1/quotes",
    {
      schema: {
        body: createQuoteBodySchema,
        response: {
          201: quoteResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
          429: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.createQuote({
          tenantId: tenantAuth.tenantId,
          sourceCurrency: request.body.source_currency,
          sourceAmount: request.body.source_amount,
          destCurrency: request.body.dest_currency,
          destCountry: request.body.dest_country,
        }, request.id);
        return reply.status(201).send(mapQuote(result.quote));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { quoteId: string };
  }>(
    "/v1/quotes/:quoteId",
    {
      schema: {
        params: {
          type: "object",
          properties: { quoteId: { type: "string", format: "uuid" } },
          required: ["quoteId"],
        },
        response: {
          200: quoteResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getQuote({
          tenantId: tenantAuth.tenantId,
          quoteId: request.params.quoteId,
        }, request.id);
        return reply.send(mapQuote(result.quote));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
