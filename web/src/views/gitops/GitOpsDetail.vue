<script setup lang="ts">
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { VueFlow, useVueFlow, MarkerType, Position, type Node, type Edge } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import dagre from 'dagre'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'

import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { gitopsApi } from '@/api/gitops'
import { eventsApi } from '@/api/events'
import { databaseApi } from '@/api/resources'
import ResourceNode from './ResourceNode.vue'
import ProjectNode from './ProjectNode.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import LogViewer from '@/components/LogViewer.vue'
import { kindOf, nodeStatusMeta, gitSourceStatusMeta, edgeLabel, resourceRoute } from './topologyMeta'
import type { GitSource, Topology, TopologyNode, PlanChange, NodeStatus, AppEvent, ApplyPlan } from '@/api/types'

// Synthetic id for the project root node (the GitSource itself is not a
// declarative resource, so it never collides with a "<Kind>/<name>" key).
const PROJECT_ID = '__project__'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const sourceId = computed(() => Number(route.params.id))

const source = ref<GitSource | null>(null)
const topo = ref<Topology | null>(null)
const planByKey = ref<Record<string, PlanChange>>({})
const loading = ref(false)
const syncing = ref(false)
const selectedKey = ref<string | null>(null)
// Per-resource action state (sync/delete of the selected node).
const resourceBusy = ref(false)
const confirmDelete = ref(false)

// Resource drawer tabs. Overview is always present; Events/Logs appear for the
// kinds that have them (and only once the resource is live).
type DrawerTab = 'overview' | 'events' | 'logs'
const activeTab = ref<DrawerTab>('overview')
const drawerEvents = ref<AppEvent[]>([])
const eventsLoading = ref(false)
const eventsHasMore = ref(false)
const loadingMoreEvents = ref(false)
const drawerLogs = ref<string[]>([])
const logsConnected = ref(false)
let eventsES: EventSource | null = null
let logsES: EventSource | null = null
const EVENTS_PAGE = 30
const LOG_CAP = 2000

// Header status filter: clicking a chip toggles that status; when the set is
// non-empty, non-matching nodes are dimmed on the graph.
const statusFilter = ref<Set<NodeStatus>>(new Set())
// Preview-&-sync modal: shows the full plan (from the diff endpoint) before applying.
const previewOpen = ref(false)
const previewLoading = ref(false)
const previewPlan = ref<ApplyPlan | null>(null)

const { setNodes, setEdges, fitView, updateNodeData } = useVueFlow()

// Live status: a cheap poll (no repo clone) keeps each node's runtime health
// fresh between full topology loads.
const LIVE_POLL_MS = 8000
let liveTimer: ReturnType<typeof setTimeout> | null = null
const liveOn = ref(true)

function stopLive() {
  if (liveTimer) {
    clearTimeout(liveTimer)
    liveTimer = null
  }
}
function scheduleLive() {
  stopLive()
  if (!liveOn.value) return
  liveTimer = setTimeout(pollStatus, LIVE_POLL_MS)
}
async function pollStatus() {
  // Skip work when nothing to update or the tab is in the background.
  if (!currentWorkspaceId.value || !sourceId.value || !topo.value?.nodes?.length || document.hidden) {
    scheduleLive()
    return
  }
  try {
    const res = await gitopsApi.status(currentWorkspaceId.value, sourceId.value)
    const map = res.data.data?.statuses ?? {}
    for (const n of topo.value.nodes) {
      const h = map[n.key]
      if (h !== undefined && h !== n.health) {
        n.health = h // updates the side panel (selectedNode reads from here)
        updateNodeData(n.key, { health: h }) // updates the graph node
      }
    }
    liveError.value = false
  } catch {
    liveError.value = true // a transient blip shouldn't spam toasts
  } finally {
    scheduleLive()
  }
}
const liveError = ref(false)
function toggleLive() {
  liveOn.value = !liveOn.value
  if (liveOn.value) pollStatus()
  else stopLive()
}
onBeforeUnmount(stopLive)

// Header badge reuses the shared project status meta.
const sourceStatusMeta = gitSourceStatusMeta

// ---- data load + graph build -------------------------------------------------

async function load() {
  if (!currentWorkspaceId.value || !sourceId.value) return
  loading.value = true
  // Load the source first so the header + sync error always render, even if the
  // topology cannot be built (a broken commit, an unreachable repo).
  try {
    const src = await gitopsApi.get(currentWorkspaceId.value, sourceId.value)
    source.value = src.data.data
  } catch (e) {
    notify.apiError(e, 'Could not load git source')
    loading.value = false
    return
  }
  try {
    await refreshGraph()
    restoreSelection() // re-open the resource named in ?resource=, if any
  } catch (e) {
    // Topology itself failed (rare — the backend degrades to live state). Keep the
    // header + error visible rather than blanking the page.
    topo.value = null
    notify.apiError(e, 'Could not load project topology')
  } finally {
    loading.value = false
  }
}

// refreshGraph (re)loads the topology and the per-resource diff, then rebuilds the
// Vue Flow graph. The diff is best-effort — it fails the same way the topology
// would on a broken revision, in which case the side panel simply omits diffs.
async function refreshGraph() {
  if (!currentWorkspaceId.value || !sourceId.value) return
  const tp = await gitopsApi.topology(currentWorkspaceId.value, sourceId.value)
  topo.value = tp.data.data
  try {
    const diff = await gitopsApi.diff(currentWorkspaceId.value, sourceId.value)
    const map: Record<string, PlanChange> = {}
    for (const c of diff.data.data?.changes ?? []) map[`${c.kind}/${c.name}`] = c
    planByKey.value = map
  } catch {
    planByKey.value = {}
  }
  buildGraph()
  scheduleLive() // keep node health live after the structure is (re)built
}

