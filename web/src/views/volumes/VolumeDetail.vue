<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { volumeApi } from '@/api/resources'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ResourceIcon from '@/components/ResourceIcon.vue'
import MetadataCard from '@/components/MetadataCard.vue'
import OwnerChip from '@/components/OwnerChip.vue'
import LocationName from '@/components/LocationName.vue'
import { copyText } from '@/utils/clipboard'
import type { VolumeDetail, VolumeFile, VolumeBackup } from '@/api/types'

const route = useRoute()
const { t } = useI18n()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const volId = computed(() => Number(route.params.id))
const wid = computed(() => ws.currentWorkspaceId)

const vol = ref<VolumeDetail | null>(null)
const loading = ref(false)
const confirmOpen = ref(false)
const deleting = ref(false)

// --- Tabs (state mirrored in the URL query) ---
type TabKey = 'overview' | 'files' | 'backups' | 'settings'
const tabs = computed<{ key: TabKey; label: string; icon: string }[]>(() => {
  const list: { key: TabKey; label: string; icon: string }[] = [
    { key: 'overview', label: 'volumes.overview', icon: 'mdi-information-outline' },
    { key: 'files', label: 'volumes.files', icon: 'mdi-folder-outline' },
    { key: 'backups', label: 'volumes.backups', icon: 'mdi-cloud-outline' },
  ]
  if (ws.canEdit) list.push({ key: 'settings', label: 'volumes.settings', icon: 'mdi-cog-outline' })
  return list
})
function tabFromQuery(): TabKey {
  const q = route.query.tab
  const valid: TabKey[] = ['overview', 'files', 'backups', 'settings']
  return typeof q === 'string' && valid.includes(q as TabKey) ? (q as TabKey) : 'overview'
}
const tab = ref<TabKey>(tabFromQuery())
watch(tab, (t) => router.replace({ query: { ...route.query, tab: t } }))

// Most recent backup, for the overview stat card.
const lastBackup = computed(() => backups.value[0] ?? null)

