<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { databaseApi, backupApi } from '@/api/resources'
import { eventsApi } from '@/api/events'
import { appApi } from '@/api/apps'
import { networkApi } from '@/api/networks'
import { apiErrorMessage } from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import MetadataCard from '@/components/MetadataCard.vue'
import OwnerChip from '@/components/OwnerChip.vue'
import LocationName from '@/components/LocationName.vue'
import ResourceIcon from '@/components/ResourceIcon.vue'
import { fmtSize } from '@/utils/format'
import { engineLogo, engineMdi } from '@/utils/resourceIcon'
import { copyText } from '@/utils/clipboard'
import type { DatabaseInstance, DBStatus, UpgradeProgress, LogicalDatabase, ConnectionInfo, ForwardSession, Backup, BackupSchedule, DatabaseBackupSet, DatabaseBackupSetSchedule, DiscoveredSet, Application, Network, UpgradeOptions, UpgradePlan, StatsSample, AppEvent, DatabaseSize, DatabaseSizeOffer } from '@/api/types'
import AppModal from '@/components/AppModal.vue'
import { relativeTime } from '@/utils/time'

const route = useRoute()
const { t } = useI18n()
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
const sets = ref<DatabaseBackupSet[]>([])
// Recovery points require object storage; without it the action is unavailable
// rather than silently writing somewhere that dies with the host.
const setsS3Ready = ref(false)
// Taking a recovery point is Enterprise; existing ones stay restorable in every edition.
const setsEntitled = ref(false)
const runningSet = ref(false)
const discovered = ref<DiscoveredSet[] | null>(null)
const scanning = ref(false)
const setSchedules = ref<DatabaseBackupSetSchedule[]>([])
const setCron = ref('0 3 * * *')
const setMax = ref(7)
const setRetentionDays = ref(0)
// Which recovery point has its per-database items open. One at a time: the point
// of the list is the sets, not their contents.
const openSet = ref<number | null>(null)
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
type TabKey = 'overview' | 'databases' | 'backups' | 'recovery-points' | 'events' | 'logs' | 'network' | 'settings'
const tabs = computed<{ key: TabKey; label: string }[]>(() => {
  const list: { key: TabKey; label: string }[] = [{ key: 'overview', label: t('db.tab.overview') }]
  if (supportsLogical.value) {
    list.push({ key: 'databases', label: t('nav.data.databases') }, { key: 'backups', label: t('db.backups') }, { key: 'recovery-points', label: t('db.recoveryPoints') })
  }
  list.push({ key: 'events', label: t('nav.workspace.events') }, { key: 'logs', label: t('db.tab.logs') }, { key: 'network', label: t('dashboard.resources.network') }, { key: 'settings', label: t('nav.workspace.settings') })
  return list
})
function tabFromQuery(): TabKey {
  const q = route.query.tab
  const valid: TabKey[] = ['overview', 'databases', 'backups', 'recovery-points', 'events', 'logs', 'network', 'settings']
  return typeof q === 'string' && valid.includes(q as TabKey) ? (q as TabKey) : 'overview'
}
const tab = ref<TabKey>(tabFromQuery())
watch(tab, (t) => {
  router.replace({ query: { ...route.query, tab: t } })
  if (t === 'logs') startLogs()
  else stopLogs()
  if (t === 'events') startEvents()
  else stopEvents()
  if (t === 'recovery-points') loadSets()
})

const EVENTS_PAGE = 50
const events = ref<AppEvent[]>([])
const eventsLoading = ref(false)
const eventsHasMore = ref(false)
const loadingMoreEvents = ref(false)
let eventsES: EventSource | null = null

async function startEvents() {
  if (!wid.value || eventsES) return
  eventsLoading.value = true
  try {
    const first = (await eventsApi.databaseList(wid.value, instId.value, undefined, EVENTS_PAGE)).data.data ?? []
    events.value = first
    eventsHasMore.value = first.length === EVENTS_PAGE
  } catch (e) { notify.apiError(e) }
  finally { eventsLoading.value = false }

  eventsES = new EventSource(eventsApi.databaseStreamUrl(wid.value, instId.value))
  eventsES.onmessage = (ev) => {
    try {
      const payload = JSON.parse(ev.data) as { data?: AppEvent }
      const e = payload.data
      // The bus replays on reconnect, so drop anything already listed.
      if (e && !events.value.some((x) => x.id === e.id)) events.value.unshift(e)
    } catch { /* ignore keep-alives */ }
  }
  eventsES.onerror = () => { eventsES?.close(); eventsES = null }
}
function stopEvents() {
  eventsES?.close()
  eventsES = null
}
async function loadMoreEvents() {
  if (!wid.value || loadingMoreEvents.value || events.value.length === 0) return
  loadingMoreEvents.value = true
  try {
    const oldest = events.value[events.value.length - 1].id
    const older = (await eventsApi.databaseList(wid.value, instId.value, oldest, EVENTS_PAGE)).data.data ?? []
    events.value = events.value.concat(older)
    eventsHasMore.value = older.length === EVENTS_PAGE
  } catch (e) { notify.apiError(e) }
  finally { loadingMoreEvents.value = false }
}

function eventIcon(type: string): string {
  if (type === 'container.died' || type === 'container.oom') return 'mdi-alert-circle-outline'
  if (type === 'container.health') return 'mdi-heart-pulse'
  if (type.startsWith('container')) return 'mdi-cube-outline'
  if (type.startsWith('backup') || type.startsWith('restore')) return 'mdi-backup-restore'
  if (type === 'database.upgraded' || type === 'database.upgrade_failed') return 'mdi-arrow-up-bold-box-outline'
  if (type.startsWith('database')) return 'mdi-database-outline'
  return 'mdi-circle-small'
}

