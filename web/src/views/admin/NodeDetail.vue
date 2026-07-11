<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { nodesApi, type CreateNodePayload, type NodeWorkloads, type NodeGPUList, type GPUDevice } from '@/api/nodes'
import { clusterApi } from '@/api/cluster'
import { ACCESS_MODES, CONNECTIVITY_TYPES, nodeOptionDescription } from '@/constants/node'
import type { Server, NodeStats, NodeHostMetrics, GatewayStatus, GatewayCandidate, GatewayUpdateProgress, StatsSample, Container, ContainerStat, DockerVolume, DockerNetwork, NodePortUsage, ClusterMember } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import FieldInfo from '@/components/FieldInfo.vue'
import { copyText } from '@/utils/clipboard'

// Managed gateways are named mb-node-gateway, but an imported gateway keeps its
// original container name (returned as gateway.container) — use that for stats.
const GATEWAY_CONTAINER = 'mb-node-gateway'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const id = Number(route.params.id)

const node = ref<Server | null>(null)
const stats = ref<NodeStats | null>(null)
const gateway = ref<GatewayStatus | null>(null)
const gwBusy = ref(false)
const gwStats = ref<StatsSample | null>(null)
const loading = ref(false)
const offline = ref(false)

// --- Live gateway container stats (CPU/mem/net) ---
let statsES: EventSource | null = null
function stopGatewayStats() {
  statsES?.close()
  statsES = null
  gwStats.value = null
}
function startGatewayStats() {
  stopGatewayStats()
  // Use the tracked container name (imported gateways are not mb-node-gateway).
  const cid = gateway.value?.container || GATEWAY_CONTAINER
  statsES = new EventSource(nodesApi.containerStatsUrl(id, cid))
  statsES.onmessage = (ev) => {
    try { gwStats.value = JSON.parse(ev.data) as StatsSample } catch { /* ignore */ }
  }
  statsES.onerror = () => stopGatewayStats()
}
// Stream stats only while the gateway is running; restart if the tracked
// container name changes (e.g. after an import). Tear down otherwise.
watch(() => [gateway.value?.running, gateway.value?.container], ([running]) => {
  if (running) startGatewayStats()
  else stopGatewayStats()
})
onBeforeUnmount(stopGatewayStats)
onBeforeUnmount(stopGatewayEvents)

const connected = computed(() => !!node.value && (node.value.is_local || !!node.value.agent_connected))
const isEdge = computed(() => node.value?.connectivity === 'edge-gateway')

// Cluster membership (docker node ls) — shown on the manager's page only, since
// the manager is the swarm's source of truth. Includes unmanaged members.
const clusterMembers = ref<ClusterMember[]>([])
async function loadClusterMembers() {
  try {
    clusterMembers.value = (await clusterApi.members()).data.data ?? []
  } catch { clusterMembers.value = [] }
}

