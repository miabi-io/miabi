// The stream opens with a snapshot, so a late or reconnected page is correct without refetching.
import { onUnmounted, ref } from 'vue'
import type { Runner } from '@/api/runners'

// Catches runners added, edited or removed elsewhere, which a status event cannot carry.
const RECONCILE_MS = 30000

export function useRunnerStatusStream(
  onSnapshot: (runners: Runner[]) => void,
  onStatus: (runner: Runner) => void,
  onReconcile?: () => void,
) {
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

  function open(url: string) {
    close()
    es = new EventSource(url)
    es.onopen = () => { streaming.value = true }
    es.onmessage = (m) => {
      let msg: { type?: string; data?: Runner | Runner[] }
      try {
        msg = JSON.parse(m.data)
      } catch {
        return // keep-alive / non-JSON frame
      }
      if (msg.type === 'snapshot' && Array.isArray(msg.data)) onSnapshot(msg.data)
      else if (msg.type === 'status' && msg.data && !Array.isArray(msg.data)) onStatus(msg.data)
    }
    es.onerror = () => {
      // EventSource reconnects on its own; the flag lets the UI say it is catching up.
      streaming.value = false
    }
    if (onReconcile) timer = setInterval(onReconcile, RECONCILE_MS)
  }

  onUnmounted(close)

  return { streaming, open, close }
}
