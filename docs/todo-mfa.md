# MFA for Operations Dashboard

## Status: TODO — Deferred to dedicated security sprint

## Problem
The operations dashboard currently uses single-factor authentication (API key or email/password). A compromised credential gives an attacker full access to:
- Tenant management (suspend, approve KYB, change fees/limits)
- Settlement operations (mark as paid)
- Manual review decisions (approve/reject)
- System reconciliation triggers

## Recommended Architecture

### TOTP (Time-based One-Time Password)
- **Library:** `otplib` npm package (lightweight, no external dependencies)
- **Algorithm:** SHA-1, 6 digits, 30-second step (RFC 6238 compliant)

### Setup Flow
1. User navigates to Settings > Security > Enable MFA
2. Server generates TOTP secret (`otplib.authenticator.generateSecret()`)
3. Server returns QR code URI (`otplib.authenticator.keyuri(email, 'Settla Ops', secret)`)
4. User scans QR code with authenticator app (Google Authenticator, Authy, 1Password)
5. User enters first TOTP code to verify setup
6. Server stores encrypted TOTP secret in `portal_users.totp_secret_encrypted`
7. Server generates 10 backup codes (random 8-char alphanumeric), stores hashed
8. User downloads backup codes (shown once)

### Login Flow
1. User enters email + password, server validates credentials
2. If MFA enabled: server returns `{ mfa_required: true, mfa_session_token: "..." }`
3. User enters TOTP code (or backup code)
4. Server validates: `otplib.authenticator.verify({ token: code, secret: decryptedSecret })`
5. If backup code: mark as used (single-use)
6. On success: issue full session JWT

### Database Changes
Add to `portal_users` table (new migration):
```sql
ALTER TABLE portal_users ADD COLUMN totp_secret_encrypted TEXT;
ALTER TABLE portal_users ADD COLUMN totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE portal_users ADD COLUMN backup_codes_hash TEXT[];  -- bcrypt hashed
ALTER TABLE portal_users ADD COLUMN mfa_enforced_at TIMESTAMPTZ;
```

### Enforcement Policy
- **Phase 1:** Optional — users can enable MFA from settings
- **Phase 2:** Required — all ops dashboard users must enable MFA within 7 days
- **Phase 3:** Enforced — login blocked without MFA for ops roles

### Alternative: SSO Integration
For enterprise tenants, consider Okta/Auth0 SSO integration:
- SAML 2.0 or OIDC protocol
- Delegates MFA enforcement to identity provider
- Reduces credential management burden

### Estimated Effort
- Backend (TOTP setup, verify, backup codes): 2 days
- Frontend (QR code display, MFA prompt, settings page): 1.5 days
- Testing (unit + integration + manual): 1 day
- **Total: ~4.5 days**
