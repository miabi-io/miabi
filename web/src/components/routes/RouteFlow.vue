<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { routeApi } from '@/api/routes'
import type { RouteFlow, RouteStats, RouteTrace, RouteTraceStep } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { relativeTime } from '@/utils/time'
import { relativeBox, wirePath } from '@/utils/flowWires'
import { parseHeaders, tracePath } from '@/utils/routeTrace'
import { canaryWorse, earlyAnswers, errorShare, formatMs, formatPercent, formatRate } from '@/utils/routeStats'

// revision changes whenever the page's own poll sees the route or its app change (a canary step, a promote).
const props = defineProps<{ wsId: number; routeId: number; revision?: string }>()
const emit = defineEmits<{ openSettings: [] }>()
const { t, te } = useI18n()
const notify = useNotificationStore()

const flow = ref<RouteFlow | null>(null)
const loading = ref(false)

type NodeKey = 'visitors' | 'gateway' | 'maint' | 'chain' | 'split' | 'stable' | 'canary' | 'app'
type Selection = { kind: 'visitors' | 'gateway' | 'maint' | 'split' | 'stable' | 'canary' | 'app' } | { kind: 'mw'; index: number }
const selected = ref<Selection | null>(null)

// quiet keeps the trace and skips error toasts: a background refresh must not interrupt what the user is doing.
async function load(quiet = false) {
  if (!quiet) {
    loading.value = true
    trace.value = null
  }
  let changed = true
  try {
    const next = (await routeApi.flow(props.wsId, props.routeId)).data.data
    changed = JSON.stringify(next) !== JSON.stringify(flow.value)
    if (changed) flow.value = next
    if (!traceForm.value.path) traceForm.value.path = flow.value?.route.path || '/'
    if (!traceForm.value.host) traceForm.value.host = flow.value?.hosts[0]?.host || ''
  } catch (e) {
    if (!quiet) notify.apiError(e)
  } finally {
    loading.value = false
  }
  if (quiet && !changed) return
  await nextTick()
  draw()
  if (quiet && trace.value) runTrace()
}
watch(() => props.routeId, () => load())
watch(() => props.revision, (now, before) => {
  if (before !== undefined && now !== before) load(true)
})

const stable = computed(() => flow.value?.backends.find((b) => b.role === 'stable'))
const canary = computed(() => flow.value?.backends.find((b) => b.role === 'canary'))
const serving = computed(() => !!flow.value?.route.serving)
// A parked or suspended route is answered by the gateway itself; nothing downstream sees traffic.
const parked = computed(() => serving.value && (!!flow.value?.route.maintenance.enabled || !!flow.value?.route.suspended))
const downstreamOff = computed(() => !serving.value || parked.value)
const noReplica = computed(() => {
  const h = stable.value?.health
  return !!h && h.desired > 0 && h.running === 0
})

const routeLink = (id: number) => ({ path: `/routes/${id}`, query: { tab: 'flow' } })
const appLink = computed(() => (flow.value ? `/apps/${flow.value.app.id}` : '/apps'))

interface Banner { kind: 'danger' | 'warning' | 'info'; text: string; to?: string | { path: string; query?: Record<string, string> }; settings?: boolean; action: string }
function servingReason(reason = ''): Banner {
  if (reason === 'disabled') return { kind: 'danger', text: t('routes.flow.state.disabled'), settings: true, action: t('routes.flow.openSettings') }
  if (reason === 'app not deployed') return { kind: 'danger', text: t('routes.flow.state.notDeployed'), to: appLink.value, action: t('routes.flow.openApp') }
  if (reason === 'route has no hosts') return { kind: 'danger', text: t('routes.flow.state.noHosts'), settings: true, action: t('routes.flow.openSettings') }
  const m = /^domain (not verified|banned|not registered): (.+)$/.exec(reason)
  if (m) {
    const key = m[1] === 'banned' ? 'banned' : m[1] === 'not verified' ? 'unverified' : 'unregistered'
    return { kind: 'danger', text: t(`routes.flow.state.${key}`, { host: m[2] }), to: '/domains', action: t('routes.flow.openDomains') }
  }
  return { kind: 'danger', text: t('routes.flow.state.notServed', { reason }), action: '' }
}
const banners = computed<Banner[]>(() => {
  const f = flow.value
  if (!f) return []
  const out: Banner[] = []
  if (!f.route.serving) out.push(servingReason(f.route.serving_reason))
  else if (f.route.suspended) out.push({ kind: 'warning', text: t('routes.flow.state.suspended'), action: '' })
  else if (f.route.maintenance.enabled) {
    out.push({ kind: 'warning', text: t('routes.flow.state.maintenance', { code: f.route.maintenance.status_code }), settings: true, action: t('routes.flow.openSettings') })
  }
  if (f.route.serving && !parked.value && noReplica.value) {
    out.push({ kind: 'danger', text: t('routes.flow.state.noReplica'), to: appLink.value, action: t('routes.flow.openApp') })
  }
  const worse = liveStats.value ? canaryWorse(liveStats.value) : null
  if (canary.value?.release && worse) {
    out.push({
      kind: 'danger',
      text: t('routes.flow.state.canaryWorse', { version: canary.value.release.version, canary: formatPercent(worse.canaryRate), stable: formatPercent(worse.stableRate) }),
      to: { path: appLink.value, query: { tab: 'deployments' } },
      action: t('routes.flow.canaryControls'),
    })
  } else if (canary.value?.release) {
    out.push({
      kind: 'info',
      text: canaryRules.value.length
        ? t('routes.flow.state.canaryRules', { version: canary.value.release.version, weight: canary.value.weight })
        : t('routes.flow.state.canary', { version: canary.value.release.version, weight: canary.value.weight }),
      to: { path: appLink.value, query: { tab: 'deployments' } },
      action: t('routes.flow.canaryControls'),
    })
  }
  return out
})

const canaryRules = computed(() => canary.value?.match ?? [])
function ruleText(r: { source: string; name: string; operator: string; value: string }): string {
  const op = t(`routes.flow.op.${r.operator}`)
  return r.source === 'ip' ? `IP ${op} ${r.value}` : `${r.source} ${r.name} ${op} ${r.value}`
}

const gatewayTitle = computed(() => {
  const g = flow.value?.gateway
  if (!g) return ''
  if (g.kind === 'cluster') return t('routes.flow.gateway.cluster', { cluster: g.cluster || g.server_name || '' })
  if (g.kind === 'edge') return t('routes.flow.gateway.edge', { server: g.server_name || '' })
  return t('routes.flow.gateway.platform')
})
const tlsText = computed(() => {
  const tls = flow.value?.tls
  if (!tls) return ''
  if (tls.mode === 'none') return t('routes.flow.tls.none')
  if (tls.mode === 'custom') return t('routes.flow.tls.custom', { name: tls.certificate_name || `#${tls.certificate_id}` })
  if (tls.mode === 'external') return t('routes.flow.tls.external')
  return tls.provider ? t('routes.flow.tls.acmeProvider', { provider: tls.provider }) : t('routes.flow.tls.acme')
})
const narrower = computed(() => flow.value?.siblings.filter((s) => s.relation === 'narrower') ?? [])

