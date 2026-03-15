export default defineNuxtConfig({
  devtools: { enabled: true },

  modules: [
    '@nuxtjs/tailwindcss',
    '@nuxtjs/color-mode',
  ],

  colorMode: {
    classSuffix: '',
    preference: 'dark',
    fallback: 'dark',
  },

  tailwindcss: {
    cssPath: '~/assets/css/main.css',
  },

  runtimeConfig: {
    public: {
      apiBase: 'http://localhost:3000',
      dashboardApiKey: '', // Set via NUXT_PUBLIC_DASHBOARD_API_KEY env var
      prometheusBase: process.env.SETTLA_PROMETHEUS_URL || 'http://localhost:9092',
      pollIntervalTransfers: 5000,
      pollIntervalTreasury: 10000,
      pollIntervalCapacity: 3000,
    },
  },

  app: {
    head: {
      title: 'Settla Dashboard',
      link: [
        { rel: 'preconnect', href: 'https://fonts.googleapis.com' },
        { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' },
        { rel: 'stylesheet', href: 'https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap' },
      ],
    },
  },

  compatibilityDate: '2025-01-01',
})
