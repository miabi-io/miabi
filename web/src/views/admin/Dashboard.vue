<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import type { AdminEvent, PlatformMetrics } from '@/api/types'
import { sseUrl } from '@/api/client'
import { useNotificationStore } from '@/stores/notification'
import Sparkline from '@/components/Sparkline.vue'

const notify = useNotificationStore()
const router = useRouter()

const metrics = ref<PlatformMetrics | null>(null)
const events = ref<AdminEvent[]>([])
let es: EventSource | null = null

// Ring buffer of streamed samples, so each figure carries its direction.
const TREND_POINTS = 12
const trend = ref<{ containers: number[]; memory: number[]; goroutines: number[] }>({
  containers: [], memory: [], goroutines: [],
})

function pushTrend(m: PlatformMetrics) {
  const push = (arr: number[], v: number) => {
    arr.push(v)
    if (arr.length > TREND_POINTS) arr.shift()
  }
  push(trend.value.containers, m.running_containers)
  push(trend.value.memory, m.memory_alloc_bytes)
  push(trend.value.goroutines, m.goroutines)
}

function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = n
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  const rounded = value >= 100 || i === 0 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[i]}`
}

function fmtUptime(seconds: number): string {
  if (!seconds || seconds < 0) return '0m'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const parts: string[] = []
  if (d > 0) parts.push(`${d}d`)
  if (h > 0) parts.push(`${h}h`)
  parts.push(`${m}m`)
  return parts.join(' ')
}

function relTime(ts: string): string {
  const then = new Date(ts).getTime()
  if (Number.isNaN(then)) return ''
  const diff = Math.floor((Date.now() - then) / 1000)
  if (diff < 60) return `${Math.max(0, diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

// Turn an audit action like "admin.user.update" into "User update".
function prettyAction(action: string): string {
  const s = action.replace(/^admin\./, '').replace(/[._]/g, ' ').trim()
  return s.charAt(0).toUpperCase() + s.slice(1)
}

function eventIcon(action: string): string {
  if (action.includes('delete') || action.includes('revoke')) return 'mdi-delete-outline'
  if (action.includes('create') || action.includes('invite')) return 'mdi-plus-circle-outline'
  if (action.includes('login')) return 'mdi-login-variant'
  if (action.includes('update') || action.includes('settings')) return 'mdi-pencil-outline'
  if (action.includes('2fa') || action.includes('password')) return 'mdi-shield-key-outline'
  return 'mdi-circle-small'
}

function eventSeverity(action: string): string {
  const a = action.toLowerCase()
  if (a.includes('delete')) return 'sev-error'
  if (a.includes('fail') || a.includes('revoke') || a.includes('disable')) return 'sev-warning'
  return ''
}

// --- Capacity: a ratio against a limit is a meter, not a card with a % in it.
type Level = 'ok' | 'warn' | 'crit'

interface Meter {
  key: string
  label: string
  icon: string
  value: string
  detail: string
  pct: number
  level: Level
  hint?: string
  invert?: boolean   // the bar shows what is UP, so a full bar is good
  to?: string
}

function levelFor(pct: number, warn: number, crit: number): Level {
  if (pct >= crit) return 'crit'
  if (pct >= warn) return 'warn'
  return 'ok'
}

const poolPct = computed(() => {
  const p = metrics.value?.network_pool
  if (!p || !p.total) return 0
  return Math.round((p.used / p.total) * 100)
})

const storagePct = computed(() => {
  const m = metrics.value
  if (!m || !m.storage_declared_bytes) return 0
  return Math.round((m.storage_used_bytes / m.storage_declared_bytes) * 100)
})

const meters = computed<Meter[]>(() => {
  const m = metrics.value
  if (!m) return []
  const out: Meter[] = []

  // Stopping a container is usually deliberate, so this only escalates once a
  // large share is down, and never past "degraded".
  const stopped = Math.max(0, m.total_containers - m.running_containers)
  const stoppedPct = m.total_containers ? Math.round((stopped / m.total_containers) * 100) : 0
  out.push({
    key: 'containers', label: 'Containers running', icon: 'mdi-docker',
    value: `${m.running_containers}/${m.total_containers}`,
    detail: stopped ? `${stopped} stopped` : 'all running',
    pct: m.total_containers ? Math.round((m.running_containers / m.total_containers) * 100) : 0,
    level: stoppedPct >= 40 ? 'warn' : 'ok',
    invert: true,
    hint: stoppedPct >= 40 ? 'A large share is down — check for crash loops.' : undefined,
  })

  if (m.storage_declared_bytes > 0) {
    out.push({
      key: 'storage',
      label: 'Volume storage',
      icon: 'mdi-harddisk',
      value: `${storagePct.value}%`,
      detail: `${fmtBytes(m.storage_used_bytes)} of ${fmtBytes(m.storage_declared_bytes)} declared`,
      pct: storagePct.value,
      level: levelFor(storagePct.value, 75, 90),
      hint: storagePct.value >= 75 ? 'Raise volume quotas or reclaim space.' : undefined,
    })
  }

  if (m.network_pool && m.network_pool.total) {
    out.push({
      key: 'pool',
      label: 'Subnet pool',
      icon: 'mdi-ip-network-outline',
      value: `${poolPct.value}%`,
      detail: `${m.network_pool.used} of ${m.network_pool.total} subnets · ${m.network_pool.available} free`,
      pct: poolPct.value,
      level: levelFor(poolPct.value, 75, 90),
      hint: poolPct.value >= 75 ? 'Enlarge MIABI_NETWORK_POOL_CIDR before it runs out.' : undefined,
    })
  }
  return out
})

// --- Health: derived, never asserted.
interface Health {
  level: Level
  label: string
  icon: string
  reasons: string[]
}

const health = computed<Health>(() => {
  const m = metrics.value
  const reasons: string[] = []
  // Numeric rank: comparing the string narrows it to its initial literal.
  const rank: Record<Level, number> = { ok: 0, warn: 1, crit: 2 }
  let worst = 0
  const raise = (l: Level) => {
    worst = Math.max(worst, rank[l])
  }

  if (m) {
    if (m.connected_workers === 0) {
      raise('crit')
      reasons.push('No workers connected — deploys and jobs will queue')
    }
    for (const meter of meters.value) {
      if (meter.level === 'ok') continue
      raise(meter.level)
      reasons.push(meter.key === 'containers'
        ? `${meter.detail} of ${m.total_containers} containers`
        : `${meter.label} at ${meter.pct}%`)
    }
    const runners = m.shared_runners + m.workspace_runners
    const online = m.shared_runners_online + m.workspace_runners_online
    if (runners > 0 && online === 0) {
      raise('warn')
      reasons.push('No build runners online — builds cannot start')
    }
  }

  // Distinct icon per state, so severity does not rest on hue alone.
  if (worst === 2) return { level: 'crit', label: 'Needs attention', icon: 'mdi-alert-octagon', reasons }
  if (worst === 1) return { level: 'warn', label: 'Degraded', icon: 'mdi-alert', reasons }
  return { level: 'ok', label: 'Operational', icon: 'mdi-check-circle', reasons }
})

// --- Inventory: flat counts, so one compact row rather than six cards.
const inventory = computed(() => {
  const m = metrics.value
  if (!m) return []
  return [
    { label: 'Applications', value: m.total_applications, icon: 'mdi-cube-outline' },
    { label: 'Databases', value: m.total_databases, icon: 'mdi-database-outline' },
    { label: 'Stacks', value: m.total_stacks, icon: 'mdi-layers-outline' },
    { label: 'Volumes', value: m.total_volumes, icon: 'mdi-harddisk' },
    { label: 'Routes', value: m.total_routes, icon: 'mdi-sitemap-outline', to: '/admin/routes' },
    { label: 'Active users', value: m.active_users, icon: 'mdi-account-check-outline', to: '/admin/users' },
    { label: 'Sessions', value: m.active_sessions, icon: 'mdi-key-outline' },
  ]
})

// Fleet has the same shape as a meter, so it shares the list rather than a section.
const fleetMeters = computed<Meter[]>(() => {
  const m = metrics.value
  if (!m) return []
  const out: Meter[] = [{
    key: 'workers', label: 'Workers', icon: 'mdi-cog-sync-outline',
    value: `${m.connected_workers}`,
    detail: m.connected_workers > 0 ? 'processing jobs' : 'nothing processing jobs',
    pct: m.connected_workers > 0 ? 100 : 0,
    level: m.connected_workers === 0 ? 'crit' : 'ok',
    invert: true,
  }]
  const runners: Array<[string, string, string, number, number, string | undefined]> = [
    ['shared', 'Shared runners', 'mdi-cog-transfer-outline', m.shared_runners_online, m.shared_runners, '/admin/runners'],
    ['ws', 'Workspace runners', 'mdi-cog-outline', m.workspace_runners_online, m.workspace_runners, undefined],
  ]
  for (const [key, label, icon, online, total, to] of runners) {
    if (!total) continue
    out.push({
      key, label, icon, to,
      value: `${online}/${total}`,
      detail: online ? 'online' : 'none online — builds cannot start',
      pct: total ? Math.round((online / total) * 100) : 0,
      level: online === 0 ? 'warn' : 'ok',
      invert: true,
    })
  }
  return out
})

const systemRows = computed(() => [...meters.value, ...fleetMeters.value])

const quickActions = computed(() => {
  const m = metrics.value
  return [
    { label: 'Nodes', icon: 'mdi-server-network', to: '/admin/nodes', count: m?.total_nodes },
    { label: 'Routes', icon: 'mdi-sitemap-outline', to: '/admin/routes', count: m?.total_routes },
    { label: 'Users', icon: 'mdi-account-group-outline', to: '/admin/users', count: m?.total_users },
    { label: 'Workspaces', icon: 'mdi-briefcase-outline', to: '/admin/workspaces', count: m?.total_workspaces },
    { label: 'Runners', icon: 'mdi-cog-transfer-outline', to: '/admin/runners', count: m ? m.shared_runners + m.workspace_runners : undefined },
    { label: 'Events', icon: 'mdi-timeline-text-outline', to: '/admin/events' },
    { label: 'Settings', icon: 'mdi-cog-outline', to: '/admin/settings' },
  ]
})

async function loadInitial() {
  try {
    const first = (await adminApi.metrics()).data.data
    metrics.value = first
    pushTrend(first)
  } catch (err) {
    notify.apiError(err, 'Failed to load metrics')
  }
}

async function loadEvents() {
  try {
    events.value = (await adminApi.listEvents('', '', 0, 8)).data.data ?? []
  } catch {
    // best-effort
  }
}

function openStream() {
  es = new EventSource(sseUrl('/admin/metrics/stream'))
  es.onmessage = (e) => {
    try {
      const next = JSON.parse(e.data) as PlatformMetrics
      metrics.value = next
      pushTrend(next)
    } catch {
      // ignore malformed payloads
    }
  }
}

onMounted(() => {
  loadInitial()
  loadEvents()
  openStream()
})

onBeforeUnmount(() => {
  es?.close()
  es = null
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Platform Admin Dashboard</h1>
        <p class="subtitle">Platform health and activity at a glance</p>
      </div>
      <div class="live-indicator">
        <span class="live-dot"></span>
        <span class="text-muted">Live</span>
      </div>
    </div>

    <div class="quick-actions">
      <button v-for="a in quickActions" :key="a.to" class="qa" @click="router.push(a.to)">
        <span class="mdi" :class="a.icon"></span>{{ a.label }}
        <span v-if="a.count !== undefined" class="qa-count">{{ a.count }}</span>
      </button>
    </div>

    <div v-if="!metrics" class="card">
      <div class="card-body" style="display: flex; justify-content: center; padding: 48px 0">
        <span class="spinner"></span>
      </div>
    </div>

    <template v-else>
      <!-- Health, derived from live signals. -->
      <div class="hero card" :class="`hero-${health.level}`">
        <div class="hero-status">
          <span class="hero-badge"><span class="mdi" :class="health.icon"></span></span>
          <div class="hero-text">
            <div class="hero-title">{{ health.label }}</div>
            <div v-if="health.reasons.length" class="hero-sub">{{ health.reasons.join(' · ') }}</div>
            <div v-else class="hero-sub">All checks passing across the platform</div>
          </div>
        </div>
        <div class="hero-figure">
          <span class="hero-number">{{ metrics.running_containers }}</span>
          <span class="hero-number-label">containers running</span>
          <Sparkline
            v-if="trend.containers.length > 1"
            :values="trend.containers" :width="120" :height="26"
            stroke="var(--text-muted)"
          />
        </div>
        <div class="hero-meta">
          <div class="hero-stat"><span class="hero-stat-label">Uptime</span><span class="hero-stat-value">{{ fmtUptime(metrics.uptime_seconds) }}</span></div>
          <div class="hero-stat"><span class="hero-stat-label">Version</span><span class="hero-stat-value">{{ metrics.version || 'dev' }}</span></div>
        </div>
      </div>

      <h2 class="section-title">System</h2>
      <div class="card">
        <div class="sys">
          <div
            v-for="r in systemRows" :key="r.key"
            class="sys-row" :class="[{ 'is-link': r.to }, r.level !== 'ok' ? `lvl-${r.level}` : '']"
            @click="r.to && router.push(r.to)"
          >
            <span class="sys-icon"><span class="mdi" :class="r.icon"></span></span>
            <div class="sys-main">
              <div class="sys-top">
                <span class="sys-label">{{ r.label }}</span>
                <span class="sys-value" :class="r.level !== 'ok' ? `lvl-${r.level}` : ''">{{ r.value }}</span>
              </div>
              <div class="sys-track">
                <div
                  class="sys-fill" :class="r.level !== 'ok' ? `lvl-${r.level}` : (r.invert ? 'is-good' : '')"
                  :style="{ width: Math.min(100, r.pct) + '%' }"
                ></div>
              </div>
              <div class="sys-detail">
                {{ r.detail }}<template v-if="r.hint"> · <span :class="`lvl-${r.level}`">{{ r.hint }}</span></template>
              </div>
            </div>
          </div>
        </div>
      </div>

      <h2 class="section-title">Control plane</h2>
      <div class="runtime-row">
        <div class="card runtime-card">
          <div class="card-body">
            <span class="meter-label"><span class="mdi mdi-memory"></span> Memory in use</span>
            <div class="runtime-value">{{ fmtBytes(metrics.memory_alloc_bytes) }}</div>
            <Sparkline v-if="trend.memory.length > 1" :values="trend.memory" :width="180" :height="32" stroke="var(--primary-500)" />
            <div class="meter-detail">Go heap allocation, this process</div>
          </div>
        </div>
        <div class="card runtime-card">
          <div class="card-body">
            <span class="meter-label"><span class="mdi mdi-sync"></span> Goroutines</span>
            <div class="runtime-value">{{ metrics.goroutines }}</div>
            <Sparkline v-if="trend.goroutines.length > 1" :values="trend.goroutines" :width="180" :height="32" stroke="var(--primary-500)" />
            <div class="meter-detail">A steady climb suggests a leak</div>
          </div>
        </div>
      </div>

      <h2 class="section-title">Inventory</h2>
      <div class="card">
        <div class="inventory">
          <button
            v-for="i in inventory" :key="i.label"
            class="inv-item" :class="{ 'is-link': i.to }"
            :disabled="!i.to" @click="i.to && router.push(i.to)"
          >
            <span class="inv-value">{{ i.value }}</span>
            <span class="inv-label"><span class="mdi" :class="i.icon"></span> {{ i.label }}</span>
          </button>
        </div>
      </div>

      <!-- Connected workers -->
      <template v-if="metrics.workers && metrics.workers.length">
        <h2 class="section-title">Workers</h2>
        <div class="card worker-card">
          <div class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Host</th>
                  <th>Type</th>
                  <th>PID</th>
                  <th>Queues</th>
                  <th>Active</th>
                  <th>Status</th>
                  <th>Started</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="w in metrics.workers" :key="`${w.host}:${w.pid}`">
                  <td class="mono">{{ w.host }}</td>
                  <td>
                    <span class="badge" :class="w.type === 'embedded' ? 'badge-info' : 'badge-success'">{{ w.type }}</span>
                  </td>
                  <td class="mono">{{ w.pid }}</td>
                  <td>
                    <span v-for="(c, q) in w.queues" :key="q" class="badge badge-neutral queue-badge">{{ q }}: {{ c }}</span>
                  </td>
                  <td class="mono">{{ w.active_tasks }} / {{ w.concurrency }}</td>
                  <td>
                    <span class="badge" :class="w.status === 'active' ? 'badge-success' : 'badge-warning'">{{ w.status }}</span>
                  </td>
                  <td class="text-muted">{{ relTime(w.started) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>

      <!-- Recent platform activity -->
      <div class="card" style="margin-top: 20px">
        <div class="card-header">
          <h2>Recent activity</h2>
          <button class="btn btn-ghost btn-sm" @click="router.push('/admin/events')">View all</button>
        </div>
        <div v-if="events.length === 0" class="empty-state" style="padding: 28px">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>No recorded activity yet.</p>
        </div>
        <ul v-else class="timeline">
          <li v-for="e in events" :key="e.id" class="event">
            <span class="event-icon" :class="eventSeverity(e.action)"><span class="mdi" :class="eventIcon(e.action)"></span></span>
            <div class="event-body">
              <div class="event-row">
                <span class="event-msg">{{ prettyAction(e.action) }}</span>
                <span class="event-time">{{ relTime(e.created_at) }}</span>
              </div>
              <span class="event-type">{{ e.target_type }}{{ e.target_id ? ' · ' + e.target_id : '' }}{{ e.ip_address ? ' · ' + e.ip_address : '' }}</span>
            </div>
          </li>
        </ul>
      </div>

      <div class="mt-4 text-muted version-line">
        Miabi {{ metrics.version }} · {{ metrics.commit }}
      </div>
    </template>
  </div>
</template>

<style scoped>
.page-header { display: flex; align-items: center; justify-content: space-between; }
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }

.live-indicator { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--success-600);
  animation: live-pulse 1.6s ease-out infinite;
}
@keyframes live-pulse { 0%, 100% { opacity: 1 } 50% { opacity: 0.35 } }
@media (prefers-reduced-motion: reduce) { .live-dot { animation: none } }

/* --- Health hero --- */
.hero {
  display: flex; align-items: center; gap: 28px; flex-wrap: wrap;
  padding: 14px 20px; margin-bottom: 18px;
  border-left: 3px solid var(--success-600);
}
.hero-ok { border-left-color: var(--success-600); }
.hero-warn { border-left-color: var(--warning-600); }
.hero-crit { border-left-color: var(--danger-600); }
.hero-status { display: flex; align-items: center; gap: 14px; flex: 1; min-width: 260px; }
.hero-badge { font-size: 26px; line-height: 1; color: var(--success-600); }
.hero-warn .hero-badge { color: var(--warning-600); }
.hero-crit .hero-badge { color: var(--danger-600); }
.hero-title { font-size: 17px; font-weight: 600; color: var(--text-primary); }
.hero-sub { font-size: 13px; color: var(--text-muted); margin-top: 2px; }

.hero-figure { display: flex; flex-direction: column; gap: 2px; min-width: 150px; }
.hero-number { font-size: 36px; font-weight: 600; line-height: 1; color: var(--text-primary); letter-spacing: -0.02em; }
.hero-number-label { font-size: 12px; color: var(--text-muted); }
.hero-meta { display: flex; gap: 28px; }
.hero-stat { display: flex; flex-direction: column; gap: 2px; }
.hero-stat-label { font-size: 11px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
.hero-stat-value { font-size: 14px; font-weight: 500; color: var(--text-primary); }

.section-title { font-size: 13px; font-weight: 600; color: var(--text-secondary); margin: 20px 0 8px; }

/* --- Quick actions --- */
.quick-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 20px; }
.qa {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 7px 12px; border-radius: var(--radius-lg);
  border: 1px solid var(--border-primary); background: var(--bg-primary);
  font-size: 13px; color: var(--text-secondary); cursor: pointer;
}
.qa:hover { border-color: var(--primary-400); color: var(--text-primary); background: var(--bg-hover); }
.qa .mdi { font-size: 15px; }
.qa-count {
  padding: 1px 6px; border-radius: 999px; background: var(--bg-tertiary);
  font-size: 12px; font-weight: 600; color: var(--text-primary);
}

/* --- System (capacity + fleet, one dense list) --- */
.sys { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); }
.sys-row {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 12px 16px; border-right: 1px solid var(--border-secondary);
  border-top: 1px solid var(--border-secondary);
}
.sys-row:nth-child(-n+2) { border-top: 0; }
.sys-row.is-link { cursor: pointer; }
.sys-row.is-link:hover { background: var(--bg-hover); }
.sys-icon { font-size: 16px; color: var(--text-muted); line-height: 1.4; }
.sys-row.lvl-warn .sys-icon { color: var(--warning-600); }
.sys-row.lvl-crit .sys-icon { color: var(--danger-600); }
.sys-main { flex: 1; min-width: 0; }
.sys-top { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.sys-label { font-size: 13px; color: var(--text-secondary); }
.sys-value { font-size: 15px; font-weight: 600; color: var(--text-primary); }
.sys-track { height: 4px; border-radius: 2px; margin: 6px 0 5px; overflow: hidden; background: var(--primary-100); }
.sys-fill { height: 100%; border-radius: 2px; background: var(--primary-500); transition: width 0.4s ease; }
.sys-fill.is-good { background: var(--success-600); }
.sys-fill.lvl-warn { background: var(--warning-600); }
.sys-fill.lvl-crit { background: var(--danger-600); }
.sys-detail { font-size: 12px; color: var(--text-muted); }
.lvl-warn { color: var(--warning-600); }
.lvl-crit { color: var(--danger-600); }

/* --- Control plane --- */
.runtime-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 14px; }
.runtime-value { font-size: 22px; font-weight: 600; color: var(--text-primary); margin: 4px 0 6px; }

/* --- Inventory --- */
.inventory { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); }
.inv-item {
  display: flex; flex-direction: column; gap: 3px; align-items: flex-start;
  padding: 12px 16px; background: none; border: 0;
  border-right: 1px solid var(--border-secondary); text-align: left; color: inherit;
}
.inv-item:last-child { border-right: 0; }
.inv-item.is-link { cursor: pointer; }
.inv-item.is-link:hover { background: var(--bg-hover); }
.inv-value { font-size: 20px; font-weight: 600; color: var(--text-primary); }
.inv-label { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text-muted); }

/* --- Activity --- */
.timeline { list-style: none; margin: 0; padding: 8px 0; }
.event { display: flex; gap: 12px; padding: 10px 20px; }
.event + .event { border-top: 1px solid var(--border-secondary); }
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

.version-line { font-size: 12px; }

/* Workers table */
.worker-card { margin-bottom: 24px; }
.worker-card .mono { font-family: 'JetBrains Mono', monospace; font-size: 12.5px; }
.queue-badge { margin-right: 6px; }
.queue-badge:last-child { margin-right: 0; }
</style>
