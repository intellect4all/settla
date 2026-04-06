import type { FastifyInstance } from "fastify";
import type { SettlaGrpcClient } from "../grpc/client.js";
import { errorResponseSchema } from "../schemas/index.js";
import { mapGrpcError } from "../errors.js";

/**
 * Maps a gRPC transfer response to a unified transaction verification response.
 */
function mapTransferToTransaction(t: any): any {
  return {
    type: "transfer",
    id: t.id,
    tenant_id: t.tenantId,
    status: t.status,
    external_ref: t.externalRef,
    source_currency: t.sourceCurrency,
    source_amount: t.sourceAmount,
    dest_currency: t.destCurrency,
    dest_amount: t.destAmount,
    chain: t.chain,
    fees: t.fees
      ? {
          on_ramp_fee: t.fees.onRampFee,
          network_fee: t.fees.networkFee,
          off_ramp_fee: t.fees.offRampFee,
          total_fee_usd: t.fees.totalFeeUsd,
        }
      : undefined,
    sender: t.sender
      ? {
          name: t.sender.name,
          country: t.sender.country,
        }
      : undefined,
    recipient: t.recipient
      ? {
          name: t.recipient.name,
          country: t.recipient.country,
        }
      : undefined,
    created_at: t.createdAt,
    updated_at: t.updatedAt,
    completed_at: t.completedAt,
    failed_at: t.failedAt,
    failure_reason: t.failureReason,
  };
}

/**
 * Maps a gRPC deposit session response to a unified transaction verification response.
 */
function mapDepositToTransaction(s: any): any {
  return {
    type: "deposit",
    id: s.id,
    tenant_id: s.tenantId,
    status: s.status,
    chain: s.chain,
    token: s.token,
    expected_amount: s.expectedAmount,
    received_amount: s.receivedAmount,
    deposit_address: s.depositAddress,
    transactions: (s.transactions || []).map((tx: any) => ({
      tx_hash: tx.txHash,
      amount: tx.amount,
      confirmations: tx.confirmations,
      status: tx.status,
    })),
    created_at: s.createdAt,
    updated_at: s.updatedAt,
    expires_at: s.expiresAt,
  };
}

const verifyResponseSchema = {
  type: "object" as const,
  properties: {
    type: { type: "string" as const, enum: ["transfer", "deposit"] },
    id: { type: "string" as const },
    tenant_id: { type: "string" as const },
    status: { type: "string" as const },
    external_ref: { type: "string" as const },
    source_currency: { type: "string" as const },
    source_amount: { type: "string" as const },
    dest_currency: { type: "string" as const },
    dest_amount: { type: "string" as const },
    chain: { type: "string" as const },
    token: { type: "string" as const },
    expected_amount: { type: "string" as const },
    received_amount: { type: "string" as const },
    deposit_address: { type: "string" as const },
    fees: {
      type: "object" as const,
      properties: {
        on_ramp_fee: { type: "string" as const },
        network_fee: { type: "string" as const },
        off_ramp_fee: { type: "string" as const },
        total_fee_usd: { type: "string" as const },
      },
    },
    sender: {
      type: "object" as const,
      properties: {
        name: { type: "string" as const },
        country: { type: "string" as const },
      },
    },
    recipient: {
      type: "object" as const,
      properties: {
        name: { type: "string" as const },
        country: { type: "string" as const },
      },
    },
    transactions: {
      type: "array" as const,
      items: {
        type: "object" as const,
        properties: {
          tx_hash: { type: "string" as const },
          amount: { type: "string" as const },
          confirmations: { type: "integer" as const },
          status: { type: "string" as const },
        },
      },
    },
    created_at: { type: "string" as const },
    updated_at: { type: "string" as const },
    completed_at: { type: "string" as const },
    failed_at: { type: "string" as const },
    failure_reason: { type: "string" as const },
    expires_at: { type: "string" as const },
  },
};

