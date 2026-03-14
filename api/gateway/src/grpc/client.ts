import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { GrpcPool } from "./pool.js";
import { config } from "../config.js";
import { grpcCircuitBreakerRejections, grpcCircuitBreakerState } from "../metrics.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Load proto definitions at startup (compiled to fast accessor objects).
// Proto files are loaded once; gRPC internally compiles them to efficient
// serializers/deserializers.
const PROTO_DIR = path.resolve(__dirname, "../../../../proto");

let proto: any;

function loadProto() {
  if (proto) return proto;
  const packageDef = protoLoader.loadSync(
    [
      path.join(PROTO_DIR, "settla/v1/settlement.proto"),
      path.join(PROTO_DIR, "settla/v1/types.proto"),
      path.join(PROTO_DIR, "settla/v1/tenant_portal.proto"),
    ],
    {
      keepCase: false, // convert to camelCase
      longs: String, // decimal precision — never use JS number for money
      enums: String,
      defaults: true,
      oneofs: true,
      includeDirs: [PROTO_DIR],
    },
  );
  proto = grpc.loadPackageDefinition(packageDef);
  return proto;
}

/**
 * SettlaGrpcClient wraps all gRPC service clients, distributing calls
 * across the connection pool via round-robin.
 */
export class SettlaGrpcClient {
  private pool: GrpcPool;
  private SettlementServiceCtor: any;
  private TreasuryServiceCtor: any;
  private AuthServiceCtor: any;
  private TenantPortalServiceCtor: any;

  constructor(pool: GrpcPool) {
    this.pool = pool;
    const p = loadProto();
    this.SettlementServiceCtor = p.settla.v1.SettlementService;
    this.TreasuryServiceCtor = p.settla.v1.TreasuryService;
    this.AuthServiceCtor = p.settla.v1.AuthService;
    this.TenantPortalServiceCtor = p.settla.v1.TenantPortalService;
  }

  /** Check circuit breaker before dispatch, record result after. */
  private async withCircuitBreaker<T>(fn: () => Promise<T>): Promise<T> {
    if (this.pool.isOpen()) {
      grpcCircuitBreakerRejections.inc();
      throw Object.assign(new Error("settla-gateway: gRPC circuit breaker is open"), {
        code: grpc.status.UNAVAILABLE,
      });
    }
    // Update state gauge
    const stateMap = { closed: 0, "half-open": 0.5, open: 1 } as const;
    grpcCircuitBreakerState.set(stateMap[this.pool.circuitState]);
    try {
      const result = await fn();
      this.pool.recordSuccess();
      grpcCircuitBreakerState.set(0); // closed
      return result;
    } catch (err) {
      this.pool.recordFailure();
      grpcCircuitBreakerState.set(stateMap[this.pool.circuitState]);
      throw err;
    }
  }

  /** Promise wrapper for gRPC unary calls. */
  private callSettlement<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.SettlementServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();
      if (requestId) meta.add("x-request-id", requestId);
      const deadline = new Date(Date.now() + config.grpcDeadlineMs.settlement);

