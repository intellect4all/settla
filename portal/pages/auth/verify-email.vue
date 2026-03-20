<template>
  <div>
    <NuxtLayout name="auth">
      <div class="card p-6 text-center animate-slide-up">
        <div v-if="loading" class="text-surface-400">
          Verifying your email...
        </div>

        <div v-else-if="success">
          <div class="text-emerald-400 text-lg font-medium mb-2">Email verified!</div>
          <p class="text-surface-400 mb-4">You can now sign in to your account.</p>
          <NuxtLink to="/auth/login" class="btn-primary inline-block">
            Sign in
          </NuxtLink>
        </div>

        <div v-else>
          <div class="text-red-400 text-lg font-medium mb-2">Verification failed</div>
          <p class="text-surface-400">{{ error || 'Invalid or expired verification link.' }}</p>
        </div>
      </div>
    </NuxtLayout>
  </div>
</template>

<script setup lang="ts">
definePageMeta({ layout: false })

const route = useRoute()

const loading = ref(true)
const success = ref(false)
const error = ref('')

onMounted(async () => {
  const token = route.query.token as string
  if (!token) {
    loading.value = false
    error.value = 'No verification token provided'
    return
  }

  try {
    const api = usePortalApi()
    await api.verifyEmail(token)
    success.value = true
  } catch (e: any) {
    error.value = e?.data?.message || e?.message || 'Verification failed'
  } finally {
    loading.value = false
  }
})
</script>
