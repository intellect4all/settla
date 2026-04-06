import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  quoteResponseSchema,
  createQuoteBodySchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError, assertTenantMatch } from "../errors.js";

// In-memory per-tenant quote cache with 30-second TTL. Avoids re-evaluating
// all candidate routes for identical corridor+amount requests within the TTL
// window. Keyed by tenant_id:source:dest:amount_bucket.

const QUOTE_CACHE_TTL_MS = 30_000;
const QUOTE_CACHE_MAX_ENTRIES = 10_000;

interface QuoteCacheEntry {
  response: any;
  expiresAt: number;
}

const quoteCache = new Map<string, QuoteCacheEntry>();

/**
 * Bucket an amount string into a range so nearby amounts share a cache key.
 * - <1,000      → round to nearest 10
 * - <10,000     → round to nearest 100
 * - <100,000    → round to nearest 1,000
 * - >=100,000   → round to nearest 10,000
 */
function amountBucket(amountStr: string): string {
  const amount = Number(amountStr);
  if (!Number.isFinite(amount) || amount <= 0) return amountStr;

  let step: number;
  if (amount < 1_000) {
    step = 10;
  } else if (amount < 10_000) {
    step = 100;
  } else if (amount < 100_000) {
    step = 1_000;
  } else {
    step = 10_000;
  }
  return String(Math.round(amount / step) * step);
}

function buildQuoteCacheKey(
  tenantId: string,
  sourceCurrency: string,
  destCurrency: string,
  sourceAmount: string,
): string {
  return `${tenantId}:${sourceCurrency}:${destCurrency}:${amountBucket(sourceAmount)}`;
}

function getQuoteFromCache(key: string): any | undefined {
  const entry = quoteCache.get(key);
  if (!entry) return undefined;
  if (Date.now() > entry.expiresAt) {
    quoteCache.delete(key);
    return undefined;
  }
  return entry.response;
}

function putQuoteInCache(key: string, response: any): void {
  // Evict expired entries when approaching max size to prevent unbounded growth
  if (quoteCache.size >= QUOTE_CACHE_MAX_ENTRIES) {
    const now = Date.now();
    for (const [k, v] of quoteCache) {
      if (now > v.expiresAt) quoteCache.delete(k);
    }
    // If still over limit after expiry sweep, drop oldest 20%
    if (quoteCache.size >= QUOTE_CACHE_MAX_ENTRIES) {
      const toDelete = Math.floor(QUOTE_CACHE_MAX_ENTRIES * 0.2);
      let deleted = 0;
      for (const k of quoteCache.keys()) {
        if (deleted >= toDelete) break;
        quoteCache.delete(k);
        deleted++;
      }
    }
  }
  quoteCache.set(key, {
    response,
    expiresAt: Date.now() + QUOTE_CACHE_TTL_MS,
  });
}

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

  // Quotes are intentionally not idempotency-protected. They are stateless
  // price lookups that don't mutate state — each call returns an ephemeral,
  // cached quote with a short TTL. No side effects are created.
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
        tags: ["Quotes"],
        summary: "Create a quote",
        operationId: "createQuote",
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

      // Check quote cache — skip route evaluation for identical corridor+amount
      const cacheKey = buildQuoteCacheKey(
        tenantAuth.tenantId,
        request.body.source_currency,
        request.body.dest_currency,
        request.body.source_amount,
      );
      const cached = getQuoteFromCache(cacheKey);
      if (cached) {
        return reply.status(201).send(cached);
      }

      try {
        const result = await grpc.createQuote({
          tenantId: tenantAuth.tenantId,
          sourceCurrency: request.body.source_currency,
          sourceAmount: request.body.source_amount,
          destCurrency: request.body.dest_currency,
          destCountry: request.body.dest_country,
        }, request.id, request);
        assertTenantMatch(tenantAuth.tenantId, result.quote?.tenantId, 'quote');
        const mapped = mapQuote(result.quote);
        putQuoteInCache(cacheKey, mapped);
        return reply.status(201).send(mapped);
      } catch (err) {
        // Never cache error responses
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
        tags: ["Quotes"],
        summary: "Get a quote by ID",
        operationId: "getQuote",
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
        }, request.id, request);
        assertTenantMatch(tenantAuth.tenantId, result.quote?.tenantId, 'quote');
        return reply.send(mapQuote(result.quote));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}
