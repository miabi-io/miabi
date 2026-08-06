<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  registryApi,
  type RegistryLocks,
  type RegistryRuntime,
  type RegistrySettingsPayload,
} from '@/api/registry'
import { apiError as decodeApiError } from '@/api/client'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()

type Tab = 'overview' | 'configuration' | 'storage' | 'maintenance'
const tab = ref<Tab>('overview')
const tabs: Array<{ id: Tab; label: string; icon: string }> = [
  { id: 'overview', label: 'Overview', icon: 'mdi-view-dashboard-outline' },
  { id: 'configuration', label: 'Configuration', icon: 'mdi-cog-outline' },
  { id: 'storage', label: 'Storage', icon: 'mdi-database-outline' },
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
  enabled: false, host: false, storage: false,
  s3_endpoint: false, s3_bucket: false, s3_region: false,
  s3_access_key: false, s3_secret_key: false, s3_force_path_style: false,
})
const s3SecretSet = ref(false)

// The whole editable surface. Sent as one payload — the server ignores whatever
// the environment pins, so a locked field round-trips harmlessly.
const form = ref<Required<Omit<RegistrySettingsPayload, 'confirm'>>>({
  delete_enabled: false,
  per_workspace_quota_mb: 0,
  enabled: false,
  host: '',
  storage_type: 'filesystem',
  s3_endpoint: '',
  s3_bucket: '',
  s3_region: '',
  s3_access_key: '',
  s3_secret_key: '',
  s3_force_path_style: false,
})

const usesS3 = computed(() => form.value.storage_type === 's3')
const envManaged = computed(() => Object.values(locks.value).some(Boolean))

const hostOrigin = computed(() => {
  switch (hostSource.value) {
    case 'env': return 'Pinned by MIABI_REGISTRY_HOST.'
    case 'base_domain': return 'Derived from the external base domain (registry.<domain>) — set a host here to override it.'
    case 'stored': return 'Set here.'
    default: return 'No usable hostname yet — set one here, or configure an external base domain.'
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

// Polled only while the Overview tab is open: a stats sample costs the daemon two
// seconds of streaming, and nothing else on the page reads it.
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
      // Show the stored host, not the derived one: editing a field pre-filled
      // with a value you never set would silently pin the derived name.
      host: s.host ?? '',
      storage_type: s.storage_type,
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

// The server answers 409 with the sentence to show when a change strands data;
// confirming re-sends the identical payload with confirm set.
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
  await load()
  syncPolling()
})
onBeforeUnmount(() => { if (poll) clearInterval(poll) })
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

          <!-- Usage is the point of this tab: the registry belongs to no workspace,
               so this is the only place its cost is visible. -->
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
            Blobs <strong>do not migrate</strong> between backends. Switching a registry that already holds images
            leaves them in the old one — the change asks for confirmation and tells you how much is affected.
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

          <div v-if="!usesS3" class="form-group">
            <label class="form-label">Data volume</label>
            <input :value="volumeName" class="form-input mono" style="max-width: 320px" disabled />
            <small class="form-hint">A fixed platform name, so the data location stays predictable.</small>
          </div>

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
      title="Confirm this change"
      :message="pendingConfirm"
      confirm-label="Change it anyway"
      variant="danger"
      :busy="saving"
      @confirm="save(true)"
      @cancel="pendingConfirm = ''"
    />

    <ConfirmDialog
      :open="showGcConfirm"
      title="Run garbage collection"
      message="Run garbage collection? The registry switches to read-only (pulls keep working, pushes pause) while it reclaims space."
      confirm-label="Run garbage collection"
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