// --- Header/overview helpers ---
const roleLabel = computed(() => node.value?.role || (node.value?.is_local ? 'manager' : 'node'))
const statusLabel = computed(() =>
  node.value?.is_local ? 'Manager node' : node.value?.agent_connected ? 'Online' : 'Offline',
)
const accessModeLabel = computed(() => {
  const m = node.value?.access_mode
  return m === 'api' ? 'Docker API' : m === 'socket' ? 'Local socket' : 'Agent'
})
const containerPct = computed(() => {
  const s = stats.value
  if (!s || !s.containers) return 0
  return Math.round((s.containers_running / s.containers) * 100)
})
function fmtDateTime(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

// --- Cluster membership helpers ---
function memberRole(m: ClusterMember): string {
  return m.leader ? 'leader' : m.role || 'worker'
}
function memberRoleClass(m: ClusterMember): string {
  return m.leader ? 'badge-info' : 'badge-muted'
}
function memberStateClass(m: ClusterMember): string {
  return m.state === 'ready' ? 'badge-success badge-dot' : 'badge-warning'
}

async function load() {
  loading.value = true
  offline.value = false
  try {
    const list = (await nodesApi.list()).data.data ?? []
    node.value = list.find((n) => n.id === id) ?? null
    if (!node.value) {
      notify.error('Node not found')
      return
    }
    // The manager is the swarm authority; show the full cluster membership here.
    if (node.value.is_local) await loadClusterMembers()
    if (connected.value) {
      try {
        stats.value = (await nodesApi.stats(id)).data.data
        await loadResources()
        await loadGpus()
        startStatsPolling()
      } catch {
        offline.value = true
      }
      if (isEdge.value) {
        await loadGateway()
        gwUpdate.value = gateway.value?.update ?? null
        startGatewayEvents()
      }
    } else {
      offline.value = true
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// --- GPUs ---
const gpuInfo = ref<NodeGPUList | null>(null)
const gpuBusy = ref<number | null>(null) // device id with an action in flight
const gpuRescanning = ref(false)

async function loadGpus() {
  try {
    gpuInfo.value = (await nodesApi.gpus(id)).data.data
  } catch {
    gpuInfo.value = null
  }
}
async function setGpu(dev: GPUDevice, patch: { enabled?: boolean; shared?: boolean }) {
  gpuBusy.value = dev.id
  try {
    const updated = (await nodesApi.updateGpu(id, dev.id, patch)).data.data
    if (gpuInfo.value) {
      gpuInfo.value.devices = gpuInfo.value.devices.map((d) => (d.id === updated.id ? updated : d))
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    gpuBusy.value = null
  }
}
async function rescanGpus() {
  gpuRescanning.value = true
  try {
    gpuInfo.value = (await nodesApi.rescanGpus(id)).data.data
    notify.success('GPU rescan complete')
  } catch (e) {
    notify.apiError(e)
  } finally {
    gpuRescanning.value = false
  }
}

// --- Docker resources (containers / volumes / networks) ---
const containers = ref<Container[]>([])
const volumes = ref<DockerVolume[]>([])
const networks = ref<DockerNetwork[]>([])
const nodePorts = ref<NodePortUsage[]>([])
const cstats = ref<Record<string, ContainerStat>>({})
const netRate = ref<Record<string, number>>({}) // bytes/s per container id
const statsUpdatedAt = ref<number>(0)
const cbusy = ref('') // container id with an action in flight
const showAllContainers = ref(true)
let prevNet: Record<string, { bytes: number; t: number }> = {}
let statsTimer: number | undefined

async function loadResources() {
  const [conts, vols, nets, ports] = await Promise.all([
    nodesApi.containers(id, showAllContainers.value),
    nodesApi.volumes(id),
    nodesApi.networks(id),
    nodesApi.ports(id),
  ])
  containers.value = conts.data.data ?? []
  volumes.value = vols.data.data ?? []
  networks.value = nets.data.data ?? []
  nodePorts.value = ports.data.data ?? []
}

function startStatsPolling() {
  stopStatsPolling()
  pollStats()
  statsTimer = window.setInterval(pollStats, 4000)
}
function stopStatsPolling() {
  if (statsTimer) { clearInterval(statsTimer); statsTimer = undefined }
}
async function pollStats() {
  try {
    const list = (await nodesApi.containersStats(id)).data.data ?? []
    const now = Date.now()
    const map: Record<string, ContainerStat> = {}
    const rate: Record<string, number> = {}
    for (const s of list) {
      map[s.id] = s
      const total = (s.network_rx_bytes || 0) + (s.network_tx_bytes || 0)
      const prev = prevNet[s.id]
      if (prev && now > prev.t) rate[s.id] = Math.max(0, (total - prev.bytes) / ((now - prev.t) / 1000))
      prevNet[s.id] = { bytes: total, t: now }
    }
    cstats.value = map
    netRate.value = rate
    statsUpdatedAt.value = now
  } catch { /* node may have gone offline */ }
  // Real host metrics (local node only); leaves the container-aggregate fallback
  // in place when unavailable.
  try {
    hostMetrics.value = (await nodesApi.hostMetrics(id)).data.data
  } catch { /* host metrics unavailable */ }
}
onBeforeUnmount(stopStatsPolling)

// Container actions
async function containerAction(c: Container, fn: 'stopContainer' | 'restartContainer', label: string) {
  cbusy.value = c.id
  try {
    await nodesApi[fn](id, c.id)
    notify.success(label)
    await loadResources()
  } catch (e) { notify.apiError(e) } finally { cbusy.value = '' }
}
const toRemoveContainer = ref<Container | null>(null)
async function removeContainer() {
  if (!toRemoveContainer.value) return
  cbusy.value = toRemoveContainer.value.id
  try {
    await nodesApi.removeContainer(id, toRemoveContainer.value.id)
    notify.success('Container removed')
    toRemoveContainer.value = null
    await loadResources()
  } catch (e) { notify.apiError(e) } finally { cbusy.value = '' }
}

// Volume actions
const newVolume = ref('')
async function createVolume() {
  if (!newVolume.value.trim()) return
  try {
    await nodesApi.createVolume(id, newVolume.value.trim())
    newVolume.value = ''
    notify.success('Volume created')
    await loadResources()
  } catch (e) { notify.apiError(e) }
}
async function removeVolume(v: DockerVolume) {
  try {
    await nodesApi.removeVolume(id, v.name)
    notify.success('Volume removed')
    await loadResources()
  } catch (e) { notify.apiError(e) }
}

// Network actions
const newNetwork = ref('')
async function createNetwork() {
  if (!newNetwork.value.trim()) return
  try {
    await nodesApi.createNetwork(id, newNetwork.value.trim())
    newNetwork.value = ''
    notify.success('Network created')
    await loadResources()
  } catch (e) { notify.apiError(e) }
}
async function removeNetwork(n: DockerNetwork) {
  try {
    await nodesApi.removeNetwork(id, n.name)
    notify.success('Network removed')
    await loadResources()
  } catch (e) { notify.apiError(e) }
}

// Live stats for Miabi's own runtime container on this node (manager/agent),
// picked from the polled container-stats by the id the backend reports.
const selfStat = computed(() =>
  stats.value?.self_container ? cstats.value[stats.value.self_container.id] : undefined,
)

// Real host CPU/memory for the local node, read from procfs by the backend.
// available is false for remote nodes (or when no procfs is readable).
const hostMetrics = ref<NodeHostMetrics | null>(null)

// Node resource usage: aggregated live from the running containers' stats we
// already poll, against the node's total cores/memory. cpu_percent is Docker-CLI
// style (100% = one core), so dividing the sum by the core count gives node
// utilization. Null until container stats arrive (or none are running).
const nodeUsage = computed(() => {
  const list = Object.values(cstats.value)
  if (!list.length || !stats.value) return null
  let cpu = 0
  let mem = 0
  for (const s of list) {
    cpu += s.cpu_percent || 0
    mem += s.memory_usage_bytes || 0
  }
  const cores = stats.value.cpus || 0
  const memTotal = stats.value.mem_total || 0
  return {
    containers: list.length,
    cpuCores: cpu / 100,
    cpuPercent: cores > 0 ? Math.min(100, cpu / cores) : Math.min(100, cpu),
    memUsed: mem,
    memPercent: memTotal > 0 ? Math.min(100, (mem / memTotal) * 100) : 0,
  }
})

// Unified resource usage for the card: prefer real host metrics (procfs) when the
// node reports them, otherwise fall back to the container-stats aggregate.
const usage = computed(() => {
  const h = hostMetrics.value
  if (h?.available) {
    return {
      source: 'host' as const,
      cpuPercent: Math.min(100, h.cpu_percent),
      memUsed: h.mem_used_bytes,
      memTotal: h.mem_total_bytes,
      memPercent: Math.min(100, h.mem_percent),
      cpuSub: 'Real host usage',
      memSub: 'Real host usage',
    }
  }
  const n = nodeUsage.value
  if (n && stats.value) {
    return {
      source: 'containers' as const,
      cpuPercent: n.cpuPercent,
      memUsed: n.memUsed,
      memTotal: stats.value.mem_total || 0,
      memPercent: n.memPercent,
      cpuSub: n.cpuCores.toFixed(2) + ' of ' + stats.value.cpus + ' cores · ' + n.containers + ' containers',
      memSub: n.memPercent.toFixed(0) + '% across ' + n.containers + ' containers',
    }
  }
  return null
})

// Helpers
function cname(c: Container) { return ((c.names && c.names[0]) || c.id).replace(/^\//, '') }
function isManaged(labels?: Record<string, string>) {
  return Object.keys(labels ?? {}).some((k) => k.startsWith('miabi.'))
}
function fmtPorts(c: Container): string {
  const ps = (c.ports || []).filter((p) => p.public_port).map((p) => p.public_port)
  return ps.length ? Array.from(new Set(ps)).join(', ') : '—'
}
function rate(c: Container): string {
  const r = netRate.value[c.id]
  return r === undefined ? '—' : fmtSize(r) + '/s'
}
function healthClass(h?: string): string {
  if (h === 'healthy') return 'badge-success badge-dot'
  if (h === 'unhealthy') return 'badge-danger badge-dot'
  if (h === 'starting') return 'badge-warning badge-dot'
  return 'badge-neutral'
}

async function loadGateway() {
  try {
    gateway.value = (await nodesApi.gateway(id)).data.data
  } catch { /* offline */ }
}

async function deployGateway() {
  gwBusy.value = true
  try {
    await nodesApi.deployGateway(id)
    notify.success('Gateway installed')
    await loadGateway()
  } catch (e) { notify.apiError(e) } finally { gwBusy.value = false }
}

// --- Safe gateway update (test container → observe → promote) ---
// The gateway is the node's single ingress, so an update is rolled out by first
// validating the new image as a throwaway test container, then promoting it.
// Progress streams over SSE and is mirrored to gateway.update.
const gwUpdate = ref<GatewayUpdateProgress | null>(null)
const GW_PHASE_LABELS: Record<string, string> = {
  queued: 'Queued',
  pulling: 'Pulling new image',
  testing: 'Starting test container',
  observing: 'Observing test container',
  promoting: 'Promoting to live',
  verifying: 'Verifying',
  done: 'Done',
}
const GW_UPDATE_STEPS = ['pulling', 'testing', 'observing', 'promoting', 'verifying']
const gwUpdating = computed(() => {
  const p = gwUpdate.value?.phase
  return !!p && p !== 'done' && p !== 'failed'
})
function gwPhaseLabel(phase: string): string {
  return GW_PHASE_LABELS[phase] ?? phase
}
function gwPhaseState(phase: string): 'done' | 'current' | 'todo' {
  const cur = gwUpdate.value?.phase ?? ''
  if (cur === 'done') return 'done'
  const ci = GW_UPDATE_STEPS.indexOf(cur)
  const pi = GW_UPDATE_STEPS.indexOf(phase)
  if (ci < 0 || pi < 0) return 'todo'
  if (pi < ci) return 'done'
  return pi === ci ? 'current' : 'todo'
}

let gwUpdateNotified = false
let gwEventsES: EventSource | null = null
function stopGatewayEvents() {
  gwEventsES?.close()
  gwEventsES = null
}
function applyGatewayUpdate(u: GatewayUpdateProgress | null) {
  gwUpdate.value = u
  if (gateway.value) gateway.value.update = u ?? undefined
  if (u?.phase === 'failed' && !gwUpdateNotified) {
    gwUpdateNotified = true
    notify.error(`Gateway update failed: ${u.error ?? 'unknown error'}`)
    void loadGateway()
  } else if (u?.phase === 'done' && !gwUpdateNotified) {
    gwUpdateNotified = true
    notify.success('Gateway updated')
    void loadGateway()
  }
}
function startGatewayEvents() {
  stopGatewayEvents()
  gwEventsES = new EventSource(nodesApi.gatewayEventsUrl(id))
  gwEventsES.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as { type?: string; data?: { update?: GatewayUpdateProgress } }
      if (msg.type === 'status') applyGatewayUpdate(msg.data?.update ?? null)
    } catch { /* ignore */ }
  }
  gwEventsES.onerror = () => { /* keep the last known state; EventSource auto-reconnects */ }
}
async function updateGateway() {
  gwBusy.value = true
  gwUpdateNotified = false
  try {
    gwUpdate.value = (await nodesApi.updateGateway(id)).data.data
    notify.success('Safe update started — testing the new image before promoting')
  } catch (e) { notify.apiError(e) } finally { gwBusy.value = false }
}

// --- Import an existing gateway (adopt a running Goma container) ---
const showImportGw = ref(false)
const gwCandidates = ref<GatewayCandidate[]>([])
const gwCandidate = ref('') // selected container id ('' = auto-detect)
const gwImporting = ref(false)
const gwCandidatesLoading = ref(false)
async function openImportGateway() {
  showImportGw.value = true
  gwCandidate.value = ''
  gwCandidates.value = []
  gwCandidatesLoading.value = true
  try {
    gwCandidates.value = (await nodesApi.gatewayCandidates(id)).data.data ?? []
    if (gwCandidates.value.length === 1) gwCandidate.value = gwCandidates.value[0].id
  } catch (e) { notify.apiError(e) } finally { gwCandidatesLoading.value = false }
}
async function importGateway() {
  gwImporting.value = true
  try {
    await nodesApi.importGateway(id, gwCandidate.value || undefined)
    notify.success('Gateway imported')
    showImportGw.value = false
    await load()
  } catch (e) { notify.apiError(e) } finally { gwImporting.value = false }
}

// --- Gateway image override ---
const gwImage = ref('')
const gwImgSaving = ref(false)
// Keep the input in sync with the stored override as the gateway state loads.
watch(() => gateway.value?.image_override, (v) => { gwImage.value = v ?? '' })
async function saveImage() {
  gwImgSaving.value = true
  try {
    await nodesApi.updateGatewayImage(id, gwImage.value.trim())
    notify.success('Gateway image set — redeploy to apply')
    await loadGateway()
  } catch (e) { notify.apiError(e) } finally { gwImgSaving.value = false }
}

// --- Gateway config editor ---
const showConfig = ref(false)
const cfgText = ref('')
const cfgDefault = ref('')
const cfgSaving = ref(false)
async function openConfig() {
  try {
    const c = (await nodesApi.gatewayConfig(id)).data.data
    cfgText.value = c.config
    cfgDefault.value = c.default
    showConfig.value = true
  } catch (e) { notify.apiError(e) }
}
async function saveConfig() {
  cfgSaving.value = true
  try {
    await nodesApi.updateGatewayConfig(id, cfgText.value)
    notify.success('Gateway config saved — redeploy to apply')
    showConfig.value = false
  } catch (e) { notify.apiError(e) } finally { cfgSaving.value = false }
}

const showTeardown = ref(false)
async function teardownGateway() {
  gwBusy.value = true
  try {
    await nodesApi.teardownGateway(id)
    notify.success('Gateway torn down')
    showTeardown.value = false
    await loadGateway()
  } catch (e) { notify.apiError(e) } finally { gwBusy.value = false }
}

async function toggleCordon() {
  if (!node.value) return
  try {
    if (node.value.cordoned) await nodesApi.uncordon(id)
    else await nodesApi.cordon(id)
    notify.success(node.value.cordoned ? 'Node uncordoned' : 'Node cordoned')
    load()
  } catch (e) {
    notify.apiError(e)
  }
}

const regenToken = ref<string | null>(null)
async function regenerate() {
  try {
    regenToken.value = (await nodesApi.regenerateToken(id)).data.data.token
  } catch (e) {
    notify.apiError(e)
  }
}

// --- Edit node ---
const showEdit = ref(false)
const editSaving = ref(false)
const editForm = ref<CreateNodePayload>({
  name: '', address: '', public_ip: '', public_hostname: '', connectivity: 'port-forward', access_mode: 'agent',
  docker_endpoint: '', tls_ca_cert: '', tls_cert: '', tls_key: '',
})
function openEdit() {
  if (!node.value) return
  const n = node.value
  // Seed every field from the node — the Edit modal only exposes the safe
  // metadata (name / public addresses); the reachability fields ride along
  // unchanged so this save never resets them. Credentials are write-only
  // (never returned), so they stay blank = "keep".
  editForm.value = {
    name: n.name, address: n.address || '', public_ip: n.public_ip || '', public_hostname: n.public_hostname || '',
    connectivity: n.connectivity || 'port-forward',
    access_mode: n.access_mode || 'agent', docker_endpoint: n.docker_endpoint || '',
    tls_ca_cert: '', tls_cert: '', tls_key: '',
  }
  showEdit.value = true
}
async function submitEdit() {
  if (!editForm.value.name.trim()) return
  const mode = editForm.value.access_mode || 'agent'
  if (mode === 'api' && !editForm.value.docker_endpoint?.trim()) {
    notify.error('A Docker endpoint is required for this access mode')
    return
  }
  editSaving.value = true
  const payload: CreateNodePayload = {
    name: editForm.value.name.trim(),
    address: editForm.value.address?.trim() || undefined,
    public_ip: editForm.value.public_ip?.trim() || undefined,
    public_hostname: editForm.value.public_hostname?.trim() || undefined,
    connectivity: editForm.value.connectivity,
    access_mode: mode,
    docker_endpoint: editForm.value.docker_endpoint?.trim() || undefined,
    tls_ca_cert: editForm.value.tls_ca_cert || undefined,
    tls_cert: editForm.value.tls_cert || undefined,
    tls_key: editForm.value.tls_key || undefined,
  }
  try {
    await nodesApi.update(id, payload)
    notify.success('Node updated')
    showEdit.value = false
    load()
  } catch (e) { notify.apiError(e) } finally { editSaving.value = false }
}

// --- Change connectivity (impactful: how the control plane reaches this node
// and how its workloads are exposed). Split out of Edit and gated behind an
// explicit acknowledgement, because a change here can briefly interrupt the
// apps and databases already running on the node. ---
const showConnectivity = ref(false)
const connSaving = ref(false)
const connAck = ref(false)
const connImpact = ref<NodeWorkloads | null>(null)
const connForm = ref<CreateNodePayload>({
  name: '', address: '', public_ip: '', public_hostname: '', connectivity: 'port-forward',
  access_mode: 'agent', docker_endpoint: '', tls_ca_cert: '', tls_cert: '', tls_key: '',
})
const connEndpointPlaceholder = computed(() => connForm.value.access_mode === 'api' ? 'tcp://10.0.0.10:2376' : '')
const connAccessModeDesc = computed(() => nodeOptionDescription(ACCESS_MODES, connForm.value.access_mode))
const connConnectivityDesc = computed(() => nodeOptionDescription(CONNECTIVITY_TYPES, connForm.value.connectivity))
async function openConnectivity() {
  if (!node.value) return
  const n = node.value
  connAck.value = false
  // Fetch the blast radius so the warning can name what's at stake.
  connImpact.value = null
  nodesApi.workloads(id).then((r) => { connImpact.value = r.data.data }).catch(() => { connImpact.value = null })
  // Seed every field so the reachability save preserves the node's metadata
  // (name / public addresses) alongside the connectivity changes.
  connForm.value = {
    name: n.name, address: n.address || '', public_ip: n.public_ip || '', public_hostname: n.public_hostname || '',
    connectivity: n.connectivity || 'port-forward',
    access_mode: n.access_mode || 'agent', docker_endpoint: n.docker_endpoint || '',
    tls_ca_cert: '', tls_cert: '', tls_key: '',
  }
  showConnectivity.value = true
}
async function submitConnectivity() {
  if (!connAck.value) return
  const mode = connForm.value.access_mode || 'agent'
  if (!node.value?.is_local && mode === 'api' && !connForm.value.docker_endpoint?.trim()) {
    notify.error('A Docker endpoint is required for this access mode')
    return
  }
  connSaving.value = true
  const payload: CreateNodePayload = {
    name: connForm.value.name.trim(),
    address: connForm.value.address?.trim() || undefined,
    public_ip: connForm.value.public_ip?.trim() || undefined,
    public_hostname: connForm.value.public_hostname?.trim() || undefined,
    connectivity: connForm.value.connectivity,
    access_mode: mode,
    docker_endpoint: connForm.value.docker_endpoint?.trim() || undefined,
    tls_ca_cert: connForm.value.tls_ca_cert || undefined,
    tls_cert: connForm.value.tls_cert || undefined,
    tls_key: connForm.value.tls_key || undefined,
    acknowledge: true,
  }
  try {
    await nodesApi.update(id, payload)
    notify.success('Connectivity updated')
    showConnectivity.value = false
    load()
  } catch (e) { notify.apiError(e) } finally { connSaving.value = false }
}

// --- Agent join command ---
const joinCmd = ref<{ command: string; token_hint: string } | null>(null)
async function showJoinCommand() {
  try {
    const c = (await nodesApi.joinCommand(id)).data.data
    joinCmd.value = { command: c.command, token_hint: c.token_hint }
  } catch (e) { notify.apiError(e) }
}

const showDelete = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  deleting.value = true
  try {
    await nodesApi.remove(id)
    notify.success('Node removed')
    router.push('/admin/nodes')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}
function fmtBytes(b?: number) { return b ? (b / 1073741824).toFixed(1) + ' GB' : '—' }
function fmtSize(n?: number): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}
function connectivityLabel(): string {
  return isEdge.value ? 'Edge gateway (own TLS)' : 'Port forwarding'
}
const gwBadge = computed(() => {
  if (!gateway.value?.deployed) return { cls: 'badge-neutral', text: 'not deployed' }
  if (gateway.value.running) return { cls: 'badge-success', text: 'running' }
  return { cls: 'badge-danger', text: 'stopped' }
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link to="/admin/nodes" class="back-link"><span class="mdi mdi-arrow-left"></span> Nodes</router-link>
        <h1>{{ node?.name || 'Node' }}</h1>
      </div>
      <div v-if="node" class="header-actions">
        <router-link :to="`/admin/nodes/${id}/housekeeping`" class="btn btn-secondary"><span class="mdi mdi-broom"></span> Housekeeping</router-link>
        <button class="btn btn-secondary" @click="openEdit"><span class="mdi mdi-pencil-outline"></span> Edit</button>
        <button class="btn btn-secondary" @click="openConnectivity"><span class="mdi mdi-lan-connect"></span> Change connectivity</button>
        <template v-if="!node.is_local">
          <button v-if="node.access_mode === 'agent'" class="btn btn-secondary" @click="showJoinCommand"><span class="mdi mdi-console"></span> Join command</button>
          <button class="btn btn-secondary" @click="toggleCordon">{{ node.cordoned ? 'Uncordon' : 'Cordon' }}</button>
          <button class="btn btn-secondary" @click="regenerate">Regenerate token</button>
          <button class="btn btn-danger" @click="showDelete = true">Remove</button>
        </template>
      </div>
    </div>

    <div v-if="loading && !node" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="node">
      <!-- Status hero -->
      <div class="hero card">
        <div class="hero-status">
          <span class="hero-badge" :class="connected ? 'is-up' : 'is-down'">
            <span class="mdi" :class="connected ? 'mdi-check-circle' : 'mdi-alert-circle'"></span>
          </span>
          <div>
            <div class="hero-title">{{ statusLabel }}</div>
            <div class="hero-chips">
              <span class="chip"><span class="mdi mdi-shield-account-outline"></span> {{ roleLabel }}</span>
              <span class="chip"><span class="mdi mdi-transit-connection-variant"></span> {{ connectivityLabel() }}</span>
              <span v-if="node.cordoned" class="chip chip-warn"><span class="mdi mdi-cancel"></span> cordoned</span>
              <span v-else class="chip chip-ok"><span class="mdi mdi-check"></span> schedulable</span>
            </div>
          </div>
        </div>
        <div class="hero-meta">
          <div class="hero-stat">
            <span class="hero-stat-label">Docker</span>
            <span class="hero-stat-value">{{ stats?.version || '—' }}</span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">CPU</span>
            <span class="hero-stat-value">{{ stats ? stats.cpus : '—' }}<small v-if="stats"> cores</small></span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">Memory</span>
            <span class="hero-stat-value">{{ stats ? fmtBytes(stats.mem_total) : '—' }}</span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">Platform</span>
            <span class="hero-stat-value">{{ stats ? stats.os + '/' + stats.arch : '—' }}</span>
          </div>
        </div>
      </div>

      <!-- Workload -->
      <template v-if="stats">
        <h2 class="section-title">Workload</h2>
        <div class="stats-grid">
          <div class="stat-card">
            <div class="stat-header">
              <span class="stat-label">Containers</span>
              <span class="stat-icon stat-icon-primary"><span class="mdi mdi-docker"></span></span>
            </div>
            <div class="stat-value">{{ stats.containers_running }}<span class="stat-total">/{{ stats.containers }}</span></div>
            <div class="usage-bar"><div class="usage-fill" :style="{ width: containerPct + '%' }"></div></div>
            <div class="stat-sub">{{ containerPct }}% running</div>
          </div>
          <div class="stat-card">
            <div class="stat-header">
              <span class="stat-label">Images</span>
              <span class="stat-icon stat-icon-info"><span class="mdi mdi-layers-outline"></span></span>
            </div>
            <div class="stat-value">{{ stats.images }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-header">
              <span class="stat-label">Volumes</span>
              <span class="stat-icon stat-icon-secondary"><span class="mdi mdi-harddisk"></span></span>
            </div>
            <div class="stat-value">{{ stats.volumes }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-header">
              <span class="stat-label">Networks</span>
              <span class="stat-icon stat-icon-info"><span class="mdi mdi-lan"></span></span>
            </div>
            <div class="stat-value">{{ stats.networks }}</div>
          </div>
        </div>

        <!-- Resource usage: real host CPU/memory when available, else aggregated
             live from the running containers' stats. -->
        <div class="card mb-4 usage-card">
          <div class="card-header">
            <h2>Resource usage</h2>
            <span v-if="usage" class="badge" :class="usage.source === 'host' ? 'badge-success' : 'badge-info'" :title="usage.source === 'host' ? 'Read from the host procfs' : 'Aggregated from running container stats'">
              {{ usage.source === 'host' ? 'host' : 'containers' }}
            </span>
          </div>
          <div class="card-body">
            <div class="usage-metrics">
              <div class="usage-metric">
                <div class="usage-metric-head">
                  <span class="usage-metric-label"><span class="mdi mdi-cpu-64-bit"></span> CPU</span>
                  <span class="usage-metric-value">{{ usage ? usage.cpuPercent.toFixed(1) + '%' : '—' }}</span>
                </div>
                <div class="usage-bar"><div class="usage-fill" :style="{ width: (usage?.cpuPercent ?? 0) + '%' }"></div></div>
                <div class="stat-sub">{{ usage ? usage.cpuSub : 'No data yet' }}</div>
              </div>
              <div class="usage-metric">
                <div class="usage-metric-head">
                  <span class="usage-metric-label"><span class="mdi mdi-memory"></span> Memory</span>
                  <span class="usage-metric-value">{{ usage ? fmtSize(usage.memUsed) + ' / ' + fmtSize(usage.memTotal) : '—' }}</span>
                </div>
                <div class="usage-bar"><div class="usage-fill" :style="{ width: (usage?.memPercent ?? 0) + '%' }"></div></div>
                <div class="stat-sub">{{ usage ? usage.memSub : 'No data yet' }}</div>
              </div>
            </div>
          </div>
        </div>
      </template>

      <!-- GPUs: discovered devices + admin enable/share policy. Only rendered when
           platform GPU support is on. Devices arrive disabled until opted in. -->
      <template v-if="gpuInfo && gpuInfo.enabled">
        <h2 class="section-title">GPUs</h2>
        <div class="card mb-4">
          <div class="card-header">
            <h2>Devices <span class="text-muted" style="font-weight: 400">{{ gpuInfo.devices.length }}</span></h2>
            <button class="btn btn-sm btn-secondary" :disabled="gpuRescanning" @click="rescanGpus">
              <span class="mdi" :class="gpuRescanning ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
              {{ gpuRescanning ? 'Rescanning…' : 'Rescan GPUs' }}
            </button>
          </div>
          <div v-if="!gpuInfo.toolkit_present" class="card-body">
            <p class="text-muted">
              GPUs may be present, but the NVIDIA Container Toolkit is not installed on this node.
              Install it and rescan to inventory the devices.
            </p>
          </div>
          <div v-else-if="!gpuInfo.devices.length" class="card-body">
            <p class="text-muted">No GPUs discovered on this node yet.</p>
          </div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>Model</th><th>UUID</th><th>Memory</th><th>Index</th><th>Enabled</th><th>Mode</th></tr></thead>
              <tbody>
                <tr v-for="dev in gpuInfo.devices" :key="dev.id">
                  <td>{{ dev.model || dev.vendor }}</td>
                  <td><code>{{ dev.uuid }}</code></td>
                  <td>{{ dev.memory_mb ? dev.memory_mb + ' MiB' : '—' }}</td>
                  <td>{{ dev.index }}</td>
                  <td>
                    <label class="switch">
                      <input type="checkbox" :checked="dev.enabled" :disabled="gpuBusy === dev.id" @change="setGpu(dev, { enabled: !dev.enabled })" />
                      <span>{{ dev.enabled ? 'Enabled' : 'Disabled' }}</span>
                    </label>
                  </td>
                  <td>
                    <select class="form-select form-select-sm" :value="String(dev.shared)" :disabled="gpuBusy === dev.id || !dev.enabled" @change="setGpu(dev, { shared: ($event.target as HTMLSelectElement).value === 'true' })">
                      <option value="true">Shared</option>
                      <option value="false">Dedicated</option>
                    </select>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>

      <!-- Details -->
      <h2 class="section-title">Details</h2>
      <div class="card mb-4">
        <div class="card-body details">
          <div class="detail">
            <span class="text-muted">Connectivity</span>
            <span>{{ connectivityLabel() }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">Access mode</span>
            <span>{{ accessModeLabel }}<span v-if="node.access_mode === 'api' && node.tls_enabled" class="mdi mdi-lock-outline" title="TLS" style="margin-left: 4px"></span></span>
          </div>
          <div class="detail">
            <span class="text-muted">Address</span>
            <span><code v-if="node.address">{{ node.address }}</code><span v-else>{{ node.is_local ? 'local socket' : '—' }}</span></span>
          </div>
          <div class="detail">
            <span class="text-muted">Public IP</span>
            <span><code v-if="node.public_ip">{{ node.public_ip }}</code><span v-else>—</span></span>
          </div>
          <div class="detail">
            <span class="text-muted">Public hostname</span>
            <span><code v-if="node.public_hostname">{{ node.public_hostname }}</code><span v-else>—</span></span>
          </div>
          <div v-if="!node.is_local" class="detail">
            <span class="text-muted">Agent version</span>
            <span>{{ node.agent_version || '—' }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">Slug</span>
            <span><code>{{ node.slug || '—' }}</code></span>
          </div>
          <div class="detail">
            <span class="text-muted">Last seen</span>
            <span>{{ node.is_local ? 'now (manager)' : fmtDateTime(node.last_seen_at) }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">Added</span>
            <span>{{ fmtDateTime(node.created_at) }}</span>
          </div>
        </div>
      </div>

      <!-- Cluster nodes (manager only): the swarm's real membership from
           `docker node ls`, including members that are not Miabi nodes. -->
      <div v-if="node.is_local && clusterMembers.length" class="card mb-4">
        <div class="card-header">
          <h2>Cluster nodes <span class="text-muted" style="font-weight: 400">{{ clusterMembers.length }}</span></h2>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Hostname</th><th>Role</th><th>Availability</th><th>State</th><th>Engine</th><th>Miabi node</th></tr></thead>
            <tbody>
              <tr v-for="m in clusterMembers" :key="m.id">
                <td><span class="cell-title">{{ m.hostname || '—' }}</span></td>
                <td><span class="badge" :class="memberRoleClass(m)">{{ memberRole(m) }}</span></td>
                <td class="cell-sub">{{ m.availability || '—' }}</td>
                <td><span class="badge" :class="memberStateClass(m)">{{ m.state || 'unknown' }}</span></td>
                <td class="cell-sub">{{ m.engine_version || '—' }}</td>
                <td>
                  <a v-if="m.managed" style="cursor: pointer" @click.prevent="router.push(`/admin/nodes/${m.server_id}`)" href="#">{{ m.server_name }}<span v-if="m.is_manager" class="cell-sub"> (this node)</span></a>
                  <span v-else class="badge badge-warning" title="A swarm member with no Miabi node record (joined directly)">unmanaged</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="offline" class="card mb-4">
        <div class="empty-state">
          <span class="mdi mdi-lan-disconnect" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>Node offline</h3>
          <p>Docker resources are unavailable until the agent reconnects.</p>
        </div>
      </div>

      <template v-else>
        <!-- Miabi runtime (the manager/agent container on this node) -->
        <div v-if="stats?.self_container" class="card mb-4">
          <div class="card-header">
            <h2>Miabi Runtime</h2>
            <span class="badge badge-info badge-dot" title="Protected from stop, restart and removal">protected</span>
          </div>
          <div class="card-body">
            <p class="text-muted" style="margin-bottom: 8px">
              This node runs the Miabi {{ node.is_local ? 'control plane (manager)' : 'agent' }}. It can't be stopped, restarted or removed from the containers list.
            </p>
            <div class="gw-meta">
              <span class="gw-meta-item" title="Container"><span class="mdi mdi-cube-outline"></span> <code>{{ stats.self_container.name }}</code></span>
            </div>
            <div class="gw-stats">
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-cpu-64-bit"></span> CPU</span>
                <span class="gw-stat-value">{{ selfStat ? selfStat.cpu_percent.toFixed(1) + '%' : '—' }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-memory"></span> Memory</span>
                <span class="gw-stat-value">{{ selfStat ? fmtSize(selfStat.memory_usage_bytes) : '—' }}</span>
                <span v-if="selfStat" class="gw-stat-sub">{{ selfStat.memory_percent.toFixed(0) }}% of {{ fmtSize(selfStat.memory_limit_bytes) }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-download"></span> Net In</span>
                <span class="gw-stat-value">{{ selfStat ? fmtSize(selfStat.network_rx_bytes) : '—' }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-upload"></span> Net Out</span>
                <span class="gw-stat-value">{{ selfStat ? fmtSize(selfStat.network_tx_bytes) : '—' }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- Edge gateway -->
        <div v-if="isEdge" class="card mb-4">
          <div class="card-header">
            <h2>Edge Gateway</h2>
            <span class="badge badge-dot" :class="gwBadge.cls">{{ gwBadge.text }}</span>
          </div>
          <div class="card-body">
            <p class="text-muted" style="margin-bottom: 8px">
              This node terminates TLS and serves its own routes via a Goma Gateway.
            </p>
            <div v-if="gateway?.image || node.gateway_deployed_at || gateway?.imported || gateway?.redis_enabled" class="gw-meta">
              <span v-if="gateway?.imported" class="badge badge-info" title="Adopted from an existing container">imported</span>
              <span v-if="gateway?.redis_enabled" class="badge badge-neutral" :title="gateway?.redis_shared ? 'Shared cache + rate limiting via the platform Redis' : 'Shared cache + rate limiting via a per-node Redis on this node'"><span class="mdi mdi-database"></span> redis{{ gateway?.redis_shared ? ' · shared' : '' }}</span>
              <span v-if="gateway?.image" class="gw-meta-item" title="Gateway image"><span class="mdi mdi-package-variant-closed"></span> <code>{{ gateway.image }}</code></span>
              <span v-if="gateway?.imported && gateway?.container" class="gw-meta-item" title="Tracked container"><span class="mdi mdi-cube-outline"></span> <code>{{ gateway.container }}</code></span>
              <span v-if="node.gateway_deployed_at" class="gw-meta-item" title="Last deployed"><span class="mdi mdi-clock-outline"></span> {{ new Date(node.gateway_deployed_at).toLocaleString() }}</span>
            </div>

            <!-- Safe update progression (test container → observe → promote) -->
            <div v-if="gwUpdate && (gwUpdating || gwUpdate.phase === 'failed')" class="gw-update">
              <p class="text-sm" style="margin: 0 0 10px">
                Updating gateway <code v-if="gwUpdate.to_image">{{ gwUpdate.to_image }}</code>
                <span class="badge badge-info" style="margin-left: 6px">test → promote</span>
              </p>
              <ol v-if="gwUpdate.phase !== 'failed'" class="upgrade-steps">
                <li v-for="step in GW_UPDATE_STEPS" :key="step" class="upgrade-step" :class="`is-${gwPhaseState(step)}`">
                  <span class="upgrade-step-mark" aria-hidden="true">
                    <span v-if="gwPhaseState(step) === 'done'" class="mdi mdi-check-circle"></span>
                    <span v-else-if="gwPhaseState(step) === 'current'" class="mdi mdi-loading mdi-spin"></span>
                    <span v-else class="mdi mdi-circle-outline"></span>
                  </span>
                  <span class="upgrade-step-label">{{ gwPhaseLabel(step) }}</span>
                </li>
              </ol>
              <div v-else class="gw-update-fail">
                <span class="mdi mdi-alert-circle-outline"></span>
                <div>
                  <p style="margin: 0"><strong>Update failed.</strong> {{ gwUpdate.error }}</p>
                  <p class="text-muted" style="margin: 2px 0 0; font-size: 12px">The previous gateway is still running — it was never replaced. Adjust the image and try again.</p>
                </div>
              </div>
              <p v-if="gwUpdate.phase !== 'failed'" class="text-muted" style="font-size: 12px; margin: 8px 0 0">
                <span class="mdi mdi-shield-check-outline"></span>
                The new image runs as a test container first; the live gateway is only replaced once it stays healthy.
              </p>
            </div>

            <div v-if="gateway?.running" class="gw-stats">
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-cpu-64-bit"></span> CPU</span>
                <span class="gw-stat-value">{{ gwStats ? gwStats.cpu_percent.toFixed(1) + '%' : '—' }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-memory"></span> Memory</span>
                <span class="gw-stat-value">{{ gwStats ? fmtSize(gwStats.memory_usage_bytes) : '—' }}</span>
                <span v-if="gwStats" class="gw-stat-sub">{{ gwStats.memory_percent.toFixed(0) }}% of {{ fmtSize(gwStats.memory_limit_bytes) }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-download"></span> Net In</span>
                <span class="gw-stat-value">{{ gwStats ? fmtSize(gwStats.network_rx_bytes) : '—' }}</span>
              </div>
              <div class="gw-stat">
                <span class="gw-stat-label"><span class="mdi mdi-upload"></span> Net Out</span>
                <span class="gw-stat-value">{{ gwStats ? fmtSize(gwStats.network_tx_bytes) : '—' }}</span>
              </div>
            </div>

            <label class="form-label" style="margin-bottom: 4px">Image</label>
            <div class="gw-image">
              <input v-model="gwImage" class="form-input" :placeholder="gateway?.image_effective || 'jkaninda/goma-gateway:latest'" />
              <button class="btn btn-secondary btn-sm" :disabled="gwImgSaving" @click="saveImage">Set image</button>
            </div>
            <p class="text-muted" style="font-size: 12px; margin: 4px 0 14px">Override the gateway image/tag for this node (blank = default <code>{{ gateway?.image_effective }}</code>). Redeploy to apply.</p>

            <div class="flex items-center gap-2">
              <button class="btn btn-primary btn-sm" :disabled="gwBusy || gwUpdating" @click="deployGateway">
                <span class="mdi mdi-rocket-launch-outline"></span> {{ gateway?.deployed && !gateway?.imported ? 'Reinstall' : 'Install' }}
              </button>
              <button v-if="gateway?.deployed && !gateway?.imported" class="btn btn-secondary btn-sm" :disabled="gwBusy || gwUpdating" title="Validate the new image as a test container, then promote it with no downtime" @click="updateGateway">
                <span class="mdi mdi-update"></span> {{ gwUpdating ? 'Updating…' : 'Update' }}
              </button>
              <button class="btn btn-secondary btn-sm" :disabled="gwBusy" @click="openImportGateway"><span class="mdi mdi-import"></span> Import existing</button>
              <button class="btn btn-secondary btn-sm" @click="openConfig"><span class="mdi mdi-file-cog-outline"></span> Edit config</button>
              <button v-if="gateway?.deployed" class="btn btn-danger btn-sm" :disabled="gwBusy" @click="showTeardown = true">{{ gateway?.imported ? 'Stop managing' : 'Teardown' }}</button>
            </div>
            <p v-if="gateway?.imported" class="text-muted" style="font-size: 12px; margin-top: 8px">
              This gateway was imported — Miabi tracks the existing container without recreating it. Reinstall to replace it with a managed gateway.
            </p>
          </div>
        </div>

        <!-- All Containers -->
        <div class="card mb-4">
          <div class="card-header">
            <h2>All Containers <span class="text-muted" style="font-weight: 400">{{ containers.length }}</span></h2>
            <div class="flex items-center gap-2">
              <button class="btn btn-secondary btn-sm" @click="router.push(`/admin/nodes/${id}/import`)"><span class="mdi mdi-import"></span> Import existing</button>
              <label class="flex items-center gap-1 text-sm"><input v-model="showAllContainers" type="checkbox" @change="loadResources" /> include stopped</label>
            </div>
          </div>
          <div v-if="containers.length === 0" class="empty-state" style="padding: 24px"><p class="text-muted">No containers.</p></div>
          <div v-else class="table-wrapper">
            <table class="ctable">
              <thead>
                <tr><th>Name</th><th>CPU</th><th>Memory</th><th>Net</th><th>Health</th><th>Ports</th><th>Image</th><th>Status</th><th>Actions</th><th>Updated</th></tr>
              </thead>
              <tbody>
                <tr v-for="c in containers" :key="c.id" class="row-clickable" @click="router.push(`/admin/nodes/${id}/containers/${c.id}`)">
                  <td>
                    <div class="name-cell">
                      <span class="trunc" :title="cname(c)">{{ cname(c) }}</span>
                      <span v-if="isManaged(c.labels)" class="badge badge-info" title="Managed by Miabi">managed</span>
                    </div>
                  </td>
                  <td class="cell-sub">{{ cstats[c.id] ? cstats[c.id].cpu_percent.toFixed(2) + '%' : '—' }}</td>
                  <td class="cell-sub">{{ cstats[c.id] ? fmtSize(cstats[c.id].memory_usage_bytes) : '—' }}</td>
                  <td class="cell-sub">{{ rate(c) }}</td>
                  <td><span class="badge" :class="healthClass(c.health)">{{ c.health || 'none' }}</span></td>
                  <td class="cell-sub">{{ fmtPorts(c) }}</td>
                  <td class="trunc cell-sub" :title="c.image">{{ c.image }}</td>
                  <td class="cell-sub">{{ c.status }}</td>
                  <td class="text-right table-actions" @click.stop>
                    <button class="btn-icon btn-icon-muted" title="Restart" aria-label="Restart" :disabled="cbusy === c.id" @click="containerAction(c, 'restartContainer', 'Restarted')"><span class="mdi mdi-restart"></span></button>
                    <button v-if="c.state === 'running'" class="btn-icon btn-icon-muted" title="Stop" aria-label="Stop" :disabled="cbusy === c.id" @click="containerAction(c, 'stopContainer', 'Stopped')"><span class="mdi mdi-stop"></span></button>
                    <button class="btn-icon btn-icon-danger" :title="isManaged(c.labels) ? 'Managed by Miabi' : 'Remove'" :aria-label="isManaged(c.labels) ? 'Managed by Miabi' : 'Remove'" :disabled="cbusy === c.id || isManaged(c.labels)" @click="toRemoveContainer = c"><span class="mdi mdi-delete-outline"></span></button>
                  </td>
                  <td class="cell-sub">{{ statsUpdatedAt ? new Date(statsUpdatedAt).toLocaleTimeString() : '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- Published host ports -->
        <div class="card mb-4">
          <div class="card-header">
            <h2>Published ports <span class="text-muted" style="font-weight: 400">{{ nodePorts.length }}</span></h2>
            <span class="text-muted" style="font-size: 12px">Host ports in use on this node — bind a new one only to a free port.</span>
          </div>
          <div v-if="nodePorts.length === 0" class="empty-state" style="padding: 24px"><p class="text-muted">No host ports published.</p></div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>Host port</th><th>Protocol</th><th>Container port</th><th>Owner</th></tr></thead>
              <tbody>
                <tr v-for="p in nodePorts" :key="`${p.host_port}/${p.protocol}/${p.container_id}`">
                  <td class="mono">{{ p.host_port }}</td>
                  <td class="cell-sub">{{ p.protocol }}</td>
                  <td class="cell-sub">{{ p.private_port || '—' }}</td>
                  <td class="cell-sub">
                    {{ p.container }}
                    <span class="badge" :class="p.managed ? 'badge-info' : 'badge-neutral'" style="margin-left: 6px">{{ p.managed ? 'Miabi' : 'external' }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- All Volumes -->
        <div class="card mb-4">
          <div class="card-header">
            <h2>All Volumes <span class="text-muted" style="font-weight: 400">{{ volumes.length }}</span></h2>
            <form class="flex items-center gap-2" @submit.prevent="createVolume">
              <input v-model="newVolume" class="form-input form-input-sm" placeholder="volume name" aria-label="Volume name" style="max-width: 180px" />
              <button class="btn btn-primary btn-sm" :disabled="!newVolume.trim()">Create</button>
            </form>
          </div>
          <div v-if="volumes.length === 0" class="empty-state" style="padding: 24px"><p class="text-muted">No volumes.</p></div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>Name</th><th>Driver</th><th>Mountpoint</th><th></th></tr></thead>
              <tbody>
                <tr v-for="v in volumes" :key="v.name">
                  <td class="trunc mono" :title="v.name">{{ v.name }}<span v-if="isManaged(v.labels)" class="badge badge-info" style="margin-left: 6px">managed</span></td>
                  <td class="cell-sub">{{ v.driver }}</td>
                  <td class="trunc cell-sub mono" :title="v.mountpoint">{{ v.mountpoint || '—' }}</td>
                  <td class="text-right">
                    <button class="btn-icon btn-icon-danger" :title="isManaged(v.labels) ? 'Managed by Miabi' : 'Remove'" :aria-label="isManaged(v.labels) ? 'Managed by Miabi' : 'Remove'" :disabled="isManaged(v.labels)" @click="removeVolume(v)"><span class="mdi mdi-delete-outline"></span></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- All Networks -->
        <div class="card mb-4">
          <div class="card-header">
            <h2>All Networks <span class="text-muted" style="font-weight: 400">{{ networks.length }}</span></h2>
            <form class="flex items-center gap-2" @submit.prevent="createNetwork">
              <input v-model="newNetwork" class="form-input form-input-sm" placeholder="network name" aria-label="Network name" style="max-width: 180px" />
              <button class="btn btn-primary btn-sm" :disabled="!newNetwork.trim()">Create</button>
            </form>
          </div>
          <div v-if="networks.length === 0" class="empty-state" style="padding: 24px"><p class="text-muted">No networks.</p></div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>Name</th><th>Driver</th><th>Scope</th><th></th></tr></thead>
              <tbody>
                <tr v-for="n in networks" :key="n.id">
                  <td class="trunc mono" :title="n.name">{{ n.name }}<span v-if="isManaged(n.labels)" class="badge badge-info" style="margin-left: 6px">managed</span></td>
                  <td class="cell-sub">{{ n.driver }}</td>
                  <td class="cell-sub">{{ n.scope }}</td>
                  <td class="text-right">
                    <button class="btn-icon btn-icon-danger" :title="isManaged(n.labels) ? 'Managed by Miabi' : 'Remove'" :aria-label="isManaged(n.labels) ? 'Managed by Miabi' : 'Remove'" :disabled="isManaged(n.labels)" @click="removeNetwork(n)"><span class="mdi mdi-delete-outline"></span></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

      </template>
    </template>

    <Teleport to="body">
      <div v-if="showConfig" class="modal-overlay" @click.self="showConfig = false">
        <div class="modal" style="max-width: 760px; width: 100%">
          <div class="modal-header">
            <h3>Gateway config — {{ node?.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showConfig = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p class="text-muted" style="margin-bottom: 10px; font-size: 13px">
              The node's <code>goma.yml</code>. The provider token is injected as <code>${INSTANCE_API_KEY}</code> at deploy — don't hardcode it. Listens on 80/443. <strong>Redeploy</strong> the gateway to apply.
            </p>
            <textarea v-model="cfgText" class="form-input editor" spellcheck="false" rows="20"></textarea>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-ghost" @click="cfgText = cfgDefault">Reset to default</button>
            <div class="flex items-center gap-2" style="margin-left: auto">
              <button type="button" class="btn btn-secondary" @click="showConfig = false">Cancel</button>
              <button type="button" class="btn btn-primary" :disabled="cfgSaving" @click="saveConfig">{{ cfgSaving ? 'Saving…' : 'Save' }}</button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <Teleport to="body">
      <div v-if="showEdit" class="modal-overlay" @click.self="showEdit = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Edit node</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEdit = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="submitEdit">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="editForm.name" class="form-input" placeholder="e.g. edge-eu-1" :disabled="node?.is_local" required autofocus />
              </div>
              <div class="form-group">
                <label class="form-label">Public IP <span class="text-muted">(A/AAAA record target for domains served by this node)</span></label>
                <input v-model="editForm.public_ip" class="form-input" placeholder="e.g. 203.0.113.10" style="font-family: monospace" />
              </div>
              <div class="form-group">
                <label class="form-label">Public hostname <span class="text-muted">(optional CNAME target, e.g. node.example.com)</span></label>
                <input v-model="editForm.public_hostname" class="form-input" placeholder="optional" style="font-family: monospace" />
              </div>
              <p class="conn-note" style="margin-bottom: 0">
                <span class="mdi mdi-lan-connect"></span>
                Connectivity, access mode and Docker endpoint moved to their own action —
                use <strong>Change connectivity</strong> to edit how Miabi reaches and exposes this node.
              </p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showEdit = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="editSaving">{{ editSaving ? 'Saving…' : 'Save changes' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Change connectivity — impactful reachability settings, gated by a warning. -->
    <Teleport to="body">
      <div v-if="showConnectivity" class="modal-overlay" @click.self="showConnectivity = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Change connectivity — {{ node?.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showConnectivity = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="submitConnectivity">
            <div class="modal-body">
              <div class="conn-warning">
                <span class="mdi mdi-alert-outline"></span>
                <div>
                  <strong>This can interrupt running workloads.</strong>
                  Changing how Miabi reaches and exposes this node may briefly disconnect the
                  apps and databases already running on it — containers can become temporarily
                  unreachable, and external routes and SSL for those services may need to re-sync.
                  Apply during a maintenance window.
                  <div v-if="connImpact && (connImpact.apps || connImpact.databases)" class="conn-impact">
                    <span class="mdi mdi-cube-outline"></span>
                    This node runs
                    <strong>{{ connImpact.apps }} app{{ connImpact.apps === 1 ? '' : 's' }}</strong>
                    and
                    <strong>{{ connImpact.databases }} database{{ connImpact.databases === 1 ? '' : 's' }}</strong>
                    that may be affected.
                  </div>
                </div>
              </div>
              <div class="form-group">
                <span class="form-label label-row">
                  Connectivity
                  <FieldInfo :items="CONNECTIVITY_TYPES" title="Connectivity types explained" />
                </span>
                <select v-model="connForm.connectivity" class="form-input">
                  <option v-for="o in CONNECTIVITY_TYPES" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <p class="form-hint">{{ connConnectivityDesc }}</p>
                <p v-if="node?.is_local" class="text-muted" style="font-size: 12px; margin-top: 4px">
                  As the manager, this node can run its own Goma gateway — install a fresh one or import an existing one.
                </p>
              </div>
              <template v-if="!node?.is_local">
                <div class="form-group">
                  <span class="form-label label-row">
                    Access mode
                    <FieldInfo :items="ACCESS_MODES" title="Access modes explained" />
                  </span>
                  <select v-model="connForm.access_mode" class="form-input">
                    <option v-for="o in ACCESS_MODES" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                  <p class="form-hint">{{ connAccessModeDesc }}</p>
                </div>
                <template v-if="connForm.access_mode === 'api'">
                  <div class="form-group">
                    <label class="form-label">Docker endpoint</label>
                    <input v-model="connForm.docker_endpoint" class="form-input" :placeholder="connEndpointPlaceholder" required style="font-family: monospace" />
                  </div>
                  <div class="form-group">
                    <label class="form-label">TLS <span class="text-muted">(leave blank to keep existing)</span></label>
                    <textarea v-model="connForm.tls_ca_cert" class="form-input" rows="2" placeholder="CA certificate (PEM)" style="font-family: monospace; font-size: 12px"></textarea>
                    <textarea v-model="connForm.tls_cert" class="form-input" rows="2" placeholder="Client certificate (PEM) — for mTLS" style="font-family: monospace; font-size: 12px; margin-top: 6px"></textarea>
                    <textarea v-model="connForm.tls_key" class="form-input" rows="2" placeholder="Client key (PEM) — stored encrypted" style="font-family: monospace; font-size: 12px; margin-top: 6px"></textarea>
                  </div>
                </template>
                <div v-if="connForm.access_mode !== 'api'" class="form-group">
                  <label class="form-label">Address <span class="text-muted">(host/IP the proxy reaches published ports at)</span></label>
                  <input v-model="connForm.address" class="form-input" placeholder="e.g. 10.0.0.7" />
                </div>
              </template>
              <label class="conn-ack">
                <input type="checkbox" v-model="connAck" />
                <span>I understand this may temporarily interrupt apps and databases running on this node.</span>
              </label>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showConnectivity = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="connSaving || !connAck">{{ connSaving ? 'Applying…' : 'Apply change' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <Teleport to="body">
      <div v-if="showImportGw" class="modal-overlay" @click.self="showImportGw = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Import existing gateway</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showImportGw = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p class="text-muted" style="font-size: 13px; margin-bottom: 12px">
              Adopt a Goma gateway that already runs on this node, instead of installing a new one. The container keeps running — Miabi just tracks it (zero downtime) and copies its current <code>goma.yml</code> as the node's gateway config.
            </p>
            <div v-if="gwCandidatesLoading" class="empty-state"><p class="text-muted">Scanning for gateway containers…</p></div>
            <div v-else-if="gwCandidates.length === 0" class="empty-state">
              <p class="text-muted">No gateway-like containers found on this node. Install a fresh gateway instead.</p>
            </div>
            <div v-else class="form-group">
              <label class="form-label">Gateway container</label>
              <label v-for="ct in gwCandidates" :key="ct.id" class="gw-cand">
                <input type="radio" :value="ct.id" v-model="gwCandidate" />
                <div>
                  <div class="gw-cand-name">{{ ct.name }} <span class="badge" :class="ct.state === 'running' ? 'badge-success' : 'badge-muted'">{{ ct.state }}</span></div>
                  <div class="text-muted" style="font-size: 12px"><code>{{ ct.image }}</code></div>
                </div>
              </label>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showImportGw = false">Cancel</button>
            <button type="button" class="btn btn-primary" :disabled="gwImporting || gwCandidates.length === 0 || !gwCandidate" @click="importGateway">
              {{ gwImporting ? 'Importing…' : 'Import gateway' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <Teleport to="body">
      <div v-if="regenToken" class="modal-overlay" @click.self="regenToken = null">
        <div class="modal">
          <div class="modal-header">
            <h3>New join token</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="regenToken = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="app-banner app-banner--warning">
              <span class="mdi mdi-alert-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-title">Copy now</p>
                <p class="app-banner-text">The old token is invalid. Update the agent's MIABI_NODE_TOKEN.</p>
              </div>
            </div>
            <div class="code-block" style="margin-top: 14px">{{ regenToken }}</div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copy(regenToken!)">Copy</button>
            <button type="button" class="btn btn-primary" @click="regenToken = null">Done</button>
          </div>
        </div>
      </div>
    </Teleport>

    <Teleport to="body">
      <div v-if="joinCmd" class="modal-overlay" @click.self="joinCmd = null">
        <div class="modal" style="max-width: 680px; width: 100%">
          <div class="modal-header">
            <h3>Agent join command</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="joinCmd = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p class="text-muted" style="font-size: 13px; margin-bottom: 10px">Run this on the node host to start the agent and connect it to this manager.</p>
            <pre class="code-block cmd">{{ joinCmd.command }}</pre>
            <p class="text-muted" style="font-size: 12px; margin-top: 10px"><span class="mdi mdi-information-outline"></span> {{ joinCmd.token_hint }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copy(joinCmd.command)">Copy</button>
            <button type="button" class="btn btn-primary" @click="joinCmd = null">Done</button>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="showTeardown"
      title="Tear down gateway"
      message="Remove the Goma Gateway container from this node? Routing through it stops until you redeploy. ACME certs are preserved."
      confirm-label="Teardown"
      variant="danger"
      :busy="gwBusy"
      @confirm="teardownGateway"
      @cancel="showTeardown = false"
    />

    <ConfirmDialog
      :open="showDelete"
      title="Remove node"
      :message="`Remove node &quot;${node?.name}&quot;? Its agent tunnel is closed and the edge gateway (if any) is torn down.`"
      confirm-label="Remove"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="showDelete = false"
    />

    <ConfirmDialog
      :open="!!toRemoveContainer"
      title="Remove container"
      :message="`Remove container &quot;${toRemoveContainer ? cname(toRemoveContainer) : ''}&quot;? This cannot be undone.`"
      confirm-label="Remove"
      variant="danger"
      @confirm="removeContainer"
      @cancel="toRemoveContainer = null"
    />
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.label-row { display: inline-flex; align-items: center; gap: 6px; }
.header-actions { display: flex; gap: 8px; }
.conn-note {
  display: flex; align-items: flex-start; gap: 8px;
  font-size: 13px; color: var(--text-muted);
  background: var(--surface-2, rgba(127, 127, 127, 0.08));
  border-radius: 8px; padding: 10px 12px;
}
.conn-note .mdi { margin-top: 1px; }
.conn-warning {
  display: flex; align-items: flex-start; gap: 10px;
  font-size: 13px; line-height: 1.5;
  color: var(--warning-text, #92400e);
  background: var(--warning-bg, rgba(245, 158, 11, 0.12));
  border: 1px solid var(--warning-border, rgba(245, 158, 11, 0.35));
  border-radius: 8px; padding: 12px 14px; margin-bottom: 16px;
}
.conn-warning .mdi { font-size: 18px; margin-top: 1px; flex-shrink: 0; }
.conn-impact { display: flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 12.5px; }
.conn-impact .mdi { font-size: 15px; }
.conn-ack {
  display: flex; align-items: flex-start; gap: 8px;
  font-size: 13px; margin-top: 4px; cursor: pointer;
}
.conn-ack input { margin-top: 2px; }
.back-link { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); font-size: 13px; text-decoration: none; margin-bottom: 4px; }
.back-link:hover { color: var(--text); }

/* Status hero */
.hero {
  display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap;
  gap: 20px; padding: 20px 24px; margin-bottom: 20px;
}
.hero-status { display: flex; align-items: center; gap: 14px; }
.hero-badge {
  width: 46px; height: 46px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center; font-size: 26px;
}
.hero-badge.is-up { background: var(--success-50); color: var(--success-600); }
.hero-badge.is-down { background: var(--danger-50); color: var(--danger-600); }
.hero-title { font-size: 17px; font-weight: 700; color: var(--text-primary); }
.hero-chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 9px; border-radius: 9999px; font-size: 12px; font-weight: 500;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.chip .mdi { font-size: 13px; }
.chip-ok { background: var(--success-50); color: var(--success-600); }
.chip-warn { background: var(--warning-50); color: var(--warning-600); }
.hero-meta { display: flex; gap: 28px; flex-wrap: wrap; }
.hero-stat { display: flex; flex-direction: column; }
.hero-stat-label { font-size: 12px; color: var(--text-muted); }
.hero-stat-value { font-size: 18px; font-weight: 700; color: var(--text-primary); font-variant-numeric: tabular-nums; }
.hero-stat-value small { font-size: 12px; font-weight: 500; color: var(--text-muted); }

.section-title {
  font-size: 13px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em;
  color: var(--text-muted); margin: 0 0 12px; padding-top: 4px;
}

/* Workload utilization */
.stat-total { font-size: 16px; font-weight: 600; color: var(--text-muted); }
.usage-bar { height: 6px; border-radius: 9999px; background: var(--bg-tertiary); overflow: hidden; margin: 10px 0 6px; }
.usage-fill { height: 100%; border-radius: 9999px; background: var(--success-500); transition: width 0.4s ease; }

/* Node resource-usage card */
.usage-card { margin-bottom: 24px; }
.usage-metrics { display: grid; grid-template-columns: repeat(2, 1fr); gap: 24px; }
.usage-metric-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.usage-metric-label { color: var(--text-muted); font-size: 13px; display: inline-flex; align-items: center; gap: 6px; }
.usage-metric-value { font-size: 18px; font-weight: 700; font-variant-numeric: tabular-nums; }
@media (max-width: 640px) { .usage-metrics { grid-template-columns: 1fr; } }

/* Details panel */
.details {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 18px 24px;
}
.detail { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.detail > .text-muted { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em; }

/* Tighter stat cards so the workload cards fit on one row. */
.stats-grid { grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); margin-bottom: 24px; }
.stats-grid .stat-card { padding: 14px 16px; }
.stats-grid .stat-value { font-size: 22px; }
code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; }
/* Truncate long names/images; full value in the title tooltip. */
.trunc { max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.name-cell { display: flex; align-items: center; gap: 6px; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.ctable td, .ctable th { white-space: nowrap; }
.form-input-sm { padding: 4px 8px; font-size: 13px; }
.editor { width: 100%; font-family: 'JetBrains Mono', monospace; font-size: 13px; line-height: 1.5; white-space: pre; overflow-wrap: normal; overflow-x: auto; tab-size: 2; }
.code-block.cmd { white-space: pre-wrap; word-break: break-all; font-family: 'JetBrains Mono', monospace; font-size: 12.5px; line-height: 1.55; }
.gw-image { display: flex; align-items: center; gap: 8px; }
.gw-cand { display: flex; align-items: flex-start; gap: 10px; padding: 8px 10px; border: 1px solid var(--border-primary); border-radius: 8px; margin-bottom: 6px; cursor: pointer; }
.gw-cand input { margin-top: 3px; }
.gw-cand-name { font-size: 13px; display: flex; align-items: center; gap: 6px; }
.badge-muted { background: var(--bg-tertiary); color: var(--text-muted); }
.gw-image .form-input { flex: 1; min-width: 0; font-family: 'JetBrains Mono', monospace; font-size: 13px; }
.gw-meta { display: flex; flex-wrap: wrap; gap: 8px 16px; margin-bottom: 12px; }
.gw-meta-item { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; color: var(--text-muted); }
.gw-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 10px; margin-bottom: 14px; }
.gw-stat { display: flex; flex-direction: column; gap: 2px; padding: 10px 12px; background: var(--bg-tertiary); border-radius: 8px; }
.gw-stat-label { font-size: 12px; color: var(--text-muted); display: inline-flex; align-items: center; gap: 4px; }
.gw-stat-value { font-size: 18px; font-weight: 600; }
.gw-stat-sub { font-size: 11px; color: var(--text-muted); }

/* Safe-update progression */
.gw-update { padding: 12px 14px; margin-bottom: 14px; background: var(--bg-tertiary); border: 1px solid var(--border-primary); border-radius: 8px; }
.upgrade-steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
.upgrade-step { display: flex; align-items: center; gap: 10px; padding: 5px 0; font-size: 14px; color: var(--text-muted); }
.upgrade-step-mark { font-size: 18px; line-height: 1; display: inline-flex; }
.upgrade-step.is-done { color: var(--text-primary); }
.upgrade-step.is-done .upgrade-step-mark { color: var(--success-600, #16a34a); }
.upgrade-step.is-current { color: var(--text-primary); font-weight: 600; }
.upgrade-step.is-current .upgrade-step-mark { color: var(--primary-600, #2563eb); }
.upgrade-step.is-todo { opacity: 0.6; }
.gw-update-fail { display: flex; gap: 10px; align-items: flex-start; color: var(--danger-700, #b91c1c); }
.gw-update-fail .mdi { font-size: 20px; line-height: 1.2; }
</style>
