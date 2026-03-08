export function usePolling<T>(
  fetcher: () => Promise<T>,
  intervalMs: number,
  options: { immediate?: boolean } = { immediate: true },
) {
  const data = ref<T | null>(null) as Ref<T | null>
  const error = ref<Error | null>(null)
  const loading = ref(false)
  let timer: ReturnType<typeof setInterval> | null = null

  async function refresh() {
    loading.value = true
    error.value = null
    try {
      data.value = await fetcher()
    } catch (e) {
      error.value = e instanceof Error ? e : new Error(String(e))
    } finally {
      loading.value = false
    }
  }

  function start() {
    stop()
    if (options.immediate) refresh()
    timer = setInterval(refresh, intervalMs)
  }

  function stop() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  onMounted(start)
  onUnmounted(stop)

  return { data, error, loading, refresh, start, stop }
}
