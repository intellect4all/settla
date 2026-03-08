<template>
  <div class="flex h-screen overflow-hidden">
    <AppSidebar />
    <main class="flex-1 overflow-y-auto">
      <div class="p-6 max-w-[1600px] mx-auto">
        <slot />
      </div>
    </main>
  </div>
</template>
