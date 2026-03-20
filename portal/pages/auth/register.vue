<template>
  <div>
    <NuxtLayout name="auth">
      <div class="card p-6 animate-slide-up">
        <h2 class="text-xl font-semibold text-surface-100 mb-6">Create your account</h2>

        <div v-if="success" class="mb-4 p-3 bg-emerald-900/50 border border-emerald-800 rounded-lg text-emerald-300 text-sm">
          {{ success }}
        </div>

        <div v-if="error" class="mb-4 p-3 bg-red-900/50 border border-red-800 rounded-lg text-red-300 text-sm">
          {{ error }}
        </div>

        <form v-if="!success" @submit.prevent="handleRegister" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Company name</label>
            <input
              v-model="companyName"
              type="text"
              required
              class="input w-full"
              placeholder="Acme Payments"
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Email</label>
            <input
              v-model="email"
              type="email"
              required
              class="input w-full"
              placeholder="you@company.com"
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Password</label>
            <input
              v-model="password"
              type="password"
              required
              minlength="8"
              class="input w-full"
              placeholder="Min 8 characters"
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Confirm password</label>
            <input
              v-model="confirmPassword"
              type="password"
              required
              class="input w-full"
              :class="{ 'input-error': confirmPassword && password !== confirmPassword }"
              placeholder="Repeat password"
              aria-describedby="confirm-error"
            />
            <p v-if="confirmPassword && password !== confirmPassword" id="confirm-error" class="text-xs text-red-400 mt-1">Passwords do not match</p>
          </div>

          <AppButton type="submit" :loading="loading" class="w-full">
            Create account
          </AppButton>
        </form>

        <p class="mt-4 text-center text-sm text-surface-400">
          Already have an account?
          <NuxtLink to="/auth/login" class="text-violet-400 hover:text-violet-300">Sign in</NuxtLink>
        </p>
      </div>
    </NuxtLayout>
  </div>
</template>

<script setup lang="ts">
definePageMeta({ layout: false })

const authStore = useAuthStore()

const companyName = ref('')
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const error = ref('')
const success = ref('')

async function handleRegister() {
  error.value = ''
  success.value = ''

  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match'
    return
  }

  loading.value = true
  try {
    const res = await authStore.register(companyName.value, email.value, password.value)
    success.value = res.message || 'Account created! Check your email for verification.'
  } catch (e: any) {
    error.value = e?.data?.message || e?.message || 'Registration failed'
  } finally {
    loading.value = false
  }
}
</script>
