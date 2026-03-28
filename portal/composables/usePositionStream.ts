import { ref, onUnmounted } from 'vue'

/** SSE position update event with enriched position metadata. */
interface PositionUpdateEvent {
  positionId: string
  eventType: string
  amount: string
  balanceAfter: string
  lockedAfter: string
  currency: string
  location: string
  referenceId: string
  referenceType: string
  recordedAt: string
}

/**
 * Composable for real-time treasury position updates via SSE.
 *
 * Connects to the gateway's SSE endpoint and updates the treasury store
 * reactively when position events arrive.
 */
export function usePositionStream() {
  const connected = ref(false)
  const lastEvent = ref<PositionUpdateEvent | null>(null)
  let eventSource: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  const maxReconnectDelay = 30_000

  function connect() {
    if (eventSource) {
      eventSource.close()
    }

    const url = '/api/v1/treasury/stream'
    eventSource = new EventSource(url)

    eventSource.onopen = () => {
      connected.value = true
      reconnectAttempts = 0
    }

    eventSource.addEventListener('position_update', (event: MessageEvent) => {
      try {
        const data = JSON.parse(event.data) as PositionUpdateEvent
        lastEvent.value = data

        // Update the treasury store positions reactively.
        const store = useTreasuryStore()
        const pos = store.positions.find(
          p => p.currency === data.currency && p.location === data.location,
        )
        if (pos) {
          pos.balance = data.balanceAfter
          pos.locked = data.lockedAfter
          // Recompute available.
          const balance = parseFloat(data.balanceAfter || '0')
          const locked = parseFloat(data.lockedAfter || '0')
          pos.available = String(Math.max(0, balance - locked))
          pos.updated_at = data.recordedAt
        }
      } catch {
        // Ignore malformed events.
      }
    })

    eventSource.onerror = () => {
      connected.value = false
      eventSource?.close()
      eventSource = null

      // Exponential backoff reconnect.
      const delay = Math.min(1000 * 2 ** reconnectAttempts, maxReconnectDelay)
      reconnectAttempts++
      reconnectTimer = setTimeout(connect, delay)
    }
  }

  function disconnect() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (eventSource) {
      eventSource.close()
      eventSource = null
    }
    connected.value = false
  }

  onUnmounted(disconnect)

  return {
    connected,
    lastEvent,
    connect,
    disconnect,
  }
}
