import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  tenantProfileResponseSchema,
  webhookConfigBodySchema,
  webhookConfigResponseSchema,
  apiKeyInfoSchema,
  apiKeyListResponseSchema,
  createApiKeyBodySchema,
  createApiKeyResponseSchema,
  dashboardMetricsResponseSchema,
  transferStatsQuerySchema,
  transferStatsResponseSchema,
  feeReportQuerySchema,
  feeReportResponseSchema,
  analyticsPeriodQuerySchema,
  statusDistributionResponseSchema,
  corridorMetricsResponseSchema,
  latencyPercentilesResponseSchema,
  volumeComparisonQuerySchema,
  volumeComparisonResponseSchema,
  recentActivityQuerySchema,
  recentActivityResponseSchema,
  webhookDeliveriesQuerySchema,
  webhookDeliveriesResponseSchema,
  webhookDeliveryDetailResponseSchema,
  webhookDeliveryStatsQuerySchema,
  webhookDeliveryStatsResponseSchema,
  webhookEventSubscriptionsResponseSchema,
  updateWebhookEventSubscriptionsBodySchema,
  testWebhookResponseSchema,
  errorResponseSchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

function mapTenantProfile(t: any): any {
  if (!t) return {};
  return {
    id: t.id,
    name: t.name,
    slug: t.slug,
    status: t.status,
    settlement_model: t.settlementModel,
    kyb_status: t.kybStatus,
    kyb_verified_at: t.kybVerifiedAt,
    fee_schedule: t.feeSchedule
      ? {
          on_ramp_bps: t.feeSchedule.onRampBps,
          off_ramp_bps: t.feeSchedule.offRampBps,
          min_fee_usd: t.feeSchedule.minFeeUsd,
          max_fee_usd: t.feeSchedule.maxFeeUsd,
        }
      : undefined,
    daily_limit_usd: t.dailyLimitUsd,
    per_transfer_limit: t.perTransferLimit,
    webhook_url: t.webhookUrl,
    created_at: t.createdAt,
    updated_at: t.updatedAt,
  };
}

function mapApiKey(k: any): any {
  if (!k) return {};
  return {
    id: k.id,
    key_prefix: k.keyPrefix,
    environment: k.environment,
    name: k.name,
    is_active: k.isActive,
    last_used_at: k.lastUsedAt,
    expires_at: k.expiresAt,
    created_at: k.createdAt,
  };
}

