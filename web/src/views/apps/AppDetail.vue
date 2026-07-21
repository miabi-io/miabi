<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { appApi, type ExternalAccess } from '@/api/apps'
import { volumeApi, monitoringApi, databaseApi, usageApi } from '@/api/resources'
import { registryApi } from '@/api/registries'
import { gitRepositoryApi } from '@/api/gitRepositories'
import { networkApi } from '@/api/networks'
import { stackApi } from '@/api/stacks'
import { routeApi } from '@/api/routes'
import { portBindingApi } from '@/api/portBindings'
import { eventsApi } from '@/api/events'
import { sseUrl } from '@/api/client'
import ResourceIcon from '@/components/ResourceIcon.vue'
import ShellTerminal from '@/components/ShellTerminal.vue'
import ContainerProcesses from '@/components/ContainerProcesses.vue'
import LogViewer from '@/components/LogViewer.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import MetadataCard from '@/components/MetadataCard.vue'
import AppAccessPanel from '@/components/AppAccessPanel.vue'
import type { Application, AppOverview, Deployment, Release, AppEnvVar, Route, Network, Stack, Volume, StatsSample, Registry, GitRepository, AppEvent, AppPort, PortBinding, AppDatabase, ConnectionInfo, DeployStrategy, RestartPolicy, ImagePullPolicy, BuildMethod, HealthcheckType, ResourceLimits, LiveStatus, HostMountPreset, DatabaseInstance, LogicalDatabase, NodePlacement } from '@/api/types'

// Secret-reference example built here so `}}` doesn't break the template's
// mustache parser.
const secretRefHint = '${{ secrets.NAME }}'

const STRATEGIES: { value: DeployStrategy; label: string; hint: string }[] = [
  { value: 'recreate', label: 'Recreate', hint: 'Stops the old container before starting the new one (brief downtime).' },
  { value: 'rolling', label: 'Rolling', hint: 'Starts the new container, waits for health, then retires the old one (no downtime).' },
  { value: 'canary', label: 'Canary', hint: 'Runs the new version alongside the old and shifts traffic to it gradually.' },
]
function strategyHint(s: DeployStrategy | undefined): string {
  return STRATEGIES.find((x) => x.value === s)?.hint ?? ''
}

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const appId = computed(() => Number(route.params.id))
const wid = computed(() => ws.currentWorkspaceId)
const base = computed(() => `/workspaces/${wid.value}/apps/${appId.value}`)

const app = ref<Application | null>(null)
const tabs = [
  { key: 'overview', label: 'Overview' },
  { key: 'events', label: 'Events' },
  { key: 'logs', label: 'Logs' },
  { key: 'deployments', label: 'Deployments' },
  { key: 'environment', label: 'Environment' },
  { key: 'network', label: 'Network' },
  { key: 'routes', label: 'Routes' },
  { key: 'ports', label: 'Ports' },
  { key: 'volumes', label: 'Volumes' },
  { key: 'databases', label: 'Databases' },
  { key: 'releases', label: 'Releases' },
  { key: 'access', label: 'Access' },
  { key: 'settings', label: 'Settings' },
] as const
type TabKey = (typeof tabs)[number]['key']
// The active tab is mirrored in the URL (?tab=…) so refresh, back/forward, and
// shared links land on the same tab.
function tabFromQuery(): TabKey {
  const q = route.query.tab
  return typeof q === 'string' && tabs.some((t) => t.key === q) ? (q as TabKey) : 'overview'
}
const tab = ref<TabKey>(tabFromQuery())

// Overview: aggregated summary + latest events (live, from the shared stream)
const overview = ref<AppOverview | null>(null)
const overviewLoading = ref(false)

// Network tab: stable hostname/alias + per-port endpoints, derived from the app.
const hostname = computed(() => app.value?.alias || (app.value ? `mb-app-${app.value.id}` : ''))
const stackHostname = computed(() => (app.value?.stack_id ? app.value.name : ''))
const networkPorts = computed<number[]>(() => {
  const ps = (app.value?.ports || []).map((p: AppPort) => p.container_port).filter((n: number) => n > 0)
  if (ps.length) return ps
  return app.value?.port ? [app.value.port] : []
})

// External access (one-click public URLs via the platform wildcard domain).
const extAccess = ref<ExternalAccess | null>(null)
const extSaving = ref(false)
// The set of currently-exposed ports, edited locally before Save.
const extSelected = ref<Set<number>>(new Set())
watch(extAccess, (v) => { extSelected.value = new Set((v?.ports || []).map((p) => p.port)) })
function toggleExtPort(port: number) {
  const next = new Set(extSelected.value)
  next.has(port) ? next.delete(port) : next.add(port)
  extSelected.value = next
}
const extDirty = computed(() => {
  const cur = new Set((extAccess.value?.ports || []).map((p) => p.port))
  if (cur.size !== extSelected.value.size) return true
  for (const p of extSelected.value) if (!cur.has(p)) return true
  return false
})
function extUrlFor(port: number): string {
  return extAccess.value?.ports?.find((p) => p.port === port)?.url || ''
}
async function saveExternalAccess() {
  if (!wid.value) return
  extSaving.value = true
  try {
    extAccess.value = (await appApi.setExternalAccess(wid.value, appId.value, [...extSelected.value])).data.data ?? null
    notify.success('External access updated — routes are being applied')
  } catch (e) {
    notify.apiError(e, 'Failed to update external access')
  } finally {
    extSaving.value = false
  }
}
// Turn external access off entirely: clears the selection and removes all
// generated routes (and their managed host ports on port-forward nodes).
function disableExternalAccess() {
  extSelected.value = new Set()
  saveExternalAccess()
}
const extExposedCount = computed(() => extAccess.value?.ports?.length ?? 0)

// Events (timeline) + runtime logs
const appEvents = ref<AppEvent[]>([])
const eventsLoading = ref(false)
let eventsES: EventSource | null = null
// The Events tab pages the history (newest-first) with a "Load more" cursor
// rather than dumping everything; live events still prepend over the SSE.
const EVENTS_PAGE = 20
const eventsHasMore = ref(false)
const loadingMoreEvents = ref(false)
// The Overview "Latest events" card shows the head of the live events list.
const latestEvents = computed(() => appEvents.value.slice(0, 6))

// Live container status (polled), with a stats snapshot when running.
const liveStatus = ref<LiveStatus | null>(null)
let statusPoll: ReturnType<typeof setInterval> | null = null
const runtimeLogs = ref<string[]>([])
const runtimeConnected = ref(false)
let runtimeES: EventSource | null = null
// Cap the in-memory log buffer so a chatty container can't grow the DOM (and the
// tab) without bound; oldest lines are dropped and a note is shown when trimmed.
const RUNTIME_LOG_CAP = 5000
const logsTrimmed = ref(false)
// Client-side log search: narrows only the rendered view (the buffer keeps
// filling over the SSE). Plain substring or regex, with hit highlighting.
// Seeded from the URL (?q=…&re=1&size=…) so refresh and shared links persist.
const logSearch = ref(typeof route.query.q === 'string' ? route.query.q : '')
const logRegexMode = ref(route.query.re === '1')
// Logs panel size: a few presets the user can switch between (persisted to ?size).
type LogSize = 'small' | 'medium' | 'large'
const LOG_SIZES: { value: LogSize; label: string; title: string; height: string }[] = [
  { value: 'small', label: 'S', title: 'Small', height: '350px' },
  { value: 'medium', label: 'M', title: 'Medium', height: '600px' },
  { value: 'large', label: 'L', title: 'Large (fill screen)', height: 'calc(100vh - 240px)' },
]
function isLogSize(v: unknown): v is LogSize { return v === 'small' || v === 'medium' || v === 'large' }
const logSize = ref<LogSize>(isLogSize(route.query.size) ? route.query.size : 'small')
const logViewStyle = computed(() => {
  const h = LOG_SIZES.find((s) => s.value === logSize.value)?.height ?? '350px'
  return { height: h, minHeight: logSize.value === 'large' ? '420px' : undefined }
})
// A single compiled matcher shared by the filter and the highlighter. Global +
// case-insensitive; plain queries are escaped so regex metachars are literal.
const logMatcher = computed<RegExp | null>(() => {
  const q = logSearch.value.trim()
  if (!q) return null
  try {
    const pattern = logRegexMode.value ? q : q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    return new RegExp(pattern, 'gi')
  } catch {
    return null // invalid regex
  }
})
// True when regex mode is on but the pattern doesn't compile.
const logRegexError = computed(() => logRegexMode.value && !!logSearch.value.trim() && logMatcher.value === null)
const filteredRuntimeLogs = computed(() => {
  const re = logMatcher.value
  if (!re) return runtimeLogs.value // no query, or invalid regex → show everything
  return runtimeLogs.value.filter((l) => { re.lastIndex = 0; return re.test(l) })
})
interface LogSegment { text: string; hit: boolean }
// Split a line into matched / unmatched segments for highlighting. Falls back to
// the whole line when there's no active matcher.
function logSegments(line: string): LogSegment[] {
  const re = logMatcher.value
  if (!re) return [{ text: line, hit: false }]
  const segs: LogSegment[] = []
  let last = 0
  let m: RegExpExecArray | null
  re.lastIndex = 0
  while ((m = re.exec(line)) !== null) {
    if (m.index > last) segs.push({ text: line.slice(last, m.index), hit: false })
    segs.push({ text: m[0], hit: true })
    last = m.index + m[0].length
    if (m[0].length === 0) re.lastIndex++ // guard against zero-width matches looping
  }
  if (last < line.length) segs.push({ text: line.slice(last), hit: false })
  return segs.length ? segs : [{ text: line, hit: false }]
}

// Follow mode: keep the log view pinned to the newest line as output streams in.
// Scrolling up pauses it (the user is reading history); scrolling back to the
// bottom resumes. The toggle button does the same explicitly.
const logFollow = ref(true)
const logViewEl = ref<HTMLElement | null>(null)
function scrollLogsToBottom() {
  const el = logViewEl.value
  if (el) el.scrollTop = el.scrollHeight
}
// Programmatic scroll-to-bottom keeps us "at bottom", so this never fights the
// auto-follow; a real upward scroll trips the threshold and pauses follow.
function onLogScroll() {
  const el = logViewEl.value
  if (!el) return
  logFollow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}
function toggleLogFollow() {
  logFollow.value = !logFollow.value
  if (logFollow.value) nextTick(scrollLogsToBottom)
}
// New (or newly-filtered) lines, or a panel resize: stick to the bottom while
// following.
watch([() => filteredRuntimeLogs.value.length, logSize], () => {
  if (logFollow.value) nextTick(scrollLogsToBottom)
})

// Copy / download operate on the currently-visible lines, so an active search
// narrows the export to what you see.
const logCopied = ref(false)
async function copyLogs() {
  try {
    await navigator.clipboard.writeText(filteredRuntimeLogs.value.join('\n'))
    logCopied.value = true
    setTimeout(() => { logCopied.value = false }, 1500)
  } catch (e) {
    notify.apiError(e, 'Failed to copy logs')
  }
}
function downloadLogs() {
  const blob = new Blob([filteredRuntimeLogs.value.join('\n')], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${app.value?.name || 'app'}-runtime.log`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// Deployments + logs
const deployments = ref<Deployment[]>([])
const logs = ref<string[]>([])
const streamingId = ref<number | null>(null)
// deployStreaming is true while a deployment's log stream is open (live); its
// status drives the toolbar badge. The buffer is capped like the runtime logs.
const deployStreaming = ref(false)
const deployStatus = ref('')
const deployLogsTrimmed = ref(false)
let es: EventSource | null = null

// streamingNumber is the per-app deployment number of the currently streamed
// deployment (streamingId is its global id, used for the SSE stream). Shown in
// the Logs header so it matches the "#<number>" in the deployments table.
const streamingNumber = computed(() =>
  deployments.value.find((d) => d.id === streamingId.value)?.number ?? null,
)

// depStatusClass maps a deployment status to a badge variant for the log toolbar.
function depStatusClass(s: string): string {
  if (s === 'failed') return 'badge-danger'
  if (s === 'succeeded' || s === 'running' || s === 'streaming') return 'badge-success'
  return 'badge-warning' // building / deploying / pending / canary
}

// Environment
const envVars = ref<AppEnvVar[]>([])
const revealedEnv = ref<Record<string, string>>({}) // key -> revealed secret value
const revealingEnv = ref('') // key with a reveal request in flight
const newEnv = ref({ key: '', value: '', secret: false })
const showEnvImport = ref(false)
const envImport = ref({ content: '', secret: false })
const importingEnv = ref(false)

// Routes (this app's Goma routes)
const appRoutes = ref<Route[]>([])
// Browser URL for a route host (https when TLS is on), so users can open the app
// directly from the Routes tab.
function routeUrl(r: Route, host: string): string {
  const scheme = r.tls_mode && r.tls_mode !== 'none' ? 'https' : 'http'
  const path = r.path && r.path !== '/' ? r.path : ''
  return `${scheme}://${host}${path}`
}

// Ports / host bindings
const appBindings = ref<PortBinding[]>([])
const showBindReq = ref(false)
const requestingBind = ref(false)
const bindForm = ref<{ container_port: number; protocol: 'tcp' | 'udp'; host_port: number }>({ container_port: 0, protocol: 'tcp', host_port: 0 })
// Declare a container port (Ports tab) without leaving for Settings.
const showAddPort = ref(false)
const portForm = ref<{ container_port: number; protocol: 'tcp' | 'udp'; scheme: 'http' | 'https'; name: string }>({ container_port: 0, protocol: 'tcp', scheme: 'http', name: '' })

// Promise-based confirm dialog, replacing window.confirm for the port flows so
// they get a real modal. askConfirm resolves true on confirm, false on cancel.
interface ConfirmOpts { title: string; message?: string; confirmLabel?: string; cancelLabel?: string; variant?: 'danger' | 'primary' }
const confirmDialog = ref<ConfirmOpts & { open: boolean }>({ open: false, title: '' })
let confirmResolve: ((ok: boolean) => void) | null = null
function askConfirm(opts: ConfirmOpts): Promise<boolean> {
  confirmDialog.value = { open: true, ...opts }
  return new Promise((resolve) => { confirmResolve = resolve })
}
function resolveConfirm(ok: boolean) {
  confirmDialog.value.open = false
  confirmResolve?.(ok)
  confirmResolve = null
}

// Volumes
const volumes = ref<Volume[]>([])
// Co-location: an app can only mount volumes on its own node.
const volumesOnNode = computed(() =>
  volumes.value.filter((v) => (v.server_id ?? 0) === (app.value?.server_id ?? 0)),
)
const hiddenVolumeCount = computed(() => volumes.value.length - volumesOnNode.value.length)
const appDatabases = ref<AppDatabase[]>([])
const dbConnModal = ref<{ title: string; info: ConnectionInfo } | null>(null)
const mount = ref({ volume_id: 0, path: '' })

// Releases
const releases = ref<Release[]>([])

// Metrics
const metrics = ref<StatsSample | null>(null)

// Settings: full app configuration form (PATCH replaces these fields)
const registries = ref<Registry[]>([])
const gitRepos = ref<GitRepository[]>([])
const networks = ref<Network[]>([])
const stacks = ref<Stack[]>([])
// The workspace networks the app is attached to (always including the workspace
// default). In cluster mode these are Swarm overlays, which is what lets the app
// reach a database on another node.
const attachedNets = computed<Network[]>(() => app.value?.networks ?? [])
interface SettingsForm {
  image: string; tag: string; command: string; registry_id: number | null; git_repository_id: number | null
  git_repo: string; git_ref: string; build_method: BuildMethod; builder: string
  stack_id: number | null; network_ids: number[]; ports: AppPort[]
  deploy_strategy: DeployStrategy; canary_initial_weight: number; canary_step_weight: number; canary_step_interval_seconds: number
  // Resources (0 = unlimited)
  cpu_cores: number; memory_mb: number; gpu_count: number; gpu_kind: string; restart_policy: RestartPolicy; image_pull_policy: ImagePullPolicy
  // Healthcheck
  hc_type: HealthcheckType; hc_path: string; hc_port: number | null; hc_command: string
  hc_interval: number; hc_timeout: number; hc_retries: number; hc_start_period: number
}
function emptySettingsForm(): SettingsForm {
  return {
    image: '', tag: '', command: '', registry_id: null, git_repository_id: null, git_repo: '', git_ref: '', build_method: 'auto', builder: '', stack_id: null, network_ids: [], ports: [],
    deploy_strategy: 'rolling', canary_initial_weight: 10, canary_step_weight: 20, canary_step_interval_seconds: 60,
    cpu_cores: 0, memory_mb: 0, gpu_count: 0, gpu_kind: '', restart_policy: 'unless-stopped', image_pull_policy: 'always',
    hc_type: 'none', hc_path: '/', hc_port: null, hc_command: '', hc_interval: 30, hc_timeout: 5, hc_retries: 3, hc_start_period: 0,
  }
}
const settingsForm = ref<SettingsForm>(emptySettingsForm())
const limits = ref<ResourceLimits>({ max_cpu_cores: 0, max_memory_mb: 0 })

// splitCommand turns the command input into argv (whitespace-separated), matching
// how one-off Jobs parse their command. Empty = use the image's default CMD.
function splitCommand(s: string): string[] {
  return s.trim().split(/\s+/).filter(Boolean)
}

const HEALTHCHECK_TYPES: { value: HealthcheckType; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'http', label: 'HTTP' },
  { value: 'command', label: 'Command' },
]
const RESTART_POLICIES: { value: RestartPolicy; label: string }[] = [
  { value: 'unless-stopped', label: 'Unless stopped' },
  { value: 'always', label: 'Always' },
  { value: 'on-failure', label: 'On failure' },
  { value: 'no', label: 'No' },
]
const IMAGE_PULL_POLICIES: { value: ImagePullPolicy; label: string }[] = [
  { value: 'always', label: 'Always' },
  { value: 'if-not-present', label: 'If not present' },
  { value: 'never', label: 'Never' },
]
const MB = 1024 * 1024
// Cap guards (0 cap = unlimited). Used to disable Save + show inline errors.
const cpuOverCap = computed(() => limits.value.max_cpu_cores > 0 && settingsForm.value.cpu_cores > limits.value.max_cpu_cores)
const memOverCap = computed(() => limits.value.max_memory_mb > 0 && settingsForm.value.memory_mb > limits.value.max_memory_mb)
const resourcesValid = computed(() => !cpuOverCap.value && !memOverCap.value)
function addSettingsPort() {
  settingsForm.value.ports.push({ container_port: 0, protocol: 'tcp', scheme: 'http', name: '' })
}
function removeSettingsPort(i: number) {
  settingsForm.value.ports.splice(i, 1)
}
const savingSettings = ref(false)

