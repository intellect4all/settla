<template>
  <div class="min-h-screen bg-surface-950 flex items-center justify-center">
    <div class="w-full max-w-md mx-auto px-4">
      <!-- Settla Branding -->
      <div class="text-center mb-6">
        <div class="mx-auto w-10 h-10 bg-violet-600 rounded-xl flex items-center justify-center mb-3">
          <span class="text-white font-bold text-lg">S</span>
        </div>
        <h1 class="text-xl font-bold text-surface-100 tracking-tight">Settla</h1>
        <p class="text-xs text-surface-500 mt-1">Secure Payment</p>
      </div>

      <!-- Card -->
      <div class="card shadow-xl overflow-hidden animate-slide-up">
        <slot />
      </div>

      <!-- Footer -->
      <p class="text-center text-xs text-surface-600 mt-4">
        Powered by Settla &middot; Secure stablecoin infrastructure
      </p>
    </div>
  </div>
</template>
