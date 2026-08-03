<script setup lang="ts">
// Traffic at a glance on the workspace dashboard: the last 24 hours of gateway
// traffic, plus who is on the site right now.
//
// It keeps its own state rather than sharing the analytics store, so opening the
// dashboard never rewrites the range or app filter the /analytics pages remember.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { analyticsApi, type AnalyticsSummary } from '@/api/analytics'
import RequestsChart from '@/views/analytics/RequestsChart.vue'
import Breakdown from '@/views/analytics/Breakdown.vue'
import { fmtNum, fmtMs, fmtPct, delta, fmtDelta } from '@/views/analytics/format'

const props = defineProps<{ workspaceId: number | null }>()
const router = useRouter()

const report = ref<AnalyticsSummary | null>(null)
const live = ref<number | null>(null)
const loading = ref(false)
const failed = ref(false)

// The dashboard shows one fixed window; the analytics pages own range picking.
const RANGE = '24h'
// Live visitors is a count over a multi-minute window, so a slow poll is plenty.
const LIVE_POLL_MS = 15_000
let liveTimer: ReturnType<typeof setInterval> | null = null

async function loadReport(id: number | null) {
  report.value = null
  failed.value = false
  if (!id) return
  loading.value = true
  try {
    report.value = (await analyticsApi.summary(id, RANGE)).data.data
  } catch {
    // Analytics is optional infrastructure (it needs the gateway's event stream
    // and Redis). A failure hides the card rather than breaking the dashboard.
    failed.value = true
  } finally {
    loading.value = false
  }
}

async function loadLive(id: number | null) {
  if (!id) return
  try {
    live.value = (await analyticsApi.live(id)).data.data?.visitors ?? 0
  } catch {
    // Leave the last known value rather than flashing the pill away.
  }
}

function stopPolling() {
  if (liveTimer) clearInterval(liveTimer)
  liveTimer = null
  document.removeEventListener('visibilitychange', onVisible)
}

function onVisible() {
  if (!document.hidden) loadLive(props.workspaceId)
}

watch(
  () => props.workspaceId,
  (id) => {
    stopPolling()
    live.value = null
    loadReport(id)
    if (!id) return
    loadLive(id)
    liveTimer = setInterval(() => {
      if (!document.hidden) loadLive(id)
    }, LIVE_POLL_MS)
    document.addEventListener('visibilitychange', onVisible)
  },
  { immediate: true },
)
onBeforeUnmount(stopPolling)

const totals = computed(() => report.value?.totals ?? null)
const hasTraffic = computed(() => (totals.value?.requests ?? 0) > 0)

// Countries come from the gateway's GeoIP database, which many installs don't
// have — and an empty list looks the same as "no traffic from anywhere". The
// panel is dropped entirely rather than explaining itself; the chart then takes
// the full width, and /analytics/web is where the GeoIP hint belongs.
const topCountries = computed(() => report.value?.top_countries ?? [])
const hasCountries = computed(() => topCountries.value.length > 0)

// 5xx as a share of all requests — the number worth leading with, since a
// workspace can serve plenty of traffic and still be broken.
const serverErrorRate = computed(() => {
  const r = report.value
  if (!r || r.totals.requests === 0) return 0
  return r.status.s5xx / r.totals.requests
})

interface Tile {
  label: string
  value: string
  delta: number | null
  invert?: boolean
  danger?: boolean
}

const tiles = computed<Tile[]>(() => {
  const r = report.value
  if (!r) return []
  return [
    {
      label: 'Requests',
      value: fmtNum(r.totals.requests),
      delta: delta(r.totals.requests, r.compare?.requests),
    },
    {
      label: 'Visitors',
      value: fmtNum(r.totals.unique_visitors),
      delta: delta(r.totals.unique_visitors, r.compare?.unique_visitors),
    },
    {
      label: 'Server errors',
      value: fmtPct(serverErrorRate.value),
      delta: null,
      invert: true,
      danger: serverErrorRate.value >= 0.01,
    },
    {
      label: 'p95 latency',
      value: fmtMs(r.totals.p95_latency_ms),
      delta: delta(r.totals.p95_latency_ms, r.compare?.p95_latency_ms),
      invert: true,
    },
  ]
})

function deltaDir(d: number | null): string {
  if (d === null || Math.abs(d) < 0.001) return 'flat'
  return d > 0 ? 'up' : 'down'
}
</script>

