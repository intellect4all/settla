// Server-side session endpoint for httpOnly cookie-based auth.
// Decodes the JWT payload from the access token cookie for SSR hydration.
// The token itself is never sent to the client — only the extracted user fields.
export default defineEventHandler(async (event) => {
  const accessToken = getCookie(event, 'settla_portal_access')
  if (!accessToken) {
    return { authenticated: false, user: null }
  }

  try {
    // Decode JWT payload (base64url, no verification — just for SSR hydration)
    const parts = accessToken.split('.')
    if (parts.length !== 3) {
      return { authenticated: false, user: null }
    }
    const payload = JSON.parse(
      Buffer.from(parts[1].replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString()
    )

    // Check expiry
    if (payload.exp && payload.exp < Math.floor(Date.now() / 1000)) {
      return { authenticated: false, user: null }
    }

    return {
      authenticated: true,
      user: {
        id: payload.sub,
        tenant_id: payload.tid,
        role: payload.role,
        email: payload.email,
      },
    }
  } catch {
    return { authenticated: false, user: null }
  }
})
