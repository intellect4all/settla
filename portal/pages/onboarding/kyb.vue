<template>
  <div class="max-w-2xl mx-auto">
    <h1 class="text-2xl font-bold text-white mb-2">Complete KYB Verification</h1>
    <p class="text-surface-400 mb-6">Submit your business details to activate your account.</p>

    <div v-if="success" class="p-4 bg-green-900/50 border border-green-800 rounded text-green-300">
      <div class="font-medium">KYB submitted for review</div>
      <p class="text-sm mt-1">We'll review your application and notify you once approved.</p>
    </div>

    <div v-if="error" class="mb-4 p-3 bg-red-900/50 border border-red-800 rounded text-red-300 text-sm">
      {{ error }}
    </div>

    <form v-if="!success" @submit.prevent="handleSubmit" class="space-y-4">
      <div class="card p-6 space-y-4 animate-fade-in">
        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Company registration number</label>
          <input
            v-model="form.company_registration_number"
            type="text"
            required
            class="input w-full"
            placeholder="e.g. 12345678"
          />
        </div>

        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Country</label>
          <select
            v-model="form.country"
            required
            class="input w-full"
          >
            <option value="" disabled>Select country</option>
            <option value="GB">United Kingdom</option>
            <option value="NG">Nigeria</option>
            <option value="GH">Ghana</option>
            <option value="KE">Kenya</option>
            <option value="US">United States</option>
            <option value="CA">Canada</option>
            <option value="DE">Germany</option>
            <option value="FR">France</option>
          </select>
        </div>

        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Business type</label>
          <select
            v-model="form.business_type"
            required
            class="input w-full"
          >
            <option value="" disabled>Select type</option>
            <option value="fintech">Fintech</option>
            <option value="bank">Bank</option>
            <option value="payment_processor">Payment Processor</option>
            <option value="other">Other</option>
          </select>
        </div>

        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Contact name</label>
          <input
            v-model="form.contact_name"
            type="text"
            required
            class="input w-full"
            placeholder="John Doe"
          />
        </div>

        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Contact email</label>
          <input
            v-model="form.contact_email"
            type="email"
            required
            class="input w-full"
            placeholder="contact@company.com"
          />
        </div>

        <div>
          <label class="block text-sm font-medium text-surface-300 mb-1">Contact phone (optional)</label>
          <input
            v-model="form.contact_phone"
            type="tel"
            class="input w-full"
            placeholder="+44 7700 900000"
          />
        </div>
      </div>

      <AppButton type="submit" :loading="loading" class="w-full">Submit KYB</AppButton>
    </form>
  </div>
</template>

<script setup lang="ts">
const api = usePortalApi()
const loading = ref(false)
const error = ref('')
const success = ref(false)

const form = reactive({
  company_registration_number: '',
  country: '',
  business_type: '',
  contact_name: '',
  contact_email: '',
  contact_phone: '',
})

async function handleSubmit() {
  loading.value = true
  error.value = ''
  try {
    await api.submitKYB(form)
    success.value = true
  } catch (e: any) {
    error.value = e?.data?.message || e?.message || 'Failed to submit KYB'
  } finally {
    loading.value = false
  }
}
</script>