// dagre lays the tree left→right rooted at the project node: the project fans out
// to its resources, which then chain by dependency (route→app→db/volume), giving
// the ArgoCD-style "project → resources" shape.
function buildGraph() {
  const tnodes = topo.value?.nodes ?? []
  const tedges = topo.value?.edges ?? []
  const hasProject = tnodes.length > 0 && !!source.value

  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: 24, ranksep: 80, marginx: 16, marginy: 16 })
  g.setDefaultEdgeLabel(() => ({}))
  const W = 200
  const H = 56
  const PW = 220
  const PH = 64
  if (hasProject) g.setNode(PROJECT_ID, { width: PW, height: PH })
  for (const n of tnodes) g.setNode(n.key, { width: W, height: H })
  for (const e of tedges) {
    if (g.hasNode(e.from) && g.hasNode(e.to)) g.setEdge(e.from, e.to)
  }
  // Link the project to every resource nothing else points to (the roots of the
  // dependency forest), so isolated resources (e.g. an unreferenced secret) and
  // entry points (routes) both hang off the project.
  const hasIncoming = new Set(tedges.map((e) => e.to))
  const roots = tnodes.filter((n) => !hasIncoming.has(n.key))
  if (hasProject) for (const r of roots) g.setEdge(PROJECT_ID, r.key)
  dagre.layout(g)

  const flowNodes: Node[] = []
  if (hasProject) {
    const p = g.node(PROJECT_ID)
    flowNodes.push({
      id: PROJECT_ID,
      type: 'project',
      position: { x: (p?.x ?? 0) - PW / 2, y: (p?.y ?? 0) - PH / 2 },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
      data: { name: source.value!.name, status: source.value!.status, count: tnodes.length },
    })
  }
  for (const n of tnodes) {
    const p = g.node(n.key)
    flowNodes.push({
      id: n.key,
      type: 'resource',
      position: { x: (p?.x ?? 0) - W / 2, y: (p?.y ?? 0) - H / 2 },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
      data: { kind: n.kind, name: n.name, status: n.status, health: n.health, liveId: n.live_id },
    })
  }

  const flowEdges: Edge[] = tedges.map((e, i) => ({
    id: `e${i}`,
    source: e.from,
    target: e.to,
    type: 'smoothstep',
    animated: false,
    markerEnd: MarkerType.ArrowClosed,
    label: edgeLabel[e.type],
    labelBgStyle: { fill: 'var(--bg-primary)' },
    labelStyle: { fontSize: '10px', fill: 'var(--text-muted)' },
    style: { stroke: 'var(--border-primary)', strokeWidth: 1.5 },
  }))
  // Ownership edges from the project to its root resources — solid, accented, no
  // label, to read as "manages" rather than a data dependency.
  if (hasProject) {
    roots.forEach((r, i) => {
      flowEdges.push({
        id: `pe${i}`,
        source: PROJECT_ID,
        target: r.key,
        type: 'smoothstep',
        animated: false,
        markerEnd: MarkerType.ArrowClosed,
        style: { stroke: 'var(--text-muted)', strokeWidth: 1.5 },
      })
    })
  }

  setNodes(flowNodes)
  setEdges(flowEdges)
  // Preserve the selection across refreshes (e.g. after a per-resource sync) when
  // the node still exists; drop it only if the resource is gone.
  const keys = new Set(tnodes.map((n) => n.key))
  if (selectedKey.value && !keys.has(selectedKey.value)) selectedKey.value = null
  applyDimming()
  nextTick(() => fitView({ padding: 0.2, maxZoom: 1.1 }))
}

// applyDimming pushes the current status filter onto each node so ResourceNode can
// fade the ones that don't match. Cheap (data-only update, no relayout).
function applyDimming() {
  const active = statusFilter.value.size > 0
  for (const n of topo.value?.nodes ?? []) {
    updateNodeData(n.key, { dimmed: active && !statusFilter.value.has(n.status) })
  }
}

function toggleStatusFilter(s: NodeStatus) {
  const next = new Set(statusFilter.value)
  if (next.has(s)) next.delete(s)
  else next.add(s)
  statusFilter.value = next
}
watch(statusFilter, applyDimming)

watch([currentWorkspaceId, sourceId], load, { immediate: true })

// ---- interactions ------------------------------------------------------------

function onNodeClick(e: { node: Node }) {
  // The project root has no side panel (the header is its detail); clicking it
  // just clears any resource selection.
  selectedKey.value = e.node.id === PROJECT_ID ? null : e.node.id
}

const selectedNode = computed<TopologyNode | null>(() => {
  if (!selectedKey.value) return null
  return (topo.value?.nodes ?? []).find((n) => n.key === selectedKey.value) ?? null
})
const selectedChange = computed<PlanChange | null>(() =>
  selectedKey.value ? planByKey.value[selectedKey.value] ?? null : null,
)
// Edges touching the selected node, split into outgoing (depends on) / incoming.
const selectedDeps = computed(() => {
  if (!selectedKey.value) return { out: [], in: [] as { key: string; type: string }[] }
  const edges = topo.value?.edges ?? []
  return {
    out: edges.filter((e) => e.from === selectedKey.value).map((e) => ({ key: e.to, type: e.type })),
    in: edges.filter((e) => e.to === selectedKey.value).map((e) => ({ key: e.from, type: e.type })),
  }
})

function nameOf(key: string) {
  return key.split('/').slice(1).join('/')
}
function openResource(node: TopologyNode | null) {
  if (!node) return
  const target = resourceRoute(node.kind, node.live_id)
  if (target) router.push(target)
}

