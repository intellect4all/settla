export function useAnimatedNumber(source: Ref<number> | ComputedRef<number>, duration = 600) {
  const display = ref(toValue(source))
  let raf: number | null = null

  watch(source, (to) => {
    if (raf) cancelAnimationFrame(raf)
    const from = display.value
    const start = performance.now()

    function tick(now: number) {
      const elapsed = now - start
      const progress = Math.min(elapsed / duration, 1)
      // ease-out cubic
      const eased = 1 - Math.pow(1 - progress, 3)
      display.value = from + (to - from) * eased
      if (progress < 1) {
        raf = requestAnimationFrame(tick)
      }
    }

    raf = requestAnimationFrame(tick)
  })

  onUnmounted(() => {
    if (raf) cancelAnimationFrame(raf)
  })

  return display
}
