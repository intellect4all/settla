import { defineStore } from 'pinia'
import type { DepositSession } from '~/types'

interface DepositState {
  sessions: DepositSession[]
  total: number
  loading: boolean
  error: string | null
  offset: number
  limit: number
}

export const useDepositsStore = defineStore('deposits', {
  state: (): DepositState => ({
    sessions: [],
    total: 0,
    loading: false,
    error: null,
    offset: 0,
    limit: 20,
  }),

  actions: {
    async fetchSessions(offset?: number) {
      this.loading = true
      this.error = null
      if (offset !== undefined) this.offset = offset
      try {
        const api = usePortalApi()
        const res = await api.listDeposits({ limit: this.limit, offset: this.offset })
        this.sessions = res.sessions ?? []
        this.total = res.total ?? 0
      } catch (e: any) {
        this.error = e.message ?? 'Failed to load deposits'
      } finally {
        this.loading = false
      }
    },

    nextPage() {
      if (this.offset + this.limit < this.total) {
        this.fetchSessions(this.offset + this.limit)
      }
    },

    prevPage() {
      if (this.offset > 0) {
        this.fetchSessions(Math.max(0, this.offset - this.limit))
      }
    },
  },

  getters: {
    hasNext: (state) => state.offset + state.limit < state.total,
    hasPrev: (state) => state.offset > 0,
    currentPage: (state) => Math.floor(state.offset / state.limit) + 1,
    totalPages: (state) => Math.ceil(state.total / state.limit) || 1,
  },
})