// syncResource reconciles just the selected resource from Git (leaves the rest of
// the project untouched), then refreshes the graph.
async function syncResource() {
  const node = selectedNode.value
  if (!node || !currentWorkspaceId.value || !source.value) return
  resourceBusy.value = true
  try {
    const res = await gitopsApi.syncResource(currentWorkspaceId.value, source.value.id, node.kind, node.name)
    const fail = res.data.data?.failures?.[0]
    if (fail) notify.error(fail.error || `Failed to sync ${node.name}`)
    else notify.success(`Synced ${node.name}`)
    await refreshGraph()
  } catch (e) {
    notify.apiError(e, `Could not sync ${node.name}`)
  } finally {
    resourceBusy.value = false
  }
}

// deleteResource deletes the selected resource's live object. Under auto-sync it
// is recreated on the next reconcile — the confirm dialog says so.
async function deleteResource() {
  const node = selectedNode.value
  if (!node || !currentWorkspaceId.value || !source.value) return
  resourceBusy.value = true
  try {
    const res = await gitopsApi.deleteResource(currentWorkspaceId.value, source.value.id, node.kind, node.name)
    const fail = res.data.data?.failures?.[0]
    if (fail) notify.error(fail.error || `Failed to delete ${node.name}`)
    else notify.success(`Deleted ${node.name}`)
    confirmDelete.value = false
    await refreshGraph()
  } catch (e) {
    notify.apiError(e, `Could not delete ${node.name}`)
  } finally {
    resourceBusy.value = false
  }
}

const deleteMessage = computed(() => {
  const n = selectedNode.value
  if (!n) return ''
  const base = `Delete the live ${kindOf(n.kind).label} “${n.name}”. This removes the running resource now.`
  return source.value?.sync_policy === 'auto'
    ? `${base} This project auto-syncs, so it will be recreated from Git on the next reconcile.`
    : `${base} It stays gone until the next sync re-applies it from Git.`
})

// --- Resource drawer tabs (Overview / Events / Logs) ---

// Which tabs the selected resource supports. Events/Logs need a live resource
// (live_id) and only apply to the kinds that emit them.
const drawerTabs = computed<{ key: DrawerTab; label: string }[]>(() => {
  const tabs: { key: DrawerTab; label: string }[] = [{ key: 'overview', label: 'Overview' }]
  const n = selectedNode.value
  if (!n || !n.live_id) return tabs
  if (n.kind === 'Application') tabs.push({ key: 'events', label: 'Events' })
  if (n.kind === 'Application' || n.kind === 'Database') tabs.push({ key: 'logs', label: 'Logs' })
  return tabs
})
// Widen the drawer for the data-heavy tabs.
const wideDrawer = computed(() => activeTab.value !== 'overview')

function closeStreams() {
  eventsES?.close()
  eventsES = null
  logsES?.close()
  logsES = null
  logsConnected.value = false
}

async function loadEvents(node: TopologyNode) {
  if (!currentWorkspaceId.value || !node.live_id) return
  eventsLoading.value = true
  try {
    const res = await eventsApi.list(currentWorkspaceId.value, node.live_id, undefined, EVENTS_PAGE)
    drawerEvents.value = res.data.data ?? []
    eventsHasMore.value = drawerEvents.value.length >= EVENTS_PAGE
  } catch (e) {
    notify.apiError(e, 'Could not load events')
  } finally {
    eventsLoading.value = false
  }
}

async function loadMoreEvents() {
  const node = selectedNode.value
  if (!currentWorkspaceId.value || !node?.live_id || drawerEvents.value.length === 0) return
  loadingMoreEvents.value = true
  try {
    const oldest = drawerEvents.value[drawerEvents.value.length - 1].id
    const res = await eventsApi.list(currentWorkspaceId.value, node.live_id, oldest, EVENTS_PAGE)
    const more = res.data.data ?? []
    drawerEvents.value.push(...more)
    eventsHasMore.value = more.length >= EVENTS_PAGE
  } finally {
    loadingMoreEvents.value = false
  }
}

function streamEvents(node: TopologyNode) {
  if (!currentWorkspaceId.value || !node.live_id) return
  eventsES = new EventSource(eventsApi.streamUrl(currentWorkspaceId.value, node.live_id))
  eventsES.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as { type: string; data: AppEvent }
      const e = msg.data
      if (e?.id && !drawerEvents.value.some((x) => x.id === e.id)) drawerEvents.value.unshift(e)
    } catch {
      /* keep-alive */
    }
  }
  eventsES.onerror = () => eventsES?.close()
}

function streamLogs(node: TopologyNode) {
  if (!currentWorkspaceId.value || !node.live_id) return
  drawerLogs.value = []
  const url =
    node.kind === 'Database'
      ? databaseApi.logsUrl(currentWorkspaceId.value, node.live_id)
      : eventsApi.logsUrl(currentWorkspaceId.value, node.live_id)
  logsES = new EventSource(url)
  logsES.onopen = () => (logsConnected.value = true)
  logsES.onmessage = (ev) => {
    try {
      const l = JSON.parse(ev.data) as { text?: string }
      if (l.text != null) {
        drawerLogs.value.push(l.text)
        if (drawerLogs.value.length > LOG_CAP) drawerLogs.value.splice(0, drawerLogs.value.length - LOG_CAP)
      }
    } catch {
      /* keep-alive */
    }
  }
  logsES.onerror = () => {
    logsConnected.value = false
    logsES?.close()
  }
}

// Drive the active tab's data: (re)selecting a resource or switching tabs tears
// down any open stream and sets up the one the active tab needs. Selecting a new
// resource resets to Overview.
watch(selectedKey, () => {
  activeTab.value = 'overview'
  // Keep the selection in the URL so it survives reload and is shareable.
  router.replace({ query: { ...route.query, resource: selectedKey.value || undefined } })
})

