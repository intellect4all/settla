<template>
  <div ref="chartRef" class="w-full" :style="{ height: `${height}px` }" />
</template>

<script setup lang="ts">
import * as echarts from 'echarts/core'
import { PieChart } from 'echarts/charts'
import { TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { CorridorMetric } from '~/types'

echarts.use([PieChart, TooltipComponent, LegendComponent, CanvasRenderer])

const props = withDefaults(defineProps<{
  corridors: CorridorMetric[]
  height?: number
  metric?: 'volume' | 'count' | 'fees'
}>(), { height: 240, metric: 'volume' })

const chartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null

const colors = ['#8b5cf6', '#10b981', '#f59e0b', '#3b82f6', '#ef4444', '#ec4899', '#14b8a6', '#f97316']

function buildOption() {
  const data = props.corridors.map((c, i) => {
    const label = `${c.source_currency} → ${c.dest_currency}`
    let value = 0
    switch (props.metric) {
      case 'count': value = c.transfer_count; break
      case 'fees': value = parseFloat(c.fees_usd || '0'); break
      default: value = parseFloat(c.volume_usd || '0'); break
    }
    return { name: label, value, itemStyle: { color: colors[i % colors.length] } }
  })

  return {
    tooltip: {
      trigger: 'item',
      backgroundColor: '#111827',
      borderColor: '#374151',
      textStyle: { color: '#e5e7eb', fontSize: 12 },
      formatter: (p: any) => {
        const val = props.metric === 'count' ? p.value : `$${p.value.toLocaleString()}`
        return `${p.name}<br/>${val} (${p.percent}%)`
      },
    },
    legend: {
      orient: 'vertical',
      right: 10,
      top: 'center',
      textStyle: { color: '#9ca3af', fontSize: 11 },
    },
    series: [{
      type: 'pie',
      radius: ['40%', '70%'],
      center: ['35%', '50%'],
      avoidLabelOverlap: false,
      label: { show: false },
      data,
    }],
  }
}

watch(() => [props.corridors, props.metric], () => {
  if (chart && props.corridors.length) {
    chart.setOption(buildOption(), true)
  }
}, { deep: true })

onMounted(() => {
  if (chartRef.value) {
    chart = echarts.init(chartRef.value, 'dark')
    if (props.corridors.length) {
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
