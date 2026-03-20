<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between animate-fade-in">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Data Export</h1>
        <p class="text-sm text-surface-500 mt-1">Export your analytics data</p>
      </div>
    </div>

    <!-- Export Form -->
    <div class="card p-6 animate-fade-in">
      <h2 class="text-sm font-medium text-surface-300 mb-4">Create Export</h2>
      <form class="flex flex-wrap gap-4 items-end" @submit.prevent="handleExport">
        <div>
          <label class="block text-xs text-surface-500 mb-1">Export Type</label>
          <select v-model="exportType" class="input text-sm">
            <option value="transfers">Transfers</option>
            <option value="fees">Fees</option>
            <option value="providers">Providers</option>
            <option value="deposits">Deposits</option>
          </select>
        </div>
        <div>
          <label class="block text-xs text-surface-500 mb-1">Period</label>
          <select v-model="period" class="input text-sm">
            <option value="24h">Last 24 hours</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
          </select>
        </div>
        <div>
          <label class="block text-xs text-surface-500 mb-1">Format</label>
          <select v-model="format" class="input text-sm">
            <option value="csv">CSV</option>
          </select>
        </div>
        <AppButton type="submit" :loading="exporting" icon="download">
          Export
        </AppButton>
      </form>
    </div>

    <!-- Export Jobs Table -->
    <div class="card overflow-hidden animate-fade-in">
      <div class="px-4 py-3 border-b border-surface-800">
        <h2 class="text-sm font-semibold text-surface-200">Recent Exports</h2>
      </div>
      <div v-if="store.exportJobs.length === 0" class="p-8 text-center text-surface-500">
        No exports yet. Create one above.
      </div>
      <table v-else class="w-full text-sm">
        <thead>
          <tr class="border-b border-surface-800">
            <th class="text-left px-4 py-3 text-xs font-medium text-surface-500 uppercase">Type</th>
            <th class="text-left px-4 py-3 text-xs font-medium text-surface-500 uppercase">Status</th>
            <th class="text-right px-4 py-3 text-xs font-medium text-surface-500 uppercase">Rows</th>
            <th class="text-left px-4 py-3 text-xs font-medium text-surface-500 uppercase">Created</th>
            <th class="text-left px-4 py-3 text-xs font-medium text-surface-500 uppercase">Action</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="job in store.exportJobs" :key="job.id" class="border-b border-surface-800/50 last:border-0 hover:bg-surface-800/50 transition-colors">
            <td class="px-4 py-3 font-mono text-xs text-surface-300">{{ job.export_type }}</td>
            <td class="px-4 py-3">
              <span
                class="px-2 py-0.5 rounded-full text-xs font-medium"
                :class="statusClass(job.status)"
              >
                {{ job.status }}
              </span>
            </td>
            <td class="px-4 py-3 text-right font-mono text-surface-300">{{ job.row_count }}</td>
            <td class="px-4 py-3 text-surface-400">{{ formatDate(job.created_at) }}</td>
            <td class="px-4 py-3">
              <a
                v-if="job.status === 'completed' && job.download_url"
                :href="job.download_url"
                class="text-violet-400 hover:text-violet-300 text-xs"
              >
                Download
              </a>
              <button
                v-else-if="job.status === 'pending' || job.status === 'processing'"
                class="text-surface-500 hover:text-surface-300 text-xs"
                @click="pollJob(job.id)"
              >
                Refresh
              </button>
              <span v-else-if="job.status === 'failed'" class="text-red-400 text-xs" :title="job.error_message">
                Failed
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useAnalyticsStore } from '~/stores/analytics'

const store = useAnalyticsStore()

const exportType = ref('transfers')
const period = ref('7d')
const format = ref('csv')
const exporting = ref(false)

async function handleExport() {
  exporting.value = true
  try {
    const job = await store.triggerExport(exportType.value, format.value)
    if (job.status === 'pending') {
      setTimeout(() => pollJob(job.id), 3000)
    }
  } finally {
    exporting.value = false
  }
}

async function pollJob(jobId: string) {
  const job = await store.pollExportJob(jobId)
  if (job.status === 'pending' || job.status === 'processing') {
    setTimeout(() => pollJob(jobId), 3000)
  }
}

function statusClass(status: string) {
  switch (status) {
    case 'completed': return 'bg-emerald-500/10 text-emerald-400'
    case 'processing': return 'bg-amber-500/10 text-amber-400'
    case 'pending': return 'bg-surface-700/50 text-surface-300'
    case 'failed': return 'bg-red-500/10 text-red-400'
    default: return 'bg-surface-700/50 text-surface-300'
  }
}

function formatDate(dateStr?: string) {
  if (!dateStr) return '-'
  return new Date(dateStr).toLocaleString()
}
</script>
