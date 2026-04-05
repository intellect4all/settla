import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  getRoutingOptionsBodySchema,
  routingOptionsResponseSchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

function mapScoreBreakdown(b: any): any {
  if (!b) return undefined;
  return {
    cost: b.cost,
    speed: b.speed,
    liquidity: b.liquidity,
    reliability: b.reliability,
  };
}

function mapRoutingOption(r: any): any {
  if (!r) return {};
  return {
    provider: r.provider,
    off_ramp_provider: r.offRampProvider,
    chain: r.chain,
    stablecoin: r.stablecoin,
    score: r.score,
    estimated_fee_usd: r.estimatedFeeUsd,
    estimated_settlement_seconds: r.estimatedSettlementSeconds,
    score_breakdown: mapScoreBreakdown(r.scoreBreakdown),
  };
}

export async function routeRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.post<{
    Body: {
      from_currency: string;
      to_currency: string;
      amount: string;
    };
  }>(
    "/v1/routes",
    {
      schema: {
        tags: ["Routes"],
        summary: "Get routing options",
        operationId: "getRoutingOptions",
        body: getRoutingOptionsBodySchema,
        response: {
          200: routingOptionsResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
          429: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getRoutingOptions({
          tenantId: tenantAuth.tenantId,
          fromCurrency: request.body.from_currency,
          toCurrency: request.body.to_currency,
          amount: request.body.amount,
        }, request.id, request);

        // quotedAt is a protobuf Timestamp {seconds, nanos} — convert to ISO string
        let quotedAt: string | undefined;
        if (result.quotedAt?.seconds) {
          quotedAt = new Date(Number(result.quotedAt.seconds) * 1000).toISOString();
        }

        return reply.send({
          routes: (result.routes || []).map(mapRoutingOption),
          quoted_at: quotedAt,
          valid_for_seconds: result.validForSeconds,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
