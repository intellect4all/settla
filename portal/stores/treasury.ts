import { defineStore } from 'pinia'
import type { Position, PositionTransaction, PositionEvent } from '~/types'

export const useTreasuryStore = defineStore('treasury', {
  state: () => ({
    positions: [] as Position[],
    transactions: [] as PositionTransaction[],
    transactionsTotal: 0,
    loading: false,
    transactionsLoading: false,
    error: null as string | null,
  }),

  getters: {
    alertPositions: (state) =>
      state.positions.filter(p => {
        const available = parseFloat(p.available || '0')
        const minBalance = parseFloat(p.min_balance || '0')
        return minBalance > 0 && available < minBalance
      }),
  },

  actions: {
    async fetchPositions() {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.getPositions()
        this.positions = result.positions
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load positions'
      } finally {
        this.loading = false
      }
    },

    async requestTopUp(currency: string, location: string, amount: string, method: string) {
      const api = usePortalApi()
      const result = await api.requestTopUp({ currency, location, amount, method })
      // Refresh positions after top-up
      await this.fetchPositions()
      await this.fetchTransactions()
      return result.transaction
    },

    async requestWithdrawal(currency: string, location: string, amount: string, method: string, destination: string) {
      const api = usePortalApi()
      const result = await api.requestWithdrawal({ currency, location, amount, method, destination })
      // Refresh positions after withdrawal
      await this.fetchPositions()
      await this.fetchTransactions()
      return result.transaction
    },

    async fetchTransactions(limit = 20, offset = 0) {
      this.transactionsLoading = true
      try {
        const api = usePortalApi()
        const result = await api.listPositionTransactions({ limit, offset })
        this.transactions = result.transactions || []
        this.transactionsTotal = result.totalCount || 0
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load transactions'
      } finally {
        this.transactionsLoading = false
      }
    },

    async fetchPositionEvents(currency: string, location: string, params?: { from?: string; to?: string; limit?: number; offset?: number }) {
      const api = usePortalApi()
      return api.getPositionEventHistory(currency, location, params)
    },
  },
})
