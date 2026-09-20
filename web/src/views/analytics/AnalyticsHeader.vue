<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useAnalyticsStore, ANALYTICS_RANGES } from '@/stores/analytics'
import { useWorkspaceStore } from '@/stores/workspace'
import { analyticsApi } from '@/api/analytics'
import { apiUrl } from '@/api/client'
import { windowLabel } from './timeaxis'

// Shared sticky header for every analytics page: title, sub-page tabs, the app
// filter and the time-range selector. It owns the (re)load lifecycle so each page
// only renders the report, and it mirrors the range + app selection into the URL
// query so a reload or shared link restores the same view.
const store = useAnalyticsStore()
const { range, appFilter, appIds, appNames, report, live, liveWindowSeconds } = storeToRefs(store)
const ws = useWorkspaceStore()
const { currentWorkspaceId } = storeToRefs(ws)
const route = useRoute()
const router = useRouter()

// Length of each range in days, to compare against the retention cap.
const RANGE_DAYS: Record<string, number> = { '30m': 0.5 / 24, '1h': 1 / 24, '24h': 1, '7d': 7, '30d': 30 }
// A range is locked when it reaches past the edition's retention cap (-1 = none).
function rangeLocked(key: string): boolean {
  const cap = report.value?.retention_days ?? -1
  return cap >= 0 && (RANGE_DAYS[key] ?? 0) > cap
}
// The absolute window behind the relative range, in the viewer's own timezone —
// "24h" alone doesn't say where the charts start, and every axis label is local.
const rangeWindow = computed(() => (report.value ? windowLabel(report.value.range.since, report.value.range.until) : ''))
const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local time'
const exportable = computed(() => report.value?.exportable ?? false)
const exportHref = computed(() =>
  currentWorkspaceId.value
    ? apiUrl(analyticsApi.exportPath(currentWorkspaceId.value, range.value, appFilter.value ?? undefined))
    : '#',
)

const tabs = [
  { name: 'analytics.tab.overview', to: '/analytics', icon: 'mdi-view-dashboard-outline' },
  { name: 'analytics.tab.httpTraffic', to: '/analytics/http', icon: 'mdi-earth' },
  { name: 'analytics.tab.performance', to: '/analytics/performance', icon: 'mdi-speedometer' },
  { name: 'analytics.tab.webAnalytics', to: '/analytics/web', icon: 'mdi-account-group-outline' },
]

// Seed the store from ?range=&app= on entry (deep-link / reload), before loading.
function seedFromQuery() {
  const r = typeof route.query.range === 'string' ? route.query.range : ''
  if (r && ANALYTICS_RANGES.some((x) => x.key === r)) store.range = r
  const a = Number(route.query.app)
  if (Number.isFinite(a) && a > 0) store.appFilter = a
}

let stopLive: (() => void) | null = null
onMounted(() => {
  seedFromQuery()
  store.load()
  stopLive = store.watchLive()
})
onBeforeUnmount(() => stopLive?.())
// Reload when the workspace changes (range/app changes go through store actions).
watch(currentWorkspaceId, () => {
  store.load()
  store.loadLive()
})

// The live pill hides until the first poll answers, so it never flashes a 0 that
// only means "not asked yet".
const liveLabel = computed(() => {
  const m = Math.round(liveWindowSeconds.value / 60)
  return `${live.value} visitor${live.value === 1 ? '' : 's'} active in the last ${m} minute${m === 1 ? '' : 's'}`
})

// Mirror the current selection into the URL (replace, so it doesn't spam history).
watch([range, appFilter], () => {
  const query: Record<string, string> = { ...(route.query as Record<string, string>), range: range.value }
  if (appFilter.value) query.app = String(appFilter.value)
  else delete query.app
  if (query.range !== route.query.range || query.app !== route.query.app) {
    router.replace({ query })
  }
}, { immediate: true })

function onAppChange(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  store.setApp(v ? Number(v) : null)
}
</script>

<template>
  <div class="a-header">
    <div class="a-topline">
      <div class="a-title">
        <h1>{{ $t('analytics.analytics') }}</h1>
        <span class="a-ns">{{ ws.contextLabel }}</span>
        <span v-if="rangeWindow" class="a-window" :title="`All times shown in your local timezone (${tz})`">
          <span class="mdi mdi-clock-outline"></span> {{ rangeWindow }}
        </span>
      </div>
      <div class="a-controls">
        <span v-if="live !== null" class="a-live" :class="{ idle: live === 0 }" :title="liveLabel">
          <i class="a-live-dot"></i>
          <b>{{ live }}</b>{{ $t('analytics.live') }}</span>
        <select
          :value="appFilter ?? ''"
          class="form-select a-select" style="max-width: 180px;"
          @change="onAppChange"
        >
          <option value="">{{ $t('analytics.allApplications') }}</option>
          <option v-for="id in appIds" :key="id" :value="id">{{ appNames[id] || `App #${id}` }}</option>
        </select>
        <div class="a-range">
          <button
            v-for="r in ANALYTICS_RANGES"
            :key="r.key"
            class="a-range-btn"
            :class="{ active: range === r.key }"
            :disabled="rangeLocked(r.key)"
            :title="rangeLocked(r.key) ? `Retention is limited to ${report?.retention_days} days on this plan — upgrade to Enterprise for longer history` : ''"
            @click="store.setRange(r.key)"
          >
            {{ $t(r.label) }}
            <span v-if="rangeLocked(r.key)" class="mdi mdi-lock-outline lock"></span>
          </button>
        </div>
        <a
          v-if="exportable"
          class="a-export"
          :href="exportHref"
          download="analytics.csv"
          :title="$t('analytics.exportTheAnalyticsTimeSeries')"
        >
          <span class="mdi mdi-download"></span>{{ $t('analytics.export') }}</a>
        <button
          v-else
          class="a-export locked"
          disabled
          :title="$t('analytics.csvExportIsAnEnterprise')"
        >
          <span class="mdi mdi-lock-outline"></span>{{ $t('analytics.export') }}</button>
      </div>
    </div>

    <nav class="a-tabs">
      <RouterLink v-for="item in tabs" :key="item.to" :to="{ path: item.to, query: route.query }" class="a-tab" active-class="active" exact-active-class="active">
        <span class="mdi" :class="item.icon"></span> {{ $t(item.name) }}
      </RouterLink>
    </nav>
  </div>
