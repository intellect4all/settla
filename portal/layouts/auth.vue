<template>
  <div class="min-h-screen flex items-center justify-center bg-surface-950">
    <div class="w-full max-w-md px-4 animate-fade-in">
      <div class="text-center mb-8">
        <div class="mx-auto w-10 h-10 bg-violet-600 rounded-xl flex items-center justify-center mb-4">
          <span class="text-white font-bold text-lg">S</span>
        </div>
        <h1 class="text-2xl font-bold text-surface-100">Settla</h1>
        <p class="text-surface-400 mt-1">B2B Stablecoin Settlement</p>
      </div>
      <slot />
    </div>
  </div>
</template>