// restoreSelection re-selects the resource named in ?resource= after a (re)load,
// when nothing is selected and that resource exists.
function restoreSelection() {
  if (selectedKey.value) return
  const q = route.query.resource
  if (typeof q === 'string' && (topo.value?.nodes ?? []).some((n) => n.key === q)) {
    selectedKey.value = q
  }
}

// openPreview fetches the desired-vs-live plan and shows it before a full sync.
async function openPreview() {
  if (!currentWorkspaceId.value || !source.value) return
  previewOpen.value = true
  previewLoading.value = true
  previewPlan.value = null
  try {
    const res = await gitopsApi.diff(currentWorkspaceId.value, source.value.id)
    previewPlan.value = res.data.data ?? null
  } catch (e) {
    notify.apiError(e, 'Could not load the plan')
  } finally {
    previewLoading.value = false
  }
}
// Non-noop changes the preview lists (create/update/delete).
const previewChanges = computed<PlanChange[]>(() =>
  (previewPlan.value?.changes ?? []).filter((c) => c.action !== 'noop'),
)
async function previewAndSync() {
  previewOpen.value = false
  await sync()
}
// planActionMeta styles a plan change by its action.
function planActionMeta(action: string): { label: string; badge: string } {
  switch (action) {
    case 'create':
      return { label: 'Create', badge: 'badge-success' }
    case 'update':
      return { label: 'Update', badge: 'badge-info' }
    case 'delete':
      return { label: 'Delete', badge: 'badge-danger' }
    default:
      return { label: action, badge: 'badge-neutral' }
  }
}
watch([selectedKey, activeTab], () => {
  closeStreams()
  const node = selectedNode.value
  if (!node) return
  if (activeTab.value === 'events') {
    void loadEvents(node)
    streamEvents(node)
  } else if (activeTab.value === 'logs') {
    streamLogs(node)
  }
})
onBeforeUnmount(closeStreams)

// eventIcon maps an event type to an mdi glyph (mirrors the app timeline).
function eventIcon(type: string): string {
  if (type.includes('deploy')) return 'mdi-rocket-launch-outline'
  if (type.includes('fail') || type.includes('error')) return 'mdi-alert-circle-outline'
  if (type.includes('stop')) return 'mdi-stop-circle-outline'
  if (type.includes('start') || type.includes('restart')) return 'mdi-play-circle-outline'
  if (type.includes('scale')) return 'mdi-arrow-expand-vertical'
  return 'mdi-information-outline'
}

async function sync() {
  if (!currentWorkspaceId.value || !source.value) return
  syncing.value = true
  try {
    const res = await gitopsApi.sync(currentWorkspaceId.value, source.value.id)
    source.value = res.data.data
    if (source.value.status === 'error') notify.error(source.value.message || 'Sync failed')
    else notify.success('Synced')
    // Reflect new live state in the graph (degrades gracefully if the sync errored).
    await refreshGraph()
  } catch (e) {
    notify.apiError(e, 'Sync failed')
  } finally {
    syncing.value = false
  }
}

