export function getChartTheme(isDark: boolean) {
  return {
    textStyle: {
      color: isDark ? '#a1a1aa' : '#52525b',
      fontFamily: 'Inter, system-ui, sans-serif',
    },
    grid: {
      borderColor: isDark ? '#27272a' : '#e4e4e7',
    },
    tooltip: {
      backgroundColor: isDark ? '#18181b' : '#ffffff',
      borderColor: isDark ? '#3f3f46' : '#e4e4e7',
      textStyle: {
        color: isDark ? '#f4f4f5' : '#18181b',
        fontSize: 12,
      },
      borderWidth: 1,
    },
    xAxis: {
      axisLine: { lineStyle: { color: isDark ? '#3f3f46' : '#e4e4e7' } },
      axisTick: { lineStyle: { color: isDark ? '#3f3f46' : '#e4e4e7' } },
      axisLabel: { color: isDark ? '#71717a' : '#71717a' },
      splitLine: { lineStyle: { color: isDark ? '#27272a' : '#f4f4f5' } },
    },
    yAxis: {
      axisLine: { lineStyle: { color: isDark ? '#3f3f46' : '#e4e4e7' } },
      axisTick: { lineStyle: { color: isDark ? '#3f3f46' : '#e4e4e7' } },
      axisLabel: { color: isDark ? '#71717a' : '#71717a' },
      splitLine: { lineStyle: { color: isDark ? '#27272a' : '#f4f4f5' } },
    },
    animationDuration: 800,
    animationEasing: 'cubicOut' as const,
    color: ['#8b5cf6', '#6366f1', '#a78bfa', '#c4b5fd', '#818cf8', '#7c3aed', '#4f46e5', '#6d28d9'],
  }
}
