import { describe, it, expect } from "vitest";
import { computeSignature, verifySignature } from "../signature.js";

describe("HMAC-SHA256 signature", () => {
  const secret = "whsec_test_secret_key";
  const body = JSON.stringify({
    event: "transfer.completed",
    event_id: "evt_abc123",
    timestamp: "2025-02-06T17:15:32Z",
    data: { transfer_id: "t_001", status: "COMPLETED" },
  });

  it("computes a deterministic hex signature", () => {
    const sig1 = computeSignature(body, secret);
    const sig2 = computeSignature(body, secret);
    expect(sig1).toBe(sig2);
    expect(sig1).toMatch(/^[0-9a-f]{64}$/);
  });

  it("produces different signatures for different secrets", () => {
    const sig1 = computeSignature(body, secret);
    const sig2 = computeSignature(body, "other_secret");
    expect(sig1).not.toBe(sig2);
  });

  it("produces different signatures for different bodies", () => {
    const sig1 = computeSignature(body, secret);
    const sig2 = computeSignature("different body", secret);
    expect(sig1).not.toBe(sig2);
  });

  it("verifies a valid signature", () => {
    const sig = computeSignature(body, secret);
    expect(verifySignature(body, secret, sig)).toBe(true);
  });

  it("rejects an invalid signature", () => {
    expect(verifySignature(body, secret, "invalid")).toBe(false);
  });

  it("rejects a signature from the wrong secret", () => {
    const sig = computeSignature(body, "wrong_secret");
    expect(verifySignature(body, secret, sig)).toBe(false);
  });
});
