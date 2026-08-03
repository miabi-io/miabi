<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { registryApi, type RegistrySettingsPayload } from '@/api/registry'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()

const loading = ref(true)
const saving = ref(false)
const runningGc = ref(false)
const effectiveHost = ref('')
const s3Entitled = ref(false)

const volumeName = ref('mb-registry-data')


const enabled = ref(false)
const hostSource = ref<'env' | 'stored' | 'base_domain' | 'unset'>('unset')

// Storage read-out (never edited here).
const storage = ref({
  storage_type: 'filesystem' as 'filesystem' | 's3',
  s3_endpoint: '',
  s3_bucket: '',
  s3_region: '',
  s3_access_key: '',
  s3_force_path_style: false,
  s3_secret_set: false,
  storage_source: 'default' as 'env' | 'stored' | 'default',
  storage_error: '',
})

const usesS3 = computed(() => storage.value.storage_type === 's3')

const hostOrigin = computed(() => {
  switch (hostSource.value) {
    case 'env':
      return 'Set by MIABI_REGISTRY_HOST.'
    case 'base_domain':
      return 'Derived from the external base domain (registry.<domain>).'
    case 'stored':
      return 'A value saved before the host became environment-only. Set MIABI_REGISTRY_HOST to move it.'
    default:
      return 'No usable hostname — set MIABI_REGISTRY_HOST or an external base domain.'
  }
})

const storageOrigin = computed(() => {
  switch (storage.value.storage_source) {
    case 'env':
      return 'Set by MIABI_REGISTRY_STORAGE.'
    case 'stored':
      return 'A value saved before storage became environment-only. Set MIABI_REGISTRY_STORAGE to change it.'
    default:
      return 'The default — set MIABI_REGISTRY_STORAGE to change it.'
  }
})

const form = ref<RegistrySettingsPayload>({
  delete_enabled: false,
  per_workspace_quota_mb: 0,
})

