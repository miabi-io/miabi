<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { databaseApi } from '@/api/resources'
import type { DatabaseInstance, DBEngine, DBStatus } from '@/api/types'
import NodePicker from '@/components/NodePicker.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const dbs = ref<DatabaseInstance[]>([])
const loading = ref(false) // first-load spinner only; background updates never toggle it
const showCreate = ref(false)
const creating = ref(false)
const form = ref<{ name: string; engine: DBEngine; version: string; server_id: number; size_mb: number | null }>({ name: '', engine: 'postgres', version: '', server_id: 0, size_mb: null })

// Live updates: one SSE connection streams status deltas for the whole workspace
// (no per-row polling). A slow reconcile is kept only as a safety net — to catch
// creates/deletes/size changes and recover if the SSE connection drops.
let es: EventSource | null = null
let reconcileTimer: ReturnType<typeof setInterval> | null = null
let inFlight = false
const RECONCILE_MS = 25000

const engines: { value: DBEngine; label: string; icon: string; default: string }[] = [
  { value: 'postgres', label: 'PostgreSQL', icon: 'mdi-elephant', default: '17-alpine' },
  { value: 'mysql', label: 'MySQL', icon: 'mdi-database', default: '8.4' },
  { value: 'mariadb', label: 'MariaDB', icon: 'mdi-database', default: '11' },
  { value: 'redis', label: 'Redis', icon: 'mdi-database-outline', default: '7-alpine' },
  { value: 'mongodb', label: 'MongoDB', icon: 'mdi-leaf', default: '7.0' },
  { value: 'libsql', label: 'libSQL', icon: 'mdi-database-search', default: 'latest' },
]
// Default versions resolved from the admin deployment-config catalog (overrides
// the hardcoded fallbacks above so the form reflects the configured tags).
const engineDefaults = ref<Record<string, string>>({})
const defaultVersion = computed(
  () => engineDefaults.value[form.value.engine] || engines.find((e) => e.value === form.value.engine)?.default || 'latest',
)

// mergeList folds an incoming list into dbs while keeping the existing row
// objects for unchanged instances, so the table patches only what changed instead
// of re-rendering (and flickering) every row on each refresh.
function mergeList(incoming: DatabaseInstance[]) {
  const byId = new Map(dbs.value.map((d) => [d.id, d]))
  dbs.value = incoming.map((item) => {
    const existing = byId.get(item.id)
    if (existing) { Object.assign(existing, item); return existing }
    return item
  })
}

// reconcile re-fetches the list and merges it in place. Used for the initial load
// and the slow safety-net refresh; concurrent calls are skipped.
async function reconcile(id: number | null) {
  if (!id || inFlight) return
  inFlight = true
  try {
    mergeList((await databaseApi.list(id)).data.data ?? [])
  } catch {
    // Keep the last good list; SSE or the next reconcile recovers.
  } finally {
    inFlight = false
  }
}

async function loadEngines(id: number) {
  // Engine defaults come from the deployment-config catalog and rarely change, so
  // they are fetched once per workspace rather than on every refresh.
  try {
    const defs = (await databaseApi.engines(id)).data.data ?? []
    engineDefaults.value = Object.fromEntries(defs.map((d) => [d.engine, d.version]))
  } catch {
    // Keep the hardcoded fallbacks.
  }
}

// applyStatus patches one row's status from a live SSE delta. A status for an
// instance we don't have yet (created elsewhere) pulls in the new row.
function applyStatus(ev: { id: number; status: DBStatus }) {
  const row = dbs.value.find((d) => d.id === ev.id)
  if (row) row.status = ev.status
  else reconcile(currentWorkspaceId.value)
}

function closeStream() { if (es) { es.close(); es = null } }
function openStream(id: number) {
  closeStream()
  es = new EventSource(databaseApi.workspaceEventsUrl(id))
  es.onmessage = (m) => {
    let msg: { type?: string; data?: { id?: number; status?: DBStatus } }
    try { msg = JSON.parse(m.data) } catch { return } // ignore keep-alives
    if (msg.type === 'status' && msg.data?.id && msg.data.status) {
      applyStatus({ id: msg.data.id, status: msg.data.status })
    }
  }
  // EventSource auto-reconnects on transient network errors; nothing to do here.
}

function stopReconcile() { if (reconcileTimer) { clearInterval(reconcileTimer); reconcileTimer = null } }
function startReconcile() {
  stopReconcile()
  reconcileTimer = setInterval(() => { if (!document.hidden) reconcile(currentWorkspaceId.value) }, RECONCILE_MS)
}