function relTime(ts: string): string {
  const s = Math.round((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.round(h / 24)}d ago`
}

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
onUnmounted(() => { stopLogs(); stopEvents() })

const showResize = ref(false)
const resizing = ref(false)
const resizeForm = ref<{ memory_mb: number | null; cpu_cores: number | null; size: string }>({ memory_mb: null, cpu_cores: null, size: '' })
const sizeOffer = ref<DatabaseSizeOffer>({ sizes: [], bound: false })
function openResize() {
  if (!inst.value) return
  resizeForm.value = {
    memory_mb: inst.value.memory_bytes ? Math.round(inst.value.memory_bytes / 1048576) : null,
    cpu_cores: inst.value.nano_cpus ? inst.value.nano_cpus / 1e9 : null,
    size: inst.value.size_class ?? '',
  }
  showResize.value = true
  if (!wid.value) return
  databaseApi.sizes(wid.value).then((r) => {
    sizeOffer.value = r.data.data ?? { sizes: [], bound: false }
    const offered = sizeOffer.value.sizes.some((s) => s.name === resizeForm.value.size)
    if (sizeOffer.value.bound && !offered) {
      resizeForm.value.size = sizeOffer.value.sizes.find((s) => s.id === sizeOffer.value.default_id)?.name ?? ''
    }
  }).catch(() => { sizeOffer.value = { sizes: [], bound: false } })
}
function sizeLabel(s: DatabaseSize): string {
  return `${s.display_name || s.name} · ${+(s.nano_cpus / 1e9).toFixed(2)} CPU · ${Math.round(s.memory_bytes / 1048576)} MB`
}
async function saveResize() {
  if (!wid.value || !inst.value) return
  resizing.value = true
  try {
    const f = resizeForm.value
    inst.value = (await databaseApi.resize(wid.value, inst.value.id,
      f.size ? 0 : Number(f.memory_mb) || 0, f.size ? 0 : Number(f.cpu_cores) || 0, f.size || undefined)).data.data
    notify.success(t('db.toast.resourcesSavedTheInstanceRestarts'))
    showResize.value = false
  } catch (e) {
    notify.apiError(e)
  } finally {
    resizing.value = false
  }
}
function fmtLimits(i: DatabaseInstance): string {
  const cpu = i.nano_cpus ? `${+(i.nano_cpus / 1e9).toFixed(2)} CPU` : 'unlimited CPU'
  const memory = i.memory_bytes ? fmtSize(i.memory_bytes) : 'unlimited memory'
  return i.size_class ? `${i.size_class} · ${cpu} · ${memory}` : `${cpu} · ${memory}`
}

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
  if (tab.value === 'recovery-points') loadSets() // the tab may be the one restored from the URL
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
    notify.success(t('db.toast.sizesRefreshed'))
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
    notify.success(t('db.toast.networkConnected'))
  } catch (e) { notify.apiError(e) }
  finally { netBusy.value = false }
}
async function detachNetwork(n: Network) {
  if (!wid.value || netBusy.value) return
  netBusy.value = true
  try {
    inst.value = (await databaseApi.detachNetwork(wid.value, instId.value, n.id)).data.data
    notify.success(t('db.toast.disconnectedFrom', { name: n.name }))
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
    notify.success(t('db.toast.forwardOpenedConnectYourClient'))
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
    kind: 'remove-db', title: t('confirm.title.databaseDetail.deleteDatabase'), confirmLabel: t('action.delete'), variant: 'danger',
    message: t('confirm.message.databaseDetail.deleteDatabaseAndIts', { name: d.name }),
    run: () => removeDb(d),
  }
}
async function removeDb(d: LogicalDatabase) {
  if (!wid.value) return
  try {
    await databaseApi.removeDatabase(wid.value, instId.value, d.id)
    if (selected.value?.id === d.id) selected.value = null
    notify.success(t('db.toast.databaseDeleted'))
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
// Recovery points span the whole instance, so they load with the tab rather than
// with the selected database.
async function loadSets() {
  if (!wid.value) return
  try {
    const res = (await backupApi.sets(wid.value, instId.value)).data.data
    sets.value = res?.sets ?? []
    setsS3Ready.value = res?.s3_configured ?? false
    setsEntitled.value = res?.entitled ?? false
    setSchedules.value = (await backupApi.setSchedules(wid.value, instId.value)).data.data ?? []
  } catch (e) { notify.apiError(e) }
}
async function runSet() {
  if (!wid.value) return
  runningSet.value = true
  try {
    const set = (await backupApi.runSet(wid.value, instId.value, backupComment.value.trim())).data.data
    notify[set.status === 'completed' ? 'success' : 'error'](
      set.status === 'completed'
        ? `Recovery point ${set.ref} created`
        : `Recovery point failed: ${set.error ?? 'see the instance events'}`,
    )
    backupComment.value = ''
    loadSets()
    if (selected.value) loadBackups()
  } catch (e) { notify.apiError(e) }
  finally { runningSet.value = false }
}
// Reads the bucket rather than this workspace's history, so it finds recovery
// points taken by an install that is no longer here.
async function scanBucket() {
  if (!wid.value) return
  scanning.value = true
  try {
    discovered.value = (await backupApi.discoverSets(wid.value)).data.data ?? []
    if (discovered.value.length === 0) notify.info(t('notify.databaseDetail.noRecoveryPointsFoundIn'))
  } catch (e) { notify.apiError(e) }
  finally { scanning.value = false }
}

async function verifySet(set: DatabaseBackupSet) {
  if (!wid.value) return
  try {
    const res = (await backupApi.verifySet(wid.value, instId.value, set.id)).data.data
    notify[res.ok ? 'success' : 'error'](
      res.ok ? `${set.ref} verified — ${res.checked} artifact(s) intact` : `${set.ref}: ${res.error}`,
    )
    loadSets()
  } catch (e) { notify.apiError(e) }
}

async function adoptSet(d: DiscoveredSet) {
  if (!wid.value) return
  try {
    const res = (await backupApi.adoptSet(wid.value, instId.value, d.ref)).data.data
    if (res.already_known) notify.info(t('notify.databaseDetail.wasAlreadyInThisWorkspace', { ref: d.ref }))
    else notify.success(t('db.toast.adoptedDatabaseS', { ref: d.ref, adopted: res.adopted }))
    if (res.skipped?.length) {
      notify.error(t('notify.databaseDetail.artifactSSkippedNoDatabase', { count: res.skipped.length, join: res.skipped.map((k) => k.database).join(', ') }))
    }
    loadSets()
  } catch (e) { notify.apiError(e) }
}
function askRestoreSet(set: DatabaseBackupSet) {
  confirm.value = {
    kind: 'restore-backup-set', title: t('confirm.title.databaseDetail.restoreRecoveryPoint'), confirmLabel: t('action.restore'), variant: 'danger',
    message: t('confirm.message.databaseDetail.restoreAllDatabaseS', { length: set.items?.length ?? 0, ref: set.ref }),
    run: () => restoreSet(set),
  }
}
async function restoreSet(set: DatabaseBackupSet) {
  if (!wid.value) return
  try {
    const res = (await backupApi.restoreSet(wid.value, instId.value, set.id)).data.data
    if (res.failed?.length) {
      notify.error(t('notify.databaseDetail.restoredFailed', { count: res.restored.length, count2: res.failed.length, join: res.failed.join('; ') }))
    } else {
      notify.success(t('db.toast.restoredDatabaseSFrom', { count: res.restored.length, ref: set.ref }))
    }
  } catch (e) { notify.apiError(e) }
}

async function addSetSchedule() {
  if (!wid.value) return
  try {
    await backupApi.createSetSchedule(wid.value, instId.value, setCron.value, setMax.value, setRetentionDays.value)
    notify.success(t('db.toast.scheduleCreated'))
    loadSets()
  } catch (e) { notify.apiError(e) }
}
async function removeSetSchedule(id: number) {
  if (!wid.value) return
  try {
    await backupApi.deleteSetSchedule(wid.value, instId.value, id)
    notify.success(t('db.toast.scheduleDeleted'))
    loadSets()
  } catch (e) { notify.apiError(e) }
}

// The kit is what someone reads when Miabi is not there to read it for them, so it
// is a file to keep, not a page to visit.
async function downloadRecoveryKit(set: DatabaseBackupSet) {
  if (!wid.value) return
  try {
    const res = await backupApi.recoveryKit(wid.value, instId.value, set.id)
    const url = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = url
    a.download = `${set.ref}-recovery-kit.md`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) { notify.apiError(e) }
}

function askRemoveSet(set: DatabaseBackupSet) {
  confirm.value = {
    kind: 'remove-backup-set', title: t('confirm.title.databaseDetail.deleteRecoveryPoint'), confirmLabel: t('action.delete'), variant: 'danger',
    message: t('confirm.message.databaseDetail.deleteItsDatabaseBackup', { ref: set.ref, length: set.items?.length ?? 0 }),
    run: () => removeSet(set),
  }
}
async function removeSet(set: DatabaseBackupSet) {
  if (!wid.value) return
  try {
    await backupApi.removeSet(wid.value, instId.value, set.id)
    notify.success(t('db.toast.recoveryPointDeleted'))
    loadSets()
    if (selected.value) loadBackups()
  } catch (e) { notify.apiError(e) }
}

// Annotating at run time is the common case ("before the v1.3.0 rollout"), so the
// note sits next to the button rather than behind a dialog.
const backupComment = ref('')
async function runBackup() {
  if (!wid.value || !selected.value) return
  running.value = true
  try {
    const b = (await backupApi.run(wid.value, instId.value, selected.value.id, backupComment.value.trim())).data.data
    notify[b.status === 'completed' ? 'success' : 'error'](`Backup #${b.number} ${b.status}`)
    backupComment.value = ''
    loadBackups()
  } catch (e) { notify.apiError(e) }
  finally { running.value = false }
}

