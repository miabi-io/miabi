<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { useAuthStore } from '@/stores/auth'
import { workspaceApi } from '@/api/workspaces'
import { stackApi } from '@/api/stacks'
import { domainApi } from '@/api/domains'
import { eventsApi } from '@/api/events'
import { usageApi } from '@/api/resources'
import GettingStarted from '@/components/GettingStarted.vue'
import Sparkline from '@/components/Sparkline.vue'
import type { AppEvent, Overview, PendingInvitation, RecentEvent, Stack, WorkspaceLiveSample } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const auth = useAuthStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)
const overview = ref<Overview | null>(null)
const stacks = ref<Stack[]>([])
const domainCount = ref(0)
const loading = ref(false)

// Live stream: a workspace-wide SSE feed of application events. While the user
// is on the dashboard, activity and health update in place — no page refresh.
let es: EventSource | null = null
let refreshTimer: ReturnType<typeof setTimeout> | null = null
const streamLive = ref(false)
// id of the most recently arrived event, briefly highlighted in the timeline.
const freshEventId = ref<number | null>(null)

// Live resources: the current aggregated sample (SSE) plus rolling series seeded
// from stored history, driving the compact CPU/memory strip and its sparklines.
const liveUsage = ref<WorkspaceLiveSample | null>(null)
const cpuSeries = ref<number[]>([])
const memSeries = ref<number[]>([])
const SERIES_CAP = 90 // ~ last hour of history + a few minutes of live points
let usageES: EventSource | null = null

async function seedUsageHistory(id: number | null) {
  cpuSeries.value = []
  memSeries.value = []
  liveUsage.value = null
  if (!id) return
  try {
    const pts = (await usageApi.history(id, '1h')).data.data ?? []
    cpuSeries.value = pts.map((p) => p.cpu_cores)
    memSeries.value = pts.map((p) => p.memory_bytes)
  } catch {
    // Non-critical; the live stream will start filling the series.
  }
}

function pushSeries(arr: number[], v: number): number[] {
  const next = [...arr, v]
  return next.length > SERIES_CAP ? next.slice(next.length - SERIES_CAP) : next
}