const canvas = ref<HTMLElement | null>(null)
const nodes = new Map<NodeKey, HTMLElement>()
function setNode(key: NodeKey) {
  return (el: unknown) => {
    if (el instanceof HTMLElement) nodes.set(key, el)
    else nodes.delete(key)
  }
}

const traceForm = ref({ method: 'GET', scheme: 'https' as 'http' | 'https', host: '', path: '', headers: '', ip: '' })
const trace = ref<RouteTrace | null>(null)
const tracing = ref(false)
const traceView = computed(() => {
  if (!trace.value) return null
  const v = tracePath(trace.value)
  // The empty-chain placeholder has no step of its own; a request that got past it went through it.
  if (!flow.value?.chain.length && ['split', 'stable', 'canary', 'app'].some((k) => v.visited.has(k))) v.visited.add('chain')
  return v
})

async function runTrace() {
  if (!traceForm.value.path.trim()) return
  tracing.value = true
  try {
    const f = traceForm.value
    trace.value = (await routeApi.trace(props.wsId, props.routeId, {
      method: f.method, scheme: f.scheme, host: f.host || undefined, path: f.path.trim(),
      headers: parseHeaders(f.headers), ip: f.ip.trim() || undefined,
    })).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    tracing.value = false
  }
}
function clearTrace() {
  trace.value = null
}
const canaryHeaderRule = computed(() => canaryRules.value.find((r) => r.source === 'header'))
function addCanaryHeader() {
  const r = canaryHeaderRule.value
  if (!r) return
  const line = `${r.name}: ${r.operator === 'equals' ? r.value : ''}`
  const lines = traceForm.value.headers.split('\n').filter((l) => l.trim() && !l.toLowerCase().startsWith(r.name.toLowerCase() + ':'))
  traceForm.value.headers = [...lines, line].join('\n')
}
function stepText(s: RouteTraceStep): string {
  const key = `routes.flow.trace.code.${s.code}`
  const params: Record<string, string> = { ...(s.params || {}) }
  if (params.body) params.body = params.body === 'page' ? t('routes.flow.maintPage') : t('routes.flow.maintMessage')
  if (params.role) params.role = t(`routes.flow.role.${params.role}`)
  return te(key) ? t(key, params) : s.detail
}
const resultText = computed(() => {
  const r = trace.value?.result
  if (!r) return ''
  const key = `routes.flow.trace.result.${r.kind}`
  const params = {
    status: r.status ?? '', location: r.location ?? '', route: r.route_name ?? '', path: r.upstream_path ?? '',
    backend: r.backend ? t(`routes.flow.trace.backend.${r.backend}`, { chance: r.canary_chance ?? 0 }) : '',
  }
  return te(key) ? t(key, params) : r.detail
})
const resultTone = computed(() => {
  const k = trace.value?.result.kind
  if (k === 'app') return 'is-ok'
  if (k === 'redirect' || k === 'other_route' || k === 'no_route') return 'is-info'
  return 'is-stop'
})
// Classes for a canvas box while a trace is shown: on the path, off it, or where the request stopped.
function tclass(key: string) {
  const v = traceView.value
  if (!v) return {}
  return { 'on-path': v.visited.has(key), 'off-path': !v.visited.has(key), 'stop-here': v.stop === key }
}
watch(trace, () => nextTick(draw))

type WireKind = 'ok' | 'canary' | 'warn' | 'bad' | 'off'
interface DrawnWire { d: string; kind: WireKind; label?: string; x: number; y: number; onPath?: boolean }
const wires = ref<DrawnWire[]>([])

const plannedWires = computed<[NodeKey, NodeKey, WireKind, string?][]>(() => {
  const f = flow.value
  if (!f) return []
  const out: [NodeKey, NodeKey, WireKind, string?][] = []
  // The empty-chain placeholder is a stop too, so the wire never runs across its text.
  const hasChain = f.chain.length > 0 || !parked.value
  const target: NodeKey = !f.app.deployed ? 'app' : canary.value ? 'split' : 'stable'
  const rate = liveStats.value ? t('routes.flow.live.perMinute', { n: formatRate(liveStats.value.per_minute) }) : undefined
  out.push(['visitors', 'gateway', serving.value ? 'ok' : 'bad', serving.value ? rate : t('routes.flow.notServed')])
  if (parked.value) out.push(['gateway', 'maint', 'warn', String(f.route.maintenance.status_code || 503)])
  const live: WireKind = downstreamOff.value ? 'off' : 'ok'
  if (hasChain) {
    out.push(['gateway', 'chain', live])
    out.push(['chain', target, live])
  } else if (!parked.value) {
    out.push(['gateway', target, live])
  }
  if (canary.value && stable.value) {
    // Measured shares once the gateway names backends; the configured weights until then.
    const v = liveStats.value?.variants
    const pct = (share?: number) => `${Math.round((share ?? 0) * 100)}%`
    out.push(['split', 'stable', live, v ? pct(v.stable?.share) : `${stable.value.weight}%`])
    out.push(['split', 'canary', downstreamOff.value ? 'off' : 'canary', v ? pct(v.canary?.share) : canary.value.weight > 0 ? `${canary.value.weight}%` : t('routes.flow.rulesOnly')])
  }
  return out
})

function draw() {
  const c = canvas.value
  if (!c) return
  const vertical = getComputedStyle(c).gridTemplateColumns.split(' ').length === 1
  const out: DrawnWire[] = []
  for (const [from, to, kind, label] of plannedWires.value) {
    const a = nodes.get(from)
    const b = nodes.get(to)
    if (!a || !b) continue
    const w = wirePath(relativeBox(a, c), relativeBox(b, c), vertical)
    const v = traceView.value
    out.push({ d: w.d, kind, label, x: w.mid.x, y: w.mid.y, onPath: v ? v.visited.has(from) && v.visited.has(to) : undefined })
  }
  wires.value = out
}