      client[method](
        request,
        meta,
        { deadline },
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    }));
  }

  private callTreasury<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.TreasuryServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();
      if (requestId) meta.add("x-request-id", requestId);
      const deadline = new Date(Date.now() + config.grpcDeadlineMs.treasury);

      client[method](
        request,
        meta,
        { deadline },
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    }));
  }

  private callAuth<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.AuthServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();
      if (requestId) meta.add("x-request-id", requestId);
      const deadline = new Date(Date.now() + config.grpcDeadlineMs.auth);

      client[method](
        request,
        meta,
        { deadline },
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    }));
  }

  // ── Auth Service ────────────────────────────────────────────────────────

  validateApiKey(req: { keyHash: string }, requestId?: string): Promise<{
    valid: boolean;
    tenantId: string;
    slug: string;
    status: string;
    feeScheduleJson: string;
    dailyLimitUsd: string;
    perTransferLimit: string;
  }> {
    return this.callAuth("ValidateAPIKey", req, requestId);
  }

  // ── Settlement Service ──────────────────────────────────────────────────

  createQuote(req: {
    tenantId: string;
    sourceCurrency: string;
    sourceAmount: string;
    destCurrency: string;
    destCountry?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("createQuote", req, requestId);
  }

  getQuote(req: { tenantId: string; quoteId: string }, requestId?: string): Promise<any> {
    return this.callSettlement("getQuote", req, requestId);
  }

  createTransfer(req: {
    tenantId: string;
    idempotencyKey: string;
    externalRef?: string;
    sourceCurrency: string;
    sourceAmount: string;
    destCurrency: string;
    sender?: any;
    recipient: any;
    quoteId?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("createTransfer", req, requestId);
  }

  getTransfer(req: { tenantId: string; transferId: string }, requestId?: string): Promise<any> {
    return this.callSettlement("getTransfer", req, requestId);
  }

  listTransfers(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("listTransfers", req, requestId);
  }

  cancelTransfer(req: {
    tenantId: string;
    transferId: string;
    reason?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("cancelTransfer", req, requestId);
  }

  // ── Treasury Service ────────────────────────────────────────────────────

  getPositions(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callTreasury("getPositions", req, requestId);
  }

  getPosition(req: {
    tenantId: string;
    currency: string;
    location: string;
  }, requestId?: string): Promise<any> {
    return this.callTreasury("getPosition", req, requestId);
  }

  getLiquidityReport(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callTreasury("getLiquidityReport", req, requestId);
  }

  // ── Tenant Portal Service ─────────────────────────────────────────────

  private callPortal<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.TenantPortalServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();
      if (requestId) meta.add("x-request-id", requestId);
      const deadline = new Date(Date.now() + config.grpcDeadlineMs.portal);

      client[method](
        request,
        meta,
        { deadline },
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    }));
  }

  getMyTenant(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callPortal("getMyTenant", req, requestId);
  }

  updateWebhookConfig(req: {
    tenantId: string;
    webhookUrl: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("updateWebhookConfig", req, requestId);
  }

  listAPIKeys(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callPortal("listAPIKeys", req, requestId);
  }

  createAPIKey(req: {
    tenantId: string;
    environment: string;
    name?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("createAPIKey", req, requestId);
  }

  revokeAPIKey(req: { tenantId: string; keyId: string }, requestId?: string): Promise<any> {
    return this.callPortal("revokeAPIKey", req, requestId);
  }

  rotateAPIKey(req: {
    tenantId: string;
    oldKeyId: string;
    name?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("rotateAPIKey", req, requestId);
  }

  getDashboardMetrics(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callPortal("getDashboardMetrics", req, requestId);
  }

  getTransferStats(req: {
    tenantId: string;
    period: string;
    granularity: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getTransferStats", req, requestId);
  }

  getFeeReport(req: {
    tenantId: string;
    from?: any;
    to?: any;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getFeeReport", req, requestId);
  }

  // ── Webhook Management ────────────────────────────────────────────────

  listWebhookDeliveries(req: {
    tenantId: string;
    eventType?: string;
    status?: string;
    pageSize?: number;
    pageOffset?: number;
  }, requestId?: string): Promise<any> {
    return this.callPortal("listWebhookDeliveries", req, requestId);
  }

  getWebhookDelivery(req: {
    tenantId: string;
    deliveryId: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getWebhookDelivery", req, requestId);
  }

  getWebhookDeliveryStats(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getWebhookDeliveryStats", req, requestId);
  }

  listWebhookEventSubscriptions(req: {
    tenantId: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("listWebhookEventSubscriptions", req, requestId);
  }

  updateWebhookEventSubscriptions(req: {
    tenantId: string;
    eventTypes: string[];
  }, requestId?: string): Promise<any> {
    return this.callPortal("updateWebhookEventSubscriptions", req, requestId);
  }

  testWebhook(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callPortal("testWebhook", req, requestId);
  }

  // ── Analytics ─────────────────────────────────────────────────────────

  getTransferStatusDistribution(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getTransferStatusDistribution", req, requestId);
  }

  getCorridorMetrics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getCorridorMetrics", req, requestId);
  }

  getTransferLatencyPercentiles(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getTransferLatencyPercentiles", req, requestId);
  }

  getVolumeComparison(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getVolumeComparison", req, requestId);
  }

  getRecentActivity(req: {
    tenantId: string;
    limit?: number;
  }, requestId?: string): Promise<any> {
    return this.callPortal("getRecentActivity", req, requestId);
  }
}

/** Create a SettlaGrpcClient from proto files. */
export function createGrpcClient(pool: GrpcPool): SettlaGrpcClient {
  return new SettlaGrpcClient(pool);
}