async function load() {
  loading.value = true
  try {
    const s = (await registryApi.getSettings()).data.data
    effectiveHost.value = s.effective_host
    s3Entitled.value = s.s3_entitled
    volumeName.value = s.volume_name || 'mb-registry-data'
    enabled.value = s.enabled
    hostSource.value = s.host_source
    storage.value = {
      storage_type: s.storage_type,
      s3_endpoint: s.s3_endpoint ?? '',
      s3_bucket: s.s3_bucket ?? '',
      s3_region: s.s3_region ?? '',
      s3_access_key: s.s3_access_key ?? '',
      s3_force_path_style: s.s3_force_path_style,
      s3_secret_set: s.s3_secret_set,
      storage_source: s.storage_source ?? 'default',
      storage_error: s.storage_error ?? '',
    }
    form.value = {
      delete_enabled: s.delete_enabled,
      per_workspace_quota_mb: s.per_workspace_quota_mb,
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const s = (await registryApi.updateSettings(form.value)).data.data
    effectiveHost.value = s.effective_host
    hostSource.value = s.host_source
    notify.success('Registry settings saved')
  } catch (e) {
    notify.apiError(e)
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

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Container Registry</h1>
    </div>

    <div v-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <div v-else class="card" style="max-width: 720px">
      <div class="card-header">
        <div>
          <h2>Built-in registry</h2>
          <p class="text-muted text-sm" style="margin: 4px 0 0">
            A first-party, multi-tenant Docker registry. Members push & pull with
            <code>docker login {{ effectiveHost || 'registry.&lt;domain&gt;' }}</code>.
          </p>
        </div>
      </div>
      <div class="card-body">
        <h3 class="section-title">
          Platform configuration
          <span class="lock-pill"><i class="mdi mdi-lock-outline"></i> environment-only</span>
        </h3>
        <p class="section-note">
          Set with <code>MIABI_REGISTRY_*</code> and applied at boot. Shown here so you can see what the registry
          is actually running with; a change takes an environment edit and a restart.
        </p>

        <label class="toggle-row is-locked">
          <input :checked="enabled" type="checkbox" disabled />
          <span>Enable the registry (runs the container and seeds its gateway route)</span>
        </label>
        <small class="form-hint">
          Set by <code>MIABI_REGISTRY_ENABLED={{ enabled ? 'true' : 'false' }}</code>. Fixed by the platform —
          change it and restart Miabi.
        </small>

        <div class="form-group" style="margin-top: 16px">
          <label class="form-label">Host</label>
          <input :value="effectiveHost" class="form-input mono" placeholder="—" style="max-width: 420px" disabled />
          <small class="form-hint">
            Public hostname for docker login. {{ hostOrigin }} Fixed by the platform: every image reference
            Miabi stores is anchored to this hostname, and matching against it is how a workspace's own
            images are told apart from another's — so it cannot move while apps are running.
          </small>
        </div>

        <div v-if="storage.storage_error" class="alert-banner" role="alert">
          <i class="mdi mdi-alert-circle-outline"></i>
          <p><strong>The registry is not running.</strong> {{ storage.storage_error }}</p>
        </div>

        <div class="form-group">
          <label class="form-label">Storage driver</label>
          <input
            :value="usesS3 ? 'S3 / MinIO' : 'Local volume (filesystem)'"
            class="form-input"
            style="max-width: 280px"
            disabled
          />
          <small class="form-hint">
            {{ storageOrigin }} Fixed by the platform: every pushed blob lives in this backend, and drivers
            <strong>do not migrate</strong> — switching one under a running registry would orphan every image
            already stored. Change it in the environment and restart Miabi.
            <template v-if="!s3Entitled"> S3/MinIO storage requires an Enterprise license; local storage is free.</template>
          </small>
        </div>

        <div v-if="!usesS3" class="form-group">
          <label class="form-label">Data volume</label>
          <input :value="volumeName" class="form-input mono" style="max-width: 320px" disabled />
          <small class="form-hint">The managed volume images are stored in. Fixed by the platform.</small>
        </div>

        <fieldset v-else class="s3-fields">
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label">Bucket</label>
              <input :value="storage.s3_bucket || '—'" class="form-input mono" disabled />
            </div>
            <div class="form-group">
              <label class="form-label">Region</label>
              <input :value="storage.s3_region || '—'" class="form-input mono" disabled />
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">Endpoint <span class="text-muted">(S3-compatible / MinIO)</span></label>
            <input :value="storage.s3_endpoint || '— (AWS S3)'" class="form-input mono" disabled />
          </div>
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label">Access key</label>
              <input :value="storage.s3_access_key || '—'" class="form-input mono" disabled />
            </div>
            <div class="form-group">
              <label class="form-label">Secret key</label>
              <input :value="storage.s3_secret_set ? '••••• (set)' : '— (not set)'" class="form-input mono" disabled />
            </div>
          </div>
          <label class="toggle-row is-locked">
            <input :checked="storage.s3_force_path_style" type="checkbox" disabled />
            <span>Force path-style URLs (MinIO and some S3-compatible stores)</span>
          </label>
          <small class="form-hint">
            Read-only: set by <code>MIABI_REGISTRY_S3_BUCKET</code>, <code>_REGION</code>, <code>_ENDPOINT</code>,
            <code>_ACCESS_KEY</code>, <code>_SECRET_KEY</code>, <code>_FORCE_PATH_STYLE</code>. Restart Miabi to apply
            a change.
          </small>
        </fieldset>

        <h3 class="section-title" style="margin-top: 28px">Settings</h3>

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
          <button class="btn btn-primary" :disabled="saving" @click="save">
            {{ saving ? 'Saving…' : 'Save settings' }}
          </button>
          <!-- Nothing to collect while the registry is down for a storage reason. -->
          <button
            v-if="enabled && form.delete_enabled && !storage.storage_error"
            class="btn btn-secondary"
            :disabled="runningGc"
            @click="showGcConfirm = true"
          >
            {{ runningGc ? 'Collecting…' : 'Run garbage collection' }}
          </button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :open="showGcConfirm"
      title="Run garbage collection"
      :message="`Run garbage collection? The registry switches to read-only (pulls keep working, pushes pause) while it reclaims space.`"
      confirm-label="Run garbage collection"
      variant="primary"
      :busy="runningGc"
      @confirm="runGc"
      @cancel="showGcConfirm = false"
    />
  </div>
</template>

<style scoped>
.actions { display: flex; gap: 10px; margin-top: 20px; flex-wrap: wrap; }
.toggle-row { display: flex; align-items: center; gap: 8px; cursor: pointer; color: var(--text-primary); }
.toggle-row input { width: auto; margin: 0; }
.s3-fields { border: 0; padding: 0; margin: 0 0 8px; min-width: 0; }
.form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px 16px; }
.form-hint { display: block; font-size: 12px; color: var(--text-muted); margin-top: 4px; }
/* A locked toggle is not clickable, so it must not present as one. */
.toggle-row.is-locked { cursor: default; color: var(--text-muted); }
.alert-banner {
  display: flex; align-items: flex-start; gap: 10px; flex-wrap: wrap;
  padding: 12px 16px; margin-bottom: 20px;
  border: 1px solid var(--danger-500); border-radius: var(--radius-lg);
  background: var(--danger-50); font-size: 13px; color: var(--text-secondary);
}
.alert-banner .mdi { font-size: 20px; flex-shrink: 0; color: var(--danger-600); }
.alert-banner strong { color: var(--danger-600); }
.alert-banner p { margin: 0; flex: 1; min-width: 200px; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mono { font-family: monospace; }
.section-title {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  margin: 0 0 4px; font-size: 14px; font-weight: 600; color: var(--text-primary);
}
.section-note { margin: 0 0 16px; font-size: 12px; color: var(--text-muted); }
.lock-pill {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 2px 8px; border-radius: 999px;
  border: 1px solid var(--border-primary); background: var(--bg-tertiary);
  font-size: 11px; font-weight: 500; color: var(--text-muted);
}
.lock-pill .mdi { font-size: 13px; }
</style>
