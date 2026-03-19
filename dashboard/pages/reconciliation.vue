<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between animate-fade-in">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Reconciliation</h1>
        <p class="text-sm text-surface-400 mt-0.5">Automated consistency checks across treasury, ledger, and transfers</p>
      </div>
      <AppButton
        variant="primary"
        size="sm"
        :loading="running"
        :disabled="running"
        @click="runNow"
      >
        {{ running ? 'Running...' : 'Run now' }}
      </AppButton>
    </div>

    <!-- Auth / fetch error -->
    <AlertBanner
      v-if="fetchError"
      type="error"
      :title="fetchError"
      description="Check your API key and gateway connectivity."
    />

    <!-- Sample data notice -->
    <AlertBanner
      v-else-if="usingSampleData"
      type="info"
      title="Showing sample data"
      description="The /v1/ops/reconciliation endpoint is not yet available on the gateway. Live data will appear automatically once the endpoint is deployed."
    />

    <!-- Last run summary -->
    <div v-if="report" class="card p-5 animate-fade-in">
      <div class="flex items-center justify-between flex-wrap gap-4">
        <div class="flex items-center gap-4">
          <span
            class="text-sm font-semibold px-3 py-1 rounded-full"
            :class="overallClass(report.status)"
          >{{ report.status }}</span>
          <div>
            <p class="text-sm text-surface-300">Last run: <span class="text-surface-100">{{ formatDate(report.ran_at) }}</span></p>
            <p class="text-xs text-surface-500">Completed in {{ report.duration_ms }}ms</p>
          </div>
        </div>
        <div class="flex gap-6 text-center">
          <div>
            <p class="text-xl font-semibold text-emerald-400">{{ okCount }}</p>
            <p class="text-xs text-surface-500">OK</p>
          </div>
          <div>
            <p class="text-xl font-semibold text-amber-400">{{ warnCount }}</p>
            <p class="text-xs text-surface-500">Warnings</p>
          </div>
          <div>
            <p class="text-xl font-semibold text-red-400">{{ critCount }}</p>
            <p class="text-xs text-surface-500">Critical</p>
          </div>
        </div>
      </div>
    </div>

    <!-- Loading skeleton -->
    <template v-if="loading">
      <SkeletonLoader variant="card" height="80px" />
      <div class="space-y-4">
        <SkeletonLoader v-for="i in 5" :key="i" variant="card" height="72px" />
      </div>
    </template>

    <!-- Checks -->
    <div v-else-if="report" class="space-y-4 animate-fade-in">
      <div
        v-for="check in report.checks"
        :key="check.name"
        class="card overflow-hidden"
      >
        <!-- Check header -->
        <button
          class="w-full flex items-center justify-between p-5 text-left hover:bg-surface-800/50 transition-colors focus-ring"
          @click="toggleCheck(check.name)"
        >
          <div class="flex items-center gap-4">
            <span
              class="w-2.5 h-2.5 rounded-full shrink-0"
              :class="dotClass(check.status)"
            />
            <div>
              <p class="text-sm font-medium text-surface-100">{{ check.name }}</p>
              <p class="text-xs text-surface-500 mt-0.5">{{ check.description }}</p>
            </div>
          </div>
          <div class="flex items-center gap-6 shrink-0">
            <div class="text-right">
              <p
                class="text-sm font-semibold"
                :class="check.discrepancy_count > 0 ? 'text-amber-400' : 'text-emerald-400'"
              >
                {{ check.discrepancy_count }} discrepanc{{ check.discrepancy_count === 1 ? 'y' : 'ies' }}
              </p>
              <p class="text-xs text-surface-500">{{ check.duration_ms }}ms</p>
            </div>
            <span
              class="text-xs font-semibold px-2.5 py-1 rounded-full"
              :class="checkStatusClass(check.status)"
            >{{ check.status }}</span>
            <Icon :name="expanded.includes(check.name) ? 'chevron-up' : 'chevron-down'" :size="12" class="text-surface-500" />
          </div>
        </button>

        <!-- Discrepancy table -->
        <div v-if="expanded.includes(check.name)" class="border-t border-surface-800">
          <div v-if="check.details.length === 0" class="px-5 py-4 text-sm text-surface-500">
            No discrepancies found.
          </div>
          <div v-else class="overflow-x-auto">
            <table class="w-full text-xs">
              <thead>
                <tr class="border-b border-surface-800 text-surface-500">
                  <th class="px-5 py-2.5 text-left font-medium">Entity</th>
                  <th class="px-5 py-2.5 text-left font-medium">ID</th>
                  <th class="px-5 py-2.5 text-right font-medium">Expected</th>
                  <th class="px-5 py-2.5 text-right font-medium">Actual</th>
                  <th class="px-5 py-2.5 text-right font-medium">Delta</th>
                  <th class="px-5 py-2.5 text-center font-medium">Severity</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="d in check.details"
                  :key="d.id"
                  class="border-b border-surface-800/50 hover:bg-surface-800/40"
                >
                  <td class="px-5 py-2.5 text-surface-400">{{ d.entity_type }}</td>
                  <td class="px-5 py-2.5 font-mono text-surface-300">{{ truncate(d.entity_id) }}</td>
                  <td class="px-5 py-2.5 text-right font-mono text-surface-300">{{ d.expected }}</td>
                  <td class="px-5 py-2.5 text-right font-mono text-surface-300">{{ d.actual }}</td>
                  <td class="px-5 py-2.5 text-right font-mono" :class="d.delta.startsWith('-') ? 'text-red-400' : 'text-amber-400'">
                    {{ d.delta }}
                  </td>
                  <td class="px-5 py-2.5 text-center">
                    <span
                      class="px-2 py-0.5 rounded-full font-semibold"
                      :class="severityClass(d.severity)"
                    >{{ d.severity }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>

    <!-- Empty -->
    <EmptyState
      v-else
      icon="shield"
      title="No reconciliation data"
      description="Click 'Run now' to execute all checks."
    />
  </div>
</template>

<script setup lang="ts">
import type { ReconciliationReport } from '~/types'

const api = useApi()
const toast = useToast()

const report = ref<ReconciliationReport | null>(null)
const loading = ref(true)
const running = ref(false)
const fetchError = ref<string | null>(null)
const usingSampleData = ref(false)
// Vue 3 reactivity does not track Set mutations; use a plain ref<string[]> instead.
const expanded = ref<string[]>([])

// ── Data ───────────────────────────────────────────────────────────────────

async function loadData() {
  loading.value = true
  fetchError.value = null
  usingSampleData.value = false
  try {
    report.value = await api.getReconciliationReport()
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 401 || status === 403) {
      fetchError.value = 'Authentication failed — check your API key.'
    } else {
      report.value = generateSampleReport()
      usingSampleData.value = true
    }
  } finally {
    loading.value = false
  }
}