// Deploy dialog
const showDeploy = ref(false)
const deployTag = ref('')
const deployStrategy = ref<DeployStrategy>('rolling')
const deploying = ref(false)

// Canary (live rollout state derived from the app)
const canaryActive = computed(() => !!app.value?.canary_release_id)
const canaryWeight = computed(() => app.value?.canary_weight ?? 0)
const canaryBusy = ref(false)
async function promoteCanary() {
  if (!wid.value || !app.value) return
  if (!(await askConfirm({
    title: 'Promote canary to stable?',
    message: `The canary release will take 100% of traffic and become the stable release of ${app.value.name}. The previous stable release is retired.`,
    confirmLabel: 'Promote now',
    cancelLabel: 'Cancel',
    variant: 'primary',
  }))) return
  canaryBusy.value = true
  try {
    const dep = (await appApi.promoteCanary(wid.value, appId.value)).data.data
    notify.success('Promoting canary…')
    tab.value = 'deployments'
    streamLogs(dep.id)
  } catch (e) { notify.apiError(e) } finally { canaryBusy.value = false }
}
async function abortCanary() {
  if (!wid.value || !app.value) return
  if (!(await askConfirm({
    title: 'Abort canary rollout?',
    message: `The canary container is stopped and discarded, and all traffic returns to the current stable release of ${app.value.name}.`,
    confirmLabel: 'Abort canary',
    cancelLabel: 'Keep rolling out',
    variant: 'danger',
  }))) return
  canaryBusy.value = true
  try {
    await appApi.abortCanary(wid.value, appId.value)
    notify.success('Canary aborted')
    loadApp()
  } catch (e) { notify.apiError(e) } finally { canaryBusy.value = false }
}

// Interactive shell modal. Gated by Admin+ role and the plan's shell-exec
// capability (resolved from the workspace usage endpoint).
const shellOpen = ref(false)
const processesOpen = ref(false)
const shellExecAllowed = ref(false)

// Custom container labels (Traefik &c.). Gated by the plan's custom-labels
// capability + global kill-switch (resolved from the usage endpoint). Reserved
// io.miabi.* / com.docker.* keys are rejected server-side; edits apply on the
// next deploy.
const containerLabels = ref<Record<string, string>>({})
const newLabel = ref({ key: '', value: '' })
const customLabelsAllowed = ref(false)

// GPU access. Gated by the plan's allow_gpu capability (resolved from the usage
// endpoint). When false the GPU controls are hidden entirely — no dangling field
// that always 403s.
const gpuAllowed = ref(false)

// Delete dialog (type-to-confirm)
const showDelete = ref(false)
const deleteConfirm = ref('')
const deleting = ref(false)

// Release management
const releaseDetail = ref<Release | null>(null)
const releaseBusy = ref<number | null>(null)

// When a saved repository is selected in settings, reflect its clone URL in the
// URL field (still editable as a per-app override).
function onSettingsRepoSelect() {
  const repo = gitRepos.value.find((r) => r.id === settingsForm.value.git_repository_id)
  if (repo) settingsForm.value.git_repo = repo.url
}

function syncSettingsForm() {
  if (!app.value) return
  settingsForm.value = {
    image: app.value.image || '',
    tag: app.value.tag || '',
    command: (app.value.command || []).join(' '),
    registry_id: app.value.registry_id ?? null,
    git_repository_id: app.value.git_repository_id ?? null,
    git_repo: app.value.git_repo || '',
    git_ref: app.value.git_ref || '',
    build_method: app.value.build_method || 'auto',
    builder: app.value.builder || '',
    stack_id: app.value.stack_id ?? null,
    network_ids: (app.value.networks || []).map((n: Network) => n.id),
    ports: (app.value.ports || []).map((p: AppPort) => ({ container_port: p.container_port, protocol: p.protocol, scheme: p.scheme || 'http', name: p.name })),
    deploy_strategy: app.value.deploy_strategy || 'rolling',
    canary_initial_weight: app.value.canary_initial_weight || 10,
    canary_step_weight: app.value.canary_step_weight || 20,
    canary_step_interval_seconds: app.value.canary_step_interval_seconds || 60,
    cpu_cores: app.value.nano_cpus ? +(app.value.nano_cpus / 1e9).toFixed(2) : 0,
    memory_mb: app.value.memory_bytes ? Math.round(app.value.memory_bytes / MB) : 0,
    gpu_count: app.value.gpu_count || 0,
    gpu_kind: app.value.gpu_kind || '',
    restart_policy: app.value.restart_policy || 'unless-stopped',
    image_pull_policy: app.value.image_pull_policy || 'always',
    hc_type: app.value.healthcheck_type || 'none',
    hc_path: app.value.healthcheck_http_path || '/',
    hc_port: app.value.healthcheck_port || null,
    hc_command: app.value.healthcheck_command || '',
    hc_interval: app.value.healthcheck_interval_seconds || 30,
    hc_timeout: app.value.healthcheck_timeout_seconds || 5,
    hc_retries: app.value.healthcheck_retries || 3,
    hc_start_period: app.value.healthcheck_start_period_seconds || 0,
  }
}

async function loadApp() {
  if (!wid.value || !appId.value) return
  try {
    app.value = (await appApi.get(wid.value, appId.value)).data.data
    syncSettingsForm()
  } catch (e) {
    notify.apiError(e)
  }
  // Resolve capabilities separately so a usage error never blocks the app from
  // loading; the shell icon simply stays hidden and the Labels panel read-only.
  try {
    const usage = (await usageApi.get(wid.value)).data.data
    shellExecAllowed.value = usage.capabilities.shell_exec
    customLabelsAllowed.value = usage.capabilities.custom_labels
    gpuAllowed.value = !!usage.limits.allow_gpu
  } catch {
    shellExecAllowed.value = false
    customLabelsAllowed.value = false
    gpuAllowed.value = false
  }
}

async function loadTab() {
  if (!wid.value) return
  // Tear down any live streams when leaving the events/logs tabs.
  closeStreams()
  try {
    if (tab.value === 'overview') await loadOverview()
    else if (tab.value === 'network') extAccess.value = (await appApi.externalAccess(wid.value, appId.value)).data.data ?? null
    else if (tab.value === 'events') await startEventsStream()
    else if (tab.value === 'logs') startRuntimeLogs()
    else if (tab.value === 'deployments') {
      deployments.value = (await appApi.deployments(wid.value, appId.value)).data.data ?? []
      // Show logs immediately: stream the live deployment, else the most recent,
      // unless one is already being followed.
      if (deployments.value.length && streamingId.value === null) {
        const target = deployments.value.find((d) => d.current) ?? deployments.value[0]
        streamLogs(target.id)
      }
    }
    else if (tab.value === 'environment') { envVars.value = (await appApi.envVars(wid.value, appId.value)).data.data ?? []; revealedEnv.value = {} }
    else if (tab.value === 'routes') appRoutes.value = (await routeApi.listByApp(wid.value, appId.value)).data.data ?? []
    else if (tab.value === 'ports') appBindings.value = (await portBindingApi.listByApp(wid.value, appId.value)).data.data ?? []
    else if (tab.value === 'volumes') { volumes.value = (await volumeApi.list(wid.value)).data.data ?? []; await loadHostPresets() }
    else if (tab.value === 'databases') appDatabases.value = (await appApi.databases(wid.value, appId.value)).data.data ?? []
    else if (tab.value === 'releases') releases.value = (await appApi.releases(wid.value, appId.value)).data.data ?? []
    else if (tab.value === 'settings') {
      registries.value = (await registryApi.list(wid.value)).data.data ?? []
      gitRepos.value = (await gitRepositoryApi.list(wid.value)).data.data ?? []
      networks.value = (await networkApi.list(wid.value)).data.data ?? []
      stacks.value = (await stackApi.list(wid.value)).data.data ?? []
      limits.value = (await appApi.resourceLimits(wid.value)).data.data ?? { max_cpu_cores: 0, max_memory_mb: 0 }
      containerLabels.value = (await appApi.labels(wid.value, appId.value)).data.data ?? {}
    }
  } catch (e) {
    notify.apiError(e)
  }
}

// closeStreams tears down per-tab streams (runtime logs). The events stream and
// status poll are page-level and live for the whole detail page.
function closeStreams() {
  runtimeES?.close()
  runtimeES = null
  runtimeConnected.value = false
}

function closeAllStreams() {
  closeStreams()
  eventsES?.close()
  eventsES = null
  if (statusPoll) { clearInterval(statusPoll); statusPoll = null }
}

// loadLiveStatus polls the real container status (best-effort).
async function loadLiveStatus() {
  if (!wid.value) return
  try {
    liveStatus.value = (await appApi.status(wid.value, appId.value)).data.data
  } catch { /* transient; keep last value */ }
}

async function loadOverview() {
  if (!wid.value) return
  overviewLoading.value = true
  metrics.value = null
  try {
    overview.value = (await appApi.overview(wid.value, appId.value)).data.data
    // Seed the events list (if not yet streaming) so the Overview shows history.
    if (appEvents.value.length === 0) appEvents.value = overview.value?.recent_events ?? []
    // Metrics snapshot is best-effort (409 when the app has no running container).
    try {
      metrics.value = (await monitoringApi.metrics(wid.value, appId.value)).data.data
    } catch {
      metrics.value = null
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    overviewLoading.value = false
  }
}

// Once an app has an active release, deploying again is a re-deploy.
const isDeployed = computed(() => !!app.value?.current_release_id || (overview.value?.current_version ?? 0) > 0)
const deployVerb = computed(() => (isDeployed.value ? 'Redeploy' : 'Deploy'))

// changeNote: config changes no longer auto-deploy — a deployed app is flagged
// as needing a redeploy, which the user applies manually.
function changeNote(): string {
  return isDeployed.value ? ' — redeploy required' : ''
}

// startEventsStream loads the event history and opens a single live SSE shared
// by the Overview "Latest events" card and the Events tab. Idempotent.
async function startEventsStream() {
  if (!wid.value || eventsES) return
  eventsLoading.value = true
  try {
    const first = (await eventsApi.list(wid.value, appId.value, undefined, EVENTS_PAGE)).data.data ?? []
    appEvents.value = first
    eventsHasMore.value = first.length >= EVENTS_PAGE
  } finally {
    eventsLoading.value = false
  }
  eventsES = new EventSource(eventsApi.streamUrl(wid.value, appId.value))
  eventsES.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as { type: string; data: AppEvent }
      if (msg.type === 'event' && msg.data) appEvents.value.unshift(msg.data)
    } catch { /* ignore */ }
  }
  eventsES.onerror = () => eventsES?.close()
}

// loadMoreEvents appends the next older page using the oldest loaded event as the
// cursor. Live events keep prepending via the SSE, so only the tail grows here.
async function loadMoreEvents() {
  if (!wid.value || appEvents.value.length === 0) return
  loadingMoreEvents.value = true
  try {
    const oldest = appEvents.value[appEvents.value.length - 1].id
    const older = (await eventsApi.list(wid.value, appId.value, oldest, EVENTS_PAGE)).data.data ?? []
    appEvents.value.push(...older)
    eventsHasMore.value = older.length >= EVENTS_PAGE
  } catch (e) {
    notify.apiError(e)
  } finally {
    loadingMoreEvents.value = false
  }
}

function startRuntimeLogs() {
  if (!wid.value) return
  runtimeLogs.value = []
  logsTrimmed.value = false
  logFollow.value = true // a fresh stream starts pinned to the latest output
  runtimeES = new EventSource(eventsApi.logsUrl(wid.value, appId.value))
  runtimeES.onopen = () => { runtimeConnected.value = true }
  runtimeES.onmessage = (ev) => {
    try {
      const l = JSON.parse(ev.data) as { stream?: string; text?: string }
      if (l.text != null) {
        runtimeLogs.value.push(l.text)
        if (runtimeLogs.value.length > RUNTIME_LOG_CAP) {
          runtimeLogs.value.splice(0, runtimeLogs.value.length - RUNTIME_LOG_CAP)
          logsTrimmed.value = true
        }
      }
    } catch { /* ignore */ }
  }
  runtimeES.onerror = () => { runtimeConnected.value = false; runtimeES?.close() }
}

async function saveSettings() {
  if (!wid.value || !app.value) return
  savingSettings.value = true
  try {
    app.value = (await appApi.update(wid.value, appId.value, {
      image: settingsForm.value.image.trim() || undefined,
      tag: settingsForm.value.tag.trim(),
      command: splitCommand(settingsForm.value.command),
      git_repo: app.value.source_type === 'git' ? settingsForm.value.git_repo.trim() : undefined,
      git_ref: app.value.source_type === 'git' ? settingsForm.value.git_ref.trim() : undefined,
      build_method: app.value.source_type === 'git' ? settingsForm.value.build_method : undefined,
      builder: app.value.source_type === 'git' && settingsForm.value.build_method !== 'dockerfile' ? settingsForm.value.builder.trim() : undefined,
      registry_id: settingsForm.value.registry_id,
      git_repository_id: settingsForm.value.git_repository_id,
      stack_id: settingsForm.value.stack_id,
      network_ids: settingsForm.value.network_ids,
      ports: settingsForm.value.ports.filter((p) => p.container_port > 0),
      deploy_strategy: settingsForm.value.deploy_strategy,
      canary_initial_weight: settingsForm.value.canary_initial_weight,
      canary_step_weight: settingsForm.value.canary_step_weight,
      canary_step_interval_seconds: settingsForm.value.canary_step_interval_seconds,
      memory_bytes: Math.max(0, Math.round((settingsForm.value.memory_mb || 0) * MB)),
      nano_cpus: Math.max(0, Math.round((settingsForm.value.cpu_cores || 0) * 1e9)),
      // Only send a GPU request when the plan allows it (the field is hidden
      // otherwise), so a disallowed workspace never trips the 403.
      gpu_count: gpuAllowed.value ? Math.max(0, Math.round(settingsForm.value.gpu_count || 0)) : 0,
      gpu_kind: gpuAllowed.value ? settingsForm.value.gpu_kind.trim() : '',
      restart_policy: settingsForm.value.restart_policy,
      image_pull_policy: settingsForm.value.image_pull_policy,
      healthcheck_type: settingsForm.value.hc_type,
      healthcheck_http_path: settingsForm.value.hc_path,
      healthcheck_port: settingsForm.value.hc_port || 0,
      healthcheck_command: settingsForm.value.hc_command,
      healthcheck_interval_seconds: settingsForm.value.hc_interval,
      healthcheck_timeout_seconds: settingsForm.value.hc_timeout,
      healthcheck_retries: settingsForm.value.hc_retries,
      healthcheck_start_period_seconds: settingsForm.value.hc_start_period,
    })).data.data
    syncSettingsForm()
    notify.success('Settings saved' + changeNote())
    loadApp() // refresh status + redeploy_required for the header
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingSettings.value = false
  }
}

function openBindReq(port?: AppPort) {
  bindForm.value = { container_port: port?.container_port || app.value?.port || 0, protocol: port?.protocol || 'tcp', host_port: 0 }
  showBindReq.value = true
}