function openUsageStream(id: number | null) {
  closeUsageStream()
  if (!id) return
  usageES = new EventSource(usageApi.liveStreamUrl(id))
  usageES.onmessage = (m) => {
    let s: WorkspaceLiveSample
    try { s = JSON.parse(m.data) } catch { return }
    liveUsage.value = s
    cpuSeries.value = pushSeries(cpuSeries.value, s.cpu_cores)
    memSeries.value = pushSeries(memSeries.value, s.memory_bytes)
  }
}
function closeUsageStream() {
  usageES?.close()
  usageES = null
}
function fmtBytes(n?: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

// Getting-started checklist: each step's done state is derived from real
// resources, so it ticks itself. Steps are monotonic (a later one can't complete
// before an earlier one), and the platform's own "Miabi System" workspace is
// ignored for the freshly-seeded admin.
const showOnboarding = computed(() => !auth.user?.onboarding_dismissed)
const hasWorkspace = computed(() => ws.workspaces.some((w) => !w.system))
const hasApp = computed(() => hasWorkspace.value && (overview.value?.total_apps ?? 0) > 0)
const hasDomain = computed(() => hasApp.value && domainCount.value > 0)

// Time-of-day greeting with the user's first name.
const greeting = computed(() => {
  const h = new Date().getHours()
  const part = h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
  const name = auth.user?.name?.trim().split(' ')[0]
  return name ? `${part}, ${name}` : part
})

// At-a-glance health derived from the overview counts.
const health = computed(() => {
  const o = overview.value
  if (!o) return { tone: 'neutral', icon: 'mdi-circle-outline', text: 'Loading…' }
  if (o.failed > 0) {
    return { tone: 'danger', icon: 'mdi-alert-circle', text: `${o.failed} application${o.failed === 1 ? '' : 's'} failing` }
  }
  if (o.total_apps === 0) {
    return { tone: 'neutral', icon: 'mdi-information-outline', text: 'No applications yet' }
  }
  return { tone: 'success', icon: 'mdi-check-circle', text: 'All applications healthy' }
})

const invitations = ref<PendingInvitation[]>([])
const acceptingId = ref<number | null>(null)

async function loadInvitations() {
  try {
    invitations.value = (await workspaceApi.myInvitations()).data.data ?? []
  } catch {
    // Non-critical; the dashboard works without it.
  }
}

async function accept(inv: PendingInvitation) {
  acceptingId.value = inv.id
  try {
    await workspaceApi.acceptInvitation(inv.id)
    notify.success(`Joined ${inv.workspace_name}`)
    await ws.fetchWorkspaces()
    ws.setWorkspace(inv.workspace_id)
    await loadInvitations()
  } catch (e) {
    notify.apiError(e, 'Failed to accept invitation')
  } finally {
    acceptingId.value = null
  }
}

onMounted(loadInvitations)

async function load(id: number | null) {
  overview.value = null
  stacks.value = []
  domainCount.value = 0
  if (!id) return
  loading.value = true
  try {
    overview.value = (await workspaceApi.overview(id)).data.data
    stacks.value = (await stackApi.list(id)).data.data ?? []
    // Domain count drives the onboarding checklist; only fetched when the card
    // is still showing, to avoid an extra request for settled users.
    if (showOnboarding.value) {
      domainCount.value = (await domainApi.list(id)).data.data?.length ?? 0
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// refreshOverview re-fetches the overview (and stacks) without tearing the page
// down to a skeleton — used by the live stream so counts, per-app status and
// health stay truthful as events arrive.
async function refreshOverview(id: number | null) {
  if (!id) return
  try {
    const [ov, st] = await Promise.all([workspaceApi.overview(id), stackApi.list(id)])
    overview.value = ov.data.data
    stacks.value = st.data.data ?? []
  } catch {
    // Transient; the next event (or the stream reconnect) will retry.
  }
}

// scheduleRefresh debounces overview refreshes so a burst of events (e.g. a
// deploy emitting several) triggers a single reconcile.
function scheduleRefresh(id: number | null) {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => refreshOverview(id), 1200)
}

// onStreamEvent prepends a live event to the timeline (enriched with its app's
// name from the current overview) and schedules a reconcile of the counts/health.
function onStreamEvent(id: number | null, e: AppEvent) {
  const ov = overview.value
  if (!ov) return
  const app = ov.apps?.find((a) => a.id === e.application_id)
  const enriched: RecentEvent = { ...e, app_name: app?.name ?? '', app_display_name: app?.display_name ?? '' }
  const existing = ov.recent_events ?? []
  // Dedupe (the same id can't appear twice) and cap the live feed length.
  ov.recent_events = [enriched, ...existing.filter((x) => x.id !== enriched.id)].slice(0, 12)
  freshEventId.value = enriched.id
  scheduleRefresh(id)
}

function closeStream() {
  if (es) {
    es.close()
    es = null
  }
  streamLive.value = false
  closeUsageStream()
}

function openStream(id: number | null) {
  closeStream()
  if (!id) return
  es = new EventSource(eventsApi.workspaceStreamUrl(id))
  es.onopen = () => {
    streamLive.value = true
  }
  es.onmessage = (m) => {
    let msg: { type?: string; data?: AppEvent }
    try {
      msg = JSON.parse(m.data)
    } catch {
      return // keep-alive / non-JSON frame
    }
    if (msg.type === 'event' && msg.data) onStreamEvent(id, msg.data)
  }
  es.onerror = () => {
    // EventSource auto-reconnects on transient errors; reflect the gap in the UI.
    streamLive.value = false
  }
}

onUnmounted(closeStream)

function stackBadge(s: Stack) {
  const total = s.status?.total ?? 0
  const running = s.status?.running ?? 0
  if (total === 0) return 'badge-neutral'
  if (running === total) return 'badge-success'
  if (running === 0) return 'badge-danger'
  return 'badge-warning'
}

watch(
  currentWorkspaceId,
  (id) => {
    load(id)
    openStream(id)
    seedUsageHistory(id)
    openUsageStream(id)
  },
  { immediate: true },
)

function healthBadge(health: string) {
  return health === 'healthy' ? 'badge-success' : health === 'unhealthy' ? 'badge-danger' : 'badge-neutral'
}
function statusBadge(status: string) {
  return status === 'running' ? 'badge-success' : status === 'failed' ? 'badge-danger' : 'badge-neutral'
}

function eventIcon(type: string): string {
  if (type === 'app.created') return 'mdi-cube-outline'
  if (type === 'app.deleted') return 'mdi-delete-outline'
  if (type.startsWith('deploy')) return 'mdi-rocket-launch-outline'
  if (type.startsWith('rollback')) return 'mdi-backup-restore'
  if (type.startsWith('release')) return 'mdi-tag-outline'
  if (type === 'container.died' || type === 'container.oom') return 'mdi-alert-circle-outline'
  if (type === 'container.health') return 'mdi-heart-pulse'
  if (type.startsWith('container')) return 'mdi-cube-outline'
  if (type.startsWith('domain')) return 'mdi-web'
  if (type.startsWith('env')) return 'mdi-tune-variant'
  if (type.startsWith('volume')) return 'mdi-harddisk'
  if (type.startsWith('settings')) return 'mdi-cog-outline'
  return 'mdi-circle-small'
}

function fmtDate(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function relTime(ts: string): string {
  const d = new Date(ts).getTime()
  const diff = Date.now() - d
  const s = Math.round(diff / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return new Date(ts).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ greeting }}</h1>
        <p class="subtitle">
          Overview of <strong>{{ ws.contextLabel }}</strong>
          <span v-if="overview" class="health-pill" :class="[`health-${health.tone}`, { 'health-attention': health.tone === 'danger' }]">
            <span class="mdi" :class="health.icon"></span> {{ health.text }}
          </span>
          <span v-if="overview" class="live-pill" :class="{ 'is-live': streamLive }" :title="streamLive ? 'Live — updates as things happen' : 'Reconnecting…'">
            <span class="live-dot"></span>
          </span>
        </p>
      </div>
      <div class="page-header-actions">
      <button v-if="ws.canEdit" class="btn btn-primary" @click="router.push('/apps')">
        <span class="mdi mdi-plus"></span> New application
      </button>
    </div>
    </div>

    <!-- Getting-started checklist (first-login onboarding) -->
    <GettingStarted
      v-if="showOnboarding"
      :has-workspace="hasWorkspace"
      :has-app="hasApp"
      :has-domain="hasDomain"
    />

    <!-- Quick actions -->
    <div v-if="ws.isWorkspaceContext && ws.canEdit" class="quick-actions">
      <button class="quick-action card qa-primary" @click="router.push('/apps')">
        <span class="qa-icon stat-icon-primary"><span class="mdi mdi-cube-outline"></span></span>
        <span class="qa-text"><span class="qa-title">Deploy application</span><span class="qa-sub">From image or Git</span></span>
      </button>
      <button class="quick-action card qa-info" @click="router.push('/databases')">
        <span class="qa-icon stat-icon-info"><span class="mdi mdi-database-plus-outline"></span></span>
        <span class="qa-text"><span class="qa-title">New database</span><span class="qa-sub">Postgres, MySQL, Redis…</span></span>
      </button>
      <button class="quick-action card qa-secondary" @click="router.push('/stacks')">
        <span class="qa-icon stat-icon-secondary"><span class="mdi mdi-layers-outline"></span></span>
        <span class="qa-text"><span class="qa-title">Create stack</span><span class="qa-sub">Compose multiple apps</span></span>
      </button>
      <button class="quick-action card qa-success" @click="router.push('/routes')">
        <span class="qa-icon stat-icon-success"><span class="mdi mdi-routes"></span></span>
        <span class="qa-text"><span class="qa-title">Add route</span><span class="qa-sub">Expose an app on a domain</span></span>
      </button>
      <button class="quick-action card qa-danger" @click="router.push('/secrets')">
        <span class="qa-icon stat-icon-danger"><span class="mdi mdi-key-variant"></span></span>
        <span class="qa-text"><span class="qa-title">Add secret</span><span class="qa-sub">Store a reusable value</span></span>
      </button>
      <button class="quick-action card qa-warning" @click="router.push('/gitops')">
        <span class="qa-icon stat-icon-warning"><span class="mdi mdi-git"></span></span>
        <span class="qa-text"><span class="qa-title">GitOps</span><span class="qa-sub">Deploy from a Git repo</span></span>
      </button>
    </div>

    <!-- Pending workspace invitations addressed to the current user. -->
    <div v-if="invitations.length" class="card invites-card">
      <div class="card-header">
        <h2><span class="mdi mdi-email-outline"></span> Workspace invitations</h2>
      </div>
      <ul class="invites">
        <li v-for="inv in invitations" :key="inv.id" class="invite">
          <div class="invite-info">
            <span class="invite-name">{{ inv.workspace_name }}</span>
            <span class="invite-sub">
              Invited as <strong>{{ inv.role }}</strong>
              <template v-if="inv.invited_by_name"> by {{ inv.invited_by_name }}</template>
            </span>
          </div>
          <button class="btn btn-primary btn-sm" :disabled="acceptingId === inv.id" @click="accept(inv)">
            {{ acceptingId === inv.id ? 'Joining…' : 'Accept' }}
          </button>
        </li>
      </ul>
    </div>

    <div v-if="loading && !overview" class="stats-grid">
      <div v-for="i in 4" :key="i" class="stat-card"><span class="skeleton skeleton-text" style="width: 40%"></span><span class="skeleton" style="height: 28px; width: 50%; margin-top: 10px"></span></div>
    </div>

    <template v-else-if="overview">
      <div class="stats-grid">
        <div class="stat-card stat-card-clickable" @click="router.push('/apps')">
          <div class="stat-header">
            <span class="stat-label">Applications</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-cube-outline"></span></span>
          </div>
          <div class="stat-value">{{ overview.total_apps }}</div>
        </div>
        <div class="stat-card stat-card-clickable" @click="router.push('/apps')">
          <div class="stat-header">
            <span class="stat-label">Running</span>
            <span class="stat-icon stat-icon-success"><span class="mdi mdi-play-circle-outline"></span></span>
          </div>
          <div class="stat-value">{{ overview.running }}</div>
        </div>
        <div class="stat-card stat-card-clickable" :class="{ 'stat-card-alert': overview.failed > 0 }" @click="router.push('/apps')">
          <div class="stat-header">
            <span class="stat-label">Failed</span>
            <span class="stat-icon stat-icon-danger"><span class="mdi mdi-alert-circle-outline"></span></span>
          </div>
          <div class="stat-value">{{ overview.failed }}</div>
        </div>
        <div class="stat-card stat-card-clickable" @click="router.push('/databases')">
          <div class="stat-header">
            <span class="stat-label">Databases</span>
            <span class="stat-icon stat-icon-info"><span class="mdi mdi-database-outline"></span></span>
          </div>
          <div class="stat-value">{{ overview.databases }}</div>
        </div>
        <div class="stat-card stat-card-clickable" @click="router.push('/stacks')">
          <div class="stat-header">
            <span class="stat-label">Stacks</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-layers-outline"></span></span>
          </div>
          <div class="stat-value">{{ overview.stacks }}</div>
        </div>
      </div>

      <!-- Live resources: current CPU/memory across running containers + trend -->
      <div v-if="overview.total_apps > 0" class="card resources-card">
        <div class="card-header">
          <h2>Resources</h2>
          <span class="live-pill" :class="{ 'is-live': !!liveUsage }" :title="liveUsage ? 'Live' : 'Connecting…'">
            <span class="live-dot"></span> {{ liveUsage ? `${liveUsage.containers} container${liveUsage.containers === 1 ? '' : 's'}` : 'Connecting…' }}
          </span>
        </div>
        <div class="resources-body">
          <div class="resource">
            <div class="resource-head">
              <span class="resource-label"><span class="mdi mdi-chip"></span> CPU</span>
              <span class="resource-value">{{ (liveUsage?.cpu_cores ?? 0).toFixed(2) }} <small>cores</small></span>
            </div>
            <Sparkline :values="cpuSeries" :width="220" :height="40" stroke="var(--primary-500)" />
          </div>
          <div class="resource">
            <div class="resource-head">
              <span class="resource-label"><span class="mdi mdi-memory"></span> Memory</span>
              <span class="resource-value">{{ fmtBytes(liveUsage?.memory_bytes) }}</span>
            </div>
            <Sparkline :values="memSeries" :width="220" :height="40" stroke="var(--info-500, #0ea5e9)" />
          </div>
          <div class="resource">
            <div class="resource-head">
              <span class="resource-label"><span class="mdi mdi-swap-vertical"></span> Network</span>
            </div>
            <div class="net-figures">
              <span class="net-figure"><span class="mdi mdi-arrow-down"></span> {{ fmtBytes(liveUsage?.net_rx_bytes) }} <small>RX</small></span>
              <span class="net-figure"><span class="mdi mdi-arrow-up"></span> {{ fmtBytes(liveUsage?.net_tx_bytes) }} <small>TX</small></span>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <h2>Applications</h2>
          <button class="btn btn-ghost btn-sm" @click="router.push('/apps')">View all</button>
        </div>
        <div v-if="!overview.apps || overview.apps.length === 0" class="empty-state">
          <span class="mdi mdi-cube-outline" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>No applications yet</h3>
          <p>Deploy your first application to get started.</p>
          <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="router.push('/apps')">Deploy application</button>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Application</th><th>Node</th><th>Created</th><th>Status</th><th class="text-right">Health</th></tr>
            </thead>
            <tbody>
              <tr v-for="a in overview.apps" :key="a.id" class="row-clickable" @click="router.push(`/apps/${a.id}`)">
                <td>
                  <div class="cell-id">
                    <span class="avatar avatar-sm">{{ (a.display_name || a.name).charAt(0).toUpperCase() }}</span>
                    <span class="cell-text">
                      <span class="cell-title">{{ a.display_name || a.name }}</span>
                      <span class="cell-sub">{{ a.name }}</span>
                    </span>
                  </div>
                </td>
                <td>
                  <span class="node-cell">
                    <span class="mdi mdi-server-network"></span> {{ a.server_name || 'Local' }}
                  </span>
                </td>
                <td class="cell-sub">{{ fmtDate(a.created_at) }}</td>
                <td><span class="badge badge-dot" :class="statusBadge(a.status)">{{ a.status }}</span></td>
                <td class="text-right"><span class="badge" :class="healthBadge(a.health)">{{ a.health }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="stacks.length" class="card" style="margin-top: 20px">
        <div class="card-header">
          <h2>Stacks</h2>
          <button class="btn btn-ghost btn-sm" @click="router.push('/stacks')">View all</button>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Stack</th><th>Apps</th><th>Created</th><th class="text-right">Status</th></tr></thead>
            <tbody>
              <tr v-for="s in stacks" :key="s.id" class="row-clickable" @click="router.push(`/stacks/${s.id}`)">
                <td>
                  <div class="cell-id">
                    <span class="avatar avatar-sm"><span class="mdi mdi-layers-outline" style="font-size: 14px"></span></span>
                    <span class="cell-text"><span class="cell-title">{{ s.name }}</span></span>
                  </div>
                </td>
                <td class="cell-sub">{{ s.app_count ?? 0 }}</td>
                <td class="cell-sub">{{ fmtDate(s.created_at) }}</td>
                <td class="text-right">
                  <span class="badge badge-dot" :class="stackBadge(s)">{{ s.status?.running ?? 0 }}/{{ s.status?.total ?? 0 }} running</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card" style="margin-top: 20px">
        <div class="card-header">
          <h2>Latest events</h2>
          <button class="btn btn-ghost btn-sm" @click="router.push('/apps')">View applications</button>
        </div>
        <div v-if="!overview.recent_events || overview.recent_events.length === 0" class="empty-state" style="padding: 28px">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>No application activity yet.</p>
        </div>
        <ul v-else class="timeline">
          <li v-for="e in overview.recent_events" :key="e.id" class="event row-clickable" :class="{ 'event-fresh': e.id === freshEventId }" @click="router.push(`/apps/${e.application_id}`)">
            <span class="event-icon" :class="`sev-${e.severity}`"><span class="mdi" :class="eventIcon(e.type)"></span></span>
            <div class="event-body">
              <div class="event-row">
                <span class="event-msg">{{ e.message || e.type }}</span>
                <span class="event-time">{{ relTime(e.created_at) }}</span>
              </div>
              <span class="event-type">{{ e.app_display_name || e.app_name || `app #${e.application_id}` }} · {{ e.type }}</span>
            </div>
          </li>
        </ul>
      </div>
    </template>

    <div v-else-if="ws.loaded && !currentWorkspaceId" class="card">
      <div class="empty-state">
        <span class="mdi mdi-briefcase-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No workspace yet</h3>
        <p v-if="invitations.length">Accept an invitation above to join a workspace.</p>
        <p v-else>Create a workspace to get started.</p>
        <button class="btn btn-primary mt-4" @click="router.push('/workspaces')">Create workspace</button>
      </div>
    </div>

    <div v-else class="loading-page"><span class="spinner"></span></div>
  </div>
</template>

<style scoped>
.subtitle {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 4px;
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
  min-width: 0;
}
.subtitle strong { color: var(--text-secondary); word-break: break-word; }

/* Health pill */
.health-pill {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 3px 10px; border-radius: 9999px; font-size: 12px; font-weight: 600;
}
.health-pill .mdi { font-size: 14px; }
.health-success { background: var(--success-50); color: var(--success-600); }
.health-danger { background: var(--danger-50); color: var(--danger-600); }
.health-neutral { background: var(--bg-tertiary); color: var(--text-muted); }
/* Pulse the health pill while something is failing, to draw the eye. */
.health-attention { animation: health-pulse 1.6s ease-in-out infinite; }
@keyframes health-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(220, 38, 38, 0.4); }
  50% { box-shadow: 0 0 0 5px rgba(220, 38, 38, 0); }
}

/* Live indicator: a small connected/animated dot next to the health pill. */
.live-pill {
  display: inline-flex; align-items: center; gap: 5px;
  font-size: 11px; font-weight: 600; color: var(--text-muted);
}
.live-dot {
  width: 7px; height: 7px; border-radius: 50%;
  background: var(--text-muted); flex-shrink: 0;
}
.live-pill.is-live { color: var(--success-600); }
.live-pill.is-live .live-dot {
  background: var(--success-500, #16a34a);
  animation: live-blink 1.6s ease-in-out infinite;
}
@keyframes live-blink {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(22, 163, 74, 0.5); }
  50% { opacity: 0.5; box-shadow: 0 0 0 4px rgba(22, 163, 74, 0); }
}

/* Quick actions */
.quick-actions {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 12px; margin-bottom: 20px;
}

.quick-action {
  display: flex; align-items: center; gap: 12px; padding: 14px 16px; text-align: left;
  cursor: pointer; background: var(--bg-primary); border: 1px solid var(--border-primary);
  transition: border-color 0.15s, transform 0.15s;
}
.quick-action:hover { border-color: var(--local-color); transform: translateY(-1px); }
.qa-icon {
  flex-shrink: 0; width: 38px; height: 38px; border-radius: var(--radius);
  display: inline-flex; align-items: center; justify-content: center; font-size: 20px;
}
.qa-text { display: flex; flex-direction: column; min-width: 0; }
.qa-title { font-size: 14px; font-weight: 600; color: var(--text-primary); }
.qa-sub { font-size: 12px; color: var(--text-muted); }

.quick-action.qa-primary {
  --local-color: var(--primary-400);
  --local-bg-hover: var(--primary-50);
}

.quick-action.qa-secondary {
  --local-color: var(--secondary-400);
  --local-bg-hover: var(--secondary-50);
}

.quick-action.qa-info {
  --local-color: var(--info-400);
  --local-bg-hover: var(--info-50);
}

.quick-action.qa-success {
  --local-color: var(--success-400);
  --local-bg-hover: var(--success-50);
}

.quick-action.qa-warning {
  --local-color: var(--warning-400);
  --local-bg-hover: var(--warning-50);
}

.quick-action.qa-danger {
  --local-color: var(--danger-400);
  --local-bg-hover: var(--danger-50);
}

[data-theme="dark"] .quick-action.qa-primary {
  --local-color: var(--primary-800);
  --local-bg-hover: var(--primary-50);
}

[data-theme="dark"] .quick-action.qa-secondary {
  --local-color: var(--secondary-600);
  --local-bg-hover: var(--secondary-50);
}

[data-theme="dark"] .quick-action.qa-info {
  --local-color: var(--info-800);
  --local-bg-hover: var(--info-50);
}

[data-theme="dark"] .quick-action.qa-success {
  --local-color: var(--success-800);
  --local-bg-hover: var(--success-50);
}

[data-theme="dark"] .quick-action.qa-warning {
  --local-color: var(--warning-800);
  --local-bg-hover: var(--warning-50);
}

[data-theme="dark"] .quick-action.qa-danger {
  --local-color: var(--danger-800);
  --local-bg-hover: var(--danger-50);
}

.stat-card-clickable { cursor: pointer; transition: border-color 0.15s, transform 0.15s; }
.stat-card-clickable:hover { border-color: var(--border-primary); transform: translateY(-1px); }
.stat-card-alert { border-color: var(--danger-500); }

.node-cell { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; color: var(--text-secondary); }
.node-cell .mdi { font-size: 15px; color: var(--text-muted); }

/* Live resources card */
.resources-card { margin-bottom: 20px; }
.resources-body {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 20px; padding: 16px 20px;
}
.resource { display: flex; flex-direction: column; gap: 8px; }
.resource-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.resource-label { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); }
.resource-label .mdi { font-size: 16px; }
.resource-value { font-size: 20px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
.resource-value small { font-size: 12px; font-weight: 400; color: var(--text-muted); }
.net-figures { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; }
.net-figure { display: inline-flex; align-items: center; gap: 6px; font-size: 16px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
.net-figure .mdi { font-size: 16px; color: var(--text-muted); }
.net-figure small { font-size: 11px; font-weight: 400; color: var(--text-muted); }

.invites-card { margin-bottom: 20px; border-color: var(--primary-500); }
[data-theme="dark"] .invites-card {border-color: var(--primary-900);}
.invites-card .card-header h2 { display: flex; align-items: center; gap: 8px; }
.invites { list-style: none; margin: 0; padding: 0; }
.invite {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 14px 20px;
}
.invite + .invite { border-top: 1px solid var(--border-secondary); }
.invite-info { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.invite-name { font-size: 14px; font-weight: 600; color: var(--text-primary); }
.invite-sub { font-size: 13px; color: var(--text-muted); }
.invite-sub strong { color: var(--text-secondary); text-transform: capitalize; }

.timeline { list-style: none; margin: 0; padding: 8px 0; }
.event { display: flex; gap: 12px; padding: 10px 20px; }
.event + .event { border-top: 1px solid var(--border-secondary); }
/* Briefly highlight an event that just streamed in. */
.event-fresh { animation: event-fresh-in 2.4s ease-out; }
@keyframes event-fresh-in {
  0% { background: var(--primary-50); }
  100% { background: transparent; }
}

.event-icon {
  flex-shrink: 0; width: 30px; height: 30px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center; font-size: 16px;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.event-icon.sev-warning { background: var(--warning-50); color: var(--warning-600); }
.event-icon.sev-error { background: var(--danger-50); color: var(--danger-600); }
.event-body { flex: 1; min-width: 0; }
.event-row { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.event-msg { font-size: 14px; color: var(--text-primary); }
.event-time { flex-shrink: 0; font-size: 12px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.event-type { font-size: 11px; color: var(--text-muted); font-family: 'JetBrains Mono', monospace; }

@media (max-width: 639px) {
  .quick-actions {
    display: grid;
    grid-template-columns: 1fr;
    gap: 4px;
    margin-bottom: 20px;
    padding-left: 1px;
    padding-right: 1px;
  }
}
</style>
