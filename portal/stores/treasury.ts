import { defineStore } from 'pinia'
import type { Position } from '~/types'

export const useTreasuryStore = defineStore('treasury', {
  state: () => ({
    positions: [] as Position[],
    loading: false,
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
  },
})
