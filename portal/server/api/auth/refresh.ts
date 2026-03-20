export default defineEventHandler(async (event) => {
  const config = useRuntimeConfig()
  const apiBase: string = config.apiBase as string
  const refreshToken = getCookie(event, 'settla_portal_refresh')

  if (!refreshToken) {
    throw createError({ statusCode: 401, statusMessage: 'No refresh token' })
  }

  try {
    const res = await $fetch<any>(`${apiBase}/v1/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: { refresh_token: refreshToken },
    })

    const isProduction = process.env.NODE_ENV === 'production'

    setCookie(event, 'settla_portal_access', res.access_token, {
      httpOnly: true,
      secure: isProduction,
      sameSite: 'lax',
      path: '/',
      maxAge: 15 * 60,
    })

    // Rotate refresh token if returned
    if (res.refresh_token) {
      setCookie(event, 'settla_portal_refresh', res.refresh_token, {
        httpOnly: true,
        secure: isProduction,
        sameSite: 'lax',
        path: '/',
        maxAge: 7 * 24 * 60 * 60,
      })
    }

    return { user: res.user }
  } catch (err: any) {
    // Clear invalid cookies
    deleteCookie(event, 'settla_portal_access')
    deleteCookie(event, 'settla_portal_refresh')
    const status = err?.response?.status || err?.statusCode || 401
    throw createError({ statusCode: status, statusMessage: 'Session expired' })
  }
})
