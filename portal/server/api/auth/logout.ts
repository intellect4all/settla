export default defineEventHandler(async (event) => {
  deleteCookie(event, 'settla_portal_access')
  deleteCookie(event, 'settla_portal_refresh')
  return { ok: true }
})
