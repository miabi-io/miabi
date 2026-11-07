// One SSE stream for every node, opening with a snapshot, so late or reconnected pages need no refetch.
import { onUnmounted, ref } from 'vue'
import { nodesApi, type NodeStatusEvent } from '@/api/nodes'

// Safety net for what a status event cannot carry: nodes added/removed elsewhere, role changes, version bumps.
const RECONCILE_MS = 30000

export function useNodeStatusStream(onStatus: (e: NodeStatusEvent) => void, onReconcile?: () => void) {
  const streaming = ref(false)

  let es: EventSource | null = null
  let timer: ReturnType<typeof setInterval> | null = null

  function close() {
    es?.close()
    es = null
    if (timer) clearInterval(timer)
    timer = null
    streaming.value = false
  }

  function open() {
    close()
    es = new EventSource(nodesApi.statusEventsUrl())
    es.onopen = () => { streaming.value = true }
    es.onmessage = (m) => {
      let msg: { type?: string; data?: NodeStatusEvent | NodeStatusEvent[] }
      try {
        msg = JSON.parse(m.data)
      } catch {
        return // keep-alive / non-JSON frame
      }
      if (msg.type === 'snapshot' && Array.isArray(msg.data)) msg.data.forEach(onStatus)
      else if (msg.type === 'status' && msg.data && !Array.isArray(msg.data)) onStatus(msg.data)
    }
    es.onerror = () => {
      // EventSource reconnects on its own; reflect the gap so the UI can say it is catching up.
      streaming.value = false
    }
    if (onReconcile) timer = setInterval(onReconcile, RECONCILE_MS)
  }

  onUnmounted(close)

  return { streaming, open, close }
}
