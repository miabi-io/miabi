<script setup lang="ts">
import { ref, reactive, onMounted, computed, watch } from 'vue'
import {
  platformBackupApi,
  type PlatformBackup,
  type PlatformBackupSet,
  type PlatformBackupSettings,
  type PlatformBackupSettingsPayload,
  type PlatformVolume,
  type RecoveryStatus,
  type VerifyReport,
  type DiscoveredSet,
  type DiscoveredArtifact,
  type SelectiveRestoreReport,
} from '@/api/platformBackup'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const licenseStore = useLicenseStore()
const ent = useEntitlement('platform_backup')

const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const running = ref(false)
const restoringId = ref<number | null>(null)

const backups = ref<PlatformBackup[]>([])
const sets = ref<PlatformBackupSet[]>([])
const volumes = ref<PlatformVolume[]>([])
const settings = ref<PlatformBackupSettings | null>(null)
const recovery = ref<RecoveryStatus | null>(null)

const creatingSet = ref(false)
const verifyingId = ref<number | null>(null)
const verifyReport = ref<VerifyReport | null>(null)
const verifyPassphrase = ref('')
const verifyTarget = ref<PlatformBackupSet | null>(null)
const reconciling = ref(false)
const completing = ref(false)

// Fields this deployment takes from environment variables. They are shown
// read-only: the process configuration wins, so accepting an edit here would be
// a lie.
const envLocked = computed(() => new Set(settings.value?.env_locked ?? []))
function locked(field: string): boolean {
  return envLocked.value.has(field)
}
const s3FromEnv = computed(() => locked('s3_bucket'))

// A passphrase is available if one is stored (or supplied by the environment) or
// the operator is entering one right now.
const havePassphrase = computed(() => passphraseSet.value || !!form.backup_passphrase)

// Editable settings form.
const form = reactive<PlatformBackupSettingsPayload>({
  // S3/MinIO is the only destination, so it starts selected — an unchecked box
  // would hide the fields the operator came here to fill in.
  s3_enabled: true,
  s3_endpoint: '',
  s3_bucket: '',
  s3_region: '',
  s3_access_key: '',
  s3_secret_key: '',
  s3_use_ssl: true,
  s3_force_path_style: false,
  root_path: '',
  database_backup_path: '',
  volume_backup_path: '',
  encrypt_backups: false,
  backup_passphrase: '',
  // Off until a passphrase exists — sealing the envelope needs one, and a
  // checkbox that cannot be honoured is worse than one that is not offered.
  include_identity: false,
  include_tenant_data: false,
  schedule_enabled: false,
  schedule_cron: '0 3 * * *',
  max_backups: 7,
  retention_days: 30,
  volumes: [],
})
const secretSet = ref(false)
const passphraseSet = ref(false)

// "Back up now" selection.
const backupDB = ref(true)
const backupVolumes = ref<string[]>([])

function applySettings(s: PlatformBackupSettings) {
  settings.value = s
  form.s3_enabled = s.s3_enabled
  form.s3_endpoint = s.s3_endpoint ?? ''
  form.s3_bucket = s.s3_bucket ?? ''
  form.s3_region = s.s3_region ?? ''
  form.s3_access_key = s.s3_access_key ?? ''
  form.s3_secret_key = ''
  form.s3_use_ssl = s.s3_use_ssl
  form.s3_force_path_style = s.s3_force_path_style
  form.root_path = s.root_path ?? ''
  form.database_backup_path = s.database_backup_path ?? ''
  form.volume_backup_path = s.volume_backup_path ?? ''
  form.encrypt_backups = s.encrypt_backups
  form.backup_passphrase = ''
  form.include_identity = s.include_identity
  form.include_tenant_data = s.include_tenant_data
  form.schedule_enabled = s.schedule_enabled
  form.schedule_cron = s.schedule_cron || '0 3 * * *'
  form.max_backups = s.max_backups
  form.retention_days = s.retention_days
  form.volumes = [...(s.volumes ?? [])]
  secretSet.value = s.s3_secret_set
  passphraseSet.value = s.passphrase_set
}

// Each panel loads independently. With Promise.all, one failing call — a
// volume listing that cannot reach Docker, a history query that errors — took
// the settings down with it: applySettings never ran, the form kept its blank
// defaults, and a perfectly good configuration looked like it had vanished.
async function load() {
  if (!ent.has.value) return
  loading.value = true
  try {
    const [s, b, v, rp, rec] = await Promise.allSettled([
      platformBackupApi.getSettings(),
      platformBackupApi.list(0, 50),
      platformBackupApi.volumes(),
      platformBackupApi.listSets(0, 25),
      platformBackupApi.recoveryStatus(),
    ])

    if (s.status === 'fulfilled') {
      applySettings(s.value.data.data)
    } else {
      // The only failure worth interrupting the operator for: without settings
      // there is nothing on this page to act on.
      notify.apiError(s.reason)
    }
    if (b.status === 'fulfilled') backups.value = b.value.data.data ?? []
    if (v.status === 'fulfilled') volumes.value = v.value.data.data ?? []
    if (rp.status === 'fulfilled') sets.value = rp.value.data.data ?? []
    if (rec.status === 'fulfilled') recovery.value = rec.value.data.data ?? null
  } finally {
    loading.value = false
  }
}

