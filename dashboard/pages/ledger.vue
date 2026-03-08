<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Ledger</h1>
        <p class="text-sm text-surface-500 mt-0.5">Account hierarchy & journal entries</p>
      </div>
    </div>

    <!-- Journal Entry Search -->
    <div class="card p-4 mb-6">
      <div class="flex gap-3">
        <input
          v-model="searchRef"
          type="text"
          placeholder="Search journal entries by reference..."
          class="input text-sm flex-1"
          @keydown.enter="searchEntries"
        />
        <button class="btn-primary text-sm" @click="searchEntries">Search</button>
      </div>

      <!-- Search Results -->
      <div v-if="searchResults.length > 0" class="mt-4 space-y-3">
        <div v-for="entry in searchResults" :key="entry.id" class="bg-surface-800 rounded-lg p-4">
          <div class="flex items-center justify-between mb-2">
            <div>
              <span class="text-sm font-medium text-surface-200">{{ entry.description || entry.reference }}</span>
              <span class="text-xs text-surface-500 ml-2 font-mono">{{ entry.reference }}</span>
            </div>
            <span class="text-xs text-surface-500">{{ formatDate(entry.created_at) }}</span>
          </div>
          <table class="w-full text-xs">
            <thead>
              <tr class="text-surface-500 border-b border-surface-700">
                <th class="text-left py-1.5 font-medium">Account</th>
                <th class="text-right py-1.5 font-medium">Debit</th>
                <th class="text-right py-1.5 font-medium">Credit</th>
                <th class="text-left py-1.5 font-medium pl-3">Currency</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="line in entry.entries" :key="line.account_code + line.type" class="border-b border-surface-800/50">
                <td class="py-1.5 font-mono text-surface-300">{{ line.account_code }}</td>
                <td class="py-1.5 text-right font-mono">
                  <span v-if="line.type === 'DEBIT'" class="text-amber-400">{{ formatAmount(line.amount) }}</span>
                </td>
                <td class="py-1.5 text-right font-mono">
                  <span v-if="line.type === 'CREDIT'" class="text-emerald-400">{{ formatAmount(line.amount) }}</span>
                </td>
                <td class="py-1.5 pl-3 text-surface-500">{{ line.currency }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <p v-if="searchedOnce && searchResults.length === 0" class="mt-3 text-sm text-surface-500">
        No entries found for "{{ lastSearchRef }}"
      </p>
    </div>

    <!-- Account Tree -->
    <div class="card p-4">
      <h3 class="text-sm font-semibold text-surface-200 mb-4">Account Hierarchy</h3>
      <LoadingSpinner v-if="loading && !accounts.length" />
      <div v-else-if="accounts.length" class="space-y-0.5">
        <LedgerTreeNode
          v-for="account in accounts"
          :key="account.code"
          :account="account"
          :depth="0"
          :selected-code="selectedCode"
          @select="onSelectAccount"
        />
      </div>
      <EmptyState v-else title="No accounts" description="Ledger accounts will appear here once created." />
    </div>

    <!-- Selected Account Entries -->
    <div v-if="selectedCode && selectedEntries.length > 0" class="card p-4 mt-4">
      <h3 class="text-sm font-semibold text-surface-200 mb-3">
        Recent entries for <span class="font-mono text-violet-400">{{ selectedCode }}</span>
      </h3>
      <div class="space-y-2">
        <div v-for="entry in selectedEntries" :key="entry.id" class="bg-surface-800 rounded-lg p-3">
          <div class="flex justify-between mb-1">
            <span class="text-xs text-surface-300">{{ entry.description }}</span>
            <span class="text-xs text-surface-500">{{ formatDate(entry.created_at) }}</span>
          </div>
          <div class="flex gap-4">
            <div v-for="line in entry.entries" :key="line.account_code + line.type" class="text-xs">
              <span class="font-mono text-surface-400">{{ line.account_code }}</span>
              <span :class="line.type === 'DEBIT' ? 'text-amber-400' : 'text-emerald-400'" class="ml-1 font-mono">
                {{ line.type === 'DEBIT' ? 'DR' : 'CR' }} {{ formatAmount(line.amount) }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { LedgerAccount, JournalEntry } from '~/types'

const api = useApi()
const { error: showError } = useToast()
const { format } = useMoney()

const accounts = ref<LedgerAccount[]>([])
const loading = ref(true)
const searchRef = ref('')
const lastSearchRef = ref('')
const searchResults = ref<JournalEntry[]>([])
const searchedOnce = ref(false)
const selectedCode = ref('')
const selectedEntries = ref<JournalEntry[]>([])

async function fetchAccounts() {
  loading.value = true
  try {
    const result = await api.getLedgerAccounts()
    accounts.value = result.accounts
  } catch {
    // Generate sample tree structure for demo
    accounts.value = generateSampleAccounts()
  } finally {
    loading.value = false
  }
}

function generateSampleAccounts(): LedgerAccount[] {
  return [
    {
      code: 'assets',
      name: 'Assets',
      balance: '0',
      currency: 'USD',
      children: [
        {
          code: 'assets:bank',
          name: 'Bank Accounts',
          balance: '0',
          currency: 'USD',
          children: [
            { code: 'assets:bank:gbp:clearing', name: 'GBP Clearing', balance: '1250000.00', currency: 'GBP' },
            { code: 'assets:bank:ngn:clearing', name: 'NGN Clearing', balance: '450000000.00', currency: 'NGN' },
            { code: 'assets:bank:eur:clearing', name: 'EUR Clearing', balance: '890000.00', currency: 'EUR' },
          ],
        },
        {
          code: 'assets:crypto',
          name: 'Crypto',
          balance: '0',
          currency: 'USD',
          children: [
            { code: 'assets:crypto:usdt:tron', name: 'USDT on Tron', balance: '2500000.00', currency: 'USDT' },
            { code: 'assets:crypto:usdc:ethereum', name: 'USDC on Ethereum', balance: '1800000.00', currency: 'USDC' },
          ],
        },
      ],
    },
    {
      code: 'liabilities',
      name: 'Liabilities',
      balance: '0',
      currency: 'USD',
      children: [
        {
          code: 'liabilities:tenant',
          name: 'Tenant Balances',
          balance: '0',
          currency: 'USD',
          children: [
            { code: 'liabilities:tenant:lemfi', name: 'Lemfi', balance: '850000.00', currency: 'USD' },
            { code: 'liabilities:tenant:fincra', name: 'Fincra', balance: '620000.00', currency: 'USD' },
          ],
        },
      ],
    },
    {
      code: 'revenue',
      name: 'Revenue',
      balance: '0',
      currency: 'USD',
      children: [
        { code: 'revenue:fees:onramp', name: 'On-ramp Fees', balance: '45230.00', currency: 'USD' },
        { code: 'revenue:fees:offramp', name: 'Off-ramp Fees', balance: '38100.00', currency: 'USD' },
        { code: 'revenue:fees:network', name: 'Network Fees', balance: '12450.00', currency: 'USD' },
      ],
    },
  ]
}

async function searchEntries() {
  if (!searchRef.value.trim()) return
  lastSearchRef.value = searchRef.value
  searchedOnce.value = true
  try {
    const result = await api.searchJournalEntries(searchRef.value)
    searchResults.value = result.entries
  } catch {
    searchResults.value = []
  }
}

async function onSelectAccount(code: string) {
  if (selectedCode.value === code) {
    selectedCode.value = ''
    selectedEntries.value = []
    return
  }
  selectedCode.value = code
  try {
    const result = await api.getAccountEntries(code)
    selectedEntries.value = result.entries
  } catch {
    selectedEntries.value = []
  }
}

function formatAmount(amount: string) {
  return format(amount)
}

function formatDate(ts: string) {
  if (!ts) return '\u2014'
  const d = new Date(ts)
  return d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) + ' ' +
    d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
}

onMounted(fetchAccounts)
</script>
