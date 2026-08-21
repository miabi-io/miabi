<script setup lang="ts">
// Portable backup: export this workspace to an encrypted bundle on its S3
// target, and restore one back — here, or into a fresh workspace.
//
// The bundle list comes from the bucket, not from this platform's run history:
// the whole point of the feature is that a bundle outlives the install that
// wrote it, so what the bucket holds is the truth and the runs below are only
// what this platform remembers doing.
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { portableBackupApi, type BundleInfo, type BundleRun } from '@/api/portableBackup'
import { useNotificationStore } from '@/stores/notification'
import { fmtSize } from '@/utils/format'
import { relativeTime } from '@/utils/time'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const props = defineProps<{ wsId: number; canRestore: boolean }>()

const notify = useNotificationStore()

const configured = ref(false)
const reason = ref('')
const loading = ref(false)
const bundles = ref<BundleInfo[]>([])
const runs = ref<BundleRun[]>([])
const exporting = ref(false)

// A run in flight is polled: an export dumps databases and archives volumes, so
// it finishes minutes after the button was pressed.
const activeRun = computed(() => runs.value.find((r) => r.status === 'pending' || r.status === 'running') ?? null)
let poll: ReturnType<typeof setInterval> | null = null

async function loadStatus() {
  try {
    const s = (await portableBackupApi.status(props.wsId)).data.data
    configured.value = s.configured
    reason.value = s.reason ?? ''
  } catch (e) {
    notify.apiError(e)
  }
}

async function loadRuns() {
  try {
    runs.value = (await portableBackupApi.runs(props.wsId)).data.data ?? []
  } catch {
    runs.value = []
  }
}