// The entitlement is read from the license store, which loads asynchronously.
// Firing load() without awaiting it meant the first paint ran while `ent.has`
// was still false, load() returned immediately, and nothing was ever fetched —
// the page stayed empty until some other action happened to call load() again,
// which is why history only appeared after running a backup.
onMounted(async () => {
  await licenseStore.load()
  await load()
})

// A later license refresh (or a slow first load resolving after mount) must also
// populate the page rather than leaving it blank.
watch(
  () => ent.has.value,
  (has, was) => {
    if (has && !was) load()
  },
)

const payload = computed<PlatformBackupSettingsPayload>(() => ({
  ...form,
  // Omit secrets when left blank so the stored ones are preserved.
  s3_secret_key: form.s3_secret_key ? form.s3_secret_key : undefined,
  backup_passphrase: form.backup_passphrase ? form.backup_passphrase : undefined,
}))

async function save() {
  if (form.s3_enabled && !form.s3_bucket.trim()) {
    notify.error('An S3 bucket is required when S3 is enabled')
    return
  }
  if ((form.encrypt_backups || form.include_identity) && !havePassphrase.value) {
    notify.error(
      'Encrypting artifacts and sealing the identity envelope both need a backup passphrase. ' +
        'Set one, or turn both off — backups work without either.',
    )
    return
  }
  saving.value = true
  try {
    const res = await platformBackupApi.updateSettings(payload.value)
    applySettings(res.data.data)
    notify.success('Platform backup settings saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  try {
    await platformBackupApi.testSettings(payload.value)
    notify.success('Platform backup settings look valid')
  } catch (e) {
    notify.apiError(e)
  } finally {
    testing.value = false
  }
}

async function runBackup() {
  if (!backupDB.value && backupVolumes.value.length === 0) {
    notify.error('Select the database and/or at least one volume')
    return
  }
  running.value = true
  try {
    await platformBackupApi.create({ database: backupDB.value, volumes: backupVolumes.value })
    notify.success('Backup started')
    backupVolumes.value = []
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    running.value = false
  }
}

async function createSet() {
  creatingSet.value = true
  try {
    await platformBackupApi.createSet()
    notify.success('Recovery point started')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    creatingSet.value = false
  }
}

function openVerify(set: PlatformBackupSet) {
  verifyTarget.value = set
  verifyPassphrase.value = ''
  verifyReport.value = null
}

async function runVerify() {
  const set = verifyTarget.value
  if (!set) return
  verifyingId.value = set.id
  try {
    const res = await platformBackupApi.verifySet(set.id, verifyPassphrase.value)
    verifyReport.value = res.data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    verifyingId.value = null
  }
}

// Restore from the bucket: browse what is there, pick artifacts, restore them
// into this live platform.
const discovering = ref(false)
const discovered = ref<DiscoveredSet[]>([])
const openRef = ref<string | null>(null)
const selected = ref<Set<string>>(new Set())
const restorePassphrase = ref('')
const stopApps = ref(true)
const restoring = ref(false)
const restoreReport = ref<SelectiveRestoreReport | null>(null)
const pendingRestoreSet = ref<DiscoveredSet | null>(null)

async function discover() {
  discovering.value = true
  try {
    const res = await platformBackupApi.discover()
    discovered.value = res.data.data ?? []
    if (!discovered.value.length) notify.info('No recovery points found in the backup target')
  } catch (e) {
    notify.apiError(e)
  } finally {
    discovering.value = false
  }
}

function toggleOpen(set: DiscoveredSet) {
  openRef.value = openRef.value === set.ref ? null : set.ref
  selected.value = new Set()
  restoreReport.value = null
}

// Selection is keyed by ref+file: a discovered artifact has no local id until
// its recovery point has been imported.
function artifactKey(set: DiscoveredSet, a: DiscoveredArtifact): string {
  return `${set.ref}::${a.key}`
}
function isSelected(set: DiscoveredSet, a: DiscoveredArtifact): boolean {
  return selected.value.has(artifactKey(set, a))
}
function toggleArtifact(set: DiscoveredSet, a: DiscoveredArtifact) {
  if (!a.restorable) return
  const key = artifactKey(set, a)
  const next = new Set(selected.value)
  next.has(key) ? next.delete(key) : next.add(key)
  selected.value = next
}
const selectedCount = computed(() => selected.value.size)

function restorableArtifacts(set: DiscoveredSet): DiscoveredArtifact[] {
  return (set.artifacts ?? []).filter((a) => a.restorable)
}
function selectAll(set: DiscoveredSet) {
  selected.value = new Set(restorableArtifacts(set).map((a) => artifactKey(set, a)))
}

// The recovery point must exist locally before its artifacts have ids to
// restore by, so a discovered-but-unknown one is adopted first. Import is
// idempotent, so this is safe to repeat.
async function runSelectiveRestore() {
  const set = pendingRestoreSet.value
  if (!set) return
  restoring.value = true
  restoreReport.value = null
  try {
    let setID = set.set_id
    if (!set.known || !setID) {
      const imported = await platformBackupApi.importSet(set.ref)
      setID = imported.data.data.id
    }
    const full = await platformBackupApi.getSet(setID!)
    const keys = new Set(
      restorableArtifacts(set)
        .filter((a) => isSelected(set, a))
        .map((a) => a.file),
    )
    const ids = (full.data.data.items ?? [])
      .filter((i) => keys.has(i.filename ?? ''))
      .map((i) => i.id)

    if (!ids.length) {
      notify.error('Could not match the selected artifacts to this recovery point')
      return
    }
    const res = await platformBackupApi.restoreSelected(setID!, {
      artifact_ids: ids,
      passphrase: restorePassphrase.value || undefined,
      stop_apps: stopApps.value,
    })
    restoreReport.value = res.data.data
    pendingRestoreSet.value = null
    notify.success(`Restored ${res.data.data.restored} of ${res.data.data.requested}`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    restoring.value = false
  }
}

function subjectLabel(a: DiscoveredArtifact): string {
  switch (a.subject) {
    case 'tenant-database':
      return `database ${a.workspace}/${a.database}`
    case 'tenant-volume':
      return `volume ${a.volume}`
    case 'volume':
      return `platform volume ${a.volume}`
    case 'database':
      return 'control-plane database'
    case 'identity':
      return 'identity envelope'
    default:
      return a.subject
  }
}

const retryingId = ref<number | null>(null)
async function retrySet(set: PlatformBackupSet) {
  retryingId.value = set.id
  try {
    await platformBackupApi.retrySet(set.id)
    notify.success('Retrying the failed artifacts')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    retryingId.value = null
  }
}

function failedCount(set: PlatformBackupSet): number {
  return (set.items ?? []).filter((i) => i.status === 'failed').length
}

const pendingSetDelete = ref<PlatformBackupSet | null>(null)
async function removeSet() {
  const set = pendingSetDelete.value
  if (!set) return
  try {
    await platformBackupApi.removeSet(set.id)
    notify.success('Recovery point deleted')
    pendingSetDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}

async function reconcile() {
  reconciling.value = true
  try {
    const res = await platformBackupApi.reconcile()
    if (recovery.value) recovery.value.report = res.data.data
    notify.success('Reconcile finished — review the report below')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    reconciling.value = false
  }
}

const pendingComplete = ref(false)
async function completeRecovery() {
  completing.value = true
  try {
    await platformBackupApi.completeRecovery()
    pendingComplete.value = false
    notify.success('Recovery completed — schedules resume')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    completing.value = false
  }
}

function setSummary(set: PlatformBackupSet): string {
  const items = set.items ?? []
  const count = (subject: string) => items.filter((i) => i.subject === subject).length
  const parts: string[] = []
  if (count('database')) parts.push('control plane')
  const vols = count('volume')
  if (vols) parts.push(`${vols} platform volume${vols > 1 ? 's' : ''}`)
  const tdb = count('tenant-database')
  if (tdb) parts.push(`${tdb} tenant database${tdb > 1 ? 's' : ''}`)
  const tvol = count('tenant-volume')
  if (tvol) parts.push(`${tvol} tenant volume${tvol > 1 ? 's' : ''}`)
  return parts.join(' · ') || '—'
}

const pendingRestore = ref<PlatformBackup | null>(null)
const restoreMessage = computed(() => {
  const b = pendingRestore.value
  if (!b) return ''
  const what =
    b.subject === 'database'
      ? 'the control-plane database (overwrites the running database in place)'
      : `volume "${b.volume_name}" (overwrites its contents)`
  return (
    `Restore ${what}? This is destructive. Put the platform in maintenance mode first. ` +
    `You also need the original MIABI_ENCRYPTION_KEY to decrypt restored secrets.`
  )
})
async function restore() {
  const b = pendingRestore.value
  if (!b) return
  pendingRestore.value = null
  restoringId.value = b.id
  try {
    await platformBackupApi.restore(b.id)
    notify.success('Restore completed')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    restoringId.value = null
  }
}

const pendingDelete = ref<PlatformBackup | null>(null)
const deleting = ref(false)
async function remove() {
  const b = pendingDelete.value
  if (!b) return
  deleting.value = true
  try {
    await platformBackupApi.remove(b.id)
    notify.success('Backup deleted')
    pendingDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

function canRestore(b: PlatformBackup): boolean {
  return b.status === 'completed' && !!b.filename
}

function statusBadge(s: string): string {
  switch (s) {
    case 'completed':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    case 'running':
      return 'badge-info'
    default:
      return 'badge-warning'
  }
}
function fmtDate(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}
function fmtSize(n: number): string {
  if (!n) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${u[i]}`
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Platform Backup</h1>
        <p class="text-muted">
          Disaster recovery for Miabi itself — back up and restore the control-plane database and
          platform volumes. Separate from per-workspace backups.
        </p>
      </div>
    </div>

    <!-- Locked (Community / not entitled) -->
    <div v-if="!ent.has.value" class="card">
      <div class="card-body locked">
        <span class="mdi mdi-lock-outline"></span>
        <div>
          <p>
            Platform backup is an Enterprise feature. Back up Miabi's own database and platform
            volumes to S3, on demand or on a schedule, and restore them for disaster recovery.
          </p>
          <router-link to="/admin/license" class="btn btn-secondary btn-sm">Manage license</router-link>
        </div>
      </div>
    </div>

    <template v-else>
      <!-- DR key reminder -->
      <div class="card dr-note">
        <div class="card-body">
          <span class="mdi mdi-key-alert-outline"></span>
          <div>
            <strong>Keep your encryption key safe.</strong>
            A database backup contains ciphertext (workspace keys, provider credentials) encrypted
            under <code>MIABI_ENCRYPTION_KEY</code>. You must preserve that key out-of-band — a
            restore onto a fresh server is useless without it. Store S3 off-box for true DR.
          </div>
        </div>
      </div>

      <!-- Recovery in progress -->
      <div v-if="recovery?.pending" class="card recovering">
        <div class="card-body">
          <div class="recovering-head">
            <span class="mdi mdi-lifebuoy"></span>
            <div>
              <strong>This platform was restored and is running quiesced.</strong>
              <p class="text-muted">
                {{ recovery.note }} — schedules do not fire and nothing redeploys until you complete
                recovery. Reconcile brings the restored state onto this host; complete it once DNS
                points here, so certificates can be issued against the right address.
              </p>
            </div>
          </div>
          <div class="recovering-actions">
            <button class="btn btn-primary btn-sm" :disabled="reconciling" @click="reconcile">
              {{ reconciling ? 'Reconciling…' : 'Reconcile now' }}
            </button>
            <button class="btn btn-secondary btn-sm" :disabled="completing" @click="pendingComplete = true">
              Complete recovery
            </button>
          </div>

          <div v-if="recovery.report" class="recovery-report">
            <div class="recovery-counts">
              <span>{{ recovery.report.nodes_reset }} nodes reset</span>
              <span>{{ recovery.report.networks_ensured }} networks</span>
              <span>{{ recovery.report.databases_started }} databases started</span>
              <span>{{ recovery.report.apps_redeployed }} apps redeployed</span>
              <span>{{ recovery.report.routes_synced }} route sets synced</span>
            </div>
            <p v-if="recovery.report.tenant_data" class="text-muted">
              Tenant data from {{ recovery.report.tenant_data.ref }}:
              {{ recovery.report.tenant_data.databases_restored }} databases and
              {{ recovery.report.tenant_data.volumes_restored }} volumes restored.
            </p>
            <div v-if="recovery.report.unrecoverable?.length" class="recovery-list danger">
              <strong>Could not be recovered</strong>
              <ul><li v-for="(u, i) in recovery.report.unrecoverable" :key="i">{{ u }}</li></ul>
            </div>
            <div v-if="recovery.report.failures?.length" class="recovery-list danger">
              <strong>Failures</strong>
              <ul><li v-for="(f, i) in recovery.report.failures" :key="i">{{ f }}</li></ul>
            </div>
            <div v-if="recovery.report.manual?.length" class="recovery-list">
              <strong>Still to do by hand</strong>
              <ul><li v-for="(m, i) in recovery.report.manual" :key="i">{{ m }}</li></ul>
            </div>
          </div>
        </div>
      </div>

      <!-- Recovery points -->
      <div class="card">
        <div class="card-header">
          <h3>Recovery points</h3>
          <button class="btn btn-primary btn-sm" :disabled="creatingSet || !settings?.s3_enabled" @click="createSet">
            {{ creatingSet ? 'Starting…' : 'Take a recovery point' }}
          </button>
        </div>
        <div class="card-body">
          <p class="text-muted">
            A recovery point is what <code>miabi restore</code> consumes to rebuild this platform on
            a fresh host: the sealed identity envelope, the control-plane dump, the selected platform
            volumes, and — when enabled — every workspace's databases and volumes. Its info file is
            written to the backup root as <code>recovery-&lt;ref&gt;.xml</code>.
          </p>

          <div v-if="!sets.length" class="empty">No recovery points yet.</div>
          <table v-else class="table">
            <thead>
              <tr>
                <th>Reference</th>
                <th>Contents</th>
                <th>Status</th>
                <th>Size</th>
                <th>Taken</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="set in sets" :key="set.id">
                <td>
                  <code class="ref">{{ set.ref }}</code>
                  <div class="sub">
                    Miabi {{ set.miabi_version || '—' }}
                    <span v-if="set.encrypted" class="chip">encrypted</span>
                    <span v-if="set.identity_sealed" class="chip">identity sealed</span>
                    <span v-else class="chip chip-warn">no identity envelope</span>
                  </div>
                </td>
                <td>{{ setSummary(set) }}</td>
                <td>
                  <span class="badge" :class="statusBadge(set.status)">{{ set.status }}</span>
                  <pre v-if="set.error" class="set-error">{{ set.error }}</pre>
                </td>
                <td>{{ fmtSize(set.size_bytes) }}</td>
                <td>{{ fmtDate(set.created_at) }}</td>
                <td class="actions">
                  <button
                    v-if="failedCount(set) > 0"
                    class="btn-icon btn-icon-muted"
                    :title="`Retry ${failedCount(set)} failed artifact(s)`"
                    aria-label="Retry failed artifacts"
                    :disabled="retryingId === set.id"
                    @click="retrySet(set)"
                  >
                    <span class="mdi mdi-refresh"></span>
                  </button>
                  <button
                    class="btn-icon btn-icon-muted"
                    title="Verify without restoring"
                    aria-label="Verify"
                    :disabled="verifyingId === set.id"
                    @click="openVerify(set)"
                  >
                    <span class="mdi mdi-shield-check-outline"></span>
                  </button>
                  <button class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingSetDelete = set">
                    <span class="mdi mdi-trash-can-outline"></span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>

          <!-- Verify drill -->
          <div v-if="verifyTarget" class="verify">
            <h4>Verify {{ verifyTarget.ref }}</h4>
            <p class="text-muted">
              Checks every artifact is still in the bucket and that the identity envelope opens —
              without restoring anything. Enter the passphrase you recorded out-of-band: verifying
              with the stored one proves the files exist, not that you can still open them after
              losing this database.
            </p>
            <div class="verify-row">
              <input
                v-model="verifyPassphrase"
                type="password"
                class="form-input"
                placeholder="Backup passphrase (blank uses the stored one)"
                autocomplete="off"
              />
              <button class="btn btn-primary btn-sm" :disabled="verifyingId !== null" @click="runVerify">
                {{ verifyingId !== null ? 'Verifying…' : 'Verify' }}
              </button>
              <button class="btn btn-secondary btn-sm" @click="verifyTarget = null">Close</button>
            </div>
            <div v-if="verifyReport" class="verify-result">
              <p>
                <span class="badge" :class="verifyReport.restorable ? 'badge-success' : 'badge-danger'">
                  {{ verifyReport.restorable ? 'Restorable' : 'Not restorable' }}
                </span>
                {{ verifyReport.artifacts_found }}/{{ verifyReport.artifacts_total }} artifacts present ·
                identity envelope {{ verifyReport.identity_opened ? 'opened' : 'not opened' }}
                <template v-if="verifyReport.identity_opened">
                  · encryption key {{ verifyReport.kek_matches ? 'matches' : 'DOES NOT MATCH' }}
                </template>
              </p>
              <ul v-if="verifyReport.findings?.length">
                <li v-for="(f, i) in verifyReport.findings" :key="i" :class="f.severity">
                  {{ f.message }}
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>

      <!-- Restore from the bucket -->
      <div class="card">
        <div class="card-header">
          <h3>Restore from the backup target</h3>
          <button class="btn btn-secondary btn-sm" :disabled="discovering || !settings?.s3_enabled" @click="discover">
            {{ discovering ? 'Reading…' : 'Browse recovery points' }}
          </button>
        </div>
        <div class="card-body">
          <p class="text-muted">
            Reads the bucket itself, so recovery points this platform has no record of — from
            before a rebuild, or from another install — show up here too. Pick what you want back;
            each artifact is restored independently.
          </p>

          <div v-if="!discovered.length" class="empty">
            Nothing loaded yet. <strong>Browse recovery points</strong> reads the configured target.
          </div>

          <div v-for="set in discovered" :key="set.ref" class="disc-set">
            <button class="disc-head" @click="toggleOpen(set)">
              <span class="mdi" :class="openRef === set.ref ? 'mdi-chevron-down' : 'mdi-chevron-right'"></span>
              <code class="ref">{{ set.ref }}</code>
              <span class="sub">
                {{ fmtDate(set.created_at) }} ·
                {{ (set.artifacts ?? []).length }} artifacts
                <span v-if="set.encrypted" class="chip">encrypted</span>
                <span v-if="!set.known" class="chip">not in this platform's history</span>
                <span v-if="set.foreign" class="chip chip-warn">another install</span>
              </span>
            </button>

            <div v-if="openRef === set.ref" class="disc-body">
              <div v-if="set.foreign" class="env-note">
                <span class="mdi mdi-alert-outline"></span>
                <div>
                  This recovery point was taken by a different install. Its <strong>data</strong>
                  restores here perfectly well — a dump is a dump. Anything control-plane from it
                  would not: those secrets are encrypted under a key this platform does not have.
                  You will need <em>its</em> backup passphrase, not this one.
                </div>
              </div>

              <table class="table">
                <thead>
                  <tr><th style="width:32px"></th><th>Artifact</th><th>File</th><th>Size</th><th></th></tr>
                </thead>
                <tbody>
                  <tr v-for="a in set.artifacts ?? []" :key="a.key" :class="{ 'row-off': !a.restorable }">
                    <td>
                      <input
                        type="checkbox"
                        :disabled="!a.restorable"
                        :checked="isSelected(set, a)"
                        @change="toggleArtifact(set, a)"
                      />
                    </td>
                    <td>
                      {{ subjectLabel(a) }}
                      <div v-if="a.reason" class="cell-sub">{{ a.reason }}</div>
                    </td>
                    <td><code class="ref">{{ a.file }}</code></td>
                    <td>{{ fmtSize(a.size_bytes ?? 0) }}</td>
                    <td class="actions">
                      <span v-if="!a.present" class="chip chip-warn">missing</span>
                      <span v-else-if="a.encrypted" class="chip">encrypted</span>
                    </td>
                  </tr>
                </tbody>
              </table>

              <div class="disc-actions">
                <button class="btn btn-secondary btn-sm" @click="selectAll(set)">Select all restorable</button>
                <label class="check-row">
                  <input v-model="stopApps" type="checkbox" />
                  Stop the apps using a volume while it is restored
                </label>
                <input
                  v-model="restorePassphrase"
                  type="password"
                  class="form-input"
                  autocomplete="off"
                  :placeholder="set.foreign ? 'Passphrase for THIS recovery point (required)' : 'Passphrase (blank uses the stored one)'"
                />
                <button
                  class="btn btn-danger btn-sm"
                  :disabled="!selectedCount || restoring"
                  @click="pendingRestoreSet = set"
                >
                  {{ restoring ? 'Restoring…' : `Restore ${selectedCount} selected` }}
                </button>
              </div>

              <div v-if="restoreReport && restoreReport.ref === set.ref" class="restore-report">
                <p>
                  <strong>{{ restoreReport.restored }} of {{ restoreReport.requested }} restored.</strong>
                </p>
                <ul>
                  <li v-for="r in restoreReport.results ?? []" :key="r.artifact_id" :class="r.ok ? 'okline' : 'error'">
                    {{ r.ok ? '✓' : '✗' }} {{ r.label }}<template v-if="r.error"> — {{ r.error }}</template>
                  </li>
                </ul>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Settings -->
      <div class="card">
        <div class="card-header"><h3>Destination &amp; schedule</h3></div>
        <div class="card-body">
          <div class="form-group">
            <label class="check-row">
              <input v-model="form.s3_enabled" type="checkbox" :disabled="locked('s3_enabled')" />
              Store backups in S3 / MinIO
            </label>
            <span class="form-hint">
              This is the only destination. A backup written to a disk on the host it protects
              cannot be read once that host is gone.
            </span>
          </div>

          <div v-if="s3FromEnv" class="env-note">
            <span class="mdi mdi-lock-outline"></span>
            <div>
              The S3 target is set by <code>MIABI_PLATFORM_BACKUP_S3_*</code> on this deployment and
              is read-only here — the process configuration wins, so an edit would be ignored.
              Change it in the stack manifest and restart.
            </div>
          </div>

          <template v-if="form.s3_enabled">
            <div class="grid2">
              <div class="form-group">
                <label class="form-label">Endpoint</label>
                <input v-model="form.s3_endpoint" class="form-input" placeholder="https://s3.amazonaws.com (blank = AWS)" :disabled="locked('s3_endpoint')" />
              </div>
              <div class="form-group">
                <label class="form-label">Bucket</label>
                <input v-model="form.s3_bucket" class="form-input" placeholder="miabi-platform-backups" :disabled="locked('s3_bucket')" />
              </div>
              <div class="form-group">
                <label class="form-label">Region</label>
                <input v-model="form.s3_region" class="form-input" placeholder="us-east-1" :disabled="locked('s3_region')" />
              </div>
              <div class="form-group">
                <label class="form-label">Access key</label>
                <input v-model="form.s3_access_key" class="form-input" autocomplete="off" :disabled="locked('s3_access_key')" />
              </div>
              <div class="form-group">
                <label class="form-label">Secret key</label>
                <input
                  v-model="form.s3_secret_key"
                  class="form-input"
                  type="password"
                  autocomplete="off"
                  :disabled="locked('s3_secret_key')"
                  :placeholder="secretSet ? '••••• (set — leave blank to keep)' : ''"
                />
              </div>
              <div class="form-group">
                <label class="form-label">Backup root</label>
                <input v-model="form.root_path" class="form-input" placeholder="(bucket root)" :disabled="locked('root_path')" />
                <span class="form-hint">
                  Scopes this instance's tree so one bucket can hold several. The recovery-point info
                  file is written here.
                </span>
              </div>
              <div class="form-group">
                <label class="form-label">Database path prefix</label>
                <input v-model="form.database_backup_path" class="form-input" placeholder="databases (default)" :disabled="locked('database_backup_path')" />
              </div>
              <div class="form-group">
                <label class="form-label">Volume path prefix</label>
                <input v-model="form.volume_backup_path" class="form-input" placeholder="volumes (default)" :disabled="locked('volume_backup_path')" />
              </div>
            </div>
            <div class="form-group">
              <label class="check-row"><input v-model="form.s3_use_ssl" type="checkbox" :disabled="locked('s3_use_ssl')" /> Use SSL</label>
              <label class="check-row"><input v-model="form.s3_force_path_style" type="checkbox" :disabled="locked('s3_force_path_style')" /> Force path-style addressing (MinIO)</label>
            </div>
          </template>

          <hr class="sep" />

          <h4 class="section-title">Encryption &amp; contents</h4>
          <div v-if="!havePassphrase" class="env-note">
            <span class="mdi mdi-information-outline"></span>
            <div>
              A backup passphrase is <strong>optional</strong>. Without one, recovery points are
              still taken — unencrypted, and without the sealed identity envelope, which means they
              can be restored onto this platform's own host but not onto a fresh one. Set a
              passphrase to encrypt the artifacts and make them restorable anywhere.
            </div>
          </div>
          <div class="form-group">
            <label class="check-row">
              <input v-model="form.encrypt_backups" type="checkbox" :disabled="locked('encrypt_backups')" />
              Encrypt artifacts with a backup passphrase
            </label>
            <span class="form-hint">
              This passphrase is <strong>not</strong> <code>MIABI_ENCRYPTION_KEY</code>. It protects
              the files; the master key decrypts what is inside them after a restore. Record it
              out-of-band — it is stored in the very database these backups protect.
            </span>
          </div>
          <div class="form-group">
            <label class="form-label">Backup passphrase</label>
            <input
              v-model="form.backup_passphrase"
              class="form-input"
              type="password"
              autocomplete="new-password"
              :disabled="locked('backup_passphrase')"
              :placeholder="passphraseSet ? '••••• (set — leave blank to keep)' : 'At least 12 characters, mixing letters with digits or symbols'"
            />
          </div>
          <div class="form-group">
            <label class="check-row">
              <input
                v-model="form.include_identity"
                type="checkbox"
                :disabled="locked('include_identity') || !havePassphrase"
              />
              Seal the identity envelope into each recovery point
            </label>
            <span class="form-hint">
              Carries this platform's encryption key and JWT secret, sealed under the passphrase.
              Without it a recovery point can only be restored onto a host that still has the
              original <code>MIABI_ENCRYPTION_KEY</code> — never onto a fresh one.
            </span>
          </div>
          <div class="form-group">
            <label class="check-row">
              <input v-model="form.include_tenant_data" type="checkbox" :disabled="locked('include_tenant_data')" />
              Include tenant data (workspace databases and volumes)
            </label>
            <span class="form-hint">
              Turns a control-plane recovery point into a whole-platform one. Expect it to take as
              long, and as much space, as the data itself.
            </span>
          </div>

          <hr class="sep" />

          <div class="form-group">
            <label class="check-row"><input v-model="form.schedule_enabled" type="checkbox" :disabled="locked('schedule_cron')" /> Run on a schedule</label>
          </div>
          <div v-if="form.schedule_enabled" class="grid2">
            <div class="form-group">
              <label class="form-label">Cron</label>
              <input v-model="form.schedule_cron" class="form-input" placeholder="0 3 * * *" />
              <span class="form-hint">Standard 5-field cron. Backs up the DB plus every selected volume.</span>
            </div>
            <div class="form-group">
              <label class="form-label">Keep at most (max backups)</label>
              <input v-model.number="form.max_backups" class="form-input" type="number" min="0" />
            </div>
            <div class="form-group">
              <label class="form-label">Retention (days)</label>
              <input v-model.number="form.retention_days" class="form-input" type="number" min="0" />
            </div>
          </div>

          <div v-if="volumes.length" class="form-group">
            <label class="form-label">Platform volumes in scheduled backups</label>
            <div class="vol-list">
              <label v-for="v in volumes" :key="v.name" class="check-row">
                <input v-model="form.volumes" type="checkbox" :value="v.name" />
                <span class="mono">{{ v.name }}</span>
                <span v-if="v.role" class="badge badge-info">{{ v.role }}</span>
              </label>
            </div>
          </div>
        </div>
        <div class="card-footer actions-right">
          <button class="btn btn-secondary" :disabled="testing || !form.s3_enabled" @click="test">
            {{ testing ? 'Testing…' : 'Test S3' }}
          </button>
          <button class="btn btn-primary" :disabled="saving" @click="save">
            {{ saving ? 'Saving…' : 'Save settings' }}
          </button>
        </div>
      </div>

      <!-- Back up now -->
      <div class="card">
        <div class="card-header"><h3>Back up now</h3></div>
        <div class="card-body">
          <label class="check-row"><input v-model="backupDB" type="checkbox" /> Control-plane database</label>
          <div v-if="volumes.length" class="vol-list mt-2">
            <label v-for="v in volumes" :key="v.name" class="check-row">
              <input v-model="backupVolumes" type="checkbox" :value="v.name" :disabled="!form.s3_enabled" />
              <span class="mono">{{ v.name }}</span>
              <span v-if="v.role" class="badge badge-info">{{ v.role }}</span>
            </label>
            <p v-if="!form.s3_enabled" class="form-hint">Enable S3 above to back up volumes.</p>
          </div>
        </div>
        <div class="card-footer actions-right">
          <button class="btn btn-primary" :disabled="running" @click="runBackup">
            <span class="mdi mdi-cloud-upload-outline"></span> {{ running ? 'Starting…' : 'Back up now' }}
          </button>
        </div>
      </div>

      <!-- History -->
      <div class="card">
        <div class="card-header"><h3>History</h3></div>
        <div v-if="loading && backups.length === 0" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="backups.length === 0" class="empty-state">
          <span class="mdi mdi-database-clock-outline" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>No platform backups yet</h3>
          <p class="text-muted">Run a backup above to get started.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Subject</th><th>Status</th><th>Trigger</th><th>Destination</th><th>Size</th><th>Created</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="b in backups" :key="b.id">
                <td>
                  <span class="cell-title">{{ b.subject === 'database' ? 'Database' : 'Volume' }}</span>
                  <span v-if="b.volume_name" class="cell-sub mono">{{ b.volume_name }}</span>
                </td>
                <td>
                  <span class="badge badge-dot" :class="statusBadge(b.status)" :title="b.error || ''">{{ b.status }}</span>
                </td>
                <td class="text-muted">{{ b.trigger }}</td>
                <td class="text-muted">{{ b.destination }}</td>
                <td class="text-muted">{{ fmtSize(b.size_bytes) }}</td>
                <td class="text-muted">{{ fmtDate(b.created_at) }}</td>
                <td class="text-right actions" @click.stop>
                  <button
                    v-if="canRestore(b)"
                    class="btn-icon btn-icon-muted"
                    title="Restore"
                    aria-label="Restore"
                    :disabled="restoringId === b.id"
                    @click="pendingRestore = b"
                  >
                    <span class="mdi mdi-backup-restore"></span>
                  </button>
                  <button class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingDelete = b"><span class="mdi mdi-delete"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="!!pendingRestore"
      title="Restore platform backup?"
      :message="restoreMessage"
      confirm-label="Restore"
      variant="danger"
      :busy="restoringId !== null"
      @confirm="restore"
      @cancel="pendingRestore = null"
    />

    <ConfirmDialog
      :open="!!pendingRestoreSet"
      title="Restore the selected artifacts?"
      :message="`This overwrites live data. ${selectedCount} artifact(s) will be restored into this platform: databases are dropped and recreated from the backup, and volumes are overwritten. Data written since ${pendingRestoreSet ? fmtDate(pendingRestoreSet.created_at) : 'the backup'} is lost for whatever you selected.`"
      confirm-label="Restore"
      variant="danger"
      :busy="restoring"
      @confirm="runSelectiveRestore"
      @cancel="pendingRestoreSet = null"
    />

    <ConfirmDialog
      :open="!!pendingSetDelete"
      title="Delete this recovery point?"
      message="Deletes the recovery point and its artifacts from the bucket, including the identity envelope. This cannot be undone."
      confirm-label="Delete"
      variant="danger"
      @confirm="removeSet"
      @cancel="pendingSetDelete = null"
    />

    <ConfirmDialog
      :open="pendingComplete"
      title="Complete recovery?"
      message="Schedules and certificate issuance resume. Do this only once DNS points at this host — otherwise certificate requests will be issued against an address that still resolves elsewhere."
      confirm-label="Complete recovery"
      :busy="completing"
      @confirm="completeRecovery"
      @cancel="pendingComplete = false"
    />

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete platform backup?"
      message="Delete this platform backup?"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="remove"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.locked {
  display: flex;
  align-items: center;
  gap: 14px;
}
.locked .mdi {
  font-size: 28px;
  color: var(--text-muted);
}
.locked p {
  margin: 0 0 8px;
  max-width: 64ch;
  color: var(--text-secondary, var(--text-muted));
}
.dr-note .card-body {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}
.dr-note .mdi {
  font-size: 24px;
  color: var(--warning, #d97706);
}
.dr-note code {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.grid2 {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.vol-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.sep {
  border: none;
  border-top: 1px solid var(--border, #2a2a2a);
  margin: 16px 0;
}
.actions-right {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
.cell-sub {
  display: block;
  font-size: 11px;
  color: var(--text-muted);
}

.section-title {
  margin: 0 0 12px;
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
}

/* Recovery banner — deliberately loud: a quiesced platform looks healthy and is
   not yet serving, and that has to be impossible to miss. */
.recovering {
  border-left: 3px solid var(--warning, #d29922);
}
.recovering-head {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}
.recovering-head .mdi {
  font-size: 22px;
  color: var(--warning, #d29922);
}
.recovering-head p {
  margin: 4px 0 0;
}
.recovering-actions {
  display: flex;
  gap: 8px;
  margin-top: 14px;
}
.recovery-report {
  margin-top: 16px;
  border-top: 1px solid var(--border, #2a2a2a);
  padding-top: 12px;
}
.recovery-counts {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  font-size: 12px;
  color: var(--text-muted);
}
.recovery-list {
  margin-top: 12px;
  font-size: 12px;
}
.recovery-list ul {
  margin: 4px 0 0;
  padding-left: 18px;
}
.recovery-list.danger strong {
  color: var(--danger, #f85149);
}

.ref {
  font-size: 12px;
}
.sub {
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-muted);
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}
.chip {
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 10px;
  padding: 1px 8px;
}
.chip-warn {
  border-color: var(--warning, #d29922);
  color: var(--warning, #d29922);
}

.verify {
  margin-top: 16px;
  border-top: 1px solid var(--border, #2a2a2a);
  padding-top: 12px;
}
.verify h4 {
  margin: 0 0 6px;
}
.verify-row {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 10px;
}
.verify-row .form-input {
  max-width: 380px;
}
.verify-result {
  margin-top: 12px;
  font-size: 12px;
}
.verify-result ul {
  margin: 8px 0 0;
  padding-left: 18px;
}
.verify-result li.error {
  color: var(--danger, #f85149);
}
.verify-result li.warning {
  color: var(--warning, #d29922);
}
.empty {
  color: var(--text-muted);
  font-size: 13px;
  padding: 8px 0;
}
.set-error {
  margin: 6px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--danger, #f85149);
  white-space: pre-wrap;
  word-break: break-word;
  max-width: 460px;
  max-height: 140px;
  overflow: auto;
  font-family: inherit;
}
.disc-set {
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 6px;
  margin-bottom: 8px;
}
.disc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 10px 12px;
  background: none;
  border: none;
  color: inherit;
  cursor: pointer;
  text-align: left;
}
.disc-body {
  padding: 0 12px 12px;
  border-top: 1px solid var(--border, #2a2a2a);
}
.disc-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  margin-top: 10px;
}
.disc-actions .form-input {
  max-width: 320px;
}
.row-off {
  opacity: 0.55;
}
.restore-report {
  margin-top: 12px;
  font-size: 12px;
}
.restore-report ul {
  margin: 6px 0 0;
  padding-left: 18px;
}
.restore-report li.error {
  color: var(--danger, #f85149);
}
.restore-report li.okline {
  color: var(--success, #3fb950);
}
.env-note {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  font-size: 12px;
  color: var(--text-muted);
  border: 1px solid var(--border, #2a2a2a);
  border-radius: 6px;
  padding: 10px 12px;
  margin-bottom: 14px;
}
.env-note .mdi {
  font-size: 16px;
}
</style>
