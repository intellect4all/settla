import { defineStore } from 'pinia'
import type { DashboardMetrics, TransferStatsBucket } from '~/types'

export const useDashboardStore = defineStore('dashboard', {
  state: () => ({
    metrics: null as DashboardMetrics | null,
    chartBuckets: [] as TransferStatsBucket[],
    chartPeriod: '24h' as string,
    chartGranularity: 'hour' as string,
    loading: false,
    chartLoading: false,
    error: null as string | null,
  }),

  getters: {
    successRate: (state) => state.metrics?.success_rate_30d ?? '0',
    dailyUsagePercent: (state) => {
      if (!state.metrics) return 0
      const usage = parseFloat(state.metrics.daily_usage_usd || '0')
      const limit = parseFloat(state.metrics.daily_limit_usd || '1')
      if (limit === 0) return 0
      return Math.min(100, (usage / limit) * 100)
    },
  },

  actions: {
    async fetchMetrics() {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        this.metrics = await api.getDashboard()
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load dashboard'
      } finally {
        this.loading = false
      }
    },

    async fetchChart(period?: string, granularity?: string) {
      if (period) this.chartPeriod = period
      if (granularity) this.chartGranularity = granularity

      this.chartLoading = true
      try {
        const api = usePortalApi()
        const result = await api.getTransferStats(this.chartPeriod, this.chartGranularity)
        this.chartBuckets = result.buckets
      } catch {
        // Chart failure is non-critical
      } finally {
        this.chartLoading = false
      }
    },
  },
})
