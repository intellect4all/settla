<template>
  <div>
    <NuxtLayout name="auth">
      <div class="card p-6 animate-slide-up">
        <h2 class="text-xl font-semibold text-surface-100 mb-6">Sign in to your account</h2>

        <div v-if="error" class="mb-4 p-3 bg-red-900/50 border border-red-800 rounded-lg text-red-300 text-sm">
          {{ error }}
        </div>

        <form @submit.prevent="handleLogin" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Email</label>
            <input
              v-model="email"
              type="email"
              required
              class="input w-full"
              :class="{ 'input-error': emailTouched && !email }"
              placeholder="you@company.com"
              aria-describedby="email-error"
              @blur="emailTouched = true"
            />
            <p v-if="emailTouched && !email" id="email-error" class="text-xs text-red-400 mt-1">Email is required</p>
          </div>

          <div>
            <label class="block text-sm font-medium text-surface-300 mb-1">Password</label>
            <input
              v-model="password"
              type="password"
              required
              class="input w-full"
              :class="{ 'input-error': passwordTouched && !password }"
              placeholder="********"
              aria-describedby="password-error"
              @blur="passwordTouched = true"
            />
            <p v-if="passwordTouched && !password" id="password-error" class="text-xs text-red-400 mt-1">Password is required</p>
          </div>

          <AppButton type="submit" :loading="loading" class="w-full">
            Sign in
          </AppButton>
        </form>

        <p class="mt-4 text-center text-sm text-surface-400">
          Don't have an account?
          <NuxtLink to="/auth/register" class="text-violet-400 hover:text-violet-300">Create one</NuxtLink>
        </p>
      </div>
    </NuxtLayout>
  </div>
</template>

<script setup lang="ts">
definePageMeta({ layout: false })

const authStore = useAuthStore()
const router = useRouter()

const email = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')
const emailTouched = ref(false)
const passwordTouched = ref(false)

async function handleLogin() {
  loading.value = true
  error.value = ''
  try {
    await authStore.login(email.value, password.value)
    router.push('/')
  } catch (e: any) {
    error.value = e?.data?.message || e?.message || 'Invalid email or password'
  } finally {
    loading.value = false
  }
}
</script>
