import { ref, onUnmounted } from 'vue';

/**
 * WebSocket composable for real-time event streaming.
 * Falls back gracefully if WebSocket connection fails.
 */
export function useWebSocket(url: string) {
  const connected = ref(false);
  const data = ref<Record<string, any>>({});
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let destroyed = false;

  function connect() {
    if (destroyed) return;
    try {
      ws = new WebSocket(url);
    } catch {
      scheduleReconnect();
      return;
    }

    ws.onopen = () => {
      connected.value = true;
    };

    ws.onclose = () => {
      connected.value = false;
      scheduleReconnect();
    };

    ws.onerror = () => {
      // onclose will fire after onerror
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.topic) {
          data.value = { ...data.value, [msg.topic]: msg.payload };
        }
      } catch {
        // Ignore malformed messages
      }
    };
  }

  function scheduleReconnect() {
    if (destroyed) return;
    reconnectTimer = setTimeout(connect, 3000);
  }

  function subscribe(topics: string[]) {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'subscribe', topics }));
    }
  }

  function close() {
    destroyed = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    ws?.close();
  }

  connect();

  onUnmounted(close);

  return { connected, data, subscribe, close };
}