const counts = computed(() => topo.value?.counts ?? {})
const statusOrder: NodeStatus[] = ['synced', 'out_of_sync', 'missing', 'orphaned']
const visibleCounts = computed(() =>
  statusOrder.filter((s) => (counts.value[s] ?? 0) > 0).map((s) => ({ status: s, n: counts.value[s] ?? 0 })),
)
const nodeCount = computed(() => topo.value?.nodes?.length ?? 0)
function shortSha(sha?: string) {
  return sha ? sha.slice(0, 7) : '—'
}
function relTime(ts?: string | null) {
  if (!ts) return 'never'
  const diff = Date.now() - new Date(ts).getTime()
  const m = Math.round(diff / 60000)
  if (m < 1) return 'just now'
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.round(h / 24)}d ago`
}
function absDate(ts?: string | null) {
  return ts ? new Date(ts).toLocaleString() : ''
}
// A browsable https link for the repo, when the URL is http(s) (SSH urls aren't linkified).
const repoLink = computed(() => {
  const u = source.value?.repo_url || ''
  return /^https?:\/\//.test(u) ? u.replace(/\.git$/, '') : ''
})
// Enabled reconcile-policy flags, shown as chips.
const policyFlags = computed(() => {
  const s = source.value
  if (!s) return [] as { label: string; icon: string; help: string }[]
  const out: { label: string; icon: string; help: string }[] = []
  if (s.prune) out.push({ label: 'Prune', icon: 'mdi-broom', help: 'Deletes managed resources removed from Git' })
  if (s.self_heal) out.push({ label: 'Self-heal', icon: 'mdi-heart-pulse', help: 'Re-applies when live state drifts from Git' })
  if (s.allow_empty) out.push({ label: 'Allow empty', icon: 'mdi-delete-sweep-outline', help: 'An empty manifest set prunes all managed resources' })
  return out
})
</script>

<template>
  <div class="detail">
    <!-- Header -->
    <div class="page-header">
      <div class="head-left">
        <router-link to="/gitops" class="btn-icon btn-icon-muted" title="Back to GitOps"><span class="mdi mdi-arrow-left"></span></router-link>
        <span class="avatar"><span class="mdi mdi-git"></span></span>
        <div class="head-title">
          <h1>{{ source?.display_name || source?.name || '…' }}</h1>
          <p v-if="source" class="subtitle">
            <a v-if="repoLink" class="repo-link mono" :href="repoLink" target="_blank" rel="noopener noreferrer">
              <span class="mdi mdi-source-repository"></span>{{ source.repo_url }}
            </a>
            <span v-else class="mono"><span class="mdi mdi-source-repository"></span>{{ source.repo_url }}</span>
            <span class="sep">·</span><span class="mono"><span class="mdi mdi-source-branch"></span>{{ source.ref }}</span>
            <span class="sep">·</span><span class="mono"><span class="mdi mdi-folder-outline"></span>{{ source.path || '.' }}</span>
          </p>
        </div>
        <span v-if="source" class="badge status-badge" :class="sourceStatusMeta[source.status].badge">
          <span class="mdi" :class="sourceStatusMeta[source.status].icon"></span> {{ sourceStatusMeta[source.status].label }}
        </span>
      </div>
      <div class="head-actions">
        <button
          class="live-toggle"
          :class="{ on: liveOn, err: liveOn && liveError }"
          :title="liveOn ? 'Live status updates are on — click to pause' : 'Live status updates paused — click to resume'"
          @click="toggleLive"
        >
          <span class="live-dot"></span>{{ liveOn ? 'Live' : 'Paused' }}
        </button>
        <button v-if="ws.canEdit" class="btn btn-secondary" title="Preview the plan before syncing" :disabled="syncing" @click="openPreview">
          <span class="mdi mdi-file-search-outline"></span> Preview
        </button>
        <button v-if="ws.canEdit" class="btn btn-secondary" :disabled="syncing" @click="sync">
          <span class="mdi" :class="syncing ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span> Sync
        </button>
        <button v-if="ws.canEdit" class="btn btn-secondary" title="Edit source"
          @click="router.push({ name: 'gitops', query: { edit: String(sourceId) } })">
          <span class="mdi mdi-pencil-outline"></span> Edit
        </button>
      </div>
    </div>

    <!-- Meta bar: operational facts at a glance -->
    <div v-if="source" class="meta-bar">
      <div class="fact">
        <span class="fact-label">Sync</span>
        <span class="badge" :class="source.sync_policy === 'auto' ? 'badge-info' : 'badge-neutral'">
          <span class="mdi" :class="source.sync_policy === 'auto' ? 'mdi-autorenew' : 'mdi-gesture-tap'"></span>
          {{ source.sync_policy === 'auto' ? 'Automatic' : 'Manual' }}
        </span>
        <span v-for="f in policyFlags" :key="f.label" class="badge badge-neutral" :title="f.help">
          <span class="mdi" :class="f.icon"></span> {{ f.label }}
        </span>
      </div>
      <span class="meta-sep"></span>
      <div class="fact">
        <span class="fact-label">Last sync</span>
        <span class="fact-value" :title="source.last_synced_subject || ''">
          <template v-if="source.last_synced_at">
            <span v-if="source.last_synced_commit" class="mono">{{ shortSha(source.last_synced_commit) }}</span>
            <span class="fact-muted"> · {{ relTime(source.last_synced_at) }}</span>
            <span v-if="source.last_synced_author" class="fact-muted"> · by {{ source.last_synced_author }}</span>
          </template>
          <span v-else class="fact-muted">never</span>
        </span>
      </div>
      <span class="meta-sep"></span>
      <div class="fact">
        <span class="fact-label">Created</span>
        <span class="fact-value" :title="absDate(source.created_at)">{{ relTime(source.created_at) }}</span>
      </div>
    </div>

    <div v-if="source?.status === 'error' && source.message" class="banner banner-danger">
      <span class="mdi mdi-alert-circle-outline"></span> {{ source.message }}
    </div>

    <!-- Degraded: the latest manifests couldn't be loaded; we're showing the last
         deployed state instead of going blank. -->
    <div v-if="topo?.error" class="banner banner-warning">
      <span class="mdi mdi-history"></span>
      <span><strong>Showing last-synced state.</strong> The latest revision could not be loaded: <span class="mono">{{ topo.error }}</span></span>
    </div>

    <!-- Status legend — click a chip to filter the graph by that status -->
    <div v-if="nodeCount" class="legend">
      <span class="legend-total">{{ nodeCount }} resource{{ nodeCount === 1 ? '' : 's' }}</span>
      <button
        v-for="c in visibleCounts"
        :key="c.status"
        type="button"
        class="legend-item legend-chip"
        :class="{ active: statusFilter.has(c.status), faded: statusFilter.size > 0 && !statusFilter.has(c.status) }"
        :title="`Show only ${nodeStatusMeta[c.status].label.toLowerCase()}`"
        @click="toggleStatusFilter(c.status)"
      >
        <span class="dot" :style="{ background: nodeStatusMeta[c.status].color }"></span>
        {{ c.n }} {{ nodeStatusMeta[c.status].label.toLowerCase() }}
      </button>
      <button v-if="statusFilter.size" type="button" class="legend-clear" @click="statusFilter = new Set()">
        <span class="mdi mdi-close"></span> clear filter
      </button>
    </div>

    <!-- Graph + side panel -->
    <div class="graph-wrap card">
      <div v-if="loading" class="graph-placeholder"><span class="spinner"></span></div>
      <div v-else-if="!nodeCount" class="empty-state">
        <span class="mdi mdi-graph-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No resources</h3>
        <p>This source has no <code>miabi.io/v1</code> resources at <code>{{ source?.ref }}/{{ source?.path }}</code>, or it hasn't been synced yet.</p>
      </div>
      <VueFlow
        v-else
        :default-viewport="{ zoom: 1 }"
        :min-zoom="0.2"
        :max-zoom="2"
        :nodes-draggable="true"
        :nodes-connectable="false"
        :elements-selectable="true"
        fit-view-on-init
        @node-click="onNodeClick"
        @pane-click="selectedKey = null"
      >
        <template #node-project="props">
          <ProjectNode :data="props.data" />
        </template>
        <template #node-resource="props">
          <ResourceNode :data="props.data" :selected="props.id === selectedKey" />
        </template>
        <Background pattern-color="var(--border-primary)" :gap="18" />
        <Controls :show-interactive="false" />
      </VueFlow>

      <!-- Side panel -->
      <transition name="slide">
        <aside v-if="selectedNode" class="side" :class="{ 'side-wide': wideDrawer }">
          <div class="side-head">
            <span class="side-icon" :style="{ color: nodeStatusMeta[selectedNode.status].color }">
              <span class="mdi" :class="kindOf(selectedNode.kind).icon"></span>
            </span>
            <div class="side-title">
              <span class="side-name">{{ selectedNode.name }}</span>
              <span class="side-kind">{{ kindOf(selectedNode.kind).label }}</span>
            </div>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="selectedKey = null"><span class="mdi mdi-close"></span></button>
          </div>

          <div v-if="drawerTabs.length > 1" class="side-tabs" role="tablist">
            <button
              v-for="t in drawerTabs"
              :key="t.key"
              class="side-tab"
              :class="{ active: activeTab === t.key }"
              role="tab"
              :aria-selected="activeTab === t.key"
              @click="activeTab = t.key"
            >
              {{ t.label }}
            </button>
          </div>

          <!-- Overview tab -->
          <div v-if="activeTab === 'overview'" class="side-body">
            <div class="side-row">
              <span class="side-label">Sync status</span>
              <span class="badge" :class="nodeStatusMeta[selectedNode.status].badge">
                <span class="mdi" :class="nodeStatusMeta[selectedNode.status].icon"></span>
                {{ nodeStatusMeta[selectedNode.status].label }}
              </span>
            </div>
            <div v-if="selectedNode.health" class="side-row">
              <span class="side-label">Health</span>
              <span class="badge badge-neutral">{{ selectedNode.health }}</span>
            </div>

            <!-- Public URL (routes) — open the live service from here -->
            <div v-if="selectedNode.url" class="side-section">
              <span class="side-label">URL</span>
              <a class="side-url" :href="selectedNode.url" target="_blank" rel="noopener noreferrer">
                <span class="mdi mdi-open-in-new"></span>
                <span class="side-url-text">{{ selectedNode.url }}</span>
              </a>
            </div>

            <!-- Field diffs from the plan -->
            <div v-if="selectedChange?.fields?.length" class="side-section">
              <span class="side-label">Pending changes</span>
              <table class="diff-fields">
                <tr v-for="(f, j) in selectedChange.fields" :key="j">
                  <td class="mono diff-field">{{ f.field }}</td>
                  <td class="mono diff-from">{{ f.from || '∅' }}</td>
                  <td class="diff-arrow"><span class="mdi mdi-arrow-right"></span></td>
                  <td class="mono diff-to">{{ f.to || '∅' }}</td>
                </tr>
              </table>
            </div>

            <!-- Relationships -->
            <div v-if="selectedDeps.out.length" class="side-section">
              <span class="side-label">Depends on</span>
              <div v-for="(d, i) in selectedDeps.out" :key="'o' + i" class="rel">
                <span class="rel-verb">{{ edgeLabel[d.type as keyof typeof edgeLabel] }}</span>
                <span class="rel-target mono">{{ nameOf(d.key) }}</span>
              </div>
            </div>
            <div v-if="selectedDeps.in.length" class="side-section">
              <span class="side-label">Used by</span>
              <div v-for="(d, i) in selectedDeps.in" :key="'i' + i" class="rel">
                <span class="rel-target mono">{{ nameOf(d.key) }}</span>
                <span class="rel-verb">{{ edgeLabel[d.type as keyof typeof edgeLabel] }} this</span>
              </div>
            </div>
          </div>

          <!-- Events tab -->
          <div v-else-if="activeTab === 'events'" class="side-body side-scroll">
            <div v-if="eventsLoading && drawerEvents.length === 0" class="drawer-empty"><span class="spinner"></span></div>
            <div v-else-if="drawerEvents.length === 0" class="drawer-empty">
              <span class="mdi mdi-timeline-text-outline"></span>
              <p>No events yet.</p>
            </div>
            <template v-else>
              <ul class="timeline">
                <li v-for="e in drawerEvents" :key="e.id" class="event">
                  <span class="event-icon" :class="`sev-${e.severity}`"><span class="mdi" :class="eventIcon(e.type)"></span></span>
                  <div class="event-body">
                    <div class="event-row">
                      <span class="event-msg">{{ e.message || e.type }}</span>
                      <span class="event-time">{{ relTime(e.created_at) }}</span>
                    </div>
                    <span class="event-type">{{ e.type }}</span>
                  </div>
                </li>
              </ul>
              <div v-if="eventsHasMore" class="text-center" style="padding: 8px 0 2px">
                <button class="btn btn-secondary btn-sm" :disabled="loadingMoreEvents" @click="loadMoreEvents">
                  {{ loadingMoreEvents ? 'Loading…' : 'Load more' }}
                </button>
              </div>
            </template>
          </div>

          <!-- Logs tab -->
          <div v-else class="side-body side-logs">
            <LogViewer
              :lines="drawerLogs"
              :streaming="logsConnected"
              :status-label="logsConnected ? 'Live' : 'Disconnected'"
              :status-class="logsConnected ? 'badge-success' : 'badge-neutral'"
              :download-name="`${selectedNode.name}-logs`"
              placeholder="Waiting for log output…"
            />
          </div>

          <div class="side-foot">
            <div v-if="ws.canEdit" class="side-actions">
              <button class="btn btn-secondary" :disabled="resourceBusy" @click="syncResource">
                <span class="mdi" :class="resourceBusy ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span> Sync
              </button>
              <button class="btn btn-danger" :disabled="resourceBusy" @click="confirmDelete = true">
                <span class="mdi mdi-delete-outline"></span> Delete
              </button>
            </div>
            <button class="btn btn-primary btn-block" :disabled="!resourceRoute(selectedNode.kind, selectedNode.live_id)" @click="openResource(selectedNode)">
              <span class="mdi mdi-open-in-new"></span> Open detail
            </button>
          </div>
        </aside>
      </transition>
    </div>

    <ConfirmDialog
      :open="confirmDelete"
      title="Delete this resource?"
      :message="deleteMessage"
      confirm-label="Delete"
      variant="danger"
      :busy="resourceBusy"
      @confirm="deleteResource"
      @cancel="confirmDelete = false"
    />

    <!-- Preview & sync -->
    <div v-if="previewOpen" class="modal-overlay" @click.self="previewOpen = false">
      <div class="preview-modal">
        <div class="preview-head">
          <h3>Sync preview</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="previewOpen = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="preview-body">
          <div v-if="previewLoading" class="drawer-empty"><span class="spinner"></span></div>
          <div v-else-if="previewChanges.length === 0" class="drawer-empty">
            <span class="mdi mdi-check-circle-outline"></span>
            <p>In sync — nothing to apply.</p>
          </div>
          <ul v-else class="preview-list">
            <li v-for="(c, i) in previewChanges" :key="i" class="preview-change">
              <span class="badge" :class="planActionMeta(c.action).badge">{{ planActionMeta(c.action).label }}</span>
              <span class="preview-kind">{{ c.kind }}</span>
              <span class="preview-name mono">{{ c.name }}</span>
            </li>
          </ul>
        </div>
        <div class="preview-foot">
          <button class="btn btn-secondary" @click="previewOpen = false">Cancel</button>
          <button class="btn btn-primary" :disabled="syncing || previewLoading" @click="previewAndSync">
            <span class="mdi" :class="syncing ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
            Sync<span v-if="previewChanges.length"> ({{ previewChanges.length }})</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.head-left { display: flex; align-items: center; gap: 12px; min-width: 0; }
.head-left h1 { margin: 0; }
.head-actions { display: flex; align-items: center; gap: 8px; }
.head-title { min-width: 0; }
.subtitle { font-size: 12px; color: var(--text-muted); margin: 3px 0 0; display: flex; align-items: center; flex-wrap: wrap; }
.subtitle .mdi { font-size: 13px; margin-right: 3px; opacity: 0.7; }
.subtitle .sep { margin: 0 7px; opacity: 0.6; }
.repo-link { color: var(--primary-600, #2563eb); text-decoration: none; }
.repo-link:hover { text-decoration: underline; }
.status-badge { margin-left: 8px; align-self: center; }
.mono { font-family: 'JetBrains Mono', monospace; }

/* Meta bar */
.meta-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px 14px;
  padding: 10px 16px;
  margin: 4px 0 16px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: 10px;
}
.fact { display: inline-flex; align-items: center; gap: 8px; }
.fact-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.fact-value { font-size: 13px; color: var(--text-primary); }
.fact-muted { color: var(--text-muted); }
.fact .badge + .badge { margin-left: 4px; }
.meta-sep { width: 1px; height: 18px; background: var(--border-primary); }
@media (max-width: 640px) { .meta-sep { display: none; } }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.badge .mdi { font-size: 13px; }
.live-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  padding: 5px 10px;
  border-radius: 6px;
  border: 1px solid var(--border-primary);
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
}
.live-toggle .live-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--text-muted); }
.live-toggle.on { color: var(--success-700, var(--success-600)); border-color: color-mix(in srgb, var(--success-600) 40%, var(--border-primary)); }
.live-toggle.on .live-dot { background: var(--success-600); animation: live-blink 1.6s ease-in-out infinite; }
.live-toggle.err.on { color: var(--warning-700, var(--warning-600)); }
.live-toggle.err.on .live-dot { background: var(--warning-600); }
@keyframes live-blink { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
.banner { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: 8px; font-size: 13px; margin-bottom: 14px; }
.banner-danger { background: color-mix(in srgb, var(--danger-600) 10%, transparent); color: var(--danger-600); }
.banner-warning { background: color-mix(in srgb, var(--warning-600) 12%, transparent); color: var(--warning-700, var(--warning-600)); }
.banner-warning .mono { word-break: break-all; }

.legend { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; font-size: 12px; color: var(--text-muted); flex-wrap: wrap; }
.legend-total { font-weight: 600; color: var(--text-primary); }
.legend-item { display: inline-flex; align-items: center; gap: 6px; }
.legend-item .dot { width: 9px; height: 9px; border-radius: 50%; }
/* Clickable filter chips. */
.legend-chip {
  appearance: none; border: 1px solid transparent; background: none; cursor: pointer;
  color: inherit; font-size: 12px; padding: 3px 8px; border-radius: 999px; transition: background 0.12s, border-color 0.12s, opacity 0.12s;
}
.legend-chip:hover { background: var(--bg-secondary); }
.legend-chip.active { border-color: var(--border-primary); background: var(--bg-secondary); color: var(--text-primary); font-weight: 600; }
.legend-chip.faded { opacity: 0.5; }
.legend-clear {
  appearance: none; border: none; background: none; cursor: pointer; color: var(--text-muted);
  font-size: 12px; display: inline-flex; align-items: center; gap: 3px;
}
.legend-clear:hover { color: var(--text-primary); }

/* Preview & sync modal. */
.modal-overlay {
  position: fixed; inset: 0; z-index: 50; display: flex; align-items: center; justify-content: center;
  background: rgba(0, 0, 0, 0.45); padding: 24px;
}
.preview-modal {
  width: 100%; max-width: 560px; max-height: 80vh; display: flex; flex-direction: column;
  background: var(--bg-primary); border: 1px solid var(--border-primary); border-radius: 12px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.25);
}
.preview-head { display: flex; align-items: center; justify-content: space-between; padding: 14px 16px; border-bottom: 1px solid var(--border-primary); }
.preview-head h3 { margin: 0; font-size: 15px; }
.preview-body { padding: 8px 16px; overflow-y: auto; flex: 1; }
.preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
.preview-change { display: flex; align-items: center; gap: 10px; padding: 8px 0; border-bottom: 1px solid var(--border-primary); }
.preview-change:last-child { border-bottom: none; }
.preview-change .badge { flex-shrink: 0; }
.preview-kind { font-size: 12px; color: var(--text-muted); }
.preview-name { font-size: 13px; color: var(--text-primary); word-break: break-all; }
.preview-foot { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--border-primary); }

.graph-wrap { position: relative; height: calc(100vh - 230px); min-height: 460px; padding: 0; overflow: hidden; }
.graph-placeholder { display: grid; place-items: center; height: 100%; }
.empty-state { display: grid; place-items: center; height: 100%; text-align: center; }

/* Side panel */
.side {
  position: absolute;
  top: 0; right: 0; bottom: 0;
  width: 340px;
  background: var(--bg-primary);
  border-left: 1px solid var(--border-primary);
  display: flex;
  flex-direction: column;
  box-shadow: -4px 0 16px rgba(0, 0, 0, 0.06);
  z-index: 5;
  transition: width 0.15s ease;
}
/* Data-heavy tabs (Events/Logs) get more room. */
.side-wide { width: clamp(420px, 46vw, 640px); }

.side-tabs {
  display: flex;
  gap: 2px;
  padding: 0 8px;
  border-bottom: 1px solid var(--border-primary);
  flex-shrink: 0;
}
.side-tab {
  appearance: none;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  padding: 9px 10px;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
}
.side-tab:hover { color: var(--text-primary); }
.side-tab.active { color: var(--primary-600, #2563eb); border-bottom-color: var(--primary-600, #2563eb); font-weight: 600; }

.side-scroll { overflow-y: auto; }
.side-logs { padding: 12px; overflow: hidden; }
.drawer-empty {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 8px; padding: 32px 12px; color: var(--text-muted); text-align: center; flex: 1;
}
.drawer-empty .mdi { font-size: 36px; }

/* Event timeline (compact form of the app-detail timeline). */
.timeline { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.event { display: flex; gap: 10px; }
.event-icon {
  flex-shrink: 0; width: 26px; height: 26px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--bg-secondary); color: var(--text-muted); font-size: 15px;
}
.event-icon.sev-warning { color: var(--warning-600, #b45309); background: var(--warning-50, #fffbeb); }
.event-icon.sev-error { color: var(--danger-600, #dc2626); background: var(--danger-50, #fef2f2); }
.event-body { min-width: 0; flex: 1; }
.event-row { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.event-msg { font-size: 13px; color: var(--text-primary); word-break: break-word; }
.event-time { font-size: 11px; color: var(--text-muted); white-space: nowrap; }
.event-type { font-size: 11px; color: var(--text-muted); font-family: monospace; }
.side-head { display: flex; align-items: center; gap: 10px; padding: 14px; border-bottom: 1px solid var(--border-primary); }
.side-icon { font-size: 22px; }
.side-title { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.side-name { font-weight: 600; font-size: 14px; word-break: break-all; }
.side-kind { font-size: 11px; color: var(--text-muted); }
.side-body { padding: 14px; overflow-y: auto; flex: 1; display: flex; flex-direction: column; gap: 14px; }
.side-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.side-section { display: flex; flex-direction: column; gap: 8px; }
.side-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.side-foot { padding: 12px 14px; border-top: 1px solid var(--border-primary); display: flex; flex-direction: column; gap: 8px; }
.side-actions { display: flex; gap: 8px; }
.side-actions .btn { flex: 1; display: inline-flex; align-items: center; justify-content: center; gap: 6px; }
.btn-block { width: 100%; justify-content: center; }
.side-url {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--primary-600, #2563eb);
  text-decoration: none;
  word-break: break-all;
}
.side-url:hover { text-decoration: underline; }
.side-url .mdi { flex-shrink: 0; font-size: 14px; }
.rel { display: flex; align-items: baseline; gap: 8px; font-size: 12px; }
.rel-verb { color: var(--text-muted); white-space: nowrap; }
.rel-target { font-weight: 600; word-break: break-all; }

.diff-fields { width: 100%; border-collapse: collapse; }
.diff-fields td { padding: 3px 5px; font-size: 11px; vertical-align: top; }
.diff-field { color: var(--text-muted); white-space: nowrap; }
.diff-from { color: var(--danger-600); }
.diff-to { color: var(--success-600); }
.diff-arrow { color: var(--text-muted); width: 18px; text-align: center; }

.slide-enter-active, .slide-leave-active { transition: transform 0.18s ease; }
.slide-enter-from, .slide-leave-to { transform: translateX(100%); }

:deep(.vue-flow__edge-text) { font-family: 'JetBrains Mono', monospace; }
:deep(.vue-flow__controls) { box-shadow: 0 1px 4px rgba(0, 0, 0, 0.12); border-radius: 6px; overflow: hidden; }
</style>
