export default defineEventHandler(async (event) => {
  const config = useRuntimeConfig()
  const apiBase: string = config.apiBase as string
  const body = await readBody(event)

  try {
    const res = await $fetch<any>(`${apiBase}/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body,
    })

    // Set httpOnly cookies — tokens never reach client JS
    const isProduction = process.env.NODE_ENV === 'production'

    setCookie(event, 'settla_portal_access', res.access_token, {
      httpOnly: true,
      secure: isProduction,
      sameSite: 'lax',
      path: '/',
      maxAge: 15 * 60, // 15 minutes
    })

    setCookie(event, 'settla_portal_refresh', res.refresh_token, {
      httpOnly: true,
      secure: isProduction,
      sameSite: 'lax',
      path: '/',
      maxAge: 7 * 24 * 60 * 60, // 7 days
    })

    // Return user data only — no tokens in response body
    return { user: res.user, tenant: res.tenant }
  } catch (err: any) {
    const status = err?.response?.status || err?.statusCode || 502
    const data = err?.data || { message: err?.message || 'Login failed' }
    throw createError({ statusCode: status, data })
  }
})
