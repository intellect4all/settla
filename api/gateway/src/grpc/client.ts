import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { GrpcPool } from "./pool.js";

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

  constructor(pool: GrpcPool) {
    this.pool = pool;
    const p = loadProto();
    this.SettlementServiceCtor = p.settla.v1.SettlementService;
    this.TreasuryServiceCtor = p.settla.v1.TreasuryService;
    this.AuthServiceCtor = p.settla.v1.AuthService;
  }

  /** Promise wrapper for gRPC unary calls. */
  private callSettlement<T>(method: string, request: any): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.SettlementServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();

      client[method](
        request,
        meta,
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    });
  }

  private callTreasury<T>(method: string, request: any): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.TreasuryServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();

      client[method](
        request,
        meta,
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    });
  }

  private callAuth<T>(method: string, request: any): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const channel = this.pool.getChannel();
      const client = new this.AuthServiceCtor(null, null, {
        channelOverride: channel,
      });
      const meta = new grpc.Metadata();

      client[method](
        request,
        meta,
        (error: grpc.ServiceError | null, response: T) => {
          if (error) reject(error);
          else resolve(response);
        },
      );
    });
  }

  // ── Auth Service ────────────────────────────────────────────────────────

  validateApiKey(req: { keyHash: string }): Promise<{
    valid: boolean;
    tenantId: string;
    slug: string;
    status: string;
    feeScheduleJson: string;
    dailyLimitUsd: string;
    perTransferLimit: string;
  }> {
    return this.callAuth("ValidateAPIKey", req);
  }

  // ── Settlement Service ──────────────────────────────────────────────────

  createQuote(req: {
    tenantId: string;
    sourceCurrency: string;
    sourceAmount: string;
    destCurrency: string;
    destCountry?: string;
  }): Promise<any> {
    return this.callSettlement("createQuote", req);
  }

  getQuote(req: { tenantId: string; quoteId: string }): Promise<any> {
    return this.callSettlement("getQuote", req);
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
  }): Promise<any> {
    return this.callSettlement("createTransfer", req);
  }

  getTransfer(req: { tenantId: string; transferId: string }): Promise<any> {
    return this.callSettlement("getTransfer", req);
  }

  listTransfers(req: {
    tenantId: string;
    pageSize?: number;
    pageToken?: string;
  }): Promise<any> {
    return this.callSettlement("listTransfers", req);
  }

  cancelTransfer(req: {
    tenantId: string;
    transferId: string;
    reason?: string;
  }): Promise<any> {
    return this.callSettlement("cancelTransfer", req);
  }

  // ── Treasury Service ────────────────────────────────────────────────────

  getPositions(req: { tenantId: string }): Promise<any> {
    return this.callTreasury("getPositions", req);
  }

  getPosition(req: {
    tenantId: string;
    currency: string;
    location: string;
  }): Promise<any> {
    return this.callTreasury("getPosition", req);
  }

  getLiquidityReport(req: { tenantId: string }): Promise<any> {
    return this.callTreasury("getLiquidityReport", req);
  }
}

/** Create a SettlaGrpcClient from proto files. */
export function createGrpcClient(pool: GrpcPool): SettlaGrpcClient {
  return new SettlaGrpcClient(pool);
}
