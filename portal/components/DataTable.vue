<template>
  <div class="card overflow-hidden">
    <div v-if="title || searchable" class="flex items-center justify-between px-4 py-3 border-b border-surface-800">
      <h3 v-if="title" class="text-sm font-semibold text-surface-200">{{ title }}</h3>
      <div v-if="searchable" class="relative">
        <Icon name="search" :size="14" class="absolute left-3 top-1/2 -translate-y-1/2 text-surface-500" />
        <input v-model="searchInput" type="text" :placeholder="searchPlaceholder" class="input text-sm w-64 pl-8" />
      </div>
    </div>
    <div
      ref="scrollContainerRef"
      class="overflow-x-auto"
      :class="{ 'overflow-y-auto': useVirtual }"
      :style="useVirtual ? { maxHeight: `${virtualContainerHeight}px` } : {}"
      @scroll="useVirtual ? onScroll() : undefined"
    >
      <table class="w-full text-sm" role="table">
        <thead>
          <tr class="border-b border-surface-800">
            <th
              v-for="col in columns" :key="col.key"
              :class="[col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : 'text-left', col.sortable ? 'cursor-pointer select-none hover:text-surface-200' : '']"
              :style="col.width ? { width: col.width } : {}"
              :aria-sort="col.sortable && sortKey === col.key ? (sortOrder === 'asc' ? 'ascending' : 'descending') : undefined"
              :role="col.sortable ? 'button' : undefined"
              :tabindex="col.sortable ? 0 : undefined"
              class="px-4 py-3 text-xs font-medium text-surface-500 uppercase tracking-wider"
              @click="col.sortable ? toggleSort(col.key) : null"
              @keydown.enter="col.sortable ? toggleSort(col.key) : null"
              @keydown.space.prevent="col.sortable ? toggleSort(col.key) : null"
            >
              <span class="inline-flex items-center gap-1">
                {{ col.label }}
                <Icon v-if="col.sortable && sortKey === col.key" :name="sortOrder === 'asc' ? 'chevron-up' : 'chevron-down'" :size="12" class="text-violet-400" />
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <!-- Skeleton rows when loading -->
          <template v-if="loading">
            <tr v-for="i in 5" :key="`skeleton-${i}`" class="border-b border-surface-800/50 last:border-0">
              <td v-for="col in columns" :key="col.key" class="px-4 py-3">
                <div class="skeleton h-4 rounded" :style="{ width: `${50 + Math.random() * 40}%` }" />
              </td>
            </tr>
          </template>
          <!-- Data rows with virtual scrolling -->
          <template v-else-if="useVirtual">
            <!-- Spacer for rows above the visible window -->
            <tr v-if="virtualStart > 0" aria-hidden="true">
              <td :colspan="columns.length" :style="{ height: `${virtualStart * rowHeight}px`, padding: 0, border: 'none' }" />
            </tr>
            <tr
              v-for="(row, idx) in virtualRows" :key="rowKey ? row[rowKey] : virtualStart + idx"
              class="border-b border-surface-800/50 last:border-0 hover:bg-surface-800/50 transition-colors duration-150 cursor-pointer"
              :style="{ height: `${rowHeight}px` }"
              @click="$emit('row-click', row)"
            >
              <td v-for="col in columns" :key="col.key" :class="col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : ''" class="px-4 py-3 text-surface-300">
                <slot :name="`cell-${col.key}`" :value="row[col.key]" :row="row">
                  {{ col.render ? col.render(row[col.key], row) : row[col.key] ?? '&#8212;' }}
                </slot>
              </td>
            </tr>
            <!-- Spacer for rows below the visible window -->
            <tr v-if="virtualEnd < paginatedRows.length" aria-hidden="true">
              <td :colspan="columns.length" :style="{ height: `${(paginatedRows.length - virtualEnd) * rowHeight}px`, padding: 0, border: 'none' }" />
            </tr>
            <tr v-if="paginatedRows.length === 0">
              <td :colspan="columns.length" class="px-4 py-8 text-center text-surface-500">{{ emptyMessage }}</td>
            </tr>
          </template>
          <!-- Data rows (regular rendering) -->
          <template v-else>
            <tr
              v-for="(row, idx) in paginatedRows" :key="rowKey ? row[rowKey] : idx"
              class="border-b border-surface-800/50 last:border-0 hover:bg-surface-800/50 transition-colors duration-150 cursor-pointer"
              @click="$emit('row-click', row)"
            >
              <td v-for="col in columns" :key="col.key" :class="col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : ''" class="px-4 py-3 text-surface-300">
                <slot :name="`cell-${col.key}`" :value="row[col.key]" :row="row">
                  {{ col.render ? col.render(row[col.key], row) : row[col.key] ?? '&#8212;' }}
                </slot>
              </td>
            </tr>
            <tr v-if="paginatedRows.length === 0">
              <td :colspan="columns.length" class="px-4 py-8 text-center text-surface-500">{{ emptyMessage }}</td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
    <div v-if="totalPages > 1" class="flex items-center justify-between px-4 py-3 border-t border-surface-800" role="navigation" aria-label="Table pagination">
      <p class="text-xs text-surface-500">Showing {{ (currentPage - 1) * pageSize + 1 }}&#8211;{{ Math.min(currentPage * pageSize, filteredRows.length) }} of {{ filteredRows.length }}</p>
      <div class="flex gap-1">
        <button :disabled="currentPage === 1" class="px-2 py-1 text-xs rounded bg-surface-800 text-surface-400 hover:text-surface-200 disabled:opacity-30 focus-ring" @click="currentPage--">Prev</button>
        <button v-for="p in visiblePages" :key="p" :class="p === currentPage ? 'bg-violet-600 text-white' : 'bg-surface-800 text-surface-400 hover:text-surface-200'" class="px-2.5 py-1 text-xs rounded focus-ring" @click="currentPage = p">{{ p }}</button>
        <button :disabled="currentPage === totalPages" class="px-2 py-1 text-xs rounded bg-surface-800 text-surface-400 hover:text-surface-200 disabled:opacity-30 focus-ring" @click="currentPage++">Next</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { Column } from '~/types'

