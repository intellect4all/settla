<template>
  <div ref="chartRef" class="w-full" :style="{ height: `${height}px` }" />
</template>

<script setup lang="ts">
import * as echarts from 'echarts/core'
import { LineChart, BarChart } from 'echarts/charts'
import {
  TooltipComponent, GridComponent, LegendComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { TransferStatsBucket } from '~/types'

echarts.use([LineChart, BarChart, TooltipComponent, GridComponent, LegendComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  buckets: TransferStatsBucket[]
  height?: number
  showVolume?: boolean
}>(), { height: 240, showVolume: true })

const chartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null

function buildOption() {
  const timestamps = props.buckets.map(b =>
    new Date(b.timestamp).toLocaleString('en-GB', { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' }),
  )

  const series: any[] = [
    {
      name: 'Completed',
      type: 'bar',
      stack: 'transfers',
      data: props.buckets.map(b => b.completed),
      itemStyle: { color: '#10b981' },
      barMaxWidth: 20,
    },
    {
      name: 'Failed',
      type: 'bar',
      stack: 'transfers',
      data: props.buckets.map(b => b.failed),
      itemStyle: { color: '#ef4444' },
      barMaxWidth: 20,
    },
  ]

  if (props.showVolume) {
    series.push({
      name: 'Volume (USD)',
      type: 'line',
      yAxisIndex: 1,
      data: props.buckets.map(b => parseFloat(b.volume_usd || '0')),
      lineStyle: { color: '#8b5cf6', width: 2 },
      itemStyle: { color: '#8b5cf6' },
      smooth: true,
      symbol: 'none',
    })
  }

  const yAxis: any[] = [
    {
      type: 'value',
      name: 'Transfers',
      axisLabel: { color: '#6b7280', fontSize: 10 },
      axisLine: { show: false },
      splitLine: { lineStyle: { color: '#1f2937' } },
    },
  ]

  if (props.showVolume) {
    yAxis.push({
      type: 'value',
      name: 'Volume (USD)',
      axisLabel: {
        color: '#6b7280',
        fontSize: 10,
        formatter: (v: number) => {
          if (v >= 1e6) return `$${(v / 1e6).toFixed(1)}M`
          if (v >= 1e3) return `$${(v / 1e3).toFixed(0)}K`
          return `$${v}`
        },
      },
      axisLine: { show: false },
      splitLine: { show: false },
    })
  }

  return {
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#111827',
      borderColor: '#374151',
      textStyle: { color: '#e5e7eb', fontSize: 12 },
    },
    legend: {
      data: series.map(s => s.name),
      textStyle: { color: '#9ca3af', fontSize: 11 },
      top: 0,
    },
    grid: { left: 50, right: props.showVolume ? 70 : 20, top: 30, bottom: 30 },
    xAxis: {
      type: 'category',
      data: timestamps,
      axisLabel: { color: '#6b7280', fontSize: 10, rotate: 30 },
      axisLine: { lineStyle: { color: '#374151' } },
    },
    yAxis,
    series,
  }
}

watch(() => props.buckets, () => {
  if (chart && props.buckets.length) {
    chart.setOption(buildOption(), true)
  }
}, { deep: true })

onMounted(() => {
  if (chartRef.value) {
    chart = echarts.init(chartRef.value, 'dark')
    if (props.buckets.length) {
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
