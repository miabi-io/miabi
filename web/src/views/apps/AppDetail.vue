<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { appApi, isPipelineRun, type ExternalAccess, type PipelineRunAccepted, type SetSourceInput } from '@/api/apps'
import { pipelineApi } from '@/api/pipelines'
import { volumeApi, monitoringApi, databaseApi, usageApi } from '@/api/resources'
import { registryApi } from '@/api/registries'
import { gitRepositoryApi } from '@/api/gitRepositories'
import { networkApi } from '@/api/networks'
import LocationName from '@/components/LocationName.vue'
import { stackApi } from '@/api/stacks'
import { routeApi } from '@/api/routes'
import { configApi, type Config } from '@/api/configs'
import { portBindingApi } from '@/api/portBindings'
import { capabilityApi } from '@/api/capabilities'
import { eventsApi } from '@/api/events'
import { sseUrl } from '@/api/client'
import { fmtDateTime } from '@/utils/datetime'
import ResourceIcon from '@/components/ResourceIcon.vue'
import ShellTerminal from '@/components/ShellTerminal.vue'
import ContainerProcesses from '@/components/ContainerProcesses.vue'
import LogViewer from '@/components/LogViewer.vue'
import NetworkDetailModal from '@/components/NetworkDetailModal.vue'
import LogSizeControl from '@/components/LogSizeControl.vue'
import { useLogSize, isLogSize, logHeight } from '@/composables/useLogSize'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import MetadataCard from '@/components/MetadataCard.vue'
import EnvVarModal from '@/components/EnvVarModal.vue'
import RouteFormModal from '@/components/RouteFormModal.vue'
import CanaryPanel from '@/components/CanaryPanel.vue'
import AppAccessPanel from '@/components/AppAccessPanel.vue'
import type { Application, AppOverview, Deployment, Release, AppEnvVar, Route, Network, Stack, Volume, StatsSample, Registry, GitRepository, AppEvent, AppPort, LastDeploy, PortBinding, AppDatabase, ConnectionInfo, DeployStrategy, RestartPolicy, ImagePullPolicy, ReconcilePolicy, BuildMethod, HealthcheckType, ResourceLimits, LiveStatus, HostMountPreset, DatabaseInstance, LogicalDatabase, NodePlacement, PipelineDefinition, CapabilityCatalog } from '@/api/types'
import AppModal from '@/components/AppModal.vue'
import { fmtSize } from '@/utils/format'
import { copyText } from '@/utils/clipboard'

// Secret-reference example built here so `}}` doesn't break the template's
// mustache parser.
const secretRefHint = '${{ secrets.NAME }}'

const STRATEGIES: { value: DeployStrategy; label: string; hint: string }[] = [
  { value: 'recreate', label: 'appDetail.strategy.recreate', hint: 'appDetail.strategy.recreateHint' },
  { value: 'rolling', label: 'appDetail.strategy.rolling', hint: 'appDetail.strategy.rollingHint' },
  { value: 'canary', label: 'appDetail.strategy.canary', hint: 'appDetail.strategy.canaryHint' },
]
function strategyHint(s: DeployStrategy | undefined): string {
  return STRATEGIES.find((x) => x.value === s)?.hint ?? ''
}

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()

const appId = computed(() => Number(route.params.id))
const wid = computed(() => ws.currentWorkspaceId)
const base = computed(() => `/workspaces/${wid.value}/apps/${appId.value}`)

const app = ref<Application | null>(null)
const tabs = [
  { key: 'overview', label: 'appDetail.tab.overview' },
  { key: 'events', label: 'appDetail.tab.events' },
  { key: 'logs', label: 'appDetail.tab.logs' },
  { key: 'deployments', label: 'appDetail.tab.deployments' },
  { key: 'environment', label: 'appDetail.tab.environment' },
  { key: 'network', label: 'appDetail.tab.network' },
  { key: 'routes', label: 'appDetail.tab.routes' },
  { key: 'ports', label: 'appDetail.tab.ports' },
  { key: 'volumes', label: 'appDetail.tab.volumes' },
  { key: 'databases', label: 'appDetail.tab.databases' },
  { key: 'releases', label: 'appDetail.tab.releases' },
  { key: 'access', label: 'appDetail.tab.access' },
  { key: 'settings', label: 'appDetail.tab.settings' },
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
    notify.success(t('notify.appDetail.extAccessUpdated'))
  } catch (e) {
    notify.apiError(e, 'Failed to update external access')
  } finally {
    extSaving.value = false
  }
}
// Turn external access off entirely: clears the selection and removes all
// generated routes.
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
// Logs panel size: shared with every other log panel and remembered between
// sessions; ?size= still wins for a shared link.
const { size: logSize } = useLogSize(route.query.size)
const logViewStyle = computed(() => ({
  height: logHeight(logSize.value),
  minHeight: logSize.value === 'large' ? '420px' : undefined,
}))
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
// --- GitOps manifest export -------------------------------------------------
const manifestOpen = ref(false)
const manifestYaml = ref('')
const manifestLoading = ref(false)
const manifestCopied = ref(false)

// Marketplace- and GitOps-managed apps are excluded: the first is described by its template, the
// second already has a manifest, and a second copy invites two documents claiming the same app.
const canExportManifest = computed(() => !!app.value && !imageManaged.value)

async function openManifest() {
  manifestOpen.value = true
  if (manifestYaml.value || !app.value) return
  manifestLoading.value = true
  try {
    const { data } = await appApi.manifest(ws.currentWorkspaceId!, app.value.id)
    manifestYaml.value = typeof data === 'string' ? data : String(data)
  } catch (e) {
    manifestOpen.value = false
    notify.apiError(e, 'Failed to generate the manifest')
  } finally {
    manifestLoading.value = false
  }
}

async function copyManifest() {
  if (!(await copyText(manifestYaml.value))) {
    notify.error(t('notify.appDetail.couldNotCopyTheManifest'))
    return
  }
  manifestCopied.value = true
  setTimeout(() => { manifestCopied.value = false }, 1500)
}

