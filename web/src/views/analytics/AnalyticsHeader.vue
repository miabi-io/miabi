<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useAnalyticsStore, ANALYTICS_RANGES } from '@/stores/analytics'
import { useWorkspaceStore } from '@/stores/workspace'
import { analyticsApi } from '@/api/analytics'
import { apiUrl } from '@/api/client'

// Shared sticky header for every analytics page: title, sub-page tabs, the app
// filter and the time-range selector. It owns the (re)load lifecycle so each page
// only renders the report, and it mirrors the range + app selection into the URL
// query so a reload or shared link restores the same view.
const store = useAnalyticsStore()
const { range, appFilter, appIds, appNames, report } = storeToRefs(store)
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
const exportable = computed(() => report.value?.exportable ?? false)
const exportHref = computed(() =>
  currentWorkspaceId.value
    ? apiUrl(analyticsApi.exportPath(currentWorkspaceId.value, range.value, appFilter.value ?? undefined))
    : '#',
)

const tabs = [
  { name: 'Overview', to: '/analytics', icon: 'mdi-view-dashboard-outline' },
  { name: 'HTTP Traffic', to: '/analytics/http', icon: 'mdi-earth' },
  { name: 'Performance', to: '/analytics/performance', icon: 'mdi-speedometer' },
  { name: 'Web Analytics', to: '/analytics/web', icon: 'mdi-account-group-outline' },
]

// Seed the store from ?range=&app= on entry (deep-link / reload), before loading.
function seedFromQuery() {
  const r = typeof route.query.range === 'string' ? route.query.range : ''
  if (r && ANALYTICS_RANGES.some((x) => x.key === r)) store.range = r
  const a = Number(route.query.app)
  if (Number.isFinite(a) && a > 0) store.appFilter = a
}

onMounted(() => {
  seedFromQuery()
  store.load()
})
// Reload when the workspace changes (range/app changes go through store actions).
watch(currentWorkspaceId, () => store.load())

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
        <h1>Analytics</h1>
        <span class="a-ns">{{ ws.contextLabel }}</span>
      </div>
      <div class="a-controls">
        <select
          :value="appFilter ?? ''"
          class="a-select"
          @change="onAppChange"
        >
          <option value="">All applications</option>
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
            {{ r.label }}
            <span v-if="rangeLocked(r.key)" class="mdi mdi-lock-outline lock"></span>
          </button>
        </div>
        <a
          v-if="exportable"
          class="a-export"
          :href="exportHref"
          download="analytics.csv"
          title="Export the analytics time series as CSV"
        >
          <span class="mdi mdi-download"></span> Export
        </a>
        <button
          v-else
          class="a-export locked"
          disabled
          title="CSV export is an Enterprise feature"
        >
          <span class="mdi mdi-lock-outline"></span> Export
        </button>
      </div>
    </div>

    <nav class="a-tabs">
      <RouterLink v-for="t in tabs" :key="t.to" :to="{ path: t.to, query: route.query }" class="a-tab" active-class="active" exact-active-class="active">
        <span class="mdi" :class="t.icon"></span> {{ t.name }}
      </RouterLink>
    </nav>
  </div>
</template>

<style scoped>
.a-header { position: sticky; top: 0; z-index: 5; background: var(--bg-primary); padding-bottom: 2px; margin-bottom: 18px; }
.a-topline { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; flex-wrap: wrap; }
.a-title { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.a-title h1 { margin: 0; }
.a-ns { color: var(--text-muted); font-size: 14px; }
.a-controls { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.a-select { padding: 7px 10px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); color: var(--text-primary); font-size: 13px; }
.a-range { display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; }
.a-range-btn { padding: 7px 12px; background: var(--bg-secondary); color: var(--text-muted); border: none; border-left: 1px solid var(--border-primary); cursor: pointer; font-size: 13px; white-space: nowrap; }
.a-range-btn:first-child { border-left: none; }
.a-range-btn.active { background: var(--primary-500); color: #fff; }
.a-range-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.a-range-btn .lock { font-size: 12px; margin-left: 3px; opacity: 0.8; }

.a-export { display: inline-flex; align-items: center; gap: 6px; padding: 7px 12px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); color: var(--text-secondary); font-size: 13px; text-decoration: none; cursor: pointer; white-space: nowrap; }
.a-export:hover { color: var(--text-primary); border-color: var(--primary-500); }
.a-export.locked { opacity: 0.6; cursor: not-allowed; }

.a-tabs { display: flex; gap: 4px; margin-top: 16px; border-bottom: 1px solid var(--border-primary); overflow-x: auto; }
.a-tab { display: inline-flex; align-items: center; gap: 6px; padding: 10px 16px; border-bottom: 2px solid transparent; color: var(--text-muted); text-decoration: none; font-size: 14px; white-space: nowrap; }
.a-tab:hover { color: var(--text-primary); }
.a-tab.active { color: var(--primary-500); border-bottom-color: var(--primary-500); font-weight: 600; }
</style>