// --- Backup annotations: a note and a retention pin, both editable after the run.
// A scheduled or pre-upgrade backup has nobody present when it happens, and what a
// backup was for is often only clear once the change it guarded went wrong.
const editingNote = ref<number | null>(null)
const noteDraft = ref('')

function startNote(b: Backup) {
  editingNote.value = b.id
  noteDraft.value = b.comment ?? ''
}
function cancelNote() {
  editingNote.value = null
  noteDraft.value = ''
}
async function saveNote(b: Backup) {
  if (!wid.value || !selected.value) return
  const comment = noteDraft.value.trim()
  cancelNote()
  if (comment === (b.comment ?? '')) return
  try {
    b.comment = comment
    await backupApi.update(wid.value, instId.value, selected.value.id, b.id, { comment })
  } catch (e) {
    notify.apiError(e)
    loadBackups()
  }
}
async function togglePin(b: Backup) {
  if (!wid.value || !selected.value) return
  const pinned = !b.pinned
  try {
    b.pinned = pinned
    await backupApi.update(wid.value, instId.value, selected.value.id, b.id, { pinned })
    notify.success(pinned ? `Backup #${b.number} pinned — retention will skip it` : `Backup #${b.number} unpinned`)
  } catch (e) {
    b.pinned = !pinned
    notify.apiError(e)
  }
}
// --- Restore dialog (existing backup or uploaded file; normal or force) ---
const restoreModal = ref<{ backupId: number | null; number: number | null; comment: string; version: string; encrypted: boolean } | null>(null)
const restoreMethod = ref<'normal' | 'force'>('normal')
const restoreFile = ref<File | null>(null)
const restoring = ref(false)
function openRestore(b: Backup | null) {
  restoreModal.value = { backupId: b?.id ?? null, number: b?.number ?? null, comment: b?.comment ?? '', version: b?.version ?? '', encrypted: b?.encrypted ?? false }
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
      if (!restoreFile.value) { notify.error(t('notify.databaseDetail.chooseADumpFile')); restoring.value = false; return }
      await backupApi.restoreFile(wid.value, instId.value, selected.value.id, restoreFile.value, restoreMethod.value)
    }
    notify.success(t('db.toast.databaseRestored'))
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
    a.download = b.filename || `backup-${b.number}.sql.gz`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  } catch (e) { notify.apiError(e, 'Download failed') }
}
function askRemoveBackup(b: Backup) {
  confirm.value = {
    kind: 'remove-backup', title: t('confirm.title.databaseDetail.deleteBackup'), confirmLabel: t('action.delete'), variant: 'danger',
    message: t('confirm.message.databaseDetail.deleteBackupTheArtifact', { number: b.number }),
    run: () => removeBackup(b),
  }
}
async function removeBackup(b: Backup) {
  if (!wid.value || !selected.value) return
  try {
    await backupApi.remove(wid.value, instId.value, selected.value.id, b.id)
    notify.success(t('db.toast.backupDeleted'))
    loadBackups()
  } catch (e) { notify.apiError(e) }
}
async function addSchedule() {
  if (!wid.value || !selected.value) return
  try {
    await backupApi.createSchedule(wid.value, instId.value, selected.value.id, cron.value, maxBackups.value, retentionDays.value)
    notify.success(t('db.toast.scheduleCreated'))
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
    // Three keys, not a participle interpolated into one: it agrees with the subject in French.
    notify.success(t(action === 'stop' ? 'db.toast.stopped' : action === 'start' ? 'db.toast.started' : 'db.toast.restarted'))
    load()
  } catch (e) { notify.apiError(e) }
  finally { lifecycleBusy.value = false }
}

