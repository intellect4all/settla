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
    // Server-only keys — never sent to the browser.
    // The dashboard server proxy (server/api/[...path].ts) reads these to
    // authenticate against the gateway on behalf of the client.
    apiBase: process.env.NUXT_API_BASE || 'http://localhost:3100',
    dashboardApiKey: '', // Set via NUXT_DASHBOARD_API_KEY env var
    opsApiKey: '', // Set via NUXT_OPS_API_KEY env var (SETTLA_OPS_API_KEY value)
    public: {
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

  routeRules: {
    '/**': {
      headers: {
        'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'",
      },
    },
  },

  compatibilityDate: '2025-01-01',
})