</template>

<style scoped>
.a-header { position: sticky; top: 0; z-index: 5; background: var(--bg-primary); padding: 12px 16px 0; margin-bottom: 18px; }
.a-topline { display: flex; justify-content: space-between; align-items: center; gap: 16px; flex-wrap: wrap; }
.a-title { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.a-title h1 { margin: 0; font-size: 24px; line-height: 1.2; }
.a-ns { color: var(--text-muted); font-size: 14px; }
.a-window { color: var(--text-muted); font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }

/* Live visitors: a count of who's on the site right now, so it sits with the
   controls rather than in the report body — it doesn't move with the range. */
.a-live {
  height: 34px; flex-shrink: 0;
  display: inline-flex; align-items: center; gap: 7px;
  padding: 0 12px; border: 1px solid var(--border-primary); border-radius: 8px;
  background: var(--bg-secondary); color: var(--text-secondary);
  font-size: 13px; white-space: nowrap;
}
.a-live b { color: var(--text-primary); font-variant-numeric: tabular-nums; }
.a-live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--success-600); flex: 0 0 auto; animation: a-live-pulse 2s ease-in-out infinite; }
.a-live.idle { color: var(--text-muted); }
.a-live.idle .a-live-dot { background: var(--text-muted); animation: none; }
@keyframes a-live-pulse {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(22, 163, 74, 0.45); }
  50% { opacity: 0.75; box-shadow: 0 0 0 4px rgba(22, 163, 74, 0); }
}
@media (prefers-reduced-motion: reduce) {
  .a-live-dot { animation: none; }
}
.a-window .mdi { font-size: 13px; vertical-align: -1px; }
.a-controls { display: flex; gap: 10px; align-items: center; flex-wrap: nowrap; max-width: 100%;}
.a-select { padding: 7px 10px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); color: var(--text-primary); font-size: 13px; }
.a-range {display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); flex-shrink: 1; min-width: 0; overflow-x: auto; scrollbar-width: none;}
.a-range::-webkit-scrollbar { display: none; }
.a-range::-webkit-scrollbar { display: none; }
.a-range-btn { height: 34px; padding: 0 12px; background: transparent; color: var(--text-muted); border: none; border-left: 1px solid var(--border-primary); cursor: pointer; font-size: 13px; white-space: nowrap; display: inline-flex; align-items: center; justify-content: center; transition: background 0.2s, color 0.2s; }
.a-range-btn:first-child { border-left: none;}
.a-range-btn.active { background: var(--primary-500); color: #fff;}
.a-range-btn:disabled { opacity: 0.5; cursor: not-allowed;}
.a-range-btn .lock { font-size: 12px; margin-left: 4px; opacity: 0.8;}
.a-export { height: 34px; display: inline-flex; align-items: center; gap: 6px; padding: 7px 12px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); color: var(--text-secondary); font-size: 13px; text-decoration: none; cursor: pointer; white-space: nowrap; }
.a-export:hover { color: var(--text-primary); border-color: var(--primary-500); }
.a-export.locked { opacity: 0.6; cursor: not-allowed; }
.a-select,
.a-export {flex-shrink: 0; }
.a-tabs { display: flex; gap: 4px; margin-top: 16px; border-bottom: 1px solid var(--border-primary); overflow-x: auto; }
.a-tab { display: inline-flex; align-items: center; gap: 6px; padding: 10px 16px; border-bottom: 2px solid transparent; color: var(--text-muted); text-decoration: none; font-size: 14px; white-space: nowrap; }
.a-tab:hover { color: var(--text-primary); }
.a-tab.active { color: var(--primary-500); border-bottom-color: var(--primary-500); font-weight: 600; }

@media (max-width: 639px) {
  .a-topline { flex-direction: column; align-items: stretch; gap: 12px; }
  .a-controls { width: 100%; flex-wrap: wrap; gap: 8px; }
  .a-select { flex: 1; max-width: none; min-width: 0; }
  .a-export { height: 36px; flex-shrink: 0; }
  .a-range { order: 3; width: 100%; display: flex; overflow-x: auto; scrollbar-width: none; -webkit-overflow-scrolling: touch; }
  .a-range-btn { flex: 0 0 auto; padding: 0 10px; }
}
</style>