const props = withDefaults(defineProps<{
  columns: Column[]
  rows: any[]
  rowKey?: string
  title?: string
  searchable?: boolean
  searchPlaceholder?: string
  searchKeys?: string[]
  pageSize?: number
  loading?: boolean
  emptyMessage?: string
  virtualScroll?: boolean
  rowHeight?: number
  virtualContainerHeight?: number
}>(), {
  pageSize: 20,
  searchPlaceholder: 'Search...',
  emptyMessage: 'No data available',
  searchKeys: () => [],
  virtualScroll: true,
  rowHeight: 44,
  virtualContainerHeight: 600,
})

defineEmits<{ 'row-click': [row: any] }>()

const searchInput = ref('')
const search = ref('')
const sortKey = ref('')
const sortOrder = ref<'asc' | 'desc'>('desc')
const currentPage = ref(1)

// Virtual scroll state
const scrollContainerRef = ref<HTMLElement>()
const scrollTop = ref(0)
const bufferRows = 10

// Whether virtual scrolling is active (enabled + enough rows)
const useVirtual = computed(() => props.virtualScroll && paginatedRows.value.length > 100)

const virtualStart = computed(() => {
  if (!useVirtual.value) return 0
  const firstVisible = Math.floor(scrollTop.value / props.rowHeight)
  return Math.max(0, firstVisible - bufferRows)
})

const virtualEnd = computed(() => {
  if (!useVirtual.value) return paginatedRows.value.length
  const visibleCount = Math.ceil(props.virtualContainerHeight / props.rowHeight)
  const firstVisible = Math.floor(scrollTop.value / props.rowHeight)
  return Math.min(paginatedRows.value.length, firstVisible + visibleCount + bufferRows)
})

const virtualRows = computed(() => {
  if (!useVirtual.value) return paginatedRows.value
  return paginatedRows.value.slice(virtualStart.value, virtualEnd.value)
})

function onScroll() {
  if (scrollContainerRef.value) {
    scrollTop.value = scrollContainerRef.value.scrollTop
  }
}

// Reset scroll position on page change
watch(currentPage, () => {
  scrollTop.value = 0
  if (scrollContainerRef.value) {
    scrollContainerRef.value.scrollTop = 0
  }
})

// 300ms debounce on search
let debounceTimer: ReturnType<typeof setTimeout>
watch(searchInput, (val) => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    search.value = val
    currentPage.value = 1
  }, 300)
})

function toggleSort(key: string) {
  if (sortKey.value === key) { sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc' }
  else { sortKey.value = key; sortOrder.value = 'desc' }
  currentPage.value = 1
}

const filteredRows = computed(() => {
  let rows = [...props.rows]
  if (search.value && props.searchKeys.length) {
    const q = search.value.toLowerCase()
    rows = rows.filter(row => props.searchKeys.some(key => String(row[key] ?? '').toLowerCase().includes(q)))
  }
  if (sortKey.value) {
    rows.sort((a, b) => {
      const cmp = String(a[sortKey.value] ?? '').localeCompare(String(b[sortKey.value] ?? ''), undefined, { numeric: true })
      return sortOrder.value === 'asc' ? cmp : -cmp
    })
  }
  return rows
})

const totalPages = computed(() => Math.ceil(filteredRows.value.length / props.pageSize) || 1)
const paginatedRows = computed(() => { const s = (currentPage.value - 1) * props.pageSize; return filteredRows.value.slice(s, s + props.pageSize) })
const visiblePages = computed(() => { const pages: number[] = []; const s = Math.max(1, currentPage.value - 2); const e = Math.min(totalPages.value, s + 4); for (let i = s; i <= e; i++) pages.push(i); return pages })
</script>