const LIVE_KEY = 'miabi.routeFlow.live'
const LIVE_POLL_MS = 30_000
const live = ref(readLivePref())
const stats = ref<RouteStats | null>(null)
let pollTimer: ReturnType<typeof setInterval> | undefined
function readLivePref(): boolean {
  try {
    return localStorage.getItem(LIVE_KEY) !== 'off'
  } catch {
    return true
  }
}
const statsAvailable = computed(() => !!stats.value?.enabled)
// Null while off or unavailable, so every live number in the template hides with one check.
const liveStats = computed(() => (live.value && statsAvailable.value ? stats.value : null))
async function loadStats() {
  if (document.hidden) return
  // Replica health is not in the page's poll, so the live tick refreshes the drawing too.
  if (flow.value) load(true)
  try {
    stats.value = (await routeApi.flowStats(props.wsId, props.routeId)).data.data
  } catch {
    stats.value = null
  }
}
function startPolling() {
  stopPolling()
  if (!live.value) return
  loadStats()
  pollTimer = setInterval(loadStats, LIVE_POLL_MS)
}
function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = undefined
}
function onVisibility() {
  if (!document.hidden && live.value) loadStats()
}
watch(live, (on) => {
  try { localStorage.setItem(LIVE_KEY, on ? 'on' : 'off') } catch { /* private window: not remembered */ }
  if (on) startPolling()
  else stopPolling()
})
watch(liveStats, () => nextTick(draw))
watch(() => props.routeId, () => { stats.value = null; startPolling() })
const earlyCounts = computed(() => {
  if (!liveStats.value || !flow.value) return []
  return earlyAnswers(liveStats.value, flow.value.chain.flatMap((m) => m.early_status))
})
const maintCount = computed(() => {
  const code = String(flow.value?.route.maintenance.status_code || 503)
  return liveStats.value?.codes[code] ?? 0
})
const sparkMax = computed(() => Math.max(1, ...(liveStats.value?.series.map((p) => p.requests) ?? [0])))

let observer: ResizeObserver | null = null
onMounted(() => {
  load()
  // First load even with live off, so the toggle only shows when analytics is on.
  if (live.value) startPolling()
  else loadStats()
  document.addEventListener('visibilitychange', onVisibility)
  observer = new ResizeObserver(() => draw())
  watch(canvas, (el) => {
    observer?.disconnect()
    if (el) observer?.observe(el)
  }, { immediate: true })
})
onBeforeUnmount(() => {
  observer?.disconnect()
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibility)
})

function select(s: Selection) {
  const same = selected.value && selected.value.kind === s.kind && (s.kind !== 'mw' || (selected.value.kind === 'mw' && selected.value.index === s.index))
  selected.value = same ? null : s
}
function isSelected(kind: Selection['kind'], index?: number) {
  const s = selected.value
  if (!s || s.kind !== kind) return false
  return kind !== 'mw' || (s.kind === 'mw' && s.index === index)
}
function onKey(e: KeyboardEvent, s: Selection) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    select(s)
  }
}

function shortDigest(d?: string) {
  return d ? d.replace(/^sha256:/, '').slice(0, 12) : ''
}
function healthText(h?: { status: string; running: number; desired: number }) {
  if (!h) return ''
  return t('routes.flow.replicas', { running: h.running, desired: h.desired })
}

interface DetailRow { label: string; value: string; to?: string | { path: string; query?: Record<string, string> } }
const details = computed<{ title: string; rows: DetailRow[]; link?: { label: string; to?: string | { path: string; query?: Record<string, string> }; settings?: boolean } } | null>(() => {
  const f = flow.value
  const s = selected.value
  if (!f || !s) return null
  switch (s.kind) {
    case 'visitors':
      return {
        title: t('routes.flow.visitors'),
        rows: f.hosts.map((h) => ({
          label: h.host,
          value: h.external ? t('routes.flow.host.external')
            : !h.registered ? t('routes.flow.host.unregistered')
              : h.banned ? t('routes.flow.host.banned', { domain: h.domain })
                : h.verified ? t('routes.flow.host.verified', { domain: h.domain }) : t('routes.flow.host.unverified', { domain: h.domain }),
          to: h.domain_id ? `/domains/${h.domain_id}` : undefined,
        })),
        link: { label: t('routes.flow.openDomains'), to: '/domains' },
      }
    case 'gateway': {
      const rows: DetailRow[] = [
        { label: t('routes.flow.d.gateway'), value: gatewayTitle.value + (f.gateway.server_name && f.gateway.kind !== 'edge' ? ` · ${f.gateway.server_name}` : '') },
        { label: 'TLS', value: tlsText.value + (f.tls.not_after ? ` · ${t('routes.flow.expires', { date: new Date(f.tls.not_after).toLocaleDateString() })}` : ''), to: f.tls.certificate_id ? `/certificates/${f.tls.certificate_id}` : undefined },
        { label: t('routes.path'), value: f.route.rewrite ? `${f.route.path} → ${f.route.rewrite}` : f.route.path },
        { label: t('routes.methods'), value: f.route.methods.length ? f.route.methods.join(', ') : t('routes.flow.allMethods') },
        { label: t('routes.flow.d.exploit'), value: f.route.exploit_protection ? t('routes.flow.on') : t('routes.flow.off') },
        { label: t('routes.maintenanceTitle'), value: f.route.maintenance.enabled ? t('routes.flow.on') : t('routes.flow.off') },
      ]
      for (const sib of f.siblings) {
        rows.push({
          label: t('routes.flow.d.sameHost'),
          value: `${sib.path} → ${sib.display_name || sib.name}${sib.app_name ? ` (${sib.app_name})` : ''} · ${t(`routes.flow.relation.${sib.relation}`)}`,
          to: routeLink(sib.route_id),
        })
      }
      return { title: t('routes.flow.gatewayTitle'), rows, link: { label: t('routes.flow.openSettings'), settings: true } }
    }
    case 'maint':
      return {
        title: t('routes.flow.maintTitle'),
        rows: [
          { label: t('routes.statusCode'), value: String(f.route.maintenance.status_code || 503) },
          { label: t('routes.flow.d.browsers'), value: f.route.maintenance.has_html ? t('routes.flow.maintPage') : t('routes.flow.maintMessage') },
          { label: t('routes.flow.d.apiClients'), value: t('routes.flow.maintMessage') },
        ],
        link: { label: t('routes.flow.openSettings'), settings: true },
      }
    case 'mw': {
      const m = f.chain[s.index]
      if (!m) return null
      const rows: DetailRow[] = [
        { label: t('routes.flow.d.order'), value: `${s.index + 1} / ${f.chain.length}` },
      ]
      if (m.missing) rows.push({ label: t('routes.flow.d.state'), value: t('routes.flow.mwMissing') })
      else {
        rows.push({ label: t('routes.flow.d.type'), value: m.display_name || m.type || '' })
        rows.push({ label: t('routes.flow.d.answersEarly'), value: m.early_status.length ? m.early_status.join(', ') : t('routes.flow.never') })
        rows.push({ label: t('routes.flow.d.paths'), value: m.paths.length ? m.paths.join(', ') : t('routes.flow.allPaths') })
      }
      return { title: m.name, rows, link: { label: t('routes.flow.openMiddleware'), to: m.id ? `/middlewares/${m.id}` : '/middlewares' } }
    }
    case 'split': {
      const c = canary.value
      if (!c) return null
      const rows: DetailRow[] = [{ label: t('routes.flow.d.release'), value: c.release ? `v${c.release.version} · ${relativeTime(c.release.created_at)}` : '' }]
      canaryRules.value.forEach((r, i) => rows.push({ label: t('routes.flow.d.rule', { n: i + 1 }), value: ruleText(r) + (c.exclusive ? ` · ${t('routes.flow.exclusive')}` : '') }))
      rows.push({ label: t('routes.flow.d.weight'), value: t('routes.flow.weightSplit', { stable: stable.value?.weight ?? 0, canary: c.weight }) })
      return { title: t('routes.flow.splitTitle'), rows, link: { label: t('routes.flow.canaryControls'), to: { path: appLink.value, query: { tab: 'deployments' } } } }
    }
    case 'stable':
    case 'canary': {
      const b = s.kind === 'stable' ? stable.value : canary.value
      if (!b) return null
      const rows: DetailRow[] = []
      if (b.release) {
        rows.push({ label: t('routes.flow.d.release'), value: `v${b.release.version} · ${relativeTime(b.release.created_at)}` })
        rows.push({ label: t('routes.flow.d.image'), value: b.release.image + (b.release.digest ? ` @${shortDigest(b.release.digest)}` : '') })
      }
      if (b.health) rows.push({ label: t('routes.flow.d.health'), value: `${b.health.status} · ${healthText(b.health)}` })
      rows.push({ label: t('routes.flow.d.target'), value: b.endpoint })
      return { title: `${f.app.name} · ${t(`routes.flow.role.${b.role}`)}`, rows, link: { label: t('routes.flow.openApp'), to: appLink.value } }
    }
    case 'app':
      return { title: f.app.name, rows: [{ label: t('routes.flow.d.state'), value: t('routes.flow.notDeployed') }], link: { label: t('routes.flow.openApp'), to: appLink.value } }
  }
  return null
})
</script>