function openAddPort() {
  portForm.value = { container_port: 0, protocol: 'tcp', scheme: 'http', name: '' }
  showAddPort.value = true
}
// addContainerPort / removeContainerPort declare/undeclare a container port from
// the Ports tab. The app Update endpoint replaces the whole port set (and other
// config), so reuse the Settings save path with the full current state rather
// than a partial update that would wipe unrelated fields.
async function addContainerPort() {
  if (!app.value || portForm.value.container_port <= 0) return
  const dup = (app.value.ports || []).some((p: AppPort) => p.container_port === portForm.value.container_port && p.protocol === portForm.value.protocol)
  if (dup) { notify.error(`Port ${portForm.value.container_port}/${portForm.value.protocol} is already declared`); return }
  syncSettingsForm()
  settingsForm.value.ports.push({ container_port: portForm.value.container_port, protocol: portForm.value.protocol, scheme: portForm.value.scheme, name: portForm.value.name.trim() })
  await saveSettings()
  showAddPort.value = false
}
async function removeContainerPort(p: AppPort) {
  if (!app.value) return
  if (!(await askConfirm({ title: `Remove container port ${p.container_port}/${p.protocol}?`, message: 'Any host binding for it should be released first. Applies on the next deploy.', confirmLabel: 'Remove', variant: 'danger' }))) return
  syncSettingsForm()
  settingsForm.value.ports = settingsForm.value.ports.filter((x) => !(x.container_port === p.container_port && x.protocol === p.protocol))
  await saveSettings()
}
async function requestBind() {
  if (!wid.value) return
  requestingBind.value = true
  try {
    const b = (await portBindingApi.request(wid.value, { application_id: appId.value, container_port: bindForm.value.container_port, protocol: bindForm.value.protocol, host_port: bindForm.value.host_port })).data.data
    showBindReq.value = false
    appBindings.value = (await portBindingApi.listByApp(wid.value, appId.value)).data.data ?? []
    if (b.status === 'approved') {
      // Auto-approved (privileged): the port only publishes on the next deploy.
      await loadApp() // refresh the "redeploy required" header indicator
      if (isDeployed.value && await askConfirm({ title: 'Host port bound', message: 'The port only publishes on the next deploy. Redeploy now to publish it?', confirmLabel: 'Redeploy now', cancelLabel: 'Later' })) {
        const dep = (await appApi.deploy(wid.value, appId.value, {})).data.data
        notify.success('Redeploying to publish the port')
        tab.value = 'deployments'
        streamLogs(dep.id)
      } else {
        notify.success(`Host port bound${changeNote()}`)
      }
    } else {
      notify.success('Host binding requested — pending admin approval')
    }
  } catch (e) {
    notify.apiError(e, 'Failed to request binding')
  } finally {
    requestingBind.value = false
  }
}
const suggestingPort = ref(false)
async function suggestPort() {
  if (!wid.value) return
  suggestingPort.value = true
  try {
    const port = (await portBindingApi.suggest(wid.value, appId.value, bindForm.value.protocol, bindForm.value.host_port)).data.data.host_port
    bindForm.value.host_port = port
  } catch (e) {
    notify.apiError(e, 'No free host port available')
  } finally {
    suggestingPort.value = false
  }
}
// removeBind withdraws a pending request, or releases an approved (live) host
// port. An approved port stays published until the app is redeployed, so offer
// to redeploy now and apply it.
async function removeBind(b: PortBinding) {
  if (!wid.value) return
  const approved = b.status === 'approved'
  const ok = await askConfirm(approved
    ? { title: `Release host port ${b.host_port}?`, message: 'It stays published until the app is redeployed.', confirmLabel: 'Release', variant: 'danger' }
    : { title: 'Cancel port binding?', message: 'Withdraw this pending host-port request.', confirmLabel: 'Cancel request', cancelLabel: 'Keep', variant: 'danger' })
  if (!ok) return
  try {
    await portBindingApi.cancel(wid.value, b.id)
    appBindings.value = (await portBindingApi.listByApp(wid.value, appId.value)).data.data ?? []
    if (approved && await askConfirm({ title: 'Binding released', message: 'Redeploy now to free the host port?', confirmLabel: 'Redeploy now', cancelLabel: 'Later' })) {
      const dep = (await appApi.deploy(wid.value, appId.value, {})).data.data
      notify.success('Redeploying to free the port')
      tab.value = 'deployments'
      streamLogs(dep.id)
    } else {
      notify.success(approved ? 'Binding released — redeploy the app to free the port' : 'Binding cancelled')
    }
  } catch (e) {
    notify.apiError(e)
  }
}
function bindBadge(s: string) {
  return s === 'approved' ? 'badge-success' : s === 'rejected' ? 'badge-danger' : 'badge-warning'
}

watch([appId, wid], loadApp, { immediate: true })
watch([tab, appId, wid], loadTab, { immediate: true })

// Keep the URL ?tab=… in sync with the active tab, and react to back/forward.
watch(tab, (t) => {
  if (route.query.tab !== t) router.replace({ query: { ...route.query, tab: t } })
}, { immediate: true })
watch(() => route.query.tab, () => {
  const t = tabFromQuery()
  if (t !== tab.value) tab.value = t
})

// Mirror the log search + panel size into the URL (?q=…&re=1&size=…) and react to
// back/forward. Uses replace (no history spam) and drops params at their default.
watch([logSearch, logRegexMode, logSize], ([q, re, size]) => {
  const next = { ...route.query }
  if (q.trim()) next.q = q; else delete next.q
  if (re) next.re = '1'; else delete next.re
  if (size !== 'small') next.size = size; else delete next.size
  if (next.q !== route.query.q || next.re !== route.query.re || next.size !== route.query.size) router.replace({ query: next })
})
watch(() => [route.query.q, route.query.re, route.query.size], ([q, re, size]) => {
  const qs = typeof q === 'string' ? q : ''
  if (qs !== logSearch.value) logSearch.value = qs
  const rb = re === '1'
  if (rb !== logRegexMode.value) logRegexMode.value = rb
  const sz = isLogSize(size) ? size : 'small'
  if (sz !== logSize.value) logSize.value = sz
})

// Page-level live data: a single events stream + a status poll for the whole
// detail page (independent of the active tab). This only tears down its OWN
// resources — the runtime logs stream is owned by loadTab (which also watches
// appId), so closing it here would race the initial loadTab and leave the Logs
// tab stuck on "connecting…" after a refresh straight into it.
watch([appId, wid], () => {
  eventsES?.close()
  eventsES = null
  if (statusPoll) { clearInterval(statusPoll); statusPoll = null }
  appEvents.value = []
  liveStatus.value = null
  startEventsStream()
  loadLiveStatus()
  statusPoll = setInterval(loadLiveStatus, 5000)
}, { immediate: true })

// While a canary is rolling out, poll the app so the auto-progress is visible.
let canaryPoll: ReturnType<typeof setInterval> | null = null
watch(canaryActive, (active) => {
  if (canaryPoll) { clearInterval(canaryPoll); canaryPoll = null }
  if (active) canaryPoll = setInterval(loadApp, 5000)
}, { immediate: true })

onBeforeUnmount(() => { es?.close(); closeAllStreams(); if (canaryPoll) clearInterval(canaryPoll) })

async function streamLogs(id: number) {
  es?.close()
  logs.value = []
  streamingId.value = id
  deployStreaming.value = true
  deployStatus.value = 'streaming'
  deployLogsTrimmed.value = false
  // A finished deployment loads its stored logs once (no dangling SSE); a running
  // one (or a freshly-started deploy not yet in the list) streams live below.
  const dep = deployments.value.find((d) => d.id === id)
  if (dep && wid.value != null && ['succeeded', 'running', 'failed'].includes(dep.status)) {
    const w = wid.value
    deployStreaming.value = false
    deployStatus.value = dep.status
    try {
      const hist = (await appApi.deploymentLogsHistory(w, appId.value, id)).data.data
      logs.value = hist.lines ?? []
      logs.value.push(`— status: ${hist.status} —`)
    } catch (e) { notify.apiError(e) }
    return
  }
  es = new EventSource(sseUrl(`${base.value}/deployments/${id}/logs`))
  es.onmessage = (ev) => {
    try {
      const e = JSON.parse(ev.data) as { type: string; data: unknown }
      if (e.type === 'log') {
        logs.value.push(String(e.data))
        // Cap the buffer so a very chatty build can't grow the DOM without bound.
        if (logs.value.length > RUNTIME_LOG_CAP) {
          logs.value.splice(0, logs.value.length - RUNTIME_LOG_CAP)
          deployLogsTrimmed.value = true
        }
      }
      if (e.type === 'status') {
        const s = String(e.data)
        deployStatus.value = s
        logs.value.push(`— status: ${s} —`)
        // Canary just went live (non-terminal): reveal the rollout card + refresh
        // the list, and keep the stream open for the live progression.
        if (e.data === 'canary') { loadApp(); loadTab() }
        else if (e.data === 'succeeded' || e.data === 'running' || e.data === 'failed') { deployStreaming.value = false; es?.close(); loadApp(); loadTab() }
      }
    } catch { /* ignore */ }
  }
  es.onerror = () => { deployStreaming.value = false; es?.close() }
}

// managedBy records how the app came to exist ('marketplace' | 'gitops' | 'user'
// | …), from the reserved miabi.io/managed-by metadata.
const managedBy = computed(() => app.value?.metadata?.['miabi.io/managed-by'] || '')

// imageManaged is true when the image/tag are owned by an external source of
// truth — a marketplace template or a GitOps manifest — so editing them in
// Settings is blocked: the change belongs in that source (a marketplace upgrade
// or the Git manifest), and an in-place edit would just drift or be overwritten.
const imageManaged = computed(() => managedBy.value === 'marketplace' || managedBy.value === 'gitops')

// template surfaces marketplace provenance (set on install via the reserved
// miabi.io/* metadata keys). When present, the app's image is owned by a
// template, so a manual image change is warned about and the proper upgrade path
// is offered. Returns null for non-marketplace apps.
const template = computed(() => {
  const m = app.value?.metadata ?? {}
  if (m['miabi.io/managed-by'] !== 'marketplace') return null
  const slug = m['miabi.io/template'] || ''
  return {
    slug, // stable template identity (the install id is workspace-local & mutable)
    name: slug || 'a marketplace template',
    version: m['miabi.io/template-version'] || '',
  }
})

// goToUpgrade leaves for the marketplace upgrade flow for this app's template,
// addressed by the template slug (the recommended way to change a template app's
// image). Slug is the portable identity across installs and the planned central
// template registry.
function goToUpgrade() {
  showDeploy.value = false
  // Use the real template handle (miabi.io/template), not the display fallback
  // (`name`, which can be "a marketplace template"). An empty handle falls back
  // to the marketplace Installed tab instead of pushing a broken route.
  const slug = template.value?.slug
  router.push(
    slug
      ? { name: 'template-install', params: { slug }, query: { upgrade: '1' } }
      : { name: 'marketplace', query: { tab: 'installed' } },
  )
}

// confirmTemplateImageChange gates a manual image/tag change on a template-managed
// app behind an explicit confirmation. Returns true to proceed, false to abort.
// Ad-hoc apps (no template) always proceed.
async function confirmTemplateImageChange(): Promise<boolean> {
  if (!template.value) return true
  return askConfirm({
    title: 'Change a template-managed image?',
    message: `${app.value?.name} was installed from the “${template.value.name}” template${template.value.version ? ` (v${template.value.version})` : ''}. Changing its image here means it no longer matches the template, and a future template upgrade may overwrite your change. Upgrade through the marketplace when you can.`,
    confirmLabel: 'Change anyway',
    cancelLabel: 'Cancel',
    variant: 'danger',
  })
}

function openDeploy() {
  deployTag.value = app.value?.tag || ''
  deployStrategy.value = app.value?.deploy_strategy || 'rolling'
  showDeploy.value = true
}

async function confirmDeploy() {
  if (!wid.value || !app.value) return
  // Warn before deploying a different image tag onto a template-managed app.
  if (app.value.source_type === 'image' && deployTag.value.trim() !== (app.value.tag || '')) {
    if (!(await confirmTemplateImageChange())) return
  }
  deploying.value = true
  try {
    const opts = {
      strategy: deployStrategy.value,
      ...(app.value.source_type === 'image' ? { tag: deployTag.value.trim() } : {}),
    }
    const dep = (await appApi.deploy(wid.value, appId.value, opts)).data.data
    notify.success('Deployment started')
    showDeploy.value = false
    tab.value = 'deployments'
    streamLogs(dep.id)
  } catch (e) {
    notify.apiError(e, 'Deploy failed')
  } finally {
    deploying.value = false
  }
}

// activate redeploys a previous release's exact image (rollback mechanism).
async function activate(releaseId: number) {
  if (!wid.value) return
  try {
    const dep = (await appApi.rollback(wid.value, appId.value, releaseId)).data.data
    notify.success('Redeploying release…')
    tab.value = 'deployments'
    streamLogs(dep.id)
  } catch (e) { notify.apiError(e) }
}

async function togglePin(r: Release) {
  if (!wid.value) return
  releaseBusy.value = r.id
  try {
    await appApi.pinRelease(wid.value, appId.value, r.id, !r.pinned)
    notify.success(r.pinned ? 'Release unpinned' : 'Release pinned')
    loadTab()
  } catch (e) {
    notify.apiError(e)
  } finally {
    releaseBusy.value = null
  }
}

async function deleteRelease(r: Release) {
  if (!wid.value) return
  if (!(await askConfirm({
    title: 'Delete release',
    message: `Delete release v${r.version}? This removes its retired container and history.`,
    confirmLabel: 'Delete',
    variant: 'danger',
  }))) return
  releaseBusy.value = r.id
  try {
    await appApi.deleteRelease(wid.value, appId.value, r.id)
    notify.success('Release deleted')
    if (releaseDetail.value?.id === r.id) releaseDetail.value = null
    loadTab()
  } catch (e) {
    notify.apiError(e, 'Cannot delete (active or pinned?)')
  } finally {
    releaseBusy.value = null
  }
}

async function setEnv() {
  if (!wid.value || !newEnv.value.key) return
  try {
    await appApi.setEnvVar(wid.value, appId.value, newEnv.value.key, newEnv.value.value, newEnv.value.secret)
    newEnv.value = { key: '', value: '', secret: false }
    notify.success('Environment variable set' + changeNote())
    loadApp()
    loadTab()
  } catch (e) { notify.apiError(e) }
}

async function delEnv(key: string) {
  if (!wid.value) return
  await appApi.deleteEnvVar(wid.value, appId.value, key).catch((e: unknown) => notify.apiError(e))
  loadTab()
  loadApp() // refresh the "redeploy required" header badge
}

// --- Custom container labels (Traefik &c.) ---

// Keys a user may never set — the server rejects them too; this is a fast
// client-side guard mirroring docker.IsReservedLabelKey.
const RESERVED_LABEL_PREFIXES = ['io.miabi.', 'miabi.', 'com.docker.']
function isReservedLabelKey(k: string): boolean {
  return RESERVED_LABEL_PREFIXES.some((p) => k.startsWith(p))
}

// PUT replaces the whole set: the panel edits locally then sends the full map.
async function setLabel() {
  if (!wid.value) return
  const key = newLabel.value.key.trim()
  if (!key) return
  if (isReservedLabelKey(key)) { notify.error(`"${key}" is reserved by Miabi and can't be used as a label`); return }
  const next = { ...containerLabels.value, [key]: newLabel.value.value }
  try {
    await appApi.setLabels(wid.value, appId.value, next)
    containerLabels.value = next
    newLabel.value = { key: '', value: '' }
    notify.success('Label set' + changeNote())
    loadApp() // refresh the "redeploy required" header badge
  } catch (e) { notify.apiError(e) }
}

async function delLabel(key: string) {
  if (!wid.value) return
  const next = { ...containerLabels.value }
  delete next[key]
  try {
    await appApi.setLabels(wid.value, appId.value, next)
    containerLabels.value = next
    notify.success('Label removed' + changeNote())
    loadApp()
  } catch (e) { notify.apiError(e) }
}

// editEnv loads a variable into the inline form to update its value (secret
// values aren't returned, so they're re-entered).
function editEnv(e: AppEnvVar) {
  newEnv.value = { key: e.key, value: e.is_secret ? '' : e.value, secret: e.is_secret }
}

// Secret env vars are masked in the list; reveal fetches the decrypted value on
// demand (admin only, audited server-side). Toggling again hides it.
async function toggleReveal(e: AppEnvVar) {
  if (revealedEnv.value[e.key] !== undefined) {
    const next = { ...revealedEnv.value }
    delete next[e.key]
    revealedEnv.value = next
    return
  }
  if (!wid.value) return
  revealingEnv.value = e.key
  try {
    const value = (await appApi.revealEnvVar(wid.value, appId.value, e.key)).data.data.value
    revealedEnv.value = { ...revealedEnv.value, [e.key]: value }
  } catch (err) {
    notify.apiError(err, 'Failed to reveal value')
  } finally {
    revealingEnv.value = ''
  }
}

