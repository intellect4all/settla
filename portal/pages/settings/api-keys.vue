<template>
  <div class="space-y-6">
    <div class="flex items-start justify-between flex-wrap gap-4">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">API Keys</h1>
        <p class="text-sm text-surface-500 mt-1">Manage your live and test API keys</p>
      </div>
      <button class="btn-primary text-sm" @click="showCreateModal = true">Create Key</button>
    </div>

    <LoadingSpinner v-if="store.loading" size="lg" full-page />

    <template v-else>
      <!-- Raw key banner (shown once after create/rotate) -->
      <div v-if="store.lastCreatedRawKey" class="bg-emerald-900/20 border border-emerald-700 rounded-lg p-4">
        <p class="text-sm font-medium text-emerald-300 mb-2">New API key created</p>
        <p class="text-xs text-emerald-400 mb-2">Copy this key now. It will not be shown again.</p>
        <div class="flex items-center gap-2">
          <code class="flex-1 text-xs font-mono bg-surface-950 text-surface-200 px-3 py-2 rounded border border-surface-700 break-all">
            {{ store.lastCreatedRawKey }}
          </code>
          <button class="btn-primary text-xs shrink-0" @click="copyKey(store.lastCreatedRawKey!)">Copy</button>
        </div>
        <button class="text-xs text-surface-500 hover:text-surface-300 mt-2" @click="store.clearRawKey()">Dismiss</button>
      </div>

      <!-- Live keys -->
      <div>
        <h2 class="text-sm font-medium text-surface-300 mb-3">Live Keys</h2>
        <div v-if="store.liveKeys.length" class="space-y-2">
          <KeyRow v-for="key in store.liveKeys" :key="key.id" :api-key="key" @revoke="confirmRevoke" @rotate="confirmRotate" />
        </div>
        <p v-else class="text-sm text-surface-500">No live keys</p>
      </div>

      <!-- Test keys -->
      <div>
        <h2 class="text-sm font-medium text-surface-300 mb-3">Test Keys</h2>
        <div v-if="store.testKeys.length" class="space-y-2">
          <KeyRow v-for="key in store.testKeys" :key="key.id" :api-key="key" @revoke="confirmRevoke" @rotate="confirmRotate" />
        </div>
        <p v-else class="text-sm text-surface-500">No test keys</p>
      </div>

      <!-- Revoked keys -->
      <div v-if="store.revokedKeys.length">
        <h2 class="text-sm font-medium text-surface-500 mb-3">Revoked</h2>
        <div class="space-y-2 opacity-50">
          <KeyRow v-for="key in store.revokedKeys" :key="key.id" :api-key="key" disabled />
        </div>
      </div>
    </template>

    <!-- Create modal -->
    <Teleport to="body">
      <div v-if="showCreateModal" class="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4" @click.self="showCreateModal = false">
        <div class="bg-surface-900 border border-surface-700 rounded-xl p-6 w-full max-w-md">
          <h3 class="text-base font-semibold text-surface-100 mb-4">Create API Key</h3>
          <div class="space-y-4">
            <div>
              <label class="text-sm text-surface-400 block mb-1">Environment</label>
              <select v-model="newKeyEnv" class="input-field w-full">
                <option value="LIVE">Live</option>
                <option value="TEST">Test</option>
              </select>
            </div>
            <div>
              <label class="text-sm text-surface-400 block mb-1">Name (optional)</label>
              <input v-model="newKeyName" type="text" class="input-field w-full" placeholder="e.g. Production server" />
            </div>
          </div>
          <div class="flex justify-end gap-2 mt-6">
            <button class="btn-secondary text-sm" @click="showCreateModal = false">Cancel</button>
            <button class="btn-primary text-sm" :disabled="creating" @click="handleCreate">
              {{ creating ? 'Creating...' : 'Create' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Confirm modal -->
    <Teleport to="body">
      <div v-if="confirmAction" class="fixed inset-0 bg-black/60 z-50 flex items-center justify-center p-4" @click.self="confirmAction = null">
        <div class="bg-surface-900 border border-surface-700 rounded-xl p-6 w-full max-w-md">
          <h3 class="text-base font-semibold text-surface-100 mb-2">{{ confirmAction.title }}</h3>
          <p class="text-sm text-surface-400 mb-4">{{ confirmAction.message }}</p>
          <div class="flex justify-end gap-2">
            <button class="btn-secondary text-sm" @click="confirmAction = null">Cancel</button>
            <button class="btn-danger text-sm" :disabled="actionLoading" @click="confirmAction.action()">
              {{ actionLoading ? 'Processing...' : confirmAction.confirmLabel }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import type { APIKeyInfo } from '~/types'

const store = useApiKeysStore()
const { success, error: showError } = useToast()

const showCreateModal = ref(false)
const newKeyEnv = ref<'LIVE' | 'TEST'>('TEST')
const newKeyName = ref('')
const creating = ref(false)
const actionLoading = ref(false)
const confirmAction = ref<{ title: string; message: string; confirmLabel: string; action: () => void } | null>(null)

async function handleCreate() {
  creating.value = true
  try {
    await store.createKey(newKeyEnv.value, newKeyName.value || undefined)
    showCreateModal.value = false
    newKeyName.value = ''
    success('API key created')
  } catch {
    showError('Failed to create API key')
  } finally {
    creating.value = false
  }
}

function confirmRevoke(key: APIKeyInfo) {
  confirmAction.value = {
    title: 'Revoke API Key',
    message: `This will permanently disable key ${key.key_prefix}... Any integrations using this key will stop working.`,
    confirmLabel: 'Revoke',
    action: async () => {
      actionLoading.value = true
      try {
        await store.revokeKey(key.id)
        confirmAction.value = null
        success('API key revoked')
      } catch {
        showError('Failed to revoke key')
      } finally {
        actionLoading.value = false
      }
    },
  }
}

function confirmRotate(key: APIKeyInfo) {
  confirmAction.value = {
    title: 'Rotate API Key',
    message: `This will revoke ${key.key_prefix}... and create a new key. Your integration will need to be updated.`,
    confirmLabel: 'Rotate',
    action: async () => {
      actionLoading.value = true
      try {
        await store.rotateKey(key.id)
        confirmAction.value = null
        success('API key rotated')
      } catch {
        showError('Failed to rotate key')
      } finally {
        actionLoading.value = false
      }
    },
  }
}

function copyKey(raw: string) {
  navigator.clipboard.writeText(raw)
  success('Copied to clipboard')
}

onMounted(() => {
  store.fetchKeys()
})
</script>