export async function tenantPortalRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  // ── GET /v1/me — tenant profile ────────────────────────────────────────
  app.get(
    "/v1/me",
    {
      schema: {
        response: {
          200: tenantProfileResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getMyTenant({ tenantId: tenantAuth.tenantId }, request.id);
        return reply.send(mapTenantProfile(result.tenant));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── PUT /v1/me/webhooks — update webhook config ────────────────────────
  app.put<{
    Body: { webhook_url: string };
  }>(
    "/v1/me/webhooks",
    {
      schema: {
        body: webhookConfigBodySchema,
        response: {
          200: webhookConfigResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.updateWebhookConfig({
          tenantId: tenantAuth.tenantId,
          webhookUrl: request.body.webhook_url,
        }, request.id);
        return reply.send({
          webhook_url: result.webhookUrl,
          webhook_secret: result.webhookSecret,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/api-keys — list API keys ────────────────────────────────
  app.get(
    "/v1/me/api-keys",
    {
      schema: {
        response: {
          200: apiKeyListResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listAPIKeys({ tenantId: tenantAuth.tenantId }, request.id);
        return reply.send({
          keys: (result.keys || []).map(mapApiKey),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── POST /v1/me/api-keys — create new API key ─────────────────────────
  app.post<{
    Body: { environment: string; name?: string };
  }>(
    "/v1/me/api-keys",
    {
      schema: {
        body: createApiKeyBodySchema,
        response: {
          201: createApiKeyResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.createAPIKey({
          tenantId: tenantAuth.tenantId,
          environment: request.body.environment,
          name: request.body.name,
        }, request.id);
        return reply.status(201).send({
          key: mapApiKey(result.key),
          raw_key: result.rawKey,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── DELETE /v1/me/api-keys/:keyId — revoke API key ─────────────────────
  app.delete<{
    Params: { keyId: string };
  }>(
    "/v1/me/api-keys/:keyId",
    {
      schema: {
        params: {
          type: "object",
          properties: { keyId: { type: "string", format: "uuid" } },
          required: ["keyId"],
        },
        response: {
          204: { type: "null" },
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.revokeAPIKey({
          tenantId: tenantAuth.tenantId,
          keyId: request.params.keyId,
        }, request.id);

        // SEC-2: immediately invalidate the revoked key from L1+L2 auth caches
        // and broadcast to peer gateways via Redis pub/sub.
        // result.keyHash is the SHA-256 hex of the revoked raw key, returned by
        // the Go server after looking it up from the DB before deactivation.
        if (result?.keyHash) {
          await app.invalidateAuthCache(result.keyHash).catch((err: unknown) => {
            request.log.warn(
              { err, keyId: request.params.keyId },
              "Failed to invalidate auth cache after revoke — key may remain valid until TTL expires",
            );
          });
        }

        return reply.status(204).send();
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── POST /v1/me/api-keys/:keyId/rotate — rotate API key ───────────────
  app.post<{
    Params: { keyId: string };
    Body: { name?: string };
  }>(
    "/v1/me/api-keys/:keyId/rotate",
    {
      schema: {
        params: {
          type: "object",
          properties: { keyId: { type: "string", format: "uuid" } },
          required: ["keyId"],
        },
        body: {
          type: "object",
          properties: { name: { type: "string", maxLength: 255 } },
        },
        response: {
          200: createApiKeyResponseSchema,
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.rotateAPIKey({
          tenantId: tenantAuth.tenantId,
          oldKeyId: request.params.keyId,
          name: request.body.name,
        }, request.id);
        return reply.send({
          key: mapApiKey(result.key),
          raw_key: result.rawKey,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/dashboard — dashboard metrics ──────────────────────────
  app.get(
    "/v1/me/dashboard",
    {
      schema: {
        response: {
          200: dashboardMetricsResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getDashboardMetrics({
          tenantId: tenantAuth.tenantId,
        }, request.id);
        return reply.send({
          transfers_today: result.transfersToday,
          volume_today_usd: result.volumeTodayUsd,
          completed_today: result.completedToday,
          failed_today: result.failedToday,
          transfers_7d: result.transfers7d,
          volume_7d_usd: result.volume7dUsd,
          fees_7d_usd: result.fees7dUsd,
          transfers_30d: result.transfers30d,
          volume_30d_usd: result.volume30dUsd,
          fees_30d_usd: result.fees30dUsd,
          success_rate_30d: result.successRate30d,
          daily_limit_usd: result.dailyLimitUsd,
          daily_usage_usd: result.dailyUsageUsd,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/transfers/stats — transfer time-series ─────────────────
  app.get<{
    Querystring: { period?: string; granularity?: string };
  }>(
    "/v1/me/transfers/stats",
    {
      schema: {
        querystring: transferStatsQuerySchema,
        response: {
          200: transferStatsResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getTransferStats({
          tenantId: tenantAuth.tenantId,
          period: request.query.period || "24h",
          granularity: request.query.granularity || "hour",
        }, request.id);
        return reply.send({
          buckets: (result.buckets || []).map((b: any) => ({
            timestamp: b.timestamp,
            total: b.total,
            completed: b.completed,
            failed: b.failed,
            volume_usd: b.volumeUsd,
            fees_usd: b.feesUsd,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/fees/report — fee breakdown ────────────────────────────
  app.get<{
    Querystring: { from?: string; to?: string };
  }>(
    "/v1/me/fees/report",
    {
      schema: {
        querystring: feeReportQuerySchema,
        response: {
          200: feeReportResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getFeeReport({
          tenantId: tenantAuth.tenantId,
          from: request.query.from
            ? { seconds: Math.floor(new Date(request.query.from).getTime() / 1000) }
            : undefined,
          to: request.query.to
            ? { seconds: Math.floor(new Date(request.query.to).getTime() / 1000) }
            : undefined,
        }, request.id);
        return reply.send({
          entries: (result.entries || []).map((e: any) => ({
            source_currency: e.sourceCurrency,
            dest_currency: e.destCurrency,
            transfer_count: e.transferCount,
            total_volume_usd: e.totalVolumeUsd,
            on_ramp_fees_usd: e.onRampFeesUsd,
            off_ramp_fees_usd: e.offRampFeesUsd,
            network_fees_usd: e.networkFeesUsd,
            total_fees_usd: e.totalFeesUsd,
          })),
          total_fees_usd: result.totalFeesUsd,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/analytics/status-distribution ────────────────────────
  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/status-distribution",
    {
      schema: {
        querystring: analyticsPeriodQuerySchema,
        response: { 200: statusDistributionResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getTransferStatusDistribution({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id);
        return reply.send({
          statuses: (result.statuses || []).map((s: any) => ({
            status: s.status,
            count: s.count,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/analytics/corridors ─────────────────────────────────
  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/corridors",
    {
      schema: {
        querystring: analyticsPeriodQuerySchema,
        response: { 200: corridorMetricsResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getCorridorMetrics({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id);
        return reply.send({
          corridors: (result.corridors || []).map((c: any) => ({
            source_currency: c.sourceCurrency,
            dest_currency: c.destCurrency,
            transfer_count: c.transferCount,
            volume_usd: c.volumeUsd,
            fees_usd: c.feesUsd,
            completed: c.completed,
            failed: c.failed,
            success_rate: c.successRate,
            avg_latency_ms: c.avgLatencyMs,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/analytics/latency ───────────────────────────────────
  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/latency",
    {
      schema: {
        querystring: analyticsPeriodQuerySchema,
        response: { 200: latencyPercentilesResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getTransferLatencyPercentiles({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id);
        return reply.send({
          sample_count: result.sampleCount,
          p50_ms: result.p50Ms,
          p90_ms: result.p90Ms,
          p95_ms: result.p95Ms,
          p99_ms: result.p99Ms,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/analytics/comparison ────────────────────────────────
  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/comparison",
    {
      schema: {
        querystring: volumeComparisonQuerySchema,
        response: { 200: volumeComparisonResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getVolumeComparison({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id);
        return reply.send({
          current_count: result.currentCount,
          current_volume_usd: result.currentVolumeUsd,
          current_fees_usd: result.currentFeesUsd,
          previous_count: result.previousCount,
          previous_volume_usd: result.previousVolumeUsd,
          previous_fees_usd: result.previousFeesUsd,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/analytics/activity ──────────────────────────────────
  app.get<{ Querystring: { limit?: number } }>(
    "/v1/me/analytics/activity",
    {
      schema: {
        querystring: recentActivityQuerySchema,
        response: { 200: recentActivityResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getRecentActivity({
          tenantId: request.tenantAuth.tenantId,
          limit: request.query.limit || 20,
        }, request.id);
        return reply.send({
          items: (result.items || []).map((item: any) => ({
            transfer_id: item.transferId,
            external_ref: item.externalRef,
            status: item.status,
            source_currency: item.sourceCurrency,
            source_amount: item.sourceAmount,
            dest_currency: item.destCurrency,
            dest_amount: item.destAmount,
            updated_at: item.updatedAt,
            failure_reason: item.failureReason,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/webhooks/deliveries — delivery history ─────────────────
  app.get<{
    Querystring: { event_type?: string; status?: string; page_size?: number; page_offset?: number };
  }>(
    "/v1/me/webhooks/deliveries",
    {
      schema: {
        querystring: webhookDeliveriesQuerySchema,
        response: {
          200: webhookDeliveriesResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listWebhookDeliveries({
          tenantId: tenantAuth.tenantId,
          eventType: request.query.event_type || "",
          status: request.query.status || "",
          pageSize: request.query.page_size || 50,
          pageOffset: request.query.page_offset || 0,
        }, request.id);
        return reply.send({
          deliveries: (result.deliveries || []).map(mapWebhookDelivery),
          total_count: result.totalCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/webhooks/deliveries/:deliveryId — delivery detail ──────
  app.get<{
    Params: { deliveryId: string };
  }>(
    "/v1/me/webhooks/deliveries/:deliveryId",
    {
      schema: {
        params: {
          type: "object",
          properties: { deliveryId: { type: "string", format: "uuid" } },
          required: ["deliveryId"],
        },
        response: {
          200: webhookDeliveryDetailResponseSchema,
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getWebhookDelivery({
          tenantId: tenantAuth.tenantId,
          deliveryId: request.params.deliveryId,
        }, request.id);
        return reply.send({
          delivery: mapWebhookDelivery(result.delivery),
          request_body: result.requestBody ? JSON.parse(Buffer.from(result.requestBody).toString()) : null,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/webhooks/stats — delivery stats ────────────────────────
  app.get<{
    Querystring: { period?: string };
  }>(
    "/v1/me/webhooks/stats",
    {
      schema: {
        querystring: webhookDeliveryStatsQuerySchema,
        response: {
          200: webhookDeliveryStatsResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getWebhookDeliveryStats({
          tenantId: tenantAuth.tenantId,
          period: request.query.period || "24h",
        }, request.id);
        return reply.send({
          total_deliveries: result.totalDeliveries,
          successful: result.successful,
          failed: result.failed,
          dead_lettered: result.deadLettered,
          pending: result.pending,
          avg_latency_ms: result.avgLatencyMs,
          p95_latency_ms: result.p95LatencyMs,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── GET /v1/me/webhooks/subscriptions — event type subscriptions ──────
  app.get(
    "/v1/me/webhooks/subscriptions",
    {
      schema: {
        response: {
          200: webhookEventSubscriptionsResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listWebhookEventSubscriptions({
          tenantId: tenantAuth.tenantId,
        }, request.id);
        return reply.send({
          subscriptions: (result.subscriptions || []).map((s: any) => ({
            id: s.id,
            event_type: s.eventType,
            created_at: s.createdAt,
          })),
          available_event_types: result.availableEventTypes || [],
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── PUT /v1/me/webhooks/subscriptions — update event subscriptions ────
  app.put<{
    Body: { event_types: string[] };
  }>(
    "/v1/me/webhooks/subscriptions",
    {
      schema: {
        body: updateWebhookEventSubscriptionsBodySchema,
        response: {
          200: webhookEventSubscriptionsResponseSchema,
          400: errorResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.updateWebhookEventSubscriptions({
          tenantId: tenantAuth.tenantId,
          eventTypes: request.body.event_types,
        }, request.id);
        return reply.send({
          subscriptions: (result.subscriptions || []).map((s: any) => ({
            id: s.id,
            event_type: s.eventType,
            created_at: s.createdAt,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  // ── POST /v1/me/webhooks/test — test webhook endpoint ─────────────────
  app.post(
    "/v1/me/webhooks/test",
    {
      schema: {
        response: {
          200: testWebhookResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.testWebhook({
          tenantId: tenantAuth.tenantId,
        }, request.id);
        return reply.send({
          success: result.success,
          status_code: result.statusCode,
          duration_ms: result.durationMs,
          error: result.error,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );
}

function mapWebhookDelivery(d: any): any {
  if (!d) return {};
  return {
    id: d.id,
    tenant_id: d.tenantId,
    event_type: d.eventType,
    transfer_id: d.transferId,
    delivery_id: d.deliveryId,
    webhook_url: d.webhookUrl,
    status: d.status,
    status_code: d.statusCode,
    attempt: d.attempt,
    max_attempts: d.maxAttempts,
    error_message: d.errorMessage,
    duration_ms: d.durationMs,
    created_at: d.createdAt,
    delivered_at: d.deliveredAt,
  };
}