async function load() {
  if (!wid.value) return
  loading.value = true
  try {
    vol.value = (await volumeApi.get(wid.value, volId.value)).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch([volId, wid], load, { immediate: true })

async function copy(text: string) {
  if (!text) return
  if (await copyText(text)) notify.success(t('notify.common.copied'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

async function remove() {
  if (!wid.value || !vol.value) return
  deleting.value = true
  try {
    await volumeApi.remove(wid.value, vol.value.id)
    notify.success(t('notify.volumeDetail.deleted'))
    router.push('/volumes')
  } catch (e) {
    notify.apiError(e, 'Failed to delete (volume may be in use)')
  } finally {
    deleting.value = false
  }
}

function fmtTime(s?: string) {
  return s ? new Date(s).toLocaleString() : '—'
}

// --- Files ---
const files = ref<VolumeFile[]>([])
const filesLoading = ref(false)
const uploading = ref(false)
const uploadPath = ref('')
const fileInput = ref<HTMLInputElement | null>(null)
const fileConfirmOpen = ref(false)
const pendingDelete = ref<VolumeFile | null>(null)
const deletingFile = ref(false)

async function loadFiles() {
  if (!wid.value) return
  filesLoading.value = true
  try {
    files.value = (await volumeApi.listFiles(wid.value, volId.value)).data.data
  } catch (e) {
    notify.apiError(e, 'Failed to list files')
  } finally {
    filesLoading.value = false
  }
}
watch([volId, wid], loadFiles, { immediate: true })

async function onUpload(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || !wid.value) return
  uploading.value = true
  try {
    await volumeApi.uploadFile(wid.value, volId.value, file, uploadPath.value.trim() || undefined)
    notify.success(t('notify.volumeDetail.uploaded', { name: file.name }))
    uploadPath.value = ''
    await loadFiles()
  } catch (err) {
    notify.apiError(err, 'Failed to upload file')
  } finally {
    uploading.value = false
    if (fileInput.value) fileInput.value.value = ''
  }
}

async function download(f: VolumeFile) {
  if (!wid.value) return
  try {
    const blob = (await volumeApi.downloadFile(wid.value, volId.value, f.path)).data
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = f.path.split('/').pop() || 'file'
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    notify.apiError(e, 'Failed to download')
  }
}

async function removeFile() {
  if (!wid.value || !pendingDelete.value) return
  deletingFile.value = true
  try {
    await volumeApi.deleteFile(wid.value, volId.value, pendingDelete.value.path)
    notify.success(t('notify.volumeDetail.fileDeleted'))
    fileConfirmOpen.value = false
    pendingDelete.value = null
    await loadFiles()
  } catch (e) {
    notify.apiError(e, 'Failed to delete')
  } finally {
    deletingFile.value = false
  }
}

function fmtBytes(n: number): string {
  if (!n || n <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}
function fmtEpoch(s: number) {
  return s ? new Date(s * 1000).toLocaleString() : '—'
}

// --- Backups (to the workspace S3 target) ---
const backups = ref<VolumeBackup[]>([])
const backupConfigured = ref(false)
const backupsLoading = ref(false)
const backingUp = ref(false)
const restoreConfirmOpen = ref(false)
const pendingRestore = ref<VolumeBackup | null>(null)
const restoring = ref(false)

const backupBadge: Record<string, string> = {
  completed: 'badge-success',
  running: 'badge-info',
  pending: 'badge-neutral',
  failed: 'badge-danger',
}

async function loadBackups() {
  if (!wid.value) return
  backupsLoading.value = true
  try {
    const [list, status] = await Promise.all([
      volumeApi.listBackups(wid.value, volId.value),
      volumeApi.backupStatus(wid.value, volId.value),
    ])
    backups.value = list.data.data ?? []
    backupConfigured.value = status.data.data.s3_configured
  } catch (e) {
    notify.apiError(e, 'Failed to list backups')
  } finally {
    backupsLoading.value = false
  }
}
watch([volId, wid], loadBackups, { immediate: true })

async function runBackup() {
  if (!wid.value) return
  backingUp.value = true
  try {
    await volumeApi.runBackup(wid.value, volId.value)
    notify.success(t('notify.volumeDetail.backupStarted'))
    await loadBackups()
    pollBackups()
  } catch (e) {
    notify.apiError(e, 'Failed to back up volume')
  } finally {
    backingUp.value = false
  }
}

// Refresh the list while a backup is still pending/running (it runs async on the
// worker). Bounded so it can't loop forever; cleared on unmount.
let pollTimer: ReturnType<typeof setTimeout> | null = null
function pollBackups(remaining = 20) {
  if (pollTimer) clearTimeout(pollTimer)
  if (remaining <= 0) return
  const active = backups.value.some((b) => b.status === 'pending' || b.status === 'running')
  if (!active) return
  pollTimer = setTimeout(async () => {
    await loadBackups()
    pollBackups(remaining - 1)
  }, 3000)
}
onUnmounted(() => {
  if (pollTimer) clearTimeout(pollTimer)
})

async function doRestore() {
  if (!wid.value || !pendingRestore.value) return
  restoring.value = true
  try {
    await volumeApi.restoreBackup(wid.value, volId.value, pendingRestore.value.id)
    notify.success(t('notify.volumeDetail.restored'))
    restoreConfirmOpen.value = false
    pendingRestore.value = null
  } catch (e) {
    notify.apiError(e, 'Restore failed')
  } finally {
    restoring.value = false
  }
}

const deleteConfirmOpen = ref(false)
const pendingDeleteBackup = ref<VolumeBackup | null>(null)
const deletingBackup = ref(false)

async function doDeleteBackup() {
  if (!wid.value || !pendingDeleteBackup.value) return
  deletingBackup.value = true
  try {
    await volumeApi.deleteBackup(wid.value, volId.value, pendingDeleteBackup.value.id)
    notify.success(t('notify.volumeDetail.backupDeleted'))
    deleteConfirmOpen.value = false
    pendingDeleteBackup.value = null
    await loadBackups()
  } catch (e) {
    notify.apiError(e, 'Failed to delete backup')
  } finally {
    deletingBackup.value = false
  }
}

function getFileName(path: string): string {
  return path.split('/').pop() || path
}
function getDepth(path: string): number {
  const segments = path.replace(/^\/|\/$/g, '').split('/')
  return Math.max(0, segments.length - 1)
}
function getFileIcon(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase()
  switch (ext) {
    case 'json':
    case 'yaml':
    case 'yml':
    case 'env':
      return 'mdi-code-json'
    case 'js':
    case 'ts':
    case 'vue':
    case 'go':
    case 'php':
      return 'mdi-file-code-outline'
    case 'png':
    case 'jpg':
    case 'svg':
      return 'mdi-file-image-outline'
    case 'log':
      return 'mdi-file-document-outline'
    case 'zip':
    case 'tar':
    case 'gz':
      return 'mdi-zip-box-outline'
    default:
      return 'mdi-file-outline'
  }
}

const expandedFolders = ref<Set<string>>(new Set())


function toggleFolder(path: string) {
  if (expandedFolders.value.has(path)) {
    expandedFolders.value.delete(path)
  } else {
    expandedFolders.value.add(path)
  }
}

function isFileVisible(file: VolumeFile): boolean {
  const parts = file.path.split('/').filter(Boolean)

  if (parts.length <= 1) return true

  let currentParent = ''
  for (let i = 0; i < parts.length - 1; i++) {
    currentParent += (currentParent ? '/' : '') + parts[i]
    if (!expandedFolders.value.has(currentParent)) {
      return false
    }
  }

  return true
}
</script>

<template>
  <div v-if="vol">
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/volumes')">
          <span class="mdi mdi-arrow-left"></span>{{ $t('volumes.volumes') }}</button>
        <div class="flex items-center gap-3" style="margin-top: 8px">
          <ResourceIcon mdi="mdi-harddisk" :name="vol.name" :size="44" />
          <div>
            <h1>{{ vol.display_name || vol.name }}</h1>
            <div class="text-muted text-sm">
              <span class="mdi mdi-docker"></span> Persistent volume · {{ vol.driver || 'local' }}
              <template v-if="vol.server_name"> · <span class="mdi mdi-server-network"></span> {{ vol.server_name }}</template>
              <LocationName :cluster-id="vol.cluster_id" />
            </div>
          </div>
        </div>
      </div>
      <div class="flex items-center gap-3">
        <span class="badge badge-dot" :class="vol.in_use ? 'badge-success' : 'badge-neutral'">
          {{ vol.in_use ? `in use · ${vol.used_by.length} app${vol.used_by.length === 1 ? '' : 's'}` : 'unused' }}
        </span>
        <span v-if="!vol.exists" class="badge badge-danger" :title="$t('volumes.missingHint')">{{ $t('volumes.missing') }}</span>
      </div>
    </div>

    <div class="tabs">
      <button v-for="item in tabs" :key="item.key" class="tab" :class="{ active: tab === item.key }" @click="tab = item.key">
        <span class="mdi" :class="item.icon"></span> {{ $t(item.label) }}
      </button>
    </div>

    <!-- OVERVIEW -->
    <template v-if="tab === 'overview'">
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('volumes.sizeOnDisk') }}</span><span class="stat-icon stat-icon-primary"><span class="mdi mdi-database"></span></span></div>
          <div class="stat-value">{{ fmtBytes(vol.size_bytes ?? 0) }}</div>
          <div class="stat-sub">{{ vol.exists ? 'reported by Docker' : 'volume not found' }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('volumes.mountedBy') }}</span><span class="stat-icon stat-icon-info"><span class="mdi mdi-application-outline"></span></span></div>
          <div class="stat-value">{{ vol.used_by.length }}</div>
          <div class="stat-sub">{{ vol.used_by.length === 1 ? 'application' : 'applications' }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('volumes.files') }}</span><span class="stat-icon stat-icon-secondary"><span class="mdi mdi-folder-outline"></span></span></div>
          <div class="stat-value">{{ filesLoading ? '…' : files.length }}</div>
          <div class="stat-sub">{{ $t('volumes.topLevelEntries') }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header"><span class="stat-label">{{ $t('volumes.backups') }}</span><span class="stat-icon stat-icon-success"><span class="mdi mdi-cloud-outline"></span></span></div>
          <div class="stat-value">{{ backupsLoading ? '…' : backups.length }}</div>
          <div class="stat-sub">
            <span v-if="lastBackup">{{ $t('volumes.last') }} <span class="badge badge-dot" :class="backupBadge[lastBackup.status] || 'badge-neutral'">{{ lastBackup.status }}</span></span>
            <span v-else>{{ $t('volumes.noneYet') }}</span>
          </div>
        </div>
      </div>

      <!-- Details -->
      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('volumes.details') }}</h2></div>
        <div class="detail-grid">
          <div><span class="detail-label">{{ $t('dashboard.col.status') }}</span><span class="badge badge-dot" :class="vol.in_use ? 'badge-success' : 'badge-neutral'">{{ vol.in_use ? 'in use' : 'unused' }}</span></div>
          <div><span class="detail-label">{{ $t('volumes.owner') }}</span><OwnerChip :metadata="vol.metadata" /></div>
          <div><span class="detail-label">{{ $t('volumes.driver') }}</span>{{ vol.driver || 'local' }}</div>
          <div v-if="vol.storage_class"><span class="detail-label">{{ $t('volumes.storageClass') }}</span>{{ vol.storage_class }}</div>
          <div v-if="vol.host_path"><span class="detail-label">{{ $t('volumes.hostPath') }}</span><code class="copyable" :title="$t('volumes.copy')" @click="copy(vol.host_path || '')">{{ vol.host_path }} <span class="mdi mdi-content-copy"></span></code></div>
          <div><span class="detail-label">{{ $t('db.dockerName') }}</span><code class="copyable" :title="$t('volumes.copy')" @click="copy(vol.docker_name)">{{ vol.docker_name }} <span class="mdi mdi-content-copy"></span></code></div>
          <div v-if="vol.server_name"><span class="detail-label">{{ $t('volumes.node') }}</span>{{ vol.server_name }}</div>
          <div><span class="detail-label">{{ $t('dashboard.col.created') }}</span>{{ fmtTime(vol.created_at) }}</div>
        </div>
      </div>

      <!-- Used by -->
      <div class="card">
        <div class="card-header"><h2>{{ $t('volumes.usedBy') }}</h2></div>
        <div v-if="vol.used_by.length === 0" class="empty-state">
          <span class="mdi mdi-application-outline" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>{{ $t('volumes.noMounts') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('volumes.application') }}</th><th>{{ $t('volumes.mountPath') }}</th></tr></thead>
            <tbody>
              <tr v-for="u in vol.used_by" :key="u.app_id">
                <td>
                  <RouterLink :to="`/apps/${u.app_id}`" class="cell-title">{{ u.app_display_name || u.app_name }}</RouterLink>
                  <div class="cell-sub">{{ u.app_name }}</div>
                </td>
                <td class="mono">{{ u.path }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <MetadataCard :metadata="vol.metadata" class="mt-4" />

      <MetadataCard :metadata="vol.annotations" :title="$t('volumes.annotations')" :reserved="false" class="mt-4" />
    </template>

    <!-- FILES -->
    <div v-else-if="tab === 'files'" class="card">
      <div class="card-header flex items-center justify-between">
        <h2>{{ $t('volumes.files') }}</h2>
        <div class="flex items-center gap-2">
          <button class="btn btn-ghost btn-sm" :disabled="filesLoading" :title="$t('volumes.refresh')" :aria-label="$t('volumes.refresh')" @click="loadFiles">
            <span class="mdi mdi-refresh"></span>
          </button>
          <template v-if="ws.canEdit">
            <input
              v-model="uploadPath"
              class="form-input input-sm"
              style="width: 160px"
              :aria-label="$t('volumes.uploadSubdirectory')"
              :placeholder="$t('volumes.subdirPlaceholder')"
            />
            <input ref="fileInput" type="file" class="hidden-file" :aria-label="$t('volumes.chooseFileToUpload')" @change="onUpload" />
            <button class="btn btn-primary btn-sm" :disabled="uploading || !vol.exists" @click="fileInput?.click()">
              <span class="mdi" :class="uploading ? 'mdi-loading mdi-spin' : 'mdi-upload'"></span>
              {{ uploading ? 'Uploading…' : 'Upload file' }}
            </button>
          </template>
        </div>
      </div>
      <div v-if="filesLoading" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="files.length === 0" class="empty-state">
        <span class="mdi mdi-folder-open-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('volumes.emptyHint') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table class="file-table">
          <thead>
            <tr>
              <th>{{ $t('apps.form.name') }}</th>
              <th>{{ $t('volumes.size') }}</th>
              <th>{{ $t('volumes.modified') }}</th>
              <th class="text-right">{{ $t('volumes.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in files" :key="f.path" v-show="isFileVisible(f)" class="row-clickable"
              :class="{ 'is-dir': f.is_dir }" @click="f.is_dir ? toggleFolder(f.path) : null">
              <td>
                <div class="file-cell" :style="{ paddingLeft: `${getDepth(f.path) * 20}px` }">
                  <span v-if="f.is_dir" class="mdi chevron-icon"
                    :class="expandedFolders.has(f.path) ? 'mdi-chevron-down' : 'mdi-chevron-right'"></span>
                  <span v-else class="chevron-placeholder"></span>

                  <span class="mdi file-icon" :class="[
                    f.is_dir
                      ? (expandedFolders.has(f.path) ? 'mdi-folder-open' : 'mdi-folder')
                      : getFileIcon(f.path),
                    f.is_dir ? 'icon-folder' : 'icon-file'
                  ]"></span>

                  <span class="file-name" :title="f.path">{{ getFileName(f.path) }}</span>
                </div>
              </td>

              <td class="cell-sub cell-num">{{ f.is_dir ? '—' : fmtBytes(f.size) }}</td>
              <td class="cell-sub">{{ fmtEpoch(f.mod_time) }}</td>
              <td class="text-right" @click.stop>
                <div class="table-actions">
                  <button v-if="!f.is_dir" class="btn-icon btn-icon-muted" :title="$t('volumes.download')" :aria-label="$t('volumes.download')"
                    @click="download(f)">
                    <span class="mdi mdi-download"></span>
                  </button>
                  <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')"
                    @click="pendingDelete = f; fileConfirmOpen = true">
                    <span class="mdi mdi-trash-can-outline"></span>
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- BACKUPS -->
    <div v-else-if="tab === 'backups'" class="card">
      <div class="card-header flex items-center justify-between">
        <div>
          <h2>{{ $t('volumes.backups') }}</h2>
          <div class="text-muted text-sm">{{ $t('volumes.backupsHint') }}</div>
        </div>
        <div class="flex items-center gap-2">
          <button class="btn btn-ghost btn-sm" :disabled="backupsLoading" :title="$t('volumes.refresh')" :aria-label="$t('volumes.refresh')" @click="loadBackups">
            <span class="mdi mdi-refresh"></span>
          </button>
          <button
            v-if="ws.canEdit"
            class="btn btn-primary btn-sm"
            :disabled="backingUp || !vol.exists || !backupConfigured"
            :title="!backupConfigured ? $t('volumes.backupNeedsS3') : $t('volumes.backupNow')"
            @click="runBackup"
          >
            <span class="mdi" :class="backingUp ? 'mdi-loading mdi-spin' : 'mdi-cloud-upload-outline'"></span>
            {{ backingUp ? 'Backing up…' : 'Back up now' }}
          </button>
        </div>
      </div>
      <div v-if="!backupConfigured && !backupsLoading" class="app-banner app-banner--info" style="margin: 12px 16px 0">
        <span class="mdi mdi-information-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <i18n-t keypath="volumes.backupsDisabledHint" tag="p" class="app-banner-text"><template #link><RouterLink :to="{ name: 'workspace-detail', params: { id: wid }, query: { tab: 'backup' } }">{{ $t('volumes.workspaceBackupSettings') }}</RouterLink></template></i18n-t>
        </div>
      </div>
      <div v-if="backupsLoading" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="backups.length === 0" class="empty-state">
        <span class="mdi mdi-cloud-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('volumes.noBackups') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('dashboard.col.created') }}</th><th>{{ $t('dashboard.col.status') }}</th><th>{{ $t('volumes.archive') }}</th><th>{{ $t('volumes.trigger') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="b in backups" :key="b.id">
              <td class="cell-sub">{{ fmtTime(b.created_at) }}</td>
              <td>
                <span class="badge badge-dot" :class="backupBadge[b.status] || 'badge-neutral'">{{ b.status }}</span>
                <span v-if="b.status === 'failed' && b.error" class="cell-sub" :title="b.error" style="margin-left: 6px">⚠</span>
              </td>
              <td class="mono">{{ b.filename || '—' }}</td>
              <td class="cell-sub">{{ b.trigger }}</td>
              <td class="text-right">
                <button
                  v-if="ws.canEdit"
                  class="btn btn-ghost btn-sm"
                  :disabled="b.status !== 'completed' || !vol.exists"
                  :title="$t('volumes.restoreHint')"
                  @click="pendingRestore = b; restoreConfirmOpen = true"
                >
                  <span class="mdi mdi-backup-restore"></span>{{ $t('volumes.restore') }}</button>
                <button
                  v-if="ws.canEdit"
                  class="btn-icon btn-icon-danger"
                  :disabled="b.status === 'pending' || b.status === 'running'"
                  :title="$t('volumes.deleteThisBackup')"
                  :aria-label="$t('volumes.deleteThisBackup')"
                  @click="pendingDeleteBackup = b; deleteConfirmOpen = true"
                >
                  <span class="mdi mdi-trash-can-outline"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- SETTINGS -->
    <template v-else-if="tab === 'settings'">
      <div class="card danger-card">
        <div class="card-header"><h2>{{ $t('db.dangerZone') }}</h2></div>
        <div class="card-body flex items-center justify-between gap-3">
          <div>
            <div class="cell-title">{{ $t('volumes.deleteThisVolume') }}</div>
            <div class="cell-sub">{{ $t('volumes.deleteHint') }}<template v-if="vol.in_use">{{ $t('volumes.detachFirst') }}</template>
            </div>
          </div>
          <button
            class="btn btn-danger"
            :disabled="vol.in_use"
            :title="vol.in_use ? $t('volumes.detachFirstShort') : $t('volumes.deleteThisVolume')"
            @click="confirmOpen = true"
          >{{ $t('action.delete') }}</button>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="deleteConfirmOpen"
      :title="$t('confirm.title.deleteBackup')"
      :message="$t('confirm.message.volumeDetail.deleteTheBackupTaken', { fmtTime: fmtTime(pendingDeleteBackup?.created_at) })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deletingBackup"
      @confirm="doDeleteBackup"
      @cancel="deleteConfirmOpen = false; pendingDeleteBackup = null"
    />

    <ConfirmDialog
      :open="restoreConfirmOpen"
      :title="$t('confirm.title.restoreVolume')"
      :message="$t('confirm.message.volumeDetail.restoreNameFromThe', { name: vol.name, fmtTime: fmtTime(pendingRestore?.created_at) })"
      :confirm-label="$t('action.restore')"
      variant="danger"
      :busy="restoring"
      @confirm="doRestore"
      @cancel="restoreConfirmOpen = false; pendingRestore = null"
    />

    <ConfirmDialog
      :open="fileConfirmOpen"
      :title="$t('confirm.title.deleteFile')"
      :message="$t('confirm.message.volumeDetail.deletePathFromThis', { path: pendingDelete?.path })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deletingFile"
      @confirm="removeFile"
      @cancel="fileConfirmOpen = false; pendingDelete = null"
    />

    <ConfirmDialog
      :open="confirmOpen"
      :title="$t('confirm.title.deleteVolume')"
      :message="$t('confirm.message.volumeDetail.deleteVolumeNameThis', { name: vol.name })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="remove"
      @cancel="confirmOpen = false"
    />
  </div>
  <div v-else-if="loading" class="loading-page"><span class="spinner"></span></div>
  <div v-else class="empty-state"><p>{{ $t('volumes.volumeNotFound') }}</p></div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 2px 8px; border-radius: 4px; }
.hidden-file { display: none; }
.text-right { text-align: right; white-space: nowrap; }

/* Details grid — matches the database detail page */
.detail-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; padding: 16px; }
.detail-grid > div { display: flex; flex-direction: column; gap: 4px; font-size: 14px; align-items: flex-start; }
.detail-label { font-size: 12px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }
code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; font-family: 'JetBrains Mono', monospace; }
.copyable { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; max-width: 100%; }
.copyable .mdi { color: var(--text-muted); font-size: 13px; }
.copyable:hover { color: var(--text-primary); }