async function importEnv() {
  if (!wid.value || !envImport.value.content.trim()) return
  importingEnv.value = true
  try {
    const res = (await appApi.importEnvVars(wid.value, appId.value, envImport.value.content, envImport.value.secret)).data.data
    notify.success(`Imported ${res?.imported ?? 0} variable(s)` + changeNote())
    showEnvImport.value = false
    envImport.value = { content: '', secret: false }
    loadApp()
    loadTab()
  } catch (e) { notify.apiError(e) }
  finally { importingEnv.value = false }
}

async function attachVolume() {
  if (!wid.value || !mount.value.volume_id || !mount.value.path) return
  try {
    await appApi.attachVolume(wid.value, appId.value, mount.value.volume_id, mount.value.path)
    notify.success('Volume attached' + changeNote())
    mount.value = { volume_id: 0, path: '' }
    loadApp()
  } catch (e) { notify.apiError(e) }
}

async function detachVolume(volumeId: number) {
  if (!wid.value) return
  await appApi.detachVolume(wid.value, appId.value, volumeId).catch((e: unknown) => notify.apiError(e))
  loadApp()
}

// --- Privileged host mounts (allow-listed; privileged workspaces only) ---
const hostPresets = ref<HostMountPreset[]>([])
const hostMount = ref({ preset: '', path: '', read_only: false })
// Only workspace admins in a privileged workspace may manage host binds.
const canHostMount = computed(() => !!ws.currentWorkspace?.privileged && ws.isWorkspaceAdmin)
// Split mounts: named volumes vs. privileged host binds.
const volumeMounts = computed(() => (app.value?.mounts ?? []).filter((m) => !m.host_preset))
const hostMounts = computed(() => (app.value?.mounts ?? []).filter((m) => m.host_preset))
const selectedPreset = computed(() => hostPresets.value.find((p) => p.key === hostMount.value.preset) || null)

function presetLabel(key?: string) {
  return hostPresets.value.find((p) => p.key === key)?.label || key || ''
}

async function loadHostPresets() {
  if (!wid.value || !canHostMount.value || hostPresets.value.length) return
  try {
    hostPresets.value = (await appApi.hostMountPresets(wid.value)).data.data ?? []
  } catch (e) { notify.apiError(e) }
}

function onPresetChange() {
  const p = selectedPreset.value
  hostMount.value.path = p?.default_target ?? ''
  hostMount.value.read_only = p?.default_read_only ?? false
}

async function attachHostMount() {
  if (!wid.value || !hostMount.value.preset) return
  try {
    await appApi.attachHostMount(wid.value, appId.value, hostMount.value.preset, hostMount.value.path, hostMount.value.read_only)
    notify.success('Host mount attached' + changeNote())
    hostMount.value = { preset: '', path: '', read_only: false }
    loadApp()
  } catch (e) { notify.apiError(e) }
}

async function detachHostMount(preset?: string) {
  if (!wid.value || !preset) return
  await appApi.detachHostMount(wid.value, appId.value, preset).catch((e: unknown) => notify.apiError(e))
  loadApp()
}

function openDelete() {
  deleteConfirm.value = ''
  showDelete.value = true
}

