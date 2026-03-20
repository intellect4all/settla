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
import { getChartTheme } from '~/utils/chartTheme'

echarts.use([LineChart, BarChart, TooltipComponent, GridComponent, LegendComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  buckets: TransferStatsBucket[]
  height?: number
  showVolume?: boolean
}>(), { height: 240, showVolume: true })

const colorMode = useColorMode()
const chartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null

function buildOption() {
  const isDark = colorMode.value === 'dark'
  const theme = getChartTheme(isDark)
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
      axisLabel: { ...theme.yAxis.axisLabel, fontSize: 10 },
      axisLine: { show: false },
      splitLine: { lineStyle: theme.yAxis.splitLine.lineStyle },
    },
  ]

  if (props.showVolume) {
    yAxis.push({
      type: 'value',
      name: 'Volume (USD)',
      axisLabel: {
        ...theme.yAxis.axisLabel,
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
    textStyle: theme.textStyle,
    tooltip: {
      trigger: 'axis',
      ...theme.tooltip,
    },
    legend: {
      data: series.map(s => s.name),
      textStyle: { color: theme.textStyle.color, fontSize: 11 },
      top: 0,
    },
    grid: { left: 50, right: props.showVolume ? 70 : 20, top: 30, bottom: 30, borderColor: theme.grid.borderColor },
    xAxis: {
      type: 'category',
      data: timestamps,
      axisLabel: { ...theme.xAxis.axisLabel, fontSize: 10, rotate: 30 },
      axisLine: theme.xAxis.axisLine,
    },
    yAxis,
    series,
    animationDuration: 800,
    animationEasing: 'cubicOut' as const,
  }
}

watch(() => props.buckets, () => {
  if (chart && props.buckets.length) {
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
