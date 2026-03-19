import { createHmac } from "node:crypto";

export interface JwtPayload {
  sub: string;   // user ID
  tid: string;   // tenant ID
  role: string;  // OWNER, ADMIN, MEMBER
  type: string;  // access, refresh
  iss: string;   // issuer
  aud: string;   // audience (mandatory — prevents cross-service token reuse)
  iat: number;   // issued at (epoch seconds)
  exp: number;   // expires at (epoch seconds)
  nbf?: number;  // not before (epoch seconds, optional)
}

/** Expected JWT issuer claim. Tokens with a different iss are rejected. */
const EXPECTED_ISSUER = process.env.SETTLA_JWT_ISSUER || "settla";

/**
 * Verify and decode a JWT token signed with HS256.
 * Accepts a single secret or an array of secrets for key rotation.
 * When multiple secrets are provided (e.g. SETTLA_JWT_SECRET=new_secret,old_secret),
 * each is tried in order — the first successful verification wins.
 * Returns the decoded payload or null if invalid/expired.
 */
export function verifyJwt(token: string, secret: string | string[]): JwtPayload | null {
  const secrets = Array.isArray(secret) ? secret : [secret];

  const parts = token.split(".");
  if (parts.length !== 3) return null;

  const [headerB64, payloadB64, signatureB64] = parts;

  // Decode header and verify algorithm (only needs to be done once)
  try {
    const header = JSON.parse(base64UrlDecode(headerB64));
    if (header.alg !== "HS256") return null;
  } catch {
    return null;
  }

  // Decode payload (only needs to be done once)
  let payload: JwtPayload;
  try {
    payload = JSON.parse(base64UrlDecode(payloadB64)) as JwtPayload;
  } catch {
    return null;
  }

  // Try each secret for signature verification (supports key rotation)
  const data = `${headerB64}.${payloadB64}`;
  let signatureValid = false;
  for (const s of secrets) {
    const expectedSig = base64UrlEncode(
      createHmac("sha256", s).update(data).digest(),
    );
    if (expectedSig === signatureB64) {
      signatureValid = true;
      break;
    }
  }

  if (!signatureValid) return null;

  // Check expiry
  if (payload.exp && payload.exp < Math.floor(Date.now() / 1000)) {
    return null;
  }

  // Reject tokens not yet valid (nbf = not-before)
  if (payload.nbf && typeof payload.nbf === "number") {
    const now = Math.floor(Date.now() / 1000);
    if (payload.nbf > now + 30) { // 30s clock skew tolerance
      return null;
    }
  }

  // Validate issuer — reject tokens from unexpected issuers
  if (!payload.iss || payload.iss !== EXPECTED_ISSUER) {
    return null;
  }

  // Validate audience — prevents token reuse across services.
  // Audience is mandatory; tokens without aud are rejected to prevent
  // cross-service token reuse. Set SETTLA_JWT_AUDIENCE to match the issuing service.
  const expectedAudience = process.env.SETTLA_JWT_AUDIENCE || "settla-gateway";
  if (!payload.aud || payload.aud !== expectedAudience) {
    return null;
  }

  return payload;
}

function base64UrlEncode(buf: Buffer): string {
  return buf
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function base64UrlDecode(str: string): string {
  const padded = str + "=".repeat((4 - (str.length % 4)) % 4);
  return Buffer.from(padded.replace(/-/g, "+").replace(/_/g, "/"), "base64").toString("utf8");
}
