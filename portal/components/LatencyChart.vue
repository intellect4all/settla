<template>
  <div ref="chartRef" class="w-full" :style="{ height: `${height}px` }" />
</template>

<script setup lang="ts">
import * as echarts from 'echarts/core'
import { BarChart } from 'echarts/charts'
import { TooltipComponent, GridComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { LatencyPercentiles } from '~/types'

echarts.use([BarChart, TooltipComponent, GridComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  latency: LatencyPercentiles | null
  height?: number
}>(), { height: 180 })

const chartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null

function buildOption() {
  if (!props.latency) return {}

  const labels = ['P50', 'P90', 'P95', 'P99']
  const values = [
    props.latency.p50_ms,
    props.latency.p90_ms,
    props.latency.p95_ms,
    props.latency.p99_ms,
  ]

  return {
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#111827',
      borderColor: '#374151',
      textStyle: { color: '#e5e7eb', fontSize: 12 },
      formatter: (params: any) => `${params[0].name}: ${params[0].value}ms`,
    },
    grid: { left: 50, right: 20, top: 10, bottom: 30 },
    xAxis: {
      type: 'category',
      data: labels,
      axisLabel: { color: '#9ca3af', fontSize: 11 },
      axisLine: { lineStyle: { color: '#374151' } },
    },
    yAxis: {
      type: 'value',
      name: 'ms',
      axisLabel: { color: '#6b7280', fontSize: 10 },
      axisLine: { show: false },
      splitLine: { lineStyle: { color: '#1f2937' } },
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
        color: '#9ca3af',
        fontSize: 10,
        formatter: (p: any) => `${p.value}ms`,
      },
    }],
  }
}

watch(() => props.latency, () => {
  if (chart && props.latency) {
    chart.setOption(buildOption(), true)
  }
}, { deep: true })

onMounted(() => {
  if (chartRef.value) {
    chart = echarts.init(chartRef.value, 'dark')
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
