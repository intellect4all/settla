<template>
  <span :class="[sizeClasses, colorClass]" class="font-mono tabular-nums">
    {{ formatted }}
  </span>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  amount: string | number
  currency?: string
  compact?: boolean
  size?: 'xs' | 'sm' | 'base' | 'lg' | 'xl' | '2xl'
  color?: 'default' | 'positive' | 'negative' | 'muted'
}>(), {
  size: 'base',
  color: 'default',
  compact: false,
})

const { format, formatCompact } = useMoney()

const formatted = computed(() => {
  if (props.compact) return formatCompact(props.amount, props.currency)
  return format(props.amount, props.currency)
})

const sizeClasses = computed(() => {
  const map: Record<string, string> = {
    xs: 'text-xs',
    sm: 'text-sm',
    base: 'text-base',
    lg: 'text-lg',
    xl: 'text-xl',
    '2xl': 'text-2xl',
  }
  return map[props.size]
})

const colorClass = computed(() => {
  const map: Record<string, string> = {
    default: 'text-surface-100',
    positive: 'text-emerald-400',
    negative: 'text-red-400',
    muted: 'text-surface-500',
  }
  return map[props.color]
})
</script>
