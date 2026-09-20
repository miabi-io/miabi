<script setup lang="ts">
import LocationPicker from '@/components/LocationPicker.vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { volumeApi, usageApi } from '@/api/resources'
import type { Volume, WorkspaceUsage, WorkspaceStorage, StorageClassOption } from '@/api/types'
import { fmtSize } from '@/utils/format'
import { relativeTime } from '@/utils/time'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const volumes = ref<Volume[]>([])
// Client-side filter over the list (matches name, docker name, node, driver, storage class).
const search = ref('')
const filteredVolumes = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return volumes.value
  return volumes.value.filter((v) =>
    [v.display_name, v.name, v.docker_name, v.server_name, v.driver, v.storage_class]
      .some((f) => (f || '').toLowerCase().includes(q)),
  )
})
const usage = ref<WorkspaceUsage | null>(null)
const storage = ref<WorkspaceStorage | null>(null)
// Shared storage (NFS/CIFS) is a plan capability — only constrains the UI when
// enforcement is on; otherwise the server allows any driver.
const sharedAllowed = computed(() => !usage.value || !usage.value.enforced || usage.value.capabilities.shared_storage)
// Host-path volumes need the privileged-host-mount capability (the workspace must
// also carry the platform-admin privileged flag — enforced server-side).
const hostAllowed = computed(() => !usage.value || !usage.value.enforced || usage.value.capabilities.privileged_host_mounts)
const loading = ref(false)
const showCreate = ref(false)
const creating = ref(false)
const name = ref('')
const serverId = ref(0)
const volLocation = ref('')
const sizeMb = ref<number | null>(null)
// local = node-local (rwo); nfs/cifs = shared (rwx) for replicated cluster apps;
// host = bind an operator-managed /mnt/* path present on every node (rwx).
const driver = ref<'local' | 'nfs' | 'cifs' | 'host'>('local')
const nfsServer = ref('')
const nfsExport = ref('')
const cifsShare = ref('')
const cifsUser = ref('')
const cifsPass = ref('')
const hostPath = ref('')
// The storage classes this workspace may use, i.e. which of the operator's disks a volume can land
// on. Empty means the install registered none and Docker's own directory is the only option.
const storageClasses = ref<StorageClassOption[]>([])
const storageClass = ref('')
// name -> display name, for the Storage column.
const classLabels = ref<Record<string, string>>({})