async function runNow() {
  running.value = true
  try {
    report.value = await api.runReconciliation()
    toast.success('Reconciliation complete')
    usingSampleData.value = false
    fetchError.value = null
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 401 || status === 403) {
      toast.error('Authentication failed — check your API key.')
    } else {
      report.value = generateSampleReport()
      usingSampleData.value = true
      toast.info('Using sample data (backend unavailable)')
    }
  } finally {
    running.value = false
  }
}

// ── Computed ───────────────────────────────────────────────────────────────

const okCount = computed(() => report.value?.checks.filter(c => c.status === 'OK').length ?? 0)
const warnCount = computed(() => report.value?.checks.filter(c => c.status === 'WARNING').length ?? 0)
const critCount = computed(() => report.value?.checks.filter(c => c.status === 'CRITICAL').length ?? 0)

// ── UI helpers ─────────────────────────────────────────────────────────────

function toggleCheck(name: string) {
  const idx = expanded.value.indexOf(name)
  if (idx !== -1) {
    expanded.value.splice(idx, 1)
  } else {
    expanded.value.push(name)
  }
}

function overallClass(status: string) {
  switch (status) {
    case 'OK': return 'bg-emerald-500/10 text-emerald-400'
    case 'WARNING': return 'bg-amber-500/10 text-amber-400'
    case 'CRITICAL': return 'bg-red-500/10 text-red-400'
    default: return 'bg-surface-700 text-surface-400'
  }
}

