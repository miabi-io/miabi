<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { databaseApi, backupApi } from '@/api/resources'
import { appApi } from '@/api/apps'
import { networkApi } from '@/api/networks'
import { apiErrorMessage } from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import MetadataCard from '@/components/MetadataCard.vue'
import OwnerChip from '@/components/OwnerChip.vue'
import ResourceIcon from '@/components/ResourceIcon.vue'
import { fmtSize } from '@/utils/format'
import { engineLogo, engineMdi } from '@/utils/resourceIcon'
import { copyText } from '@/utils/clipboard'
import type { DatabaseInstance, DBStatus, UpgradeProgress, LogicalDatabase, ConnectionInfo, ForwardSession, Backup, BackupSchedule, Application, Network, UpgradeOptions, UpgradePlan, StatsSample } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const instId = computed(() => Number(route.params.id))
const wid = computed(() => ws.currentWorkspaceId)

const inst = ref<DatabaseInstance | null>(null)
const databases = ref<LogicalDatabase[]>([])
const apps = ref<Application[]>([])
const selected = ref<LogicalDatabase | null>(null)
const backups = ref<Backup[]>([])
const schedules = ref<BackupSchedule[]>([])
const cron = ref('0 3 * * *')
const maxBackups = ref(0)
const retentionDays = ref(0)
const running = ref(false)

const showCreateDb = ref(false)
const creatingDb = ref(false)
const dbForm = ref<{ name: string; app: number | null }>({ name: '', app: null })
const connModal = ref<{ title: string; info: ConnectionInfo } | null>(null)

const forwards = ref<ForwardSession[]>([])
const forwardBusy = ref(false)

const supportsLogical = computed(() => inst.value && inst.value.engine !== 'redis')
// libSQL hosts a single auto-created database (listable + backup-able) but users
// cannot create additional databases on it, unlike the SQL/Mongo engines.
const canCreateLogical = computed(
  () => supportsLogical.value && inst.value?.engine !== 'libsql',
)
const attachedCount = computed(() => databases.value.filter((d) => d.application_id).length)
const deleteBlockedReason = computed(() => {
  if (inst.value?.status === 'running') return 'Stop the database before deleting it'
  if (attachedCount.value > 0) return 'Detach its databases from apps before deleting'
  return ''
})
const appName = (id?: number | null) => apps.value.find((a) => a.id === id)?.name
// Co-location: a database can only be attached to apps on its own node.
const appsOnNode = computed(() =>
  apps.value.filter((a) => (a.server_id ?? 0) === (inst.value?.server_id ?? 0)),
)
const hiddenAppCount = computed(() => apps.value.length - appsOnNode.value.length)

// --- Tabs (state mirrored in the URL query, like the app detail page) ---
type TabKey = 'overview' | 'databases' | 'backups' | 'logs' | 'network' | 'settings'
const tabs = computed<{ key: TabKey; label: string }[]>(() => {
  const t: { key: TabKey; label: string }[] = [{ key: 'overview', label: 'Overview' }]
  if (supportsLogical.value) t.push({ key: 'databases', label: 'Databases' }, { key: 'backups', label: 'Backups' })
  t.push({ key: 'logs', label: 'Logs' }, { key: 'network', label: 'Network' }, { key: 'settings', label: 'Settings' })
  return t
})
function tabFromQuery(): TabKey {
  const q = route.query.tab
  const valid: TabKey[] = ['overview', 'databases', 'backups', 'logs', 'network', 'settings']
  return typeof q === 'string' && valid.includes(q as TabKey) ? (q as TabKey) : 'overview'
}
const tab = ref<TabKey>(tabFromQuery())
watch(tab, (t) => {
  router.replace({ query: { ...route.query, tab: t } })
  if (t === 'logs') startLogs()
  else stopLogs()
})

// --- Container logs (SSE) ---
const logs = ref<string[]>([])
const logsConnected = ref(false)
let logsES: EventSource | null = null
function startLogs() {
  if (!wid.value) return
  stopLogs()
  logs.value = []
  logsES = new EventSource(databaseApi.logsUrl(wid.value, instId.value))
  logsES.onopen = () => { logsConnected.value = true }
  logsES.onmessage = (ev) => {
    try {
      const l = JSON.parse(ev.data) as { text?: string }
      if (l.text != null) logs.value.push(l.text)
    } catch { /* ignore keep-alives */ }
  }
  logsES.onerror = () => { logsConnected.value = false; logsES?.close() }
}
function stopLogs() {
  logsES?.close()
  logsES = null
  logsConnected.value = false
}
onUnmounted(stopLogs)

async function load() {
  if (!wid.value) return
  try {
    inst.value = (await databaseApi.get(wid.value, instId.value)).data.data
    apps.value = (await appApi.list(wid.value)).data.data ?? []
    if (supportsLogical.value) {
      databases.value = (await databaseApi.listDatabases(wid.value, instId.value)).data.data ?? []
      if (selected.value) {
        const still = databases.value.find((d) => d.id === selected.value?.id)
        selected.value = still ?? null
      }
    }
    await loadNetworks()
    if (ws.isWorkspaceAdmin) await loadForwards()
  } catch (e) { notify.apiError(e) }
}
watch([instId, wid], async () => {
  await load()
  loadUpgradeOptions()
  startStatusStream() // live provisioning/upgrade/start-stop status (SSE)
  startMetricsPoll() // live CPU/memory utilisation while running
}, { immediate: true })

// --- Live CPU/memory utilisation ---
// The status SSE stream only carries lifecycle transitions, not stats; the
// one-shot status endpoint returns a container stats snapshot while the instance
// is running, so poll it on an interval (mirrors the AppDetail resource usage).
const metrics = ref<StatsSample | null>(null)
let metricsPoll: ReturnType<typeof setInterval> | null = null

async function loadMetrics() {
  if (!wid.value || inst.value?.status !== 'running') {
    metrics.value = null
    return
  }
  try {
    metrics.value = (await databaseApi.status(wid.value, instId.value)).data.data.stats ?? null
  } catch {
    /* transient; keep the last sample */
  }
}
function startMetricsPoll() {
  stopMetricsPoll()
  void loadMetrics()
  metricsPoll = setInterval(loadMetrics, 5000)
}
function stopMetricsPoll() {
  if (metricsPoll) {
    clearInterval(metricsPoll)
    metricsPoll = null
  }
}

// Resource-usage helpers (shared shape with the AppDetail overview).
function usageTone(pct: number): string {
  if (pct >= 90) return 'usage-danger'
  if (pct >= 70) return 'usage-warn'
  return 'usage-ok'
}

