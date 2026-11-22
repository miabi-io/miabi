<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  registryApi,
  type AdminRegistryUsage,
  type RetentionPolicy,
  type RetentionRun,
  type RetentionWorkspaceReport,
  type RegistryLocks,
  type RegistryRuntime,
  type RegistrySettingsPayload,
} from '@/api/registry'
import { apiError as decodeApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import type { StorageClass } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { relativeTime } from '@/utils/time'

const notify = useNotificationStore()

type Tab = 'overview' | 'configuration' | 'storage' | 'usage' | 'retention' | 'maintenance'
const tab = ref<Tab>('overview')
const tabs: Array<{ id: Tab; label: string; icon: string }> = [
  { id: 'overview', label: 'Overview', icon: 'mdi-view-dashboard-outline' },
  { id: 'configuration', label: 'Configuration', icon: 'mdi-cog-outline' },
  { id: 'storage', label: 'Storage', icon: 'mdi-database-outline' },
  { id: 'usage', label: 'Usage', icon: 'mdi-chart-donut' },
  { id: 'retention', label: 'Retention', icon: 'mdi-history' },
  { id: 'maintenance', label: 'Maintenance', icon: 'mdi-broom' },
]

const loading = ref(true)
const saving = ref(false)
const runningGc = ref(false)

const effectiveHost = ref('')
const s3Entitled = ref(false)
const volumeName = ref('mb-registry-data')
const hostSource = ref<'env' | 'stored' | 'base_domain' | 'unset'>('unset')
const storageSource = ref<'env' | 'stored' | 'default'>('default')
const storageError = ref('')
const locks = ref<RegistryLocks>({
  enabled: false, host: false, storage: false, storage_class: false,
  s3_endpoint: false, s3_bucket: false, s3_region: false,
  s3_access_key: false, s3_secret_key: false, s3_force_path_style: false,
})
const s3SecretSet = ref(false)

// The server ignores env-pinned fields, so a locked field round-trips harmlessly.
const form = ref<Required<Omit<RegistrySettingsPayload, 'confirm'>>>({
  delete_enabled: false,
  per_workspace_quota_mb: 0,
  enabled: false,
  host: '',
  storage_type: 'filesystem',
  storage_class: '',
  s3_endpoint: '',
  s3_bucket: '',
  s3_region: '',
  s3_access_key: '',
  s3_secret_key: '',
  s3_force_path_style: false,
})

const usesS3 = computed(() => form.value.storage_type === 's3')

const storageClasses = ref<StorageClass[]>([])
// Disabled classes stay listed only when already selected, so the select can show the current value.
const classOptions = computed(() =>
  storageClasses.value.filter(c => !c.builtin && (c.enabled || c.name === form.value.storage_class)),
)
const selectedClass = computed(() => storageClasses.value.find(c => c.name === form.value.storage_class))
const dataVolume = computed(() =>
  form.value.storage_class ? `mb-registry-data-${form.value.storage_class}` : 'mb-registry-data',
)

async function loadStorageClasses() {
  try {
    storageClasses.value = (await adminApi.listStorageClasses()).data.data ?? []
  } catch {
    storageClasses.value = []
  }
}
const envManaged = computed(() => Object.values(locks.value).some(Boolean))

const hostOrigin = computed(() => {
  switch (hostSource.value) {
    case 'env': return 'Pinned by MIABI_REGISTRY_HOST.'
    case 'base_domain': return 'Derived from the external domain of the default cluster (registry.<domain>) — set a host here to override it.'
    case 'stored': return 'Set here.'
    default: return 'No usable hostname yet — set one here, or set an external domain on the default cluster.'
  }
})

// --- runtime -----------------------------------------------------------------

const runtime = ref<RegistryRuntime | null>(null)
const runtimeError = ref('')
let poll: ReturnType<typeof setInterval> | undefined

async function loadRuntime() {
  try {
    runtime.value = (await registryApi.runtime()).data.data
    runtimeError.value = ''
  } catch (e) {
    runtimeError.value = decodeApiError(e).message
    runtime.value = null
  }
}

// A stats sample costs the daemon two seconds of streaming, so poll only on the Overview tab.
function syncPolling() {
  if (poll) { clearInterval(poll); poll = undefined }
  if (tab.value === 'overview') {
    void loadRuntime()
    poll = setInterval(loadRuntime, 5000)
  }
}
function selectTab(id: Tab) {
  tab.value = id
  syncPolling()
  if (id === 'usage') void loadUsage()
  if (id === 'retention') void loadRetention()
}

const retention = ref<RetentionPolicy | null>(null)
const protectedText = ref('')
const savingRetention = ref(false)
const runs = ref<RetentionRun[]>([])
const report = ref<(RetentionRun & { workspaces: RetentionWorkspaceReport[] }) | null>(null)
const showRunConfirm = ref(false)
const startingRun = ref(false)
const workspaceNames = ref<Map<number, string>>(new Map())
let runPoll: ReturnType<typeof setTimeout> | undefined

async function loadRetention() {
  try {
    const [p, r] = await Promise.all([registryApi.retentionPolicy(), registryApi.retentionRuns()])
    retention.value = p.data.data
    protectedText.value = (p.data.data.protected_tags ?? []).join(', ')
    runs.value = r.data.data ?? []
    if (!report.value && runs.value[0]) await openRun(runs.value[0].id)
  } catch (e) {
    notify.apiError(e)
  }
  // Report rows name workspaces; the usage list is the cheap source of names.
  try {
    const u = (await registryApi.adminUsage()).data.data
    workspaceNames.value = new Map(u.workspaces.map((w) => [w.workspace_id, w.workspace]))
  } catch { /* ids are shown instead */ }
}

async function saveRetention() {
  if (!retention.value) return
  savingRetention.value = true
  try {
    const p = retention.value
    retention.value = (await registryApi.saveRetentionPolicy({
      enabled: p.enabled, keep_images: p.keep_images, min_age_days: p.min_age_days, release_window: p.release_window,
      protected_tags: protectedText.value.split(',').map((t) => t.trim()).filter(Boolean),
    })).data.data
    notify.success(retention.value.enabled ? 'Retention policy saved; it runs nightly at 03:30.' : 'Retention policy saved; it is off.')
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingRetention.value = false
  }
}

async function startRun(dryRun: boolean) {
  startingRun.value = true
  try {
    const run = (await registryApi.startRetention(dryRun)).data.data
    showRunConfirm.value = false
    await openRun(run.id)
    runs.value = [run, ...runs.value.filter((r) => r.id !== run.id)]
  } catch (e) {
    notify.apiError(e)
  } finally {
    startingRun.value = false
  }
}

// A run can take minutes across many workspaces, so its report fills in by polling.
async function openRun(id: number) {
  clearTimeout(runPoll)
  try {
    report.value = (await registryApi.retentionRun(id)).data.data
    if (report.value.status === 'running') {
      runPoll = setTimeout(() => openRun(id), 3000)
    } else {
      runs.value = runs.value.map((r) => (r.id === id ? { ...r, ...report.value! } : r))
    }
  } catch (e) {
    notify.apiError(e)
  }
}

const reportRows = computed(() => (report.value?.workspaces ?? []).filter((w) => w.count > 0 || w.skipped || w.repositories.some((r) => r.error)))
function wsName(id: number) {
  return workspaceNames.value.get(id) ?? `workspace #${id}`
}

const usage = ref<AdminRegistryUsage | null>(null)
const usageLoading = ref(false)
let usageRetry: ReturnType<typeof setTimeout> | undefined
// Workspaces counted for the first time come back pending, so look again shortly while any are.
async function loadUsage() {
  clearTimeout(usageRetry)
  usageLoading.value = true
  try {
    usage.value = (await registryApi.adminUsage()).data.data
    if (tab.value === 'usage' && usage.value.workspaces.some((w) => w.pending)) {
      usageRetry = setTimeout(loadUsage, 3000)
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    usageLoading.value = false
  }
}
const usageTotal = computed(() => (usage.value?.workspaces ?? []).reduce((sum, w) => sum + w.total_bytes, 0))
function quotaShare(bytes: number): string {
  const q = usage.value?.quota_bytes ?? 0
  return q > 0 ? `${Math.round((bytes / q) * 100)}%` : '—'
}

const statusLabel = computed(() => {
  const rt = runtime.value
  if (!rt) return 'Unknown'
  if (rt.running) return rt.health === 'unhealthy' ? 'Running (unhealthy)' : 'Running'
  if (!form.value.enabled) return 'Disabled'
  if (storageError.value) return 'Blocked'
  return rt.state ? `Not running (${rt.state})` : 'Not running'
})
const statusTone = computed(() => {
  const rt = runtime.value
  if (rt?.running) return rt.health === 'unhealthy' ? 'warn' : 'ok'
  if (!form.value.enabled) return 'muted'
  return 'bad'
})

function fmtBytes(n?: number): string {
  if (!n || n <= 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}
function fmtPercent(n?: number): string {
  return n === undefined || n === null ? '—' : `${n.toFixed(1)}%`
}
const uptime = computed(() => {
  const started = runtime.value?.started_at
  if (!started) return '—'
  const ms = Date.now() - new Date(started).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const m = Math.floor(ms / 60000)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
})

// --- load / save -------------------------------------------------------------

async function load() {
  loading.value = true
  try {
    const s = (await registryApi.getSettings()).data.data
    effectiveHost.value = s.effective_host
    s3Entitled.value = s.s3_entitled
    volumeName.value = s.volume_name || 'mb-registry-data'
    hostSource.value = s.host_source
    storageSource.value = s.storage_source ?? 'default'
    storageError.value = s.storage_error ?? ''
    locks.value = s.locks
    s3SecretSet.value = s.s3_secret_set
    form.value = {
      delete_enabled: s.delete_enabled,
      per_workspace_quota_mb: s.per_workspace_quota_mb,
      enabled: s.enabled,
      // Show the stored host: pre-filling the derived one would silently pin it on save.
      host: s.host ?? '',
      storage_type: s.storage_type,
      storage_class: s.storage_class ?? '',
      s3_endpoint: s.s3_endpoint ?? '',
      s3_bucket: s.s3_bucket ?? '',
      s3_region: s.s3_region ?? '',
      s3_access_key: s.s3_access_key ?? '',
      s3_secret_key: '',
      s3_force_path_style: s.s3_force_path_style,
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// Confirming re-sends the identical payload with confirm set.
const pendingConfirm = ref('')

async function save(confirm = false) {
  saving.value = true
  try {
    const payload: RegistrySettingsPayload = { ...form.value, confirm }
    const s = (await registryApi.updateSettings(payload)).data.data
    effectiveHost.value = s.effective_host
    hostSource.value = s.host_source
    storageSource.value = s.storage_source ?? 'default'
    storageError.value = s.storage_error ?? ''
    s3SecretSet.value = s.s3_secret_set
    volumeName.value = s.volume_name || 'mb-registry-data'
    form.value.s3_secret_key = ''
    pendingConfirm.value = ''
    notify.success('Registry settings saved')
    void loadRuntime()
  } catch (e) {
    const err = decodeApiError(e)
    if (err.status === 409 && !confirm) {
      pendingConfirm.value = err.message
    } else {
      notify.apiError(e)
    }
  } finally {
    saving.value = false
  }
}

const showGcConfirm = ref(false)
async function runGc() {
  showGcConfirm.value = false
  runningGc.value = true
  try {
    const res = (await registryApi.runGc()).data.data
    notify.success(res.message || 'Garbage collection complete')
  } catch (e) {
    notify.apiError(e)
  } finally {
    runningGc.value = false
  }
}

onMounted(async () => {
  await Promise.all([load(), loadStorageClasses()])
  syncPolling()
})
onBeforeUnmount(() => { if (poll) clearInterval(poll); clearTimeout(usageRetry); clearTimeout(runPoll) })
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Container Registry</h1>
        <p class="text-muted text-sm subtitle">
          A first-party, multi-tenant Docker registry. Members push &amp; pull with
          <code>docker login {{ effectiveHost || 'registry.&lt;domain&gt;' }}</code>.
        </p>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else>
      <div v-if="storageError" class="alert-banner" role="alert">
        <i class="mdi mdi-alert-circle-outline"></i>
        <p><strong>The registry is not running.</strong> {{ storageError }}</p>
      </div>

      <div class="tabs">
        <button
          v-for="t in tabs" :key="t.id" type="button" class="tab"
          :class="{ active: tab === t.id }" @click="selectTab(t.id)"
        >
          <i class="mdi" :class="t.icon"></i> {{ t.label }}
        </button>
      </div>

      <!-- ── Overview ─────────────────────────────────────────────────────── -->
      <div v-if="tab === 'overview'" class="card">
        <div class="card-body">
          <div class="status-row">
            <span class="status-dot" :class="statusTone"></span>
            <strong>{{ statusLabel }}</strong>
            <!-- Health is a second axis: a container can be up and failing its probe. -->
            <span v-if="runtime?.health" class="badge badge-muted">{{ runtime.health }}</span>
            <span v-if="runtime?.restart_count" class="badge badge-warning">
              {{ runtime.restart_count }} restart{{ runtime.restart_count === 1 ? '' : 's' }}
            </span>
          </div>
          <p v-if="runtimeError" class="form-hint">Live status unavailable: {{ runtimeError }}</p>

          <!-- The registry belongs to no workspace, so this is the only place its cost is visible. -->
          <div v-if="runtime?.running" class="stat-grid">
            <div class="stat">
              <span class="stat-label">CPU</span>
              <span class="stat-value">{{ fmtPercent(runtime.stats?.cpu_percent) }}</span>
            </div>
            <div class="stat">
              <span class="stat-label">Memory</span>
              <span class="stat-value">{{ fmtBytes(runtime.stats?.memory_usage_bytes) }}</span>
              <span class="stat-sub">
                {{ fmtPercent(runtime.stats?.memory_percent) }}
                <template v-if="runtime.stats?.memory_limit_bytes"> of {{ fmtBytes(runtime.stats.memory_limit_bytes) }}</template>
              </span>
            </div>
            <div class="stat">
              <span class="stat-label">Network</span>
              <span class="stat-value">↓ {{ fmtBytes(runtime.stats?.network_rx_bytes) }}</span>
              <span class="stat-sub">↑ {{ fmtBytes(runtime.stats?.network_tx_bytes) }}</span>
            </div>
            <div class="stat">
              <span class="stat-label">Uptime</span>
              <span class="stat-value">{{ uptime }}</span>
            </div>
          </div>
          <p v-if="runtime?.running && runtime.stats_error" class="form-hint">
            Resource usage unavailable: {{ runtime.stats_error }}
          </p>
          <p v-else-if="runtime && !runtime.running && form.enabled && !storageError" class="form-hint">
            The registry is enabled but its container is not running. Save the settings to start it, or check the
            control-plane logs.
          </p>

          <dl class="facts">
            <div><dt>Host</dt><dd class="mono">{{ effectiveHost || '—' }}</dd></div>
            <div><dt>Storage</dt><dd>{{ usesS3 ? `S3 / MinIO${form.s3_bucket ? ` (${form.s3_bucket})` : ''}` : `Local volume (${volumeName})` }}</dd></div>
            <div v-if="!usesS3"><dt>Storage class</dt><dd class="mono">{{ form.storage_class || 'default' }}</dd></div>
            <div v-if="runtime?.image"><dt>Image</dt><dd class="mono">{{ runtime.image }}</dd></div>
            <div><dt>Tag deletion</dt><dd>{{ form.delete_enabled ? 'Enabled' : 'Disabled' }}</dd></div>
            <div><dt>Per-workspace quota</dt><dd>{{ form.per_workspace_quota_mb ? `${form.per_workspace_quota_mb} MB` : 'Unlimited' }}</dd></div>
          </dl>
        </div>
      </div>

      <!-- ── Configuration ────────────────────────────────────────────────── -->
      <div v-else-if="tab === 'configuration'" class="card" style="max-width: 720px">
        <div class="card-body">
          <p v-if="envManaged" class="section-note">
            <i class="mdi mdi-lock-outline"></i>
            Some fields are pinned by <code>MIABI_REGISTRY_*</code> in this install's environment. They are shown
            here but can only be changed where they are declared.
          </p>

          <label class="toggle-row" :class="{ 'is-locked': locks.enabled }">
            <input v-model="form.enabled" type="checkbox" :disabled="locks.enabled" />
            <span>Enable the registry (runs the container and seeds its gateway route)</span>
          </label>
          <small class="form-hint">
            <template v-if="locks.enabled">Pinned by <code>MIABI_REGISTRY_ENABLED</code>.</template>
            <template v-else>Turning this off tears the container down; images in storage are kept.</template>
          </small>

          <div class="form-group" style="margin-top: 16px">
            <label class="form-label">Host</label>
            <input
              v-model="form.host" class="form-input mono" :disabled="locks.host"
              :placeholder="effectiveHost || 'registry.example.com'" style="max-width: 420px"
            />
            <small class="form-hint">
              The public hostname for <code>docker login</code>. {{ hostOrigin }}
              Every image reference Miabi records is anchored to it, so changing it after images exist asks for
              confirmation and needs affected apps redeployed.
            </small>
          </div>

          <div class="form-group">
            <label class="form-label">Per-workspace quota (MB)</label>
            <input v-model.number="form.per_workspace_quota_mb" type="number" min="0" class="form-input" style="max-width: 200px" />
            <small class="form-hint">0 = unlimited.</small>
          </div>

          <label class="toggle-row">
            <input v-model="form.delete_enabled" type="checkbox" />
            <span>Enable tag deletion &amp; garbage collection</span>
          </label>

          <div class="actions">
            <button class="btn btn-primary" :disabled="saving" @click="save()">
              {{ saving ? 'Saving…' : 'Save settings' }}
            </button>
          </div>
        </div>
      </div>

      <!-- ── Storage ──────────────────────────────────────────────────────── -->
      <div v-else-if="tab === 'storage'" class="card" style="max-width: 720px">
        <div class="card-body">
          <p class="section-note">
            Blobs <strong>do not migrate</strong> between backends or storage classes. Switching a registry that
            already holds images leaves them in the old one — the change asks for confirmation and tells you how much is affected.
          </p>

          <div class="form-group">
            <label class="form-label">Storage driver</label>
            <select v-model="form.storage_type" class="form-select" style="max-width: 280px" :disabled="locks.storage">
              <option value="filesystem">Local volume (filesystem)</option>
              <option value="s3" :disabled="!s3Entitled">S3 / MinIO{{ s3Entitled ? '' : ' — Enterprise' }}</option>
            </select>
            <small class="form-hint">
              <template v-if="locks.storage">Pinned by <code>MIABI_REGISTRY_STORAGE</code>.</template>
              <template v-else-if="!s3Entitled">S3/MinIO storage requires an Enterprise license; local storage is free.</template>
              <template v-else>Where every pushed blob lives.</template>
            </small>
          </div>

          <template v-if="!usesS3">
            <div class="form-group">
              <label class="form-label">Storage class</label>
              <select v-model="form.storage_class" class="form-select" style="max-width: 320px" :disabled="locks.storage_class">
                <option value="">Default (Docker data root)</option>
                <option v-for="c in classOptions" :key="c.id" :value="c.name">
                  {{ c.display_name || c.name }}{{ c.server_name ? ` — ${c.server_name}` : '' }}{{ c.enabled ? '' : ' (disabled)' }}
                </option>
              </select>
              <small class="form-hint">
                <template v-if="locks.storage_class">Pinned by <code>MIABI_REGISTRY_STORAGE_CLASS</code>.</template>
                <template v-else>
                  The disk the registry's blobs land on. It must be a class on the control-plane node, or a shared one.
                  Register disks under <router-link to="/admin/storage-classes">Storage classes</router-link>.
                </template>
                <template v-if="selectedClass?.path"> Path: <code>{{ selectedClass.path }}/{{ dataVolume }}</code>.</template>
              </small>
            </div>

            <div class="form-group">
              <label class="form-label">Data volume</label>
              <input :value="dataVolume" class="form-input mono" style="max-width: 320px" disabled />
              <small class="form-hint">Derived from the storage class. Each class gets its own volume, and the old one is kept.</small>
            </div>
          </template>

          <fieldset v-else class="s3-fields">
            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Bucket</label>
                <input v-model="form.s3_bucket" class="form-input mono" :disabled="locks.s3_bucket" placeholder="miabi-registry" />
              </div>
              <div class="form-group">
                <label class="form-label">Region</label>
                <input v-model="form.s3_region" class="form-input mono" :disabled="locks.s3_region" placeholder="us-east-1" />
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">Endpoint <span class="text-muted">(S3-compatible / MinIO)</span></label>
              <input v-model="form.s3_endpoint" class="form-input mono" :disabled="locks.s3_endpoint" placeholder="Leave blank for AWS S3" />
            </div>
            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Access key</label>
                <input v-model="form.s3_access_key" class="form-input mono" :disabled="locks.s3_access_key" autocomplete="off" />
              </div>
              <div class="form-group">
                <label class="form-label">Secret key</label>
                <input
                  v-model="form.s3_secret_key" type="password" class="form-input mono"
                  :disabled="locks.s3_secret_key" autocomplete="new-password"
                  :placeholder="s3SecretSet ? '••••• (set — leave blank to keep)' : 'Not set'"
                />
              </div>
            </div>
            <label class="toggle-row" :class="{ 'is-locked': locks.s3_force_path_style }">
              <input v-model="form.s3_force_path_style" type="checkbox" :disabled="locks.s3_force_path_style" />
              <span>Force path-style URLs (MinIO and some S3-compatible stores)</span>
            </label>
            <small class="form-hint">
              Pinned fields come from <code>MIABI_REGISTRY_S3_*</code>.
            </small>
          </fieldset>

          <div class="actions">
            <button class="btn btn-primary" :disabled="saving" @click="save()">
              {{ saving ? 'Saving…' : 'Save storage' }}
            </button>
          </div>
        </div>
      </div>

      <div v-else-if="tab === 'usage'" class="card">
        <div class="card-header usage-header">
          <div>
            <h2>Usage by workspace</h2>
            <p class="section-note" style="margin: 2px 0 0">
              {{ fmtBytes(usageTotal) }} referenced in total<template v-if="usage?.quota_bytes">, against a
              {{ fmtBytes(usage.quota_bytes) }} quota per workspace</template>. Layers shared between workspaces count
              once per workspace, so the disk holds less than the sum.
            </p>
          </div>
          <button class="btn btn-secondary btn-sm" :disabled="usageLoading" @click="loadUsage">
            <span class="mdi mdi-refresh" :class="{ 'mdi-spin': usageLoading }"></span> Refresh
          </button>
        </div>
        <div v-if="!usage" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="usage.workspaces.length === 0" class="card-body text-muted">No workspace has pushed an image yet.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Workspace</th><th>Used</th><th>Of quota</th><th>Repositories</th><th>Counted</th></tr>
            </thead>
            <tbody>
              <tr v-for="w in usage.workspaces" :key="w.workspace_id">
                <td><router-link :to="`/admin/workspaces/${w.workspace_id}`">{{ w.workspace }}</router-link></td>
                <td>{{ w.pending ? 'Counting…' : w.total_bytes ? fmtBytes(w.total_bytes) : '0 B' }}</td>
                <td>{{ w.pending ? '—' : quotaShare(w.total_bytes) }}</td>
                <td>{{ w.pending ? '—' : w.repositories }}</td>
                <td class="text-muted">{{ w.computed_at ? relativeTime(w.computed_at) : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <template v-else-if="tab === 'retention'">
        <div class="card mb-4" style="max-width: 720px">
          <div class="card-body">
            <h3 class="section-title">Retention policy</h3>
            <p class="section-note">
              Deletes old images from every workspace's registry, keeping the newest ones per repository. Tags on one image
              count as one. It runs nightly at 03:30, before garbage collection reclaims the space at 04:00.
            </p>
            <div v-if="!retention" class="spinner"></div>
            <template v-else>
              <label class="toggle-row">
                <input v-model="retention.enabled" type="checkbox" />
                <span>Run the policy every night</span>
              </label>
              <div class="form-grid">
                <div class="form-group">
                  <label class="form-label">Keep the newest images per repository</label>
                  <input v-model.number="retention.keep_images" type="number" min="1" class="form-input" />
                </div>
                <div class="form-group">
                  <label class="form-label">Never delete images younger than (days)</label>
                  <input v-model.number="retention.min_age_days" type="number" min="0" class="form-input" />
                </div>
                <div class="form-group">
                  <label class="form-label">Keep the images of each app's last releases</label>
                  <input v-model.number="retention.release_window" type="number" min="0" class="form-input" />
                  <small class="form-hint">So rolling back to a recent release still finds its image.</small>
                </div>
                <div class="form-group">
                  <label class="form-label">Protected tags</label>
                  <input v-model="protectedText" class="form-input mono" placeholder="latest, v*.*.*" />
                  <small class="form-hint">Comma-separated globs; an image carrying a matching tag is never deleted.</small>
                </div>
              </div>
              <p class="form-hint">
                Images in use by a live deployment or a pinned release, and images with no known build time, are never deleted.
                Workspace admins can opt their workspace out or keep more images.
              </p>
              <div class="actions">
                <button class="btn btn-primary" :disabled="savingRetention" @click="saveRetention">
                  {{ savingRetention ? 'Saving…' : 'Save policy' }}
                </button>
                <button class="btn btn-secondary" :disabled="startingRun" @click="startRun(true)">Preview</button>
                <button class="btn btn-danger" :disabled="startingRun || !form.delete_enabled" @click="showRunConfirm = true">Run now</button>
              </div>
              <p v-if="!form.delete_enabled" class="form-hint">Enable tag deletion under Configuration first.</p>
            </template>
          </div>
        </div>

        <div v-if="report" class="card mb-4">
          <div class="card-header usage-header">
            <div>
              <h2>{{ report.dry_run ? 'Preview' : 'Run' }} #{{ report.id }}</h2>
              <p class="section-note" style="margin: 2px 0 0">
                <template v-if="report.status === 'running'"><span class="mdi mdi-loading mdi-spin"></span> Running…</template>
                <template v-else-if="report.status === 'failed'">Failed: {{ report.error }}</template>
                <template v-else>
                  {{ report.dry_run ? 'Would delete' : 'Deleted' }} {{ report.tags }} tag(s), freeing about
                  {{ fmtBytes(report.freed_bytes) }}{{ report.dry_run ? '' : ' at the next garbage collection' }}.
                </template>
              </p>
            </div>
          </div>
          <div v-if="report.status !== 'running' && reportRows.length === 0" class="card-body text-muted">
            Nothing to delete: every repository is within the policy.
          </div>
          <div v-else-if="reportRows.length" class="table-wrapper">
            <table>
              <thead><tr><th>Workspace</th><th>Kept per repository</th><th>Tags</th><th>Frees</th><th>Repositories</th></tr></thead>
              <tbody>
                <tr v-for="w in reportRows" :key="w.workspace_id">
                  <td><router-link :to="`/admin/workspaces/${w.workspace_id}`">{{ wsName(w.workspace_id) }}</router-link></td>
                  <td>{{ w.skipped ? 'Opted out' : w.keep }}</td>
                  <td>{{ w.skipped ? '—' : w.count }}</td>
                  <td>{{ w.skipped ? '—' : fmtBytes(w.freed_bytes) }}</td>
                  <td class="text-sm">
                    <div v-for="r in w.repositories" :key="r.repository">
                      <code class="mono">{{ r.repository }}</code>: {{ r.count }}
                      <span v-if="r.tags.length" class="text-muted">({{ r.tags.slice(0, 6).join(', ') }}<template v-if="r.count > 6">, …</template>)</span>
                      <span v-if="r.error" class="text-danger"> — {{ r.error }}</span>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div v-if="runs.length" class="card">
          <div class="card-header"><h2>Recent runs</h2></div>
          <div class="table-wrapper">
            <table>
              <thead><tr><th>#</th><th>Kind</th><th>Started</th><th>Status</th><th>Tags</th><th>Frees</th></tr></thead>
              <tbody>
                <tr v-for="r in runs" :key="r.id" class="clickable" @click="openRun(r.id)">
                  <td>{{ r.id }}</td>
                  <td>{{ r.dry_run ? 'Preview' : r.trigger === 'schedule' ? 'Nightly' : 'Run now' }}</td>
                  <td class="text-muted">{{ relativeTime(r.started_at) }}</td>
                  <td>{{ r.status }}</td>
                  <td>{{ r.tags }}</td>
                  <td>{{ fmtBytes(r.freed_bytes) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>

      <!-- ── Maintenance ──────────────────────────────────────────────────── -->
      <div v-else class="card" style="max-width: 720px">
        <div class="card-body">
          <h3 class="section-title">Garbage collection</h3>
          <p class="section-note">
            Reclaims the space held by deleted and overwritten manifests. The registry switches to read-only while
            it runs — pulls keep working, pushes pause.
          </p>
          <p v-if="!form.delete_enabled" class="form-hint">
            Enable tag deletion under <a href="#" @click.prevent="selectTab('configuration')">Configuration</a> first —
            there is nothing to collect until deletes are allowed.
          </p>
          <p v-else-if="!form.enabled" class="form-hint">The registry is disabled.</p>
          <div class="actions">
            <button
              class="btn btn-secondary"
              :disabled="runningGc || !form.enabled || !form.delete_enabled || !!storageError"
              @click="showGcConfirm = true"
            >
              {{ runningGc ? 'Collecting…' : 'Run garbage collection' }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="!!pendingConfirm"
      :title="$t('confirm.title.confirmThisChange')"
      :message="pendingConfirm"
      :confirm-label="$t('action.changeItAnyway')"
      variant="danger"
      :busy="saving"
      @confirm="save(true)"
      @cancel="pendingConfirm = ''"
    />

    <ConfirmDialog
      :open="showRunConfirm"
      title="Run the retention policy now?"
      message="Old images are deleted from every workspace that hasn't opted out. Run a preview first to see what goes. This can't be undone."
      confirm-label="Run now"
      variant="danger"
      :busy="startingRun"
      @confirm="startRun(false)"
      @cancel="showRunConfirm = false"
    />

    <ConfirmDialog
      :open="showGcConfirm"
      :title="$t('confirm.title.runGarbageCollection')"
      :message="$t('confirm.message.registry.runGarbageCollectionThe')"
      :confirm-label="$t('action.runGarbageCollection')"
      variant="primary"
      :busy="runningGc"
      @confirm="runGc"
      @cancel="showGcConfirm = false"
    />
  </div>
</template>

<style scoped>
.subtitle { margin: 4px 0 0; }
.tabs { margin-bottom: 16px; }
.usage-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
tr.clickable { cursor: pointer; }
tr.clickable:hover { background: var(--bg-tertiary); }
.actions { display: flex; gap: 10px; margin-top: 20px; flex-wrap: wrap; }
.toggle-row { display: flex; align-items: center; gap: 8px; cursor: pointer; color: var(--text-primary); }
.toggle-row input { width: auto; margin: 0; }
/* A locked toggle is not clickable, so it must not present as one. */
.toggle-row.is-locked { cursor: default; color: var(--text-muted); }
.s3-fields { border: 0; padding: 0; margin: 0 0 8px; min-width: 0; }
.form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px 16px; }
.form-hint { display: block; font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.alert-banner {
  display: flex; align-items: flex-start; gap: 10px; flex-wrap: wrap;
  padding: 12px 16px; margin-bottom: 16px;
  border: 1px solid var(--danger-500); border-radius: var(--radius-lg);
  background: var(--danger-50); font-size: 13px; color: var(--text-secondary);
}
.alert-banner .mdi { font-size: 20px; flex-shrink: 0; color: var(--danger-600); }
.alert-banner strong { color: var(--danger-600); }
.alert-banner p { margin: 0; flex: 1; min-width: 200px; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mono { font-family: monospace; }
.section-title { margin: 0 0 4px; font-size: 14px; font-weight: 600; color: var(--text-primary); }
.section-note { margin: 0 0 16px; font-size: 12px; color: var(--text-muted); }

.status-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 16px; }
.status-dot { width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0; background: var(--text-muted); }
.status-dot.ok { background: var(--success-500, #22c55e); }
.status-dot.warn { background: var(--warning-500, #f59e0b); }
.status-dot.bad { background: var(--danger-500, #ef4444); }
.status-dot.muted { background: var(--text-muted); }

.stat-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
  gap: 12px; margin-bottom: 20px;
}
.stat {
  display: flex; flex-direction: column; gap: 2px;
  padding: 12px 14px; border: 1px solid var(--border-primary); border-radius: var(--radius-lg);
}
.stat-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.stat-value { font-size: 18px; font-weight: 600; color: var(--text-primary); }
.stat-sub { font-size: 12px; color: var(--text-muted); }

.facts { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 8px 24px; margin: 0; }
.facts > div { display: flex; justify-content: space-between; gap: 12px; padding: 6px 0; border-bottom: 1px solid var(--border-primary); }
.facts dt { font-size: 13px; color: var(--text-muted); }
.facts dd { margin: 0; font-size: 13px; color: var(--text-primary); text-align: right; overflow-wrap: anywhere; }
</style>
