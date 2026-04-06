import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import {
  analyticsPeriodQuerySchema,
  errorResponseSchema,
  analyticsTransferResponseSchema,
  analyticsFeeResponseSchema,
  analyticsProviderResponseSchema,
  analyticsReconciliationResponseSchema,
  analyticsDepositResponseSchema,
  analyticsExportJobSchema,
  analyticsExportBodySchema,
} from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

export async function analyticsRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc } = opts;

  app.get<{ Querystring: { period?: string } }>(
    "/v1/analytics/transfers",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get transfer analytics",
        operationId: "getOpsTransferAnalytics",
        querystring: analyticsPeriodQuerySchema,
        response: { 200: analyticsTransferResponseSchema, 401: errorResponseSchema },
      },
    },
    async (request, reply) => {
      try {
        const result = await grpc.getTransferAnalytics({
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
          statuses: (result.statuses || []).map((s: any) => ({
            status: s.status,
            count: s.count,
          })),
          total_count: result.totalCount,
          total_volume_usd: result.totalVolumeUsd,
          total_fees_usd: result.totalFeesUsd,
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
    "/v1/analytics/fees",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get fee analytics",
        operationId: "getOpsFeeAnalytics",
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
    "/v1/analytics/providers",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get provider analytics",
        operationId: "getOpsProviderAnalytics",
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
    "/v1/analytics/reconciliation",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get reconciliation analytics",
        operationId: "getOpsReconciliationAnalytics",
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
    "/v1/analytics/deposits",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get deposit analytics",
        operationId: "getOpsDepositAnalytics",
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
        return reply.send({
          crypto: mapDepositAnalytics(result.crypto),
          bank: mapDepositAnalytics(result.bank),
        });
      } catch (err) {
        return mapGrpcError(request, reply, err);
      }
    },
  );

  app.post<{
    Body: { export_type: string; period?: string; format?: string };
  }>(
    "/v1/analytics/export",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Create analytics export",
        operationId: "createOpsAnalyticsExport",
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
    "/v1/analytics/export/:jobId",
    {
      schema: {
        tags: ["Analytics"],
        summary: "Get analytics export job",
        operationId: "getOpsAnalyticsExportJob",
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
}

function mapDepositAnalytics(d: any): any {
  if (!d) return {};
  return {
    total_sessions: d.totalSessions,
    completed_sessions: d.completedSessions,
    expired_sessions: d.expiredSessions,
    failed_sessions: d.failedSessions,
    conversion_rate: d.conversionRate,
    total_received: d.totalReceived,
    total_fees: d.totalFees,
    total_net: d.totalNet,
  };
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
