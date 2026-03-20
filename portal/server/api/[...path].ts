/**
 * Catch-all server proxy for portal API requests.
 *
 * The client calls /api/<path> and this handler forwards the request to the
 * upstream gateway, injecting the access token from the httpOnly cookie so
 * that secrets never reach the browser.
 */
export default defineEventHandler(async (event) => {
  const config = useRuntimeConfig()
  const apiBase: string = config.apiBase as string

  const path = event.context.params?.path ?? ''
  const targetUrl = `${apiBase}/${path}`

  // Read body if not GET/HEAD
  const method = event.method
  let body: any
  if (method !== 'GET' && method !== 'HEAD') {
    body = await readBody(event)
  }

  // Build upstream headers — inject access token from httpOnly cookie
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }

  // Skip auth injection for auth routes (handled by dedicated handlers)
  const isAuthRoute = path.startsWith('v1/auth/')
  if (!isAuthRoute) {
    const accessToken = getCookie(event, 'settla_portal_access')
    if (accessToken) {
      headers['Authorization'] = `Bearer ${accessToken}`
    }
  }

  try {
    const response = await $fetch.raw(targetUrl, {
      method: method as any,
      headers,
      body: body ? (typeof body === 'string' ? body : JSON.stringify(body)) : undefined,
    })
    return response._data
  } catch (err: any) {
    const status = err?.response?.status || err?.statusCode || 502
    const message = err?.data?.message || err?.message || 'Upstream request failed'
    throw createError({ statusCode: status, statusMessage: message })
  }
})