// --- Confirmation dialog (delete / stop / restart) ---
type ConfirmKind = 'delete' | 'stop' | 'restart' | 'remove-db' | 'remove-backup' | 'remove-backup-set' | 'restore-backup-set' | 'upgrade'
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
    kind: 'stop', title: t('confirm.title.databaseDetail.stopDatabase'), confirmLabel: t('action.stop'), variant: 'primary',
    message: t('confirm.message.databaseDetail.appsUsingThisDatabase'),
    run: () => lifecycle('stop'),
  }
}
function askRestart() {
  confirm.value = {
    kind: 'restart', title: t('confirm.title.databaseDetail.restartDatabase'), confirmLabel: t('action.restart'), variant: 'primary',
    message: t('confirm.message.databaseDetail.theContainerWillBe', { engine: inst.value?.engine ?? 'database' }),
    run: () => lifecycle('restart'),
  }
}
function askDelete() {
  deleteConfirm.value = ''
  confirm.value = {
    kind: 'delete', title: t('confirm.title.databaseDetail.deleteDatabaseInstance'), confirmLabel: t('action.delete'), variant: 'danger',
    requireName: true,
    message: t('confirm.message.databaseDetail.deleteAllItsDatabases', { name: inst.value?.name }),
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
    if (info) connModal.value = { title: t('confirm.title.databaseDetail.admin', { name: inst.value?.name }), info }
  } catch (e) { notify.apiError(e, 'Only admins can reveal credentials') }
}
async function removeInstance() {
  if (!wid.value) return
  try { await databaseApi.remove(wid.value, instId.value); notify.success(t('db.toast.instanceDeleted')); router.push('/databases') }
  catch (e) { notify.apiError(e) }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success(t('db.toast.copied'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
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
    kind: 'upgrade', title: t('confirm.title.databaseDetail.upgradeDatabaseVersion'), confirmLabel: t('action.upgrade'),
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
    notify.success(t('db.toast.upgradeStarted'))
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
      notify.error(t('notify.databaseDetail.upgradeFailed', { error: s.upgrade.error ?? 'unknown error' }))
      void load()
    }
    return
  }

  if (prev === s.status) return

  if (prev === 'upgrading' && s.status === 'running') {
    notify.success(t('db.toast.upgradeToCompleted', { to_version: lastUpgrade.value?.to_version ?? 'the new version' }))
    void load()
  } else if (prev === 'provisioning' && s.status === 'running') {
    notify.success(t('db.toast.databaseIsReady'))
    void load()
  } else if (s.status === 'failed') {
    notify.error(t('notify.databaseDetail.databaseFailedToProvision'))
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
          <span class="mdi mdi-arrow-left"></span>{{ $t('nav.data.databases') }}</button>
        <div class="flex items-center gap-3" style="margin-top: 8px">
          <ResourceIcon :src="engineLogo(inst.engine)" :mdi="engineMdi(inst.engine)" :name="inst.name" :size="44" />
          <div>
            <h1>{{ inst.display_name || inst.name }}</h1>
            <div class="text-muted text-sm">
              <span class="mdi mdi-docker"></span>
              {{ inst.engine }} {{ inst.version }}
              <template v-if="inst.server_name"> · <span class="mdi mdi-server-network"></span> {{ inst.server_name }}</template>
              <LocationName :cluster-id="inst.cluster_id" />
              · <code :title="$t('db.inNetworkHint', { node: inst.server_name || $t('db.thisNode') })">{{ inst.host }}:{{ inst.port }}</code>
            </div>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-3 database-header-actions">
        <span class="badge badge-dot" :class="badge(inst.status)">{{ inst.status }}</span>
        <button class="btn btn-secondary btn-sm" @click="revealInstance"><span class="mdi mdi-eye-outline"></span>{{ $t('db.adminConnection') }}</button>
        <button v-if="ws.isWorkspaceAdmin" class="btn btn-secondary btn-sm" :disabled="inst.status !== 'running' || forwardBusy" :title="$t('db.openATemporaryExternalConnection')" @click="openForward"><span class="mdi mdi-lan-connect"></span>{{ $t('db.connectExternally') }}</button>
        <button v-if="ws.canEdit && inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="syncingSizes" :title="$t('db.refreshOnDiskSizeInfo')" @click="syncSizes"><span class="mdi mdi-sync" :class="{ 'mdi-spin': syncingSizes }"></span>{{ $t('db.syncSizes') }}</button>
        <template v-if="ws.canEdit">
          <button v-if="inst.status === 'stopped' || inst.status === 'failed'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="lifecycle('start')"><span class="mdi mdi-play"></span>{{ $t('db.start') }}</button>
          <button v-if="inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="askRestart"><span class="mdi mdi-restart"></span>{{ $t('action.restart') }}</button>
          <button v-if="inst.status === 'running'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" @click="askStop"><span class="mdi mdi-stop"></span>{{ $t('action.stop') }}</button>
          <button v-if="inst.status === 'running' || inst.status === 'stopped'" class="btn btn-secondary btn-sm" :disabled="lifecycleBusy" :title="$t('db.changeCpuAndMemoryLimits')" @click="openResize"><span class="mdi mdi-tune-variant"></span>{{ $t('dashboard.resources.title') }}</button>
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
        <div class="card-header"><h2>{{ $t('db.details') }}</h2></div>
        <div class="detail-grid">
          <div><span class="detail-label">{{ $t('dashboard.col.status') }}</span><span class="badge badge-dot" :class="badge(inst.status)">{{ inst.status }}</span></div>
          <div><span class="detail-label">{{ $t('db.owner') }}</span><OwnerChip :metadata="inst.metadata" /></div>
          <div><span class="detail-label">{{ $t('databases.col.engine') }}</span>{{ inst.engine }} {{ inst.version }}</div>
          <div><span class="detail-label">{{ $t('db.inNetworkAddress') }}</span><code>{{ inst.host }}:{{ inst.port }}</code></div>
          <div v-if="inst.server_name"><span class="detail-label">{{ $t('dashboard.col.node') }}</span>{{ inst.server_name }}</div>
          <div v-if="inst.volume_name"><span class="detail-label">{{ $t('db.dataVolume') }}</span><code>{{ inst.volume_name }}</code><template v-if="inst.mount_path"> → <code>{{ inst.mount_path }}</code></template></div>
          <div v-if="inst.storage_class"><span class="detail-label">{{ $t('volumes.storageClass') }}</span>{{ inst.storage_class }}</div>
          <div v-if="inst.size_synced_at"><span class="detail-label">{{ $t('db.onDiskSize') }}</span>{{ fmtBytes(inst.size_bytes) }}</div>
          <div><span class="detail-label">{{ $t('db.limits') }}</span>{{ fmtLimits(inst) }}</div>
        </div>
      </div>

      <h2 class="section-title">{{ $t('db.resourceUsage') }}<span v-if="metrics" class="live-tag"><span class="live-dot"></span>{{ $t('dashboard.analytics.live') }}</span>
      </h2>
      <div v-if="metrics" class="stats-grid mb-4">
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">CPU</span><span class="stat-icon stat-icon-primary"><span class="mdi mdi-chip"></span></span></div>
          <div class="stat-value">{{ metrics.cpu_percent.toFixed(1) }}%</div>
          <div class="usage-bar"><div class="usage-fill" :class="usageTone(metrics.cpu_percent)" :style="{ width: Math.min(100, metrics.cpu_percent) + '%' }"></div></div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('dashboard.resources.memory') }}</span><span class="stat-icon stat-icon-info"><span class="mdi mdi-memory"></span></span></div>
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
          <p>{{ $t('db.noLiveMetricsTheDatabase') }}</p>
        </div>
      </div>

      <MetadataCard :metadata="inst.metadata" class="mb-4" />

      <MetadataCard :metadata="inst.annotations" :title="$t('db.annotations')" :reserved="false" class="mb-4" />

      <!-- Live external forwards (admin only) -->
      <div v-if="ws.isWorkspaceAdmin && forwards.length" class="card">
        <div class="card-header">
          <h2>{{ $t('db.externalForwards') }}</h2>
          <span class="text-muted text-sm">{{ $t('db.temporarySourceIpLockedConnections') }}</span>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('db.endpoint') }}</th><th>{{ $t('db.expires') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="f in forwards" :key="f.id">
                <td class="cell-title" style="font-family: monospace">
                  {{ f.host }}:{{ f.port }}
                  <button class="btn-icon btn-icon-muted" :title="$t('action.copy')" :aria-label="$t('action.copy')" @click="copy(`${f.host}:${f.port}`)"><span class="mdi mdi-content-copy"></span></button>
                </td>
                <td class="cell-sub">{{ expiresLabel(f.expires_at) }}</td>
                <td class="text-right">
                  <button class="btn btn-sm btn-secondary" @click="closeForward(f.id)">{{ $t('shell.close') }}</button>
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
          <h2>{{ $t('nav.data.databases') }}</h2>
          <button v-if="ws.canEdit && canCreateLogical" class="btn btn-sm btn-primary" :disabled="inst.status !== 'running'" @click="openCreateDb">
            <span class="mdi mdi-plus"></span>{{ $t('dashboard.quick.newDatabase.label') }}</button>
        </div>
        <div v-if="inst.status !== 'running'" class="card-body text-muted text-sm">Instance is {{ inst.status }} — databases can be created once it is running.</div>
        <div v-else-if="databases.length === 0" class="empty-state">
          <span class="mdi mdi-database-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('db.noDatabasesYetCreateOne') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('databases.col.database') }}</th><th>{{ $t('db.user') }}</th><th>{{ $t('db.app') }}</th><th>{{ $t('databases.col.size') }}</th><th></th></tr></thead>
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
                  <button class="btn-icon btn-icon-muted" :title="$t('db.backups')" :aria-label="$t('db.backups')" @click="viewBackups(d)"><span class="mdi mdi-backup-restore"></span></button>
                  <button class="btn-icon btn-icon-muted" :title="$t('db.revealConnection')" :aria-label="$t('db.revealConnection')" @click="revealDb(d)"><span class="mdi mdi-key-outline"></span></button>
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="askRemoveDb(d)"><span class="mdi mdi-delete-outline"></span></button>
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
          <h2>{{ $t('db.backups') }}</h2>
          <label class="flex items-center gap-2">
            <span class="text-muted text-sm">{{ $t('databases.col.database') }}</span>
            <select class="form-select" style="min-width: 180px" :value="selected?.id ?? ''" @change="onSelectDb">
              <option value="" disabled>{{ $t('db.selectADatabase') }}</option>
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
              <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" @click="openRestore(null)"><span class="mdi mdi-upload-outline"></span>{{ $t('db.restoreFromFile') }}</button>
              <input
                v-if="ws.canEdit"
                v-model="backupComment"
                class="form-input backup-note-input"
                maxlength="200"
                :placeholder="$t('db.noteOptionalEGBefore')"
                :disabled="running"
                @keyup.enter="runBackup"
              />
              <button v-if="ws.canEdit" class="btn btn-sm btn-primary" :disabled="running" @click="runBackup">{{ running ? 'Backing up…' : 'Run backup' }}</button>
            </div>
          </div>
          <div v-if="backups.length === 0" class="empty-state">
            <span class="mdi mdi-backup-restore" style="font-size: 36px; color: var(--text-muted)"></span>
            <p>{{ $t('db.noBackupsYet') }}</p>
          </div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>{{ $t('db.backup') }}</th><th>{{ $t('dashboard.col.status') }}</th><th></th></tr></thead>
              <tbody>
                <tr v-for="b in backups" :key="b.id">
                  <td>
                    <span class="cell-title">
                      #{{ b.number }}
                      <span v-if="b.pinned" class="badge badge-neutral" style="margin-left: 4px">{{ $t('db.pinned') }}</span>
                      <span
                        v-if="b.encrypted"
                        class="badge badge-success"
                        style="margin-left: 4px"
                        :title="$t('db.encryptedWithTheWorkspaceBackup')"
                      >
                        <span class="mdi mdi-lock-outline"></span>{{ $t('db.encrypted') }}</span>
                    </span>
                    <div class="cell-sub">
                      {{ b.trigger }} · {{ b.destination }}<template v-if="b.version"> · {{ b.engine }} {{ b.version }}</template><template v-if="b.filename"> · {{ b.filename }}</template>
                    </div>
                    <div v-if="editingNote === b.id" class="backup-note-edit">
                      <input
                        v-model="noteDraft"
                        class="form-input"
                        maxlength="200"
                        :placeholder="$t('db.whatWasThisBackupFor')"
                        autofocus
                        @keyup.enter="saveNote(b)"
                        @keyup.esc="cancelNote"
                      />
                      <button class="btn btn-sm btn-primary" @click="saveNote(b)">{{ $t('db.save') }}</button>
                      <button class="btn btn-sm btn-secondary" @click="cancelNote">{{ $t('action.cancel') }}</button>
                    </div>
                    <button
                      v-else-if="ws.canEdit"
                      class="backup-note"
                      :class="{ empty: !b.comment }"
                      :title="b.comment ? $t('db.editNote') : $t('db.addNote')"
                      @click="startNote(b)"
                    >
                      <span class="mdi mdi-note-edit-outline"></span>
                      <span>{{ b.comment || 'Add a note' }}</span>
                    </button>
                    <div v-else-if="b.comment" class="backup-note static">{{ b.comment }}</div>
                  </td>
                  <td><span class="badge badge-dot" :class="badge(b.status)">{{ b.status }}</span></td>
                  <td class="text-right table-actions">
                    <button
                      v-if="ws.canEdit"
                      class="btn-icon btn-icon-muted"
                      :class="{ 'backup-pinned': b.pinned }"
                      :title="b.pinned ? $t('db.pinnedHint') : $t('db.pinHint')"
                      :aria-label="b.pinned ? $t('db.unpinBackup') : $t('db.pinBackup')"
                      @click="togglePin(b)"
                    >
                      <span class="mdi" :class="b.pinned ? 'mdi-pin' : 'mdi-pin-outline'"></span>
                    </button>
                    <button v-if="b.status === 'completed' && b.destination === 'local' && ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('db.download')" :aria-label="$t('db.download')" @click="downloadBackup(b)"><span class="mdi mdi-download-outline"></span></button>
                    <button v-if="b.status === 'completed' && ws.canEdit" class="btn btn-sm btn-secondary" @click="openRestore(b)">{{ $t('action.restore') }}</button>
                    <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="askRemoveBackup(b)"><span class="mdi mdi-delete-outline"></span></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </div>

      <div v-if="selected" class="card">
        <div class="card-header"><h2><i18n-t keypath="db.backupSchedulesFor" tag="span"><template #name><code>{{ selected.name }}</code></template></i18n-t></h2></div>
        <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <form class="flex items-center gap-2" style="flex-wrap: wrap" @submit.prevent="addSchedule">
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.cronUtc') }}</span>
              <input v-model="cron" class="form-input" placeholder="0 3 * * *" style="max-width: 160px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.keepLast0All') }}</span>
              <input v-model.number="maxBackups" type="number" min="0" class="form-input" style="max-width: 120px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.maxAgeDays0') }}</span>
              <input v-model.number="retentionDays" type="number" min="0" class="form-input" style="max-width: 130px" />
            </label>
            <button class="btn btn-primary" style="align-self: flex-end">{{ $t('db.addSchedule') }}</button>
          </form>
          <p class="form-hint" style="margin-top: 8px">{{ $t('db.pinnedBackupsAreNeverDeleted') }}</p>
        </div>
        <div v-if="schedules.length === 0" class="empty-state"><p>{{ $t('db.noSchedules') }}</p></div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('db.schedule') }}</th><th>{{ $t('db.destination') }}</th><th>{{ $t('db.retention') }}</th><th></th></tr></thead>
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
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="delSchedule(s.id)"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- RECOVERY POINTS (SQL engines; the whole instance as one set) -->
    <template v-else-if="tab === 'recovery-points'">
      <div class="card">
        <div class="card-header">
          <h2>{{ $t('db.recoveryPoints') }}<span v-if="!setsEntitled" class="badge badge-muted"><span class="mdi mdi-lock-outline"></span>{{ $t('adminNav.enterprise.title') }}</span>
          </h2>
          <div class="flex items-center gap-2">
            <button
              v-if="ws.canEdit && setsS3Ready"
              class="btn btn-sm btn-secondary"
              :disabled="scanning"
              :title="$t('db.listTheRecoveryPointsIn')"
              @click="scanBucket"
            >
              {{ scanning ? 'Scanning…' : 'Scan bucket' }}
            </button>
            <button
              v-if="ws.canEdit"
              class="btn btn-sm btn-primary"
              :disabled="runningSet || !databases.length || !setsS3Ready || !setsEntitled"
              :title="setsEntitled ? (setsS3Ready ? '' : $t('db.configureS3First')) : $t('db.setsNeedEnterprise')"
              @click="runSet"
            >
              {{ runningSet ? 'Backing up…' : 'Back up all databases' }}
            </button>
          </div>
        </div>
        <div v-if="!setsS3Ready" class="empty-state">
          <span class="mdi mdi-cloud-off-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p><i18n-t keypath="db.recoveryPointsStored" tag="span"><template #settings><strong>{{ $t('db.workspaceSettingsBackups') }}</strong></template></i18n-t></p>
        </div>
        <div v-else-if="sets.length === 0" class="empty-state">
          <span class="mdi mdi-database-lock-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p v-if="!setsEntitled">{{ $t('db.recoveryPointsBackUpEvery') }}</p>
          <p v-else>{{ $t('db.noRecoveryPointsYetOne') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('db.recoveryPoint') }}</th><th>{{ $t('nav.data.databases') }}</th><th>{{ $t('dashboard.col.status') }}</th><th></th></tr></thead>
            <tbody>
              <template v-for="s in sets" :key="s.id">
                <tr>
                  <td>
                    <span class="cell-title">
                      <code>{{ s.ref }}</code>
                      <span
                        v-if="s.encrypted"
                        class="badge badge-success"
                        style="margin-left: 6px"
                        :title="$t('db.everyArtifactInThisSet')"
                      >
                        <span class="mdi mdi-lock-outline"></span>{{ $t('db.encrypted') }}</span>
                    </span>
                    <div class="cell-sub">
                      {{ s.trigger }} · {{ s.destination }}<template v-if="s.version"> · {{ s.engine }} {{ s.version }}</template>
                      <template v-if="s.size_bytes"> · {{ fmtBytes(s.size_bytes) }}</template>
                    </div>
                    <div class="cell-sub">
                      <template v-if="s.verify_status === 'ok'">
                        <span class="mdi mdi-shield-check-outline"></span> verified {{ relativeTime(s.verified_at) }}
                      </template>
                      <template v-else-if="s.verify_status === 'failed'">
                        <span class="text-danger"><span class="mdi mdi-shield-alert-outline"></span> failed verification: {{ s.verify_error }}</span>
                      </template>
                      <template v-else>{{ $t('db.neverVerified') }}</template>
                    </div>
                    <div v-if="s.error" class="cell-sub text-danger">{{ s.error }}</div>
                  </td>
                  <td>
                    <button class="btn btn-sm btn-secondary" @click="openSet = openSet === s.id ? null : s.id">
                      {{ s.items?.length ?? 0 }}
                      <span class="mdi" :class="openSet === s.id ? 'mdi-chevron-up' : 'mdi-chevron-down'"></span>
                    </button>
                  </td>
                  <td><span class="badge badge-dot" :class="badge(s.status)">{{ s.status }}</span></td>
                  <td class="text-right table-actions">
                    <button
                      v-if="ws.canEdit"
                      class="btn-icon btn-icon-muted"
                      :title="$t('db.checkThisRecoveryPointAgainst')"
                      :aria-label="$t('db.verifyRecoveryPoint')"
                      @click="verifySet(s)"
                    >
                      <span class="mdi mdi-shield-search"></span>
                    </button>
                    <button
                      v-if="ws.canEdit"
                      class="btn-icon btn-icon-muted"
                      :title="$t('db.downloadRecoveryKitHowTo')"
                      :aria-label="$t('db.downloadRecoveryKit')"
                      @click="downloadRecoveryKit(s)"
                    >
                      <span class="mdi mdi-lifebuoy"></span>
                    </button>
                    <button
                      v-if="ws.canEdit && s.status === 'completed'"
                      class="btn btn-sm btn-secondary"
                      :title="$t('db.restoreEveryDatabaseInThis')"
                      @click="askRestoreSet(s)"
                    >{{ $t('action.restore') }}</button>
                    <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('confirm.title.databaseDetail.deleteRecoveryPoint')" @click="askRemoveSet(s)">
                      <span class="mdi mdi-delete-outline"></span>
                    </button>
                  </td>
                </tr>
                <tr v-if="openSet === s.id">
                  <td colspan="4" style="padding-top: 0">
                    <ul class="set-items">
                      <li v-for="it in s.items ?? []" :key="it.id">
                        <span class="badge badge-dot" :class="badge(it.status)">{{ it.status }}</span>
                        <code>{{ it.filename || '—' }}</code>
                        <span class="text-muted text-sm">#{{ it.number }}<template v-if="it.size_bytes"> · {{ fmtBytes(it.size_bytes) }}</template></span>
                        <span v-if="it.error" class="text-danger text-sm">{{ it.error }}</span>
                      </li>
                    </ul>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
        <div v-if="discovered" class="card-body" style="border-top: 1px solid var(--border-primary)">
          <div class="flex items-center gap-2" style="margin-bottom: 10px">
            <h3 style="margin: 0; font-size: 0.95rem">{{ $t('db.inTheBucket') }}</h3>
            <span class="text-muted text-sm">{{ discovered.length }} found</span>
            <button class="btn-icon btn-icon-muted" :title="$t('db.hide')" :aria-label="$t('db.hideBucketScan')" @click="discovered = null">
              <span class="mdi mdi-close"></span>
            </button>
          </div>
          <p v-if="discovered.length === 0" class="form-hint">{{ $t('db.nothingUnderThisWorkspaceS') }}</p>
          <ul v-else class="set-items">
            <li v-for="d in discovered" :key="d.ref">
              <code>{{ d.ref }}</code>
              <span v-if="d.known" class="badge badge-neutral">{{ $t('db.known') }}</span>
              <span v-else class="badge badge-info">{{ $t('db.notInThisInstall') }}</span>
              <span v-if="d.encrypted" class="badge badge-success"><span class="mdi mdi-lock-outline"></span>{{ $t('db.encrypted') }}</span>
              <span class="text-muted text-sm">
                {{ d.engine }}<template v-if="d.version"> {{ d.version }}</template> ·
                {{ d.artifacts.length }} database(s)<template v-if="d.size_bytes"> · {{ fmtBytes(d.size_bytes) }}</template>
              </span>
              <span v-if="d.reason" class="text-warning text-sm">{{ d.reason }}</span>
              <span v-else-if="d.openable" class="text-muted text-sm">{{ $t('db.readyToRestore') }}</span>
              <button
                v-if="ws.canEdit && !d.known"
                class="btn btn-sm btn-secondary"
                :disabled="!setsEntitled"
                :title="setsEntitled ? $t('db.adoptHint') : $t('db.adoptNeedsEnterprise')"
                @click="adoptSet(d)"
              >{{ $t('db.adopt') }}</button>
            </li>
          </ul>
        </div>

        <div v-if="setsS3Ready" class="card-body" style="border-top: 1px solid var(--border-primary)">
          <form v-if="ws.canEdit && (setsEntitled || setSchedules.length)" class="flex items-center gap-2" style="flex-wrap: wrap" @submit.prevent="addSetSchedule">
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.cronUtc') }}</span>
              <input v-model="setCron" class="form-input" placeholder="0 3 * * *" style="max-width: 160px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.keepLast0All') }}</span>
              <input v-model.number="setMax" type="number" min="0" class="form-input" style="max-width: 120px" />
            </label>
            <label class="sched-field">
              <span class="text-muted text-sm">{{ $t('db.maxAgeDays0') }}</span>
              <input v-model.number="setRetentionDays" type="number" min="0" class="form-input" style="max-width: 130px" />
            </label>
            <button class="btn btn-primary" style="align-self: flex-end" :disabled="!setsEntitled">{{ $t('db.addSchedule') }}</button>
          </form>
          <p v-if="setSchedules.length === 0" class="form-hint" style="margin-top: 8px">{{ $t('db.noScheduleYetRetentionOnly') }}</p>
          <ul v-else class="set-items" style="margin-top: 12px">
            <li v-for="sc in setSchedules" :key="sc.id">
              <code>{{ sc.cron }}</code>
              <span class="text-muted text-sm">
                keep {{ sc.max_sets || 'all' }}<template v-if="sc.retention_days"> · max {{ sc.retention_days }}d</template>
                <template v-if="sc.last_run_at"> · last {{ relativeTime(sc.last_run_at) }}</template>
              </span>
              <span v-if="!sc.enabled" class="badge badge-neutral">{{ $t('db.disabled') }}</span>
              <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('db.deleteSchedule')" :aria-label="$t('db.deleteSchedule')" @click="removeSetSchedule(sc.id)">
                <span class="mdi mdi-delete-outline"></span>
              </button>
            </li>
          </ul>
        </div>
      </div>
    </template>

    <!-- LOGS -->
    <div v-else-if="tab === 'events'" class="card">
      <div class="card-header">
        <h2>{{ $t('nav.workspace.events') }}</h2>
        <span class="live-dot" :title="$t('dashboard.resources.live')"></span>
      </div>
      <div v-if="eventsLoading && events.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="events.length === 0" class="empty-state">
        <span class="mdi mdi-timeline-text-outline" style="font-size: 40px; color: var(--text-muted)"></span>
        <h3>{{ $t('db.noEventsYet') }}</h3>
        <p>{{ $t('db.provisioningStartStopUpgradesCrashes') }}</p>
      </div>
      <template v-else>
        <ul class="timeline">
          <li v-for="e in events" :key="e.id" class="event">
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

    <div v-else-if="tab === 'logs'" class="card">
      <div class="card-header">
        <h2>{{ $t('db.containerLogs') }}</h2>
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
          <h2>{{ $t('nav.networking.networks') }}</h2>
          <span class="text-muted text-sm">{{ $t('db.workspaceNetworksThisDatabaseIs') }}</span>
        </div>
        <div v-if="attachedNets.length === 0" class="card-body text-muted text-sm">{{ $t('db.notConnectedToAnyNetwork') }}</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('dashboard.resources.network') }}</th><th>{{ $t('db.dockerName') }}</th><th>{{ $t('db.driver') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">{{ n.name }}</td>
                <td class="cell-sub" style="font-family: monospace">{{ n.docker_name }}</td>
                <td class="cell-sub">{{ n.driver }}<span v-if="n.internal"> · {{ $t('db.internalNetwork') }}</span></td>
                <td class="text-right"><span v-if="n.is_default" class="badge badge-info">{{ $t('db.default') }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="card">
        <div class="card-header"><h2>{{ $t('db.inNetworkAddress') }}</h2></div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-bottom: 8px">{{ $t('db.appsOnASharedNetwork') }}</p>
          <div class="dns-field">
            <span class="dns-field-label">{{ $t('db.host') }}</span>
            <div class="dns-field-row">
              <span class="dns-field-value">{{ inst.host }}:{{ inst.port }}</span>
              <button class="btn-icon btn-icon-muted" :title="$t('action.copy')" :aria-label="$t('action.copy')" @click="copy(`${inst.host}:${inst.port}`)"><span class="mdi mdi-content-copy"></span></button>
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
          <h2>{{ $t('db.engineVersion') }}</h2>
          <span class="text-muted text-sm">Currently {{ inst.engine }} {{ inst.version }}.</span>
        </div>

        <!-- Live progress while upgrading: a phase stepper driven by the SSE stream -->
        <div v-if="inst.status === 'upgrading' && inst.upgrade" class="card-body">
          <p class="text-sm" style="margin: 0 0 12px">{{ $t('db.upgrading') }}<strong>{{ inst.upgrade.from_version }}</strong> → <strong>{{ inst.upgrade.to_version }}</strong>
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
            <span class="mdi mdi-shield-check-outline"></span>{{ $t('db.aFullBackupWasTaken') }}</p>
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
            <label class="text-muted text-sm">{{ $t('db.upgradeTo') }}</label>
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
              <span class="mdi mdi-arrow-up-bold-circle-outline"></span>{{ $t('action.upgrade') }}</button>
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
            <p class="form-hint">{{ $t('db.aFullBackupIsTaken') }}</p>
          </div>
        </div>
        <div v-else class="card-body text-muted text-sm">{{ $t('db.theInstanceMustBeRunning') }}</div>
        </template>
      </div>

      <div class="card mb-4">
        <div class="card-header">
          <h2>{{ $t('nav.networking.networks') }}</h2>
          <span class="text-muted text-sm">{{ $t('db.connectThisDatabaseToAdditional') }}</span>
        </div>
        <div v-if="ws.canEdit" class="card-body" style="border-bottom: 1px solid var(--border-primary)">
          <form class="flex items-center gap-2" @submit.prevent="attachNetwork">
            <select v-model.number="netToAttach" class="form-select" :aria-label="$t('db.networkToConnect')" style="min-width: 220px" :disabled="netBusy || attachableNets.length === 0">
              <option :value="null" disabled>{{ attachableNets.length ? 'Select a network…' : 'All networks already attached' }}</option>
              <option v-for="n in attachableNets" :key="n.id" :value="n.id">{{ n.name }}</option>
            </select>
            <button class="btn btn-primary" :disabled="!netToAttach || netBusy"><span class="mdi mdi-lan-connect"></span>{{ $t('db.connect') }}</button>
          </form>
        </div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('dashboard.resources.network') }}</th><th>{{ $t('db.dockerName') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="n in attachedNets" :key="n.id">
                <td class="cell-title">{{ n.name }} <span v-if="n.is_default" class="badge badge-info" style="margin-left: 6px">{{ $t('db.default') }}</span></td>
                <td class="cell-sub" style="font-family: monospace">{{ n.docker_name }}</td>
                <td class="text-right">
                  <button
                    v-if="ws.canEdit && !n.is_default"
                    class="btn btn-sm btn-secondary"
                    :disabled="netBusy"
                    @click="detachNetwork(n)"
                  >{{ $t('action.disconnect') }}</button>
                  <span v-else-if="n.is_default" class="text-muted text-sm" :title="$t('db.theDefaultNetworkIsAlways')">{{ $t('db.alwaysAttached') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="ws.canEdit" class="card danger-card">
        <div class="card-header"><h2>{{ $t('db.dangerZone') }}</h2></div>
        <div class="card-body flex items-center justify-between gap-3">
          <div>
            <div class="cell-title">{{ $t('db.deleteThisDatabaseInstance') }}</div>
            <div class="cell-sub">{{ $t('db.removesTheInstanceAllIts') }}</div>
          </div>
          <button class="btn btn-danger" :disabled="deleteBlockedReason !== ''" :title="deleteBlockedReason || $t('db.deleteInstance')" @click="askDelete">{{ $t('action.delete') }}</button>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <AppModal v-if="showResize" @close="showResize = false">
        <div class="modal-header">
          <h3>{{ $t('dashboard.resources.title') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showResize = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveResize">
          <div class="modal-body">
            <div v-if="sizeOffer.sizes.length" class="form-group">
              <label class="form-label">{{ $t('databases.col.size') }}</label>
              <select v-model="resizeForm.size" class="form-select">
                <option v-if="!sizeOffer.bound" value="">{{ $t('databases.form.custom') }}</option>
                <option v-for="s in sizeOffer.sizes" :key="s.id" :value="s.name">{{ sizeLabel(s) }}</option>
              </select>
            </div>
            <template v-if="!resizeForm.size">
              <div class="form-group">
                <label class="form-label">{{ $t('databases.form.memory') }}</label>
                <input v-model.number="resizeForm.memory_mb" type="number" min="0" class="form-input" :placeholder="$t('db.unlimited')" />
                <p class="form-hint">{{ $t('db.theEngineIsTunedTo') }}</p>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('databases.form.cpu') }}</label>
                <input v-model.number="resizeForm.cpu_cores" type="number" min="0" step="0.25" class="form-input" :placeholder="$t('db.unlimited')" />
              </div>
            </template>
            <p class="form-hint" style="margin-bottom: 0">{{ $t('db.theContainerIsRecreatedOn') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showResize = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="resizing">{{ resizing ? 'Saving…' : 'Apply and restart' }}</button>
          </div>
        </form>
      </AppModal>

      <!-- Create logical database -->
      <AppModal v-if="showCreateDb" @close="showCreateDb = false">
        <div class="modal-header">
          <h3>{{ $t('dashboard.quick.newDatabase.label') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreateDb = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="createDb">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="dbForm.name" class="form-input" :placeholder="$t('db.eGBlog')" required autofocus />
              <p class="form-hint">{{ $t('db.aDedicatedUserIsCreated') }}</p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('db.attachToApp') }}<span class="text-muted">{{ $t('apps.form.optional') }}</span></label>
              <select v-model="dbForm.app" class="form-select">
                <option :value="null">{{ $t('db.donTAttach') }}</option>
                <option v-for="a in appsOnNode" :key="a.id" :value="a.id">{{ a.name }}</option>
              </select>
              <p class="form-hint">{{ $t('db.injectsDatabaseUrlDbEnv') }}<template v-if="hiddenAppCount > 0"> {{ hiddenAppCount }} app(s) on other nodes are hidden (must share the database's node).</template>
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreateDb = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creatingDb">{{ creatingDb ? 'Creating…' : 'Create database' }}</button>
          </div>
        </form>
      </AppModal>

      <!-- Connection reveal -->
      <AppModal v-if="connModal" max-width="560px" @close="connModal = null">
        <div class="modal-header">
          <h3>Connection · {{ connModal.title }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="connModal = null"><span class="mdi mdi-close"></span></button>
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
              <button v-if="f.value" class="btn-icon btn-icon-muted" :title="$t('action.copy')" :aria-label="$t('action.copy')" @click="copy(f.value)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </div>
        </div>
      </AppModal>

      <!-- Restore dialog -->
      <AppModal v-if="restoreModal" @close="restoreModal = null">
        <div class="modal-header">
          <h3>{{ restoreModal.backupId != null ? `Restore backup #${restoreModal.number}` : 'Restore from file' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="restoreModal = null"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="runRestore">
          <div class="modal-body">
            <p v-if="restoreModal.comment" class="restore-note">
              <span class="mdi mdi-note-outline"></span> {{ restoreModal.comment }}
            </p>
            <p v-if="restoreModal.version" class="form-hint" style="margin-bottom: 12px">
              Taken from {{ inst?.engine }} {{ restoreModal.version }}<template v-if="inst?.version && inst.version !== restoreModal.version">; this instance now runs {{ inst.version }}</template>.
            </p>
            <p v-if="restoreModal.encrypted" class="form-hint" style="margin-bottom: 12px">
              <span class="mdi mdi-lock-outline"></span>{{ $t('db.encryptedItIsDecryptedWith') }}</p>
            <div v-if="restoreModal.backupId == null" class="form-group">
              <label class="form-label">{{ $t('db.dumpFile') }}</label>

              <div class="file-drop-zone">
                <input type="file" accept=".sql,.gz,.sql.gz,.dump" class="file-input-hidden" required
                  @change="onRestoreFile" />
                <div class="file-drop-content">
                  <span class="mdi mdi-upload-cloud file-icon"></span>
                  <span class="file-text">{{ $t('db.clickToUploadOrDrag') }}</span>
                  <span class="file-subtext">{{ $t('db.sqlGzOrDumpFiles') }}</span>
                </div>
              </div>

              <p class="form-hint">
                <i18n-t keypath="db.dumpFormats" tag="span"><template #a><code>.sql.gz</code></template><template #b><code>.sql</code></template><template #c><code>.dump</code></template></i18n-t></p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('db.method') }}</label>
              <label class="radio-row"><input type="radio" value="normal" v-model="restoreMethod" />{{ $t('db.normalRestoreOverTheExisting') }}</label>
              <label class="radio-row"><input type="radio" value="force" v-model="restoreMethod" />{{ $t('db.forceDropRecreateTheDatabase') }}</label>
              <p v-if="restoreMethod === 'force'" class="form-hint" style="color: var(--danger-600)">{{ $t('db.forceDropsTheDatabaseBefore') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="restoreModal = null">{{ $t('action.cancel') }}</button>
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
        <label class="form-label"><i18n-t keypath="db.typeToConfirm" tag="span"><template #name><code>{{ inst?.name }}</code></template></i18n-t></label>
        <input v-model="deleteConfirm" class="form-input" :placeholder="inst?.name" autofocus autocomplete="off" />
      </div>
    </ConfirmDialog>
  </div>
  <div v-else class="loading-page"><span class="spinner"></span></div>
</template>

<style scoped>
.restore-note {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  margin: 0 0 8px;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--bg-secondary);
  font-size: 13px;
  color: var(--text-primary);
}

.backup-note-input {
  max-width: 280px;
  height: 32px;
  font-size: 12px;
}

/* The note reads as text until hovered, so an unannotated history stays quiet
   rather than showing a row of buttons. */
.set-items {
  list-style: none;
  margin: 0 0 8px;
  padding: 8px 12px;
  border-left: 2px solid var(--border-primary);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.set-items li {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.backup-note {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  margin-top: 3px;
  padding: 1px 5px 1px 3px;
  margin-left: -3px;
  border: none;
  border-radius: var(--radius-sm);
  background: none;
  cursor: pointer;
  font-size: 12px;
  text-align: left;
  color: var(--text-secondary);
}

.backup-note .mdi {
  font-size: 13px;
  opacity: 0;
  color: var(--text-muted);
}

.backup-note:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.backup-note:hover .mdi,
.backup-note.empty .mdi {
  opacity: 1;
}

.backup-note.empty {
  color: var(--text-muted);
  font-style: italic;
}

.backup-note.static {
  display: block;
  cursor: default;
  margin-top: 3px;
  font-size: 12px;
  color: var(--text-secondary);
}

.backup-note-edit {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
}

.backup-note-edit .form-input {
  max-width: 320px;
  height: 30px;
  font-size: 12px;
}

.backup-pinned {
  color: var(--primary-500);
}

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
/* Instance timeline (Events tab) — mirrors the application timeline. */
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

.database-header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  flex-wrap: wrap;
}

.database-header-actions button {
  flex: 1 1 auto;
  min-width: max-content;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
