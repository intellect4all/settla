import { defineStore } from 'pinia'
import type {
  StatusCount, CorridorMetric, LatencyPercentiles,
  VolumeComparison, ActivityItem, TransferStatsBucket,
  FeeBreakdownAnalytics, ProviderPerformance,
  DepositAnalytics, ReconciliationSummary, ExportJob,
} from '~/types'

export const useAnalyticsStore = defineStore('analytics', {
  state: () => ({
    period: '7d' as string,
    loading: false,

    // Status distribution
    statusDistribution: [] as StatusCount[],

    // Corridor metrics
    corridors: [] as CorridorMetric[],

    // Latency percentiles
    latency: null as LatencyPercentiles | null,

    // Volume comparison (WoW or MoM)
    comparison: null as VolumeComparison | null,

    // Transfer chart (reusing existing buckets)
    chartBuckets: [] as TransferStatsBucket[],
    chartLoading: false,

    // Activity feed
    activity: [] as ActivityItem[],
    activityLoading: false,

    // Extended analytics
    feeBreakdown: [] as FeeBreakdownAnalytics[],
    totalFeesUsd: '0',
    providerPerformance: [] as ProviderPerformance[],
    depositAnalytics: { crypto: {} as DepositAnalytics, bank: {} as DepositAnalytics },
    reconciliation: null as ReconciliationSummary | null,
    exportJobs: [] as ExportJob[],
  }),

  getters: {
    totalTransfers: (state) => state.statusDistribution.reduce((sum, s) => sum + s.count, 0),

    volumeChangePercent: (state) => {
      if (!state.comparison) return 0
      const curr = parseFloat(state.comparison.current_volume_usd || '0')
      const prev = parseFloat(state.comparison.previous_volume_usd || '1')
      if (prev === 0) return curr > 0 ? 100 : 0
      return Math.round(((curr - prev) / prev) * 100)
    },

    countChangePercent: (state) => {
      if (!state.comparison) return 0
      const curr = state.comparison.current_count
      const prev = state.comparison.previous_count
      if (prev === 0) return curr > 0 ? 100 : 0
      return Math.round(((curr - prev) / prev) * 100)
    },

    topCorridors: (state) => state.corridors.slice(0, 5),

    totalFees: (state) => state.totalFeesUsd,

    topProviders: (state) =>
      [...state.providerPerformance].sort((a, b) =>
        parseFloat(b.total_volume) - parseFloat(a.total_volume),
      ).slice(0, 5),

    depositConversionRate: (state) =>
      state.depositAnalytics?.crypto?.conversion_rate || '0',
  },

  actions: {
    async fetchAll(period?: string) {
      if (period) this.period = period
      this.loading = true
      try {
        await Promise.all([
          this.fetchStatusDistribution(),
          this.fetchCorridors(),
          this.fetchLatency(),
          this.fetchComparison(),
          this.fetchChart(),
          this.fetchActivity(),
        ])
      } finally {
        this.loading = false
      }
    },

    async fetchStatusDistribution() {
      try {
        const api = usePortalApi()
        const result = await api.getStatusDistribution(this.period)
        this.statusDistribution = result.statuses
      } catch {
        // Non-critical
      }
    },

    async fetchCorridors() {
      try {
        const api = usePortalApi()
        const result = await api.getCorridorMetrics(this.period)
        this.corridors = result.corridors
      } catch {
        // Non-critical
      }
    },

    async fetchLatency() {
      try {
        const api = usePortalApi()
        this.latency = await api.getLatencyPercentiles(this.period)
      } catch {
        // Non-critical
      }
    },

    async fetchComparison() {
      try {
        const api = usePortalApi()
        const compPeriod = this.period === '24h' ? '7d' : this.period
        this.comparison = await api.getVolumeComparison(compPeriod)
      } catch {
        // Non-critical
      }
    },

    async fetchChart() {
      this.chartLoading = true
      try {
        const api = usePortalApi()
        const granularity = this.period === '30d' ? 'day' : 'hour'
        const result = await api.getTransferStats(this.period, granularity)
        this.chartBuckets = result.buckets
      } catch {
        // Non-critical
      } finally {
        this.chartLoading = false
      }
    },

    async fetchActivity() {
      this.activityLoading = true
      try {
        const api = usePortalApi()
        const result = await api.getRecentActivity(20)
        this.activity = result.items
      } catch {
        // Non-critical
      } finally {
        this.activityLoading = false
      }
    },

    async fetchFeeBreakdown() {
      try {
        const api = usePortalApi()
        const result = await api.getFeeBreakdown(this.period)
        this.feeBreakdown = result.entries
        this.totalFeesUsd = result.total_fees_usd
      } catch {
        // Non-critical
      }
    },

    async fetchProviderPerformance() {
      try {
        const api = usePortalApi()
        const result = await api.getProviderPerformance(this.period)
        this.providerPerformance = result.providers
      } catch {
        // Non-critical
      }
    },

    async fetchDepositAnalytics() {
      try {
        const api = usePortalApi()
        this.depositAnalytics = await api.getDepositAnalytics(this.period)
      } catch {
        // Non-critical
      }
    },

    async fetchReconciliationSummary() {
      try {
        const api = usePortalApi()
        this.reconciliation = await api.getReconciliationSummary()
      } catch {
        // Non-critical
      }
    },

    async triggerExport(exportType: string, format = 'csv') {
      const api = usePortalApi()
      const result = await api.createExportJob({
        export_type: exportType,
        period: this.period,
        format,
      })
      this.exportJobs.unshift(result.job)
      return result.job
    },

    async pollExportJob(jobId: string) {
      const api = usePortalApi()
      const result = await api.getExportJob(jobId)
      const idx = this.exportJobs.findIndex(j => j.id === jobId)
      if (idx >= 0) {
        this.exportJobs[idx] = result.job
      }
      return result.job
    },
  },
})