<template>
  <div class="flow">
    <div v-if="loading && !flow" class="card"><div class="card-body"><span class="spinner"></span></div></div>
    <template v-else-if="flow">
      <div v-for="(b, i) in banners" :key="i" class="flow-banner" :class="`is-${b.kind}`">
        <span class="mdi" :class="b.kind === 'info' ? 'mdi-source-branch' : 'mdi-alert-outline'"></span>
        <span class="flow-banner-text">{{ b.text }}</span>
        <button v-if="b.settings" type="button" class="flow-banner-link" @click="emit('openSettings')">{{ b.action }} →</button>
        <router-link v-else-if="b.to" :to="b.to" class="flow-banner-link">{{ b.action }} →</router-link>
      </div>

      <div v-if="statsAvailable" class="live-strip">
        <label class="live-toggle">
          <input id="flow-live" v-model="live" type="checkbox" role="switch" />
          <span>{{ $t('routes.flow.live.toggle') }}</span>
        </label>
        <template v-if="liveStats">
          <span class="live-figure"><b>{{ liveStats.requests.toLocaleString() }}</b> {{ $t('routes.flow.live.requests', liveStats.requests) }}</span>
          <span class="live-figure"><b>{{ liveStats.visitors.toLocaleString() }}</b> {{ $t('routes.flow.live.visitors', liveStats.visitors) }}</span>
          <span class="live-figure"><b>{{ formatMs(liveStats.p95_ms) }}</b> p95</span>
          <span class="live-figure" :class="{ 'is-bad': errorShare(liveStats) >= 0.01 }"><b>{{ formatPercent(errorShare(liveStats)) }}</b> 5xx</span>
          <svg class="spark" viewBox="0 0 150 28" preserveAspectRatio="none" role="img" :aria-label="$t('routes.flow.live.sparkLabel')">
            <g v-for="(p, i) in liveStats.series" :key="i">
              <rect :x="i * (150 / liveStats.series.length) + 1" :width="Math.max(1, 150 / liveStats.series.length - 2)" :y="28 - (p.requests / sparkMax) * 26" :height="(p.requests / sparkMax) * 26" class="spark-bar" />
              <rect v-if="p.errors" :x="i * (150 / liveStats.series.length) + 1" :width="Math.max(1, 150 / liveStats.series.length - 2)" :y="28 - (p.errors / sparkMax) * 26" :height="Math.max(1.5, (p.errors / sparkMax) * 26)" class="spark-err" />
            </g>
          </svg>
          <span v-if="!liveStats.requests" class="text-muted text-sm">{{ $t('routes.flow.live.quiet') }}</span>
        </template>
      </div>

      <div class="flow-scroll">
        <div ref="canvas" class="flow-canvas" :class="{ 'has-split': !!canary }">
          <svg class="flow-wires" aria-hidden="true">
            <defs>
              <marker v-for="k in ['ok', 'canary', 'warn', 'bad', 'off']" :id="`flow-ah-${k}`" :key="k" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto-start-reverse">
                <path d="M0 0L10 5L0 10z" :class="`ah-${k}`" />
              </marker>
            </defs>
            <path v-for="(w, i) in wires" :key="i" :d="w.d" class="wire" :class="[`wire-${w.kind}`, { 'wire-path': w.onPath === true, 'wire-dim': w.onPath === false }]" :marker-end="`url(#flow-ah-${w.kind})`" />
          </svg>
          <div class="flow-labels" aria-hidden="true">
            <span v-for="(w, i) in wires.filter((x) => x.label)" :key="i" class="wire-label" :class="`wire-label-${w.kind}`" :style="{ left: `${w.x}px`, top: `${w.y}px` }">{{ w.label }}</span>
          </div>

          <div class="flow-col">
            <span class="flow-col-head">{{ $t('routes.flow.visitors') }}</span>
            <div
              :ref="setNode('visitors')"
              class="flow-node"
              :class="[{ selected: isSelected('visitors') }, tclass('visitors')]"
              tabindex="0"
              role="button"
              :aria-pressed="isSelected('visitors')"
              @click="select({ kind: 'visitors' })"
              @keydown="onKey($event, { kind: 'visitors' })"
            >
              <div class="node-top"><span class="node-icon"><span class="mdi mdi-earth"></span></span>
                <div class="node-heading"><span class="node-name">{{ $t('routes.flow.internet') }}</span><span class="node-sub">{{ $t('routes.flow.browsersAndClients') }}</span></div></div>
              <div v-for="h in flow.hosts" :key="h.host" class="host-line">
                <span
                  class="mdi"
                  :class="h.external || (h.registered && h.verified && !h.banned) ? 'mdi-check-circle-outline is-ok' : 'mdi-alert-circle-outline is-bad'"
                ></span>
                <span class="mono">{{ h.host }}</span>
              </div>
              <span v-if="!flow.hosts.length" class="node-sub">{{ $t('routes.flow.noHosts') }}</span>
              <div v-if="liveStats" class="live-stat">
                {{ liveStats.requests.toLocaleString() }} {{ $t('routes.flow.live.requests', liveStats.requests) }} · {{ liveStats.visitors.toLocaleString() }} {{ $t('routes.flow.live.visitors', liveStats.visitors) }}
              </div>
            </div>
          </div>

          <div class="flow-col">
            <span class="flow-col-head">{{ $t('routes.flow.gatewayTitle') }}</span>
            <div
              :ref="setNode('gateway')"
              class="flow-node"
              :class="[{ selected: isSelected('gateway') }, tclass('gateway')]"
              tabindex="0"
              role="button"
              :aria-pressed="isSelected('gateway')"
              @click="select({ kind: 'gateway' })"
              @keydown="onKey($event, { kind: 'gateway' })"
            >
              <div class="node-top"><span class="node-icon"><span class="mdi mdi-shield-outline"></span></span>
                <div class="node-heading"><span class="node-name">{{ gatewayTitle }}</span><span v-if="flow.gateway.server_name" class="node-sub">{{ flow.gateway.server_name }}</span></div></div>
              <dl class="kv">
                <dt>TLS</dt><dd>{{ tlsText }}</dd>
                <dt>{{ $t('routes.path') }}</dt><dd class="mono">{{ flow.route.path }}<template v-if="flow.route.rewrite"> → {{ flow.route.rewrite }}</template></dd>
                <dt>{{ $t('routes.methods') }}</dt><dd>{{ flow.route.methods.length ? flow.route.methods.join(', ') : $t('routes.flow.allMethods') }}</dd>
              </dl>
              <div class="chips">
                <span v-if="flow.route.exploit_protection" class="badge badge-success">{{ $t('routes.flow.exploitProtection') }}</span>
                <span class="badge" :class="flow.route.maintenance.enabled ? 'badge-warning' : 'badge-neutral'">{{ flow.route.maintenance.enabled ? $t('routes.flow.maintenanceOn') : $t('routes.flow.maintenanceOff') }}</span>
              </div>
              <div v-if="narrower.length" class="node-note">
                <span class="mdi mdi-call-split"></span>
                {{ $t('routes.flow.narrowerNote', { n: narrower.length }, narrower.length) }}
              </div>
              <div v-if="flow.route.has_advanced_config" class="node-note">
                <span class="mdi mdi-code-braces"></span>{{ $t('routes.flow.advancedNote') }}
              </div>
              <div v-if="liveStats && liveStats.requests" class="live-stat">{{ $t('routes.flow.live.gatewayTime', { ms: formatMs(liveStats.avg_gateway_ms) }) }}</div>
            </div>
          </div>

          <div class="flow-col">
            <span class="flow-col-head">{{ parked ? $t('routes.flow.answeredAtGateway') : $t('routes.flow.middlewares') }}</span>
            <div
              v-if="parked"
              :ref="setNode('maint')"
              class="flow-node is-maint"
              :class="[{ selected: isSelected('maint') }, tclass('maint')]"
              tabindex="0"
              role="button"
              :aria-pressed="isSelected('maint')"
              @click="select({ kind: 'maint' })"
              @keydown="onKey($event, { kind: 'maint' })"
            >
              <div class="node-top"><span class="node-icon"><span class="mdi mdi-pause-octagon-outline"></span></span>
                <div class="node-heading"><span class="node-name">{{ flow.route.suspended ? $t('routes.flow.suspendedNotice') : $t('routes.flow.maintTitle') }}</span>
                  <span class="node-sub">{{ flow.route.maintenance.status_code || 503 }} · {{ flow.route.maintenance.has_html ? $t('routes.flow.maintPage') : $t('routes.flow.maintMessage') }}</span></div></div>
              <div v-if="liveStats" class="live-stat">{{ $t('routes.flow.live.maintCount', { n: maintCount.toLocaleString(), code: flow.route.maintenance.status_code || 503 }) }}</div>
            </div>
            <div v-if="flow.chain.length" :ref="setNode('chain')" class="flow-node chain" :class="{ bypassed: downstreamOff }">
              <div
                v-for="(m, i) in flow.chain"
                :key="m.name"
                class="mw"
                :class="[{ selected: isSelected('mw', i), missing: m.missing }, tclass(`mw:${i}`)]"
                tabindex="0"
                role="button"
                :aria-pressed="isSelected('mw', i)"
                @click="select({ kind: 'mw', index: i })"
                @keydown="onKey($event, { kind: 'mw', index: i })"
              >
                <span class="mw-n">{{ i + 1 }}</span>
                <span class="mw-body"><span class="mw-name mono">{{ m.name }}</span><span class="mw-type">{{ m.missing ? $t('routes.flow.mwMissingShort') : m.display_name || m.type }}</span></span>
                <span v-if="m.early_status.length" class="mw-stop" :title="$t('routes.flow.canAnswer', { codes: m.early_status.join(', ') })">{{ m.early_status.join(' ') }}</span>
              </div>
              <div v-if="liveStats && earlyCounts.length" class="live-stat">
                {{ $t('routes.flow.live.earlyAnswers') }} {{ earlyCounts.map((c) => `${c.count.toLocaleString()} × ${c.code}`).join(' · ') }}
              </div>
            </div>
            <div v-else-if="!parked" :ref="setNode('chain')" class="flow-empty-col" :class="[{ bypassed: downstreamOff }, tclass('chain')]">{{ $t('routes.flow.noMiddlewares') }}</div>
          </div>

          <div v-if="canary" class="flow-col">
            <span class="flow-col-head">{{ $t('routes.flow.splitTitle') }}</span>
            <div
              :ref="setNode('split')"
              class="flow-node"
              :class="[{ selected: isSelected('split'), bypassed: downstreamOff }, tclass('split')]"
              tabindex="0"
              role="button"
              :aria-pressed="isSelected('split')"
              @click="select({ kind: 'split' })"
              @keydown="onKey($event, { kind: 'split' })"
            >
              <div class="node-top"><span class="node-icon is-canary"><span class="mdi mdi-source-branch"></span></span>
                <div class="node-heading"><span class="node-name">{{ $t('routes.flow.canary') }}</span><span v-if="canary.release" class="node-sub">v{{ canary.release.version }}</span></div></div>
              <div v-for="(r, i) in canaryRules" :key="i" class="split-rule">
                <span>{{ $t('routes.flow.ruleN', { n: i + 1 }) }}<template v-if="canary.exclusive"> · {{ $t('routes.flow.exclusive') }}</template></span>
                <b class="mono">{{ ruleText(r) }}</b>
              </div>
              <div class="split-bar" role="img" :aria-label="$t('routes.flow.weightSplit', { stable: stable?.weight ?? 0, canary: canary.weight })">
                <i class="s" :style="{ width: `${stable?.weight ?? 0}%` }"></i><i class="c" :style="{ width: `${canary.weight}%` }"></i>
              </div>
              <div class="split-legend"><span>{{ $t('routes.flow.role.stable') }} {{ stable?.weight ?? 0 }}%</span><span>{{ $t('routes.flow.role.canary') }} {{ canary.weight }}%</span></div>
              <div v-if="liveStats && liveStats.variants" class="live-stat">
                {{ $t('routes.flow.live.actualSplit', { stable: Math.round((liveStats.variants.stable?.share ?? 0) * 100), canary: Math.round((liveStats.variants.canary?.share ?? 0) * 100) }) }}
              </div>
              <div v-else-if="liveStats" class="live-stat">{{ $t('routes.flow.live.splitNote') }}</div>
            </div>
          </div>

          <div class="flow-col">
            <span class="flow-col-head">{{ $t('routes.flow.application') }}</span>
            <div
              v-if="!flow.app.deployed"
              :ref="setNode('app')"
              class="flow-node is-bad"
              :class="[{ selected: isSelected('app') }, tclass('app')]"
              tabindex="0"
              role="button"
              @click="select({ kind: 'app' })"
              @keydown="onKey($event, { kind: 'app' })"
            >
              <div class="node-top"><span class="node-icon"><span class="mdi mdi-cube-outline"></span></span>
                <div class="node-heading"><span class="node-name">{{ flow.app.name }}</span><span class="node-sub">{{ $t('routes.flow.notDeployed') }}</span></div></div>
            </div>
            <template v-for="b in flow.backends" :key="b.role">
              <div
                :ref="setNode(b.role)"
                class="flow-node backend"
                :class="[`is-${b.role}`, { selected: isSelected(b.role), bypassed: downstreamOff, 'no-replica': b.health && b.health.desired > 0 && b.health.running === 0 }, tclass(b.role)]"
                tabindex="0"
                role="button"
                :aria-pressed="isSelected(b.role)"
                @click="select({ kind: b.role })"
                @keydown="onKey($event, { kind: b.role })"
              >
                <div class="node-top"><span class="node-icon" :class="`is-${b.role}`"><span class="mdi mdi-cube-outline"></span></span>
                  <div class="node-heading">
                    <span class="node-name">{{ flow.app.name }} <span v-if="flow.backends.length > 1" class="badge" :class="b.role === 'canary' ? 'badge-info' : 'badge-success'">{{ $t(`routes.flow.role.${b.role}`) }}</span></span>
                    <span v-if="b.release" class="node-sub">v{{ b.release.version }}<template v-if="b.release.image_tag"> · {{ b.release.image_tag }}</template></span>
                  </div>
                </div>
                <div v-if="b.health" class="replicas">
                  <i v-for="n in Math.min(Math.max(b.health.desired, b.health.running), 8)" :key="n" class="rep" :class="{ down: n > b.health.running }"></i>
                  <span>{{ healthText(b.health) }}</span>
                </div>
                <span class="target mono">{{ b.endpoint }}</span>
                <div v-if="liveStats && liveStats.variants?.[b.role]" class="live-stat" :class="{ 'is-bad': b.role === 'canary' && canaryWorse(liveStats) }">
                  {{ $t('routes.flow.live.releaseLine', { requests: liveStats.variants[b.role]!.requests.toLocaleString(), p95: formatMs(liveStats.variants[b.role]!.p95_ms), errors: formatPercent(liveStats.variants[b.role]!.error_rate) }) }}
                </div>
                <div v-else-if="liveStats && liveStats.requests && flow.backends.length === 1" class="live-stat">
                  {{ $t('routes.flow.live.backendLine', { p95: formatMs(liveStats.p95_ms), errors: formatPercent(errorShare(liveStats)) }) }}
                </div>
              </div>
            </template>
          </div>
        </div>
      </div>

      <div class="flow-panels">
      <section class="flow-details" aria-live="polite">
        <template v-if="details">
          <div class="flow-details-head">
            <h3>{{ details.title }}</h3>
            <button v-if="details.link?.settings" type="button" class="btn btn-secondary btn-sm" @click="emit('openSettings')">{{ details.link.label }}</button>
            <router-link v-else-if="details.link?.to" :to="details.link.to" class="btn btn-secondary btn-sm">{{ details.link.label }}</router-link>
          </div>
          <dl class="detail-rows">
            <template v-for="(r, i) in details.rows" :key="i">
              <dt>{{ r.label }}</dt>
              <dd><router-link v-if="r.to" :to="r.to">{{ r.value }}</router-link><template v-else>{{ r.value }}</template></dd>
            </template>
          </dl>
        </template>
        <p v-else class="text-muted text-sm">{{ $t('routes.flow.selectHint') }}</p>
      </section>

      <section class="flow-details flow-trace">
        <div class="flow-details-head">
          <h3>{{ $t('routes.flow.trace.title') }}</h3>
          <button v-if="trace" type="button" class="btn btn-ghost btn-sm" @click="clearTrace">{{ $t('routes.flow.trace.clear') }}</button>
        </div>
        <p class="text-muted text-sm">{{ $t('routes.flow.trace.hint') }}</p>
        <form class="trace-form" @submit.prevent="runTrace">
          <div class="trace-line">
            <select id="trace-method" v-model="traceForm.method" class="form-select" :aria-label="$t('routes.methods')">
              <option v-for="m in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']" :key="m">{{ m }}</option>
            </select>
            <select id="trace-scheme" v-model="traceForm.scheme" class="form-select" :aria-label="$t('routes.flow.trace.scheme')">
              <option value="https">https://</option>
              <option value="http">http://</option>
            </select>
            <select v-if="flow.hosts.length > 1" id="trace-host" v-model="traceForm.host" class="form-select trace-host" :aria-label="$t('routes.host')">
              <option v-for="h in flow.hosts" :key="h.host" :value="h.host">{{ h.host }}</option>
            </select>
            <span v-else-if="flow.hosts.length" class="trace-host-text mono">{{ flow.hosts[0].host }}</span>
            <input id="trace-path" v-model="traceForm.path" class="form-input mono trace-path" spellcheck="false" :aria-label="$t('routes.path')" placeholder="/" />
          </div>
          <textarea id="trace-headers" v-model="traceForm.headers" class="form-input mono" rows="3" spellcheck="false" :placeholder="$t('routes.flow.trace.headersPlaceholder')" :aria-label="$t('routes.flow.trace.headers')"></textarea>
          <div class="trace-line">
            <input id="trace-ip" v-model="traceForm.ip" class="form-input mono trace-ip" spellcheck="false" :placeholder="$t('routes.flow.trace.ipPlaceholder')" :aria-label="$t('routes.flow.trace.ip')" />
            <button v-if="canaryHeaderRule" type="button" class="btn btn-ghost btn-sm" @click="addCanaryHeader">+ {{ canaryHeaderRule.name }}</button>
            <button type="submit" class="btn btn-primary btn-sm trace-run" :disabled="tracing || !traceForm.path.trim()">{{ tracing ? $t('routes.flow.trace.running') : $t('routes.flow.trace.run') }}</button>
          </div>
        </form>
        <template v-if="trace">
          <div class="trace-result" :class="resultTone">{{ resultText }}</div>
          <ol class="trace-steps">
            <li v-for="(s, i) in trace.steps" :key="i" :class="`is-${s.outcome}`">
              <span class="trace-mark">{{ s.outcome === 'stop' ? '!' : s.outcome === 'skip' ? '–' : s.outcome === 'note' ? 'i' : s.outcome === 'fork' ? '⑂' : '✓' }}</span>
              <span>{{ stepText(s) }}</span>
            </li>
          </ol>
          <router-link v-if="trace.result.kind === 'other_route' && trace.result.route_id" :to="routeLink(trace.result.route_id)" class="flow-banner-link">{{ $t('routes.flow.trace.openOther', { route: trace.result.route_name }) }} →</router-link>
          <p class="text-muted text-xs">{{ $t('routes.flow.trace.disclaimer') }}</p>
        </template>
      </section>
      </div>
    </template>
  </div>
