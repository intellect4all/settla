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

  private callLedger<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.LedgerServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();
      if (requestId) meta.add("x-request-id", requestId);
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
    statusFilter?: string;
    searchQuery?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("listTransfers", req, requestId);
  }

  getTransferByExternalRef(req: {
    tenantId: string;
    externalRef: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("getTransferByExternalRef", req, requestId);
  }

  cancelTransfer(req: {
    tenantId: string;
    transferId: string;
    reason?: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("cancelTransfer", req, requestId);
  }

  listTransferEvents(req: {
    tenantId: string;
    transferId: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("listTransferEvents", req, requestId);
  }

  getRoutingOptions(req: {
    tenantId: string;
    fromCurrency: string;
    toCurrency: string;
    amount: string;
  }, requestId?: string): Promise<any> {
    return this.callSettlement("getRoutingOptions", req, requestId);
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

  // ── Ledger Service ────────────────────────────────────────────────────

  getAccounts(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string): Promise<any> {
    return this.callLedger("getAccounts", req, requestId);
  }

  getAccountBalance(req: {
    tenantId: string;
    accountCode: string;
  }, requestId?: string): Promise<any> {
    return this.callLedger("getAccountBalance", req, requestId);
  }

  getTransactions(req: {
    tenantId: string;
    accountCode: string;
    from?: any;
    to?: any;
    pageSize?: number;
    pageToken?: string;
  }, requestId?: string): Promise<any> {
    return this.callLedger("getTransactions", req, requestId);
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
    return this.callPortal("listApiKeys", req, requestId);
  }

  createAPIKey(req: {
    tenantId: string;
    environment: string;
    name?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("createApiKey", req, requestId);
  }

  revokeAPIKey(req: { tenantId: string; keyId: string }, requestId?: string): Promise<any> {
    return this.callPortal("revokeApiKey", req, requestId);
  }

  rotateAPIKey(req: {
    tenantId: string;
    oldKeyId: string;
    name?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortal("rotateApiKey", req, requestId);
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

  // ── Portal Auth Service ──────────────────────────────────────────────────

  private callPortalAuth<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.PortalAuthServiceCtor(null, null, {
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

  register(req: {
    companyName: string;
    email: string;
    password: string;
    displayName?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortalAuth("register", req, requestId);
  }

  login(req: {
    email: string;
    password: string;
  }, requestId?: string): Promise<any> {
    return this.callPortalAuth("login", req, requestId);
  }

  verifyEmail(req: { token: string }, requestId?: string): Promise<any> {
    return this.callPortalAuth("verifyEmail", req, requestId);
  }

  refreshToken(req: { refreshToken: string }, requestId?: string): Promise<any> {
    return this.callPortalAuth("refreshToken", req, requestId);
  }

  submitKYB(req: {
    tenantId: string;
    companyRegistrationNumber: string;
    country: string;
    businessType: string;
    contactName: string;
    contactEmail: string;
    contactPhone?: string;
  }, requestId?: string): Promise<any> {
    return this.callPortalAuth("submitKyb", req, requestId);
  }

  approveKYB(req: { tenantId: string }, requestId?: string): Promise<any> {
    return this.callPortalAuth("approveKyb", req, requestId);
  }

  // ── Deposit Service ──────────────────────────────────────────────────

  private callDeposit<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.DepositServiceCtor(null, null, {
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

  createDepositSession(req: {
    tenantId: string;
    chain: string;
    token: string;
    expectedAmount: string;
    currency?: string;
    settlementPref?: string;
    idempotencyKey?: string;
    ttlSeconds?: number;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("createDepositSession", req, requestId);
  }

  getDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("getDepositSession", req, requestId);
  }

  listDepositSessions(req: {
    tenantId: string;
    limit?: number;
    offset?: number;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("listDepositSessions", req, requestId);
  }

  cancelDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("cancelDepositSession", req, requestId);
  }

  getDepositSessionByTxHash(req: {
    tenantId: string;
    txHash: string;
    chain: string;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("getDepositSessionByTxHash", req, requestId);
  }

  // ── Bank Deposit Service ──────────────────────────────────────────────

  private callBankDeposit<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.BankDepositServiceCtor(null, null, {
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
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("createBankDepositSession", req, requestId);
  }

  getBankDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("getBankDepositSession", req, requestId);
  }

  listBankDepositSessions(req: {
    tenantId: string;
    limit?: number;
    offset?: number;
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("listBankDepositSessions", req, requestId);
  }

  cancelBankDepositSession(req: {
    tenantId: string;
    sessionId: string;
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("cancelBankDepositSession", req, requestId);
  }

  listVirtualAccounts(req: {
    tenantId: string;
    currency?: string;
    accountType?: string;
    limit?: number;
    offset?: number;
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("listVirtualAccounts", req, requestId);
  }

  getBankingPartner(req: {
    partnerId: string;
  }, requestId?: string): Promise<any> {
    return this.callBankDeposit("getBankingPartner", req, requestId);
  }

  // ── Payment Link Service ──────────────────────────────────────────────

  private callPaymentLink<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.PaymentLinkServiceCtor(null, null, {
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
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("createPaymentLink", req, requestId);
  }

  getPaymentLink(req: {
    tenantId: string;
    linkId: string;
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("getPaymentLink", req, requestId);
  }

  listPaymentLinks(req: {
    tenantId: string;
    limit?: number;
    offset?: number;
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("listPaymentLinks", req, requestId);
  }

  resolvePaymentLink(req: {
    shortCode: string;
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("resolvePaymentLink", req, requestId);
  }

  redeemPaymentLink(req: {
    shortCode: string;
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("redeemPaymentLink", req, requestId);
  }

  disablePaymentLink(req: {
    tenantId: string;
    linkId: string;
  }, requestId?: string): Promise<any> {
    return this.callPaymentLink("disablePaymentLink", req, requestId);
  }

  getDepositPublicStatus(req: {
    sessionId: string;
  }, requestId?: string): Promise<any> {
    return this.callDeposit("getDepositSessionPublicStatus", req, requestId);
  }

  // ── Analytics Service ──────────────────────────────────────────────────

  private callAnalytics<T>(method: string, request: any, requestId?: string): Promise<T> {
    return this.withCircuitBreaker(() => new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.AnalyticsServiceCtor(null, null, {
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

  getTransferAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getTransferAnalytics", req, requestId);
  }

  getFeeAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getFeeAnalytics", req, requestId);
  }

  getProviderAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getProviderAnalytics", req, requestId);
  }

  getReconciliationAnalytics(req: {
    tenantId: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getReconciliationAnalytics", req, requestId);
  }

  getDepositAnalytics(req: {
    tenantId: string;
    period?: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getDepositAnalytics", req, requestId);
  }

  createAnalyticsExport(req: {
    tenantId: string;
    exportType: string;
    period?: string;
    format?: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("createExportJob", req, requestId);
  }

  getAnalyticsExport(req: {
    tenantId: string;
    jobId: string;
  }, requestId?: string): Promise<any> {
    return this.callAnalytics("getExportJob", req, requestId);
  }
}

/** Create a SettlaGrpcClient from proto files. */
export function createGrpcClient(pool: GrpcPool): SettlaGrpcClient {
  return new SettlaGrpcClient(pool);
}
