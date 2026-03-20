<template>
  <div class="space-y-6">
    <div class="flex items-start justify-between flex-wrap gap-4">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">API Keys</h1>
        <p class="text-sm text-surface-500 mt-1">Manage your live and test API keys</p>
      </div>
      <AppButton size="sm" @click="showCreateModal = true">Create Key</AppButton>
    </div>

    <!-- Skeleton loading -->
    <template v-if="store.loading">
      <div class="space-y-3">
        <SkeletonLoader variant="text" :lines="1" width="120px" />
        <SkeletonLoader v-for="i in 3" :key="i" variant="card" height="64px" />
      </div>
      <div class="space-y-3">
        <SkeletonLoader variant="text" :lines="1" width="100px" />
        <SkeletonLoader v-for="i in 2" :key="i" variant="card" height="64px" />
      </div>
    </template>

    <template v-else>
      <!-- Raw key banner (shown once after create/rotate) -->
      <div v-if="store.lastCreatedRawKey" class="bg-emerald-900/20 border border-emerald-700 rounded-lg p-4 animate-fade-in">
        <p class="text-sm font-medium text-emerald-300 mb-2">New API key created</p>
        <p class="text-xs text-emerald-400 mb-2">Copy this key now. It will not be shown again.</p>
        <div class="flex items-center gap-2">
          <code class="flex-1 text-xs font-mono bg-surface-950 text-surface-200 px-3 py-2 rounded border border-surface-700 break-all">
            {{ store.lastCreatedRawKey }}
          </code>
          <AppButton size="sm" @click="copyKey(store.lastCreatedRawKey!)">Copy</AppButton>
        </div>
        <button class="text-xs text-surface-500 hover:text-surface-300 mt-2" @click="store.clearRawKey()">Dismiss</button>
      </div>

      <!-- Live keys -->
      <div class="animate-fade-in">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Live Keys</h2>
        <div v-if="store.liveKeys.length" class="space-y-2">
          <KeyRow v-for="key in store.liveKeys" :key="key.id" :api-key="key" @revoke="confirmRevoke" @rotate="confirmRotate" />
        </div>
        <p v-else class="text-sm text-surface-500">No live keys</p>
      </div>

      <!-- Test keys -->
      <div class="animate-fade-in">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Test Keys</h2>
        <div v-if="store.testKeys.length" class="space-y-2">
          <KeyRow v-for="key in store.testKeys" :key="key.id" :api-key="key" @revoke="confirmRevoke" @rotate="confirmRotate" />
        </div>
        <p v-else class="text-sm text-surface-500">No test keys</p>
      </div>

      <!-- Revoked keys -->
      <div v-if="store.revokedKeys.length" class="animate-fade-in">
        <h2 class="text-sm font-medium text-surface-500 mb-3">Revoked</h2>
        <div class="space-y-2 opacity-50">
          <KeyRow v-for="key in store.revokedKeys" :key="key.id" :api-key="key" disabled />
        </div>
      </div>
    </template>

    <!-- Create modal -->
    <Modal :open="showCreateModal" title="Create API Key" size="sm" @close="showCreateModal = false">
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
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="showCreateModal = false">Cancel</AppButton>
        <AppButton size="sm" :loading="creating" @click="handleCreate">Create</AppButton>
      </template>
    </Modal>

    <!-- Confirm modal -->
    <Modal :open="!!confirmAction" :title="confirmAction?.title ?? ''" size="sm" @close="confirmAction = null">
      <p class="text-sm text-surface-400">{{ confirmAction?.message }}</p>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="confirmAction = null">Cancel</AppButton>
        <AppButton variant="danger" size="sm" :loading="actionLoading" @click="confirmAction?.action()">
          {{ confirmAction?.confirmLabel }}
        </AppButton>
      </template>
    </Modal>
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
