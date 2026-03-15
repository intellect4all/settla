import { defineStore } from 'pinia'
import type { FeeReportEntry } from '~/types'

export const useFeesStore = defineStore('fees', {
  state: () => ({
    entries: [] as FeeReportEntry[],
    totalFeesUsd: '0' as string,
    loading: false,
    error: null as string | null,
  }),

  actions: {
    async fetchReport(from?: string, to?: string) {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.getFeeReport(from, to)
        this.entries = result.entries
        this.totalFeesUsd = result.total_fees_usd
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load fee report'
      } finally {
        this.loading = false
      }
    },
  },
})