async function loadBundles() {
  if (!configured.value) return
  try {
    bundles.value = (await portableBackupApi.bundles(props.wsId)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  }
}

async function loadAll() {
  loading.value = true
  try {
    await loadStatus()
    await Promise.all([loadRuns(), loadBundles()])
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await loadAll()
  poll = setInterval(async () => {
    if (!activeRun.value) return
    const before = activeRun.value.id
    await loadRuns()
    // The run just finished: refresh the bucket listing so a new bundle appears.
    if (!runs.value.some((r) => r.id === before && (r.status === 'pending' || r.status === 'running'))) {
      await loadBundles()
    }
  }, 4000)
})
onUnmounted(() => {
  if (poll) clearInterval(poll)
})

async function runExport() {
  exporting.value = true
  try {
    await portableBackupApi.export(props.wsId)
    notify.success('Export started')
    await loadRuns()
  } catch (e) {
    notify.apiError(e)
  } finally {
    exporting.value = false
  }
}

// --- Restore ---
const restoreOpen = ref(false)
const restoreTarget = ref<BundleInfo | null>(null)
const restoreForm = ref({ intoNew: false, newWorkspace: '', restoreData: true, deployApps: false })
const restoring = ref(false)

function openRestore(b: BundleInfo) {
  restoreTarget.value = b
  restoreForm.value = {
    intoNew: false,
    newWorkspace: `${b.workspace}-restored`,
    restoreData: true,
    deployApps: false,
  }
  restoreOpen.value = true
}

async function confirmRestore() {
  if (!restoreTarget.value) return
  restoring.value = true
  try {
    await portableBackupApi.restore(props.wsId, {
      ref: restoreTarget.value.ref,
      new_workspace: restoreForm.value.intoNew ? restoreForm.value.newWorkspace.trim() : '',
      restore_data: restoreForm.value.restoreData,
      deploy_apps: restoreForm.value.deployApps,
    })
    notify.success('Restore started')
    restoreOpen.value = false
    await loadRuns()
  } catch (e) {
    notify.apiError(e)
  } finally {
    restoring.value = false
  }
}

// --- Delete a bundle from the bucket ---
const deleteRef = ref<string | null>(null)
async function confirmDeleteBundle() {
  if (!deleteRef.value) return
  try {
    await portableBackupApi.deleteBundle(props.wsId, deleteRef.value)
    notify.success('Bundle deleted')
    await loadBundles()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleteRef.value = null
  }
}

// --- Run detail (the report) ---
const openRun = ref<BundleRun | null>(null)
async function showRun(r: BundleRun) {
  try {
    openRun.value = (await portableBackupApi.run(props.wsId, r.id)).data.data
  } catch (e) {
    notify.apiError(e)
  }
}

function statusClass(status: string): string {
  switch (status) {
    case 'completed':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    case 'running':
    case 'pending':
      return 'badge-warning'
    default:
      return 'badge-neutral'
  }
}

function actionClass(action: string): string {
  switch (action) {
    case 'failed':
      return 'badge-danger'
    case 'skipped':
      return 'badge-warning'
    default:
      return 'badge-success'
  }
}

function bundleSize(b: BundleInfo): number {
  return b.artifacts.reduce((sum, a) => sum + (a.size_bytes ?? 0), 0)
}

function failedArtifacts(b: BundleInfo): number {
  return b.artifacts.filter((a) => !!a.error).length
}
</script>

<template>
  <div class="stack">
    <!-- Not configured: say exactly what is missing rather than offering a button that fails. -->
    <div v-if="!loading && !configured" class="card">
      <div class="card-body notice">
        <span class="mdi mdi-information-outline"></span>
        <div>
          <p><strong>Portable backup is not configured.</strong></p>
          <p class="text-muted text-sm">{{ reason || 'Set an S3 target and a bundle passphrase under Backup.' }}</p>
          <p class="text-muted text-sm">
            A bundle carries this workspace's configuration, its vault and its data. The passphrase
            seals it, and it is the only thing that opens it again — record it somewhere other than
            this platform.
          </p>
        </div>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div class="card-header">
          <div>
            <h2>Portable backup</h2>
            <p class="text-muted text-sm" style="margin: 4px 0 0">
              Export this workspace — apps, databases, volumes, secrets and routing — to one
              encrypted bundle in your bucket. Restore it here, or into a new workspace on any Miabi.
            </p>
          </div>
          <button class="btn btn-primary" :disabled="exporting || !!activeRun" @click="runExport">
            <span class="mdi mdi-cloud-upload-outline"></span>
            {{ activeRun ? 'Run in progress…' : exporting ? 'Starting…' : 'Back up now' }}
          </button>
        </div>
        <div v-if="activeRun" class="card-body">
          <div class="progress-row">
            <span class="badge badge-warning">{{ activeRun.kind }}</span>
            <span class="mono">{{ activeRun.ref }}</span>
            <span class="text-muted text-sm">{{ activeRun.phase || activeRun.status }}…</span>
          </div>
        </div>
      </div>

      <!-- Bundles in the bucket -->
      <div class="card">
        <div class="card-header">
          <h2>Bundles in the bucket</h2>
          <button class="btn btn-secondary btn-sm" @click="loadBundles">
            <span class="mdi mdi-refresh"></span> Refresh
          </button>
        </div>
        <div class="card-body">
          <p v-if="!bundles.length" class="text-muted text-sm">
            No bundles yet. The first export writes one, indexed by an XML file you can read with any
            S3 client.
          </p>
          <table v-else class="table">
            <thead>
              <tr>
                <th>Bundle</th>
                <th>Contents</th>
                <th>Size</th>
                <th>Taken</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in bundles" :key="b.ref">
                <td>
                  <div class="mono">{{ b.ref }}</div>
                  <div class="text-muted text-sm">
                    {{ b.workspace }}
                    <span v-if="b.miabi_version"> · v{{ b.miabi_version }}</span>
                    <span v-if="failedArtifacts(b)" class="badge badge-warning">
                      {{ failedArtifacts(b) }} artifact(s) missing
                    </span>
                  </div>
                </td>
                <td class="text-sm">
                  {{ b.apps }} apps · {{ b.databases }} databases · {{ b.volumes }} volumes ·
                  {{ b.secrets }} secrets · {{ b.routes }} routes
                  <span v-if="b.certificates"> · {{ b.certificates }} certificates</span>
                  <span v-if="b.pipelines"> · {{ b.pipelines }} pipelines</span>
                  <span v-if="b.gitops_sources"> · {{ b.gitops_sources }} GitOps</span>
                </td>
                <td class="text-sm">{{ fmtSize(bundleSize(b)) }}</td>
                <td class="text-sm">{{ relativeTime(b.created_at) }}</td>
                <td class="row-actions">
                  <button
                    class="btn btn-secondary btn-sm"
                    :disabled="!canRestore || !!activeRun"
                    :title="canRestore ? 'Restore this bundle' : 'Only an owner can restore'"
                    @click="openRestore(b)"
                  >
                    <span class="mdi mdi-backup-restore"></span> Restore
                  </button>
                  <button class="btn-icon btn-icon-danger" title="Delete from the bucket" @click="deleteRef = b.ref">
                    <span class="mdi mdi-delete-outline"></span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- History -->
      <div class="card">
        <div class="card-header"><h2>Recent runs</h2></div>
        <div class="card-body">
          <p v-if="!runs.length" class="text-muted text-sm">Nothing has run yet.</p>
          <table v-else class="table">
            <thead>
              <tr>
                <th>Kind</th>
                <th>Bundle</th>
                <th>Status</th>
                <th>When</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in runs" :key="r.id">
                <td><span class="badge badge-neutral">{{ r.kind }}</span></td>
                <td class="mono text-sm">{{ r.ref }}</td>
                <td>
                  <span class="badge" :class="statusClass(r.status)">{{ r.status }}</span>
                  <span v-if="r.phase && r.status === 'running'" class="text-muted text-sm"> · {{ r.phase }}</span>
                </td>
                <td class="text-sm">{{ relativeTime(r.created_at) }}</td>
                <td class="row-actions">
                  <button class="btn btn-secondary btn-sm" @click="showRun(r)">Report</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- Restore dialog -->
    <Teleport to="body">
      <AppModal v-if="restoreOpen && restoreTarget" @close="restoreOpen = false">
        <div class="modal-header">
          <h3>Restore bundle</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="restoreOpen = false">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="modal-body">
          <p class="text-muted text-sm mono">{{ restoreTarget.ref }}</p>
          <p class="text-muted text-sm">
            Resources are matched by name: anything already here is left exactly as it is, so a
            restore never overwrites a live secret or app.
          </p>

          <label class="toggle-row">
            <input v-model="restoreForm.intoNew" type="checkbox" />
            <span>Restore into a new workspace (a clone, beside this one)</span>
          </label>
          <div v-if="restoreForm.intoNew" class="form-group">
            <label class="form-label">New workspace name</label>
            <input v-model="restoreForm.newWorkspace" class="form-input" placeholder="shop-restored" />
          </div>

          <label class="toggle-row">
            <input v-model="restoreForm.restoreData" type="checkbox" />
            <span>Restore data (database dumps and volume archives)</span>
          </label>
          <label class="toggle-row">
            <input v-model="restoreForm.deployApps" type="checkbox" />
            <span>Deploy applications when the restore finishes</span>
          </label>

          <div class="danger-note">
            <span class="mdi mdi-alert-outline"></span>
            <div>
              Restoring data overwrites the contents of any database or volume of the same name.
              Domains come back unverified and certificates re-issue only once DNS points here.
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="restoreOpen = false">Cancel</button>
          <button class="btn btn-primary" :disabled="restoring" @click="confirmRestore">
            {{ restoring ? 'Starting…' : 'Restore' }}
          </button>
        </div>
      </AppModal>
    </Teleport>

    <!-- Run report -->
    <Teleport to="body">
      <AppModal v-if="openRun" dialog-class="modal-lg" @close="openRun = null">
        <div class="modal-header">
          <h3>{{ openRun.kind === 'export' ? 'Export' : 'Restore' }} report</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="openRun = null">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="modal-body">
          <p class="mono text-sm">{{ openRun.ref }}</p>
          <p v-if="openRun.error" class="danger-note">
            <span class="mdi mdi-alert-outline"></span>
            <span>{{ openRun.error }}</span>
          </p>
          <ul v-if="openRun.report?.notes?.length" class="notes">
            <li v-for="(n, i) in openRun.report.notes" :key="i">{{ n }}</li>
          </ul>
          <table v-if="openRun.report?.items?.length" class="table">
            <thead>
              <tr><th>Resource</th><th>Name</th><th>Result</th><th>Detail</th></tr>
            </thead>
            <tbody>
              <tr v-for="(it, i) in openRun.report.items" :key="i">
                <td class="text-sm">{{ it.kind }}</td>
                <td class="text-sm mono">{{ it.name }}</td>
                <td><span class="badge" :class="actionClass(it.action)">{{ it.action }}</span></td>
                <td class="text-sm text-muted">{{ it.detail }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!deleteRef"
      title="Delete bundle"
      :message="`This removes ${deleteRef} from the bucket — its index, its state file and every dump and archive under it. It cannot be undone.`"
      confirm-label="Delete"
      variant="danger"
      @confirm="confirmDeleteBundle"
      @cancel="deleteRef = null"
    />
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.notice {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}
.notice .mdi {
  font-size: 24px;
  color: var(--text-muted);
}
.notice p {
  margin: 0 0 6px;
}
.progress-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.85em;
  word-break: break-all;
}
.row-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  align-items: center;
}
.notes {
  margin: 0 0 16px;
  padding-left: 18px;
  color: var(--text-secondary, var(--text-muted));
  font-size: 0.9em;
}
.notes li {
  margin-bottom: 4px;
}
.danger-note {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  margin-top: 16px;
  font-size: 0.9em;
  color: var(--text-secondary, var(--text-muted));
}
.toggle-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 12px;
}
.modal-lg {
  max-width: 820px;
}
</style>