// --- Size sync ---
const syncingSizes = ref(false)
async function syncSizes() {
  if (!wid.value || syncingSizes.value) return
  syncingSizes.value = true
  try {
    inst.value = (await databaseApi.syncSizes(wid.value, instId.value)).data.data
    if (supportsLogical.value) databases.value = (await databaseApi.listDatabases(wid.value, instId.value)).data.data ?? []
    notify.success('Sizes refreshed')
  } catch (e) { notify.apiError(e) }
  finally { syncingSizes.value = false }
}
function fmtBytes(n?: number): string {
  if (!n || n <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}

// --- Networks ---
const networks = ref<Network[]>([])
const netToAttach = ref<number | null>(null)
const netBusy = ref(false)
const attachedNets = computed(() => inst.value?.networks ?? [])
const attachableNets = computed(() => {
  const have = new Set(attachedNets.value.map((n) => n.id))
  return networks.value.filter((n) => !have.has(n.id))
})
async function loadNetworks() {
  if (!wid.value) return
  networks.value = (await networkApi.list(wid.value)).data.data ?? []
}
async function attachNetwork() {
  if (!wid.value || !netToAttach.value || netBusy.value) return
  netBusy.value = true
  try {
    inst.value = (await databaseApi.attachNetwork(wid.value, instId.value, netToAttach.value)).data.data
    netToAttach.value = null
    notify.success('Network connected')
  } catch (e) { notify.apiError(e) }
  finally { netBusy.value = false }
}
async function detachNetwork(n: Network) {
  if (!wid.value || netBusy.value) return
  netBusy.value = true
  try {
    inst.value = (await databaseApi.detachNetwork(wid.value, instId.value, n.id)).data.data
    notify.success(`Disconnected from ${n.name}`)
  } catch (e) { notify.apiError(e) }
  finally { netBusy.value = false }
}

// --- Port-forward (on-demand external access; admin only) ---
async function loadForwards() {
  if (!wid.value) return
  forwards.value = (await databaseApi.listForwards(wid.value, instId.value)).data.data ?? []
}
async function openForward() {
  if (!wid.value || forwardBusy.value) return
  forwardBusy.value = true
  try {
    await databaseApi.openForward(wid.value, instId.value)
    notify.success('Forward opened — connect your client to the shown address')
    await loadForwards()
  } catch (e) { notify.apiError(e, 'Only admins can open a forward') }
  finally { forwardBusy.value = false }
}
async function closeForward(id: string) {
  if (!wid.value) return
  try {
    await databaseApi.closeForward(wid.value, instId.value, id)
    await loadForwards()
  } catch (e) { notify.apiError(e) }
}
function expiresLabel(iso: string) {
  const mins = Math.round((new Date(iso).getTime() - Date.now()) / 60000)
  return mins <= 0 ? 'expiring…' : `expires in ${mins} min`
}

// --- Logical databases ---
function openCreateDb() {
  dbForm.value = { name: '', app: null }
  showCreateDb.value = true
}
async function createDb() {
  if (!wid.value) return
  creatingDb.value = true
  try {
    const res = (await databaseApi.createDatabase(wid.value, instId.value, dbForm.value.name.trim(), dbForm.value.app)).data.data
    notify.success(res?.env_injected ? 'Database created — connection injected into the app' : 'Database created')
    showCreateDb.value = false
    load()
  } catch (e) { notify.apiError(e) }
  finally { creatingDb.value = false }
}
function askRemoveDb(d: LogicalDatabase) {
  confirm.value = {
    kind: 'remove-db', title: 'Delete database', confirmLabel: 'Delete', variant: 'danger',
    message: `Delete database "${d.name}" and its user? This cannot be undone.`,
    run: () => removeDb(d),
  }
}
async function removeDb(d: LogicalDatabase) {
  if (!wid.value) return
  try {
    await databaseApi.removeDatabase(wid.value, instId.value, d.id)
    if (selected.value?.id === d.id) selected.value = null
    notify.success('Database deleted')
    load()
  } catch (e) { notify.apiError(e) }
}
async function revealDb(d: LogicalDatabase) {
  if (!wid.value) return
  try {
    const info = (await databaseApi.databaseConnection(wid.value, instId.value, d.id)).data.data
    if (info) connModal.value = { title: d.name, info }
  } catch (e) { notify.apiError(e, 'Only admins can reveal credentials') }
}

// --- Backups (for the selected logical database) ---
async function selectDb(d: LogicalDatabase) {
  selected.value = d
  await loadBackups()
}
// Open the Backups tab focused on a database (from the Databases table).
async function viewBackups(d: LogicalDatabase) {
  await selectDb(d)
  tab.value = 'backups'
}
function onSelectDb(e: Event) {
  const id = Number((e.target as HTMLSelectElement).value)
  const d = databases.value.find((x) => x.id === id)
  if (d) selectDb(d)
  else selected.value = null
}
async function loadBackups() {
  if (!wid.value || !selected.value) return
  try {
    backups.value = (await backupApi.list(wid.value, instId.value, selected.value.id)).data.data ?? []
    schedules.value = (await backupApi.schedules(wid.value, instId.value, selected.value.id)).data.data ?? []
  } catch (e) { notify.apiError(e) }
}
async function runBackup() {
  if (!wid.value || !selected.value) return
  running.value = true
  try {
    const b = (await backupApi.run(wid.value, instId.value, selected.value.id)).data.data
    notify[b.status === 'completed' ? 'success' : 'error'](`Backup ${b.status}`)
    loadBackups()
  } catch (e) { notify.apiError(e) }
  finally { running.value = false }
}
// --- Restore dialog (existing backup or uploaded file; normal or force) ---
const restoreModal = ref<{ backupId: number | null } | null>(null)
const restoreMethod = ref<'normal' | 'force'>('normal')
const restoreFile = ref<File | null>(null)
const restoring = ref(false)
function openRestore(backupId: number | null) {
  restoreModal.value = { backupId }
  restoreMethod.value = 'normal'
  restoreFile.value = null
}
function onRestoreFile(e: Event) {
  restoreFile.value = (e.target as HTMLInputElement).files?.[0] ?? null
}
async function runRestore() {
  if (!wid.value || !selected.value || !restoreModal.value || restoring.value) return
  restoring.value = true
  try {
    if (restoreModal.value.backupId != null) {
      await backupApi.restore(wid.value, instId.value, selected.value.id, restoreModal.value.backupId, restoreMethod.value)
    } else {
      if (!restoreFile.value) { notify.error('Choose a dump file'); restoring.value = false; return }
      await backupApi.restoreFile(wid.value, instId.value, selected.value.id, restoreFile.value, restoreMethod.value)
    }
    notify.success('Database restored')
    restoreModal.value = null
    loadBackups()
  } catch (e) { notify.apiError(e) }
  finally { restoring.value = false }
}
async function downloadBackup(b: Backup) {
  if (!wid.value || !selected.value) return
  try {
    const res = await backupApi.download(wid.value, instId.value, selected.value.id, b.id)
    const url = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = url
    a.download = b.filename || `backup-${b.id}.sql.gz`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  } catch (e) { notify.apiError(e, 'Download failed') }
}
function askRemoveBackup(b: Backup) {
  confirm.value = {
    kind: 'remove-backup', title: 'Delete backup', confirmLabel: 'Delete', variant: 'danger',
    message: `Delete backup #${b.id}? The artifact will be removed.`,
    run: () => removeBackup(b),
  }
}
async function removeBackup(b: Backup) {
  if (!wid.value || !selected.value) return
  try {
    await backupApi.remove(wid.value, instId.value, selected.value.id, b.id)
    notify.success('Backup deleted')
    loadBackups()
  } catch (e) { notify.apiError(e) }
}
async function addSchedule() {
  if (!wid.value || !selected.value) return
  try {
    await backupApi.createSchedule(wid.value, instId.value, selected.value.id, cron.value, maxBackups.value, retentionDays.value)
    notify.success('Schedule created')
    loadBackups()
  } catch (e) { notify.apiError(e) }
}
async function delSchedule(id: number) {
  if (!wid.value || !selected.value) return
  await backupApi.deleteSchedule(wid.value, instId.value, selected.value.id, id).catch((e: unknown) => notify.apiError(e))
  loadBackups()
}

// --- Instance lifecycle ---
const lifecycleBusy = ref(false)
async function lifecycle(action: 'start' | 'stop' | 'restart') {
  if (!wid.value || lifecycleBusy.value) return
  lifecycleBusy.value = true
  try {
    await databaseApi[action](wid.value, instId.value)
    notify.success(`Database ${action === 'stop' ? 'stopped' : action === 'start' ? 'started' : 'restarted'}`)
    load()
  } catch (e) { notify.apiError(e) }
  finally { lifecycleBusy.value = false }
}

// --- Confirmation dialog (delete / stop / restart) ---
type ConfirmKind = 'delete' | 'stop' | 'restart' | 'remove-db' | 'remove-backup' | 'upgrade'
const confirm = ref<{
  kind: ConfirmKind
  title: string
  message: string
  confirmLabel: string
  variant: 'danger' | 'primary'
  // requireName gates the action behind typing the instance name (used for the
  // destructive instance delete, mirroring the application delete).
  requireName?: boolean
  run: () => Promise<void>
} | null>(null)
const confirmBusy = ref(false)
// Typed-name confirmation for the instance delete.
const deleteConfirm = ref('')
const confirmBlocked = computed(() => !!confirm.value?.requireName && deleteConfirm.value !== inst.value?.name)

function askStop() {
  confirm.value = {
    kind: 'stop', title: 'Stop database', confirmLabel: 'Stop', variant: 'primary',
    message: 'Apps using this database will lose connectivity until it is started again.',
    run: () => lifecycle('stop'),
  }
}
function askRestart() {
  confirm.value = {
    kind: 'restart', title: 'Restart database', confirmLabel: 'Restart', variant: 'primary',
    message: `The ${inst.value?.engine ?? 'database'} container will be restarted. Brief downtime is expected.`,
    run: () => lifecycle('restart'),
  }
}
function askDelete() {
  deleteConfirm.value = ''
  confirm.value = {
    kind: 'delete', title: 'Delete database instance', confirmLabel: 'Delete', variant: 'danger',
    requireName: true,
    message: `Delete "${inst.value?.name}", all its databases, and its data volume? This cannot be undone.`,
    run: () => removeInstance(),
  }
}
async function runConfirm() {
  if (!confirm.value || confirmBusy.value || confirmBlocked.value) return
  confirmBusy.value = true
  try {
    await confirm.value.run()
    confirm.value = null
  } finally {
    confirmBusy.value = false
  }
}

// --- Instance ---
async function revealInstance() {
  if (!wid.value) return
  try {
    const info = (await databaseApi.credentials(wid.value, instId.value)).data.data
    if (info) connModal.value = { title: `${inst.value?.name} (admin)`, info }
  } catch (e) { notify.apiError(e, 'Only admins can reveal credentials') }
}
async function removeInstance() {
  if (!wid.value) return
  try { await databaseApi.remove(wid.value, instId.value); notify.success('Instance deleted'); router.push('/databases') }
  catch (e) { notify.apiError(e) }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}
function badge(s: string) {
  return s === 'running' || s === 'completed' ? 'badge-success' : s === 'failed' ? 'badge-danger' : 'badge-warning'
}

// --- Version upgrade ---
const upgradeOpts = ref<UpgradeOptions | null>(null)
const upgradeTarget = ref('')
const upgradePlan = ref<UpgradePlan | null>(null)
const upgradePlanErr = ref('')
const stopApps = ref(true)
const upgrading = ref(false)

// --- Upgrade progress stepper -----------------------------------------------
const PHASE_LABELS: Record<string, string> = {
  queued: 'Queued',
  'backing-up': 'Backing up',
  'stopping-apps': 'Stopping apps',
  swapping: 'Swapping engine',
  dumping: 'Dumping',
  restoring: 'Restoring data',
  verifying: 'Verifying',
  'starting-apps': 'Restarting apps',
  done: 'Done',
}
function phaseLabel(p: string): string { return PHASE_LABELS[p] ?? p }

// The ordered phases for the current upgrade path; the dump & restore (major)
// path adds a data-restore step. Skipped phases (e.g. no apps to stop) still
// render as completed once the engine moves past them.
const upgradeSteps = computed<string[]>(() => {
  const base = ['queued', 'backing-up', 'stopping-apps', 'swapping']
  if (inst.value?.upgrade?.path === 'dump-restore') base.push('restoring')
  base.push('verifying')
  return base
})
function phaseState(phase: string): 'done' | 'current' | 'todo' {
  const cur = inst.value?.upgrade?.phase ?? ''
  const steps = upgradeSteps.value
  const ci = steps.indexOf(cur)
  const pi = steps.indexOf(phase)
  if (ci < 0 || pi < 0) return 'todo'
  if (pi < ci) return 'done'
  return pi === ci ? 'current' : 'todo'
}

async function loadUpgradeOptions() {
  if (!wid.value || !inst.value || inst.value.engine === undefined) return
  try {
    upgradeOpts.value = (await databaseApi.upgradeOptions(wid.value, instId.value)).data.data
  } catch { /* non-fatal: the card just shows a free-text version field */ }
}

// Preview the resolved plan (path/major/affected apps) whenever a target is set.
async function previewPlan() {
  upgradePlan.value = null
  upgradePlanErr.value = ''
  const v = upgradeTarget.value.trim()
  if (!wid.value || !v) return
  try {
    upgradePlan.value = (await databaseApi.upgradePlan(wid.value, instId.value, v)).data.data
  } catch (e) {
    upgradePlanErr.value = apiErrorMessage(e, 'Cannot upgrade to that version')
  }
}
watch(upgradeTarget, previewPlan)

const upgradeAppNames = computed(() =>
  (upgradePlan.value?.affected_app_ids ?? []).map((id) => apps.value.find((a) => a.id === id)?.name || `app #${id}`),
)

function askUpgrade() {
  if (!upgradePlan.value) return
  const p = upgradePlan.value
  const lines = [`Upgrade ${inst.value?.name} from ${p.from_version} to ${p.to_version}.`]
  if (p.path === 'dump-restore') lines.push('This is a major upgrade: data is dumped and restored into a fresh volume (the instance keeps its address and credentials).')
  else lines.push('In-place upgrade: the engine image is swapped on the same data volume.')
  if (stopApps.value && upgradeAppNames.value.length) lines.push(`These apps will be stopped during the upgrade and restarted after: ${upgradeAppNames.value.join(', ')}.`)
  lines.push('A full backup is taken first.')
  confirm.value = {
    kind: 'upgrade', title: 'Upgrade database version', confirmLabel: 'Upgrade',
    variant: p.major ? 'danger' : 'primary',
    message: lines.join(' '),
    run: () => runUpgrade(),
  }
}

async function runUpgrade() {
  if (!wid.value || !upgradePlan.value) return
  upgrading.value = true
  try {
    inst.value = (await databaseApi.upgrade(wid.value, instId.value, upgradePlan.value.to_version, stopApps.value)).data.data
    upgradeFailNotified = false
    notify.success('Upgrade started')
    upgradeTarget.value = ''
    upgradePlan.value = null
    // The live status stream (already open) drives progress + the final result.
  } catch (e) {
    notify.apiError(e, 'Upgrade failed to start')
  } finally {
    upgrading.value = false
  }
}

// --- Live status stream (SSE) ------------------------------------------------
// One persistent stream per open page carries provisioning progress, upgrade
// phases and start/stop transitions — replacing the previous per-second polls.
let statusES: EventSource | null = null
let primed = false // first snapshot syncs state without firing a toast
let upgradeFailNotified = false
const provisionMsg = ref('') // transient bring-up line (e.g. "Pulling postgres:18")
const lastUpgrade = ref<UpgradeProgress | null>(null)

function stopStatusStream() { statusES?.close(); statusES = null }
function startStatusStream() {
  if (!wid.value) return
  stopStatusStream()
  primed = false
  statusES = new EventSource(databaseApi.eventsUrl(wid.value, instId.value))
  statusES.onmessage = (ev) => {
    let msg: { type?: string; data?: unknown }
    try { msg = JSON.parse(ev.data) } catch { return } // ignore keep-alives
    if (msg.type === 'progress') {
      provisionMsg.value = (msg.data as { message?: string })?.message ?? ''
    } else if (msg.type === 'status' && msg.data) {
      applyStatus(msg.data as { status: DBStatus; upgrade?: UpgradeProgress })
    }
  }
  // EventSource auto-reconnects on transient network errors; nothing to do here.
}

// applyStatus reconciles a streamed snapshot into the page and surfaces
// success/error feedback on terminal transitions.
function applyStatus(s: { status: DBStatus; upgrade?: UpgradeProgress }) {
  const prev = inst.value?.status
  if (inst.value) { inst.value.status = s.status; inst.value.upgrade = s.upgrade }
  if (s.upgrade) lastUpgrade.value = s.upgrade
  if (s.status !== 'provisioning') provisionMsg.value = ''
  ensureBackstop()

  if (!primed) { primed = true; return } // initial snapshot: sync only

  // A failed upgrade can land as 'running' (rolled back) or 'failed', so check it
  // before the status-transition cases. Notify once.
  if (s.upgrade?.phase === 'failed') {
    if (!upgradeFailNotified) {
      upgradeFailNotified = true
      notify.error(`Upgrade failed: ${s.upgrade.error ?? 'unknown error'}`)
      void load()
    }
    return
  }

  if (prev === s.status) return

  if (prev === 'upgrading' && s.status === 'running') {
    notify.success(`Upgrade to ${lastUpgrade.value?.to_version ?? 'the new version'} completed`)
    void load()
  } else if (prev === 'provisioning' && s.status === 'running') {
    notify.success('Database is ready')
    void load()
  } else if (s.status === 'failed') {
    notify.error('Database failed to provision')
    void load()
  } else {
    void load() // running <-> stopped, etc. — pull fresh details
  }
}

// Backstop: a slow reconcile while a transition is in flight, covering the
// split-worker deployment where worker events don't reach this process's bus.
// Idle (running/stopped) pages make no requests at all.
let backstop: ReturnType<typeof setInterval> | null = null
function ensureBackstop() {
  const transient = inst.value?.status === 'provisioning' || inst.value?.status === 'upgrading'
  if (transient && !backstop) backstop = setInterval(() => { void load() }, 12000)
  else if (!transient && backstop) { clearInterval(backstop); backstop = null }
}

onUnmounted(() => { stopStatusStream(); stopMetricsPoll(); if (backstop) clearInterval(backstop) })
</script>

<template>
  <div v-if="inst">
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/databases')">
          <span class="mdi mdi-arrow-left"></span> Databases
        </button>
        <div class="flex items-center gap-3" style="margin-top: 8px">
          <ResourceIcon :src="engineLogo(inst.engine)" :mdi="engineMdi(inst.engine)" :name="inst.name" :size="44" />
          <div>
            <h1>{{ inst.display_name || inst.name }}</h1>
            <div class="text-muted text-sm">
              <span class="mdi mdi-docker"></span>
              {{ inst.engine }} {{ inst.version }}
              <template v-if="inst.server_name"> · <span class="mdi mdi-server-network"></span> {{ inst.server_name }}</template>
              · <code :title="'In-network address — reachable by apps on ' + (inst.server_name || 'this node')">{{ inst.host }}:{{ inst.port }}</code>
            </div>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <span class="badge badge-dot" :class="badge(inst.status)">{{ inst.status }}</span>
        <button class="btn btn-secondary btn-sm" @click="revealInstance"><span class="mdi mdi-eye-outline"></span> Admin connection</button>
        <button v-if="ws.isWorkspaceAdmin" class="btn btn-secondary btn-sm" :disabled="inst.status !== 'running' || forwardBusy" title="Open a temporary external connection to this database" @click="openForward"><span class="mdi mdi-lan-connect"></span> Connect externally</button>
        <button v-if="ws.canEdit && inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="syncingSizes" title="Refresh on-disk size info" @click="syncSizes"><span class="mdi mdi-sync" :class="{ 'mdi-spin': syncingSizes }"></span> Sync sizes</button>
        <template v-if="ws.canEdit">
          <button v-if="inst.status === 'stopped' || inst.status === 'failed'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="lifecycle('start')"><span class="mdi mdi-play"></span> Start</button>
          <button v-if="inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="askRestart"><span class="mdi mdi-restart"></span> Restart</button>
          <button v-if="inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="askStop"><span class="mdi mdi-stop"></span> Stop</button>
        </template>
      </div>
    </div>

    <!-- Provisioning progress (live via SSE) -->
    <div v-if="inst.status === 'provisioning'" class="app-banner app-banner--info mb-4">
      <span class="mdi mdi-progress-download app-banner-icon mdi-spin"></span>
      <div class="app-banner-content">
        <p class="app-banner-text">Provisioning {{ inst.engine }} {{ inst.version }}…</p>
        <p v-if="provisionMsg" class="app-banner-sub">{{ provisionMsg }}</p>
      </div>
    </div>

    <div class="tabs">
      <button v-for="t in tabs" :key="t.key" class="tab" :class="{ active: tab === t.key }" @click="tab = t.key">{{ t.label }}</button>
    </div>

    <!-- OVERVIEW -->
    <template v-if="tab === 'overview'">
      <div class="card mb-4">
        <div class="card-header"><h2>Details</h2></div>
        <div class="detail-grid">
          <div><span class="detail-label">Status</span><span class="badge badge-dot" :class="badge(inst.status)">{{ inst.status }}</span></div>
          <div><span class="detail-label">Owner</span><OwnerChip :metadata="inst.metadata" /></div>
          <div><span class="detail-label">Engine</span>{{ inst.engine }} {{ inst.version }}</div>
          <div><span class="detail-label">In-network address</span><code>{{ inst.host }}:{{ inst.port }}</code></div>
          <div v-if="inst.server_name"><span class="detail-label">Node</span>{{ inst.server_name }}</div>
          <div v-if="inst.volume_name"><span class="detail-label">Data volume</span><code>{{ inst.volume_name }}</code><template v-if="inst.mount_path"> → <code>{{ inst.mount_path }}</code></template></div>
          <div v-if="inst.size_synced_at"><span class="detail-label">On-disk size</span>{{ fmtBytes(inst.size_bytes) }}</div>
        </div>
      </div>

      <h2 class="section-title">
        Resource usage
        <span v-if="metrics" class="live-tag"><span class="live-dot"></span> live</span>
      </h2>
      <div v-if="metrics" class="stats-grid mb-4">
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">CPU</span><span class="stat-icon stat-icon-primary"><span class="mdi mdi-chip"></span></span></div>
          <div class="stat-value">{{ metrics.cpu_percent.toFixed(1) }}%</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.cpu_percent)" :style="{ width: Math.min(100, metrics.cpu_percent) + '%' }"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Memory</span><span class="stat-icon stat-icon-info"><span class="mdi mdi-memory"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.memory_usage_bytes) }}</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.memory_percent)" :style="{ width: Math.min(100, metrics.memory_percent) + '%' }"></div></div>
          <div class="stat-sub">
            {{ metrics.memory_percent.toFixed(1) }}%<template v-if="metrics.memory_limit_bytes"> of {{ fmtSize(metrics.memory_limit_bytes) }}</template>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Net in</span><span class="stat-icon stat-icon-success"><span class="mdi mdi-download"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.network_rx_bytes) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">Net out</span><span class="stat-icon stat-icon-warning"><span class="mdi mdi-upload"></span></span></div>
          <div class="stat-value">{{ fmtSize(metrics.network_tx_bytes) }}</div>
        </div>
      </div>
      <div v-else class="card mb-4">
        <div class="empty-state" style="padding: 28px">
          <span class="mdi mdi-chart-line" style="font-size: 32px; color: var(--text-muted)"></span>
          <p>No live metrics — the database has no running container.</p>
        </div>
      </div>

      <MetadataCard :metadata="inst.metadata" class="mb-4" />

      <MetadataCard :metadata="inst.annotations" title="Annotations" :reserved="false" class="mb-4" />

      <!-- Live external forwards (admin only) -->
      <div v-if="ws.isWorkspaceAdmin && forwards.length" class="card">
        <div class="card-header">
          <h2>External forwards</h2>
          <span class="text-muted text-sm">Temporary, source-IP-locked connections — no host port is exposed.</span>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Endpoint</th><th>Expires</th><th></th></tr></thead>
            <tbody>
              <tr v-for="f in forwards" :key="f.id">
                <td class="cell-title" style="font-family: monospace">
                  {{ f.host }}:{{ f.port }}
                  <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`${f.host}:${f.port}`)"><span class="mdi mdi-content-copy"></span></button>
                </td>
                <td class="cell-sub">{{ expiresLabel(f.expires_at) }}</td>
                <td class="text-right">
                  <button class="btn btn-sm btn-secondary" @click="closeForward(f.id)">Close</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- DATABASES (SQL engines) -->
    <template v-else-if="tab === 'databases'">
      <div class="card">
        <div class="card-header">
          <h2>Databases</h2>
          <button v-if="ws.canEdit && canCreateLogical" class="btn btn-sm btn-primary" :disabled="inst.status !== 'running'" @click="openCreateDb">
            <span class="mdi mdi-plus"></span> New database
          </button>
        </div>
        <div v-if="inst.status !== 'running'" class="card-body text-muted text-sm">Instance is {{ inst.status }} — databases can be created once it is running.</div>
        <div v-else-if="databases.length === 0" class="empty-state">
          <span class="mdi mdi-database-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No databases yet. Create one per app to share this instance.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Database</th><th>User</th><th>App</th><th>Size</th><th></th></tr></thead>
            <tbody>
              <tr v-for="d in databases" :key="d.id" class="row-clickable" :class="{ selected: selected?.id === d.id }" @click="viewBackups(d)">
                <td class="cell-title" style="font-family: monospace">{{ d.name }}</td>
                <td class="cell-sub" style="font-family: monospace">{{ d.username }}</td>
                <td class="cell-sub">
                  <RouterLink v-if="d.application_id" :to="`/apps/${d.application_id}`" @click.stop>{{ appName(d.application_id) || `app #${d.application_id}` }}</RouterLink>
                  <span v-else>—</span>
                </td>
                <td class="cell-sub">{{ fmtBytes(d.size_bytes) }}</td>
                <td class="text-right" @click.stop>
                  <button class="btn-icon btn-icon-muted" title="Backups" aria-label="Backups" @click="viewBackups(d)"><span class="mdi mdi-backup-restore"></span></button>
                  <button class="btn-icon btn-icon-muted" title="Reveal connection" aria-label="Reveal connection" @click="revealDb(d)"><span class="mdi mdi-key-outline"></span></button>
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="askRemoveDb(d)"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- BACKUPS (SQL engines; per logical database) -->
    <template v-else-if="tab === 'backups'">
      <div class="card mb-4">
        <div class="card-header">
          <h2>Backups</h2>
          <label class="flex items-center gap-2">
            <span class="text-muted text-sm">Database</span>
            <select class="form-select" style="min-width: 180px" :value="selected?.id ?? ''" @change="onSelectDb">
              <option value="" disabled>Select a database…</option>
              <option v-for="d in databases" :key="d.id" :value="d.id">{{ d.name }}</option>
            </select>
          </label>
        </div>
        <div v-if="!selected" class="empty-state">
          <span class="mdi mdi-backup-restore" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ databases.length ? 'Select a database to view its backups.' : 'Create a database first to back it up.' }}</p>
        </div>
        <template v-else>
          <div class="card-header" style="border-top: 1px solid var(--border-primary)">
            <h3 style="margin: 0"><code>{{ selected.name }}</code></h3>
            <div class="flex items-center gap-2">
              <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" @click="openRestore(null)"><span class="mdi mdi-upload-outline"></span> Restore from file</button>
              <button v-if="ws.canEdit" class="btn btn-sm btn-primary" :disabled="running" @click="runBackup">{{ running ? 'Backing up…' : 'Run backup' }}</button>
            </div>
          </div>
          <div v-if="backups.length === 0" class="empty-state">
            <span class="mdi mdi-backup-restore" style="font-size: 36px; color: var(--text-muted)"></span>
            <p>No backups yet.</p>
          </div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>Backup</th><th>Status</th><th></th></tr></thead>
              <tbody>
                <tr v-for="b in backups" :key="b.id">
                  <td>
                    <span class="cell-title">#{{ b.id }}</span>
                    <div class="cell-sub">{{ b.trigger }} · {{ b.destination }}<template v-if="b.filename"> · {{ b.filename }}</template></div>
                  </td>
                  <td><span class="badge badge-dot" :class="badge(b.status)">{{ b.status }}</span></td>
                  <td class="text-right table-actions">
                    <button v-if="b.status === 'completed' && b.destination === 'local' && ws.canEdit" class="btn-icon btn-icon-muted" title="Download" aria-label="Download" @click="downloadBackup(b)"><span class="mdi mdi-download-outline"></span></button>
                    <button v-if="b.status === 'completed' && ws.canEdit" class="btn btn-sm btn-secondary" @click="openRestore(b.id)">Restore</button>
                    <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="askRemoveBackup(b)"><span class="mdi mdi-delete-outline"></span></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </div>

      <div v-if="selected" class="card">
        <div class="card-header"><h2>Backup schedules · <code>{{ selected.name }}</code></h2></div>
        <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <form class="flex items-center gap-2" style="flex-wrap: wrap" @submit.prevent="addSchedule">
            <label class="sched-field">
              <span class="text-muted text-sm">Cron (UTC)</span>
              <input v-model="cron" class="form-input" placeholder="0 3 * * *" style="max-width: 160px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">Keep last (0 = all)</span>
              <input v-model.number="maxBackups" type="number" min="0" class="form-input" style="max-width: 120px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">Max age days (0 = ∞)</span>
              <input v-model.number="retentionDays" type="number" min="0" class="form-input" style="max-width: 130px" />
            </label>
            <button class="btn btn-primary" style="align-self: flex-end">Add schedule</button>
          </form>
        </div>
        <div v-if="schedules.length === 0" class="empty-state"><p>No schedules.</p></div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Schedule</th><th>Destination</th><th>Retention</th><th></th></tr></thead>
            <tbody>
              <tr v-for="s in schedules" :key="s.id">
                <td class="cell-title">{{ s.cron }}</td>
                <td class="text-muted">{{ s.destination === 'workspace' ? 'Workspace settings' : s.destination }}</td>
                <td class="cell-sub">
                  <template v-if="s.max_backups || s.retention_days">
                    <span v-if="s.max_backups">keep {{ s.max_backups }}</span>
                    <span v-if="s.max_backups && s.retention_days"> · </span>
                    <span v-if="s.retention_days">{{ s.retention_days }}d</span>
                  </template>
                  <span v-else>—</span>
                </td>
                <td class="text-right">
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="delSchedule(s.id)"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- LOGS -->
    <div v-else-if="tab === 'logs'" class="card">
      <div class="card-header">
        <h2>Container logs</h2>
        <span class="badge" :class="logsConnected ? 'badge-success badge-dot' : 'badge-neutral'">{{ logsConnected ? 'live' : 'connecting…' }}</span>
      </div>
      <div class="card-body">
        <pre class="code-block log-view">{{ logs.length ? logs.join('\n') : 'Waiting for output… (the instance must have a running container)' }}</pre>
      </div>
    </div>

    <!-- NETWORK -->
    <template v-else-if="tab === 'network'">
      <div class="card mb-4">
        <div class="card-header">
          <h2>Networks</h2>
          <span class="text-muted text-sm">Workspace networks this database is reachable on.</span>
        </div>
        <div v-if="attachedNets.length === 0" class="card-body text-muted text-sm">Not connected to any network yet.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Network</th><th>Docker name</th><th>Driver</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">{{ n.name }}</td>
                <td class="cell-sub" style="font-family: monospace">{{ n.docker_name }}</td>
                <td class="cell-sub">{{ n.driver }}<span v-if="n.internal"> · internal</span></td>
                <td class="text-right"><span v-if="n.is_default" class="badge badge-info">default</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><h2>In-network address</h2></div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-bottom: 8px">Apps on a shared network reach this database by its stable alias:</p>
          <div class="dns-field">
            <span class="dns-field-label">Host</span>
            <div class="dns-field-row">
              <span class="dns-field-value">{{ inst.host }}:{{ inst.port }}</span>
              <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(`${inst.host}:${inst.port}`)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- SETTINGS -->
    <template v-else-if="tab === 'settings'">
      <!-- Engine version upgrade -->
      <div class="card mb-4">
        <div class="card-header">
          <h2>Engine version</h2>
          <span class="text-muted text-sm">Currently {{ inst.engine }} {{ inst.version }}.</span>
        </div>

        <!-- Live progress while upgrading: a phase stepper driven by the SSE stream -->
        <div v-if="inst.status === 'upgrading' && inst.upgrade" class="card-body">
          <p class="text-sm" style="margin: 0 0 12px">
            Upgrading <strong>{{ inst.upgrade.from_version }}</strong> → <strong>{{ inst.upgrade.to_version }}</strong>
            <span class="badge" :class="inst.upgrade.path === 'dump-restore' ? 'badge-warning' : 'badge-info'" style="margin-left: 6px">
              {{ inst.upgrade.path === 'dump-restore' ? 'dump &amp; restore' : 'in-place' }}
            </span>
          </p>
          <ol class="upgrade-steps">
            <li v-for="step in upgradeSteps" :key="step" class="upgrade-step" :class="`is-${phaseState(step)}`">
              <span class="upgrade-step-mark" aria-hidden="true">
                <span v-if="phaseState(step) === 'done'" class="mdi mdi-check-circle"></span>
                <span v-else-if="phaseState(step) === 'current'" class="mdi mdi-loading mdi-spin"></span>
                <span v-else class="mdi mdi-circle-outline"></span>
              </span>
              <span class="upgrade-step-label">{{ phaseLabel(step) }}</span>
            </li>
          </ol>
          <p class="form-hint" style="margin-top: 10px">
            <span class="mdi mdi-shield-check-outline"></span>
            A full backup was taken first — your data is safe if anything goes wrong.
          </p>
        </div>
        <template v-else>
        <div v-if="inst.upgrade && inst.upgrade.phase === 'failed'" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <div class="app-banner app-banner--danger">
            <span class="mdi mdi-alert-circle-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-text">
                <strong>Upgrade to {{ inst.upgrade.to_version }} failed.</strong>
                {{ inst.upgrade.error }}
              </p>
              <p class="app-banner-sub">The instance was rolled back to {{ inst.upgrade.from_version }} and is safe to use. You can adjust the target and try again below.</p>
            </div>
          </div>
        </div>

        <!-- Picker (running or stopped instances) -->
        <div v-if="ws.canEdit && (inst.status === 'running' || inst.status === 'stopped' || inst.status === 'failed')" class="card-body">
          <div class="flex items-center gap-2" style="flex-wrap: wrap">
            <label class="text-muted text-sm">Upgrade to</label>
            <input
              v-model="upgradeTarget"
              class="form-input"
              list="db-versions"
              style="max-width: 160px"
              placeholder="e.g. 17"
            />
            <datalist id="db-versions">
              <option v-for="v in upgradeOpts?.suggestions ?? []" :key="v" :value="v" />
            </datalist>
            <button class="btn btn-primary" :disabled="!upgradePlan || upgrading" @click="askUpgrade">
              <span class="mdi mdi-arrow-up-bold-circle-outline"></span> Upgrade
            </button>
          </div>
          <p v-if="upgradePlanErr" class="form-hint" style="color: var(--danger-600); margin-top: 8px">{{ upgradePlanErr }}</p>
          <div v-else-if="upgradePlan" class="upgrade-plan">
            <p class="text-sm">
              <span class="badge" :class="upgradePlan.major ? 'badge-warning' : 'badge-info'">{{ upgradePlan.major ? 'major' : 'minor' }}</span>
              {{ upgradePlan.path === 'dump-restore'
                ? 'Dump & restore into a fresh volume — the instance keeps its address and credentials, so apps need no change.'
                : 'In-place image swap on the same data volume — fast, seconds of downtime.' }}
            </p>
            <label v-if="upgradeAppNames.length" class="radio-row">
              <input type="checkbox" v-model="stopApps" />
              Stop the {{ upgradeAppNames.length }} app(s) using this database during the upgrade, then restart them
              <span class="text-muted">({{ upgradeAppNames.join(', ') }})</span>
            </label>
            <p class="form-hint">A full backup is taken before the upgrade begins.</p>
          </div>
        </div>
        <div v-else class="card-body text-muted text-sm">The instance must be running or stopped to upgrade.</div>
        </template>
      </div>

      <div class="card mb-4">
        <div class="card-header">
          <h2>Networks</h2>
          <span class="text-muted text-sm">Connect this database to additional workspace networks.</span>
        </div>
        <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <form class="flex items-center gap-2" @submit.prevent="attachNetwork">
            <select v-model.number="netToAttach" class="form-select" aria-label="Network to connect" style="min-width: 220px" :disabled="netBusy || attachableNets.length === 0">
              <option :value="null" disabled>{{ attachableNets.length ? 'Select a network…' : 'All networks already attached' }}</option>
              <option v-for="n in attachableNets" :key="n.id" :value="n.id">{{ n.name }}</option>
            </select>
            <button class="btn btn-primary" :disabled="!netToAttach || netBusy"><span class="mdi mdi-lan-connect"></span> Connect</button>
          </form>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Network</th><th>Docker name</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">{{ n.name }} <span v-if="n.is_default" class="badge badge-info" style="margin-left: 6px">default</span></td>
                <td class="cell-sub" style="font-family: monospace">{{ n.docker_name }}</td>
                <td class="text-right">
                  <button
                    v-if="ws.canEdit && !n.is_default"
                    class="btn btn-sm btn-secondary"
                    :disabled="netBusy"
                    @click="detachNetwork(n)"
                  >Disconnect</button>
                  <span v-else-if="n.is_default" class="text-muted text-sm" title="The default network is always attached">always attached</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="ws.canEdit" class="card danger-card">
        <div class="card-header"><h2>Danger zone</h2></div>
        <div class="card-body flex items-center justify-between gap-3">
          <div>
            <div class="cell-title">Delete this database instance</div>
            <div class="cell-sub">Removes the instance, all its databases, and its data volume. This cannot be undone.</div>
          </div>
          <button class="btn btn-danger" :disabled="deleteBlockedReason !== ''" :title="deleteBlockedReason || 'Delete instance'" @click="askDelete">Delete</button>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <!-- Create logical database -->
      <AppModal v-if="showCreateDb" @close="showCreateDb = false">
        <div class="modal-header">
          <h3>New database</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreateDb = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="createDb">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="dbForm.name" class="form-input" placeholder="e.g. blog" required autofocus />
              <p class="form-hint">A dedicated user is created with access scoped to this database.</p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Attach to app <span class="text-muted">(optional)</span></label>
              <select v-model="dbForm.app" class="form-select">
                <option :value="null">Don't attach</option>
                <option v-for="a in appsOnNode" :key="a.id" :value="a.id">{{ a.name }}</option>
              </select>
              <p class="form-hint">
                Injects DATABASE_URL + DB_* env into the app and redeploys it.
                <template v-if="hiddenAppCount > 0"> {{ hiddenAppCount }} app(s) on other nodes are hidden (must share the database's node).</template>
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreateDb = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="creatingDb">{{ creatingDb ? 'Creating…' : 'Create database' }}</button>
          </div>
        </form>
      </AppModal>

      <!-- Connection reveal -->
      <AppModal v-if="connModal" max-width="560px" @close="connModal = null">
        <div class="modal-header">
          <h3>Connection · {{ connModal.title }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="connModal = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-for="f in [
            { label: 'Host', value: `${connModal.info.host}:${connModal.info.port}` },
            { label: 'Database', value: connModal.info.database },
            { label: 'Username', value: connModal.info.username },
            { label: 'Password', value: connModal.info.password },
            { label: 'URI', value: connModal.info.uri },
          ]" :key="f.label" class="dns-field">
            <span class="dns-field-label">{{ f.label }}</span>
            <div class="dns-field-row">
              <span class="dns-field-value">{{ f.value || '—' }}</span>
              <button v-if="f.value" class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(f.value)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </div>
        </div>
      </AppModal>

      <!-- Restore dialog -->
      <AppModal v-if="restoreModal" @close="restoreModal = null">
        <div class="modal-header">
          <h3>{{ restoreModal.backupId != null ? `Restore backup #${restoreModal.backupId}` : 'Restore from file' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="restoreModal = null"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="runRestore">
          <div class="modal-body">
            <div v-if="restoreModal.backupId == null" class="form-group">
              <label class="form-label">Dump file</label>
              <input type="file" accept=".sql,.gz,.sql.gz,.dump" class="form-input" required @change="onRestoreFile" />
              <p class="form-hint">A <code>.sql.gz</code>, <code>.sql</code>, or <code>.dump</code> produced by the matching engine.</p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Method</label>
              <label class="radio-row"><input type="radio" value="normal" v-model="restoreMethod" /> Normal — restore over the existing database</label>
              <label class="radio-row"><input type="radio" value="force" v-model="restoreMethod" /> Force — drop &amp; recreate the database first</label>
              <p v-if="restoreMethod === 'force'" class="form-hint" style="color: var(--danger-600)">Force drops the database before restoring. This cannot be undone.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="restoreModal = null">Cancel</button>
            <button type="submit" class="btn" :class="restoreMethod === 'force' ? 'btn-danger' : 'btn-primary'" :disabled="restoring">{{ restoring ? 'Restoring…' : 'Restore' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!confirm"
      :title="confirm?.title ?? ''"
      :message="confirm?.message ?? ''"
      :confirm-label="confirm?.confirmLabel ?? 'Confirm'"
      :variant="confirm?.variant ?? 'primary'"
      :busy="confirmBusy"
      :confirm-disabled="confirmBlocked"
      @confirm="runConfirm"
      @cancel="confirm = null"
    >
      <div v-if="confirm?.requireName" class="form-group" style="margin-bottom: 0; margin-top: 12px">
        <label class="form-label">Type <code>{{ inst?.name }}</code> to confirm</label>
        <input v-model="deleteConfirm" class="form-input" :placeholder="inst?.name" autofocus autocomplete="off" />
      </div>
    </ConfirmDialog>
  </div>
  <div v-else class="loading-page"><span class="spinner"></span></div>
</template>

<style scoped>
/* Resource usage section (mirrors the AppDetail overview) */
.section-title { font-size: 13px; font-weight: 600; color: var(--text-secondary); margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.live-tag { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-weight: 500; color: var(--text-muted); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--success-500); animation: live-pulse 1.6s ease-out infinite; }
@keyframes live-pulse {
  0% { box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.5); }
  70% { box-shadow: 0 0 0 5px rgba(34, 197, 94, 0); }
  100% { box-shadow: 0 0 0 0 rgba(34, 197, 94, 0); }
}
.usage-bar { height: 6px; border-radius: 9999px; background: var(--bg-tertiary); overflow: hidden; margin-top: 10px; }
.usage-fill { height: 100%; border-radius: 9999px; transition: width 0.4s ease, background 0.3s ease; }
.usage-ok { background: var(--success-500); }
.usage-warn { background: var(--warning-500); }
.usage-danger { background: var(--danger-500); }
.text-muted { color: var(--text-muted); }
code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; font-family: monospace; }
tr.selected { background: var(--bg-tertiary); }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; }
.sched-field { display: flex; flex-direction: column; gap: 4px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.radio-row { display: flex; align-items: center; gap: 8px; font-size: 14px; margin-top: 6px; }
.detail-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; padding: 16px; }
.detail-grid > div { display: flex; flex-direction: column; gap: 4px; font-size: 14px; }
.detail-label { font-size: 12px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
.danger-card { border-color: var(--danger-200, rgba(220, 38, 38, 0.2)); }
.justify-between { justify-content: space-between; }
.log-view { height: 360px; overflow: auto; white-space: pre-wrap; }
.upgrade-plan { margin-top: 12px; display: flex; flex-direction: column; gap: 8px; }

.app-banner-sub { margin: 4px 0 0; font-size: 13px; opacity: 0.85; }

/* Upgrade phase stepper */
.upgrade-steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
.upgrade-step { display: flex; align-items: center; gap: 10px; padding: 6px 0; font-size: 14px; color: var(--text-muted); }
.upgrade-step-mark { font-size: 18px; line-height: 1; display: inline-flex; }
.upgrade-step.is-done { color: var(--text-primary); }
.upgrade-step.is-done .upgrade-step-mark { color: var(--success-600, #16a34a); }
.upgrade-step.is-current { color: var(--text-primary); font-weight: 600; }
.upgrade-step.is-current .upgrade-step-mark { color: var(--primary-600, #2563eb); }
.upgrade-step.is-todo { opacity: 0.6; }
.mdi-spin { animation: mdi-spin 1s linear infinite; }
@keyframes mdi-spin { from { transform: rotate(0); } to { transform: rotate(360deg); } }
</style>
