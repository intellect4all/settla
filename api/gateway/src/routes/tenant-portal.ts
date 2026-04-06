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
  analyticsFeeResponseSchema,
  analyticsProviderResponseSchema,
  analyticsReconciliationResponseSchema,
  analyticsDepositResponseSchema,
  analyticsExportJobSchema,
  analyticsExportBodySchema,
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

  app.get(
    "/v1/me",
    {
      schema: {
        tags: ["Account"],
        summary: "Get tenant profile",
        operationId: "getTenantProfile",
        response: {
          200: tenantProfileResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.getMyTenant({ tenantId: tenantAuth.tenantId }, request.id, request);
        return reply.send(mapTenantProfile(result.tenant));
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.put<{
    Body: { webhook_url: string };
  }>(
    "/v1/me/webhooks",
    {
      schema: {
        tags: ["Account"],
        summary: "Update webhook config",
        operationId: "updateWebhookConfig",
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
        }, request.id, request);
        return reply.send({
          webhook_url: result.webhookUrl,
          webhook_secret: result.webhookSecret,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get(
    "/v1/me/api-keys",
    {
      schema: {
        tags: ["Account"],
        summary: "List API keys",
        operationId: "listApiKeys",
        response: {
          200: apiKeyListResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      try {
        const result = await grpc.listAPIKeys({ tenantId: tenantAuth.tenantId }, request.id, request);
        return reply.send({
          keys: (result.keys || []).map(mapApiKey),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.post<{
    Body: { environment: string; name?: string };
  }>(
    "/v1/me/api-keys",
    {
      schema: {
        tags: ["Account"],
        summary: "Create an API key",
        operationId: "createApiKey",
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
        }, request.id, request);
        return reply.status(201).send({
          key: mapApiKey(result.key),
          raw_key: result.rawKey,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.delete<{
    Params: { keyId: string };
  }>(
    "/v1/me/api-keys/:keyId",
    {
      schema: {
        tags: ["Account"],
        summary: "Revoke an API key",
        operationId: "revokeApiKey",
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
        }, request.id, request);

        // Immediately invalidate the revoked key from L1+L2 auth caches
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

  app.post<{
    Params: { keyId: string };
    Body: { name?: string };
  }>(
    "/v1/me/api-keys/:keyId/rotate",
    {
      schema: {
        tags: ["Account"],
        summary: "Rotate an API key",
        operationId: "rotateApiKey",
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
        }, request.id, request);
        return reply.send({
          key: mapApiKey(result.key),
          raw_key: result.rawKey,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get(
    "/v1/me/dashboard",
    {
      schema: {
        tags: ["Account"],
        summary: "Get dashboard metrics",
        operationId: "getDashboardMetrics",
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
        }, request.id, request);
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

  app.get<{
    Querystring: { period?: string; granularity?: string };
  }>(
    "/v1/me/transfers/stats",
    {
      schema: {
        tags: ["Account"],
        summary: "Get transfer stats",
        operationId: "getTransferStats",
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
        }, request.id, request);
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

  app.get<{
    Querystring: { from?: string; to?: string };
  }>(
    "/v1/me/fees/report",
    {
      schema: {
        tags: ["Account"],
        summary: "Get fee breakdown",
        operationId: "getFeeReport",
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
        }, request.id, request);
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/status-distribution",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get status distribution",
        operationId: "getStatusDistribution",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: statusDistributionResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getTransferStatusDistribution({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/corridors",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get corridor metrics",
        operationId: "getCorridorMetrics",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: corridorMetricsResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getCorridorMetrics({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/latency",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get latency percentiles",
        operationId: "getLatencyPercentiles",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: latencyPercentilesResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getTransferLatencyPercentiles({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/comparison",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get volume comparison",
        operationId: "getVolumeComparison",
        querystring: volumeComparisonQuerySchema,
        response: { 200: volumeComparisonResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getVolumeComparison({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
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

  app.get<{ Querystring: { limit?: number } }>(
    "/v1/me/analytics/activity",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get recent activity",
        operationId: "getRecentActivity",
        querystring: recentActivityQuerySchema,
        response: { 200: recentActivityResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getRecentActivity({
          tenantId: request.tenantAuth.tenantId,
          limit: request.query.limit || 20,
        }, request.id, request);
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/fees",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get fee analytics",
        operationId: "getFeeAnalytics",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: analyticsFeeResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getFeeAnalytics({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
        return reply.send({
          entries: (result.entries || []).map((e: any) => ({
            source_currency: e.sourceCurrency,
            dest_currency: e.destCurrency,
            transfer_count: e.transferCount,
            volume_usd: e.volumeUsd,
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

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/providers",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get provider analytics",
        operationId: "getProviderAnalytics",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: analyticsProviderResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getProviderAnalytics({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
        return reply.send({
          providers: (result.providers || []).map((p: any) => ({
            provider: p.provider,
            source_currency: p.sourceCurrency,
            dest_currency: p.destCurrency,
            transaction_count: p.transactionCount,
            completed: p.completed,
            failed: p.failed,
            success_rate: p.successRate,
            avg_settlement_ms: p.avgSettlementMs,
            total_volume: p.totalVolume,
          })),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get(
    "/v1/me/analytics/reconciliation",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get reconciliation analytics",
        operationId: "getReconciliationAnalytics",
        response: { 200: analyticsReconciliationResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getReconciliationAnalytics({
          tenantId: request.tenantAuth.tenantId,
        }, request.id, request);
        return reply.send({
          total_runs: result.totalRuns,
          checks_passed: result.checksPassed,
          checks_failed: result.checksFailed,
          pass_rate: result.passRate,
          last_run_at: result.lastRunAt?.seconds ? new Date(Number(result.lastRunAt.seconds) * 1000).toISOString() : null,
          needs_review_count: result.needsReviewCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{ Querystring: { period?: string } }>(
    "/v1/me/analytics/deposits",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get deposit analytics",
        operationId: "getDepositAnalytics",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: analyticsDepositResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getDepositAnalytics({
          tenantId: request.tenantAuth.tenantId,
          period: request.query.period || "7d",
        }, request.id, request);
        const mapDeposit = (d: any) => d ? {
          total_sessions: d.totalSessions,
          completed_sessions: d.completedSessions,
          expired_sessions: d.expiredSessions,
          failed_sessions: d.failedSessions,
          conversion_rate: d.conversionRate,
          total_received: d.totalReceived,
          total_fees: d.totalFees,
          total_net: d.totalNet,
        } : {};
        return reply.send({
          crypto: mapDeposit(result.crypto),
          bank: mapDeposit(result.bank),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.post<{ Body: { export_type: string; period?: string; format?: string } }>(
    "/v1/me/analytics/export",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Create analytics export",
        operationId: "createAnalyticsExport",
        body: analyticsExportBodySchema,
        response: {
          201: { type: "object" as const, properties: { job: analyticsExportJobSchema } },
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.createAnalyticsExport({
          tenantId: request.tenantAuth.tenantId,
          exportType: request.body.export_type,
          period: request.body.period || "7d",
          format: request.body.format || "csv",
        }, request.id, request);
        return reply.status(201).send({
          job: mapExportJob(result.job),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{ Params: { jobId: string } }>(
    "/v1/me/analytics/export/:jobId",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get analytics export job",
        operationId: "getAnalyticsExportJob",
        params: {
          type: "object",
          properties: { jobId: { type: "string", format: "uuid" } },
          required: ["jobId"],
        },
        response: {
          200: { type: "object" as const, properties: { job: analyticsExportJobSchema } },
          401: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getAnalyticsExport({
          tenantId: request.tenantAuth.tenantId,
          jobId: request.params.jobId,
        }, request.id, request);
        return reply.send({
          job: mapExportJob(result.job),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Querystring: { event_type?: string; status?: string; page_size?: number; page_offset?: number };
  }>(
    "/v1/me/webhooks/deliveries",
    {
      schema: {
        tags: ["Account"],
        summary: "List webhook deliveries",
        operationId: "listWebhookDeliveries",
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
        }, request.id, request);
        return reply.send({
          deliveries: (result.deliveries || []).map(mapWebhookDelivery),
          total_count: result.totalCount,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Params: { deliveryId: string };
  }>(
    "/v1/me/webhooks/deliveries/:deliveryId",
    {
      schema: {
        tags: ["Account"],
        summary: "Get webhook delivery",
        operationId: "getWebhookDelivery",
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
        }, request.id, request);
        return reply.send({
          delivery: mapWebhookDelivery(result.delivery),
          request_body: result.requestBody ? JSON.parse(Buffer.from(result.requestBody).toString()) : null,
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.get<{
    Querystring: { period?: string };
  }>(
    "/v1/me/webhooks/stats",
    {
      schema: {
        tags: ["Account"],
        summary: "Get webhook stats",
        operationId: "getWebhookStats",
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
        }, request.id, request);
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

  app.get(
    "/v1/me/webhooks/subscriptions",
    {
      schema: {
        tags: ["Account"],
        summary: "Get event subscriptions",
        operationId: "getEventSubscriptions",
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
        }, request.id, request);
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

  app.put<{
    Body: { event_types: string[] };
  }>(
    "/v1/me/webhooks/subscriptions",
    {
      schema: {
        tags: ["Account"],
        summary: "Update event subscriptions",
        operationId: "updateEventSubscriptions",
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
        }, request.id, request);
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

  app.post(
    "/v1/me/webhooks/test",
    {
      schema: {
        tags: ["Account"],
        summary: "Send test webhook",
        operationId: "sendTestWebhook",
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
        }, request.id, request);
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


  // GET /v1/portal/crypto-settings
  app.get(
    "/v1/portal/crypto-settings",
    {
      schema: {
        tags: ["Account"],
        summary: "Get crypto settings",
        operationId: "getCryptoSettings",
        response: {
          200: {
            type: "object",
            properties: {
              crypto_enabled: { type: "boolean" },
              supported_chains: { type: "array", items: { type: "string" } },
              default_settlement_pref: { type: "string" },
              payment_tolerance_bps: { type: "number" },
              default_session_ttl_secs: { type: "number" },
              min_confirmations_tron: { type: "number" },
              min_confirmations_eth: { type: "number" },
              min_confirmations_base: { type: "number" },
            },
          },
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      request.log.info({ tenantId: tenantAuth.tenantId }, "Fetching crypto settings");
      // Default crypto settings — in production these would come from a tenant config table
      return reply.send({
        crypto_enabled: true,
        supported_chains: ["Tron", "Ethereum", "Base"],
        default_settlement_pref: "AUTO_CONVERT",
        payment_tolerance_bps: 50,
        default_session_ttl_secs: 3600,
        min_confirmations_tron: 19,
        min_confirmations_eth: 12,
        min_confirmations_base: 12,
      });
    },
  );

  // POST /v1/portal/crypto-settings
  app.post(
    "/v1/portal/crypto-settings",
    {
      schema: {
        tags: ["Account"],
        summary: "Update crypto settings",
        operationId: "updateCryptoSettings",
        body: {
          type: "object",
          properties: {
            crypto_enabled: { type: "boolean" },
            supported_chains: { type: "array", items: { type: "string" } },
            default_settlement_pref: { type: "string" },
            payment_tolerance_bps: { type: "number" },
            default_session_ttl_secs: { type: "number" },
            min_confirmations_tron: { type: "number" },
            min_confirmations_eth: { type: "number" },
            min_confirmations_base: { type: "number" },
          },
        },
        response: {
          200: {
            type: "object",
            properties: {
              crypto_enabled: { type: "boolean" },
              supported_chains: { type: "array", items: { type: "string" } },
              default_settlement_pref: { type: "string" },
              payment_tolerance_bps: { type: "number" },
              default_session_ttl_secs: { type: "number" },
              min_confirmations_tron: { type: "number" },
              min_confirmations_eth: { type: "number" },
              min_confirmations_base: { type: "number" },
            },
          },
          400: errorResponseSchema,
          401: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      request.log.info({ tenantId: tenantAuth.tenantId }, "Updating crypto settings");
      return reply.send(request.body);
    },
  );
}

function tsToISO(ts: any): string | null {
  if (!ts?.seconds) return null;
  return new Date(Number(ts.seconds) * 1000).toISOString();
}

function mapExportJob(j: any): any {
  if (!j) return {};
  return {
    id: j.id,
    status: j.status,
    export_type: j.exportType,
    row_count: j.rowCount,
    download_url: j.downloadUrl || "",
    download_expires_at: tsToISO(j.downloadExpiresAt) || "",
    error_message: j.errorMessage || "",
    created_at: tsToISO(j.createdAt) || "",
    completed_at: tsToISO(j.completedAt) || "",
  };
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
