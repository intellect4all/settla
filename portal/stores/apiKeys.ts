import { defineStore } from 'pinia'
import type { APIKeyInfo } from '~/types'

export const useApiKeysStore = defineStore('apiKeys', {
  state: () => ({
    keys: [] as APIKeyInfo[],
    loading: false,
    error: null as string | null,
    lastCreatedRawKey: null as string | null,
  }),

  getters: {
    liveKeys: (state) => state.keys.filter(k => k.environment === 'LIVE' && k.is_active),
    testKeys: (state) => state.keys.filter(k => k.environment === 'TEST' && k.is_active),
    revokedKeys: (state) => state.keys.filter(k => !k.is_active),
  },

  actions: {
    async fetchKeys() {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.listApiKeys()
        this.keys = result.keys
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load API keys'
      } finally {
        this.loading = false
      }
    },

    async createKey(environment: string, name?: string) {
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.createApiKey(environment, name)
        this.lastCreatedRawKey = result.raw_key
        this.keys.unshift(result.key)
        return result
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to create API key'
        throw e
      }
    },

    async revokeKey(keyId: string) {
      this.error = null
      try {
        const api = usePortalApi()
        await api.revokeApiKey(keyId)
        const key = this.keys.find(k => k.id === keyId)
        if (key) key.is_active = false
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to revoke API key'
        throw e
      }
    },

    async rotateKey(keyId: string, name?: string) {
      this.error = null
      try {
        const api = usePortalApi()
        const result = await api.rotateApiKey(keyId, name)
        this.lastCreatedRawKey = result.raw_key
        // Mark old key inactive, add new key
        const oldKey = this.keys.find(k => k.id === keyId)
        if (oldKey) oldKey.is_active = false
        this.keys.unshift(result.key)
        return result
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to rotate API key'
        throw e
      }
    },

    clearRawKey() {
      this.lastCreatedRawKey = null
    },
  },
})
