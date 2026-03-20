<template>
  <div ref="chartRef" class="w-full" :style="{ height: `${height}px` }" />
</template>

<script setup lang="ts">
import * as echarts from 'echarts/core'
import { BarChart } from 'echarts/charts'
import { TooltipComponent, GridComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { LatencyPercentiles } from '~/types'
import { getChartTheme } from '~/utils/chartTheme'

echarts.use([BarChart, TooltipComponent, GridComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  latency: LatencyPercentiles | null
  height?: number
}>(), { height: 180 })

const colorMode = useColorMode()
const chartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null

function buildOption() {
  if (!props.latency) return {}

  const isDark = colorMode.value === 'dark'
  const theme = getChartTheme(isDark)

  const labels = ['P50', 'P90', 'P95', 'P99']
  const values = [
    props.latency.p50_ms,
    props.latency.p90_ms,
    props.latency.p95_ms,
    props.latency.p99_ms,
  ]

  return {
    textStyle: theme.textStyle,
    tooltip: {
      trigger: 'axis',
      ...theme.tooltip,
      formatter: (params: any) => `${params[0].name}: ${params[0].value}ms`,
    },
    grid: { left: 50, right: 20, top: 10, bottom: 30, borderColor: theme.grid.borderColor },
    xAxis: {
      type: 'category',
      data: labels,
      axisLabel: { ...theme.xAxis.axisLabel, fontSize: 11 },
      axisLine: theme.xAxis.axisLine,
    },
    yAxis: {
      type: 'value',
      name: 'ms',
      axisLabel: { ...theme.yAxis.axisLabel, fontSize: 10 },
      axisLine: { show: false },
      splitLine: { lineStyle: theme.yAxis.splitLine.lineStyle },
    },
    series: [{
      type: 'bar',
      data: values.map((v, i) => ({
        value: v,
        itemStyle: {
          color: i < 2 ? '#8b5cf6' : i === 2 ? '#f59e0b' : '#ef4444',
        },
      })),
      barMaxWidth: 40,
      label: {
        show: true,
        position: 'top',
        color: theme.textStyle.color,
        fontSize: 10,
        formatter: (p: any) => `${p.value}ms`,
      },
    }],
    animationDuration: 800,
    animationEasing: 'cubicOut' as const,
  }
}

watch(() => props.latency, () => {
  if (chart && props.latency) {
    chart.setOption(buildOption(), true)
  }
}, { deep: true })

watch(() => colorMode.value, () => {
  if (chart) {
    chart.setOption(buildOption(), true)
  }
})

onMounted(() => {
  if (chartRef.value) {
    chart = echarts.init(chartRef.value)
    if (props.latency) {
      chart.setOption(buildOption())
    }

    const ro = new ResizeObserver(() => chart?.resize())
    ro.observe(chartRef.value)
    onUnmounted(() => {
      ro.disconnect()
      chart?.dispose()
    })
  }
})
</script>
