import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { FastifyRequest } from "fastify";
import type { GrpcPool } from "./pool.js";
import { config } from "../config.js";
import { injectTraceContext } from "../tracing.js";
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
      path.join(PROTO_DIR, "settla/v1/portal_auth.proto"),
      path.join(PROTO_DIR, "settla/v1/deposit.proto"),
      path.join(PROTO_DIR, "settla/v1/bank_deposit.proto"),
      path.join(PROTO_DIR, "settla/v1/payment_link.proto"),
      path.join(PROTO_DIR, "settla/v1/analytics.proto"),
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

  private PortalAuthServiceCtor: any;
  private DepositServiceCtor: any;
  private BankDepositServiceCtor: any;
  private PaymentLinkServiceCtor: any;
  private AnalyticsServiceCtor: any;
  private LedgerServiceCtor: any;

  constructor(pool: GrpcPool) {
    this.pool = pool;
    const p = loadProto();
    this.SettlementServiceCtor = p.settla.v1.SettlementService;
    this.TreasuryServiceCtor = p.settla.v1.TreasuryService;
    this.AuthServiceCtor = p.settla.v1.AuthService;
    this.TenantPortalServiceCtor = p.settla.v1.TenantPortalService;
    this.PortalAuthServiceCtor = p.settla.v1.PortalAuthService;
    this.DepositServiceCtor = p.settla.v1.DepositService;
    this.BankDepositServiceCtor = p.settla.v1.BankDepositService;
    this.PaymentLinkServiceCtor = p.settla.v1.PaymentLinkService;
    this.AnalyticsServiceCtor = p.settla.v1.AnalyticsService;
    this.LedgerServiceCtor = p.settla.v1.LedgerService;
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
    // Track in-flight requests for graceful drain
    this.pool.trackStart();
    try {
      const result = await fn();
      this.pool.recordSuccess();
      grpcCircuitBreakerState.set(0); // closed
      return result;
    } catch (err) {
      this.pool.recordFailure();
      grpcCircuitBreakerState.set(stateMap[this.pool.circuitState]);
      throw err;
    } finally {
      this.pool.trackEnd();
    }
  }

  /**
   * Adds common metadata headers to gRPC calls: request ID and W3C trace context.
   */
  private buildMetadata(requestId?: string, request?: FastifyRequest): grpc.Metadata {
    const meta = new grpc.Metadata();
    if (requestId) meta.add("x-request-id", requestId);
    injectTraceContext(meta, request);
    return meta;
  }

  /** Promise wrapper for gRPC unary calls. */
  private callSettlement<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.SettlementServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  private callTreasury<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.TreasuryServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  private callAuth<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.AuthServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  private callLedger<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.LedgerServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
      const deadline = new Date(Date.now() + config.grpcDeadlineMs.ledger);

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


  validateApiKey(req: { keyHash: string }, requestId?: string, httpReq?: FastifyRequest): Promise<{
    valid: boolean;
    tenantId: string;
    slug: string;
    status: string;
    feeScheduleJson: string;
    dailyLimitUsd: string;
    perTransferLimit: string;
  }> {
    return this.callAuth("ValidateAPIKey", req, requestId, httpReq);
  }


  createQuote(req: {
    tenantId: string;
    sourceCurrency: string;
    sourceAmount: string;
    destCurrency: string;
    destCountry?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("createQuote", req, requestId, httpReq);
  }

  getQuote(req: { tenantId: string; quoteId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("getQuote", req, requestId, httpReq);
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
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("createTransfer", req, requestId, httpReq);
  }

  getTransfer(req: { tenantId: string; transferId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("getTransfer", req, requestId, httpReq);
  }

  listTransfers(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
    statusFilter?: string;
    searchQuery?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("listTransfers", req, requestId, httpReq);
  }

  getTransferByExternalRef(req: {
    tenantId: string;
    externalRef: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("getTransferByExternalRef", req, requestId, httpReq);
  }

  cancelTransfer(req: {
    tenantId: string;
    transferId: string;
    reason?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("cancelTransfer", req, requestId, httpReq);
  }

  listTransferEvents(req: {
    tenantId: string;
    transferId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("listTransferEvents", req, requestId, httpReq);
  }

  getRoutingOptions(req: {
    tenantId: string;
    fromCurrency: string;
    toCurrency: string;
    amount: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callSettlement("getRoutingOptions", req, requestId, httpReq);
  }


  getPositions(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("getPositions", req, requestId, httpReq);
  }

  getPosition(req: {
    tenantId: string;
    currency: string;
    location: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("getPosition", req, requestId, httpReq);
  }

  getLiquidityReport(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("getLiquidityReport", req, requestId, httpReq);
  }

  requestTopUp(req: {
    tenantId: string;
    currency: string;
    location: string;
    amount: string;
    method: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("requestTopUp", req, requestId, httpReq);
  }

  requestWithdrawal(req: {
    tenantId: string;
    currency: string;
    location: string;
    amount: string;
    method: string;
    destination: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("requestWithdrawal", req, requestId, httpReq);
  }

  getPositionTransaction(req: {
    tenantId: string;
    transactionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("getPositionTransaction", req, requestId, httpReq);
  }

  listPositionTransactions(req: {
    tenantId: string;
    limit?: number;
    offset?: number;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("listPositionTransactions", req, requestId, httpReq);
  }

  getPositionEventHistory(req: {
    tenantId: string;
    currency: string;
    location: string;
    from?: { seconds: number; nanos: number };
    to?: { seconds: number; nanos: number };
    limit?: number;
    offset?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callTreasury("getPositionEventHistory", req, requestId, httpReq);
  }


  getAccounts(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callLedger("getAccounts", req, requestId, httpReq);
  }

  getAccountBalance(req: {
    tenantId: string;
    accountCode: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callLedger("getAccountBalance", req, requestId, httpReq);
  }

  getTransactions(req: {
    tenantId: string;
    accountCode: string;
    from?: any;
    to?: any;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callLedger("getTransactions", req, requestId, httpReq);
  }


  private callPortal<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.TenantPortalServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  getMyTenant(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getMyTenant", req, requestId, httpReq);
  }

  updateWebhookConfig(req: {
    tenantId: string;
    webhookUrl: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("updateWebhookConfig", req, requestId, httpReq);
  }

  listAPIKeys(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("listApiKeys", req, requestId, httpReq);
  }

  createAPIKey(req: {
    tenantId: string;
    environment: string;
    name?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("createApiKey", req, requestId, httpReq);
  }

  revokeAPIKey(req: { tenantId: string; keyId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("revokeApiKey", req, requestId, httpReq);
  }

  rotateAPIKey(req: {
    tenantId: string;
    oldKeyId: string;
    name?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("rotateApiKey", req, requestId, httpReq);
  }

  getDashboardMetrics(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getDashboardMetrics", req, requestId, httpReq);
  }

  getTransferStats(req: {
    tenantId: string;
    period: string;
    granularity: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getTransferStats", req, requestId, httpReq);
  }

  getFeeReport(req: {
    tenantId: string;
    from?: any;
    to?: any;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getFeeReport", req, requestId, httpReq);
  }


  listWebhookDeliveries(req: {
    tenantId: string;
    eventType?: string;
    status?: string;
    pageSize?: number;
    pageOffset?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("listWebhookDeliveries", req, requestId, httpReq);
  }

  getWebhookDelivery(req: {
    tenantId: string;
    deliveryId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getWebhookDelivery", req, requestId, httpReq);
  }

  getWebhookDeliveryStats(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getWebhookDeliveryStats", req, requestId, httpReq);
  }

  listWebhookEventSubscriptions(req: {
    tenantId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("listWebhookEventSubscriptions", req, requestId, httpReq);
  }

  updateWebhookEventSubscriptions(req: {
    tenantId: string;
    eventTypes: string[];
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("updateWebhookEventSubscriptions", req, requestId, httpReq);
  }

  testWebhook(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("testWebhook", req, requestId, httpReq);
  }


  getTransferStatusDistribution(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getTransferStatusDistribution", req, requestId, httpReq);
  }

  getCorridorMetrics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getCorridorMetrics", req, requestId, httpReq);
  }

  getTransferLatencyPercentiles(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getTransferLatencyPercentiles", req, requestId, httpReq);
  }

  getVolumeComparison(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getVolumeComparison", req, requestId, httpReq);
  }

  getRecentActivity(req: {
    tenantId: string;
    limit?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortal("getRecentActivity", req, requestId, httpReq);
  }


  private callPortalAuth<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.PortalAuthServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  register(req: {
    companyName: string;
    email: string;
    password: string;
    displayName?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("register", req, requestId, httpReq);
  }

  login(req: {
    email: string;
    password: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("login", req, requestId, httpReq);
  }

  verifyEmail(req: { token: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("verifyEmail", req, requestId, httpReq);
  }

  refreshToken(req: { refreshToken: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("refreshToken", req, requestId, httpReq);
  }

  submitKYB(req: {
    tenantId: string;
    companyRegistrationNumber: string;
    country: string;
    businessType: string;
    contactName: string;
    contactEmail: string;
    contactPhone?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("submitKyb", req, requestId, httpReq);
  }

  approveKYB(req: { tenantId: string }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPortalAuth("approveKyb", req, requestId, httpReq);
  }


  private callDeposit<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.DepositServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  createDepositSession(req: {
    tenantId: string;
    chain: string;
    token: string;
    expectedAmount: string;
    currency?: string;
    settlementPref?: string;
    idempotencyKey?: string;
    ttlSeconds?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("createDepositSession", req, requestId, httpReq);
  }

  getDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("getDepositSession", req, requestId, httpReq);
  }

  listDepositSessions(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("listDepositSessions", req, requestId, httpReq);
  }

  cancelDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("cancelDepositSession", req, requestId, httpReq);
  }

  getDepositSessionByTxHash(req: {
    tenantId: string;
    txHash: string;
    chain: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("getDepositSessionByTxHash", req, requestId, httpReq);
  }


  private callBankDeposit<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.BankDepositServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  createBankDepositSession(req: {
    tenantId: string;
    currency: string;
    bankingPartnerId?: string;
    accountType?: string;
    expectedAmount: string;
    minAmount?: string;
    maxAmount?: string;
    mismatchPolicy?: string;
    settlementPref?: string;
    idempotencyKey?: string;
    ttlSeconds?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("createBankDepositSession", req, requestId, httpReq);
  }

  getBankDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("getBankDepositSession", req, requestId, httpReq);
  }

  listBankDepositSessions(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("listBankDepositSessions", req, requestId, httpReq);
  }

  cancelBankDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("cancelBankDepositSession", req, requestId, httpReq);
  }

  listVirtualAccounts(req: {
    tenantId: string;
    currency?: string;
    accountType?: string;
    limit?: number;
    offset?: number;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("listVirtualAccounts", req, requestId, httpReq);
  }

  getBankingPartner(req: {
    partnerId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callBankDeposit("getBankingPartner", req, requestId, httpReq);
  }


  private callPaymentLink<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.PaymentLinkServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  createPaymentLink(req: {
    tenantId: string;
    description?: string;
    redirectUrl?: string;
    useLimit?: number;
    expiresAtUnix?: number;
    amount: string;
    currency: string;
    chain: string;
    token: string;
    settlementPref?: string;
    ttlSeconds?: number;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("createPaymentLink", req, requestId, httpReq);
  }

  getPaymentLink(req: {
    tenantId: string;
    linkId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("getPaymentLink", req, requestId, httpReq);
  }

  listPaymentLinks(req: {
    tenantId: string;
    limit?: number;
    offset?: number;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("listPaymentLinks", req, requestId, httpReq);
  }

  resolvePaymentLink(req: {
    shortCode: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("resolvePaymentLink", req, requestId, httpReq);
  }

  redeemPaymentLink(req: {
    shortCode: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("redeemPaymentLink", req, requestId, httpReq);
  }

  disablePaymentLink(req: {
    tenantId: string;
    linkId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callPaymentLink("disablePaymentLink", req, requestId, httpReq);
  }

  getDepositPublicStatus(req: {
    sessionId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callDeposit("getDepositSessionPublicStatus", req, requestId, httpReq);
  }


  private callAnalytics<T>(method: string, request: any, requestId?: string, httpReq?: FastifyRequest): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.AnalyticsServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = this.buildMetadata(requestId, httpReq);
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

  getTransferAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getTransferAnalytics", req, requestId, httpReq);
  }

  getFeeAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getFeeAnalytics", req, requestId, httpReq);
  }

  getProviderAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getProviderAnalytics", req, requestId, httpReq);
  }

  getReconciliationAnalytics(req: {
    tenantId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getReconciliationAnalytics", req, requestId, httpReq);
  }

  getDepositAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getDepositAnalytics", req, requestId, httpReq);
  }

  createAnalyticsExport(req: {
    tenantId: string;
    exportType: string;
    period?: string;
    format?: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("createExportJob", req, requestId, httpReq);
  }

  getAnalyticsExport(req: {
    tenantId: string;
    jobId: string;
  }, requestId?: string, httpReq?: FastifyRequest): Promise<any> {
    return this.callAnalytics("getExportJob", req, requestId, httpReq);
  }
}

/** Create a SettlaGrpcClient from proto files. */
export function createGrpcClient(pool: GrpcPool): SettlaGrpcClient {
  return new SettlaGrpcClient(pool);
}
