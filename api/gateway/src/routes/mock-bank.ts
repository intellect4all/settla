import type { FastifyInstance } from "fastify";
import { config } from "../config.js";

/**
 * Mock bank routes for local development and testing.
 * Simulates incoming bank credits to test the bank deposit flow end-to-end.
 *
 * Gated by NODE_ENV !== 'production' — never available in production.
 */
export async function mockBankRoutes(
  app: FastifyInstance,
  opts: { natsUrl: string },
): Promise<void> {
  const { natsUrl } = opts;

  // Block registration in production
  if (config.env === "production") {
    app.log.warn("Mock bank routes are disabled in production");
    return;
  }

  // Lazy NATS connection
  let natsConn: any = null;

  async function getNatsConnection() {
    if (natsConn) return natsConn;
    try {
      const nats = await import("nats");
      natsConn = await nats.connect({ servers: natsUrl });
      app.log.info("NATS connected for mock bank");
      return natsConn;
    } catch (err) {
      app.log.error({ err }, "Failed to connect to NATS for mock bank");
      return null;
    }
  }

  app.addHook("onClose", async () => {
    if (natsConn) {
      try { await natsConn.drain(); } catch { /* best-effort */ }
    }
  });

  // In-memory mock accounts for dev
  const mockAccounts: Array<{
    account_number: string;
    account_name: string;
    sort_code: string;
    iban: string;
    currency: string;
    balance: string;
  }> = [
    {
      account_number: "12345678",
      account_name: "Settla Dev Virtual Account 1",
      sort_code: "04-00-04",
      iban: "GB82WEST12345698765432",
      currency: "GBP",
      balance: "100000.00",
    },
    {
      account_number: "87654321",
      account_name: "Settla Dev Virtual Account 2",
      sort_code: "04-00-04",
      iban: "GB82WEST12345612345678",
      currency: "GBP",
      balance: "50000.00",
    },
    {
      account_number: "11223344",
      account_name: "Settla Dev EUR Account",
      sort_code: "",
      iban: "DE89370400440532013000",
      currency: "EUR",
      balance: "75000.00",
    },
  ];

  // POST /api/mock-bank/fund — Simulate an incoming bank credit
  app.post<{
    Body: {
      account_number: string;
      amount: string;
      currency: string;
      payer_name?: string;
      payer_account_number?: string;
      payer_reference?: string;
      bank_reference?: string;
    };
  }>(
    "/api/mock-bank/fund",
    {
      schema: {
        body: {
          type: "object",
          properties: {
            account_number: { type: "string", minLength: 1 },
            amount: { type: "string", pattern: "^\\d+\\.?\\d*$" },
            currency: { type: "string", minLength: 1 },
            payer_name: { type: "string" },
            payer_account_number: { type: "string" },
            payer_reference: { type: "string" },
            bank_reference: { type: "string" },
          },
          required: ["account_number", "amount", "currency"],
        },
        response: {
          200: {
            type: "object",
            properties: {
              status: { type: "string" },
              bank_reference: { type: "string" },
            },
          },
        },
      },
    },
    async (request, reply) => {
      const body = request.body;
      const bankReference = body.bank_reference || `mock-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

      const creditEvent = {
        partnerId: "mock-bank",
        accountNumber: body.account_number,
        amount: body.amount,
        currency: body.currency.toUpperCase(),
        payerName: body.payer_name || "Mock Payer",
        payerAccountNumber: body.payer_account_number || "00000000",
        payerReference: body.payer_reference || "mock-payment",
        bankReference,
        receivedAt: new Date().toISOString(),
      };

      // Publish directly to NATS (bypasses HMAC — dev only)
      try {
        const nc = await getNatsConnection();
        if (nc) {
          const nats = await import("nats");
          const sc = nats.StringCodec();
          nc.publish(
            "settla.inbound.bank.credit.received",
            sc.encode(JSON.stringify(creditEvent)),
          );
          app.log.info(
            { bankReference, accountNumber: body.account_number, amount: body.amount },
            "Mock bank credit published",
          );
        } else {
          return reply.status(503).send({ error: "NATS unavailable" });
        }
      } catch (err) {
        app.log.error({ err }, "Failed to publish mock bank credit");
        return reply.status(503).send({ error: "publish_failed" });
      }

      return reply.status(200).send({
        status: "funded",
        bank_reference: bankReference,
      });
    },
  );

  // GET /api/mock-bank/accounts — List mock accounts
  app.get(
    "/api/mock-bank/accounts",
    {
      schema: {
        response: {
          200: {
            type: "object",
            properties: {
              accounts: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    account_number: { type: "string" },
                    account_name: { type: "string" },
                    sort_code: { type: "string" },
                    iban: { type: "string" },
                    currency: { type: "string" },
                    balance: { type: "string" },
                  },
                },
              },
            },
          },
        },
      },
    },
    async (_request, reply) => {
      return reply.send({ accounts: mockAccounts });
    },
  );
}