async function load(id: number | null) {
  if (!id) { volumes.value = []; usage.value = null; storage.value = null; return }
  loading.value = true
  try {
    volumes.value = (await volumeApi.list(id)).data.data ?? []
    void loadStorageClasses()
    usage.value = (await usageApi.get(id)).data.data
    // Non-critical; never block the volume list on it.
    storage.value = (await volumeApi.storage(id).catch(() => null))?.data.data ?? null
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// Percent of the plan cap the workspace's measured usage occupies (null when
// unlimited or unmeasured), for the summary bar.
const storagePct = computed(() => {
  const s = storage.value
  if (!s || s.limit_mb < 0 || s.limit_mb === 0) return null
  return Math.min(100, Math.round((s.used_bytes / (s.limit_mb * 1024 * 1024)) * 100))
})
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  name.value = ''
  serverId.value = 0
  volLocation.value = ''
  sizeMb.value = null
  driver.value = 'local'
  nfsServer.value = ''; nfsExport.value = ''
  cifsShare.value = ''; cifsUser.value = ''; cifsPass.value = ''
  hostPath.value = ''
  storageClass.value = ''
  showCreate.value = true
  void loadStorageClasses()
}

// The offered classes depend on the node, so they are reloaded when the location changes. A failure
// is not fatal: the field simply disappears and the create falls back to the install's default.
async function loadStorageClasses() {
  if (!currentWorkspaceId.value) return
  try {
    const res = await volumeApi.storageClasses(currentWorkspaceId.value, volLocation.value || undefined)
    storageClasses.value = res.data.data ?? []
    for (const c of storageClasses.value) classLabels.value[c.name] = c.display_name || c.name
  } catch {
    storageClasses.value = []
  }
}
watch(volLocation, () => { if (showCreate.value) void loadStorageClasses() })

// A volume stores its class by name; the readable label lives on the class. A class the workspace
// can no longer be offered — disabled, or dropped from its plan — simply shows its name.
function classLabel(name?: string): string {
  if (!name) return '—'
  return classLabels.value[name] || name
}

// Docker mount options for the selected shared-storage backend (undefined for
// a node-local volume).
function driverOpts(): Record<string, string> | undefined {
  if (driver.value === 'nfs') {
    return { o: `addr=${nfsServer.value.trim()},rw`, device: `:${nfsExport.value.trim()}` }
  }
  if (driver.value === 'cifs') {
    const o = [`username=${cifsUser.value.trim()}`, cifsPass.value ? `password=${cifsPass.value}` : '', 'vers=3.0']
      .filter(Boolean)
      .join(',')
    return { o, device: cifsShare.value.trim() }
  }
  if (driver.value === 'host') {
    return { path: hostPath.value.trim() }
  }
  return undefined
}

async function create() {
  if (!currentWorkspaceId.value) return
  creating.value = true
  try {
    const d = driver.value === 'local' ? undefined : driver.value
    await volumeApi.create(currentWorkspaceId.value, name.value.trim(), serverId.value || undefined, sizeMb.value ?? undefined, d, driverOpts(), volLocation.value || undefined, storageClass.value || undefined)
    notify.success(t('notify.volumes.created'))
    showCreate.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

function fmtDate(s?: string) {
  return s ? new Date(s).toLocaleDateString() : '—'
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('volumes.volumes') }}</h1>
        <p class="subtitle">Persistent storage you can attach to {{ ws.contextLabel }}'s applications and databases.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('volumes.newVolume') }}</button>
    </div>

    <div v-if="storage && storage.volume_count > 0" class="card storage-summary">
      <div class="storage-row">
        <div class="storage-figure">
          <span class="storage-used">{{ fmtSize(storage.used_bytes) }}</span>
          <span class="storage-of">{{ $t('volumes.used') }}</span>
          <span class="storage-declared">· {{ fmtSize(storage.declared_bytes) }} declared<template v-if="storage.limit_mb >= 0"> · {{ fmtSize(storage.limit_mb * 1024 * 1024) }} limit</template></span>
        </div>
        <span v-if="storage.measured_at" class="storage-measured" :title="storage.measured_at">measured {{ relativeTime(storage.measured_at) }}</span>
        <span v-else class="storage-measured">{{ $t('volumes.notYetMeasured') }}</span>
      </div>
      <div v-if="storagePct !== null" class="storage-bar" :title="`${storagePct}% of limit`">
        <div class="storage-bar-fill" :class="{ 'storage-bar-warn': storagePct >= 80 }" :style="{ width: storagePct + '%' }"></div>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && volumes.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="volumes.length === 0" class="empty-state">
        <span class="mdi mdi-harddisk" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('volumes.noVolumesYet') }}</h3>
        <p>{{ $t('volumes.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('volumes.createAVolume') }}</button>
      </div>
      <template v-else>
        <div class="vol-toolbar">
          <div class="vol-count text-muted">
            <strong>{{ filteredVolumes.length }}</strong>
            <template v-if="filteredVolumes.length !== volumes.length"> of {{ volumes.length }}</template>
            volume{{ volumes.length === 1 ? '' : 's' }}
          </div>
          <div class="vol-search">
            <span class="mdi mdi-magnify vol-search-icon"></span>
            <input v-model="search" class="form-input" type="search" :aria-label="$t('volumes.filterVolumes')" :placeholder="$t('volumes.filterPlaceholder')" />
          </div>
        </div>
        <div v-if="filteredVolumes.length === 0" class="empty-state">
          <span class="mdi mdi-magnify" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No volumes match “{{ search }}”.</p>
          <button class="btn btn-ghost btn-sm mt-4" @click="search = ''">{{ $t('volumes.clearFilter') }}</button>
        </div>
        <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('volumes.volume') }}</th><th>{{ $t('volumes.usage') }}</th><th>{{ $t('volumes.node') }}</th><th>{{ $t('volumes.storage') }}</th><th>{{ $t('dashboard.col.created') }}</th></tr></thead>
          <tbody>
            <tr v-for="v in filteredVolumes" :key="v.id" class="row-clickable" @click="router.push(`/volumes/${v.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-harddisk" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ v.display_name || v.name }}<span
                      v-if="v.access_mode === 'rwx'"
                      class="badge badge-info"
                      style="margin-left: 8px"
                      :title="$t('volumes.rwxHint', { driver: v.driver?.toUpperCase() })"
                    >shared · {{ v.driver }}</span><span
                      v-else
                      class="badge badge-muted"
                      style="margin-left: 8px"
                      :title="$t('volumes.rwoHint')"
                    >{{ $t('volumes.local') }}</span></span>
                    <span class="cell-sub">{{ v.docker_name }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">
                <template v-if="v.used_measured_at">
                  <span class="cell-title" style="font-weight: 500">{{ fmtSize(v.used_bytes) }}</span>
                  <span v-if="v.size_bytes"> / {{ fmtSize(v.size_bytes) }}</span>
                </template>
                <template v-else>
                  <span v-if="v.size_bytes">{{ fmtSize(v.size_bytes) }} declared</span>
                  <span v-else>—</span>
                </template>
              </td>
              <td class="cell-sub">
                <span v-if="v.server_name"><span class="mdi mdi-server-network"></span> {{ v.server_name }}</span>
                <span v-else>—</span>
              </td>
              <td class="cell-sub">{{ classLabel(v.storage_class) }}</td>
              <td class="cell-sub">{{ fmtDate(v.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        </div>
      </template>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('volumes.newVolume') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="name" class="form-input" placeholder="e.g. app-data" required autofocus />
            </div>
            <LocationPicker v-model="volLocation" v-model:server-id="serverId" />
            <div class="form-group">
              <label class="form-label">{{ $t('volumes.type') }}</label>
              <select v-model="driver" class="form-select">
                <option value="local">{{ $t('volumes.localNodeLocal') }}</option>
                <option value="nfs" :disabled="!sharedAllowed">NFS (shared){{ sharedAllowed ? '' : ' — not in your plan' }}</option>
                <option value="cifs" :disabled="!sharedAllowed">CIFS / SMB (shared){{ sharedAllowed ? '' : ' — not in your plan' }}</option>
                <option value="host" :disabled="!hostAllowed">Host path (/mnt/*){{ hostAllowed ? '' : ' — privileged only' }}</option>
              </select>
              <p class="form-hint">
                <template v-if="driver === 'local'">{{ $t('volumes.localHint') }}</template>
                <template v-else-if="driver === 'host'">{{ $t('volumes.hostBindHint') }}</template>
                <template v-else>{{ $t('volumes.nasHint') }}</template>
              </p>
              <p v-if="!sharedAllowed" class="form-hint" style="color: var(--warning, #d97706)">
                <span class="mdi mdi-lock-outline"></span>{{ $t('volumes.sharedNotInPlan') }}</p>
            </div>
            <template v-if="driver === 'nfs'">
              <div class="form-row">
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">{{ $t('volumes.nfsServer') }}</label>
                  <input v-model="nfsServer" class="form-input" placeholder="10.0.0.5" required style="font-family: monospace" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">{{ $t('volumes.exportPath') }}</label>
                  <input v-model="nfsExport" class="form-input" placeholder="/exports/app" required style="font-family: monospace" />
                </div>
              </div>
            </template>
            <template v-else-if="driver === 'cifs'">
              <div class="form-group">
                <label class="form-label">{{ $t('volumes.share') }}</label>
                <input v-model="cifsShare" class="form-input" placeholder="//10.0.0.5/share" required style="font-family: monospace" />
              </div>
              <div class="form-row">
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">{{ $t('volumes.username') }}</label>
                  <input v-model="cifsUser" class="form-input" autocomplete="off" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">{{ $t('volumes.password') }}</label>
                  <input v-model="cifsPass" type="password" class="form-input" autocomplete="new-password" />
                </div>
              </div>
            </template>
            <template v-else-if="driver === 'host'">
              <div class="form-group">
                <label class="form-label">{{ $t('volumes.hostPath') }}</label>
                <input v-model="hostPath" class="form-input" placeholder="/mnt/nas/app" required style="font-family: monospace" />
                <i18n-t keypath="volumes.hostPathHint" tag="p" class="form-hint"><template #root><code>/mnt/</code></template></i18n-t>
              </div>
            </template>
            <template v-if="driver === 'local' && storageClasses.length > 1">
              <div class="form-group">
                <label class="form-label">{{ $t('volumes.storage') }}</label>
                <select v-model="storageClass" class="form-select">
                  <option value="">
                    {{ storageClasses.find(c => c.is_default)?.display_name || 'Default' }} (default)
                  </option>
                  <option v-for="c in storageClasses.filter(c => !c.is_default)" :key="c.name" :value="c.name">
                    {{ c.display_name || c.name }}
                  </option>
                </select>
                <p class="form-hint">
                  {{ storageClasses.find(c => c.name === storageClass)?.description
                    || 'Which of this node\'s disks the volume is created on. It cannot be changed later — the data lives there.' }}
                </p>
              </div>
            </template>
            <div class="form-group" style="margin-bottom: 0; margin-top: 16px">
              <label class="form-label">{{ $t('volumes.sizeLimitMb') }}<span class="text-muted" style="font-weight: 400">{{ $t('volumes.optional') }}</span></label>
              <input v-model.number="sizeMb" type="number" min="0" class="form-input" :placeholder="$t('databases.form.noLimit')" />
              <p class="form-hint">{{ $t('volumes.sizeHint') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creating">{{ creating ? 'Creating…' : 'Create volume' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }

.vol-toolbar {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 12px 20px; border-bottom: 1px solid var(--border-primary); flex-wrap: wrap;
}
.vol-count { font-size: 13px; }
.vol-count strong { color: var(--text-secondary); }
.vol-search { position: relative; }
.vol-search .form-input { width: 260px; max-width: 100%; padding-left: 32px; }
.vol-search-icon {
  position: absolute; left: 10px; top: 50%; transform: translateY(-50%);
  color: var(--text-muted); pointer-events: none; font-size: 16px;
}

.storage-summary { padding: 14px 20px; margin-bottom: 16px; }
.storage-row { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.storage-figure { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.storage-used { font-size: 18px; font-weight: 600; color: var(--text-primary); }
.storage-of { font-size: 13px; color: var(--text-muted); }
.storage-declared { font-size: 13px; color: var(--text-muted); }
.storage-measured { font-size: 12px; color: var(--text-muted); white-space: nowrap; }
.storage-bar { height: 6px; border-radius: 3px; background: var(--bg-secondary, var(--border-secondary)); margin-top: 10px; overflow: hidden; }
.storage-bar-fill { height: 100%; border-radius: 3px; background: var(--primary-500, #6366f1); transition: width 0.3s ease; }
.storage-bar-fill.storage-bar-warn { background: var(--warning-500, #d97706); }
</style>