function checkStatusClass(status: string) {
  switch (status) {
    case 'OK': return 'bg-emerald-500/10 text-emerald-400'
    case 'WARNING': return 'bg-amber-500/10 text-amber-400'
    case 'CRITICAL': return 'bg-red-500/10 text-red-400'
    case 'SKIPPED': return 'bg-surface-700 text-surface-500'
    default: return 'bg-surface-700 text-surface-400'
  }
}

function dotClass(status: string) {
  switch (status) {
    case 'OK': return 'bg-emerald-400'
    case 'WARNING': return 'bg-amber-400 animate-pulse'
    case 'CRITICAL': return 'bg-red-400 animate-pulse'
    default: return 'bg-surface-600'
  }
}

function severityClass(severity: string) {
  switch (severity) {
    case 'LOW': return 'bg-surface-700 text-surface-400'
    case 'MEDIUM': return 'bg-amber-500/10 text-amber-400'
    case 'HIGH': return 'bg-orange-500/10 text-orange-400'
    case 'CRITICAL': return 'bg-red-500/10 text-red-400'
    default: return 'bg-surface-700 text-surface-400'
  }
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleString()
}

function truncate(s: string, n = 20) {
  return s.length > n ? s.slice(0, n) + '...' : s
}

// ── Sample data ────────────────────────────────────────────────────────────

function generateSampleReport(): ReconciliationReport {
  return {
    id: 'recon_sample_001',
    ran_at: new Date().toISOString(),
    duration_ms: 842,
    status: 'WARNING',
    checks: [
      {
        name: 'Treasury Position Consistency',
        status: 'OK',
        description: 'Compares in-memory treasury positions against DB snapshots. Detects drift from flush failures.',
        discrepancy_count: 0,
        details: [],
        ran_at: new Date().toISOString(),
        duration_ms: 12,
      },
      {
        name: 'Ledger Balance Check',
        status: 'WARNING',
        description: 'Verifies every journal entry sums to zero (debits = credits). Flags unbalanced postings.',
        discrepancy_count: 1,
        details: [
          {
            id: 'disc_001',
            entity_type: 'JournalEntry',
            entity_id: 'je_7f3a12b9-0001-0000-0000-000000000001',
            expected: '0.00 GBP',
            actual: '0.01 GBP',
            delta: '+0.01 GBP',
            severity: 'MEDIUM',
          },
        ],
        ran_at: new Date().toISOString(),
        duration_ms: 238,
      },
      {
        name: 'Transfer State Consistency',
        status: 'OK',
        description: 'Checks that transfer state matches outbox entries. Detects transfers stuck with no pending intents.',
        discrepancy_count: 0,
        details: [],
        ran_at: new Date().toISOString(),
        duration_ms: 194,
      },
      {
        name: 'Outbox Backlog Check',
        status: 'OK',
        description: 'Counts unpublished outbox entries older than 5 minutes. Indicates relay lag or worker failure.',
        discrepancy_count: 0,
        details: [],
        ran_at: new Date().toISOString(),
        duration_ms: 8,
      },
      {
        name: 'Provider Sync Check',
        status: 'OK',
        description: 'Verifies transfers expecting provider webhooks have received them within SLA window.',
        discrepancy_count: 0,
        details: [],
        ran_at: new Date().toISOString(),
        duration_ms: 390,
      },
    ],
  }
}

// ── Init ───────────────────────────────────────────────────────────────────

onMounted(loadData)
</script>