export async function verifyRoutes(
  app: FastifyInstance,
  opts: { grpc: SettlaGrpcClient },
): Promise<void> {
  const { grpc: grpcClient } = opts;

  // GET /v1/transactions/verify/:id — Verify a transaction by ID (transfer or deposit)
  app.get<{
    Params: { id: string };
  }>(
    "/v1/transactions/verify/:id",
    {
      schema: {
        tags: ["Verification"],
        summary: "Verify a transaction",
        operationId: "verifyTransaction",
        params: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
          },
          required: ["id"],
        },
        response: {
          200: verifyResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { id } = request.params;

      // Try transfer first, then deposit session — return whichever matches.
      const [transferResult, depositResult] = await Promise.allSettled([
        grpcClient.getTransfer(
          { tenantId: tenantAuth.tenantId, transferId: id },
          request.id,
          request,
        ),
        grpcClient.getDepositSession(
          { tenantId: tenantAuth.tenantId, sessionId: id },
          request.id,
          request,
        ),
      ]);

      // Check transfer
      if (
        transferResult.status === "fulfilled" &&
        transferResult.value?.transfer
      ) {
        return reply.send(
          mapTransferToTransaction(transferResult.value.transfer),
        );
      }

      // Check deposit
      if (
        depositResult.status === "fulfilled" &&
        depositResult.value?.session
      ) {
        return reply.send(
          mapDepositToTransaction(depositResult.value.session),
        );
      }

      // Both failed — return 404
      return reply.status(404).send({
        error: "NOT_FOUND",
        message: `No transfer or deposit session found with ID ${id}`,
        request_id: request.id,
      });
    },
  );

  // GET /v1/transactions/lookup — Look up a transaction by id, external reference, or tx hash
  app.get<{
    Querystring: { id?: string; reference?: string; tx_hash?: string; chain?: string };
  }>(
    "/v1/transactions/lookup",
    {
      schema: {
        tags: ["Verification"],
        summary: "Look up a transaction",
        operationId: "lookupTransaction",
        querystring: {
          type: "object",
          properties: {
            id: { type: "string", format: "uuid" },
            reference: { type: "string", minLength: 1, maxLength: 255 },
            tx_hash: { type: "string", minLength: 1, maxLength: 255 },
            chain: { type: "string", minLength: 1, maxLength: 50 },
          },
        },
        response: {
          200: verifyResponseSchema,
          400: errorResponseSchema,
          404: errorResponseSchema,
        },
      },
    },
    async (request, reply) => {
      const { tenantAuth } = request;
      const { id, reference, tx_hash } = request.query;

      // Exactly one lookup key must be provided
      const keyCount = [id, reference, tx_hash].filter(Boolean).length;
      if (keyCount === 0) {
        return reply.status(400).send({
          error: "BAD_REQUEST",
          message: "One of id, reference, or tx_hash query parameter is required",
          request_id: request.id,
        });
      }
      if (keyCount > 1) {
        return reply.status(400).send({
          error: "BAD_REQUEST",
          message: "Only one of id, reference, or tx_hash may be provided",
          request_id: request.id,
        });
      }

      if (id) {
        try {
          const [transferResult, depositResult] = await Promise.allSettled([
            grpcClient.getTransfer(
              { tenantId: tenantAuth.tenantId, transferId: id },
              request.id,
              request,
            ),
            grpcClient.getDepositSession(
              { tenantId: tenantAuth.tenantId, sessionId: id },
              request.id,
              request,
            ),
          ]);

          if (
            transferResult.status === "fulfilled" &&
            transferResult.value?.transfer
          ) {
            return reply.send(
              mapTransferToTransaction(transferResult.value.transfer),
            );
          }

          if (
            depositResult.status === "fulfilled" &&
            depositResult.value?.session
          ) {
            return reply.send(
              mapDepositToTransaction(depositResult.value.session),
            );
          }

          return reply.status(404).send({
            error: "NOT_FOUND",
            message: `No transfer or deposit session found with ID ${id}`,
            request_id: request.id,
          });
        } catch (err) {
          return mapGrpcError(request, reply, err);
        }
      }

      if (reference) {
        try {
          const result = await grpcClient.getTransferByExternalRef(
            { tenantId: tenantAuth.tenantId, externalRef: reference },
            request.id,
            request,
          );

          if (result.transfer) {
            return reply.send(
              mapTransferToTransaction(result.transfer),
            );
          }

          return reply.status(404).send({
            error: "NOT_FOUND",
            message: `No transfer found with external reference "${reference}"`,
            request_id: request.id,
          });
        } catch (err) {
          return mapGrpcError(request, reply, err);
        }
      }

      if (tx_hash) {
        try {
          const chain = request.query.chain || "tron";
          const result = await grpcClient.getDepositSessionByTxHash(
            { tenantId: tenantAuth.tenantId, txHash: tx_hash, chain },
            request.id,
            request,
          );

          if (result.session) {
            return reply.send(mapDepositToTransaction(result.session));
          }

          return reply.status(404).send({
            error: "NOT_FOUND",
            message: `No deposit found with transaction hash "${tx_hash}"`,
            request_id: request.id,
          });
        } catch (err: any) {
          if (err?.code === 5) {
            // gRPC NOT_FOUND
            return reply.status(404).send({
              error: "NOT_FOUND",
              message: `No deposit found with transaction hash "${tx_hash}"`,
              request_id: request.id,
            });
          }
          return mapGrpcError(request, reply, err);
        }
      }

      // Unreachable — all branches handled above
      return reply.status(400).send({
        error: "BAD_REQUEST",
        message: "Invalid lookup parameters",
        request_id: request.id,
      });
    },
  );
}