</template>

<style scoped>
.flow { display: flex; flex-direction: column; gap: 12px; }
.flow-banner { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 10px 14px; border-radius: 10px; font-size: 13px; color: var(--text-primary); }
.flow-banner.is-danger { background: var(--danger-50); }
.flow-banner.is-danger .mdi { color: var(--danger-600); }
.flow-banner.is-warning { background: var(--warning-50); }
.flow-banner.is-warning .mdi { color: var(--warning-600); }
.flow-banner.is-info { background: var(--info-50); }
.flow-banner.is-info .mdi { color: var(--info-600); }
.flow-banner-text { flex: 1; min-width: 200px; }
.flow-banner-link { margin-left: auto; font-weight: 600; color: var(--primary-500); background: none; border: 0; padding: 0; font: inherit; font-weight: 600; cursor: pointer; text-decoration: none; white-space: nowrap; }
.flow-banner-link:hover { text-decoration: underline; }

.live-strip { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 18px; font-size: 13px; color: var(--text-muted); }
.live-toggle { display: inline-flex; align-items: center; gap: 8px; cursor: pointer; color: var(--text-secondary); font-weight: 500; }
.live-toggle input { width: 16px; height: 16px; accent-color: var(--primary-500); }
.live-figure b { color: var(--text-primary); font-variant-numeric: tabular-nums; }
.live-figure.is-bad b { color: var(--danger-600); }
.spark { width: 150px; height: 28px; }
.spark-bar { fill: color-mix(in srgb, var(--primary-500) 45%, transparent); }
.spark-err { fill: var(--danger-500); }
.live-stat.is-bad { color: var(--danger-600); font-weight: 600; }
.live-stat { font-size: 12px; color: var(--text-secondary); padding-top: 6px; border-top: 1px dashed var(--border-primary); font-variant-numeric: tabular-nums; }
.flow-scroll { overflow-x: auto; border: 1px solid var(--border-primary); border-radius: 12px; background: radial-gradient(circle, var(--border-primary) 1px, transparent 1.2px) 0 0 / 18px 18px, var(--bg-secondary); }
.flow-canvas { position: relative; min-width: 960px; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); column-gap: 84px; align-items: center; padding: 28px 24px; }
.flow-canvas.has-split { min-width: 1180px; grid-template-columns: 1fr 1.05fr 1fr .95fr 1.05fr; column-gap: 72px; }
.flow-wires { position: absolute; inset: 0; width: 100%; height: 100%; pointer-events: none; overflow: visible; }
.flow-labels { position: absolute; inset: 0; pointer-events: none; z-index: 2; }
.flow-col { display: flex; flex-direction: column; gap: 12px; min-width: 0; position: relative; z-index: 1; }
.flow-col-head { font-size: 11px; text-transform: uppercase; letter-spacing: .07em; color: var(--text-muted); font-weight: 600; }
.flow-empty-col { font-size: 12px; color: var(--text-muted); padding: 10px 12px; border: 1px dashed var(--border-primary); border-radius: 10px; text-align: center; background: var(--bg-primary); }
.flow-empty-col.bypassed, .flow-empty-col.off-path { opacity: .45; }
.flow-empty-col.on-path { border-color: var(--primary-500); color: var(--text-secondary); }