.danger-card { border-color: var(--danger-100, rgba(220, 38, 38, 0.2)); }
[data-theme="dark"] .danger-card {
  border-color: var(--danger-900, rgba(239, 68, 68, 0.063));
}
.justify-between { justify-content: space-between; }

.input-sm { font-size: 13px; padding: 8px 8px;}

/* files table */
.file-table {
  width: 100%;
  border-collapse: collapse;
  min-width: 550px;
}

.file-cell {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.file-icon {
  font-size: 18px;
  flex-shrink: 0;
}

.icon-folder {
  color: var(--primary-500, #3b82f6);
}

.icon-file {
  color: var(--text-muted);
}

.file-name-group {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.file-name {
  font-family: var(--font-mono, monospace);
  font-size: 13.5px;
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.file-path-sub {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

tr.is-dir {
  cursor: pointer;
}

tr.is-dir:hover .file-name {
  color: var(--primary-500, #3b82f6);
}

.table-actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  opacity: 0.3;
  transition: opacity 0.15s ease;
}

tr:hover .table-actions {
  opacity: 1;
}

.btn-icon-danger:hover {
  color: var(--danger-600, #ef4444);
  background: var(--danger-50, rgba(239, 68, 68, 0.1));
}

.chevron-icon {
  font-size: 16px;
  color: var(--text-muted);
  transition: transform 0.15s ease;
  flex-shrink: 0;
}

.chevron-placeholder {
  width: 16px;
  flex-shrink: 0;
}

.file-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
</style>
