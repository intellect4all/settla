import { defineStore } from 'pinia'
import type { Transfer, TransferEvent } from '~/types'

export const useTransferStore = defineStore('transfers', {
  state: () => ({
    transfers: [] as Transfer[],
    currentTransfer: null as Transfer | null,
    currentEvents: [] as TransferEvent[],
    nextPageToken: '' as string,
    totalCount: 0,
    loading: false,
    detailLoading: false,
    error: null as string | null,
  }),

  actions: {
    async fetchTransfers(pageToken?: string) {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.listTransfers({
          page_size: 50,
          page_token: pageToken,
        })
        this.transfers = result.transfers
        this.nextPageToken = result.next_page_token ?? ''
        this.totalCount = result.total_count
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load transfers'
      } finally {
        this.loading = false
      }
    },

    async fetchTransfer(id: string) {
      this.detailLoading = true
      this.error = null
      try {
        const api = usePortalApi()
        this.currentTransfer = await api.getTransfer(id)
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load transfer'
      } finally {
        this.detailLoading = false
      }
    },

    async fetchTransferEvents(id: string) {
      try {
        const api = usePortalApi()
        const result = await api.getTransferEvents(id)
        this.currentEvents = result.events ?? []
      } catch {
        this.currentEvents = []
      }
    },
  },
})
