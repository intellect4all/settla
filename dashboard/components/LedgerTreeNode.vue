<template>
  <div>
    <button
      :style="{ paddingLeft: depth * 20 + 'px' }"
      class="flex items-center gap-2 w-full px-3 py-2 text-sm rounded-lg transition-colors hover:bg-surface-800 focus-ring"
      :class="selectedCode === account.code ? 'bg-surface-800 text-violet-400' : 'text-surface-300'"
      @click="toggle"
    >
      <!-- Expand/collapse icon -->
      <span v-if="hasChildren" class="w-4 text-center text-surface-500 transition-transform" :class="expanded ? 'rotate-90' : ''">
        <Icon name="chevron-right" :size="12" />
      </span>
      <span v-else class="w-4" />

      <!-- Account info -->
      <span class="font-mono text-xs text-surface-500">{{ shortCode }}</span>
      <span class="flex-1 text-left">{{ account.name || shortCode }}</span>

      <!-- Balance -->
      <MoneyDisplay
        v-if="isLeaf"
        :amount="account.balance"
        :currency="account.currency"
        size="xs"
        color="muted"
      />
    </button>

    <!-- Children -->
    <div v-if="hasChildren && expanded">
      <LedgerTreeNode
        v-for="child in account.children"
        :key="child.code"
        :account="child"
        :depth="depth + 1"
        :selected-code="selectedCode"
        @select="$emit('select', $event)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import type { LedgerAccount } from '~/types'

const props = defineProps<{
  account: LedgerAccount
  depth: number
  selectedCode: string
}>()

const emit = defineEmits<{
  select: [code: string]
}>()

const expanded = ref(props.depth < 1)

const hasChildren = computed(() => props.account.children && props.account.children.length > 0)
const isLeaf = computed(() => !hasChildren.value)
const shortCode = computed(() => {
  const parts = props.account.code.split(':')
  return parts[parts.length - 1]
})

function toggle() {
  if (hasChildren.value) {
    expanded.value = !expanded.value
  }
  emit('select', props.account.code)
}
</script>
