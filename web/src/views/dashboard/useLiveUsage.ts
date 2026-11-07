// Seeded from stored history so the sparklines have a shape before the first live sample.
import { onUnmounted, ref } from 'vue'
import { usageApi } from '@/api/resources'
import type { WorkspaceLiveSample } from '@/api/types'

// SERIES_CAP ≈ the last hour of history plus a few minutes of live points.
const SERIES_CAP = 90

export function useLiveUsage() {
  const sample = ref<WorkspaceLiveSample | null>(null)
  const cpuSeries = ref<number[]>([])
  const memSeries = ref<number[]>([])
  let es: EventSource | null = null

  function push(arr: number[], v: number): number[] {
    const next = [...arr, v]
    return next.length > SERIES_CAP ? next.slice(next.length - SERIES_CAP) : next
  }

  function close() {
    es?.close()
    es = null
  }

  async function seed(id: number | null) {
    cpuSeries.value = []
    memSeries.value = []
    sample.value = null
    if (!id) return
    try {
      const pts = (await usageApi.history(id, '1h')).data.data ?? []
      cpuSeries.value = pts.map((p) => p.cpu_cores)
      memSeries.value = pts.map((p) => p.memory_bytes)
    } catch {
      // Non-critical; the live stream will start filling the series.
    }
  }

  function open(id: number | null) {
    close()
    if (!id) return
    es = new EventSource(usageApi.liveStreamUrl(id))
    es.onmessage = (m) => {
      let s: WorkspaceLiveSample
      try {
        s = JSON.parse(m.data)
      } catch {
        return
      }
      sample.value = s
      cpuSeries.value = push(cpuSeries.value, s.cpu_cores)
      memSeries.value = push(memSeries.value, s.memory_bytes)
    }
  }

  // watch(immediate) fires before mount, so binding teardown here keeps the stream from outliving the page.
  onUnmounted(close)

  function start(id: number | null) {
    seed(id)
    open(id)
  }

  return { sample, cpuSeries, memSeries, start, close }
}
