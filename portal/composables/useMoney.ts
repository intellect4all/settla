import Decimal from 'decimal.js'

const currencyConfig: Record<string, { symbol: string; decimals: number; locale: string }> = {
  USD: { symbol: '$', decimals: 2, locale: 'en-US' },
  GBP: { symbol: '\u00a3', decimals: 2, locale: 'en-GB' },
  EUR: { symbol: '\u20ac', decimals: 2, locale: 'de-DE' },
  NGN: { symbol: '\u20a6', decimals: 2, locale: 'en-NG' },
  KES: { symbol: 'KSh', decimals: 2, locale: 'en-KE' },
  GHS: { symbol: 'GH\u20b5', decimals: 2, locale: 'en-GH' },
  ZAR: { symbol: 'R', decimals: 2, locale: 'en-ZA' },
  USDT: { symbol: 'USDT', decimals: 6, locale: 'en-US' },
  USDC: { symbol: 'USDC', decimals: 6, locale: 'en-US' },
}

export function useMoney() {
  function format(amount: string | number, currency?: string): string {
    if (!amount) return '\u2014'
    const d = new Decimal(amount)
    const cfg = currency ? currencyConfig[currency.toUpperCase()] : undefined
    const decimals = cfg?.decimals ?? 2

    const formatted = d.toFixed(decimals)
    const parts = formatted.split('.')
    parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ',')

    if (cfg?.symbol) {
      if (['USDT', 'USDC'].includes(currency?.toUpperCase() ?? '')) {
        return `${parts.join('.')} ${cfg.symbol}`
      }
      return `${cfg.symbol}${parts.join('.')}`
    }
    return parts.join('.')
  }

  function formatCompact(amount: string | number, currency?: string): string {
    if (!amount) return '\u2014'
    const d = new Decimal(amount)
    const abs = d.abs()

    if (abs.gte(1_000_000_000)) {
      return `${format(d.div(1_000_000_000).toFixed(1), currency)}B`
    }
    if (abs.gte(1_000_000)) {
      return `${format(d.div(1_000_000).toFixed(1), currency)}M`
    }
    if (abs.gte(1_000)) {
      return `${format(d.div(1_000).toFixed(1), currency)}K`
    }
    return format(amount, currency)
  }

  function bpsToPercent(bps: number): string {
    return new Decimal(bps).div(100).toFixed(2) + '%'
  }

  return { format, formatCompact, bpsToPercent }
}