.flow-node { background: var(--bg-primary); border: 1px solid var(--border-primary); border-radius: 12px; padding: 12px; display: flex; flex-direction: column; gap: 8px; cursor: pointer; min-width: 0; transition: border-color .15s, box-shadow .15s, opacity .2s; }
.flow-node:hover, .mw:hover { border-color: var(--primary-500); }
.flow-node:focus-visible, .mw:focus-visible { outline: 2px solid var(--primary-500); outline-offset: 2px; }
.flow-node.selected, .mw.selected { border-color: var(--primary-500); box-shadow: 0 0 0 3px color-mix(in srgb, var(--primary-500) 18%, transparent); }
.flow-node.bypassed { opacity: .45; }
.flow-node.is-maint { border-color: var(--warning-500); background: var(--warning-50); }
.flow-node.is-bad, .flow-node.no-replica { border-color: var(--danger-500); }
.node-top { display: flex; align-items: center; gap: 9px; min-width: 0; }
.node-icon { width: 30px; height: 30px; flex: none; border-radius: 8px; display: grid; place-items: center; background: color-mix(in srgb, var(--primary-500) 14%, transparent); color: var(--primary-500); font-size: 17px; }
.node-icon.is-stable { background: var(--success-50); color: var(--success-600); }
.node-icon.is-canary { background: var(--info-50); color: var(--info-600); }
.is-maint .node-icon { background: var(--bg-primary); color: var(--warning-600); }
.node-heading { display: flex; flex-direction: column; min-width: 0; }
.node-name { font-weight: 600; font-size: 13.5px; line-height: 1.3; overflow-wrap: anywhere; }
.node-sub { font-size: 12px; color: var(--text-muted); overflow-wrap: anywhere; }
.node-note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; color: var(--text-secondary); }
.host-line { display: flex; align-items: center; gap: 6px; font-size: 12px; min-width: 0; }
.host-line .mono { overflow-wrap: anywhere; min-width: 0; }
.is-ok { color: var(--success-600); }
.is-bad { color: var(--danger-600); }
.kv { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 3px 10px; margin: 0; font-size: 12px; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; text-align: right; overflow-wrap: anywhere; }
.chips { display: flex; flex-wrap: wrap; gap: 4px; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }

.chain { padding: 8px; gap: 6px; cursor: default; }
.chain:hover { border-color: var(--border-primary); }
.mw { display: flex; align-items: center; gap: 8px; padding: 7px 8px; border: 1px solid var(--border-primary); border-radius: 9px; background: var(--bg-secondary); cursor: pointer; }
.mw.missing { border-style: dashed; border-color: var(--danger-500); }
.mw-n { width: 20px; height: 20px; flex: none; border-radius: 50%; display: grid; place-items: center; font-size: 11px; font-weight: 700; background: color-mix(in srgb, var(--primary-500) 14%, transparent); color: var(--primary-500); }
.mw-body { display: flex; flex-direction: column; min-width: 0; line-height: 1.3; }
.mw-name { overflow-wrap: anywhere; }
.mw-type { font-size: 11px; color: var(--text-muted); }
.mw-stop { margin-left: auto; font-size: 11px; font-weight: 700; color: var(--warning-600); white-space: nowrap; }

.split-rule { display: flex; flex-direction: column; gap: 2px; font-size: 12px; padding: 6px 8px; border-radius: 8px; background: var(--info-50); }
.split-rule b { font-weight: 500; overflow-wrap: anywhere; }
.split-bar { display: flex; height: 8px; border-radius: 99px; overflow: hidden; background: var(--border-primary); }
.split-bar i { display: block; height: 100%; }
.split-bar .s { background: var(--success-500); }
.split-bar .c { background: var(--info-500); }
.split-legend { display: flex; justify-content: space-between; gap: 8px; font-size: 11.5px; color: var(--text-muted); font-variant-numeric: tabular-nums; }