function downloadManifest() {
  const blob = new Blob([manifestYaml.value], { type: 'application/yaml;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${app.value?.name || 'app'}.yaml`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

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
// Add/update env var modal. editingEnvKey is set while updating an existing
// variable (its name is locked); null when creating a new one.
const showEnvModal = ref(false)
const editingEnvKey = ref<string | null>(null)
const envForm = ref({ key: '', value: '', secret: false })
const savingEnv = ref(false)
// Client-side filter over the env list (matches key, and value when not secret).
const envSearch = ref('')
const filteredEnvVars = computed(() => {
  const q = envSearch.value.trim().toLowerCase()
  if (!q) return envVars.value
  return envVars.value.filter((e) => e.key.toLowerCase().includes(q) || (!e.is_secret && e.value.toLowerCase().includes(q)))
})
const secretEnvCount = computed(() => envVars.value.filter((e) => e.is_secret).length)
const showEnvImport = ref(false)
const envImport = ref({ content: '', secret: false })
const importingEnv = ref(false)
// Copy feedback: the key of the var whose value was just copied (clears after a beat).
const copiedEnvKey = ref('')

// Routes (this app's Goma routes)
const appRoutes = ref<Route[]>([])
// Browser URL for a route host (https when TLS is on), so users can open the app
// directly from the Routes tab.
// Routes are created and edited in place: routing an app is something you decide
// while looking at the app, so the same form the routes page uses opens here with
// the application fixed.
const showRouteModal = ref(false)
const editingRoute = ref<Route | null>(null)
function addRoute() {
  editingRoute.value = null
  showRouteModal.value = true
}
function editRoute(r: Route) {
  // Generated external-access routes are managed from External Access.
  if (r.generated) { router.push(`/routes/${r.id}`); return }
  editingRoute.value = r
  showRouteModal.value = true
}
async function onRouteSaved() {
  showRouteModal.value = false
  if (wid.value) appRoutes.value = (await routeApi.listByApp(wid.value, appId.value)).data.data ?? []
}

function routeUrl(r: Route, host: string): string {
  const scheme = r.tls_mode && r.tls_mode !== 'none' ? 'https' : 'http'
  const path = r.path && r.path !== '/' ? r.path : ''
  return `${scheme}://${host}${path}`
}

// Ports / host bindings
const appBindings = ref<PortBinding[]>([])
// Approved host port bindings rule out rolling: the new container would have to
// publish a port the running one still holds, which Docker refuses. The worker
// falls back to recreate, so the form stops offering rolling rather than promise
// a zero-downtime deploy it can't do.
const publishedHostPorts = computed(() => appBindings.value.filter((b) => b.status === 'approved'))
const hasHostPorts = computed(() => publishedHostPorts.value.length > 0)
const hostPortList = computed(() => publishedHostPorts.value.map((b) => `${b.host_port}/${b.protocol}`).join(', '))
// Rolling is hidden (not just disabled) when it cannot run, so the list only
// contains choices that do what they say.
const availableStrategies = computed(() =>
  hasHostPorts.value ? STRATEGIES.filter((s) => s.value !== 'rolling') : STRATEGIES,
)
async function loadBindings() {
  if (!wid.value) return
  try {
    appBindings.value = (await portBindingApi.listByApp(wid.value, appId.value)).data.data ?? []
  } catch { /* the strategy guard just stays off */ }
}
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
// Narrows the attach dropdown. The selected volume always stays in the list so
// filtering can never silently invalidate the binding.
const volumeSearch = ref('')
const volumeOptions = computed(() => {
  const q = volumeSearch.value.trim().toLowerCase()
  if (!q) return volumesOnNode.value
  return volumesOnNode.value.filter(
    (v) => v.id === mount.value.volume_id ||
      v.name.toLowerCase().includes(q) ||
      (v.display_name || '').toLowerCase().includes(q) ||
      (v.driver || '').toLowerCase().includes(q),
  )
})
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
// Which network's addressing (IPv4, IPv6, gateways) the detail modal is showing.
const netDetailFor = ref<Network | null>(null)
// The IPv6 column appears only when a container actually has an address: on a single-stack install
// it would otherwise be a dash on every row.
const anyContainerIPv6 = computed(() => (liveStatus.value?.networks ?? []).some((n) => !!n.ipv6_address))
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
  cpu_cores: number; memory_mb: number; gpu_count: number; gpu_kind: string; run_as_user: string; add_capabilities: string[]; devices: string[]; restart_policy: RestartPolicy; image_pull_policy: ImagePullPolicy
  reconcile_policy: ReconcilePolicy
  read_only_root_filesystem: boolean; no_new_privileges: boolean; drop_capabilities: string
  // Healthcheck
  hc_type: HealthcheckType; hc_path: string; hc_port: number | null; hc_command: string
  hc_interval: number; hc_timeout: number; hc_retries: number; hc_start_period: number
}
function emptySettingsForm(): SettingsForm {
  return {
    image: '', tag: '', command: '', registry_id: null, git_repository_id: null, git_repo: '', git_ref: '', build_method: 'auto', builder: '', stack_id: null, network_ids: [], ports: [],
    deploy_strategy: 'rolling', canary_initial_weight: 10, canary_step_weight: 20, canary_step_interval_seconds: 60,
    cpu_cores: 0, memory_mb: 0, gpu_count: 0, gpu_kind: '', run_as_user: '', add_capabilities: [], devices: [], restart_policy: 'unless-stopped', image_pull_policy: 'always',
    reconcile_policy: 'inherit',
    read_only_root_filesystem: false, no_new_privileges: false, drop_capabilities: '',
    hc_type: 'none', hc_path: '/', hc_port: null, hc_command: '', hc_interval: 30, hc_timeout: 5, hc_retries: 3, hc_start_period: 0,
  }
}
const settingsForm = ref<SettingsForm>(emptySettingsForm())

// --- Source editing -------------------------------------------------------
const SOURCE_TYPES: { value: 'image' | 'git'; label: string; hint: string; icon: string }[] = [
  { value: 'image', label: 'appDetail.source.image', hint: 'appDetail.source.imageHint', icon: 'mdi-docker' },
  { value: 'git', label: 'appDetail.source.git', hint: 'appDetail.source.gitHint', icon: 'mdi-git' },
]

// The source is edited separately from the rest of settings, because switching image <-> git is a
// whole-source replacement rather than a field edit: the server clears the fields belonging to the
// source being left, may drop a repo-owned pipeline, and marks the app for redeploy. Folding that
// into "Save settings" would make a destructive change look like an incidental one.
const editingSource = ref(false)
const savingSource = ref(false)
// sourceDraft.type is what the user is choosing; app.source_type is what is stored.
const sourceDraft = ref<{ type: 'image' | 'git' }>({ type: 'image' })
const sourceSwitching = computed(() => !!app.value && sourceDraft.value.type !== app.value.source_type)

function beginEditSource() {
  if (!app.value) return
  sourceDraft.value.type = app.value.source_type
  syncSettingsForm() // discard any half-typed edits from a previous attempt
  editingSource.value = true
}
function cancelEditSource() {
  editingSource.value = false
  syncSettingsForm()
}

// sourceValid mirrors the server's rules so Save is disabled rather than failing a round-trip.
const sourceValid = computed(() => {
  if (sourceDraft.value.type === 'image') return !!settingsForm.value.image.trim()
  return !!settingsForm.value.git_repo.trim() || settingsForm.value.git_repository_id != null
})

async function saveSource() {
  if (!wid.value || !app.value || !sourceValid.value) return
  savingSource.value = true
  try {
    const input: SetSourceInput =
      sourceDraft.value.type === 'image'
        ? {
            source_type: 'image',
            image: settingsForm.value.image.trim(),
            tag: settingsForm.value.tag.trim(),
            registry_id: settingsForm.value.registry_id,
          }
        : {
            source_type: 'git',
            git_repo: settingsForm.value.git_repo.trim(),
            git_ref: settingsForm.value.git_ref.trim(),
            git_repository_id: settingsForm.value.git_repository_id,
            build_method: settingsForm.value.build_method,
            builder: settingsForm.value.build_method !== 'dockerfile' ? settingsForm.value.builder.trim() : '',
          }
    const res = (await appApi.setSource(wid.value, appId.value, input)).data.data
    app.value = res.application
    syncSettingsForm()
    editingSource.value = false

    // Say what else moved. None of it is visible in the app record, and a silent pipeline removal
    // would be discovered only at the next deploy.
    const notes: string[] = []
    if (res.change.pipeline_removed) notes.push(t('notify.appDetail.pipelineRemoved'))
    if (res.change.redeploy_required) notes.push(t('notify.appDetail.redeployNeededToApply'))
    const what = res.change.switched
      ? t(res.change.to === 'git' ? 'notify.appDetail.sourceSwitchedGit' : 'notify.appDetail.sourceSwitchedImage')
      : t('notify.appDetail.sourceUpdated')
    notify.success(notes.length ? `${what} — ${notes.join(', ')}.` : what)

    await loadRepoPipeline()
    loadApp()
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingSource.value = false
  }
}

// --- Pipeline re-sync -----------------------------------------------------
const resyncingPipeline = ref(false)

// resyncPipeline re-reads pipelines.yaml from the repository: it adopts one when the app has none
// (the file was added after the app was created, or it only just became a git app) and re-syncs the
// stored spec when it already has one.
async function resyncPipeline() {
  if (!wid.value || !app.value) return
  resyncingPipeline.value = true
  try {
    const res = (await appApi.resyncPipeline(wid.value, appId.value)).data.data
    if (res.adopted) {
      notify.success(t('notify.appDetail.pipelineAdopted', { path: res.pipeline?.source_path }))
    } else if (res.changed) {
      notify.success(t('notify.appDetail.pipelineUpdated', { path: res.pipeline?.source_path }))
    } else {
      notify.info(t('notify.appDetail.alreadyUpToDateWith', { source_path: res.pipeline?.source_path }))
    }
    await loadRepoPipeline()
  } catch (e) {
    notify.apiError(e)
  } finally {
    resyncingPipeline.value = false
  }
}
const limits = ref<ResourceLimits>({ max_cpu_cores: 0, max_memory_mb: 0 })

// splitCommand turns the command input into argv (whitespace-separated), matching
// how one-off Jobs parse their command. Empty = use the image's default CMD.
function splitCommand(s: string): string[] {
  return s.trim().split(/\s+/).filter(Boolean)
}

const HEALTHCHECK_TYPES: { value: HealthcheckType; label: string }[] = [
  { value: 'none', label: 'appDetail.hc.none' },
  { value: 'http', label: 'appDetail.hc.http' },
  { value: 'command', label: 'appDetail.hc.command' },
]
const RESTART_POLICIES: { value: RestartPolicy; label: string }[] = [
  { value: 'unless-stopped', label: 'appDetail.restart.unlessStopped' },
  { value: 'always', label: 'appDetail.restart.always' },
  { value: 'on-failure', label: 'appDetail.restart.onFailure' },
  { value: 'no', label: 'appDetail.restart.no' },
]
const IMAGE_PULL_POLICIES: { value: ImagePullPolicy; label: string }[] = [
  { value: 'always', label: 'appDetail.settings.pullAlways' },
  { value: 'if-not-present', label: 'appDetail.settings.pullIfNotPresent' },
  { value: 'never', label: 'appDetail.settings.pullNever' },
]
// What the control manager may do about this app if its container disappears. Set one app to "off" or
// "observe" when an automatic redeploy would interrupt you, rather than changing the whole platform.
const RECONCILE_POLICIES: { value: ReconcilePolicy; label: string }[] = [
  { value: 'inherit', label: 'appDetail.settings.reconcileDefault' },
  { value: 'off', label: 'appDetail.settings.reconcileIgnore' },
  { value: 'observe', label: 'appDetail.settings.reconcileReport' },
  { value: 'enforce', label: 'appDetail.settings.reconcileRedeploy' },
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
// Per-deploy build cache override; reset each time the dialog opens so it never sticks.
const deployNoCache = ref(false)
const invalidatingCache = ref(false)

// Invalidation is a state change, not a build: it names a new cache generation and the next build
// (deploy or pipeline run) pays for the cold rebuild.
async function invalidateBuildCache() {
  if (!wid.value) return
  invalidatingCache.value = true
  try {
    await appApi.invalidateBuildCache(wid.value, appId.value)
    notify.success(t('notify.appDetail.cacheInvalidated'), { detail: t('notify.appDetail.cacheInvalidatedDetail') })
  } catch (e) {
    notify.apiError(e, 'Could not invalidate the build cache')
  } finally {
    invalidatingCache.value = false
  }
}
const deployTag = ref('')
const deployStrategy = ref<DeployStrategy>('rolling')
const deploying = ref(false)

// --- Repository pipeline ---
//
// When the app's repository carries a pipeline, deploys run through it. The app
// then has no direct build of its own, which changes what several controls mean:
// the build-method settings stop applying, and a deploy produces a run rather
// than a deployment.
const repoPipeline = ref<PipelineDefinition | null>(null)
const deploysViaPipeline = computed(() => !!repoPipeline.value)

async function loadRepoPipeline() {
  repoPipeline.value = null
  if (!wid.value || app.value?.source_type !== 'git') return
  try {
    // Pipelines are listed workspace-wide; find the repo-owned one bound to this
    // app. Failure is silent — this only enriches the page.
    const res = await pipelineApi.list(wid.value, 0, 100)
    repoPipeline.value =
      (res.data.data ?? []).find((p) => p.application_id === appId.value && p.source === 'repo' && p.enabled) ?? null
  } catch {
    repoPipeline.value = null
  }
}

/**
 * Follow whatever a deploy produced. A pipeline run has its own page — that is
 * where its per-step logs live — while a direct deployment streams into the
 * Deployments tab as before.
 */
function followDeploy(res: Deployment | PipelineRunAccepted, message: string) {
  if (isPipelineRun(res)) {
    notify.success(`${message} — ${t('notify.appDetail.pipelineRunStarted', { number: res.run.number })}`)
    router.push({ name: 'pipeline-run', params: { id: res.run.pipeline_id, runId: res.run.id } })
    return
  }
  notify.success(message)
  tab.value = 'deployments'
  streamLogs(res.id)
}

// Canary (live rollout state derived from the app)
const canaryActive = computed(() => !!app.value?.canary_release_id)
const canaryWeight = computed(() => app.value?.canary_weight ?? 0)
const canaryManual = computed(() => app.value?.canary_mode === 'manual')
// What the rollout card says about the weight, which differs entirely by mode:
// the ramp is going somewhere, a manual canary is being held.
const canaryPaused = computed(() => !!app.value?.canary_paused_at)

const canaryPausedFor = computed(() => {
  const at = app.value?.canary_paused_at
  if (!at) return ''
  const mins = Math.max(0, Math.round((Date.now() - new Date(at).getTime()) / 60000))
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m`
  const hours = Math.round(mins / 60)
  return hours < 24 ? `${hours}h` : `${Math.round(hours / 24)}d`
})

const canaryModeHint = computed(() => {
  if (canaryPaused.value) return `· paused ${canaryPausedFor.value}`
  if (!canaryManual.value) return '· shifting automatically, promotes at 100%'
  const rules = app.value?.canary_match?.length ?? 0
  if (!rules) return '· held manually'
  return `· held manually, ${rules} match rule${rules === 1 ? '' : 's'}`
})

async function toggleCanaryPause() {
  if (!wid.value) return
  canaryBusy.value = true
  try {
    if (canaryPaused.value) {
      await appApi.resumeCanary(wid.value, appId.value)
      notify.success(t('notify.appDetail.canaryResumed'))
    } else {
      await appApi.pauseCanary(wid.value, appId.value)
      notify.success(t('notify.appDetail.canaryPaused'))
    }
    loadApp()
  } catch (e) {
    notify.apiError(e)
  } finally {
    canaryBusy.value = false
  }
}
// The routing panel is only meaningful for an app that canaries at all.
const showCanaryPanel = computed(() => canaryActive.value || app.value?.deploy_strategy === 'canary')
const canaryBusy = ref(false)
async function promoteCanary() {
  if (!wid.value || !app.value) return
  if (!(await askConfirm({
    title: t('confirm.title.appDetail.promoteCanaryToStable'),
    message: t('confirm.message.appDetail.theCanaryReleaseWill', { name: app.value.name }),
    confirmLabel: t('confirm.label.appDetail.promoteNow'),
    cancelLabel: t('action.cancel'),
    variant: 'primary',
  }))) return
  canaryBusy.value = true
  try {
    const dep = (await appApi.promoteCanary(wid.value, appId.value)).data.data
    notify.success(t('notify.appDetail.canaryPromoting'))
    tab.value = 'deployments'
    streamLogs(dep.id)
  } catch (e) { notify.apiError(e) } finally { canaryBusy.value = false }
}
async function abortCanary() {
  if (!wid.value || !app.value) return
  if (!(await askConfirm({
    title: t('confirm.title.appDetail.abortCanaryRollout'),
    message: t('confirm.message.appDetail.theCanaryContainerIs', { name: app.value.name }),
    confirmLabel: t('confirm.label.appDetail.abortCanary'),
    cancelLabel: t('confirm.label.appDetail.keepRollingOut'),
    variant: 'danger',
  }))) return
  canaryBusy.value = true
  try {
    await appApi.abortCanary(wid.value, appId.value)
    notify.success(t('notify.appDetail.canaryAborted'))
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

// The workspace's containers must drop root (restricted profile or a server-level
// mandate), so a run-as user has to be a non-root numeric uid.
const requireNonRoot = ref(false)
// Mirrors the server's rule so the field explains itself before a save round-trips.
// The card is absent, not disabled, in an ordinary workspace: it would be greyed
// in nearly all of them.
const capCatalog = ref<CapabilityCatalog | null>(null)
const canGrant = computed(() => ws.currentWorkspace?.privileged === true && !requireNonRoot.value)
const grantsElevated = computed(() => ws.currentWorkspace?.system === true)

const offeredCapabilities = computed(() =>
  (capCatalog.value?.capabilities ?? []).filter((c) => c.tier === 0 || grantsElevated.value),
)
const offeredDevices = computed(() =>
  (capCatalog.value?.devices ?? []).filter((d) => d.tier === 0 || grantsElevated.value),
)

async function loadCapabilityCatalog() {
  if (capCatalog.value) return
  try {
    capCatalog.value = (await capabilityApi.catalog()).data.data ?? null
  } catch {
    // The picker does not render; the server is the gate either way.
  }
}

function toggleCapability(name: string) {
  const list = settingsForm.value.add_capabilities
  const i = list.indexOf(name)
  if (i >= 0) list.splice(i, 1)
  else list.push(name)
}

function addDevice() {
  settingsForm.value.devices.push('')
}

const dropCapabilityList = computed(() =>
  settingsForm.value.drop_capabilities.split(/[\s,]+/).map((c) => c.trim().toUpperCase()).filter(Boolean),
)
function setNoNewPrivileges(e: Event) {
  settingsForm.value.no_new_privileges = (e.target as HTMLInputElement).checked
}
const dropCapabilitiesError = computed(() =>
  dropCapabilityList.value.some((c) => !/^(CAP_)?[A-Z_]+$/.test(c)) ? 'List capability names such as NET_RAW, or ALL.' : '',
)

const runAsUserError = computed(() => {
  const v = settingsForm.value.run_as_user.trim()
  if (!v) return ''
  if (!/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,31})?$/.test(v)) {
    return 'Use "uid", "uid:gid", "name" or "name:group".'
  }
  if (requireNonRoot.value && !/^[1-9][0-9]*(:[0-9]+)?$/.test(v)) {
    return 'This workspace requires a non-root numeric uid (e.g. 1000 or 1000:1000).'
  }
  return ''
})

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

// The settings form as it was last filled from the server, and which app it was
// filled for. loadApp() runs on every deployment SSE event and on the canary
// rollout poll, so a background refresh must never refill the form on top of
// unsaved work — that erases whatever is being typed.
const settingsBaseline = ref('')
const settingsSyncedFor = ref<number | null>(null)
const settingsEdited = computed(() => JSON.stringify(settingsForm.value) !== settingsBaseline.value)

// refillSettingsForm is what background refreshes call: it fills the form on a
// first load or an app switch, and otherwise leaves unsaved edits alone. Explicit
// resets (cancel, source switch, save) still call syncSettingsForm directly.
function refillSettingsForm() {
  if (settingsSyncedFor.value !== (app.value?.id ?? null) || !settingsEdited.value) syncSettingsForm()
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
    run_as_user: app.value.run_as_user || '',
    add_capabilities: [...(app.value.add_capabilities ?? [])],
    devices: [...(app.value.devices ?? [])],
    read_only_root_filesystem: !!app.value.read_only_root_filesystem,
    no_new_privileges: !!app.value.no_new_privileges,
    drop_capabilities: (app.value.drop_capabilities ?? []).join(', '),
    restart_policy: app.value.restart_policy || 'unless-stopped',
    image_pull_policy: app.value.image_pull_policy || 'always',
    reconcile_policy: app.value.reconcile_policy || 'inherit',
    hc_type: app.value.healthcheck_type || 'none',
    hc_path: app.value.healthcheck_http_path || '/',
    hc_port: app.value.healthcheck_port || null,
    hc_command: app.value.healthcheck_command || '',
    hc_interval: app.value.healthcheck_interval_seconds || 30,
    hc_timeout: app.value.healthcheck_timeout_seconds || 5,
    hc_retries: app.value.healthcheck_retries || 3,
    hc_start_period: app.value.healthcheck_start_period_seconds || 0,
  }
  settingsBaseline.value = JSON.stringify(settingsForm.value)
  settingsSyncedFor.value = app.value.id
}

async function loadApp() {
  if (!wid.value || !appId.value) return
  try {
    app.value = (await appApi.get(wid.value, appId.value)).data.data
    refillSettingsForm()
    await loadRepoPipeline()
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
    requireNonRoot.value = !!usage.capabilities.require_non_root
  } catch {
    shellExecAllowed.value = false
    customLabelsAllowed.value = false
    gpuAllowed.value = false
    requireNonRoot.value = false
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
      // The strategy picker greys out rolling when a host port is published.
      await loadBindings()
      // Unconditional: canGrant depends on the workspace list, which may not have
      // resolved yet. The card's v-if does the gating.
      loadCapabilityCatalog()
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
  return isDeployed.value ? ` — ${t('notify.appDetail.redeployRequired')}` : ''
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
    // Source fields come from the STORED app, never the form: the Source card above owns them, and
    // a half-typed edit sitting in the form must not be applied through this general PATCH — that
    // would bypass the switch semantics (clearing the other source, dropping a repo pipeline).
    app.value = (await appApi.update(wid.value, appId.value, {
      image: app.value.image || undefined,
      tag: app.value.tag || '',
      command: splitCommand(settingsForm.value.command),
      git_repo: app.value.source_type === 'git' ? app.value.git_repo : undefined,
      git_ref: app.value.source_type === 'git' ? app.value.git_ref : undefined,
      build_method: app.value.source_type === 'git' ? app.value.build_method : undefined,
      builder: app.value.source_type === 'git' && app.value.build_method !== 'dockerfile' ? app.value.builder : undefined,
      registry_id: app.value.registry_id ?? null,
      git_repository_id: app.value.git_repository_id ?? null,
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
      run_as_user: settingsForm.value.run_as_user.trim(),
      add_capabilities: settingsForm.value.add_capabilities,
      devices: settingsForm.value.devices.map((d) => d.trim()).filter(Boolean),
      read_only_root_filesystem: settingsForm.value.read_only_root_filesystem,
      no_new_privileges: settingsForm.value.no_new_privileges,
      drop_capabilities: dropCapabilityList.value,
      restart_policy: settingsForm.value.restart_policy,
      image_pull_policy: settingsForm.value.image_pull_policy,
      reconcile_policy: settingsForm.value.reconcile_policy,
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
    notify.success(t('notify.appDetail.settingsSaved') + changeNote())
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
  if (dup) { notify.error(t('notify.appDetail.portIsAlreadyDeclared', { container_port: portForm.value.container_port, protocol: portForm.value.protocol })); return }
  syncSettingsForm()
  settingsForm.value.ports.push({ container_port: portForm.value.container_port, protocol: portForm.value.protocol, scheme: portForm.value.scheme, name: portForm.value.name.trim() })
  await saveSettings()
  showAddPort.value = false
}
async function removeContainerPort(p: AppPort) {
  if (!app.value) return
  if (!(await askConfirm({ title: t('confirm.title.appDetail.removeContainerPort', { container_port: p.container_port, protocol: p.protocol }), message: t('confirm.message.appDetail.anyHostBindingFor'), confirmLabel: t('action.remove'), variant: 'danger' }))) return
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
      if (isDeployed.value && await askConfirm({ title: t('confirm.title.appDetail.hostPortBound'), message: t('confirm.message.appDetail.thePortOnlyPublishes'), confirmLabel: t('confirm.label.appDetail.redeployNow'), cancelLabel: t('confirm.label.appDetail.later') })) {
        followDeploy((await appApi.deploy(wid.value, appId.value, {})).data.data, t('notify.appDetail.redeployToPublish'))
      } else {
        notify.success(t('notify.appDetail.portBound') + changeNote())
      }
    } else {
      notify.success(t('notify.appDetail.bindingRequested'))
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
    ? { title: t('confirm.title.appDetail.releaseHostPort', { host_port: b.host_port }), message: t('confirm.message.appDetail.itStaysPublishedUntil'), confirmLabel: t('confirm.label.appDetail.release'), variant: 'danger' }
    : { title: t('confirm.title.appDetail.cancelPortBinding'), message: t('confirm.message.appDetail.withdrawThisPendingHost'), confirmLabel: t('confirm.label.appDetail.cancelRequest'), cancelLabel: t('confirm.label.appDetail.keep'), variant: 'danger' })
  if (!ok) return
  try {
    await portBindingApi.cancel(wid.value, b.id)
    appBindings.value = (await portBindingApi.listByApp(wid.value, appId.value)).data.data ?? []
    if (approved && await askConfirm({ title: t('confirm.title.appDetail.bindingReleased'), message: t('confirm.message.appDetail.redeployNowToFree'), confirmLabel: t('confirm.label.appDetail.redeployNow'), cancelLabel: t('confirm.label.appDetail.later') })) {
      followDeploy((await appApi.deploy(wid.value, appId.value, {})).data.data, t('notify.appDetail.redeployToFree'))
    } else {
      notify.success(t(approved ? 'notify.appDetail.bindingReleased' : 'notify.appDetail.bindingCancelled'))
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
  // Only follow ?size= when it is present; an absent one keeps the remembered
  // preference rather than resetting the panel on every query change.
  if (isLogSize(size) && size !== logSize.value) logSize.value = size
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
    title: t('confirm.title.appDetail.changeATemplateManaged'),
    // Two keys rather than an optional fragment spliced in: "(v1.2)" sits in a different
    // place in other languages, and a translator cannot move what arrives pre-assembled.
    message: template.value.version
      ? t('confirm.message.appDetail.templateImageVersioned', { name: app.value?.name, template: template.value.name, version: template.value.version })
      : t('confirm.message.appDetail.templateImage', { name: app.value?.name, template: template.value.name }),
    confirmLabel: t('confirm.label.appDetail.changeAnyway'),
    cancelLabel: t('action.cancel'),
    variant: 'danger',
  })
}

async function openDeploy() {
  deployTag.value = app.value?.tag || ''
  deployNoCache.value = false
  showDeploy.value = true
  // Bindings are otherwise only loaded on the Ports tab, and the strategy list
  // depends on them.
  await loadBindings()
  const preferred = app.value?.deploy_strategy || 'rolling'
  deployStrategy.value = availableStrategies.value.some((s) => s.value === preferred)
    ? preferred
    : 'recreate'
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
      no_cache: deployNoCache.value,
      ...(app.value.source_type === 'image' ? { tag: deployTag.value.trim() } : {}),
    }
    const res = (await appApi.deploy(wid.value, appId.value, opts)).data.data
    showDeploy.value = false
    followDeploy(res, t('notify.appDetail.deploymentStarted'))
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
    notify.success(t('notify.appDetail.redeployingRelease'))
    tab.value = 'deployments'
    streamLogs(dep.id)
  } catch (e) { notify.apiError(e) }
}

async function togglePin(r: Release) {
  if (!wid.value) return
  releaseBusy.value = r.id
  try {
    await appApi.pinRelease(wid.value, appId.value, r.id, !r.pinned)
    notify.success(t(r.pinned ? 'notify.appDetail.releaseUnpinned' : 'notify.appDetail.releasePinned'))
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
    title: t('confirm.title.appDetail.deleteRelease'),
    message: t('confirm.message.appDetail.deleteReleaseVThis', { version: r.version }),
    confirmLabel: t('action.delete'),
    variant: 'danger',
  }))) return
  releaseBusy.value = r.id
  try {
    await appApi.deleteRelease(wid.value, appId.value, r.id)
    notify.success(t('notify.appDetail.releaseDeleted'))
    if (releaseDetail.value?.id === r.id) releaseDetail.value = null
    loadTab()
  } catch (e) {
    notify.apiError(e, 'Cannot delete (active or pinned?)')
  } finally {
    releaseBusy.value = null
  }
}

// openEnvModal opens the modal to create a new variable; openEnvEdit loads an
// existing one for update (secret values aren't returned, so they're re-entered).
function openEnvModal() {
  editingEnvKey.value = null
  envForm.value = { key: '', value: '', secret: false }
  showEnvModal.value = true
}
function openEnvEdit(e: AppEnvVar) {
  editingEnvKey.value = e.key
  envForm.value = { key: e.key, value: e.is_secret ? '' : e.value, secret: e.is_secret }
  showEnvModal.value = true
}
async function saveEnv(v: { key: string; value: string; secret: boolean }) {
  if (!wid.value || !v.key) return
  savingEnv.value = true
  try {
    await appApi.setEnvVar(wid.value, appId.value, v.key, v.value, v.secret)
    notify.success(t(editingEnvKey.value ? 'notify.appDetail.varUpdated' : 'notify.appDetail.varAdded') + changeNote())
    showEnvModal.value = false
    loadApp()
    loadTab()
  } catch (e) { notify.apiError(e) } finally { savingEnv.value = false }
}

async function delEnv(e: AppEnvVar) {
  if (!wid.value) return
  if (!(await askConfirm({
    title: t('confirm.title.appDetail.delete', { key: e.key }),
    message: isDeployed.value
      ? t('confirm.message.appDetail.removeVarDeployed', { name: app.value?.name ?? t('confirm.message.appDetail.theApp') })
      : t('confirm.message.appDetail.removeVar', { name: app.value?.name ?? t('confirm.message.appDetail.theApp') }),
    confirmLabel: t('action.delete'),
    variant: 'danger',
  }))) return
  await appApi.deleteEnvVar(wid.value, appId.value, e.key).catch((err: unknown) => notify.apiError(err))
  loadTab()
  loadApp() // refresh the "redeploy required" header badge
}

// Copy an env value to the clipboard (non-secret, or an already-revealed secret).
async function copyEnvValue(e: AppEnvVar) {
  const value = e.is_secret ? revealedEnv.value[e.key] : e.value
  if (value === undefined) return
  try {
    await navigator.clipboard.writeText(value)
    copiedEnvKey.value = e.key
    setTimeout(() => { if (copiedEnvKey.value === e.key) copiedEnvKey.value = '' }, 1500)
  } catch (err) { notify.apiError(err, 'Failed to copy') }
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
  if (isReservedLabelKey(key)) { notify.error(t('notify.appDetail.isReservedByMiabiAnd', { key: key })); return }
  const next = { ...containerLabels.value, [key]: newLabel.value.value }
  try {
    await appApi.setLabels(wid.value, appId.value, next)
    containerLabels.value = next
    newLabel.value = { key: '', value: '' }
    notify.success(t('notify.appDetail.labelSet') + changeNote())
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
    notify.success(t('notify.appDetail.labelRemoved') + changeNote())
    loadApp()
  } catch (e) { notify.apiError(e) }
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
    notify.success(t('notify.common.varsImported', res?.imported ?? 0) + changeNote())
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
    notify.success(t('notify.appDetail.volumeAttached') + changeNote())
    mount.value = { volume_id: 0, path: '' }
    loadApp()
  } catch (e) { notify.apiError(e) }
}

async function detachVolume(volumeId: number) {
  if (!wid.value) return
  await appApi.detachVolume(wid.value, appId.value, volumeId).catch((e: unknown) => notify.apiError(e))
  loadApp()
}

// --- Config file mounts ---
// A config is a set of named files. Mounting it whole projects every file under a
// directory; mounting one key places that single file at an exact path. Both shapes
// exist in the manifest, so the form exposes both rather than only the simple one.
const workspaceConfigs = ref<Config[]>([])
const configMount = ref<{ config_id: number | null; whole: boolean; key: string; path: string }>({
  config_id: null, whole: true, key: '', path: '',
})
const configAttaching = ref(false)

const selectedConfig = computed(
  () => workspaceConfigs.value.find((c) => c.id === configMount.value.config_id) ?? null,
)

async function loadWorkspaceConfigs() {
  if (!wid.value || workspaceConfigs.value.length) return
  try {
    workspaceConfigs.value = (await configApi.list(wid.value)).data.data ?? []
  } catch (e) { notify.apiError(e) }
}

// Reset the file selection whenever the config changes: a key from the previous
// config is never valid for the new one, and the API refuses it.
watch(() => configMount.value.config_id, () => {
  configMount.value.key = ''
})

function configName(id?: number) {
  return workspaceConfigs.value.find((c) => c.id === id)?.name || `config ${id}`
}

// The exact container paths the current form would produce. Path shape is where
// config mounts go wrong, so it is shown rather than described.
const configPathPreview = computed<string[]>(() => {
  const path = configMount.value.path.trim()
  if (!path || !selectedConfig.value) return []
  if (!configMount.value.whole) return [path]
  const dir = path.endsWith('/') ? path : path + '/'
  return (selectedConfig.value.keys ?? []).map((k) => dir + k)
})

const canAttachConfig = computed(() => {
  if (!configMount.value.config_id || !configMount.value.path.trim()) return false
  return configMount.value.whole || !!configMount.value.key
})

async function attachConfig() {
  if (!wid.value || !canAttachConfig.value) return
  configAttaching.value = true
  try {
    await appApi.attachConfig(
      wid.value, appId.value, configMount.value.config_id!,
      configMount.value.path.trim(),
      configMount.value.whole ? '' : configMount.value.key,
    )
    notify.success(t('notify.appDetail.configMounted') + changeNote())
    configMount.value = { config_id: null, whole: true, key: '', path: '' }
    loadApp()
  } catch (e) { notify.apiError(e) }
  finally { configAttaching.value = false }
}

async function detachConfig(configId: number, key: string) {
  if (!wid.value) return
  try {
    await appApi.detachConfig(wid.value, appId.value, configId, key)
    notify.success(t('notify.appDetail.configUnmounted') + changeNote())
    loadApp()
  } catch (e) { notify.apiError(e) }
}

// --- Privileged host mounts (allow-listed; privileged workspaces only) ---
const hostPresets = ref<HostMountPreset[]>([])
const hostMount = ref({ preset: '', path: '', read_only: false })
// Only workspace admins in a privileged workspace may manage host binds.
const canHostMount = computed(() => !!ws.currentWorkspace?.privileged && ws.isWorkspaceAdmin)
// Split mounts by source. A config mount carries neither a volume nor a preset,
// so it has to be excluded explicitly — otherwise it renders as a blank volume
// row keyed on volume_id 0, and its detach button calls detachVolume(0).
const volumeMounts = computed(() => (app.value?.mounts ?? []).filter((m) => !m.host_preset && !m.config_id))
const hostMounts = computed(() => (app.value?.mounts ?? []).filter((m) => m.host_preset))
const configMounts = computed(() => (app.value?.mounts ?? []).filter((m) => !!m.config_id))
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
    notify.success(t('notify.appDetail.hostMountAttached') + changeNote())
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
    notify.success(t('notify.appDetail.appDeleted'))
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
    notify.success(t('notify.appDetail.scaling', next))
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
      notify.success(t('notify.appDetail.redeployingLatest'))
      tab.value = 'deployments'
      streamLogs(data.id)
    } else {
      notify.success(t(`notify.appDetail.${action}Requested`))
    }
    await loadApp()
  } catch (e) {
    notify.apiError(e, t(`notify.appDetail.${action}Failed`))
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


// "by Jonas" when a person asked for it, otherwise the machine cause — pipeline, auto, reconcile.
// A deleted user leaves no name, so the trigger is what still answers "what caused this".
function deployedBy(d: LastDeploy): string {
  if (d.by_name) return t('appDetail.byName', { name: d.by_name })
  return d.trigger ? `· ${d.trigger}` : ''
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
    notify.success(t('notify.common.copied'))
  } catch {
    notify.error(t('notify.appDetail.copyFailed'))
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
      if (!linkForm.value.new_name.trim()) { notify.error(t('notify.appDetail.enterADatabaseName')); return }
      // Create unattached, then attach so the prefix is honored uniformly.
      const created = (await databaseApi.createDatabase(wid.value, selInstance.value.id, linkForm.value.new_name.trim(), null)).data.data
      await appApi.attachDatabase(wid.value, appId.value, created.database.id, prefix)
    } else {
      if (!linkForm.value.database_id) { notify.error(t('notify.appDetail.selectADatabase')); return }
      await appApi.attachDatabase(wid.value, appId.value, linkForm.value.database_id, prefix)
    }
    notify.success(t('notify.appDetail.dbAttached') + changeNote())
    linkModal.value = false
    appDatabases.value = (await appApi.databases(wid.value, appId.value)).data.data ?? []
    loadApp()
  } catch (e) { notify.apiError(e) } finally { linkBusy.value = false }
}

async function detachDatabase(d: AppDatabase) {
  if (!wid.value) return
  try {
    await appApi.detachDatabase(wid.value, appId.value, d.id)
    notify.success(t('notify.appDetail.dbDetached') + changeNote())
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
          <span class="mdi mdi-arrow-left"></span>{{ $t('appDetail.applications') }}</button>
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
              <LocationName :cluster-id="app.cluster_id" />
              <!-- Provenance: where this app came from, linking to its source. -->
              <template v-if="template"> · <span class="mdi mdi-storefront-outline"></span>
                <router-link
                  class="prov-link"
                  :to="{ name: 'template-install', params: { slug: template.name } }"
                  :title="`Installed from the “${template.name}” marketplace template`"
                >{{ template.name }}<template v-if="template.version"> v{{ template.version }}</template></router-link>
              </template>
              <template v-else-if="managedBy === 'gitops'"> · <span class="mdi mdi-source-branch"></span>
                <router-link class="prov-link" :to="{ name: 'gitops' }" :title="$t('appDetail.managedByGitops')">{{ $t('appDetail.gitops') }}</router-link>
              </template>
            </div>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-3 app-header-actions">
        <span v-if="app.redeploy_required" class="badge badge-warning" :title="$t('appDetail.redeployRequired')">
          <span class="mdi mdi-alert-outline"></span>{{ $t('stacks.redeployRequired') }}</span>
        <span class="status-wrap">
          <span class="badge badge-dot" :class="badge(displayStatus)" :title="liveStatus?.container_state ? `container: ${liveStatus.container_state}` : ''">{{ displayStatus }}</span>
          <span v-if="statusDetail" class="text-muted text-sm status-detail">{{ statusDetail }}</span>
        </span>
        <button
          class="btn btn-secondary btn-sm"
          :disabled="app.status !== 'running'"
          :title="$t(app.status === 'running' ? 'appDetail.viewProcesses' : 'appDetail.startToViewProcesses')"
          @click="processesOpen = true"
        >
          <span class="mdi mdi-format-list-bulleted"></span>{{ $t('appDetail.processes') }}</button>
        <template v-if="ws.canEdit">
          <button v-if="app.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" :title="$t('appDetail.stop')" @click="lifecycle('stop')">
            <span class="mdi mdi-stop"></span>{{ $t('appDetail.stop') }}</button>
          <button v-else class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" :title="$t('appDetail.start')" @click="lifecycle('start')">
            <span class="mdi mdi-play"></span>{{ $t('appDetail.start') }}</button>
          <button class="btn btn-secondary btn-sm" :disabled="lifecycleBusy !== ''" :title="$t('appDetail.restartAction')" @click="lifecycle('restart')">
            <span class="mdi" :class="lifecycleBusy === 'restart' ? 'mdi-loading mdi-spin' : 'mdi-restart'"></span>{{ $t('appDetail.restartAction') }}</button>
          <button
            v-if="ws.isWorkspaceAdmin && shellExecAllowed"
            class="btn btn-secondary btn-sm"
            :disabled="app.status !== 'running'"
            :title="$t(app.status === 'running' ? 'appDetail.openShell' : 'appDetail.startToOpenShell')"
            @click="shellOpen = true"
          >
            <span class="mdi mdi-console-line"></span>{{ $t('appDetail.shell') }}</button>
        </template>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openDeploy">
          <span class="mdi mdi-rocket-launch-outline"></span> {{ deployVerb }}
        </button>
      </div>
    </div>

    <!-- Header strip is a shortcut into the rollout; on the Deployments tab the
         full canary card is already shown, so it would be redundant there. -->
    <button v-if="canaryActive && tab !== 'deployments'" class="canary-strip" :title="$t('appDetail.canaryInProgress')" @click="tab = 'deployments'">
      <span class="mdi mdi-rocket-launch-outline"></span>
      <span class="canary-strip-text">Canary {{ canaryWeight }}%</span>
      <span class="split-bar canary-strip-bar">
        <span class="split-stable" :style="{ width: (100 - canaryWeight) + '%' }"></span>
        <span class="split-canary" :style="{ width: canaryWeight + '%' }"></span>
      </span>
    </button>

    <div class="tabs">
      <button v-for="item in tabs" :key="item.key" class="tab" :class="{ active: tab === item.key }" @click="tab = item.key">{{ $t(item.label) }}</button>
    </div>

    <!-- Overview -->
    <div v-if="tab === 'overview'">
      <div v-if="overview" class="card summary mb-4">
        <div class="summary-item">
          <span class="summary-label">{{ $t('dashboard.col.status') }}</span>
          <span class="badge badge-dot" :class="badge(displayStatus)">{{ displayStatus }}</span>
        </div>
        <div v-if="isService" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.replicas') }}</span>
          <span class="summary-value" style="display: inline-flex; align-items: center; gap: 6px">
            <button class="btn-icon btn-icon-muted" :disabled="scaleBusy || replicaTarget <= 1" :title="$t('appDetail.scaleDown')" :aria-label="$t('appDetail.scaleDown')" @click="scaleBy(-1)"><span class="mdi mdi-minus"></span></button>
            <span><span class="mdi mdi-server-network"></span> {{ liveStatus?.service_running_tasks ?? 0 }}/{{ replicaTarget }}</span>
            <button class="btn-icon btn-icon-muted" :disabled="scaleBusy" :title="$t('appDetail.scaleUp')" :aria-label="$t('appDetail.scaleUp')" @click="scaleBy(1)"><span class="mdi mdi-plus"></span></button>
          </span>
        </div>
        <!-- Real replica placement: where the Swarm scheduler actually put the
             running tasks, not the single node the app was created against. -->
        <div v-if="isService && nodePlacement.length" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.nodes') }}</span>
          <span class="summary-value" :title="`Running on ${nodePlacementLabel}`">
            <span class="mdi mdi-server-network"></span> {{ nodePlacementLabel }}
          </span>
        </div>
        <div v-if="liveStatus?.running && liveStatus.uptime_seconds > 0" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.uptime') }}</span>
          <span class="summary-value"><span class="mdi mdi-clock-outline"></span> {{ fmtUptime(liveStatus.uptime_seconds) }}</span>
        </div>
        <div v-if="liveStatus?.health" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.health') }}</span>
          <span class="badge" :class="healthBadge(liveStatus.health)">{{ liveStatus.health }}</span>
        </div>
        <div v-if="liveStatus && liveStatus.restart_count > 0" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.restarts') }}</span>
          <span class="summary-value">{{ liveStatus.restart_count }}×</span>
        </div>
        <!-- Beside the container's own restart count, which counts crashes: this one is a person
             acting on the app. Kept away from Last deploy, which is a different release. -->
        <div v-if="overview.last_lifecycle" class="summary-item">
          <span class="summary-label">{{ $t('appDetail.lastAction') }}</span>
          <span class="summary-value" :title="fmtDateTime(overview.last_lifecycle.at)">
            {{ $t('appDetail.action.' + overview.last_lifecycle.action) }} {{ relTime(overview.last_lifecycle.at) }}
            <span v-if="overview.last_lifecycle.by_name" class="summary-by">{{ $t('appDetail.byName', { name: overview.last_lifecycle.by_name }) }}</span>
          </span>
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
          <span class="summary-label">{{ $t('appDetail.currentRelease') }}</span>
          <span class="summary-value">{{ overview.current_version ? `v${overview.current_version}` : '—' }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'volumes'">
          <span class="summary-label">{{ $t('appDetail.volumes') }}</span>
          <span class="summary-value">{{ overview.volumes_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'routes'">
          <span class="summary-label">{{ $t('appDetail.routes') }}</span>
          <span class="summary-value">{{ overview.routes_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'network'">
          <span class="summary-label">{{ $t('appDetail.networks') }}</span>
          <span class="summary-value">{{ overview.networks_count }}</span>
        </div>
        <div class="summary-item clickable" @click="tab = 'environment'">
          <span class="summary-label">{{ $t('appDetail.envVars') }}</span>
          <span class="summary-value">{{ overview.env_count }}</span>
        </div>
        <!-- Beside Created, the other lifecycle date: when the running code last changed, and who
             asked for it. Clicks through to the deployment itself. -->
        <div class="summary-item" :class="{ clickable: overview.last_deploy }" @click="overview.last_deploy && (tab = 'deployments')">
          <span class="summary-label">{{ $t('appDetail.lastDeploy') }}</span>
          <span v-if="overview.last_deploy" class="summary-value" :title="fmtDateTime(overview.last_deploy.at)">
            {{ relTime(overview.last_deploy.at) }}
            <span class="summary-by">{{ deployedBy(overview.last_deploy) }}</span>
          </span>
          <span v-else class="summary-value">—</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">{{ $t('dashboard.col.created') }}</span>
          <span class="summary-value">{{ overview.created_at ? new Date(overview.created_at).toLocaleDateString() : '—' }}</span>
        </div>
      </div>

      <h2 class="section-title">
        {{ $t('appDetail.metrics.resourceUsage') }}
        <span v-if="metrics" class="live-tag"><span class="live-dot"></span> {{ $t('appDetail.metrics.live') }}</span>
      </h2>
      <div v-if="metrics" class="stats-grid">
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">CPU</span><span class="stat-icon stat-icon-primary"><span class="mdi mdi-chip"></span></span></div>
          <div class="stat-value">{{ metrics.cpu_percent.toFixed(1) }}%</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.cpu_percent)" :style="{ width: Math.min(100, metrics.cpu_percent) + '%' }"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('appDetail.memory') }}</span><span class="stat-icon stat-icon-info"><span class="mdi mdi-memory"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.memory_usage_bytes) }}</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.memory_percent)" :style="{ width: Math.min(100, metrics.memory_percent) + '%' }"></div></div>
          <div class="stat-sub">
            {{ metrics.memory_percent.toFixed(1) }}%<template v-if="metrics.memory_limit_bytes"> of {{ fmtSize(metrics.memory_limit_bytes) }}</template>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('db.netIn') }}</span><span class="stat-icon stat-icon-success"><span class="mdi mdi-download"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.network_rx_bytes) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('db.netOut') }}</span><span class="stat-icon stat-icon-warning"><span class="mdi mdi-upload"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.network_tx_bytes) }}</div>
        </div>
      </div>
      <div v-else class="card mb-4">
        <div class="empty-state" style="padding: 28px">
          <span class="mdi mdi-chart-line" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>{{ $t('appDetail.metrics.noMetrics') }}</p>
        </div>
      </div>

      <MetadataCard :metadata="app?.metadata" style="margin-top: 20px" />

      <MetadataCard :metadata="app?.annotations" :title="$t('appDetail.annotations')" :reserved="false" style="margin-top: 20px" />

      <div class="card" style="margin-top: 20px">
        <div class="card-header">
          <h2>{{ $t('dashboard.events.title') }}</h2>
          <button class="btn btn-ghost btn-sm" @click="tab = 'events'">{{ $t('dashboard.viewAll') }}</button>
        </div>
        <div v-if="overviewLoading && latestEvents.length === 0" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="latestEvents.length === 0" class="empty-state" style="padding: 28px">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>{{ $t('gitops.noEventsYet') }}</p>
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
        <div class="card-header"><h2>{{ $t('appDetail.internalAccess') }}</h2></div>
        <div class="card-body">
          <i18n-t keypath="appDetail.net.hostnameHint" tag="p" class="text-muted text-sm" style="margin-top: 0">
            <template #stable><strong>{{ $t('appDetail.net.stableAcrossRedeploys') }}</strong></template>
          </i18n-t>
          <div class="net-row">
            <span class="net-label">{{ $t('appDetail.hostname') }}</span>
            <div class="net-vals">
              <span class="net-chip"><code>{{ hostname }}</code>
                <button class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(hostname)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
              <span v-for="p in networkPorts" :key="`h${p}`" class="net-chip"><code>{{ hostname }}:{{ p }}</code>
                <button class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(`${hostname}:${p}`)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
            </div>
          </div>
          <div v-if="stackHostname" class="net-row">
            <span class="net-label">{{ $t('appDetail.stackHostname') }}</span>
            <div class="net-vals">
              <span class="net-chip"><code>{{ stackHostname }}</code>
                <button class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(stackHostname)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
              <span v-for="p in networkPorts" :key="`s${p}`" class="net-chip"><code>{{ stackHostname }}:{{ p }}</code>
                <button class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(`${stackHostname}:${p}`)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
              </span>
            </div>
            <p class="net-hint">{{ $t('appDetail.net.stackHostnameHint') }}</p>
          </div>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('appDetail.externalAccess') }}</h2></div>
        <div class="card-body">
          <template v-if="extAccess && !extAccess.enabled">
            <i18n-t keypath="appDetail.net.extOffHint" tag="p" class="text-muted text-sm" style="margin-top: 0">
              <template #field><strong>{{ $t('appDetail.net.externalDomain') }}</strong></template>
            </i18n-t>
          </template>
          <template v-else-if="extAccess">
            <i18n-t keypath="appDetail.net.extOnHint" tag="p" class="text-muted text-sm" style="margin-top: 0">
              <template #domain><code>*.{{ extAccess.base_domain }}</code></template>
            </i18n-t>
            <div v-if="networkPorts.length === 0" class="text-muted text-sm">{{ $t('appDetail.net.declarePortsFirst') }}</div>
            <div v-else class="ext-ports">
              <div v-for="p in networkPorts" :key="`ext${p}`" class="ext-row">
                <label class="ext-toggle">
                  <input type="checkbox" :checked="extSelected.has(p)" :disabled="!ws.canEdit" @change="toggleExtPort(p)" />
                  <code>{{ p }}</code>
                </label>
                <a v-if="extUrlFor(p)" class="host-link" :href="extUrlFor(p)" target="_blank" rel="noopener">{{ extUrlFor(p) }}<span class="mdi mdi-open-in-new"></span></a>
                <span v-else-if="extSelected.has(p)" class="text-muted text-sm">{{ $t('appDetail.urlGeneratedOnSave') }}</span>
              </div>
            </div>
            <div v-if="ws.canEdit" class="flex items-center gap-2 mt-4">
              <button class="btn btn-primary btn-sm" :disabled="extSaving || !extDirty" @click="saveExternalAccess">
                {{ extSaving ? 'Saving…' : 'Save external access' }}
              </button>
              <button v-if="extExposedCount > 0" class="btn btn-secondary btn-sm" :disabled="extSaving" @click="disableExternalAccess">{{ $t('appDetail.disableExternalAccess') }}</button>
            </div>
          </template>
        </div>
      </div>

      <!-- Which workspace networks the app is attached to. The default network is
           always one of them, and in cluster mode it is a Swarm overlay — which is
           what lets the app reach a database on another node. -->
      <div class="card mb-4">
        <div class="card-header">
          <h2>{{ $t('appDetail.networks') }}</h2>
          <span class="text-muted text-sm">{{ $t('appDetail.net.networksHint') }}</span>
        </div>
        <div v-if="attachedNets.length === 0" class="card-body text-muted text-sm">{{ $t('db.notConnectedToAnyNetwork') }}</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.network') }}</th><th>{{ $t('db.dockerName') }}</th><th>{{ $t('appDetail.driver') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">
                  {{ n.name }}
                  <span v-if="n.is_default" class="badge badge-info" style="margin-left: 6px">{{ $t('appDetail.default') }}</span>
                </td>
                <td class="cell-sub mono">{{ n.docker_name }}</td>
                <td class="cell-sub">
                  {{ n.driver }}<span v-if="n.internal"> {{ $t('appDetail.net.internal') }}</span>
                  <span v-if="n.driver === 'overlay'" class="text-muted"> {{ $t('appDetail.net.spansNodes') }}</span>
                </td>
                <td class="text-right">
                  <button class="btn-icon btn-icon-sm btn-icon-accent" :title="$t('appDetail.networkDetails')" :aria-label="$t('appDetail.networkDetails')" @click="netDetailFor = n">
                    <span class="mdi mdi-ip-network-outline"></span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <i18n-t keypath="appDetail.net.attachHint" tag="div" class="card-body text-muted text-sm" style="padding-top: 0">
          <template #link><a class="net-link" @click="tab = 'settings'">{{ $t('appDetail.tab.settings') }}</a></template>
        </i18n-t>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('appDetail.containerIp') }}</h2></div>
        <div class="card-body" style="padding-bottom: 4px">
          <i18n-t keypath="appDetail.net.ipHint" tag="p" class="text-muted text-sm" style="margin-top: 0">
            <template #ephemeral><strong>{{ $t('appDetail.net.ephemeral') }}</strong></template>
          </i18n-t>
        </div>
        <!-- A replicated service has no single container IP: Swarm may run its tasks
             on any node, and they are replaced on every update. Say that, rather than
             falling through to "no IP yet", which reads as broken for a healthy app. -->
        <div v-if="isService" class="card-body text-muted text-sm" style="padding-top: 0">{{ $t('appDetail.net.replicatedNoIp') }}</div>
        <div v-else-if="liveStatus?.networks?.length" class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>{{ $t('appDetail.network') }}</th><th>{{ $t('appDetail.ipAddress') }}</th>
                <!-- Only when something actually has one: on a single-stack install this column
                     would be a dash on every row, for every user. -->
                <th v-if="anyContainerIPv6">{{ $t('appDetail.ipv6Address') }}</th>
                <th>{{ $t('appDetail.gateway') }}</th><th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="n in liveStatus.networks" :key="n.name">
                <td class="cell-sub">{{ n.name }}</td>
                <td class="mono"><code>{{ n.ip_address }}</code></td>
                <td v-if="anyContainerIPv6" class="mono">
                  <code v-if="n.ipv6_address">{{ n.ipv6_address }}</code>
                  <span v-else class="text-muted">—</span>
                </td>
                <td class="cell-sub mono">{{ n.gateway || '—' }}</td>
                <td class="text-right">
                  <button class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(n.ip_address)"><span class="mdi mdi-content-copy" style="font-size: 13px"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="card-body text-muted text-sm" style="padding-top: 0">{{ $t('appDetail.net.noIpYet') }}</div>
      </div>

      <div class="card">
        <i18n-t keypath="appDetail.net.publicHint" tag="div" class="card-body text-sm text-muted">
          <template #routes><a class="net-link" @click="tab = 'routes'">{{ $t('appDetail.tab.routes') }}</a></template>
          <template #ports><a class="net-link" @click="tab = 'ports'">{{ $t('appDetail.tab.ports') }}</a></template>
        </i18n-t>
      </div>
    </div>

    <!-- Events -->
    <div v-else-if="tab === 'events'" class="card">
      <div class="card-header">
        <h2>{{ $t('appDetail.tab.events') }}</h2>
        <span class="live-dot" :title="$t('appDetail.logs.liveTab')"></span>
      </div>
      <div v-if="eventsLoading && appEvents.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="appEvents.length === 0" class="empty-state">
        <span class="mdi mdi-timeline-text-outline" style="font-size: 40px; color: var(--text-muted)"></span>
        <h3>{{ $t('db.noEventsYet') }}</h3>
        <p>{{ $t('appDetail.events.emptyHint') }}</p>
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
          <h2>{{ $t('appDetail.runtimeLogs') }}</h2>
          <LogSizeControl v-model="logSize" />
        </div>
        <div class="log-toolbar">
          <div class="log-search" :class="{ 'log-search-error': logRegexError }">
            <input
              v-model="logSearch"
              type="search"
              class="form-input log-search-input"
              :placeholder="$t(logRegexMode ? 'appDetail.logs.searchRegex' : 'appDetail.logs.search')"
              :aria-label="$t('appDetail.searchRuntimeLogs')"
            />
            <button v-if="logSearch" type="button" class="log-search-clear" @click="logSearch = ''">{{ $t('appDetail.clear') }}</button>
          </div>
          <button
            type="button"
            class="log-regex-toggle"
            :class="{ active: logRegexMode }"
            :aria-pressed="logRegexMode"
            :title="$t('appDetail.matchUsingARegularExpression')"
            @click="logRegexMode = !logRegexMode"
          >.*</button>
          <span v-if="logRegexError" class="text-sm log-regex-err">{{ $t('appDetail.invalidRegex') }}</span>
          <span v-else-if="logSearch.trim()" class="text-muted text-sm log-match-count">{{ filteredRuntimeLogs.length }} / {{ runtimeLogs.length }}</span>
          <button
            type="button"
            class="log-icon-btn"
            :disabled="!filteredRuntimeLogs.length"
            :title="$t(logSearch.trim() ? 'appDetail.logs.copyMatching' : 'appDetail.copyLogs')"
            :aria-label="$t('appDetail.copyLogs')"
            @click="copyLogs"
          >
            <span class="mdi" :class="logCopied ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
          <button
            type="button"
            class="log-icon-btn"
            :disabled="!filteredRuntimeLogs.length"
            :title="$t(logSearch.trim() ? 'appDetail.logs.downloadMatching' : 'appDetail.downloadLogs')"
            :aria-label="$t('appDetail.downloadLogs')"
            @click="downloadLogs"
          >
            <span class="mdi mdi-tray-arrow-down"></span>
          </button>
          <button
            type="button"
            class="log-follow-btn"
            :class="{ active: logFollow }"
            :aria-pressed="logFollow"
            :title="$t(logFollow ? 'appDetail.logs.following' : 'appDetail.logs.jumpToLatest')"
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
          <span v-if="!runtimeLogs.length" class="log-placeholder">{{ $t('appDetail.logs.waiting') }}</span>
          <span v-else-if="!filteredRuntimeLogs.length" class="log-placeholder">{{ $t('appDetail.logs.noMatches') }}</span>
          <template v-else>
            <div v-for="(line, i) in filteredRuntimeLogs" :key="i" class="log-line"><span v-for="(seg, j) in logSegments(line)" :key="j" :class="{ 'log-hit': seg.hit }">{{ seg.text }}</span></div>
          </template>
        </div>
      </div>
    </div>

    <!-- Deployments -->
    <div v-else-if="tab === 'deployments'" class="detail-grid">
      <div v-if="canaryActive" class="card canary-card">
        <div class="canary-row">
          <span class="canary-title"><span class="mdi mdi-call-split"></span>{{ $t('appDetail.canaryRollout') }}</span>
          <div class="canary-meter">
            <div class="split-bar">
              <div class="split-stable" :style="{ width: (100 - canaryWeight) + '%' }"></div>
              <div class="split-canary" :style="{ width: canaryWeight + '%' }"></div>
            </div>
            <div class="canary-legend">
              <span><i class="dot dot-stable"></i> stable {{ 100 - canaryWeight }}%</span>
              <span><i class="dot dot-canary"></i> canary {{ canaryWeight }}%</span>
              <span class="text-muted">{{ canaryModeHint }}</span>
            </div>
          </div>
          <div v-if="ws.canEdit" class="canary-actions">
            <button
              v-if="!canaryManual"
              class="btn btn-secondary btn-sm"
              :disabled="canaryBusy"
              :title="$t(canaryPaused ? 'appDetail.canary.resumeHint' : 'appDetail.canary.pauseHint')"
              @click="toggleCanaryPause"
            >
              <span class="mdi" :class="canaryPaused ? 'mdi-play' : 'mdi-pause'"></span>
              {{ canaryPaused ? 'Resume' : 'Pause' }}
            </button>
            <button class="btn btn-primary btn-sm" :disabled="canaryBusy" @click="promoteCanary">{{ $t('appDetail.promote') }}</button>
            <button class="btn btn-secondary btn-sm" :disabled="canaryBusy" @click="abortCanary">{{ $t('appDetail.abort') }}</button>
          </div>
        </div>
      </div>
      <div v-if="showCanaryPanel && app && wid" class="canary-panel-slot">
        <CanaryPanel :app="app" :ws-id="wid ?? 0" :can-edit="ws.canEdit" @changed="loadApp" />
      </div>
      <div class="card">
        <div class="card-header"><h2>{{ $t('appDetail.deployments') }}</h2></div>
        <div v-if="deployments.length === 0" class="empty-state">
          <span class="mdi mdi-rocket-launch-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('appDetail.noDeploymentsYet') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.deployment') }}</th><th>{{ $t('appDetail.image') }}</th><th>{{ $t('appDetail.by') }}</th><th>{{ $t('appDetail.when') }}</th><th class="text-right">{{ $t('dashboard.col.status') }}</th></tr></thead>
            <tbody>
              <tr v-for="d in deployments" :key="d.id" class="row-clickable" :class="{ 'row-selected': streamingId === d.id }" @click="streamLogs(d.id)">
                <td>
                  <span class="cell-title">#{{ d.number }}</span>
                  <div v-if="d.error" class="dep-err" :title="d.error"><span class="mdi mdi-alert-circle-outline"></span> {{ d.error }}</div>
                </td>
                <td class="cell-sub mono trunc" :title="d.image">{{ d.image || '—' }}</td>
                <!-- A person when one asked for it, otherwise what did: pipeline, auto, reconcile. -->
                <td class="cell-sub trunc" :title="d.triggered_by_name || d.trigger">
                  <template v-if="d.triggered_by_name">{{ d.triggered_by_name }}</template>
                  <span v-else class="text-muted">{{ d.trigger || '—' }}</span>
                </td>
                <td class="cell-sub" :title="fmtDateTime(d.created_at)">{{ relTime(d.created_at) }}</td>
                <td class="text-right">
                  <span v-if="d.current" class="badge badge-success badge-dot">{{ $t('appDetail.live') }}</span>
                  <span v-else class="badge" :class="depBadge(d.status)">{{ d.status }}</span>
                  <span v-if="streamingId === d.id" class="cell-sub streaming-tag">{{ $t('appDetail.viewingLogs') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><h2>{{ $t('appDetail.tab.logs') }} <span v-if="streamingNumber" class="cell-sub">#{{ streamingNumber }}</span></h2></div>
        <div class="card-body">
          <LogViewer
            :lines="logs"
            :streaming="deployStreaming"
            :status-label="deployStatus"
            :status-class="depStatusClass(deployStatus)"
            :trimmed-note="deployLogsTrimmed ? `Showing the most recent ${RUNTIME_LOG_CAP.toLocaleString()} lines — older output was trimmed.` : ''"
            :placeholder="$t('appDetail.logs.selectDeployment')"
            :download-name="`${app?.name || 'app'}-deployment-${streamingNumber ?? ''}`"
            search-label="Search deployment logs"
          />
        </div>
      </div>
    </div>

    <!-- Environment -->
    <div v-else-if="tab === 'environment'" class="card">
      <div class="card-header">
        <div>
          <h2>{{ $t('appDetail.environmentVariables') }}</h2>
          <i18n-t keypath="appDetail.env.subtitle" tag="p" class="card-subtitle">
            <template #ref><code>{{ secretRefHint }}</code></template>
            <template #link><RouterLink to="/secrets">{{ $t('appDetail.env.manageSecrets') }}</RouterLink></template>
          </i18n-t>
        </div>
        <div v-if="ws.canEdit" class="flex items-center gap-2">
          <button class="btn btn-secondary btn-sm" @click="showEnvImport = true"><span class="mdi mdi-import"></span>{{ $t('stacks.importEnv') }}</button>
          <button class="btn btn-primary btn-sm" @click="openEnvModal"><span class="mdi mdi-plus"></span>{{ $t('stacks.addVariable') }}</button>
        </div>
      </div>

      <!-- Toolbar: count summary + client-side filter -->
      <div v-if="envVars.length" class="env-toolbar">
        <div class="env-stats text-muted text-sm">
          <strong>{{ envVars.length }}</strong> variable{{ envVars.length === 1 ? '' : 's' }}
          <span v-if="secretEnvCount" class="env-stats-dot">·</span>
          <span v-if="secretEnvCount"><span class="mdi mdi-lock-outline"></span> {{ secretEnvCount }} secret</span>
        </div>
        <div class="env-search">
          <span class="mdi mdi-magnify env-search-icon"></span>
          <input v-model="envSearch" class="form-input" type="search" :aria-label="$t('appDetail.filterVariables')" :placeholder="$t('appDetail.env.filterPlaceholder')" />
        </div>
      </div>

      <!-- Empty: no variables at all -->
      <div v-if="envVars.length === 0" class="empty-state">
        <span class="mdi mdi-tune-variant" style="font-size: 36px; color: var(--text-muted)"></span>
        <h3>{{ $t('appDetail.noEnvironmentVariables') }}</h3>
        <p>{{ $t('appDetail.env.emptyHint') }}</p>
        <div v-if="ws.canEdit" class="flex items-center gap-2 mt-4" style="justify-content: center">
          <button class="btn btn-secondary" @click="showEnvImport = true"><span class="mdi mdi-import"></span>{{ $t('stacks.importEnv') }}</button>
          <button class="btn btn-primary" @click="openEnvModal"><span class="mdi mdi-plus"></span>{{ $t('stacks.addVariable') }}</button>
        </div>
      </div>

      <!-- Empty: filter matched nothing -->
      <div v-else-if="filteredEnvVars.length === 0" class="empty-state">
        <span class="mdi mdi-magnify" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>No variables match “{{ envSearch }}”.</p>
        <button class="btn btn-ghost btn-sm mt-4" @click="envSearch = ''">{{ $t('volumes.clearFilter') }}</button>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('appDetail.key') }}</th><th>{{ $t('appDetail.value') }}</th><th class="text-right">{{ $t('volumes.actions') }}</th></tr></thead>
          <tbody>
            <tr v-for="e in filteredEnvVars" :key="e.id" class="env-row">
              <td class="cell-title">
                <span class="env-key">{{ e.key }}</span>
                <span v-if="e.is_secret" class="badge badge-neutral env-secret-tag" :title="$t('stacks.encryptedAtRest')"><span class="mdi mdi-lock-outline"></span>{{ $t('appDetail.secret') }}</span>
              </td>
              <td class="text-muted env-value-cell">
                <template v-if="e.is_secret">
                  <code v-if="revealedEnv[e.key] !== undefined" class="env-revealed">{{ revealedEnv[e.key] }}</code>
                  <span v-else class="env-mask" :aria-label="$t('stacks.hiddenSecretValue')">••••••••••••</span>
                </template>
                <code v-else class="env-revealed">{{ e.value }}</code>
              </td>
              <td class="text-right env-actions">
                <button
                  v-if="!e.is_secret || revealedEnv[e.key] !== undefined"
                  class="btn-icon btn-icon-muted"
                  :title="$t(copiedEnvKey === e.key ? 'notify.common.copied' : 'appDetail.copyValue')"
                  :aria-label="copiedEnvKey === e.key ? 'Copied' : 'Copy value'"
                  @click="copyEnvValue(e)"
                ><span class="mdi" :class="copiedEnvKey === e.key ? 'mdi-check text-success' : 'mdi-content-copy'"></span></button>
                <button v-if="e.is_secret && ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="$t(revealedEnv[e.key] !== undefined ? 'appDetail.hideValue' : 'appDetail.revealValue')" :aria-label="revealedEnv[e.key] !== undefined ? 'Hide value' : 'Reveal value'" :disabled="revealingEnv === e.key" @click="toggleReveal(e)"><span class="mdi" :class="revealingEnv === e.key ? 'mdi-loading mdi-spin' : (revealedEnv[e.key] !== undefined ? 'mdi-eye-off-outline' : 'mdi-eye-outline')"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEnvEdit(e)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="delEnv(e)"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Routes -->
    <div v-else-if="tab === 'routes'" class="card">
      <div class="card-header">
        <h2>{{ $t('appDetail.routes') }}</h2>
        <div class="flex items-center gap-2">
          <button class="btn btn-ghost btn-sm" @click="router.push('/routes')">{{ $t('appDetail.manageRoutes') }}</button>
          <button v-if="ws.canEdit" class="btn btn-primary btn-sm" @click="addRoute">
            <span class="mdi mdi-plus"></span>{{ $t('dashboard.quick.addRoute.label') }}</button>
        </div>
      </div>
      <div v-if="appRoutes.length === 0" class="empty-state">
        <span class="mdi mdi-routes" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('appDetail.noRoutesForThisApp') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="addRoute">{{ $t('appDetail.addARoute') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('appDetail.route') }}</th><th>{{ $t('appDetail.hosts') }}</th><th>TLS</th><th class="text-right">{{ $t('dashboard.col.status') }}</th></tr></thead>
          <tbody>
            <tr v-for="r in appRoutes" :key="r.id" class="row-clickable" @click="editRoute(r)">
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
          <h2>{{ $t('apps.form.containerPorts') }}</h2>
          <button v-if="ws.canEdit" class="btn btn-ghost btn-sm" @click="openAddPort"><span class="mdi mdi-plus"></span>{{ $t('appDetail.addPort') }}</button>
        </div>
        <div v-if="!app.ports || app.ports.length === 0" class="empty-state">
          <span class="mdi mdi-ethernet" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('appDetail.noContainerPortsDeclared') }}</p>
          <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openAddPort">{{ $t('appDetail.addAPort') }}</button>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.port') }}</th><th>{{ $t('appDetail.protocol') }}</th><th>{{ $t('appDetail.scheme') }}</th><th>{{ $t('apps.form.name') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="p in app.ports" :key="p.id">
                <td class="cell-title">{{ p.container_port }}</td>
                <td class="cell-sub">{{ p.protocol }}</td>
                <td class="cell-sub">{{ p.scheme || 'http' }}</td>
                <td class="cell-sub">{{ p.name || '—' }}</td>
                <td class="text-right table-actions">
                  <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" @click="openBindReq(p)">{{ $t('appDetail.requestHostBinding') }}</button>
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('apps.form.removePort')" :aria-label="$t('apps.form.removePort')" @click="removeContainerPort(p)"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <h2>{{ $t('appDetail.hostPortBindings') }}</h2>
          <button v-if="ws.canEdit" class="btn btn-ghost btn-sm" @click="openBindReq()"><span class="mdi mdi-plus"></span>{{ $t('appDetail.requestBinding') }}</button>
        </div>
        <div v-if="appBindings.length === 0" class="empty-state">
          <span class="mdi mdi-swap-horizontal" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('appDetail.ports.noBindings') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.mapping') }}</th><th>{{ $t('dashboard.col.status') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="b in appBindings" :key="b.id">
                <td class="cell-title">
                  {{ b.host_port }} → {{ b.container_port }}/{{ b.protocol }}
                </td>
                <td>
                  <span class="badge badge-dot" :class="bindBadge(b.status)">{{ b.status }}</span>
                  <span v-if="b.review_note" class="cell-sub" style="margin-left: 8px">{{ b.review_note }}</span>
                </td>
                <td class="text-right">
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t(b.status === 'approved' ? 'appDetail.ports.releasePort' : 'appDetail.ports.cancelRequest')" :aria-label="b.status === 'approved' ? 'Release host port' : 'Cancel request'" @click="removeBind(b)"><span class="mdi" :class="b.status === 'approved' ? 'mdi-delete-outline' : 'mdi-close'"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Volumes -->
    <div v-else-if="tab === 'volumes'" class="card">
      <div class="card-header">
        <h2>{{ $t('appDetail.attachedVolumes') }}</h2>
        <button class="btn btn-ghost btn-sm" @click="router.push('/volumes')">{{ $t('appDetail.manageVolumes') }}</button>
      </div>
      <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
        <form class="flex items-center gap-2" @submit.prevent="attachVolume">
          <input
            v-if="volumesOnNode.length > 5"
            v-model="volumeSearch"
            class="form-input"
            type="search"
            :aria-label="$t('volumes.filterVolumes')"
            :placeholder="$t('volumes.filterPlaceholder')"
            style="max-width: 180px"
          />
          <select v-model.number="mount.volume_id" class="form-select" :aria-label="$t('appDetail.volumeToAttach')" style="max-width: 220px">
            <option :value="0" disabled>{{ $t('appDetail.selectVolume') }}</option>
            <option v-for="v in volumeOptions" :key="v.id" :value="v.id">{{ v.display_name || v.name }}</option>
            <option v-if="volumeSearch && volumeOptions.length === 0" :value="0" disabled>No volume matches “{{ volumeSearch }}”</option>
          </select>
          <input v-model="mount.path" class="form-input" :aria-label="$t('volumes.mountPath')" placeholder="/data" style="max-width: 200px" />
          <button class="btn btn-primary">{{ $t('appDetail.attach') }}</button>
        </form>
        <p v-if="hiddenVolumeCount > 0" class="form-hint" style="margin-top: 8px">
          {{ hiddenVolumeCount }} volume(s) on other nodes are hidden — an app can only mount volumes on its own node.
        </p>
      </div>
      <div v-if="volumeMounts.length === 0" class="empty-state">
        <span class="mdi mdi-harddisk" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('appDetail.noVolumesAttached') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('appDetail.volume') }}</th><th>{{ $t('volumes.mountPath') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="m in volumeMounts" :key="m.volume_id">
              <td class="cell-title">{{ m.docker_name }}</td>
              <td class="text-muted">{{ m.path }}</td>
              <td class="text-right"><button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('appDetail.detach')" :aria-label="$t('appDetail.detach')" @click="detachVolume(m.volume_id)"><span class="mdi mdi-delete-outline"></span></button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Config file mounts. A separate card from volumes because the two are
           different resources with different shapes: a volume is one path, a config
           is a set of files that can be projected whole or one at a time. -->
      <div class="card mt-4">
        <div class="card-header page-header">
          <h2>{{ $t('appDetail.configFiles') }}</h2>
          <span class="text-muted text-sm">{{ $t('appDetail.configs.subtitle') }}</span>
        </div>
        <div class="card-body">
          <div v-if="configMounts.length === 0" class="text-muted text-sm" style="margin-top: 0">{{ $t('appDetail.noConfigFilesMounted') }}</div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>{{ $t('appDetail.config') }}</th><th>{{ $t('appDetail.file') }}</th><th>{{ $t('volumes.mountPath') }}</th><th></th></tr></thead>
              <tbody>
                <tr v-for="m in configMounts" :key="`${m.config_id}:${m.config_key || ''}`">
                  <td><router-link to="/configs">{{ configName(m.config_id) }}</router-link></td>
                  <td>
                    <code v-if="m.config_key">{{ m.config_key }}</code>
                    <span v-else class="text-muted text-sm">{{ $t('appDetail.allFiles') }}</span>
                  </td>
                  <td><code>{{ m.path }}</code></td>
                  <td class="text-right">
                    <button
                      v-if="ws.canEdit"
                      class="btn-icon btn-icon-danger"
                      :title="$t('appDetail.removeMount')"
                      :aria-label="$t('appDetail.removeMount')"
                      @click="detachConfig(m.config_id!, m.config_key || '')"
                    >
                      <span class="mdi mdi-close"></span>
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <div v-if="ws.canEdit" class="config-attach">
            <div class="form-row">
              <div class="form-group">
                <label class="form-label" for="cfg-select">{{ $t('appDetail.config') }}</label>
                <select
                  id="cfg-select"
                  v-model="configMount.config_id"
                  class="form-select"
                  style="min-width: 300px"
                  @focus="loadWorkspaceConfigs"
                >
                  <option :value="null">{{ $t('appDetail.selectAConfig') }}</option>
                  <option v-for="c in workspaceConfigs" :key="c.id" :value="c.id">
                    {{ c.display_name || c.name }} ({{ c.keys.length }} file{{ c.keys.length === 1 ? '' : 's' }})
                  </option>
                </select>
              </div>
            </div>

            <template v-if="selectedConfig">
              <div class="form-group">
                <label class="form-label">{{ $t('appDetail.mount') }}</label>
                <div class="tabs" style="margin-bottom: 0">
                  <button type="button" class="tab" :class="{ active: configMount.whole }" @click="configMount.whole = true">{{ $t('appDetail.configs.allFilesTab') }}</button>
                  <button type="button" class="tab" :class="{ active: !configMount.whole }" @click="configMount.whole = false">{{ $t('appDetail.aSingleFile') }}</button>
                </div>
              </div>

              <div v-if="!configMount.whole" class="form-group">
                <label class="form-label" for="cfg-key">{{ $t('appDetail.file') }}</label>
                <select id="cfg-key" v-model="configMount.key" class="form-input">
                  <option value="">{{ $t('appDetail.selectAFile') }}</option>
                  <option v-for="k in selectedConfig.keys" :key="k" :value="k">{{ k }}</option>
                </select>
              </div>

              <div class="form-group">
                <label class="form-label" for="cfg-path">
                  {{ configMount.whole ? 'Directory in the container' : 'File path in the container' }}
                </label>
                <input
                  id="cfg-path"
                  v-model="configMount.path"
                  class="form-input mono"
                  :placeholder="configMount.whole ? '/etc/myapp' : '/etc/myapp/app.conf'"
                />
              </div>

              <!-- Path shape is the thing people get wrong, so show the result
                   rather than explaining the rule. -->
              <div v-if="configPathPreview.length" class="cfg-preview">
                <span class="form-label" style="margin-bottom: 4px; display: block">{{ $t('appDetail.filesInTheContainer') }}</span>
                <code v-for="p in configPathPreview" :key="p" class="cfg-preview-path">{{ p }}</code>
              </div>

              <p class="form-hint">{{ $t('appDetail.configs.mountHint') }}</p>

              <button class="btn btn-primary btn-sm" :disabled="!canAttachConfig || configAttaching" @click="attachConfig">
                {{ configAttaching ? 'Mounting…' : 'Mount config' }}
              </button>
            </template>
          </div>
        </div>
      </div>

      <!-- Privileged host mounts -->
      <template v-if="canHostMount">
        <div class="card-header" style="border-top: 1px solid var(--border-primary)">
          <h2><span class="mdi mdi-shield-alert-outline" style="color: var(--warning, #d97706)"></span>{{ $t('plans.hostMounts') }}</h2>
        </div>
        <div class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <p class="form-hint" style="margin-bottom: 10px">{{ $t('appDetail.settings.hostMountsHint') }}</p>
          <form class="flex items-center gap-2" @submit.prevent="attachHostMount">
            <select v-model="hostMount.preset" class="form-select" :aria-label="$t('appDetail.hostMountCapability')" style="max-width: 220px" @change="onPresetChange">
              <option value="" disabled>{{ $t('appDetail.selectCapability') }}</option>
              <option v-for="p in hostPresets" :key="p.key" :value="p.key">{{ p.label }}</option>
            </select>
            <input v-model="hostMount.path" class="form-input" :aria-label="$t('appDetail.hostMountPath')" :placeholder="selectedPreset?.default_target || '/path'" style="max-width: 220px" />
            <label v-if="selectedPreset?.allow_read_only" class="flex items-center gap-1 text-sm" style="white-space: nowrap">
              <input v-model="hostMount.read_only" type="checkbox" />{{ $t('appDetail.readOnly') }}</label>
            <button class="btn btn-primary" :disabled="!hostMount.preset">{{ $t('appDetail.attach') }}</button>
          </form>
          <p v-if="selectedPreset?.danger" class="form-hint" style="margin-top: 8px; color: var(--danger, #dc2626)">
            <span class="mdi mdi-alert"></span> {{ selectedPreset.danger }}
          </p>
        </div>
        <div v-if="hostMounts.length === 0" class="empty-state">
          <p>{{ $t('appDetail.noHostMountsAttached') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.capability') }}</th><th>{{ $t('volumes.mountPath') }}</th><th>{{ $t('appDetail.mode') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="m in hostMounts" :key="m.host_preset">
                <td class="cell-title">{{ presetLabel(m.host_preset) }}</td>
                <td class="text-muted">{{ m.path }}</td>
                <td class="text-muted">{{ m.read_only ? 'read-only' : 'read-write' }}</td>
                <td class="text-right"><button class="btn-icon btn-icon-danger" :title="$t('appDetail.detach')" :aria-label="$t('appDetail.detach')" @click="detachHostMount(m.host_preset)"><span class="mdi mdi-delete-outline"></span></button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>

    <!-- Databases -->
    <div v-else-if="tab === 'databases'" class="card">
      <div class="card-header">
        <h2>{{ $t('appDetail.databases') }}</h2>
        <div class="flex items-center gap-2">
          <button v-if="ws.canEdit" class="btn btn-primary btn-sm" @click="openLink"><span class="mdi mdi-link-variant"></span>{{ $t('appDetail.linkDatabase') }}</button>
          <RouterLink to="/databases" class="btn btn-ghost btn-sm">{{ $t('appDetail.manageDatabases') }}</RouterLink>
        </div>
      </div>
      <div v-if="appDatabases.length === 0" class="empty-state">
        <span class="mdi mdi-database-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('appDetail.db.emptyHint') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('appDetail.database') }}</th><th>{{ $t('appDetail.engine') }}</th><th>{{ $t('appDetail.user') }}</th><th>{{ $t('appDetail.tab.env') }}</th><th></th></tr></thead>
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
                <button class="btn btn-secondary btn-sm" @click="revealDatabase(d)"><span class="mdi mdi-key-outline"></span>{{ $t('appDetail.connection') }}</button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('appDetail.detach')" :aria-label="$t('appDetail.detach')" @click="detachDatabase(d)"><span class="mdi mdi-link-variant-off"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Releases -->
    <div v-else-if="tab === 'releases'" class="card">
      <div class="card-header"><h2>{{ $t('appDetail.releases') }}</h2></div>
      <div v-if="releases.length === 0" class="empty-state">
        <span class="mdi mdi-tag-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('appDetail.noReleasesYet') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('appDetail.version') }}</th><th>{{ $t('appDetail.image') }}</th><th>{{ $t('appDetail.state') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in releases" :key="r.id" class="row-clickable" @click="releaseDetail = r">
              <td class="cell-title">v{{ r.version }}</td>
              <td class="text-muted">{{ r.image }}</td>
              <td>
                <span v-if="r.active" class="badge badge-success badge-dot">{{ $t('appDetail.active') }}</span>
                <span v-if="r.pinned" class="badge badge-info" style="margin-left: 4px"><span class="mdi mdi-pin" style="font-size: 12px"></span>{{ $t('appDetail.pinned') }}</span>
              </td>
              <td class="text-right table-actions" @click.stop>
                <button v-if="!r.active && ws.canEdit" class="btn btn-sm btn-secondary" :title="$t('appDetail.redeployThisRelease')" @click="activate(r.id)">{{ $t('appDetail.activate') }}</button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t(r.pinned ? 'appDetail.unpin' : 'appDetail.pin')" :aria-label="r.pinned ? 'Unpin' : 'Pin'" :disabled="releaseBusy === r.id" @click="togglePin(r)">
                  <span class="mdi" :class="r.pinned ? 'mdi-pin-off-outline' : 'mdi-pin-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" :disabled="r.active || r.pinned || releaseBusy === r.id" @click="deleteRelease(r)">
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
      <!-- Source: edited separately from the rest of settings, because switching image <-> git
           clears the other source's fields, may drop a repo pipeline, and needs a redeploy. -->
      <div class="card mb-4">
        <div class="card-header source-header">
          <div>
            <h2>{{ $t('appDetail.sourceLabel') }}</h2>
            <p class="card-subtitle">{{ $t('appDetail.settings.sourceHint') }}</p>
          </div>
          <button
            v-if="ws.canEdit && !editingSource && !imageManaged"
            type="button"
            class="btn btn-ghost btn-sm"
            @click="beginEditSource"
          >
            <span class="mdi mdi-pencil-outline"></span>{{ $t('gitops.editSource') }}</button>
        </div>

        <!-- Summary -->
        <div v-if="!editingSource" class="card-body">
          <div class="source-summary">
            <span class="source-badge" :class="app.source_type">
              <span class="mdi" :class="app.source_type === 'git' ? 'mdi-git' : 'mdi-docker'"></span>
              {{ app.source_type === 'git' ? 'Git repository' : 'Docker image' }}
            </span>
            <code v-if="app.source_type === 'image'" class="source-ref">{{ app.image }}:{{ app.tag || 'latest' }}</code>
            <code v-else class="source-ref">{{ app.git_repo || '—' }}<span v-if="app.git_ref" class="text-muted"> @ {{ app.git_ref }}</span></code>
          </div>
          <p v-if="imageManaged && template" class="form-hint source-managed">
            <span class="mdi mdi-lock-outline"></span>
            Managed by the “{{ template.name }}” template — change it through a
            <button type="button" class="tpl-notice-link" @click="goToUpgrade">{{ $t('appDetail.marketplaceUpgrade') }}</button>.
          </p>
          <p v-else-if="managedBy === 'gitops'" class="form-hint source-managed">
            <span class="mdi mdi-lock-outline"></span>
            <i18n-t keypath="appDetail.settings.gitopsLocked" tag="span">
              <template #link><router-link :to="{ name: 'gitops' }">{{ $t('appDetail.deploy.sync') }}</router-link></template>
            </i18n-t>
          </p>
        </div>

        <!-- Editor -->
        <div v-else class="card-body" style="max-width: 560px">
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.sourceType') }}</label>
            <div class="source-choice">
              <label
                v-for="opt in SOURCE_TYPES"
                :key="opt.value"
                class="source-option"
                :class="{ active: sourceDraft.type === opt.value }"
              >
                <input type="radio" :value="opt.value" v-model="sourceDraft.type" />
                <span class="mdi" :class="opt.icon"></span>
                <span class="source-option-text">
                  <strong>{{ $t(opt.label) }}</strong>
                  <small>{{ $t(opt.hint) }}</small>
                </span>
              </label>
            </div>
          </div>

          <!-- What a switch costs. Shown only when actually switching, so it reads as consequence
               rather than boilerplate. -->
          <div v-if="sourceSwitching" class="tpl-notice source-warning">
            <span class="mdi mdi-alert-outline"></span>
            <div class="tpl-notice-text">
              <strong>{{ $t(sourceDraft.type === 'git' ? 'appDetail.settings.switchToGitTitle' : 'appDetail.settings.switchToImageTitle') }}</strong>
              {{ $t(sourceDraft.type === 'git' ? 'appDetail.settings.switchToGitBody' : 'appDetail.settings.switchToImageBody') }}
              <i18n-t v-if="app.source_type === 'git' && deploysViaPipeline" keypath="appDetail.settings.switchPipelineRemoved" tag="span">
                <template #path><code>{{ repoPipeline?.source_path }}</code></template>
              </i18n-t>
              {{ $t('appDetail.settings.switchKept') }}
            </div>
          </div>

          <template v-if="sourceDraft.type === 'image'">
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">{{ $t('appDetail.image') }}</label>
                <input v-model="settingsForm.image" class="form-input" placeholder="nginx" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.tag') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
                <input v-model="settingsForm.tag" class="form-input" placeholder="latest" />
              </div>
            </div>
            <i18n-t keypath="appDetail.settings.deploysImage" tag="p" class="form-hint" style="margin-top: -8px">
              <template #image><code>{{ settingsForm.image || 'image' }}:{{ settingsForm.tag || 'latest' }}</code></template>
            </i18n-t>
            <div class="form-group">
              <label class="form-label">{{ $t('appDetail.registryCredential') }}<span class="text-muted">{{ $t('apps.form.forPrivate') }}</span></label>
              <select v-model="settingsForm.registry_id" class="form-select">
                <option :value="null">{{ $t('apps.form.registryNone') }}</option>
                <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }} ({{ r.server }})</option>
              </select>
            </div>
          </template>

          <template v-else>
            <div class="form-group">
              <label class="form-label">{{ $t('appDetail.repository') }}</label>
              <select v-model="settingsForm.git_repository_id" class="form-select" @change="onSettingsRepoSelect">
                <option :value="null">{{ $t('apps.form.publicUrl') }}</option>
                <option v-for="r in gitRepos" :key="r.id" :value="r.id">{{ r.name }} — {{ r.url }}</option>
              </select>
              <p class="form-hint">
                {{ $t('appDetail.settings.savedRepoHint') }}
                <RouterLink to="/git-repositories">{{ $t('apps.form.manageRepos') }}</RouterLink>
              </p>
            </div>
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">
                  {{ $t('appDetail.settings.repositoryUrl') }}
                  <span v-if="settingsForm.git_repository_id" class="text-muted">{{ $t('apps.form.optionalOverrides') }}</span>
                </label>
                <input v-model="settingsForm.git_repo" class="form-input" placeholder="https://github.com/user/repo" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.branchRef') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
                <input v-model="settingsForm.git_ref" class="form-input" placeholder="main" />
              </div>
            </div>
            <div v-if="deploysViaPipeline && !sourceSwitching" class="tpl-notice">
              <span class="mdi mdi-pipe"></span>
              <div class="tpl-notice-text">
                <strong>{{ $t('appDetail.settings.pipelineBuildsTitle') }}</strong>
                <i18n-t keypath="appDetail.settings.pipelineBuildsHint" tag="span">
                  <template #path><code>{{ repoPipeline?.source_path }}</code></template>
                </i18n-t>
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.buildMethod') }}</label>
              <select v-model="settingsForm.build_method" class="form-select" :disabled="deploysViaPipeline && !sourceSwitching">
                <option value="auto">{{ $t('apps.form.buildAuto') }}</option>
                <option value="buildpack">{{ $t('apps.form.buildBuildpacks') }}</option>
                <option value="dockerfile">{{ $t('appDetail.dockerfile') }}</option>
              </select>
              <p class="form-hint">{{ $t('apps.form.buildAutoHint') }}</p>
            </div>
            <div v-if="settingsForm.build_method !== 'dockerfile'" class="form-group">
              <label class="form-label">{{ $t('appDetail.builderImage') }}<span class="text-muted">{{ $t('apps.form.optionalAdvanced') }}</span></label>
              <input v-model="settingsForm.builder" class="form-input" placeholder="paketobuildpacks/builder-jammy-base" :disabled="deploysViaPipeline && !sourceSwitching" />
              <p class="form-hint">{{ $t('apps.form.builderHint') }}</p>
            </div>
          </template>

          <div class="form-actions">
            <button class="btn btn-primary" :disabled="savingSource || !sourceValid" @click="saveSource">
              {{ savingSource ? 'Saving…' : sourceSwitching ? 'Switch source' : 'Save source' }}
            </button>
            <button class="btn btn-ghost" :disabled="savingSource" @click="cancelEditSource">{{ $t('action.cancel') }}</button>
            <span v-if="!sourceValid" class="form-error">
              {{ sourceDraft.type === 'image' ? 'An image is required.' : 'A repository URL or a saved repository is required.' }}
            </span>
          </div>
        </div>

        <!-- Pipeline re-sync: only meaningful for a git app, and the one place an operator can pull
             a pipelines.yaml that was added (or edited) after the app was created. -->
        <div v-if="app.source_type === 'git' && !editingSource" class="card-body source-pipeline">
          <div class="source-pipeline-row">
            <div>
              <strong class="source-pipeline-title">
                <span class="mdi mdi-pipe"></span>
                {{ deploysViaPipeline ? 'Repository pipeline' : 'No repository pipeline' }}
              </strong>
              <p class="form-hint" style="margin: 4px 0 0">
                <i18n-t v-if="deploysViaPipeline" keypath="appDetail.settings.repoPipelineHint" tag="span">
                  <template #path><code>{{ repoPipeline?.source_path }}</code></template>
                </i18n-t>
                <i18n-t v-else keypath="appDetail.settings.noRepoPipelineHint" tag="span">
                  <template #file><code>pipelines.yaml</code></template>
                </i18n-t>
              </p>
            </div>
            <button
              v-if="ws.canEdit"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="resyncingPipeline"
              @click="resyncPipeline"
            >
              <span class="mdi" :class="resyncingPipeline ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
              {{ resyncingPipeline ? 'Syncing…' : 'Re-sync' }}
            </button>
          </div>
        </div>

        <!-- The cache is the app's, shared by direct deploys and pipeline runs, so it is invalidated
             here rather than per pipeline. -->
        <div v-if="app.source_type === 'git' && !editingSource" class="card-body source-pipeline">
          <div class="source-pipeline-row">
            <div>
              <strong class="source-pipeline-title">
                <span class="mdi mdi-cached"></span>{{ $t('appDetail.buildCache') }}</strong>
              <p class="form-hint" style="margin: 4px 0 0">{{ $t('appDetail.settings.buildCacheHint') }}</p>
            </div>
            <button
              v-if="ws.canEdit"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="invalidatingCache"
              @click="invalidateBuildCache"
            >
              <span class="mdi" :class="invalidatingCache ? 'mdi-loading mdi-spin' : 'mdi-cached'"></span>
              {{ invalidatingCache ? 'Invalidating…' : 'Invalidate' }}
            </button>
          </div>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header">
          <h2>{{ $t('appDetail.configuration') }}</h2>
          <p class="card-subtitle">{{ $t('appDetail.settings.subtitle') }}</p>
        </div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.command') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
            <input v-model="settingsForm.command" class="form-input mono" :placeholder="$t('appDetail.imageDefault')" :disabled="!ws.canEdit" />
            <p class="form-hint">{{ $t('appDetail.settings.commandHint') }}</p>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('apps.form.containerPorts') }}</label>
            <div v-for="(p, i) in settingsForm.ports" :key="i" class="port-row">
              <input v-model.number="p.container_port" type="number" class="form-input" :aria-label="$t('apps.form.containerPort')" placeholder="8080" :disabled="!ws.canEdit" style="flex: 1" />
              <select v-model="p.protocol" class="form-select" :disabled="!ws.canEdit" :aria-label="$t('apps.form.portProtocol')" style="width: 84px">
                <option value="tcp">TCP</option>
                <option value="udp">UDP</option>
              </select>
              <select v-model="p.scheme" class="form-select" :disabled="!ws.canEdit" :title="$t('appDetail.ports.schemeHint')" :aria-label="$t('appDetail.ports.schemeHint')" style="width: 96px">
                <option value="http">http</option>
                <option value="https">https</option>
              </select>
              <input v-model="p.name" class="form-input" :aria-label="$t('apps.form.portName')" :placeholder="$t('apps.form.portNamePlaceholder')" :disabled="!ws.canEdit" style="flex: 1" />
              <button v-if="ws.canEdit" type="button" class="btn-icon btn-icon-danger" :aria-label="$t('apps.form.removePort')" @click="removeSettingsPort(i)"><span class="mdi mdi-close"></span></button>
            </div>
            <button v-if="ws.canEdit" type="button" class="btn btn-ghost btn-sm" @click="addSettingsPort"><span class="mdi mdi-plus"></span>{{ $t('appDetail.addPort') }}</button>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.networks') }}</label>
            <label v-for="n in networks" :key="n.id" class="checkbox-label">
              <input type="checkbox" :value="n.id" v-model="settingsForm.network_ids" :disabled="!ws.canEdit || n.is_default" />
              {{ n.name }} <span v-if="n.is_default" class="text-muted">{{ $t('appDetail.defaultAlwaysAttached') }}</span>
            </label>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.stack') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
            <select v-model="settingsForm.stack_id" class="form-select" :disabled="!ws.canEdit">
              <option :value="null">{{ $t('apps.form.none') }}</option>
              <option v-for="s in stacks" :key="s.id" :value="s.id">{{ s.name }}</option>
            </select>
            <p class="form-hint">{{ $t('appDetail.settings.stackHint') }}</p>
          </div>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save settings' }}
          </button>
          <p v-else class="text-muted text-sm">{{ $t('appDetail.settings.needsDeveloper') }}</p>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('appDetail.deploymentStrategy') }}</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.defaultStrategy') }}</label>
            <div class="strategy-options">
              <label
                v-for="s in STRATEGIES"
                :key="s.value"
                class="strategy-option"
                :class="{ active: settingsForm.deploy_strategy === s.value, unavailable: hasHostPorts && s.value === 'rolling' }"
              >
                <input
                  type="radio"
                  :value="s.value"
                  v-model="settingsForm.deploy_strategy"
                  :disabled="!ws.canEdit || (hasHostPorts && s.value === 'rolling')"
                />
                <span>
                  <span class="strategy-name">{{ $t(s.label) }}</span>
                  <span class="strategy-hint">
                    {{ hasHostPorts && s.value === 'rolling' ? $t('appDetail.strategy.rollingBlocked', { ports: hostPortList }) : $t(s.hint) }}
                  </span>
                </span>
              </label>
            </div>
            <p class="form-hint">Applied when you Deploy without choosing a strategy. Config-change redeploys always use rolling{{ hasHostPorts ? ', which falls back to recreate here' : '' }}.</p>
          </div>
          <template v-if="settingsForm.deploy_strategy === 'canary'">
            <div class="form-row">
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.initial') }}</label>
                <input v-model.number="settingsForm.canary_initial_weight" type="number" min="1" max="99" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.step') }}</label>
                <input v-model.number="settingsForm.canary_step_weight" type="number" min="1" max="99" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.intervalS') }}</label>
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
        <div class="card-header"><h2>{{ $t('appDetail.resources') }}</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-row">
            <div class="form-group" style="flex: 1">
              <label class="form-label">{{ $t('appDetail.cpuLimitCores') }}</label>
              <input v-model.number="settingsForm.cpu_cores" type="number" min="0" step="0.1" class="form-input" :class="{ 'input-error': cpuOverCap }" :disabled="!ws.canEdit" />
              <p class="form-hint" :class="{ 'hint-error': cpuOverCap }">
                <span v-if="cpuOverCap">Exceeds platform max of {{ limits.max_cpu_cores }} cores.</span>
                <span v-else-if="limits.max_cpu_cores > 0">Max {{ limits.max_cpu_cores }} cores. 0 = unlimited.</span>
                <span v-else>{{ $t('appDetail.zeroUnlimited') }}</span>
              </p>
            </div>
            <div class="form-group" style="flex: 1">
              <label class="form-label">{{ $t('appDetail.memoryLimitMb') }}</label>
              <input v-model.number="settingsForm.memory_mb" type="number" min="0" step="64" class="form-input" :class="{ 'input-error': memOverCap }" :disabled="!ws.canEdit" />
              <p class="form-hint" :class="{ 'hint-error': memOverCap }">
                <span v-if="memOverCap">Exceeds platform max of {{ limits.max_memory_mb }} MB.</span>
                <span v-else-if="limits.max_memory_mb > 0">Max {{ limits.max_memory_mb }} MB. 0 = unlimited.</span>
                <span v-else>{{ $t('appDetail.zeroUnlimited') }}</span>
              </p>
            </div>
          </div>
          <div v-if="gpuAllowed" class="form-row">
            <div class="form-group" style="flex: 1">
              <label class="form-label">{{ $t('appDetail.gpus') }}</label>
              <input v-model.number="settingsForm.gpu_count" type="number" min="0" step="1" class="form-input" :disabled="!ws.canEdit" />
              <p class="form-hint">{{ $t('appDetail.settings.gpuHint') }}</p>
            </div>
            <div class="form-group" style="flex: 1">
              <label class="form-label">{{ $t('appDetail.gpuKind') }}</label>
              <input v-model="settingsForm.gpu_kind" type="text" :placeholder="$t('appDetail.any')" class="form-input" :disabled="!ws.canEdit || settingsForm.gpu_count < 1" />
              <i18n-t keypath="appDetail.settings.gpuFilterHint" tag="p" class="form-hint">
            <template #vendor><code>nvidia</code></template>
          </i18n-t>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('jobs.runAsUser') }}</label>
            <input
              v-model="settingsForm.run_as_user" type="text" class="form-input" style="font-family: monospace"
              :placeholder="requireNonRoot ? '1000:1000' : $t('appDetail.runAsUserPlaceholder')"
              :disabled="!ws.canEdit" :aria-label="$t('jobs.runAsUser')"
            />
            <p v-if="runAsUserError" class="form-hint" style="color: var(--danger)">{{ runAsUserError }}</p>
            <p v-else class="form-hint">
              <i18n-t keypath="appDetail.settings.runAsUserHint" tag="span">
                <template #cmd><code>docker run --user</code></template>
                <template #a><code>1000</code></template>
                <template #b><code>1000:1000</code></template>
                <template #name><code>node</code></template>
              </i18n-t>
              <span v-if="requireNonRoot"><br />{{ $t('appDetail.settings.runAsUserRestricted') }}</span>
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.hardening') }}</label>
            <label class="checkbox-label">
              <input v-model="settingsForm.read_only_root_filesystem" type="checkbox" :disabled="!ws.canEdit" />{{ $t('appDetail.readOnlyRootFilesystem') }}</label>
            <i18n-t keypath="appDetail.settings.readOnlyRootHint" tag="p" class="form-hint">
              <template #path><code>/tmp</code></template>
            </i18n-t>
            <label class="checkbox-label">
              <input
                type="checkbox" :checked="settingsForm.no_new_privileges || requireNonRoot" :disabled="!ws.canEdit || requireNonRoot"
                @change="setNoNewPrivileges"
              />{{ $t('appDetail.noNewPrivileges') }}</label>
            <p class="form-hint">
              <i18n-t keypath="appDetail.settings.noNewPrivilegesHint" tag="span">
                <template #flag><code>--security-opt no-new-privileges</code></template>
              </i18n-t>
              <span v-if="requireNonRoot"> {{ $t('appDetail.settings.alwaysOnRestricted') }}</span>
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.dropCapabilities') }}</label>
            <input
              v-model="settingsForm.drop_capabilities" type="text" class="form-input mono" placeholder="NET_RAW, SYS_CHROOT or ALL"
              :disabled="!ws.canEdit" :aria-label="$t('appDetail.capabilitiesToDrop')"
            />
            <p v-if="dropCapabilitiesError" class="form-hint" style="color: var(--danger)">{{ dropCapabilitiesError }}</p>
            <i18n-t v-else keypath="appDetail.settings.dropCapsHint" tag="p" class="form-hint">
              <template #cmd><code>docker run --cap-drop</code></template>
              <template #all><code>ALL</code></template>
            </i18n-t>
          </div>

          <div v-if="canGrant && offeredCapabilities.length" class="form-group">
            <label class="form-label">{{ $t('appDetail.kernelCapabilities') }}</label>
            <div class="cap-grid">
              <label v-for="c in offeredCapabilities" :key="c.name" class="cap-option"
                :class="{ 'cap-elevated': c.tier === 1 }">
                <input type="checkbox" :checked="settingsForm.add_capabilities.includes(c.name)"
                  :disabled="!ws.canEdit" @change="toggleCapability(c.name)" />
                <span class="cap-body">
                  <span class="cap-name">
                    {{ c.name }}
                    <span v-if="c.tier === 1" class="badge badge-warning cap-badge">{{ $t('appDetail.elevated') }}</span>
                  </span>
                  <span class="cap-help">{{ c.help }}</span>
                </span>
              </label>
            </div>
            <i18n-t keypath="appDetail.settings.addCapsHint" tag="p" class="form-hint">
              <template #cmd><code>docker run --cap-add</code></template>
            </i18n-t>
          </div>

          <div v-if="canGrant && offeredDevices.length" class="form-group">
            <label class="form-label">{{ $t('appDetail.hostDevices') }}</label>
            <div v-for="(_, i) in settingsForm.devices" :key="i" class="device-row">
              <input v-model="settingsForm.devices[i]" type="text" class="form-input mono"
                placeholder="/dev/net/tun" :disabled="!ws.canEdit" :aria-label="$t('appDetail.hostDevicePath')" />
              <button class="btn-icon btn-icon-danger" :title="$t('action.remove')" :aria-label="$t('appDetail.removeDevice')"
                :disabled="!ws.canEdit" @click="settingsForm.devices.splice(i, 1)">
                <span class="mdi mdi-close"></span>
              </button>
            </div>
            <button class="btn btn-sm btn-secondary" :disabled="!ws.canEdit || settingsForm.devices.length >= (capCatalog?.max_devices ?? 8)"
              @click="addDevice">
              <span class="mdi mdi-plus"></span>{{ $t('appDetail.addDevice') }}</button>
            <p class="form-hint">
              {{ $t('appDetail.settings.devicesAllowed') }}
              <template v-for="(d, i) in offeredDevices" :key="d.path">
                <code>{{ d.path }}{{ d.exact ? '' : '*' }}</code><span v-if="i < offeredDevices.length - 1">, </span>
              </template>
              {{ $t('appDetail.settings.devicesNoBlock') }}
            </p>
          </div>

          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.restartPolicy') }}</label>
            <select v-model="settingsForm.restart_policy" class="form-select" :disabled="!ws.canEdit">
              <option v-for="p in RESTART_POLICIES" :key="p.value" :value="p.value">{{ $t(p.label) }}</option>
            </select>
            <p class="form-hint">{{ $t('appDetail.settings.restartPolicyHint') }}</p>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.imagePullPolicy') }}</label>
            <select v-model="settingsForm.image_pull_policy" class="form-select" :disabled="!ws.canEdit">
              <option v-for="p in IMAGE_PULL_POLICIES" :key="p.value" :value="p.value">{{ $t(p.label) }}</option>
            </select>
            <i18n-t keypath="appDetail.settings.pullPolicyHint" tag="p" class="form-hint">
              <template #always><strong>{{ $t('appDetail.settings.pullAlways') }}</strong></template>
              <template #ifNotPresent><strong>{{ $t('appDetail.settings.pullIfNotPresent') }}</strong></template>
              <template #never><strong>{{ $t('appDetail.settings.pullNever') }}</strong></template>
            </i18n-t>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.ifThisAppDisappears') }}</label>
            <select v-model="settingsForm.reconcile_policy" class="form-select" :disabled="!ws.canEdit">
              <option v-for="p in RECONCILE_POLICIES" :key="p.value" :value="p.value">{{ $t(p.label) }}</option>
            </select>
            <i18n-t keypath="appDetail.settings.reconcileHint" tag="p" class="form-hint">
              <template #default><strong>{{ $t('appDetail.settings.reconcileDefault') }}</strong></template>
              <template #ignore><strong>{{ $t('appDetail.settings.reconcileIgnore') }}</strong></template>
              <template #report><strong>{{ $t('appDetail.settings.reconcileReport') }}</strong></template>
              <template #redeploy><strong>{{ $t('appDetail.settings.reconcileRedeploy') }}</strong></template>
            </i18n-t>
          </div>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings || !resourcesValid || !!runAsUserError || !!dropCapabilitiesError" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save resources' }}
          </button>
        </div>
      </div>

      <!-- Container labels (Traefik &c.) -->
      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('appDetail.containerLabels') }}</h2></div>
        <div class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <i18n-t keypath="appDetail.labels.hint" tag="p" class="text-muted text-sm" style="margin: 0 0 8px">
            <template #tool><strong>Traefik</strong></template>
            <template #a><code>io.miabi.*</code></template>
            <template #b><code>com.docker.*</code></template>
            <template #cmd><code>docker inspect</code></template>
          </i18n-t>
          <p v-if="!customLabelsAllowed" class="text-muted text-sm" style="margin: 8px 0 0">
            <span class="mdi mdi-lock-outline"></span>{{ $t('appDetail.labels.notAvailable') }}</p>
          <form v-else-if="ws.canEdit" class="flex items-center gap-2" @submit.prevent="setLabel">
            <input v-model="newLabel.key" class="form-input" :aria-label="$t('appDetail.labelKey')" placeholder="traefik.enable" style="max-width: 280px" />
            <input v-model="newLabel.value" class="form-input" :aria-label="$t('appDetail.labelValue')" placeholder="true" style="max-width: 240px" />
            <button class="btn btn-primary">{{ $t('appDetail.add') }}</button>
          </form>
        </div>
        <div v-if="Object.keys(containerLabels).length === 0" class="empty-state">
          <span class="mdi mdi-label-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('appDetail.noCustomLabels') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('appDetail.key') }}</th><th>{{ $t('appDetail.value') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="(value, key) in containerLabels" :key="key">
                <td class="cell-title">{{ key }}</td>
                <td class="text-muted">{{ value }}</td>
                <td class="text-right">
                  <button v-if="ws.canEdit && customLabelsAllowed" class="btn-icon btn-icon-danger" :title="$t('action.remove')" :aria-label="$t('action.remove')" @click="delLabel(String(key))"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('appDetail.healthcheck') }}</h2></div>
        <div class="card-body" style="max-width: 460px">
          <div class="form-group">
            <label class="form-label">{{ $t('appDetail.type') }}</label>
            <select v-model="settingsForm.hc_type" class="form-select" :disabled="!ws.canEdit">
              <option v-for="item in HEALTHCHECK_TYPES" :key="item.value" :value="item.value">{{ $t(item.label) }}</option>
            </select>
            <p class="form-hint">{{ $t('appDetail.settings.waitHealthyHint') }}</p>
          </div>
          <template v-if="settingsForm.hc_type === 'http'">
            <div class="form-row">
              <div class="form-group" style="flex: 2">
                <label class="form-label">{{ $t('appDetail.path') }}</label>
                <input v-model="settingsForm.hc_path" class="form-input" placeholder="/health" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.port') }}<span class="text-muted">{{ $t('appDetail.opt') }}</span></label>
                <input v-model.number="settingsForm.hc_port" type="number" class="form-input" :placeholder="String(app.port || 80)" :disabled="!ws.canEdit" />
              </div>
            </div>
            <i18n-t keypath="appDetail.settings.httpProbeHint" tag="p" class="form-hint" style="margin-top: -8px; margin-bottom: 16px">
              <template #curl><code>curl</code></template>
              <template #wget><code>wget</code></template>
            </i18n-t>
          </template>
          <div v-else-if="settingsForm.hc_type === 'command'" class="form-group">
            <label class="form-label">{{ $t('appDetail.command') }}</label>
            <input v-model="settingsForm.hc_command" class="form-input mono" placeholder="pg_isready -U postgres" :disabled="!ws.canEdit" />
            <p class="form-hint">{{ $t('appDetail.settings.cmdProbeHint') }}</p>
          </div>
          <template v-if="settingsForm.hc_type !== 'none'">
            <div class="form-row">
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.intervalS') }}</label>
                <input v-model.number="settingsForm.hc_interval" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('jobs.timeoutShort') }}</label>
                <input v-model.number="settingsForm.hc_timeout" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.retries') }}</label>
                <input v-model.number="settingsForm.hc_retries" type="number" min="1" class="form-input" :disabled="!ws.canEdit" />
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.startPeriodS') }}</label>
                <input v-model.number="settingsForm.hc_start_period" type="number" min="0" class="form-input" :disabled="!ws.canEdit" />
              </div>
            </div>
          </template>
          <button v-if="ws.canEdit" class="btn btn-primary" :disabled="savingSettings" @click="saveSettings">
            {{ savingSettings ? 'Saving…' : 'Save healthcheck' }}
          </button>
        </div>
      </div>

      <!-- Moving this app into Git: render it as the manifest that would recreate it. -->
      <div v-if="canExportManifest" class="card mb-4">
        <div class="card-header">
          <h2>{{ $t('appDetail.gitopsManifest') }}</h2>
          <i18n-t keypath="appDetail.manifest.subtitle" tag="p" class="card-subtitle">
            <template #kind><code>miabi.io/v1</code></template>
          </i18n-t>
        </div>
        <div class="card-body">
          <i18n-t keypath="appDetail.manifest.secretsHint" tag="p" class="text-muted text-sm mb-3">
            <template #not><strong>{{ $t('appDetail.manifest.not') }}</strong></template>
            <template #field><code>secretEnv</code></template>
          </i18n-t>
          <button type="button" class="btn btn-secondary" :disabled="manifestLoading" @click="openManifest">
            <span class="mdi" :class="manifestLoading ? 'mdi-loading mdi-spin' : 'mdi-file-code-outline'"></span>{{ $t('appDetail.generateManifest') }}</button>
        </div>
      </div>

      <div class="card">
      <div class="card-header"><h2 style="color: var(--danger-600)">{{ $t('db.dangerZone') }}</h2></div>
      <div class="card-body flex items-center justify-between">
        <div>
          <div style="font-weight: 600; color: var(--text-primary)">{{ $t('appDetail.deleteThisApplication') }}</div>
          <div class="text-muted text-sm">
            {{ $t('appDetail.danger.deleteHint') }}
            <span v-if="liveStatus?.running"> {{ $t('appDetail.danger.stopFirst') }}</span>
          </div>
        </div>
        <button v-if="ws.canEdit" class="btn btn-danger" :disabled="liveStatus?.running" :title="$t(liveStatus?.running ? 'appDetail.danger.stopBeforeDelete' : 'appDetail.deleteApplication')" @click="openDelete">{{ $t('appDetail.deleteApplication') }}</button>
        <span v-else class="text-muted text-sm">{{ $t('appDetail.needsDeveloper') }}</span>
      </div>
      </div>
    </div>

    <!-- Delete application -->
    <Teleport to="body">
      <NetworkDetailModal
        v-if="netDetailFor"
        :workspace-id="wid"
        :network="netDetailFor"
        @close="netDetailFor = null"
      />
    </Teleport>

    <Teleport to="body">
      <RouteFormModal
        :open="showRouteModal"
        :workspace-id="wid"
        :editing="editingRoute"
        :apps="app ? [app] : []"
        :preset-app-id="appId"
        lock-app
        @close="showRouteModal = false"
        @saved="onRouteSaved"
      />

      <EnvVarModal
        :open="showEnvModal"
        :editing-key="editingEnvKey"
        :initial="envForm"
        :saving="savingEnv"
        :apply-note="isDeployed ? 'Applies on the next deploy.' : ''"
        @close="showEnvModal = false"
        @save="saveEnv"
      />

      <AppModal v-if="showDelete && app" @close="showDelete = false">
        <div class="modal-header">
          <h3>{{ $t('appDetail.deleteApplication') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showDelete = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="removeApp">
          <div class="modal-body">
            <i18n-t keypath="appDetail.danger.deleteWarning" tag="p"><template #name><strong>{{ app.name }}</strong></template></i18n-t>
            <div class="form-group" style="margin-bottom: 0; margin-top: 12px">
              <i18n-t keypath="appDetail.danger.typeToConfirm" tag="label" class="form-label"><template #name><code>{{ app.name }}</code></template></i18n-t>
              <input v-model="deleteConfirm" class="form-input" :placeholder="app.name" autofocus autocomplete="off" />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showDelete = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-danger" :disabled="deleteConfirm !== app.name || deleting">{{ deleting ? 'Deleting…' : 'Delete application' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Link a database -->
    <Teleport to="body">
      <AppModal v-if="linkModal" max-width="600px" @close="linkModal = false">
        <div class="modal-header">
          <h3>{{ $t('appDetail.linkADatabase') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="linkModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <!-- Step 1: instance -->
          <label class="form-label">{{ $t('appDetail.databaseInstance') }}</label>
          <div v-if="instancesOnNode.length === 0" class="form-hint" style="margin-bottom: 10px">{{ $t('appDetail.db.noInstances') }}</div>
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
              <button type="button" class="seg-btn" :class="{ active: linkMode === 'existing' }" @click="linkMode = 'existing'">{{ $t('appDetail.existingDatabase') }}</button>
              <button type="button" class="seg-btn" :class="{ active: linkMode === 'new' }" @click="linkMode = 'new'">{{ $t('appDetail.newDatabase') }}</button>
            </div>

            <div v-if="linkMode === 'existing'" style="margin-top: 12px">
              <div v-if="freeDatabases.length === 0" class="form-hint">{{ $t('appDetail.db.noUnattached') }}</div>
              <select v-else v-model.number="linkForm.database_id" class="form-select" :aria-label="$t('appDetail.databaseToLink')" style="width: 100%">
                <option :value="0" disabled>{{ $t('appDetail.selectDatabase') }}</option>
                <option v-for="d in freeDatabases" :key="d.id" :value="d.id">{{ d.name }}</option>
              </select>
            </div>

            <div v-else style="margin-top: 12px">
              <label class="form-label">{{ $t('appDetail.newDatabaseName') }}</label>
              <input v-model="linkForm.new_name" class="form-input" placeholder="myapp" style="width: 100%" />
            </div>

            <label class="form-label" style="margin-top: 14px">{{ $t('appDetail.envVarPrefix') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
            <input v-model="linkForm.env_prefix" class="form-input" :placeholder="$t('appDetail.db.prefixPlaceholder')" style="width: 100%" />
            <p v-if="!linkForm.env_prefix.trim() && hasUnprefixed" class="form-hint" style="color: var(--warning, #d97706); margin-top: 6px">
              <span class="mdi mdi-alert-outline"></span>{{ $t('appDetail.db.prefixWarning') }}</p>
          </template>
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" @click="linkModal = false">{{ $t('action.cancel') }}</button>
          <button class="btn btn-primary" :disabled="!selInstance || linkBusy" @click="confirmLink">
            {{ linkBusy ? 'Linking…' : 'Attach' }}
          </button>
        </div>
      </AppModal>
    </Teleport>

    <!-- Database connection -->
    <Teleport to="body">
      <AppModal v-if="dbConnModal" max-width="560px" @close="dbConnModal = null">
        <div class="modal-header">
          <h3>{{ $t('appDetail.connection') }} · {{ dbConnModal.title }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="dbConnModal = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-for="f in [
            { label: 'db.host', value: `${dbConnModal.info.host}:${dbConnModal.info.port}` },
            { label: 'appDetail.database', value: dbConnModal.info.database },
            { label: 'db.username', value: dbConnModal.info.username },
            { label: 'db.password', value: dbConnModal.info.password },
            { label: 'appDetail.uri', value: dbConnModal.info.uri },
          ]" :key="f.label" class="dns-field">
            <span class="dns-field-label">{{ $t(f.label) }}</span>
            <div class="dns-field-row">
              <span class="dns-field-value">{{ f.value || '—' }}</span>
              <button v-if="f.value" class="btn-icon btn-icon-muted" :title="$t('appDetail.copy')" :aria-label="$t('appDetail.copy')" @click="copy(f.value)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </div>
        </div>
      </AppModal>
    </Teleport>

    <!-- Add / update env var -->
    <Teleport to="body">
    </Teleport>

    <!-- Import .env -->
    <Teleport to="body">
      <AppModal v-if="showEnvImport" max-width="560px" @close="showEnvImport = false">
        <div class="modal-header">
          <h3>{{ $t('stacks.importEnv') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showEnvImport = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="importEnv">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('stacks.pasteKeyValueLines') }}</label>
              <textarea v-model="envImport.content" class="form-input" rows="10" spellcheck="false" style="font-family: monospace; font-size: 12px" placeholder="DATABASE_URL=postgres://...&#10;# comments and blank lines are ignored&#10;LOG_LEVEL=info" required></textarea>
            </div>
            <label class="checkbox-label" style="margin-bottom: 0"><input type="checkbox" v-model="envImport.secret" />{{ $t('stacks.markAllSecrets') }}</label>
            <p class="form-hint">Existing keys are overwritten. {{ isDeployed ? 'The app redeploys after import.' : '' }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEnvImport = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="importingEnv">{{ importingEnv ? 'Importing…' : 'Import' }}</button>
          </div>
        </form>
      </AppModal>
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
      <AppModal v-if="showAddPort" @close="showAddPort = false">
        <div class="modal-header">
          <h3>{{ $t('appDetail.addContainerPort') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showAddPort = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="addContainerPort">
          <div class="modal-body">
            <div class="flex items-center gap-3">
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('apps.form.containerPort') }}</label>
                <input v-model.number="portForm.container_port" type="number" min="1" max="65535" class="form-input" placeholder="8080" required autofocus />
              </div>
              <div class="form-group" style="width: 110px">
                <label class="form-label">{{ $t('appDetail.protocol') }}</label>
                <select v-model="portForm.protocol" class="form-select"><option value="tcp">tcp</option><option value="udp">udp</option></select>
              </div>
              <div class="form-group" style="width: 120px">
                <label class="form-label">{{ $t('appDetail.scheme') }}</label>
                <select v-model="portForm.scheme" class="form-select"><option value="http">http</option><option value="https">https</option></select>
              </div>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('apps.form.name') }}<span class="text-muted">{{ $t('appDetail.optional') }}</span></label>
              <input v-model="portForm.name" class="form-input" placeholder="http" />
            </div>
            <p class="form-hint">{{ $t('appDetail.ports.declareHint') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showAddPort = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="savingSettings || portForm.container_port <= 0">{{ savingSettings ? 'Adding…' : 'Add port' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Request host binding -->
    <Teleport to="body">
      <AppModal v-if="showBindReq" @close="showBindReq = false">
        <div class="modal-header">
          <h3>{{ $t('appDetail.requestHostBinding') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showBindReq = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="requestBind">
          <div class="modal-body">
            <p class="text-muted text-sm" style="margin-bottom: 14px">{{ $t('appDetail.ports.bindingHint') }}</p>
            <div class="form-row">
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('apps.form.containerPort') }}</label>
                <select v-model.number="bindForm.container_port" class="form-select">
                  <option v-for="p in (app.ports || [])" :key="p.id" :value="p.container_port">{{ p.container_port }}/{{ p.protocol }}</option>
                </select>
              </div>
              <div class="form-group" style="flex: 1">
                <label class="form-label">{{ $t('appDetail.hostPort') }}</label>
                <div class="flex items-center gap-2">
                  <input v-model.number="bindForm.host_port" type="number" class="form-input" placeholder="30080" required />
                  <button type="button" class="btn btn-secondary" :disabled="suggestingPort" :title="$t('appDetail.pickAFreeHostPort')" @click="suggestPort">{{ suggestingPort ? '…' : 'Suggest' }}</button>
                </div>
              </div>
            </div>
            <p class="form-hint">{{ $t('appDetail.ports.suggestHint') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showBindReq = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="requestingBind || !bindForm.host_port">{{ requestingBind ? 'Requesting…' : 'Request' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Deploy dialog -->
    <Teleport to="body">
      <AppModal v-if="showDeploy" @close="showDeploy = false">
        <div class="modal-header">
          <h3>{{ deployVerb }} {{ app.name }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showDeploy = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="confirmDeploy">
          <div class="modal-body">
            <div v-if="template" class="tpl-notice">
              <span class="mdi mdi-store-outline"></span>
              <div class="tpl-notice-text">
                <strong>Managed by the “{{ template.name }}” template.</strong>{{ $t('appDetail.settings.templateImageWarning') }}<button type="button" class="tpl-notice-link" @click="goToUpgrade">{{ $t('appDetail.upgradeViaMarketplace') }}</button>
              </div>
            </div>
            <div v-else-if="managedBy === 'gitops'" class="tpl-notice">
              <span class="mdi mdi-source-branch"></span>
              <div class="tpl-notice-text">
                <strong>{{ $t('appDetail.deploy.gitopsTitle') }}</strong>
                <i18n-t keypath="appDetail.deploy.gitopsHint" tag="span">
                  <template #link><router-link :to="{ name: 'gitops' }">{{ $t('appDetail.deploy.sync') }}</router-link></template>
                </i18n-t>
              </div>
            </div>
            <template v-if="app.source_type === 'image'">
              <div class="form-group" style="margin-bottom: 8px">
                <label class="form-label">{{ $t('appDetail.imageTag') }}</label>
                <input v-model="deployTag" class="form-input" placeholder="latest" autofocus />
              </div>
              <i18n-t keypath="appDetail.deploy.deploysImage" tag="p" class="form-hint" style="margin-bottom: 16px">
                <template #image><code>{{ app.image }}:{{ deployTag.trim() || 'latest' }}</code></template>
              </i18n-t>
            </template>
            <div v-else-if="deploysViaPipeline" class="tpl-notice" style="margin-bottom: 16px">
              <span class="mdi mdi-pipe"></span>
              <div class="tpl-notice-text">
                <strong>{{ $t('appDetail.deploy.pipelineTitle', { name: repoPipeline?.display_name || repoPipeline?.name }) }}</strong>
                <i18n-t keypath="appDetail.deploy.pipelineHint" tag="span">
                  <template #path><code>{{ repoPipeline?.source_path }}</code></template>
                  <template #ref><code>{{ app.git_ref || $t('appDetail.deploy.defaultBranch') }}</code></template>
                </i18n-t>
              </div>
            </div>
            <p v-else class="text-muted text-sm" style="margin-bottom: 16px">{{ $t('appDetail.deploy.buildsLatestCommit') }}</p>
            <div class="form-group" style="margin-bottom: 8px">
              <label class="form-label">{{ $t('appDetail.deploymentStrategy') }}</label>
              <select v-model="deployStrategy" class="form-select">
                <option v-for="item in availableStrategies" :key="item.value" :value="item.value">{{ $t(item.label) }}</option>
              </select>
            </div>
            <p class="form-hint">
              {{ $t(strategyHint(deployStrategy)) }}
              <span v-if="!isDeployed && deployStrategy === 'canary'"><br />{{ $t('appDetail.deploy.firstDeployNoCanary') }}</span>
              <span v-if="hasHostPorts"><br />Rolling isn't available: host port {{ hostPortList }} can only be held by one container at a time.</span>
              <span v-if="hasHostPorts && deployStrategy === 'canary'"><br />{{ $t('appDetail.ports.canaryHint') }}</span>
            </p>
            <div v-if="app.source_type === 'git'" class="form-group" style="margin-top: 16px; margin-bottom: 0">
              <label class="checkbox-label" style="margin-bottom: 0">
                <input v-model="deployNoCache" type="checkbox" />{{ $t('appDetail.rebuildWithoutCache') }}</label>
              <p class="form-hint">{{ $t('appDetail.deploy.noCacheHint') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showDeploy = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="deploying">
              <span class="mdi mdi-rocket-launch-outline"></span> {{ deploying ? 'Starting…' : deployVerb }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Release detail -->
    <Teleport to="body">
      <AppModal v-if="releaseDetail" @close="releaseDetail = null">
        <div class="modal-header">
          <h3>Release v{{ releaseDetail.version }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="releaseDetail = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div class="dns-field">
            <span class="dns-field-label">{{ $t('appDetail.image') }}</span>
            <span class="dns-field-value">{{ releaseDetail.image }}</span>
          </div>
          <div class="dns-field">
            <span class="dns-field-label">{{ $t('appDetail.container') }}</span>
            <span class="dns-field-value">{{ releaseDetail.container_id || '—' }}</span>
          </div>
          <div class="rel-meta">
            <span v-if="releaseDetail.active" class="badge badge-success badge-dot">{{ $t('appDetail.active') }}</span>
            <span v-else class="badge badge-neutral">{{ $t('appDetail.inactive') }}</span>
            <span v-if="releaseDetail.pinned" class="badge badge-info"><span class="mdi mdi-pin" style="font-size: 12px"></span>{{ $t('appDetail.pinned') }}</span>
            <span class="text-muted text-sm">Created {{ new Date(releaseDetail.created_at).toLocaleString() }}</span>
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="!releaseDetail.active && ws.canEdit" class="btn btn-secondary" @click="activate(releaseDetail.id); releaseDetail = null">{{ $t('appDetail.activate') }}</button>
          <button v-if="ws.canEdit" class="btn btn-secondary" @click="togglePin(releaseDetail)">{{ releaseDetail.pinned ? 'Unpin' : 'Pin' }}</button>
          <button v-if="ws.canEdit" class="btn btn-danger" :disabled="releaseDetail.active || releaseDetail.pinned" @click="deleteRelease(releaseDetail)">{{ $t('action.delete') }}</button>
        </div>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="manifestOpen" dialog-class="modal-xl" @close="manifestOpen = false">
        <div class="modal-header">
          <h3><span class="mdi mdi-file-code-outline"></span> Manifest — {{ app.name }}</h3>
          <div class="manifest-actions">
            <button type="button" class="btn btn-ghost btn-sm" @click="copyManifest">
              <span class="mdi" :class="manifestCopied ? 'mdi-check' : 'mdi-content-copy'"></span>
              {{ manifestCopied ? 'Copied' : 'Copy' }}
            </button>
            <button type="button" class="btn btn-ghost btn-sm" @click="downloadManifest">
              <span class="mdi mdi-download"></span>{{ $t('appDetail.download') }}</button>
            <button class="btn-icon btn-icon-muted" :title="$t('shell.close')" :aria-label="$t('shell.close')" @click="manifestOpen = false">
              <span class="mdi mdi-close"></span>
            </button>
          </div>
        </div>
        <div class="modal-body manifest-body">
          <div v-if="manifestLoading" class="manifest-loading"><span class="spinner"></span></div>
          <pre v-else class="manifest-yaml"><code>{{ manifestYaml }}</code></pre>
        </div>
        <div class="modal-footer manifest-footer">
          <i18n-t keypath="appDetail.manifest.commitHint" tag="span" class="text-muted text-sm">
            <template #file><code>{{ app.name }}.yaml</code></template>
            <template #link><a href="/gitops">{{ $t('appDetail.manifest.gitopsSource') }}</a></template>
            <template #cmd><code>miabi apply -f {{ app.name }}.yaml</code></template>
          </i18n-t>
        </div>
      </AppModal>

      <ShellTerminal v-if="shellOpen && app" :base="base" :app-name="app.name" @close="shellOpen = false" />
      <ContainerProcesses v-if="processesOpen && app && wid" :ws="wid" :app-id="appId" :app-name="app.name" @close="processesOpen = false" />
    </Teleport>
  </div>
  <div v-else class="loading-page"><span class="spinner"></span></div>
</template>

<style scoped>
/* ─── GitOps manifest export ─── */
.manifest-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}
.manifest-body {
  padding: 0;
  max-height: 60vh;
  overflow: auto;
}
.manifest-yaml {
  margin: 0;
  padding: 16px;
  font-size: 12.5px;
  line-height: 1.55;
  background: var(--bg-secondary);
  color: var(--text-primary);
  white-space: pre;
}
.manifest-loading {
  display: flex;
  justify-content: center;
  padding: 48px 0;
}
.manifest-footer {
  justify-content: flex-start;
}

/* ─── Environment tab ─── */
.card-subtitle { margin: 4px 0 0; font-size: 13px; color: var(--text-muted); max-width: 640px; }
.card-subtitle code { font-size: 12px; }
.env-toolbar {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 12px 24px; border-bottom: 1px solid var(--border-primary); flex-wrap: wrap;
}
.env-stats { display: flex; align-items: center; gap: 6px; }
.env-stats strong { color: var(--text-secondary); }
.env-stats-dot { opacity: 0.5; }
.env-search { position: relative; }
.env-search .form-input { width: 240px; max-width: 100%; padding-left: 32px; }
.env-search-icon {
  position: absolute; left: 10px; top: 50%; transform: translateY(-50%);
  color: var(--text-muted); pointer-events: none; font-size: 16px;
}
.env-row .cell-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.env-key { font-family: var(--font-mono, monospace); font-size: 13px; }
.env-secret-tag { display: inline-flex; align-items: center; gap: 3px; font-size: 11px; }
.env-value-cell { max-width: 420px; }
.env-mask { letter-spacing: 2px; color: var(--text-muted); }
.env-actions { white-space: nowrap; }
.mono-input { font-family: var(--font-mono, monospace); font-size: 13px; }
.mono-input[readonly] { opacity: 0.7; cursor: not-allowed; }
.text-success { color: var(--success-500, #22c55e); }
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
.log-view { height: 600px; overflow: auto; white-space: pre-wrap; }
/* Runtime Logs panel: height is driven inline by LogSizeControl. */
.log-header-left { display: flex; align-items: center; gap: 12px; }
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
.summary-by {
  margin-left: 4px;
  color: var(--text-muted);
  font-weight: 400;
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
.canary-panel-slot { grid-column: 1 / -1; }
.canary-row { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; padding: 12px 16px; }
.canary-title { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; color: var(--text-primary); white-space: nowrap; }
.canary-meter { flex: 1; min-width: 220px; }
.canary-legend { display: flex; align-items: center; gap: 10px; margin-top: 5px; font-size: 12px; color: var(--text-secondary); flex-wrap: wrap; }
.canary-legend .dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; margin-right: 4px; }
.dot-stable { background: var(--success-500, #22c55e); }
.dot-canary { background: var(--warning-500, #f59e0b); }
.dep-err { margin-top: 3px; font-size: 12px; color: var(--danger-600); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.streaming-tag { display: block; margin-top: 2px; }
.trunc { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* Compact canary rollout strip shown under the app header (any tab). */
.canary-strip { display: flex; align-items: center; gap: 10px; width: 100%; margin: 4px 0 12px; padding: 7px 12px; border: 1px solid var(--warning-500, #f59e0b); border-radius: 8px; background: transparent; color: var(--text-primary); font-size: 13px; text-align: left; cursor: pointer; }
.canary-strip:hover { background: var(--warning-50, rgba(245, 158, 11, 0.08)); }
.canary-strip .mdi { color: var(--warning-500, #f59e0b); }
.canary-strip-text { font-weight: 600; white-space: nowrap; }
.canary-strip-bar { flex: 1; height: 10px; }
.split-bar { display: flex; height: 14px; border-radius: 7px; overflow: hidden; background: var(--bg-tertiary); }
.split-stable { background: var(--success-500, #22c55e); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }
.split-canary { background: var(--warning-500, #f59e0b); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }
.canary-actions { display: flex; gap: 8px; }
.strategy-options { display: flex; flex-direction: column; gap: 8px; }
.strategy-option { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border: 1px solid var(--border-input); border-radius: var(--radius); cursor: pointer; transition: all var(--transition); }
.strategy-option:hover { border-color: var(--text-muted); }
.strategy-option.unavailable { opacity: 0.6; }
.strategy-option.unavailable .strategy-name { text-decoration: line-through; }
.strategy-option.active { border-color: var(--primary-500); background: var(--primary-50); }
.strategy-option input { margin-top: 3px; }
.strategy-option span { display: flex; flex-direction: column; }
.strategy-name { font-weight: 600; font-size: 14px; }
.strategy-hint { font-size: 12px; color: var(--text-muted); }

/* Source card */
.source-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.source-summary { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.source-badge {
  display: inline-flex; align-items: center; gap: 6px; padding: 4px 10px; border-radius: 999px;
  font-size: 12px; font-weight: 600; border: 1px solid var(--border-primary); color: var(--text-primary);
}
.source-badge .mdi { font-size: 15px; }
.source-badge.image { background: var(--primary-50); border-color: var(--primary-500); color: var(--primary-700, var(--text-primary)); }
.source-badge.git { background: var(--warning-50, var(--bg-secondary)); border-color: var(--warning-500); }
.source-ref { font-size: 13px; word-break: break-all; }
.source-managed { display: flex; align-items: flex-start; gap: 6px; margin: 12px 0 0; }

.source-choice { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 8px; }
.source-option {
  display: flex; align-items: center; gap: 10px; padding: 12px; cursor: pointer;
  border: 1px solid var(--border-input); border-radius: var(--radius); transition: all var(--transition);
}
.source-option:hover { border-color: var(--text-muted); }
.source-option.active { border-color: var(--primary-500); background: var(--primary-50); }
.source-option .mdi { font-size: 20px; color: var(--text-muted); }
.source-option.active .mdi { color: var(--primary-500); }
.source-option-text { display: flex; flex-direction: column; }
.source-option-text strong { font-size: 14px; }
.source-option-text small { font-size: 12px; color: var(--text-muted); }
.source-warning { margin-bottom: 16px; }

.form-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-top: 4px; }
.form-error { font-size: 12px; color: var(--danger-600, var(--danger-500)); }

.source-pipeline { border-top: 1px solid var(--border-primary); }
.source-pipeline-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.source-pipeline-title { display: inline-flex; align-items: center; gap: 6px; font-size: 14px; }
.source-pipeline-title .mdi { color: var(--text-muted); }
@media (max-width: 640px) {
  .source-header, .source-pipeline-row { flex-direction: column; align-items: stretch; }
}

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
@media (max-width: 639px) {
.app-header-actions {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    flex-wrap: wrap;
  }
.app-header-actions button {
  flex: 1;
  min-width: 80px;
}}
.config-attach { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border-primary); }
.cfg-preview { margin-bottom: 12px; }
.cfg-preview-path {
  display: block;
  font-family: 'JetBrains Mono', monospace;
  font-size: 12px;
  color: var(--text-muted);
  padding: 2px 0;
}

/* ─── Kernel grants ─── */
.cap-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px;
  margin-bottom: 8px;
}

.cap-option {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  padding: 8px 10px;
  border: 1px solid var(--border-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.cap-option:hover {
  background: var(--bg-hover);
}

.cap-elevated {
  border-color: color-mix(in srgb, var(--warning-600, #d97706) 40%, var(--border-secondary));
}

.cap-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.cap-name {
  font-family: monospace;
  font-size: 13px;
  color: var(--text-primary);
}

.cap-badge {
  margin-left: 6px;
  font-family: var(--font-sans, inherit);
}

.cap-help {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.4;
}

.device-row {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 6px;
}
</style>
