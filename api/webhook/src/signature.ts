import { createHmac, timingSafeEqual } from "node:crypto";

/**
 * Compute HMAC-SHA256 signature over raw body using the webhook secret.
 */
export function computeSignature(body: string, secret: string): string {
  return createHmac("sha256", secret).update(body, "utf8").digest("hex");
}

/**
 * Verify an HMAC-SHA256 signature using timing-safe comparison.
 */
export function verifySignature(
  body: string,
  secret: string,
  signature: string
): boolean {
  const expected = computeSignature(body, secret);
  if (expected.length !== signature.length) return false;
  return timingSafeEqual(Buffer.from(expected), Buffer.from(signature));
}
