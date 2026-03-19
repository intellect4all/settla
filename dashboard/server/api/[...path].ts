/**
 * Catch-all server proxy for dashboard API requests.
 *
 * The client calls /api/<path> and this handler forwards the request to the
 * upstream gateway, injecting the appropriate API key server-side so that
 * secrets never reach the browser.
 *
 * - /api/v1/ops/*  -> forwards with X-Ops-Api-Key header
 * - /api/v1/*      -> forwards with Authorization: Bearer <dashboardApiKey>
 * - /api/health    -> forwards without auth
 */
export default defineEventHandler(async (event) => {
  const config = useRuntimeConfig()
  const apiBase: string = config.apiBase as string
  const dashboardApiKey: string = config.dashboardApiKey as string
  const opsApiKey: string = (config.opsApiKey as string) || dashboardApiKey

  // The path param captures everything after /api/
  const path = event.context.params?.path ?? ''
  const targetUrl = `${apiBase}/${path}`

  // Read the incoming request body (if any)
  const method = event.method
  let body: string | undefined
  if (method !== 'GET' && method !== 'HEAD') {
    body = await readBody(event)
  }

  // Build upstream headers — never forward the browser's Authorization header
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }

  const isOpsRoute = path.startsWith('v1/ops/')
  const isHealthRoute = path === 'health'

  if (isOpsRoute) {
    if (!opsApiKey) {
      throw createError({ statusCode: 500, statusMessage: 'Ops API key not configured on server' })
    }
    headers['X-Ops-Api-Key'] = opsApiKey
  } else if (!isHealthRoute) {
    if (!dashboardApiKey) {
      throw createError({ statusCode: 500, statusMessage: 'Dashboard API key not configured on server' })
    }
    headers['Authorization'] = `Bearer ${dashboardApiKey}`
  }

  // Proxy the request
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
