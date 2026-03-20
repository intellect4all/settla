import { defineStore } from 'pinia'
import type { PaymentLink, PaymentLinkListResponse } from '~/types'

export const usePaymentLinksStore = defineStore('paymentLinks', {
  state: () => ({
    links: [] as PaymentLink[],
    total: 0,
    loading: false,
    error: null as string | null,
    currentPage: 0,
    pageSize: 20,
  }),

  actions: {
    async fetchLinks(offset = 0) {
      this.loading = true
      this.error = null
      try {
        const { usePortalApi } = await import('~/composables/usePortalApi')
        const api = usePortalApi()
        const result = await api.listPaymentLinks({ limit: this.pageSize, offset })
        this.links = result.links
        this.total = result.total
        this.currentPage = offset
      } catch (e: any) {
        this.error = e?.data?.message ?? e?.message ?? 'Failed to load payment links'
      } finally {
        this.loading = false
      }
    },

    async createLink(data: {
      description?: string
      redirect_url?: string
      use_limit?: number
      expires_at_unix?: number
      amount: string
      currency: string
      chain: string
      token: string
      settlement_pref?: string
      ttl_seconds?: number
    }) {
      const { usePortalApi } = await import('~/composables/usePortalApi')
      const api = usePortalApi()
      const result = await api.createPaymentLink(data)
      // Refresh list after creating
      await this.fetchLinks(this.currentPage)
      return result
    },

    async disableLink(linkId: string) {
      const { usePortalApi } = await import('~/composables/usePortalApi')
      const api = usePortalApi()
      await api.disablePaymentLink(linkId)
      // Refresh list after disabling
      await this.fetchLinks(this.currentPage)
    },
  },
})