.backend.is-stable { border-left: 3px solid var(--success-500); }
.backend.is-canary { border-left: 3px solid var(--info-500); }
.replicas { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; font-size: 12px; color: var(--text-muted); }
.rep { width: 10px; height: 10px; border-radius: 3px; background: var(--success-500); }
.rep.down { background: var(--danger-500); }
.target { color: var(--text-muted); overflow-wrap: anywhere; font-size: 11.5px; }

.wire { fill: none; stroke: var(--border-secondary, var(--border-primary)); stroke-width: 2; }
.wire-ok { stroke: var(--primary-500); }
.wire-canary { stroke: var(--info-500); }
.wire-warn { stroke: var(--warning-500); }
.wire-bad { stroke: var(--danger-500); stroke-dasharray: 6 5; }
.wire-off { stroke: var(--text-muted); stroke-dasharray: 4 6; opacity: .45; }
.ah-ok { fill: var(--primary-500); }
.ah-canary { fill: var(--info-500); }
.ah-warn { fill: var(--warning-500); }
.ah-bad { fill: var(--danger-500); }
.ah-off { fill: var(--text-muted); opacity: .45; }
.wire-label { position: absolute; transform: translate(-50%, -50%); background: var(--bg-primary); border: 1px solid var(--border-primary); border-radius: 7px; padding: 1px 7px; font-size: 11px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.wire-label-bad { color: var(--danger-600); border-color: var(--danger-500); }
.wire-label-warn { color: var(--warning-600); border-color: var(--warning-500); }
.wire-label-canary { color: var(--info-600); }

.flow-panels { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; align-items: start; }
.flow-details { border: 1px solid var(--border-primary); border-radius: 12px; padding: 14px 16px; background: var(--bg-primary); display: flex; flex-direction: column; gap: 10px; }
.flow-details-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.flow-details-head h3 { margin: 0; font-size: 14px; }
.trace-form { display: flex; flex-direction: column; gap: 8px; }
.trace-line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.trace-line .form-select { width: auto; }
.trace-host-text { color: var(--text-muted); overflow-wrap: anywhere; }
.trace-path { flex: 1; min-width: 140px; }
.trace-ip { width: 200px; max-width: 100%; }
.trace-run { margin-left: auto; }
.trace-result { padding: 8px 12px; border-radius: 9px; font-size: 13px; font-weight: 600; }
.trace-result.is-ok { background: var(--success-50); color: var(--success-700, var(--success-600)); }
.trace-result.is-info { background: var(--info-50); color: var(--info-700, var(--info-600)); }
.trace-result.is-stop { background: var(--warning-50); color: var(--warning-700, var(--warning-600)); }
.trace-steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
.trace-steps li { display: grid; grid-template-columns: 18px minmax(0, 1fr); gap: 8px; align-items: start; }
.trace-mark { width: 18px; height: 18px; border-radius: 50%; display: grid; place-items: center; font-size: 11px; font-weight: 700; margin-top: 1px; background: var(--success-50); color: var(--success-600); }
.is-stop .trace-mark { background: var(--warning-50); color: var(--warning-600); }
.is-skip .trace-mark, .is-note .trace-mark { background: var(--bg-tertiary); color: var(--text-muted); }
.is-skip, .is-note { color: var(--text-secondary); }
.is-fork .trace-mark { background: var(--info-50); color: var(--info-600); }
.text-xs { font-size: 12px; margin: 0; }
.flow-node.on-path, .mw.on-path { border-color: var(--primary-500); box-shadow: 0 0 0 3px color-mix(in srgb, var(--primary-500) 18%, transparent); }
.flow-node.off-path, .mw.off-path { opacity: .35; }
.flow-node.stop-here, .mw.stop-here { border-color: var(--warning-500); box-shadow: 0 0 0 3px color-mix(in srgb, var(--warning-500) 25%, transparent); opacity: 1; }
.wire-path { stroke-width: 3.5; }
.wire-dim { opacity: .25; }
.detail-rows { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 6px 16px; margin: 0; font-size: 13px; }
.detail-rows dt { color: var(--text-muted); }
.detail-rows dd { margin: 0; overflow-wrap: anywhere; }

@media (max-width: 760px) {
  .flow-canvas, .flow-canvas.has-split { min-width: 0; grid-template-columns: minmax(0, 1fr); row-gap: 44px; padding: 20px 14px; }
  .detail-rows { grid-template-columns: minmax(0, 1fr); gap: 2px; }
  .flow-panels { grid-template-columns: minmax(0, 1fr); }
  .detail-rows dd { margin-bottom: 6px; }
}
</style>