async function loadWorkspace(id: number | null) {
  closeStream(); stopReconcile()
  dbs.value = []
  if (!id) return
  loading.value = true
  loadEngines(id)
  await reconcile(id)
  loading.value = false
  openStream(id)
  startReconcile()
}

watch(currentWorkspaceId, (id) => loadWorkspace(id), { immediate: true })

// Returning to a backgrounded tab: reconcile once so the list is fresh even if the
// SSE connection was idle or reconnecting while hidden.
function onVisibility() { if (!document.hidden) reconcile(currentWorkspaceId.value) }
document.addEventListener('visibilitychange', onVisibility)

onBeforeUnmount(() => {
  closeStream(); stopReconcile()
  document.removeEventListener('visibilitychange', onVisibility)
})

function openCreate() {
  form.value = { name: '', engine: 'postgres', version: '', server_id: 0, size_mb: null }
  showCreate.value = true
}

async function create() {
  if (!currentWorkspaceId.value) return
  creating.value = true
  try {
    await databaseApi.create(currentWorkspaceId.value, form.value.name.trim(), form.value.engine, form.value.version.trim() || undefined, form.value.server_id, form.value.size_mb ?? undefined)
    notify.success('Database provisioning…')
    showCreate.value = false
    reconcile(currentWorkspaceId.value) // pull in the new row; SSE then drives it to running
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

function badge(s: string) {
  return s === 'running' ? 'badge-success' : s === 'failed' ? 'badge-danger' : 'badge-warning'
}
function fmtBytes(n?: number): string {
  if (!n || n <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Databases</h1>
        <p class="subtitle">Managed database instances Miabi provisions and runs for {{ ws.contextLabel }}.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New database
      </button>
    </div>

    <div class="card">
      <div v-if="loading && dbs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="dbs.length === 0" class="empty-state">
        <span class="mdi mdi-database-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No databases yet</h3>
        <p>Provision PostgreSQL, MySQL, MariaDB, Redis, or MongoDB in one click.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Provision a database</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Database</th><th>Engine</th><th>Node</th><th>Size</th><th>Status</th></tr></thead>
          <tbody>
            <tr v-for="d in dbs" :key="d.id" class="row-clickable" @click="router.push(`/databases/${d.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm">{{ (d.display_name || d.name).charAt(0).toUpperCase() }}</span>
                  <span class="cell-text">
                    <span class="cell-title">{{ d.display_name || d.name }}</span>
                    <span class="cell-sub">{{ d.name }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ d.engine }} {{ d.version }}</td>
              <td class="cell-sub">
                <span v-if="d.server_name"><span class="mdi mdi-server-network"></span> {{ d.server_name }}</span>
                <span v-else>—</span>
              </td>
              <td class="cell-sub">{{ fmtBytes(d.size_bytes) }}</td>
              <td><span class="badge badge-dot" :class="badge(d.status)">{{ d.status }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>New database</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. app-db" required autofocus />
            </div>
            <NodePicker v-model="form.server_id" />
            <div class="form-group">
              <label class="form-label">Engine</label>
              <div class="engine-grid">
                <button
                  v-for="e in engines"
                  :key="e.value"
                  type="button"
                  class="engine-option"
                  :class="{ active: form.engine === e.value }"
                  @click="form.engine = e.value"
                >
                  <span class="mdi" :class="e.icon"></span>{{ e.label }}
                </button>
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">Version / tag <span class="text-muted">(optional)</span></label>
              <input v-model="form.version" class="form-input" :placeholder="defaultVersion" />
              <p class="form-hint">Image tag for <code>{{ form.engine }}:{{ form.version.trim() || defaultVersion }}</code>. Leave blank for the default.</p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Data volume size (MB) <span class="text-muted">(optional)</span></label>
              <input v-model.number="form.size_mb" type="number" min="0" class="form-input" placeholder="Leave empty for no declared limit" />
              <p class="form-hint">Declared capacity of the instance's data volume, recorded for quota accounting. Hard enforcement depends on the node's storage backend.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="creating">{{ creating ? 'Provisioning…' : 'Create database' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.engine-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.engine-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px;
  border: 1px solid var(--border-input);
  border-radius: var(--radius);
  background: var(--bg-input);
  color: var(--text-secondary);
  font-size: 14px;
  font-family: inherit;
  cursor: pointer;
  transition: all var(--transition);
}
.engine-option .mdi { font-size: 18px; }
.engine-option:hover { border-color: var(--text-muted); }
.engine-option.active {
  border-color: var(--primary-500);
  background: var(--primary-50);
  color: var(--primary-700);
  font-weight: 500;
}
.text-muted { color: var(--text-muted); }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-family: monospace; }
</style>
