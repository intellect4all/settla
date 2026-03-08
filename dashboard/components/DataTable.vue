<template>
  <div class="card overflow-hidden">
    <!-- Header with search -->
    <div v-if="title || searchable" class="flex items-center justify-between px-4 py-3 border-b border-surface-800">
      <h3 v-if="title" class="text-sm font-semibold text-surface-200">{{ title }}</h3>
      <input
        v-if="searchable"
        v-model="search"
        type="text"
        :placeholder="searchPlaceholder"
        class="input text-sm w-64"
      />
    </div>

    <!-- Table -->
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-surface-800">
            <th
              v-for="col in columns"
              :key="col.key"
              :class="[
                col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : 'text-left',
                col.sortable ? 'cursor-pointer select-none hover:text-surface-200' : '',
              ]"
              :style="col.width ? { width: col.width } : {}"
              class="px-4 py-3 text-xs font-medium text-surface-500 uppercase tracking-wider"
              @click="col.sortable ? toggleSort(col.key) : null"
            >
              <span class="inline-flex items-center gap-1">
                {{ col.label }}
                <span v-if="col.sortable && sortKey === col.key" class="text-violet-400">
                  {{ sortOrder === 'asc' ? '&#9650;' : '&#9660;' }}
                </span>
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, idx) in paginatedRows"
            :key="rowKey ? row[rowKey] : idx"
            class="border-b border-surface-800/50 last:border-0 hover:bg-surface-800/30 transition-colors cursor-pointer"
            @click="$emit('row-click', row)"
          >
            <td
              v-for="col in columns"
              :key="col.key"
              :class="col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : ''"
              class="px-4 py-3 text-surface-300"
            >
              <slot :name="`cell-${col.key}`" :value="row[col.key]" :row="row">
                {{ col.render ? col.render(row[col.key], row) : row[col.key] ?? '&#8212;' }}
              </slot>
            </td>
          </tr>
          <tr v-if="paginatedRows.length === 0">
            <td :colspan="columns.length" class="px-4 py-8 text-center text-surface-500">
              {{ loading ? 'Loading...' : emptyMessage }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div v-if="totalPages > 1" class="flex items-center justify-between px-4 py-3 border-t border-surface-800">
      <p class="text-xs text-surface-500">
        Showing {{ (currentPage - 1) * pageSize + 1 }}&#8211;{{ Math.min(currentPage * pageSize, filteredRows.length) }}
        of {{ filteredRows.length }}
      </p>
      <div class="flex gap-1">
        <button
          :disabled="currentPage === 1"
          class="px-2 py-1 text-xs rounded bg-surface-800 text-surface-400 hover:text-surface-200 disabled:opacity-30"
          @click="currentPage--"
        >
          Prev
        </button>
        <button
          v-for="p in visiblePages"
          :key="p"
          :class="p === currentPage ? 'bg-violet-600 text-white' : 'bg-surface-800 text-surface-400 hover:text-surface-200'"
          class="px-2.5 py-1 text-xs rounded"
          @click="currentPage = p"
        >
          {{ p }}
        </button>
        <button
          :disabled="currentPage === totalPages"
          class="px-2 py-1 text-xs rounded bg-surface-800 text-surface-400 hover:text-surface-200 disabled:opacity-30"
          @click="currentPage++"
        >
          Next
        </button>
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
}>(), {
  pageSize: 20,
  searchPlaceholder: 'Search...',
  emptyMessage: 'No data available',
  searchKeys: () => [],
})

defineEmits<{
  'row-click': [row: any]
}>()

const search = ref('')
const sortKey = ref('')
const sortOrder = ref<'asc' | 'desc'>('desc')
const currentPage = ref(1)

function toggleSort(key: string) {
  if (sortKey.value === key) {
    sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortKey.value = key
    sortOrder.value = 'desc'
  }
  currentPage.value = 1
}

const filteredRows = computed(() => {
  let rows = [...props.rows]

  if (search.value && props.searchKeys.length) {
    const q = search.value.toLowerCase()
    rows = rows.filter(row =>
      props.searchKeys.some(key => String(row[key] ?? '').toLowerCase().includes(q))
    )
  }

  if (sortKey.value) {
    rows.sort((a, b) => {
      const av = a[sortKey.value] ?? ''
      const bv = b[sortKey.value] ?? ''
      const cmp = String(av).localeCompare(String(bv), undefined, { numeric: true })
      return sortOrder.value === 'asc' ? cmp : -cmp
    })
  }

  return rows
})

const totalPages = computed(() => Math.ceil(filteredRows.value.length / props.pageSize) || 1)

const paginatedRows = computed(() => {
  const start = (currentPage.value - 1) * props.pageSize
  return filteredRows.value.slice(start, start + props.pageSize)
})

const visiblePages = computed(() => {
  const pages: number[] = []
  const start = Math.max(1, currentPage.value - 2)
  const end = Math.min(totalPages.value, start + 4)
  for (let i = start; i <= end; i++) pages.push(i)
  return pages
})

watch(search, () => { currentPage.value = 1 })
</script>