async function removeApp() {
  if (!wid.value || deleteConfirm.value !== app.value?.name) return
  deleting.value = true
  try {
    await appApi.remove(wid.value, appId.value)
    notify.success('Application deleted')
    router.push('/apps')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

function badge(s: string) {
  if (s === 'running' || s === 'completed') return 'badge-success'
  if (s === 'failed' || s === 'exited' || s === 'unhealthy') return 'badge-danger'
  if (s === 'stopped' || s === 'paused' || s === 'no_container') return 'badge-neutral'
  return 'badge-warning' // created, deploying, restarting, starting
}

// The status shown in the header: the live container status when available,
// falling back to the stored status.
const displayStatus = computed(() => liveStatus.value?.status || app.value?.status || 'created')

// A short live detail line for the header (uptime when running, restart count
// when looping).
const statusDetail = computed(() => {
  const ls = liveStatus.value
  if (!ls) return ''
  if (ls.status === 'restarting' && ls.restart_count > 0) return `restarted ${ls.restart_count}×`
  if (ls.status === 'exited') return ls.exit_code ? `exit ${ls.exit_code}` : ''
  if (ls.running && ls.uptime_seconds > 0) return `up ${fmtUptime(ls.uptime_seconds)}`
  return ''
})

function fmtUptime(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

function fmtMB(bytes?: number) {
  return ((bytes ?? 0) / 1048576).toFixed(0)
}
function fmtKB(bytes?: number) {
  return ((bytes ?? 0) / 1024).toFixed(0)
}

// Tone for a resource-usage bar by threshold.
function usageTone(pct: number): string {
  if (pct >= 90) return 'usage-danger'
  if (pct >= 70) return 'usage-warn'
  return 'usage-ok'
}

// Live container health badge for the overview (healthy/unhealthy/starting).
function healthBadge(h: string) {
  if (h === 'healthy') return 'badge-success'
  if (h === 'unhealthy') return 'badge-danger'
  return 'badge-warning'
}

// Deployment status badge: the live deployment is shown as a green "live"; other
// successful ones are neutral history; in-progress states are amber.
function depBadge(s: string) {
  if (s === 'failed') return 'badge-danger'
  if (s === 'succeeded' || s === 'running') return 'badge-neutral'
  return 'badge-warning'
}

// Cluster (service) runtime: scaling controls on the overview.
const isService = computed(() => app.value?.runtime_kind === 'service')
const replicaTarget = computed(() => liveStatus.value?.service_replicas || app.value?.replicas || 1)
// Real replica placement for a service app: where Swarm actually scheduled the
// running tasks (populated on the app-detail read). Empty until tasks are running.
const nodePlacement = computed(() => app.value?.nodes ?? [])
const nodePlacementLabel = computed(() =>
  nodePlacement.value.map((n: NodePlacement) => `${n.name} (${n.tasks})`).join(' · '),
)
const scaleBusy = ref(false)
async function scaleBy(delta: number) {
  if (!wid.value || scaleBusy.value) return
  const next = Math.max(1, replicaTarget.value + delta)
  if (next === replicaTarget.value) return
  scaleBusy.value = true
  try {
    await appApi.scale(wid.value, appId.value, next)
    notify.success(`Scaling to ${next} replica(s)`)
    await loadApp()
    await loadLiveStatus()
  } catch (e) {
    notify.apiError(e, 'Failed to scale')
  } finally {
    scaleBusy.value = false
  }
}

const lifecycleBusy = ref('')
async function lifecycle(action: 'start' | 'stop' | 'restart') {
  if (!wid.value) return
  lifecycleBusy.value = action
  try {
    const data = (await appApi[action](wid.value, appId.value)).data.data
    // Start/Restart on an app with pending changes returns a deployment (a
    // redeploy) — follow its logs; otherwise it's a plain lifecycle action.
    if (data && 'id' in data) {
      notify.success('Redeploying with the latest changes')
      tab.value = 'deployments'
      streamLogs(data.id)
    } else {
      notify.success(`Application ${action} requested`)
    }
    await loadApp()
  } catch (e) {
    notify.apiError(e, `Failed to ${action}`)
  } finally {
    lifecycleBusy.value = ''
  }
}

function eventIcon(type: string): string {
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

async function copy(text: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    notify.success('Copied')
  } catch {
    notify.error('Copy failed')
  }
}

async function revealDatabase(d: AppDatabase) {
  if (!wid.value) return
  try {
    const info = (await appApi.databaseConnection(wid.value, appId.value, d.id)).data.data
    if (info) dbConnModal.value = { title: d.name, info }
  } catch (e) { notify.apiError(e) }
}

// --- Link existing / create-and-attach a database ---
const linkModal = ref(false)
const dbInstances = ref<DatabaseInstance[]>([])
const selInstance = ref<DatabaseInstance | null>(null)
const instDatabases = ref<LogicalDatabase[]>([])
const linkMode = ref<'existing' | 'new'>('existing')
const linkForm = ref({ database_id: 0, new_name: '', env_prefix: '' })
const linkBusy = ref(false)

// Only instances on the app's node can be attached (no cross-node service DNS).
const instancesOnNode = computed(() => dbInstances.value.filter((i) => (i.server_id ?? 0) === (app.value?.server_id ?? 0)))
const hiddenInstanceCount = computed(() => dbInstances.value.length - instancesOnNode.value.length)
// Logical DBs free to attach (not already owned by an app).
const freeDatabases = computed(() => instDatabases.value.filter((d) => !d.application_id))
// Warn when an unprefixed attachment already exists and the user adds another.
const hasUnprefixed = computed(() => appDatabases.value.some((d) => !d.env_prefix))

async function openLink() {
  if (!wid.value) return
  linkModal.value = true
  selInstance.value = null
  instDatabases.value = []
  linkMode.value = 'existing'
  linkForm.value = { database_id: 0, new_name: '', env_prefix: '' }
  try {
    dbInstances.value = (await databaseApi.list(wid.value)).data.data ?? []
  } catch (e) { notify.apiError(e) }
}

async function selectLinkInstance(inst: DatabaseInstance) {
  if (!wid.value) return
  selInstance.value = inst
  linkForm.value.database_id = 0
  linkMode.value = 'existing'
  try {
    instDatabases.value = (await databaseApi.listDatabases(wid.value, inst.id)).data.data ?? []
  } catch (e) { notify.apiError(e); instDatabases.value = [] }
}

async function confirmLink() {
  if (!wid.value || !selInstance.value) return
  const prefix = linkForm.value.env_prefix.trim()
  linkBusy.value = true
  try {
    if (linkMode.value === 'new') {
      if (!linkForm.value.new_name.trim()) { notify.error('Enter a database name'); return }
      // Create unattached, then attach so the prefix is honored uniformly.
      const created = (await databaseApi.createDatabase(wid.value, selInstance.value.id, linkForm.value.new_name.trim(), null)).data.data
      await appApi.attachDatabase(wid.value, appId.value, created.database.id, prefix)
    } else {
      if (!linkForm.value.database_id) { notify.error('Select a database'); return }
      await appApi.attachDatabase(wid.value, appId.value, linkForm.value.database_id, prefix)
    }
    notify.success('Database attached' + changeNote())
    linkModal.value = false
    appDatabases.value = (await appApi.databases(wid.value, appId.value)).data.data ?? []
    loadApp()
  } catch (e) { notify.apiError(e) } finally { linkBusy.value = false }
}

async function detachDatabase(d: AppDatabase) {
  if (!wid.value) return
  try {
    await appApi.detachDatabase(wid.value, appId.value, d.id)
    notify.success('Database detached' + changeNote())
    appDatabases.value = (await appApi.databases(wid.value, appId.value)).data.data ?? []
    loadApp()
  } catch (e) { notify.apiError(e) }
}
</script>

<template>
  <div v-if="app">
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/apps')">
          <span class="mdi mdi-arrow-left"></span> Applications
        </button>
        <div class="flex items-center gap-3" style="margin-top: 8px">
          <ResourceIcon :src="app.icon" mdi="mdi-cube-outline" :name="app.name" :size="44" />
          <div>
            <h1>{{ app.display_name || app.name }}</h1>
            <div class="text-muted text-sm">
              <span class="mdi" :class="app.source_type === 'git' ? 'mdi-git' : 'mdi-docker'"></span>
              {{ app.image || app.git_repo }}
              <!-- Container apps run on one node; a service app's replicas are spread
                   by the scheduler, so its real placement is shown in the overview. -->
              <template v-if="!isService && app.server_name"> · <span class="mdi mdi-server-network"></span> {{ app.server_name }}</template>
              <template v-else-if="isService && nodePlacement.length"> · <span class="mdi mdi-server-network"></span> {{ nodePlacementLabel }}</template>
              <!-- Provenance: where this app came from, linking to its source. -->
              <template v-if="template"> · <span class="mdi mdi-storefront-outline"></span>
                <router-link
                  class="prov-link"
                  :to="{ name: 'template-install', params: { slug: template.name } }"
                  :title="`Installed from the “${template.name}” marketplace template`"
                >{{ template.name }}<template v-if="template.version"> v{{ template.version }}</template></router-link>
              </template>
              <template v-else-if="managedBy === 'gitops'"> · <span class="mdi mdi-source-branch"></span>
                <router-link class="prov-link" :to="{ name: 'gitops' }" title="Managed by GitOps">GitOps</router-link>
              </template>
            </div>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <span v-if="app.redeploy_required" class="badge badge-warning" title="Configuration changed since the last deploy — click Redeploy (or Restart) to apply it">
          <span class="mdi mdi-alert-outline"></span> Redeploy required
        </span>
        <span class="status-wrap">
          <span class="badge badge-dot" :class="badge(displayStatus)" :title="liveStatus?.container_state ? `container: ${liveStatus.container_state}` : ''">{{ displayStatus }}</span>
          <span v-if="statusDetail" class="text-muted text-sm status-detail">{{ statusDetail }}</span>
        </span>
        <button
          class="btn btn-secondary btn-sm"
          :disabled="app.status !== 'running'"
          :title="app.status === 'running' ? 'View live container processes' : 'Start the container to view processes'"
          @click="processesOpen = true"
        >
          <span class="mdi mdi-format-list-bulleted"></span> Processes
        </button>
        <template v-if="ws.canEdit">
          <button v-if="app.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" title="Stop" @click="lifecycle('stop')">
            <span class="mdi mdi-stop"></span> Stop
          </button>
          <button v-else class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" title="Start" @click="lifecycle('start')">
            <span class="mdi mdi-play"></span> Start
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" title="Restart" @click="lifecycle('restart')">
            <span class="mdi" :class="lifecycleBusy === 'restart' ? 'mdi-loading mdi-spin' : 'mdi-restart'"></span> Restart
          </button>
          <button
            v-if="ws.isWorkspaceAdmin && shellExecAllowed"
            class="btn btn-secondary btn-sm"
            :disabled="app.status !== 'running'"
            :title="app.status === 'running' ? 'Open a shell in the container' : 'Start the container to open a shell'"
            @click="shellOpen = true"
          >
            <span class="mdi mdi-console-line"></span> Shell
          </button>
        </template>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openDeploy">
          <span class="mdi mdi-rocket-launch-outline"></span> {{ deployVerb }}
        </button>
      </div>
    </div>

    <!-- Header strip is a shortcut into the rollout; on the Deployments tab the
         full canary card is already shown, so it would be redundant there. -->
    <button v-if="canaryActive && tab !== 'deployments'" class="canary-strip" title="Canary rollout in progress — click to manage" @click="tab = 'deployments'">
      <span class="mdi mdi-rocket-launch-outline"></span>
      <span class="canary-strip-text">Canary {{ canaryWeight }}%</span>
      <span class="split-bar canary-strip-bar">
        <span class="split-stable" :style="{ width: (100 - canaryWeight) + '%' }"></span>
        <span class="split-canary" :style="{ width: canaryWeight + '%' }"></span>
      </span>
    </button>

    <div class="tabs">
      <button v-for="t in tabs" :key="t.key" class="tab" :class="{ active: tab === t.key }" @click="tab = t.key">{{ t.label }}</button>
    </div>

    <!-- Overview -->
    <div v-if="tab === 'overview'">
      <div v-if="overview" class="card summary mb-4">
        <div class="summary-item">
          <span class="summary-label">Status</span>
          <span class="badge badge-dot" :class="badge(displayStatus)">{{ displayStatus }}</span>
        </div>
        <div v-if="isService" class="summary-item">
          <span class="summary-label">Replicas</span>
          <span class="summary-value" style="display: inline-flex; align-items: center; gap: 6px">
            <button class="btn-icon btn-icon-muted" :disabled="scaleBusy || replicaTarget <= 1" title="Scale down" aria-label="Scale down" @click="scaleBy(-1)"><span class="mdi mdi-minus"></span></button>
            <span><span class="mdi mdi-server-network"></span> {{ liveStatus?.service_running_tasks ?? 0 }}/{{ replicaTarget }}</span>
            <button class="btn-icon btn-icon-muted" :disabled="scaleBusy" title="Scale up" aria-label="Scale up" @click="scaleBy(1)"><span class="mdi mdi-plus"></span></button>
          </span>
        </div>
        <!-- Real replica placement: where the Swarm scheduler actually put the
             running tasks, not the single node the app was created against. -->
        <div v-if="isService && nodePlacement.length" class="summary-item">
          <span class="summary-label">Nodes</span>
          <span class="summary-value" :title="`Running on ${nodePlacementLabel}`">
            <span class="mdi mdi-server-network"></span> {{ nodePlacementLabel }}
          </span>
        </div>
        <div v-if="liveStatus?.running && liveStatus.uptime_seconds > 0" class="summary-item">
          <span class="summary-label">Uptime</span>
          <span class="summary-value"><span class="mdi mdi-clock-outline"></span> {{ fmtUptime(liveStatus.uptime_seconds) }}</span>
        </div>
        <div v-if="liveStatus?.health" class="summary-item">
          <span class="summary-label">Health</span>
          <span class="badge" :class="healthBadge(liveStatus.health)">{{ liveStatus.health }}</span>
        </div>
        <div v-if="liveStatus && liveStatus.restart_count > 0" class="summary-item">
          <span class="summary-label">Restarts</span>
          <span class="summary-value">{{ liveStatus.restart_count }}×</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">{{ overview.source_type === 'git' ? 'Source' : 'Image tag' }}</span>
          <span class="summary-value">
            <template v-if="overview.source_type === 'git'">
              <span class="mdi mdi-git"></span> {{ overview.git_repo || 'Git repository' }}
            </template>
            <template v-else>
              <span class="mdi mdi-tag-outline"></span> {{ overview.tag || 'latest' }}
            </template>
          </span>
        </div>
        <div class="summary-item clickable" @click="tab = 'releases'">
          <span class="summary-label">Current release</span>
          <span class="summary-value">{{ overview.current_version ? `v${overview.current_version}` : '—' }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'volumes'">
          <span class="summary-label">Volumes</span>
          <span class="summary-value">{{ overview.volumes_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'routes'">
          <span class="summary-label">Routes</span>
          <span class="summary-value">{{ overview.routes_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'network'">
          <span class="summary-label">Networks</span>
          <span class="summary-value">{{ overview.networks_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'environment'">
          <span class="summary-label">Env vars</span>
          <span class="summary-value">{{ overview.env_count }}</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">Created</span>
          <span class="summary-value">{{ overview.created_at ? new Date(overview.created_at).toLocaleDateString() : '—' }}</span>
        </div>
      </div>

      <h2 class="section-title">
        Resource usage
        <span v-if="metrics" class="live-tag"><span class="live-dot"></span> live</span>
      </h2>
      <div v-if="metrics" class="stats-grid">
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">CPU</span><span class="stat-icon stat-icon-primary"><span class="mdi mdi-chip"></span></span></div>
          <div class="stat-value">{{ metrics.cpu_percent.toFixed(1) }}%</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.cpu_percent)" :style="{ width: Math.min(100, metrics.cpu_percent) + '%' }"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Memory</span><span class="stat-icon stat-icon-info"><span class="mdi mdi-memory"></span></span></div>
          <div class="stat-value">{{ fmtMB(metrics.memory_usage_bytes) }} MB</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.memory_percent)" :style="{ width: Math.min(100, metrics.memory_percent) + '%' }"></div></div>
          <div class="stat-sub">
            {{ metrics.memory_percent.toFixed(1) }}%<template v-if="metrics.memory_limit_bytes"> of {{ fmtMB(metrics.memory_limit_bytes) }} MB</template>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Net in</span><span class="stat-icon stat-icon-success"><span class="mdi mdi-download"></span></span></div>
          <div class="stat-value">{{ fmtKB(metrics.network_rx_bytes) }} KB</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Net out</span><span class="stat-icon stat-icon-warning"><span class="mdi mdi-upload"></span></span></div>
          <div class="stat-value">{{ fmtKB(metrics.network_tx_bytes) }} KB</div>
        </div>
      </div>
      <div v-else class="card mb-4">
        <div class="empty-state" style="padding: 28px">
          <span class="mdi mdi-chart-line" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>No live metrics — the app has no running container.</p>
        </div>
      </div>

      <MetadataCard :metadata="app?.metadata" style="margin-top: 20px" />

      <MetadataCard :metadata="app?.annotations" title="Annotations" :reserved="false" style="margin-top: 20px" />

      <div class="card" style="margin-top: 20px">
        <div class="card-header">
          <h2>Latest events</h2>
          <button class="btn btn-ghost btn-sm" @click="tab = 'events'">View all</button>
        </div>
        <div v-if="overviewLoading && latestEvents.length === 0" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="latestEvents.length === 0" class="empty-state" style="padding: 28px">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>No events yet.</p>
        </div>
        <ul v-else class="timeline">
          <li v-for="e in latestEvents" :key="e.id" class="event">
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
      </div>
    </div>

    <!-- Network -->
    <div v-else-if="tab === 'network'">
      <div class="card mb-4">
        <div class="card-header"><h2>Internal access</h2></div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-top: 0">
            Reach this app from other apps on the same network by its hostname. The hostname is
            <strong>stable across redeploys</strong> — prefer it over the IP below.
          </p>
          <div class="net-row">
            <span class="net-label">Hostname</span>
            <div class="net-vals">
              <span class="net-chip"><code>{{ hostname }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(hostname)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
              <span v-for="p in networkPorts" :key="`h${p}`" class="net-chip"><code>{{ hostname }}:{{ p }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`${hostname}:${p}`)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
            </div>
          </div>
          <div v-if="stackHostname" class="net-row">
            <span class="net-label">Stack hostname</span>
            <div class="net-vals">
              <span class="net-chip"><code>{{ stackHostname }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(stackHostname)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
              <span v-for="p in networkPorts" :key="`s${p}`" class="net-chip"><code>{{ stackHostname }}:{{ p }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`${stackHostname}:${p}`)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
            </div>
            <p class="net-hint">Resolves only for apps in the same stack.</p>
          </div>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>External access</h2></div>
        <div class="card-body">
          <template v-if="extAccess && !extAccess.enabled">
            <p class="text-muted text-sm" style="margin-top: 0">
              Expose this app on the internet with an auto-generated URL. An admin must first set the
              <strong>external base domain</strong> in platform settings.
            </p>
          </template>
          <template v-else-if="extAccess">
            <p class="text-muted text-sm" style="margin-top: 0">
              Pick the HTTP ports to expose. Miabi generates a public hostname + TLS route for each, under
              <code>*.{{ extAccess.base_domain }}</code>.
            </p>
            <div v-if="networkPorts.length === 0" class="text-muted text-sm">Declare container ports in Settings first.</div>
            <div v-else class="ext-ports">
              <div v-for="p in networkPorts" :key="`ext${p}`" class="ext-row">
                <label class="ext-toggle">
                  <input type="checkbox" :checked="extSelected.has(p)" :disabled="!ws.canEdit" @change="toggleExtPort(p)" />
                  <code>{{ p }}</code>
                </label>
                <a v-if="extUrlFor(p)" class="host-link" :href="extUrlFor(p)" target="_blank" rel="noopener">{{ extUrlFor(p) }}<span class="mdi mdi-open-in-new"></span></a>
                <span v-else-if="extSelected.has(p)" class="text-muted text-sm">URL generated on save</span>
              </div>
            </div>
            <div v-if="ws.canEdit" class="flex items-center gap-2 mt-4">
              <button class="btn btn-primary btn-sm" :disabled="extSaving || !extDirty" @click="saveExternalAccess">
                {{ extSaving ? 'Saving…' : 'Save external access' }}
              </button>
              <button v-if="extExposedCount > 0" class="btn btn-secondary btn-sm" :disabled="extSaving" @click="disableExternalAccess">
                Disable external access
              </button>
            </div>
          </template>
        </div>
      </div>

      <!-- Which workspace networks the app is attached to. The default network is
           always one of them, and in cluster mode it is a Swarm overlay — which is
           what lets the app reach a database on another node. -->
      <div class="card mb-4">
        <div class="card-header">
          <h2>Networks</h2>
          <span class="text-muted text-sm">Workspace networks this app is reachable on.</span>
        </div>
        <div v-if="attachedNets.length === 0" class="card-body text-muted text-sm">
          Not connected to any network yet.
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Network</th><th>Docker name</th><th>Driver</th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">
                  {{ n.name }}
                  <span v-if="n.is_default" class="badge badge-info" style="margin-left: 6px">default</span>
                </td>
                <td class="cell-sub mono">{{ n.docker_name }}</td>
                <td class="cell-sub">
                  {{ n.driver }}<span v-if="n.internal"> · internal</span>
                  <span v-if="n.driver === 'overlay'" class="text-muted"> · spans nodes</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="card-body text-muted text-sm" style="padding-top: 0">
          Attach or detach networks in <a class="net-link" @click="tab = 'settings'">Settings</a>.
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>Container IP</h2></div>
        <div class="card-body" style="padding-bottom: 4px">
          <p class="text-muted text-sm" style="margin-top: 0">
            Live address of the running container. <strong>Ephemeral</strong> — it changes on every
            redeploy, so use the hostname for anything persistent.
          </p>
        </div>
        <!-- A replicated service has no single container IP: Swarm may run its tasks
             on any node, and they are replaced on every update. Say that, rather than
             falling through to "no IP yet", which reads as broken for a healthy app. -->
        <div v-if="isService" class="card-body text-muted text-sm" style="padding-top: 0">
          A replicated service has no single container IP — its tasks can run on any node and are
          replaced on each update. Use the hostname above; Swarm load-balances it across the replicas.
        </div>
        <div v-else-if="liveStatus?.networks?.length" class="table-wrapper">
          <table>
            <thead><tr><th>Network</th><th>IP address</th><th>Gateway</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in liveStatus.networks" :key="n.name">
                <td class="cell-sub">{{ n.name }}</td>
                <td class="mono"><code>{{ n.ip_address }}</code></td>
                <td class="cell-sub mono">{{ n.gateway || '—' }}</td>
                <td class="text-right">
                  <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(n.ip_address)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="card-body text-muted text-sm" style="padding-top: 0">No IP yet — available once the container is running.</div>
      </div>

      <div class="card">
        <div class="card-body text-sm text-muted">
          Need a public URL or a host port? Use the
          <a class="net-link" @click="tab = 'routes'">Routes</a> tab for domains and the
          <a class="net-link" @click="tab = 'ports'">Ports</a> tab for published host ports.
        </div>
      </div>
    </div>

    <!-- Events -->
    <div v-else-if="tab === 'events'" class="card">
      <div class="card-header">
        <h2>Events</h2>
        <span class="live-dot" title="Live"></span>
      </div>
      <div v-if="eventsLoading && appEvents.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="appEvents.length === 0" class="empty-state">
        <span class="mdi mdi-timeline-text-outline" style="font-size: 40px; color: var(--text-muted)"></span>
        <h3>No events yet</h3>
        <p>Deploys, container lifecycle, and config changes will show up here.</p>
      </div>
      <template v-else>
        <ul class="timeline">
          <li v-for="e in appEvents" :key="e.id" class="event">
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
        <div v-if="eventsHasMore" class="text-center" style="padding: 8px 0 4px">
          <button class="btn btn-secondary btn-sm" :disabled="loadingMoreEvents" @click="loadMoreEvents">{{ loadingMoreEvents ? 'Loading…' : 'Load more' }}</button>
        </div>
      </template>
    </div>

    <!-- Logs (runtime) -->
    <div v-else-if="tab === 'logs'" class="card">
      <div class="card-header">
        <div class="log-header-left">
          <h2>Runtime logs</h2>
          <div class="log-size-control" role="group" aria-label="Log panel size">
            <button
              v-for="s in LOG_SIZES"
              :key="s.value"
              type="button"
              class="log-size-btn"
              :class="{ active: logSize === s.value }"
              :aria-pressed="logSize === s.value"
              :title="s.title"
              @click="logSize = s.value"
            >{{ s.label }}</button>
          </div>
        </div>
        <div class="log-toolbar">
          <div class="log-search" :class="{ 'log-search-error': logRegexError }">
            <input
              v-model="logSearch"
              type="search"
              class="form-input log-search-input"
              :placeholder="logRegexMode ? 'Search logs (regex)…' : 'Search logs…'"
              aria-label="Search runtime logs"
            />
            <button v-if="logSearch" type="button" class="log-search-clear" @click="logSearch = ''">Clear</button>
          </div>
          <button
            type="button"
            class="log-regex-toggle"
            :class="{ active: logRegexMode }"
            :aria-pressed="logRegexMode"
            title="Match using a regular expression"
            @click="logRegexMode = !logRegexMode"
          >.*</button>
          <span v-if="logRegexError" class="text-sm log-regex-err">invalid regex</span>
          <span v-else-if="logSearch.trim()" class="text-muted text-sm log-match-count">{{ filteredRuntimeLogs.length }} / {{ runtimeLogs.length }}</span>
          <button
            type="button"
            class="log-icon-btn"
            :disabled="!filteredRuntimeLogs.length"
            :title="logSearch.trim() ? 'Copy matching lines' : 'Copy logs'"
            aria-label="Copy logs"
            @click="copyLogs"
          >
            <span class="mdi" :class="logCopied ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
          <button
            type="button"
            class="log-icon-btn"
            :disabled="!filteredRuntimeLogs.length"
            :title="logSearch.trim() ? 'Download matching lines' : 'Download logs'"
            aria-label="Download logs"
            @click="downloadLogs"
          >
            <span class="mdi mdi-tray-arrow-down"></span>
          </button>
          <button
            type="button"
            class="log-follow-btn"
            :class="{ active: logFollow }"
            :aria-pressed="logFollow"
            :title="logFollow ? 'Following new output — click to pause' : 'Jump to latest and follow'"
            @click="toggleLogFollow"
          >
            <span class="mdi mdi-chevron-double-down"></span>
            {{ logFollow ? 'Following' : 'Follow' }}
          </button>
          <span class="badge" :class="runtimeConnected ? 'badge-success badge-dot' : 'badge-neutral'">{{ runtimeConnected ? 'live' : 'connecting…' }}</span>
        </div>
      </div>
      <div class="card-body">
        <p v-if="logsTrimmed" class="text-muted text-sm log-trim-note">Showing the most recent {{ RUNTIME_LOG_CAP.toLocaleString() }} lines — older output was trimmed.</p>
        <div ref="logViewEl" class="code-block log-view" :style="logViewStyle" @scroll="onLogScroll">
          <span v-if="!runtimeLogs.length" class="log-placeholder">Waiting for output… (the app must have a running container)</span>
          <span v-else-if="!filteredRuntimeLogs.length" class="log-placeholder">No lines match your search.</span>
          <template v-else>
            <div v-for="(line, i) in filteredRuntimeLogs" :key="i" class="log-line"><span v-for="(seg, j) in logSegments(line)" :key="j" :class="{ 'log-hit': seg.hit }">{{ seg.text }}</span></div>
          </template>
        </div>
      </div>
    </div>

    <!-- Deployments -->
    <div v-else-if="tab === 'deployments'" class="detail-grid">
      <div v-if="canaryActive" class="card canary-card">
        <div class="card-header">
          <h2>Canary rollout</h2>
          <span class="badge badge-warning badge-dot">in progress</span>
        </div>
        <div class="card-body">
          <div class="split-bar lg">
            <div class="split-stable" :style="{ width: (100 - canaryWeight) + '%' }"><span v-if="100 - canaryWeight >= 15">stable {{ 100 - canaryWeight }}%</span></div>
            <div class="split-canary" :style="{ width: canaryWeight + '%' }"><span v-if="canaryWeight >= 15">canary {{ canaryWeight }}%</span></div>
          </div>
          <p class="text-muted text-sm mt-4">
            The platform is shifting traffic to the new release automatically and will promote it at 100%
            (or roll back if it becomes unhealthy). You can override below.
          </p>
          <div v-if="ws.canEdit" class="canary-actions">
            <button class="btn btn-primary btn-sm" :disabled="canaryBusy" @click="promoteCanary"><span class="mdi mdi-rocket-launch-outline"></span> Promote now</button>
            <button class="btn btn-danger btn-sm" :disabled="canaryBusy" @click="abortCanary"><span class="mdi mdi-close-circle-outline"></span> Abort</button>
          </div>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><h2>Deployments</h2></div>
        <div v-if="deployments.length === 0" class="empty-state">
          <span class="mdi mdi-rocket-launch-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No deployments yet.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Deployment</th><th class="text-right">Status</th></tr></thead>
            <tbody>
              <tr v-for="d in deployments" :key="d.id" class="row-clickable" :class="{ 'row-selected': streamingId === d.id }" @click="streamLogs(d.id)">
                <td><span class="cell-title">#{{ d.number }}</span><div class="cell-sub">{{ d.trigger }}</div></td>
                <td class="text-right">
                  <span v-if="d.current" class="badge badge-success badge-dot">live</span>
                  <span v-else class="badge" :class="depBadge(d.status)">{{ d.status }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><h2>Logs <span v-if="streamingNumber" class="cell-sub">#{{ streamingNumber }}</span></h2></div>
        <div class="card-body">
          <LogViewer
            :lines="logs"
            :streaming="deployStreaming"
            :status-label="deployStatus"
            :status-class="depStatusClass(deployStatus)"
            :trimmed-note="deployLogsTrimmed ? `Showing the most recent ${RUNTIME_LOG_CAP.toLocaleString()} lines — older output was trimmed.` : ''"
            placeholder="Select a deployment or click Deploy to stream logs."
            :download-name="`${app?.name || 'app'}-deployment-${streamingNumber ?? ''}`"
            search-label="Search deployment logs"
          />
        </div>
      </div>
    </div>

    <!-- Environment -->
    <div v-else-if="tab === 'environment'" class="card">
      <div class="card-header">
        <h2>Environment variables</h2>
        <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" @click="showEnvImport = true"><span class="mdi mdi-import"></span> Import .env</button>
      </div>
      <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
        <form class="flex items-center gap-2" @submit.prevent="setEnv">
          <input v-model="newEnv.key" class="form-input" aria-label="Environment variable name" placeholder="KEY" style="max-width: 200px" />
          <input v-model="newEnv.value" class="form-input" aria-label="Environment variable value" placeholder="value" style="max-width: 240px" />
          <label class="checkbox-label"><input type="checkbox" v-model="newEnv.secret" /> secret</label>
          <button class="btn btn-primary">Set</button>
        </form>
        <p class="text-muted text-sm" style="margin: 8px 0 0">
          Reference a workspace secret with <code>{{ secretRefHint }}</code> — resolved at deploy time.
          <RouterLink to="/secrets">Manage secrets</RouterLink>
        </p>
      </div>
      <div v-if="envVars.length === 0" class="empty-state">
        <span class="mdi mdi-tune-variant" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No environment variables.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Key</th><th>Value</th><th></th></tr></thead>
          <tbody>
            <tr v-for="e in envVars" :key="e.id">
              <td class="cell-title">{{ e.key }}</td>
              <td class="text-muted">
                <template v-if="e.is_secret">
                  <code v-if="revealedEnv[e.key] !== undefined" class="env-revealed">{{ revealedEnv[e.key] }}</code>
                  <span v-else class="badge badge-neutral">secret</span>
                </template>
                <template v-else>{{ e.value }}</template>
              </td>
              <td class="text-right">
                <button v-if="e.is_secret && ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="revealedEnv[e.key] !== undefined ? 'Hide value' : 'Reveal value'" :aria-label="revealedEnv[e.key] !== undefined ? 'Hide value' : 'Reveal value'" :disabled="revealingEnv === e.key" @click="toggleReveal(e)"><span class="mdi" :class="revealingEnv === e.key ? 'mdi-loading mdi-spin' : (revealedEnv[e.key] !== undefined ? 'mdi-eye-off-outline' : 'mdi-eye-outline')"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="editEnv(e)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="delEnv(e.key)"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Routes -->
    <div v-else-if="tab === 'routes'" class="card">
      <div class="card-header">
        <h2>Routes</h2>
        <button class="btn btn-ghost btn-sm" @click="router.push('/routes')">Manage routes</button>
      </div>
      <div v-if="appRoutes.length === 0" class="empty-state">
        <span class="mdi mdi-routes" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No routes for this app.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="router.push('/routes')">Add a route</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Route</th><th>Hosts</th><th>TLS</th><th class="text-right">Status</th></tr></thead>
          <tbody>
            <tr v-for="r in appRoutes" :key="r.id" class="row-clickable" @click="router.push('/routes')">
              <td><span class="cell-title">{{ r.name }}</span><div class="cell-sub">{{ r.path }}</div></td>
              <td>
                <template v-if="r.hosts && r.hosts.length">
                  <a v-for="h in r.hosts" :key="h" class="host-link" :href="routeUrl(r, h)" target="_blank" rel="noopener" @click.stop>
                    {{ h }}<span class="mdi mdi-open-in-new"></span>
                  </a>
                </template>
                <span v-else class="cell-sub">—</span>
              </td>
              <td><span class="badge badge-neutral">{{ r.tls_mode }}</span></td>
              <td class="text-right"><span class="badge badge-dot" :class="r.enabled ? 'badge-success' : 'badge-neutral'">{{ r.enabled ? 'enabled' : 'disabled' }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Ports -->
    <div v-else-if="tab === 'ports'">
      <div class="card mb-4">
        <div class="card-header">
          <h2>Container ports</h2>
          <button v-if="ws.canEdit" class="btn btn-ghost btn-sm" @click="openAddPort"><span class="mdi mdi-plus"></span> Add port</button>
        </div>
        <div v-if="!app.ports || app.ports.length === 0" class="empty-state">
          <span class="mdi mdi-ethernet" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No container ports declared.</p>
          <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openAddPort">Add a port</button>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Port</th><th>Protocol</th><th>Scheme</th><th>Name</th><th></th></tr></thead>
            <tbody>
              <tr v-for="p in app.ports" :key="p.id">
                <td class="cell-title">{{ p.container_port }}</td>
                <td class="cell-sub">{{ p.protocol }}</td>
                <td class="cell-sub">{{ p.scheme || 'http' }}</td>
                <td class="cell-sub">{{ p.name || '—' }}</td>
                <td class="text-right table-actions">
                  <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" @click="openBindReq(p)">Request host binding</button>
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Remove port" aria-label="Remove port" @click="removeContainerPort(p)"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <h2>Host port bindings</h2>
          <button v-if="ws.canEdit" class="btn btn-ghost btn-sm" @click="openBindReq()"><span class="mdi mdi-plus"></span> Request binding</button>
        </div>
        <div v-if="appBindings.length === 0" class="empty-state">
          <span class="mdi mdi-swap-horizontal" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No host bindings. Requests require admin approval before they publish.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Mapping</th><th>Status</th><th></th></tr></thead>
            <tbody>
              <tr v-for="b in appBindings" :key="b.id">
                <td class="cell-title">
                  {{ b.bind_ip ? b.bind_ip + ':' : '' }}{{ b.host_port }} → {{ b.container_port }}/{{ b.protocol }}
                  <span v-if="b.managed" class="badge badge-info" title="Auto-provisioned for this app's route ingress; managed by Miabi" style="margin-left: 6px">auto</span>
                </td>
                <td>
                  <span class="badge badge-dot" :class="bindBadge(b.status)">{{ b.status }}</span>
                  <span v-if="b.review_note" class="cell-sub" style="margin-left: 8px">{{ b.review_note }}</span>
                </td>
                <td class="text-right">
                  <button v-if="ws.canEdit && !b.managed" class="btn-icon btn-icon-danger" :title="b.status === 'approved' ? 'Release host port' : 'Cancel request'" :aria-label="b.status === 'approved' ? 'Release host port' : 'Cancel request'" @click="removeBind(b)"><span class="mdi" :class="b.status === 'approved' ? 'mdi-delete-outline' : 'mdi-close'"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Volumes -->
    <div v-else-if="tab === 'volumes'" class="card">
      <div class="card-header"><h2>Attached volumes</h2></div>
      <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
        <form class="flex items-center gap-2" @submit.prevent="attachVolume">
          <select v-model.number="mount.volume_id" class="form-select" aria-label="Volume to attach" style="max-width: 220px">
            <option :value="0" disabled>Select volume…</option>
            <option v-for="v in volumesOnNode" :key="v.id" :value="v.id">{{ v.name }}</option>
          </select>
          <input v-model="mount.path" class="form-input" aria-label="Mount path" placeholder="/data" style="max-width: 200px" />
          <button class="btn btn-primary">Attach</button>
        </form>
        <p v-if="hiddenVolumeCount > 0" class="form-hint" style="margin-top: 8px">
          {{ hiddenVolumeCount }} volume(s) on other nodes are hidden — an app can only mount volumes on its own node.
        </p>
      </div>
      <div v-if="volumeMounts.length === 0" class="empty-state">
        <span class="mdi mdi-harddisk" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No volumes attached.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Volume</th><th>Mount path</th><th></th></tr></thead>
          <tbody>
            <tr v-for="m in volumeMounts" :key="m.volume_id">
              <td class="cell-title">{{ m.docker_name }}</td>
              <td class="text-muted">{{ m.path }}</td>
              <td class="text-right"><button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Detach" aria-label="Detach" @click="detachVolume(m.volume_id)"><span class="mdi mdi-delete-outline"></span></button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Privileged host mounts -->
      <template v-if="canHostMount">
        <div class="card-header" style="border-top: 1px solid var(--border-primary)">
          <h2><span class="mdi mdi-shield-alert-outline" style="color: var(--warning, #d97706)"></span> Host mounts</h2>
        </div>
        <div class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <p class="form-hint" style="margin-bottom: 10px">
            Bind allow-listed host paths into this app. These grant host-level access — attach only what the app truly needs.
          </p>
          <form class="flex items-center gap-2" @submit.prevent="attachHostMount">
            <select v-model="hostMount.preset" class="form-select" aria-label="Host mount capability" style="max-width: 220px" @change="onPresetChange">
              <option value="" disabled>Select capability…</option>
              <option v-for="p in hostPresets" :key="p.key" :value="p.key">{{ p.label }}</option>
            </select>
            <input v-model="hostMount.path" class="form-input" aria-label="Host mount path" :placeholder="selectedPreset?.default_target || '/path'" style="max-width: 220px" />
            <label v-if="selectedPreset?.allow_read_only" class="flex items-center gap-1 text-sm" style="white-space: nowrap">
              <input v-model="hostMount.read_only" type="checkbox" /> read-only
            </label>
            <button class="btn btn-primary" :disabled="!hostMount.preset">Attach</button>
          </form>
          <p v-if="selectedPreset?.danger" class="form-hint" style="margin-top: 8px; color: var(--danger, #dc2626)">
            <span class="mdi mdi-alert"></span> {{ selectedPreset.danger }}
          </p>
        </div>
        <div v-if="hostMounts.length === 0" class="empty-state">
          <p>No host mounts attached.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Capability</th><th>Mount path</th><th>Mode</th><th></th></tr></thead>
            <tbody>
              <tr v-for="m in hostMounts" :key="m.host_preset">
                <td class="cell-title">{{ presetLabel(m.host_preset) }}</td>
                <td class="text-muted">{{ m.path }}</td>
                <td class="text-muted">{{ m.read_only ? 'read-only' : 'read-write' }}</td>
                <td class="text-right"><button class="btn-icon btn-icon-danger" title="Detach" aria-label="Detach" @click="detachHostMount(m.host_preset)"><span class="mdi mdi-delete-outline"></span></button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>

    <!-- Databases -->
    <div v-else-if="tab === 'databases'" class="card">
      <div class="card-header">
        <h2>Databases</h2>
        <div class="flex items-center gap-2">
          <button v-if="ws.canEdit" class="btn btn-primary btn-sm" @click="openLink"><span class="mdi mdi-link-variant"></span> Link database</button>
          <RouterLink to="/databases" class="btn btn-ghost btn-sm">Manage databases</RouterLink>
        </div>
      </div>
      <div v-if="appDatabases.length === 0" class="empty-state">
        <span class="mdi mdi-database-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No databases attached. Link an existing database or create a new one on an instance.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Database</th><th>Engine</th><th>User</th><th>Env</th><th></th></tr></thead>
          <tbody>
            <tr v-for="d in appDatabases" :key="d.id">
              <td>
                <span class="cell-title" style="font-family: monospace">{{ d.name }}</span>
                <div class="cell-sub">{{ d.instance_name }} · {{ d.host }}:{{ d.port }}</div>
              </td>
              <td class="cell-sub">{{ d.engine }}</td>
              <td class="cell-sub" style="font-family: monospace">{{ d.username }}</td>
              <td class="cell-sub" style="font-family: monospace">{{ d.env_prefix ? d.env_prefix + '_*' : 'DB_*' }}</td>
              <td class="text-right table-actions">
                <button class="btn btn-secondary btn-sm" @click="revealDatabase(d)"><span class="mdi mdi-key-outline"></span> Connection</button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Detach" aria-label="Detach" @click="detachDatabase(d)"><span class="mdi mdi-link-variant-off"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Releases -->
    <div v-else-if="tab === 'releases'" class="card">
      <div class="card-header"><h2>Releases</h2></div>
      <div v-if="releases.length === 0" class="empty-state">
        <span class="mdi mdi-tag-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No releases yet.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Version</th><th>Image</th><th>State</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in releases" :key="r.id" class="row-clickable" @click="releaseDetail = r">
              <td class="cell-title">v{{ r.version }}</td>
              <td class="text-muted">{{ r.image }}</td>
              <td>
                <span v-if="r.active" class="badge badge-success badge-dot">active</span>
                <span v-if="r.pinned" class="badge badge-info" style="margin-left: 4px"><span class="mdi mdi-pin" style="font-size: 12px"></span> pinned</span>
              </td>
              <td class="text-right table-actions" @click.stop>
                <button v-if="!r.active && ws.canEdit" class="btn btn-sm btn-secondary" title="Redeploy this release" @click="activate(r.id)">Activate</button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="r.pinned ? 'Unpin' : 'Pin'" :aria-label="r.pinned ? 'Unpin' : 'Pin'" :disabled="releaseBusy === r.id" @click="togglePin(r)">
                  <span class="mdi" :class="r.pinned ? 'mdi-pin-off-outline' : 'mdi-pin-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" :disabled="r.active || r.pinned || releaseBusy === r.id" @click="deleteRelease(r)">
                  <span class="mdi mdi-delete-outline"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Access (per-resource policies) -->
    <AppAccessPanel v-else-if="tab === 'access'" :ws-id="wid || 0" :app-id="appId" />

    <!-- Settings -->
    <div v-else-if="tab === 'settings'">
      <div class="card mb-4">
        <div class="card-header"><h2>Configuration</h2></div>
        <div class="card-body" style="max-width: 460px">
          <template v-if="app.source_type === 'image'">
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">Image</label>
                <input v-model="settingsForm.image" class="form-input" placeholder="nginx" :disabled="!ws.canEdit || imageManaged" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Tag <span class="text-muted">(optional)</span></label>
                <input v-model="settingsForm.tag" class="form-input" placeholder="latest" :disabled="!ws.canEdit || imageManaged" />
              </div>
            </div>
            <p v-if="template" class="form-hint" style="margin-top: -8px; margin-bottom: 16px">
              <span class="mdi mdi-lock-outline"></span>
              The image is managed by the “{{ template.name }}” template — change it through a
              <button type="button" class="tpl-notice-link" @click="goToUpgrade">marketplace upgrade</button>.
            </p>
            <p v-else-if="managedBy === 'gitops'" class="form-hint" style="margin-top: -8px; margin-bottom: 16px">
              <span class="mdi mdi-lock-outline"></span>
              The image is managed by GitOps — change it in the Git manifest. Edits here are overwritten on the next
              <router-link :to="{ name: 'gitops' }">sync</router-link>.
            </p>
            <p v-else class="form-hint" style="margin-top: -8px; margin-bottom: 16px">
              Deploys <code>{{ settingsForm.image || 'image' }}:{{ settingsForm.tag || 'latest' }}</code>
            </p>
            <div class="form-group">
              <label class="form-label">Registry credential <span class="text-muted">(for private images)</span></label>
              <select v-model="settingsForm.registry_id" class="form-select" :disabled="!ws.canEdit">
                <option :value="null">Public / none</option>
                <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }} ({{ r.server }})</option>
              </select>
            </div>
          </template>
          <template v-else>
            <div class="form-group">
              <label class="form-label">Repository</label>
              <select v-model="settingsForm.git_repository_id" class="form-select" :disabled="!ws.canEdit" @change="onSettingsRepoSelect">
                <option :value="null">Public URL (no saved repository)</option>
                <option v-for="r in gitRepos" :key="r.id" :value="r.id">{{ r.name }} — {{ r.url }}</option>
              </select>
              <p class="form-hint">
                A saved repository supplies the clone URL and credentials.
                <RouterLink to="/git-repositories">Manage repositories →</RouterLink>
              </p>
            </div>
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">
                  Repository URL
                  <span v-if="settingsForm.git_repository_id" class="text-muted">(optional — overrides the saved repository)</span>
                </label>
                <input v-model="settingsForm.git_repo" class="form-input" placeholder="https://github.com/user/repo" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Branch / ref <span class="text-muted">(optional)</span></label>
                <input v-model="settingsForm.git_ref" class="form-input" placeholder="main" :disabled="!ws.canEdit" />
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">Build method</label>
              <select v-model="settingsForm.build_method" class="form-select" :disabled="!ws.canEdit">
                <option value="auto">Auto (recommended)</option>
                <option value="buildpack">Buildpacks (no Dockerfile)</option>
                <option value="dockerfile">Dockerfile</option>
              </select>
              <p class="form-hint">Auto builds the repo's Dockerfile when present, otherwise uses Cloud Native Buildpacks. Applied on the next deploy.</p>
            </div>
            <div v-if="settingsForm.build_method !== 'dockerfile'" class="form-group">
              <label class="form-label">Builder image <span class="text-muted">(optional, advanced)</span></label>
              <input v-model="settingsForm.builder" class="form-input" placeholder="paketobuildpacks/builder-jammy-base" :disabled="!ws.canEdit" />
              <p class="form-hint">Override the Cloud Native Buildpacks builder. Leave empty to use the platform default.</p>
            </div>
          </template>
          <div class="form-group">
            <label class="form-label">Command <span class="text-muted">(optional)</span></label>
            <input v-model="settingsForm.command" class="form-input mono" placeholder="image default" :disabled="!ws.canEdit" />
            <p class="form-hint">Overrides the image's default command (CMD). Space-separated args; leave blank to use the image default.</p>
          </div>
          <div class="form-group">
            <label class="form-label">Container ports</label>
            <div v-for="(p, i) in settingsForm.ports" :key="i" class="port-row">
              <input v-model.number="p.container_port" type="number" class="form-input" aria-label="Container port" placeholder="8080" :disabled="!ws.canEdit" style="flex: 1" />
              <select v-model="p.protocol" class="form-select" :disabled="!ws.canEdit" aria-label="Port protocol" style="width: 84px">
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
              </select>
              <select v-model="p.scheme" class="form-select" :disabled="!ws.canEdit" title="Application protocol the container speaks on this port" aria-label="Application protocol the container speaks on this port" style="width: 96px">
                <option value="http">http</option>
                <option value="https">https</option>
              </select>
              <input v-model="p.name" class="form-input" aria-label="Port name" placeholder="name (opt)" :disabled="!ws.canEdit" style="flex: 1" />
              <button v-if="ws.canEdit" type="button" class="btn-icon btn-icon-danger" aria-label="Remove port" @click="removeSettingsPort(i)"><span class="mdi mdi-close"></span></button>
            </div>
            <button v-if="ws.canEdit" type="button" class="btn btn-ghost btn-sm" @click="addSettingsPort"><span class="mdi mdi-plus"></span> Add port</button>
          </div>
          <div class="form-group">
            <label class="form-label">Networks</label>
            <label v-for="n in networks" :key="n.id" class="checkbox-label">
              <input type="checkbox" :value="n.id" v-model="settingsForm.network_ids" :disabled="!ws.canEdit || n.is_default" />
              {{ n.name }} <span v-if="n.is_default" class="text-muted">(default, always attached)</span>
            </label>
          </div>
          <div class="form-group">
            <label class="form-label">Stack <span class="text-muted">(optional)</span></label>
            <select v-model="settingsForm.stack_id" class="form-select" :disabled="!ws.canEdit">
              <option :value="null">None</option>
              <option v-for="s in stacks" :key="s.id" :value="s.id">{{ s.name }}</option>
            </select>
            <p class="form-hint">Stack changes take effect on the next deploy.</p>
          </div>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save settings' }}
          </button>
          <p v-else class="text-muted text-sm">You need developer access to edit settings.</p>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>Deployment strategy</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-group">
            <label class="form-label">Default strategy</label>
            <div class="strategy-options">
              <label v-for="s in STRATEGIES" :key="s.value" class="strategy-option" :class="{ active: settingsForm.deploy_strategy === s.value }">
                <input type="radio" :value="s.value" v-model="settingsForm.deploy_strategy" :disabled="!ws.canEdit" />
                <span><span class="strategy-name">{{ s.label }}</span><span class="strategy-hint">{{ s.hint }}</span></span>
              </label>
            </div>
            <p class="form-hint">Applied when you Deploy without choosing a strategy. Config-change redeploys always use rolling.</p>
          </div>
          <template v-if="settingsForm.deploy_strategy === 'canary'">
            <div class="form-row">
              <div class="form-group" style="flex: 1">
                <label class="form-label">Initial %</label>
                <input v-model.number="settingsForm.canary_initial_weight" type="number" min="1" max="99" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Step %</label>
                <input v-model.number="settingsForm.canary_step_weight" type="number" min="1" max="99" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Interval (s)</label>
                <input v-model.number="settingsForm.canary_step_interval_seconds" type="number" min="10" class="form-input" :disabled="!ws.canEdit" />
              </div>
            </div>
            <p class="form-hint" style="margin-top: -8px; margin-bottom: 16px">
              Start at {{ settingsForm.canary_initial_weight }}%, add {{ settingsForm.canary_step_weight }}% every
              {{ settingsForm.canary_step_interval_seconds }}s, auto-promote at 100%.
            </p>
          </template>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save strategy' }}
          </button>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>Resources</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-row">
            <div class="form-group" style="flex: 1">
              <label class="form-label">CPU limit (cores)</label>
              <input v-model.number="settingsForm.cpu_cores" type="number" min="0" step="0.1" class="form-input" :class="{ 'input-error': cpuOverCap }" :disabled="!ws.canEdit" />
              <p class="form-hint" :class="{ 'hint-error': cpuOverCap }">
                <span v-if="cpuOverCap">Exceeds platform max of {{ limits.max_cpu_cores }} cores.</span>
                <span v-else-if="limits.max_cpu_cores > 0">Max {{ limits.max_cpu_cores }} cores. 0 = unlimited.</span>
                <span v-else>0 = unlimited.</span>
              </p>
            </div>
            <div class="form-group" style="flex: 1">
              <label class="form-label">Memory limit (MB)</label>
              <input v-model.number="settingsForm.memory_mb" type="number" min="0" step="64" class="form-input" :class="{ 'input-error': memOverCap }" :disabled="!ws.canEdit" />
              <p class="form-hint" :class="{ 'hint-error': memOverCap }">
                <span v-if="memOverCap">Exceeds platform max of {{ limits.max_memory_mb }} MB.</span>
                <span v-else-if="limits.max_memory_mb > 0">Max {{ limits.max_memory_mb }} MB. 0 = unlimited.</span>
                <span v-else>0 = unlimited.</span>
              </p>
            </div>
          </div>
          <div v-if="gpuAllowed" class="form-row">
            <div class="form-group" style="flex: 1">
              <label class="form-label">GPUs</label>
              <input v-model.number="settingsForm.gpu_count" type="number" min="0" step="1" class="form-input" :disabled="!ws.canEdit" />
              <p class="form-hint">Whole GPU devices to attach. 0 = none. Counts against the plan’s GPU quota while running.</p>
            </div>
            <div class="form-group" style="flex: 1">
              <label class="form-label">GPU kind</label>
              <input v-model="settingsForm.gpu_kind" type="text" placeholder="any" class="form-input" :disabled="!ws.canEdit || settingsForm.gpu_count < 1" />
              <p class="form-hint">Narrow to a vendor/model (e.g. <code>nvidia</code>). Empty = any enabled GPU on the node.</p>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">Restart policy</label>
            <select v-model="settingsForm.restart_policy" class="form-select" :disabled="!ws.canEdit">
              <option v-for="p in RESTART_POLICIES" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
            <p class="form-hint">When the container should be restarted by Docker. Applied on the next deploy.</p>
          </div>
          <div class="form-group">
            <label class="form-label">Image pull policy</label>
            <select v-model="settingsForm.image_pull_policy" class="form-select" :disabled="!ws.canEdit">
              <option v-for="p in IMAGE_PULL_POLICIES" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
            <p class="form-hint">
              Whether a deploy pulls the image from the registry: <strong>Always</strong> fetches the tag each deploy,
              <strong>If not present</strong> reuses a locally cached image, <strong>Never</strong> requires it to be present
              already. Digest-pinned images are never re-pulled.
            </p>
          </div>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings || !resourcesValid" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save resources' }}
          </button>
        </div>
      </div>

      <!-- Container labels (Traefik &c.) -->
      <div class="card mb-4">
        <div class="card-header"><h2>Container labels</h2></div>
        <div class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <p class="text-muted text-sm" style="margin: 0 0 8px">
            Custom Docker labels stamped on this app's container — for label-driven tools like
            <strong>Traefik</strong>. Reserved keys (<code>io.miabi.*</code>, <code>com.docker.*</code>) are
            not allowed. Changes apply on the next deploy. Don't put secrets in labels — they're visible via
            <code>docker inspect</code>.
          </p>
          <p v-if="!customLabelsAllowed" class="text-muted text-sm" style="margin: 8px 0 0">
            <span class="mdi mdi-lock-outline"></span>
            Custom labels aren't available on your current plan, or have been disabled by your administrator.
          </p>
          <form v-else-if="ws.canEdit" class="flex items-center gap-2" @submit.prevent="setLabel">
            <input v-model="newLabel.key" class="form-input" aria-label="Label key" placeholder="traefik.enable" style="max-width: 280px" />
            <input v-model="newLabel.value" class="form-input" aria-label="Label value" placeholder="true" style="max-width: 240px" />
            <button class="btn btn-primary">Add</button>
          </form>
        </div>
        <div v-if="Object.keys(containerLabels).length === 0" class="empty-state">
          <span class="mdi mdi-label-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No custom labels.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Key</th><th>Value</th><th></th></tr></thead>
            <tbody>
              <tr v-for="(value, key) in containerLabels" :key="key">
                <td class="cell-title">{{ key }}</td>
                <td class="text-muted">{{ value }}</td>
                <td class="text-right">
                  <button v-if="ws.canEdit && customLabelsAllowed" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="delLabel(String(key))"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>Healthcheck</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-group">
            <label class="form-label">Type</label>
            <select v-model="settingsForm.hc_type" class="form-select" :disabled="!ws.canEdit">
              <option v-for="t in HEALTHCHECK_TYPES" :key="t.value" :value="t.value">{{ t.label }}</option>
            </select>
            <p class="form-hint">Deploys wait for the container to report healthy before going live.</p>
          </div>
          <template v-if="settingsForm.hc_type === 'http'">
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">Path</label>
                <input v-model="settingsForm.hc_path" class="form-input" placeholder="/health" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Port <span class="text-muted">(opt)</span></label>
                <input v-model.number="settingsForm.hc_port" type="number" class="form-input" :placeholder="String(app.port || 80)" :disabled="!ws.canEdit" />
              </div>
            </div>
            <p class="form-hint" style="margin-top: -8px; margin-bottom: 16px">Uses <code>curl</code> (or <code>wget</code>) inside the container — the image must include one.</p>
          </template>
          <div v-else-if="settingsForm.hc_type === 'command'" class="form-group">
            <label class="form-label">Command</label>
            <input v-model="settingsForm.hc_command" class="form-input mono" placeholder="pg_isready -U postgres" :disabled="!ws.canEdit" />
            <p class="form-hint">Runs via the shell; non-zero exit = unhealthy.</p>
          </div>
          <template v-if="settingsForm.hc_type !== 'none'">
            <div class="form-row">
              <div class="form-group" style="flex: 1">
                <label class="form-label">Interval (s)</label>
                <input v-model.number="settingsForm.hc_interval" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Timeout (s)</label>
                <input v-model.number="settingsForm.hc_timeout" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Retries</label>
                <input v-model.number="settingsForm.hc_retries" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">Start period (s)</label>
                <input v-model.number="settingsForm.hc_start_period" type="number" min="0" class="form-input" :disabled="!ws.canEdit" />
              </div>
            </div>
          </template>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save healthcheck' }}
          </button>
        </div>
      </div>

      <div class="card">
      <div class="card-header"><h2 style="color: var(--danger-600)">Danger zone</h2></div>
      <div class="card-body flex items-center justify-between">
        <div>
          <div style="font-weight: 600; color: var(--text-primary)">Delete this application</div>
          <div class="text-muted text-sm">
            Permanently removes the application and its container.
            <span v-if="liveStatus?.running"> Stop it first — running apps can't be deleted.</span>
          </div>
        </div>
        <button v-if="ws.canEdit" class="btn btn-danger" :disabled="liveStatus?.running" :title="liveStatus?.running ? 'Stop the application before deleting' : 'Delete application'" @click="openDelete">Delete application</button>
        <span v-else class="text-muted text-sm">Requires developer access or higher.</span>
      </div>
      </div>
    </div>

    <!-- Delete application -->
    <Teleport to="body">
      <div v-if="showDelete && app" class="modal-overlay" @click.self="showDelete = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Delete application</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDelete = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="removeApp">
            <div class="modal-body">
              <p>This permanently removes <strong>{{ app.name }}</strong>, its running container, env vars, ports, and deployment history. This cannot be undone.</p>
              <div class="form-group" style="margin-bottom: 0; margin-top: 12px">
                <label class="form-label">Type <code>{{ app.name }}</code> to confirm</label>
                <input v-model="deleteConfirm" class="form-input" :placeholder="app.name" autofocus autocomplete="off" />
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showDelete = false">Cancel</button>
              <button type="submit" class="btn btn-danger" :disabled="deleteConfirm !== app.name || deleting">{{ deleting ? 'Deleting…' : 'Delete application' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Link a database -->
    <Teleport to="body">
      <div v-if="linkModal" class="modal-overlay" @click.self="linkModal = false">
        <div class="modal" style="max-width: 600px; width: 100%">
          <div class="modal-header">
            <h3>Link a database</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="linkModal = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <!-- Step 1: instance -->
            <label class="form-label">Database instance</label>
            <div v-if="instancesOnNode.length === 0" class="form-hint" style="margin-bottom: 10px">
              No database instances on this app's node.
            </div>
            <div v-else class="instance-grid">
              <button
                v-for="i in instancesOnNode"
                :key="i.id"
                type="button"
                class="instance-chip"
                :class="{ active: selInstance?.id === i.id }"
                @click="selectLinkInstance(i)"
              >
                <span class="cell-title">{{ i.name }}</span>
                <span class="cell-sub">{{ i.engine }}</span>
              </button>
            </div>
            <p v-if="hiddenInstanceCount > 0" class="form-hint" style="margin-top: 6px">
              {{ hiddenInstanceCount }} instance(s) on other nodes are hidden — an app can only use databases on its own node.
            </p>

            <!-- Step 2: database on the instance -->
            <template v-if="selInstance">
              <div class="seg" style="margin-top: 16px">
                <button type="button" class="seg-btn" :class="{ active: linkMode === 'existing' }" @click="linkMode = 'existing'">Existing database</button>
                <button type="button" class="seg-btn" :class="{ active: linkMode === 'new' }" @click="linkMode = 'new'">＋ New database</button>
              </div>

              <div v-if="linkMode === 'existing'" style="margin-top: 12px">
                <div v-if="freeDatabases.length === 0" class="form-hint">
                  No unattached databases on this instance. Switch to “＋ New database”.
                </div>
                <select v-else v-model.number="linkForm.database_id" class="form-select" aria-label="Database to link" style="width: 100%">
                  <option :value="0" disabled>Select database…</option>
                  <option v-for="d in freeDatabases" :key="d.id" :value="d.id">{{ d.name }}</option>
                </select>
              </div>

              <div v-else style="margin-top: 12px">
                <label class="form-label">New database name</label>
                <input v-model="linkForm.new_name" class="form-input" placeholder="myapp" style="width: 100%" />
              </div>

              <label class="form-label" style="margin-top: 14px">Env var prefix <span class="text-muted">(optional)</span></label>
              <input v-model="linkForm.env_prefix" class="form-input" placeholder="e.g. ANALYTICS → ANALYTICS_DATABASE_URL" style="width: 100%" />
              <p v-if="!linkForm.env_prefix.trim() && hasUnprefixed" class="form-hint" style="color: var(--warning, #d97706); margin-top: 6px">
                <span class="mdi mdi-alert-outline"></span> This app already has an unprefixed database. Add a prefix to avoid overwriting its DATABASE_URL / DB_* vars.
              </p>
            </template>
          </div>
          <div class="modal-footer">
            <button class="btn btn-ghost" @click="linkModal = false">Cancel</button>
            <button class="btn btn-primary" :disabled="!selInstance || linkBusy" @click="confirmLink">
              {{ linkBusy ? 'Linking…' : 'Attach' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Database connection -->
    <Teleport to="body">
      <div v-if="dbConnModal" class="modal-overlay" @click.self="dbConnModal = null">
        <div class="modal" style="max-width: 560px; width: 100%">
          <div class="modal-header">
            <h3>Connection · {{ dbConnModal.title }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="dbConnModal = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div v-for="f in [
              { label: 'Host', value: `${dbConnModal.info.host}:${dbConnModal.info.port}` },
              { label: 'Database', value: dbConnModal.info.database },
              { label: 'Username', value: dbConnModal.info.username },
              { label: 'Password', value: dbConnModal.info.password },
              { label: 'URI', value: dbConnModal.info.uri },
            ]" :key="f.label" class="dns-field">
              <span class="dns-field-label">{{ f.label }}</span>
              <div class="dns-field-row">
                <span class="dns-field-value">{{ f.value || '—' }}</span>
                <button v-if="f.value" class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(f.value)"><span class="mdi mdi-content-copy"></span></button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Import .env -->
    <Teleport to="body">
      <div v-if="showEnvImport" class="modal-overlay" @click.self="showEnvImport = false">
        <div class="modal" style="max-width: 560px; width: 100%">
          <div class="modal-header">
            <h3>Import .env</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEnvImport = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="importEnv">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Paste KEY=VALUE lines</label>
                <textarea v-model="envImport.content" class="form-input" rows="10" spellcheck="false" style="font-family: monospace; font-size: 12px" placeholder="DATABASE_URL=postgres://...&#10;# comments and blank lines are ignored&#10;LOG_LEVEL=info" required></textarea>
              </div>
              <label class="checkbox-label" style="margin-bottom: 0"><input type="checkbox" v-model="envImport.secret" /> Mark all as secrets (encrypted)</label>
              <p class="form-hint">Existing keys are overwritten. {{ isDeployed ? 'The app redeploys after import.' : '' }}</p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showEnvImport = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="importingEnv">{{ importingEnv ? 'Importing…' : 'Import' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Shared confirm dialog (port request/release flows) -->
    <ConfirmDialog
      :open="confirmDialog.open"
      :title="confirmDialog.title"
      :message="confirmDialog.message"
      :confirm-label="confirmDialog.confirmLabel"
      :cancel-label="confirmDialog.cancelLabel"
      :variant="confirmDialog.variant"
      @confirm="resolveConfirm(true)"
      @cancel="resolveConfirm(false)"
    />

    <!-- Add container port -->
    <Teleport to="body">
      <div v-if="showAddPort" class="modal-overlay" @click.self="showAddPort = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Add container port</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showAddPort = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="addContainerPort">
            <div class="modal-body">
              <div class="flex items-center gap-3">
                <div class="form-group" style="flex: 1">
                  <label class="form-label">Container port</label>
                  <input v-model.number="portForm.container_port" type="number" min="1" max="65535" class="form-input" placeholder="8080" required autofocus />
                </div>
                <div class="form-group" style="width: 110px">
                  <label class="form-label">Protocol</label>
                  <select v-model="portForm.protocol" class="form-select"><option value="tcp">tcp</option><option value="udp">udp</option></select>
                </div>
                <div class="form-group" style="width: 120px">
                  <label class="form-label">Scheme</label>
                  <select v-model="portForm.scheme" class="form-select"><option value="http">http</option><option value="https">https</option></select>
                </div>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Name <span class="text-muted">(optional)</span></label>
                <input v-model="portForm.name" class="form-input" placeholder="http" />
              </div>
              <p class="form-hint">Declares a port the container listens on. Scheme is how a Gateway route reaches it (use https for TLS-only backends). Applies on the next deploy.</p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showAddPort = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="savingSettings || portForm.container_port <= 0">{{ savingSettings ? 'Adding…' : 'Add port' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Request host binding -->
    <Teleport to="body">
      <div v-if="showBindReq" class="modal-overlay" @click.self="showBindReq = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Request host binding</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showBindReq = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="requestBind">
            <div class="modal-body">
              <p class="text-muted text-sm" style="margin-bottom: 14px">Publishes a container port on a host port. A platform admin must approve it; it takes effect on the next deploy.</p>
              <div class="form-row">
                <div class="form-group" style="flex: 1">
                  <label class="form-label">Container port</label>
                  <select v-model.number="bindForm.container_port" class="form-select">
                    <option v-for="p in (app.ports || [])" :key="p.id" :value="p.container_port">{{ p.container_port }}/{{ p.protocol }}</option>
                  </select>
                </div>
                <div class="form-group" style="flex: 1">
                  <label class="form-label">Host port</label>
                  <div class="flex items-center gap-2">
                    <input v-model.number="bindForm.host_port" type="number" class="form-input" placeholder="30080" required />
                    <button type="button" class="btn btn-secondary" :disabled="suggestingPort" title="Pick a free host port on this node" @click="suggestPort">{{ suggestingPort ? '…' : 'Suggest' }}</button>
                  </div>
                </div>
              </div>
              <p class="form-hint">Host ports are shared per node — “Suggest” picks one that’s currently free.</p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showBindReq = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="requestingBind || !bindForm.host_port">{{ requestingBind ? 'Requesting…' : 'Request' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Deploy dialog -->
    <Teleport to="body">
      <div v-if="showDeploy" class="modal-overlay" @click.self="showDeploy = false">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ deployVerb }} {{ app.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDeploy = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="confirmDeploy">
            <div class="modal-body">
              <div v-if="template" class="tpl-notice">
                <span class="mdi mdi-store-outline"></span>
                <div class="tpl-notice-text">
                  <strong>Managed by the “{{ template.name }}” template.</strong>
                  Changing the image here means it no longer matches the template, and a future template upgrade may overwrite it.
                  <button type="button" class="tpl-notice-link" @click="goToUpgrade">Upgrade via Marketplace</button>
                </div>
              </div>
              <div v-else-if="managedBy === 'gitops'" class="tpl-notice">
                <span class="mdi mdi-source-branch"></span>
                <div class="tpl-notice-text">
                  <strong>Managed by GitOps.</strong>
                  This app's desired state comes from a Git manifest. A manual redeploy may be reverted on the next
                  <router-link :to="{ name: 'gitops' }">sync</router-link> — deploy from Git to make it stick.
                </div>
              </div>
              <template v-if="app.source_type === 'image'">
                <div class="form-group" style="margin-bottom: 8px">
                  <label class="form-label">Image tag</label>
                  <input v-model="deployTag" class="form-input" placeholder="latest" autofocus />
                </div>
                <p class="form-hint" style="margin-bottom: 16px">Deploys <code>{{ app.image }}:{{ deployTag.trim() || 'latest' }}</code> as a new release.</p>
              </template>
              <p v-else class="text-muted text-sm" style="margin-bottom: 16px">Builds the latest commit from the configured Git source as a new release.</p>
              <div class="form-group" style="margin-bottom: 8px">
                <label class="form-label">Deployment strategy</label>
                <select v-model="deployStrategy" class="form-select">
                  <option v-for="s in STRATEGIES" :key="s.value" :value="s.value">{{ s.label }}</option>
                </select>
              </div>
              <p class="form-hint">{{ strategyHint(deployStrategy) }}<span v-if="!isDeployed && deployStrategy === 'canary'"><br />First deploy can't canary — it will run as rolling.</span></p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showDeploy = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="deploying">
                <span class="mdi mdi-rocket-launch-outline"></span> {{ deploying ? 'Starting…' : deployVerb }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Release detail -->
    <Teleport to="body">
      <div v-if="releaseDetail" class="modal-overlay" @click.self="releaseDetail = null">
        <div class="modal">
          <div class="modal-header">
            <h3>Release v{{ releaseDetail.version }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="releaseDetail = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="dns-field">
              <span class="dns-field-label">Image</span>
              <span class="dns-field-value">{{ releaseDetail.image }}</span>
            </div>
            <div class="dns-field">
              <span class="dns-field-label">Container</span>
              <span class="dns-field-value">{{ releaseDetail.container_id || '—' }}</span>
            </div>
            <div class="rel-meta">
              <span v-if="releaseDetail.active" class="badge badge-success badge-dot">active</span>
              <span v-else class="badge badge-neutral">inactive</span>
              <span v-if="releaseDetail.pinned" class="badge badge-info"><span class="mdi mdi-pin" style="font-size: 12px"></span> pinned</span>
              <span class="text-muted text-sm">Created {{ new Date(releaseDetail.created_at).toLocaleString() }}</span>
            </div>
          </div>
          <div class="modal-footer">
            <button v-if="!releaseDetail.active && ws.canEdit" class="btn btn-secondary" @click="activate(releaseDetail.id); releaseDetail = null">Activate</button>
            <button v-if="ws.canEdit" class="btn btn-secondary" @click="togglePin(releaseDetail)">{{ releaseDetail.pinned ? 'Unpin' : 'Pin' }}</button>
            <button v-if="ws.canEdit" class="btn btn-danger" :disabled="releaseDetail.active || releaseDetail.pinned" @click="deleteRelease(releaseDetail)">Delete</button>
          </div>
        </div>
      </div>
    </Teleport>

    <Teleport to="body">
      <ShellTerminal v-if="shellOpen && app" :base="base" :app-name="app.name" @close="shellOpen = false" />
      <ContainerProcesses v-if="processesOpen && app && wid" :ws="wid" :app-id="appId" :app-name="app.name" @close="processesOpen = false" />
    </Teleport>
  </div>
  <div v-else class="loading-page"><span class="spinner"></span></div>
</template>

<style scoped>
.env-revealed { word-break: break-all; }
/* Marketplace-managed notice shown in the Deploy dialog for template apps. */
.tpl-notice {
  display: flex; gap: 10px; align-items: flex-start;
  padding: 10px 12px; margin-bottom: 16px;
  border: 1px solid var(--warning-500); border-radius: 8px;
  background: var(--warning-50);
}
.tpl-notice > .mdi { color: var(--warning-600); font-size: 18px; line-height: 1.4; flex-shrink: 0; }
.tpl-notice-text { font-size: 13px; color: var(--text-primary); line-height: 1.45; }
.tpl-notice-link {
  display: inline; padding: 0; margin-left: 4px;
  background: none; border: none; cursor: pointer;
  color: var(--primary-600); font: inherit; font-weight: 500; text-decoration: underline;
}
/* Provenance link in the header subline (marketplace template / GitOps). */
.prov-link { color: var(--primary-600); }
.prov-link:hover { text-decoration: underline; }
/* Deployments tab: a full-width canary banner on top, then a narrow Deployments
   list (pick) beside a wide Logs panel (view). */
.detail-grid { display: grid; grid-template-columns: minmax(260px, 360px) 1fr; gap: 16px; align-items: start; }
@media (max-width: 900px) { .detail-grid { grid-template-columns: 1fr; } }
.log-view { height: 320px; overflow: auto; white-space: pre-wrap; }
/* Runtime Logs panel: height is driven inline by the S/M/L size control. */
.log-header-left { display: flex; align-items: center; gap: 12px; }
.log-size-control { display: inline-flex; border: 1px solid var(--border-input); border-radius: var(--radius); overflow: hidden; }
.log-size-btn {
  padding: 0 10px; height: 26px; font-size: 12px; font-weight: 600;
  background: var(--bg-input); color: var(--text-secondary);
  border: none; border-left: 1px solid var(--border-input); cursor: pointer;
}
.log-size-btn:first-child { border-left: none; }
.log-size-btn:hover { color: var(--text-primary); }
.log-size-btn.active { background: var(--primary-600); color: #fff; }
.log-toolbar { display: flex; align-items: center; gap: 12px; }
.log-search { position: relative; display: flex; align-items: center; }
.log-search-input { width: 240px; padding-right: 52px; }
.log-search-clear {
  position: absolute; right: 8px; background: none; border: none; padding: 0;
  font-size: 12px; color: var(--text-secondary); cursor: pointer;
}
.log-search-clear:hover { color: var(--text-primary); }
.log-search-error .log-search-input { border-color: var(--danger-500); }
.log-match-count { white-space: nowrap; }
.log-regex-err { white-space: nowrap; color: var(--danger-500); }
.log-regex-toggle {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 30px; height: 30px; padding: 0 6px;
  font-family: 'JetBrains Mono', monospace; font-size: 13px; font-weight: 600;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-regex-toggle:hover { color: var(--text-primary); }
.log-regex-toggle.active { background: var(--primary-600); color: #fff; border-color: var(--primary-600); }
.log-follow-btn {
  display: inline-flex; align-items: center; gap: 4px; white-space: nowrap;
  height: 30px; padding: 0 10px; font-size: 12px; font-weight: 600;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-follow-btn:hover { color: var(--text-primary); }
.log-follow-btn.active { background: var(--primary-600); color: #fff; border-color: var(--primary-600); }
.log-follow-btn .mdi { font-size: 15px; }
.log-icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; font-size: 15px;
  background: var(--bg-input); color: var(--text-secondary);
  border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer;
}
.log-icon-btn:hover:not(:disabled) { color: var(--text-primary); }
.log-icon-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.log-trim-note { margin: 0 0 8px; }
/* Per-line log rendering (enables match highlighting). Inherits pre-wrap from
   .code-block; blank lines keep their row height. */
.log-line { min-height: 1.4em; }
.log-hit { background: var(--warning-400, #facc15); color: #1a1a2e; border-radius: 2px; }
.log-placeholder { color: var(--text-secondary); }
.text-muted { color: var(--text-muted); }
/* The deployment row whose logs are currently streamed. */
.row-selected td { background: var(--bg-hover); }
.row-selected td:first-child { box-shadow: inset 3px 0 0 var(--primary-600); }
.form-row { display: flex; gap: 12px; }
.status-wrap { display: inline-flex; align-items: center; gap: 8px; }
.status-detail { white-space: nowrap; }
.input-error { border-color: var(--danger-500) !important; }
.hint-error { color: var(--danger-600) !important; }
.form-input.mono { font-family: 'JetBrains Mono', monospace; font-size: 13px; }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; color: var(--text-secondary); }
.modal-body code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; font-family: 'JetBrains Mono', monospace; }
.rel-meta { display: flex; align-items: center; gap: 10px; margin-top: 14px; }
.port-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.section-title { font-size: 13px; font-weight: 600; color: var(--text-secondary); margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.live-tag { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-weight: 500; color: var(--text-muted); text-transform: none; letter-spacing: 0; }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--success-500); animation: live-pulse 1.6s ease-out infinite; }
@keyframes live-pulse {
  0% { box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.5); }
  70% { box-shadow: 0 0 0 5px rgba(34, 197, 94, 0); }
  100% { box-shadow: 0 0 0 0 rgba(34, 197, 94, 0); }
}
.summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 4px 24px;
  padding: 18px 24px;
}
.summary-item { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 6px 8px; margin: -6px -8px; border-radius: var(--radius-sm); }
.summary-item.clickable { cursor: pointer; transition: background 0.12s; }
.summary-item.clickable:hover { background: var(--bg-hover, var(--bg-tertiary)); }

/* Resource usage bars */
.usage-bar { height: 6px; border-radius: 9999px; background: var(--bg-tertiary); overflow: hidden; margin-top: 10px; }
.usage-fill { height: 100%; border-radius: 9999px; transition: width 0.4s ease, background 0.3s ease; }
.usage-ok { background: var(--success-500); }
.usage-warn { background: var(--warning-500); }
.usage-danger { background: var(--danger-500); }
.summary-label { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-muted); }
.summary-value { font-size: 14px; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.summary-value .mdi { color: var(--text-muted); }
.summary-value.mono { display: flex; align-items: center; gap: 4px; }
.summary-value.mono code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; overflow: hidden; text-overflow: ellipsis; }

/* Events timeline */
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
.live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--success-500);
  box-shadow: 0 0 0 0 var(--success-500); animation: pulse 2s infinite;
}
@keyframes pulse {
  0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--success-500) 50%, transparent); }
  70% { box-shadow: 0 0 0 6px transparent; }
  100% { box-shadow: 0 0 0 0 transparent; }
}

/* Canary rollout + deployment strategy */
.canary-card { border-color: var(--warning-500, #f59e0b); grid-column: 1 / -1; }

/* Compact canary rollout strip shown under the app header (any tab). */
.canary-strip { display: flex; align-items: center; gap: 10px; width: 100%; margin: 4px 0 12px; padding: 7px 12px; border: 1px solid var(--warning-500, #f59e0b); border-radius: 8px; background: transparent; color: var(--text-primary); font-size: 13px; text-align: left; cursor: pointer; }
.canary-strip:hover { background: var(--warning-50, rgba(245, 158, 11, 0.08)); }
.canary-strip .mdi { color: var(--warning-500, #f59e0b); }
.canary-strip-text { font-weight: 600; white-space: nowrap; }
.canary-strip-bar { flex: 1; height: 10px; }
.split-bar { display: flex; height: 14px; border-radius: 7px; overflow: hidden; background: var(--bg-tertiary); }
.split-bar.lg { height: 30px; border-radius: 8px; }
.split-stable { background: var(--success-500, #22c55e); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }
.split-canary { background: var(--warning-500, #f59e0b); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }
.canary-actions { display: flex; gap: 8px; margin-top: 16px; }
.strategy-options { display: flex; flex-direction: column; gap: 8px; }
.strategy-option { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer; transition: all var(--transition); }
.strategy-option:hover { border-color: var(--text-muted); }
.strategy-option.active { border-color: var(--primary-500); background: var(--primary-50); }
.strategy-option input { margin-top: 3px; }
.strategy-option span { display: flex; flex-direction: column; }
.strategy-name { font-weight: 600; font-size: 14px; }
.strategy-hint { font-size: 12px; color: var(--text-muted); }

/* Network tab */
.net-row { display: flex; flex-direction: column; gap: 6px; padding: 12px 0; border-top: 1px solid var(--border-primary); }
.net-label { font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.03em; }
.net-vals { display: flex; flex-wrap: wrap; gap: 8px; }
.net-chip { display: inline-flex; align-items: center; gap: 4px; }
.net-chip code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 2px 8px; border-radius: 4px; }
.net-hint { font-size: 12px; color: var(--text-muted); margin: 0; }
.mono code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 2px 8px; border-radius: 4px; }
.net-link { color: var(--primary-600); cursor: pointer; font-weight: 500; }
.net-link:hover { text-decoration: underline; }
.ext-ports { display: flex; flex-direction: column; gap: 6px; }
.ext-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ext-toggle { display: inline-flex; align-items: center; gap: 6px; min-width: 90px; cursor: pointer; font-size: 13px; }
.host-link { color: var(--primary-600); text-decoration: none; display: inline-flex; align-items: center; gap: 3px; margin-right: 10px; font-size: 13px; }
.host-link:hover { text-decoration: underline; }
.host-link .mdi { font-size: 13px; opacity: .7; }
.instance-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; }
.instance-chip { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; padding: 10px 12px; border: 1px solid var(--border-primary); border-radius: 8px; background: var(--bg-secondary); cursor: pointer; text-align: left; }
.instance-chip:hover { border-color: var(--primary-600); }
.instance-chip.active { border-color: var(--primary-600); background: var(--bg-tertiary); }
.seg { display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; }
.seg-btn { padding: 6px 14px; background: var(--bg-secondary); border: none; cursor: pointer; font-size: 13px; color: var(--text-muted); }
.seg-btn.active { background: var(--primary-600); color: #fff; }
</style>
