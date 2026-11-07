import { defineStore } from 'pinia'
import { ref } from 'vue'
import { analyticsApi, type AnalyticsReport } from '@/api/analytics'
import { appApi } from '@/api/apps'
import { useWorkspaceStore } from './workspace'

// Range options shared by every analytics page (Cloudflare-style windows).
export const ANALYTICS_RANGES = [
  { key: '30m', label: 'Last 30 min' },
  { key: '1h', label: 'Last hour' },
  { key: '24h', label: 'Last 24 hours' },
  { key: '7d', label: 'Last 7 days' },
  { key: '30d', label: 'Last 30 days' },
] as const

// One report shared by all four sub-pages so switching pages doesn't refetch.
export const useAnalyticsStore = defineStore('analytics', () => {
  const range = ref<string>('24h')
  const appFilter = ref<number | null>(null)

  const report = ref<AnalyticsReport | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const appIds = ref<number[]>([])
  const appNames = ref<Record<number, string>>({})
  // Each app's handle (its name), which the URL carries instead of the id.
  const appHandles = ref<Record<number, string>>({})

  // Polls on its own cadence as a "right now" number; null hides the pill.
  const live = ref<number | null>(null)
  const liveWindowSeconds = ref(300)
  let liveTimer: ReturnType<typeof setInterval> | null = null
  let liveWatchers = 0

  let lastKey = ''

  function key(ws: number) {
    return `${ws}|${range.value}|${appFilter.value ?? 0}`
  }

  async function loadApps(ws: number) {
    try {
      const [all, withData] = await Promise.all([
        appApi.list(ws),
        analyticsApi.apps(ws, range.value),
      ])
      const names: Record<number, string> = {}
      const handles: Record<number, string> = {}
      for (const a of all.data.data ?? []) {
        names[a.id] = a.display_name || a.name
        handles[a.id] = a.name
      }
      appNames.value = names
      appHandles.value = handles
      appIds.value = withData.data.data?.application_ids ?? []
      if (appFilter.value && !appIds.value.includes(appFilter.value)) appFilter.value = null
    } catch {
      // Non-fatal — the report still renders workspace-wide.
    }
  }

  // force bypasses the key guard.
  async function load(force = false) {
    const ws = useWorkspaceStore().currentWorkspaceId
    if (!ws) {
      report.value = null
      return
    }
    const k = key(ws)
    if (!force && k === lastKey && report.value) return
    lastKey = k
    loading.value = true
    error.value = null
    try {
      const res = await analyticsApi.report(ws, range.value, appFilter.value ?? undefined)
      report.value = res.data.data
      await loadApps(ws)
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: { message?: string } } } }
      error.value = err.response?.data?.error?.message || 'Failed to load analytics'
      report.value = null
    } finally {
      loading.value = false
    }
  }

  // A count over a multi-minute window, so polling faster only adds requests.
  const LIVE_POLL_MS = 10_000

  async function loadLive() {
    const ws = useWorkspaceStore().currentWorkspaceId
    if (!ws) return
    try {
      const res = await analyticsApi.live(ws, appFilter.value ?? undefined)
      live.value = res.data.data?.visitors ?? 0
      if (res.data.data?.window_seconds) liveWindowSeconds.value = res.data.data.window_seconds
    } catch {
      // A failed poll keeps the last value rather than flashing the pill away.
    }
  }

  // Ref-counted so polling stops when the last watcher leaves; it also pauses while the tab is hidden.
  function watchLive(): () => void {
    liveWatchers++
    if (liveWatchers === 1) {
      loadLive()
      liveTimer = setInterval(() => {
        if (!document.hidden) loadLive()
      }, LIVE_POLL_MS)
      document.addEventListener('visibilitychange', onVisible)
    }
    let stopped = false
    return () => {
      if (stopped) return
      stopped = true
      liveWatchers--
      if (liveWatchers === 0) {
        if (liveTimer) clearInterval(liveTimer)
        liveTimer = null
        document.removeEventListener('visibilitychange', onVisible)
      }
    }
  }

  function onVisible() {
    if (!document.hidden) loadLive()
  }

  function setRange(r: string) {
    if (r === range.value) return
    range.value = r
    load()
  }

  function setApp(id: number | null) {
    if (id === appFilter.value) return
    appFilter.value = id
    load()
    loadLive() // the pill is app-scoped too
  }

  return {
    range, appFilter, report, loading, error, appIds, appNames, appHandles,
    live, liveWindowSeconds,
    load, setRange, setApp, loadLive, watchLive,
  }
})
