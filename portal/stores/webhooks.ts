import { defineStore } from 'pinia'
import type {
  WebhookDeliveryInfo, WebhookDeliveryStats,
  WebhookEventSubscription, TestWebhookResult,
} from '~/types'

export const useWebhooksStore = defineStore('webhooks', {
  state: () => ({
    webhookUrl: '' as string,
    webhookSecret: null as string | null,
    loading: false,
    error: null as string | null,

    // Delivery logs
    deliveries: [] as WebhookDeliveryInfo[],
    deliveryTotalCount: 0,
    deliveriesLoading: false,

    // Delivery stats
    stats: null as WebhookDeliveryStats | null,
    statsPeriod: '24h' as string,
    statsLoading: false,

    // Event subscriptions
    subscriptions: [] as WebhookEventSubscription[],
    availableEventTypes: [] as string[],
    subscriptionsLoading: false,

    // Test webhook
    testResult: null as TestWebhookResult | null,
    testing: false,
  }),

  getters: {
    successRate: (state) => {
      if (!state.stats || state.stats.total_deliveries === 0) return 0
      return Math.round((state.stats.successful / state.stats.total_deliveries) * 100)
    },
    subscribedEventTypes: (state) => state.subscriptions.map(s => s.event_type),
  },

  actions: {
    setFromProfile(url: string | undefined) {
      this.webhookUrl = url ?? ''
    },

    async updateWebhook(url: string) {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.updateWebhook(url)
        this.webhookUrl = result.webhook_url
        this.webhookSecret = result.webhook_secret
        return result
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to update webhook'
        throw e
      } finally {
        this.loading = false
      }
    },

    clearSecret() {
      this.webhookSecret = null
    },

    // Delivery logs
    async fetchDeliveries(params?: {
      event_type?: string; status?: string; page_size?: number; page_offset?: number
    }) {
      this.deliveriesLoading = true
      try {
        const api = usePortalApi()
        const result = await api.listWebhookDeliveries(params)
        this.deliveries = result.deliveries
        this.deliveryTotalCount = result.total_count
      } catch {
        // Non-critical
      } finally {
        this.deliveriesLoading = false
      }
    },

    // Delivery stats
    async fetchStats(period?: string) {
      if (period) this.statsPeriod = period
      this.statsLoading = true
      try {
        const api = usePortalApi()
        this.stats = await api.getWebhookDeliveryStats(this.statsPeriod)
      } catch {
        // Non-critical
      } finally {
        this.statsLoading = false
      }
    },

    // Event subscriptions
    async fetchSubscriptions() {
      this.subscriptionsLoading = true
      try {
        const api = usePortalApi()
        const result = await api.listWebhookSubscriptions()
        this.subscriptions = result.subscriptions
        this.availableEventTypes = result.available_event_types
      } catch {
        // Non-critical
      } finally {
        this.subscriptionsLoading = false
      }
    },

    async updateSubscriptions(eventTypes: string[]) {
      this.subscriptionsLoading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.updateWebhookSubscriptions(eventTypes)
        this.subscriptions = result.subscriptions ?? []
        return result
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to update subscriptions'
        throw e
      } finally {
        this.subscriptionsLoading = false
      }
    },

    // Test webhook
    async sendTestWebhook() {
      this.testing = true
      this.testResult = null
      try {
        const api = usePortalApi()
        this.testResult = await api.testWebhook()
        return this.testResult
      } catch (e) {
        this.testResult = {
          success: false,
          error: e instanceof Error ? e.message : 'Test failed',
        }
        return this.testResult
      } finally {
        this.testing = false
      }
    },

    clearTestResult() {
      this.testResult = null
    },
  },
})