<template>
  <!-- Hidden entirely when analytics isn't reachable: an error box here would
       claim the dashboard is broken when only an optional pillar is. -->
  <div v-if="!failed" class="card traffic-card">
    <div class="card-header">
      <h2>Traffic</h2>
      <div class="tc-actions">
        <span
          v-if="live !== null"
          class="tc-live"
          :class="{ idle: live === 0 }"
          :title="`${live} visitor${live === 1 ? '' : 's'} active in the last few minutes`"
        >
          <i class="tc-live-dot"></i><b>{{ live }}</b> live
        </span>
        <span class="tc-range">Last 24 hours</span>
        <button class="btn btn-ghost btn-sm" @click="router.push('/analytics')">View analytics</button>
      </div>
    </div>

    <div v-if="loading && !report" class="tc-body">
      <div class="tc-tiles">
        <div v-for="i in 4" :key="i" class="tc-tile">
          <span class="skeleton skeleton-text" style="width: 50%"></span>
          <span class="skeleton" style="height: 22px; width: 60%; margin-top: 8px"></span>
        </div>
      </div>
    </div>

    <!-- Split only when there are countries to show: without the aside the grid
         is a single column, so the chart keeps the full width it has today. -->
    <div v-else-if="hasTraffic && report" class="tc-body tc-split" :class="{ 'has-aside': hasCountries }">
      <div class="tc-main">
        <div class="tc-tiles">
          <div v-for="t in tiles" :key="t.label" class="tc-tile">
            <span class="tc-label">{{ t.label }}</span>
            <span class="tc-value" :class="{ danger: t.danger }">
              {{ t.value }}
              <span
                v-if="t.delta !== null && deltaDir(t.delta) !== 'flat'"
                class="tc-delta"
                :class="[deltaDir(t.delta), { invert: t.invert }]"
                title="Change vs the previous 24 hours"
              >{{ fmtDelta(t.delta) }}</span>
            </span>
          </div>
        </div>
        <RequestsChart :series="report.series" :granularity="report.granularity" :height="120" />
      </div>

      <aside v-if="hasCountries" class="tc-aside">
        <Breakdown title="Top countries" :items="topCountries" kind="country" flat>
          <template #action>
            <a class="tc-more" href="#" @click.prevent="router.push('/analytics/http')">Map →</a>
          </template>
        </Breakdown>
      </aside>
    </div>

    <!-- Traffic only exists once an app is routed and reached, so the empty
         state points at the cause rather than just saying "no data". -->
    <div v-else class="empty-state" style="padding: 28px">
      <span class="mdi mdi-chart-line-variant" style="font-size: 32px; color: var(--text-muted)"></span>
      <p>No traffic in the last 24 hours.</p>
      <p class="tc-hint">Analytics are collected by the gateway as requests reach your routed applications.</p>
    </div>
  </div>
</template>

<style scoped>
.traffic-card { margin-bottom: 20px; }
.tc-actions { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.tc-range { font-size: 12px; color: var(--text-muted); white-space: nowrap; }

.tc-live {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; color: var(--text-secondary); white-space: nowrap;
}
.tc-live b { color: var(--text-primary); font-variant-numeric: tabular-nums; }
.tc-live-dot {
  width: 7px; height: 7px; border-radius: 50%; background: var(--success-600);
  animation: tc-pulse 2s ease-in-out infinite;
}
.tc-live.idle { color: var(--text-muted); }
.tc-live.idle .tc-live-dot { background: var(--text-muted); animation: none; }
@keyframes tc-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}
@media (prefers-reduced-motion: reduce) {
  .tc-live-dot { animation: none; }
}

.tc-body { padding: 16px 20px 18px; }

/* One column by default — .has-aside is what opens the rail, so the empty case
   needs no rule of its own. minmax(0, …) on the main column keeps the chart from
   forcing the grid wider than the card. */
.tc-split { display: grid; gap: 20px; }
.tc-split.has-aside { grid-template-columns: minmax(0, 1fr) minmax(230px, 0.36fr); }
.tc-main { min-width: 0; }
.tc-aside { min-width: 0; padding-left: 20px; border-left: 1px solid var(--border-primary); }
.tc-more { font-size: 12px; color: var(--text-muted); white-space: nowrap; }
.tc-more:hover { color: var(--primary-600); }

/* The shared .brow row gives the label 38% of the width, which is fine in a
   half-page card and far too little in a 230px rail — "United States" would
   ellipsize to a few characters. Rebalance for the rail: the label takes what's
   left, the bar keeps a fixed sliver so the ranking still reads visually. */
.tc-aside :deep(.brow-label) { flex: 1 1 auto; min-width: 0; }
.tc-aside :deep(.brow-track) { flex: 0 0 34px; }
.tc-aside :deep(.brow-count) { min-width: 34px; }
/* Slightly tighter than the shared row so the full list ends level with the
   chart's baseline rather than stretching the card past it. */
.tc-aside :deep(.brow) { padding: 4px 0; }
/* Match the stat tiles' label, so the rail reads as part of this card. */
.tc-aside :deep(h3) { font-size: 12px; font-weight: 500; color: var(--text-muted); }
/* Below this the rail is narrower than the flags and counts want; stack instead,
   and swap the divider to the top edge so it still reads as a separate block. */
@media (max-width: 860px) {
  .tc-split.has-aside { grid-template-columns: 1fr; gap: 16px; }
  .tc-aside { padding: 16px 0 0; border-left: 0; border-top: 1px solid var(--border-primary); }
}

.tc-tiles {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 14px; margin-bottom: 18px;
}
.tc-tile { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.tc-label { font-size: 12px; color: var(--text-muted); }
.tc-value { font-size: 22px; font-weight: 700; color: var(--text-primary); letter-spacing: -0.02em; }
.tc-value.danger { color: var(--danger-600); }
.tc-delta { font-size: 12px; font-weight: 600; margin-left: 4px; }
.tc-delta.up { color: var(--success-600); }
.tc-delta.down { color: var(--danger-600); }
.tc-delta.up.invert { color: var(--danger-600); }   /* errors/latency up = bad */
.tc-delta.down.invert { color: var(--success-600); }

.tc-hint { font-size: 12px; color: var(--text-muted); max-width: 46ch; }
</style>
