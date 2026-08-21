<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { stackApi, type StackActionResult } from '@/api/stacks'
import { appApi } from '@/api/apps'
import type { Stack, Application, StackEnvVar, AppEvent } from '@/api/types'
import MetadataCard from '@/components/MetadataCard.vue'
import EnvVarModal from '@/components/EnvVarModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const stackId = computed(() => Number(route.params.id))
const wid = computed(() => ws.currentWorkspaceId)

const stack = ref<Stack | null>(null)
const allApps = ref<Application[]>([])
const envVars = ref<StackEnvVar[]>([])
const events = ref<AppEvent[]>([])
const loading = ref(false)
const showAdd = ref(false)
const showEdit = ref(false)
const showDelete = ref(false)
const busyAction = ref('')
const deleteWithApps = ref(false)
const selectedAppId = ref<number | null>(null)
const editForm = ref({ name: '', description: '' })
// Add/update env var modal — the same component the app Environment tab uses.
// editingEnvKey is set while updating an existing variable (null when adding).
const showEnvModal = ref(false)
const editingEnvKey = ref<string | null>(null)
const envForm = ref({ key: '', value: '', secret: false })
const savingEnv = ref(false)
const showEnvImport = ref(false)
const envImport = ref({ content: '', secret: false })
const importingEnv = ref(false)

// Apps in this workspace that are not already in this stack.
const available = computed(() => allApps.value.filter((a) => a.stack_id !== stackId.value))

// Aggregate health derived from member statuses.
const aggregate = computed(() => {
  const apps = stack.value?.apps ?? []
  const running = apps.filter((a) => a.status === 'running').length
  return { running, total: apps.length }
})
function aggregateClass() {
  const { running, total } = aggregate.value
  if (total === 0) return 'badge-neutral'
  if (running === total) return 'badge-success'
  if (running === 0) return 'badge-danger'
  return 'badge-warning'
}

async function load() {
  if (!wid.value) return
  loading.value = true
  try {
    stack.value = (await stackApi.get(wid.value, stackId.value)).data.data
    allApps.value = (await appApi.list(wid.value)).data.data ?? []
    envVars.value = (await stackApi.envVars(wid.value, stackId.value)).data.data ?? []
    events.value = (await stackApi.events(wid.value, stackId.value)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch([stackId, wid], load, { immediate: true })

function summarize(results: StackActionResult[], verb: string) {
  const ok = results.filter((r) => r.status === 'ok').length
  const skipped = results.filter((r) => r.status === 'skipped').length
  const failed = results.filter((r) => r.status === 'failed').length
  let msg = `${ok} app${ok === 1 ? '' : 's'} ${verb}`
  if (skipped) msg += `, ${skipped} skipped (not deployed)`
  if (failed) msg += `, ${failed} failed`
  notify[failed ? 'error' : 'success'](msg)
}

async function lifecycle(action: 'start' | 'stop' | 'restart', rolling = false) {
  if (!wid.value || busyAction.value) return
  busyAction.value = rolling ? 'rolling' : action
  try {
    const results: StackActionResult[] =
      action === 'restart'
        ? (await stackApi.restart(wid.value, stackId.value, rolling)).data.data ?? []
        : (await stackApi[action](wid.value, stackId.value)).data.data ?? []
    summarize(results, action === 'start' ? 'started' : action === 'stop' ? 'stopped' : 'restarted')
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busyAction.value = ''
  }
}

async function deployAll() {
  if (!wid.value || busyAction.value) return
  busyAction.value = 'deploy'
  try {
    const results = (await stackApi.deploy(wid.value, stackId.value)).data.data ?? []
    const queued = results.filter((r) => r.status === 'queued').length
    const failed = results.filter((r) => r.status === 'failed').length
    notify[failed ? 'error' : 'success'](`${queued} deploy${queued === 1 ? '' : 's'} queued${failed ? `, ${failed} failed` : ''}`)
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busyAction.value = ''
  }
}

function openEdit() {
  if (!stack.value) return
  editForm.value = { name: stack.value.name, description: stack.value.description ?? '' }
  showEdit.value = true
}

async function saveEdit() {
  if (!wid.value) return
  try {
    await stackApi.update(wid.value, stackId.value, { name: editForm.value.name.trim(), description: editForm.value.description.trim() })
    notify.success('Stack updated')
    showEdit.value = false
    load()
  } catch (e) {
    notify.apiError(e)
  }
}

function openAdd() {
  selectedAppId.value = available.value[0]?.id ?? null
  showAdd.value = true
}

async function addApp() {
  if (!wid.value || !selectedAppId.value) return
  try {
    await stackApi.addApp(wid.value, stackId.value, selectedAppId.value)
    notify.success('Application added to stack')
    showAdd.value = false
    load()
  } catch (e) {
    notify.apiError(e)
  }
}

const pendingRemove = ref<Application | null>(null)
const removing = ref(false)

async function confirmRemoveApp() {
  if (!wid.value || !pendingRemove.value) return
  removing.value = true
  try {
    await stackApi.removeApp(wid.value, stackId.value, pendingRemove.value.id)
    notify.success('Application removed from stack')
    pendingRemove.value = null
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    removing.value = false
  }
}

// openEnvModal opens the modal to create a new variable; openEnvEdit loads an
// existing one for update (secret values aren't returned, so they're re-entered).
function openEnvModal() {
  editingEnvKey.value = null
  envForm.value = { key: '', value: '', secret: false }
  showEnvModal.value = true
}
function openEnvEdit(v: StackEnvVar) {
  editingEnvKey.value = v.key
  envForm.value = { key: v.key, value: v.is_secret ? '' : v.value, secret: v.is_secret }
  showEnvModal.value = true
}

async function saveEnv(v: { key: string; value: string; secret: boolean }) {
  if (!wid.value || !v.key) return
  savingEnv.value = true
  try {
    await stackApi.setEnvVar(wid.value, stackId.value, v.key, v.value, v.secret)
    notify.success((editingEnvKey.value ? 'Variable updated' : 'Variable added') + ' — applies on each app’s next deploy')
    showEnvModal.value = false
    envVars.value = (await stackApi.envVars(wid.value, stackId.value)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingEnv.value = false
  }
}

async function deleteEnv(key: string) {
  if (!wid.value) return
  try {
    await stackApi.deleteEnvVar(wid.value, stackId.value, key)
    envVars.value = envVars.value.filter((v) => v.key !== key)
  } catch (e) {
    notify.apiError(e)
  }
}

async function importEnv() {
  if (!wid.value || !envImport.value.content.trim()) return
  importingEnv.value = true
  try {
    const res = (await stackApi.importEnvVars(wid.value, stackId.value, envImport.value.content, envImport.value.secret)).data.data
    notify.success(`Imported ${res?.imported ?? 0} variable(s)`)
    showEnvImport.value = false
    envImport.value = { content: '', secret: false }
    envVars.value = (await stackApi.envVars(wid.value, stackId.value)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    importingEnv.value = false
  }
}

async function confirmDelete() {
  if (!wid.value) return
  try {
    await stackApi.remove(wid.value, stackId.value, deleteWithApps.value)
    notify.success(deleteWithApps.value ? 'Stack and its applications deleted' : 'Stack deleted')
    router.push('/stacks')
  } catch (e) {
    notify.apiError(e)
  }
}

function badge(status: string) {
  return status === 'running' ? 'badge-success' : status === 'failed' ? 'badge-danger' : status === 'deploying' ? 'badge-warning' : 'badge-neutral'
}
function eventIcon(type: string): string {
  if (type.startsWith('deploy')) return 'mdi-rocket-launch-outline'
  if (type.startsWith('rollback')) return 'mdi-backup-restore'
  if (type.startsWith('release')) return 'mdi-tag-outline'
  if (type === 'container.died' || type === 'container.oom') return 'mdi-alert-circle-outline'
  if (type.startsWith('container')) return 'mdi-cube-outline'
  if (type.startsWith('env')) return 'mdi-tune-variant'
  return 'mdi-circle-small'
}
function relTime(ts: string): string {
  const s = Math.round((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return new Date(ts).toLocaleString()
}
const appName = (id: number) => allApps.value.find((a) => a.id === id)?.name ?? `app #${id}`
</script>

<template>
  <div v-if="stack">
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/stacks')">
          <span class="mdi mdi-arrow-left"></span> Stacks
        </button>
        <h1 style="margin-top: 8px">
          {{ stack.name }}
          <span class="badge" :class="aggregateClass()" style="margin-left: 8px; vertical-align: middle">
            {{ aggregate.running }}/{{ aggregate.total }} running
          </span>
        </h1>
        <div class="text-muted text-sm">{{ stack.description || 'No description' }}</div>
        <div class="text-muted text-sm">Docker project: <code>{{ stack.docker_name }}</code></div>
      </div>
      <div class="flex items-center gap-2">
        <template v-if="ws.canEdit && stack.apps && stack.apps.length">
          <button class="btn btn-primary btn-sm" :disabled="!!busyAction" @click="deployAll">
            <span class="mdi mdi-rocket-launch-outline"></span> {{ busyAction === 'deploy' ? 'Deploying…' : 'Deploy all' }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="!!busyAction" @click="lifecycle('start')">
            <span class="mdi mdi-play"></span> {{ busyAction === 'start' ? 'Starting…' : 'Start' }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="!!busyAction" @click="lifecycle('stop')">
            <span class="mdi mdi-stop"></span> {{ busyAction === 'stop' ? 'Stopping…' : 'Stop' }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="!!busyAction" @click="lifecycle('restart')">
            <span class="mdi mdi-restart"></span> {{ busyAction === 'restart' ? 'Restarting…' : 'Restart' }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="!!busyAction" title="Restart one app at a time" @click="lifecycle('restart', true)">
            <span class="mdi mdi-sync"></span> {{ busyAction === 'rolling' ? 'Rolling…' : 'Rolling' }}
          </button>
          <span class="header-divider"></span>
        </template>
        <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" @click="openEdit">Edit</button>
        <button v-if="ws.canEdit" class="btn btn-danger btn-sm" @click="deleteWithApps = false; showDelete = true">Delete</button>
      </div>
    </div>

    <div class="card">
      <div class="card-header flex items-center justify-between">
        <h3>Applications</h3>
        <button v-if="ws.canEdit" class="btn btn-primary btn-sm" @click="openAdd" :disabled="available.length === 0">
          <span class="mdi mdi-plus"></span> Add application
        </button>
      </div>
      <div v-if="loading && !stack.apps" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="!stack.apps || stack.apps.length === 0" class="empty-state">
        <span class="mdi mdi-cube-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No applications in this stack</h3>
        <p>Add an application to group it under this stack.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Application</th><th>Source</th><th>Status</th><th></th></tr></thead>
          <tbody>
            <tr v-for="a in stack.apps" :key="a.id" class="row-clickable" @click="router.push(`/apps/${a.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm">{{ (a.display_name || a.name).charAt(0).toUpperCase() }}</span>
                  <span class="cell-text">
                    <span class="cell-title">{{ a.display_name || a.name }}</span>
                    <span class="cell-sub">{{ a.name }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ a.source_type === 'git' ? a.git_repo : `${a.image}:${a.tag || 'latest'}` }}</td>
              <td><span class="badge badge-dot" :class="badge(a.status)">{{ a.status }}</span></td>
              <td class="text-right" @click.stop>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Remove from stack" aria-label="Remove from stack" @click="pendingRemove = a">
                  <span class="mdi mdi-link-variant-off"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Shared environment variables -->
    <div class="card" style="margin-top: 20px">
      <div class="card-header">
        <h3>Shared environment</h3>
        <div class="flex items-center gap-2">
          <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" @click="showEnvImport = true"><span class="mdi mdi-import"></span> Import .env</button>
          <button v-if="ws.canEdit" class="btn btn-primary btn-sm" @click="openEnvModal"><span class="mdi mdi-plus"></span> Add variable</button>
        </div>
      </div>
      <div class="card-body">
        <p class="text-muted text-sm" style="margin-top: 0">Injected into every app in the stack on its next deploy. An app's own variable with the same key wins.</p>
        <table v-if="envVars.length" style="margin-bottom: 12px">
          <tbody>
            <tr v-for="v in envVars" :key="v.id">
              <td class="cell-title" style="font-family: monospace">{{ v.key }}</td>
              <td class="cell-sub" style="font-family: monospace">{{ v.is_secret ? '••••••••' : v.value }}</td>
              <td class="text-right">
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEnvEdit(v)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="deleteEnv(v.key)"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!envVars.length" class="text-muted text-sm" style="margin-bottom: 0">
          No shared variables yet.
        </p>
      </div>
    </div>

    <MetadataCard :metadata="stack.metadata" title="Metadata" style="margin-top: 20px" />

    <MetadataCard :metadata="stack.annotations" title="Annotations" :reserved="false" style="margin-top: 20px" />

    <!-- Activity feed -->
    <div class="card" style="margin-top: 20px">
      <div class="card-header"><h3>Activity</h3></div>
      <div v-if="events.length === 0" class="empty-state" style="padding: 28px">
        <span class="mdi mdi-timeline-text-outline" style="font-size: 32px; color: var(--text-muted)"></span>
        <p>No activity yet.</p>
      </div>
      <ul v-else class="timeline">
        <li v-for="e in events" :key="e.id" class="event row-clickable" @click="router.push(`/apps/${e.application_id}`)">
          <span class="event-icon" :class="`sev-${e.severity}`"><span class="mdi" :class="eventIcon(e.type)"></span></span>
          <div class="event-body">
            <div class="event-row">
              <span class="event-msg">{{ e.message || e.type }}</span>
              <span class="event-time">{{ relTime(e.created_at) }}</span>
            </div>
            <span class="event-type">{{ appName(e.application_id) }} · {{ e.type }}</span>
          </div>
        </li>
      </ul>
    </div>

    <Teleport to="body">
      <EnvVarModal
        :open="showEnvModal"
        :editing-key="editingEnvKey"
        :initial="envForm"
        :saving="savingEnv"
        apply-note="Applies to every app in the stack on its next deploy."
        @close="showEnvModal = false"
        @save="saveEnv"
      />

      <AppModal v-if="showAdd" @close="showAdd = false">
        <div class="modal-header">
          <h3>Add application</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showAdd = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="addApp">
          <div class="modal-body">
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Application</label>
              <select v-model="selectedAppId" class="form-select" aria-label="Application">
                <option v-for="a in available" :key="a.id" :value="a.id">{{ a.name }}</option>
              </select>
              <p class="form-hint">Compose labels apply when the application is next deployed.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showAdd = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="!selectedAppId">Add</button>
          </div>
        </form>
      </AppModal>

      <AppModal v-if="showEdit" @close="showEdit = false">
        <div class="modal-header">
          <h3>Edit stack</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEdit = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveEdit">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="editForm.name" class="form-input" aria-label="Name" required autofocus />
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Description <span class="text-muted">(optional)</span></label>
              <input v-model="editForm.description" class="form-input" aria-label="Description" />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEdit = false">Cancel</button>
            <button type="submit" class="btn btn-primary">Save</button>
          </div>
        </form>
      </AppModal>

      <AppModal v-if="showEnvImport" max-width="560px" @close="showEnvImport = false">
        <div class="modal-header">
          <h3>Import shared .env</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEnvImport = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="importEnv">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Paste KEY=VALUE lines</label>
              <textarea v-model="envImport.content" class="form-input" rows="10" spellcheck="false" style="font-family: monospace; font-size: 12px" placeholder="SHARED_SECRET=...&#10;# comments and blank lines are ignored&#10;REGION=eu" aria-label="Paste KEY=VALUE lines" required></textarea>
            </div>
            <label class="checkbox-label" style="margin-bottom: 0"><input type="checkbox" v-model="envImport.secret" /> Mark all as secrets (encrypted)</label>
            <p class="form-hint">Existing keys are overwritten. Applies to each app on its next deploy.</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEnvImport = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="importingEnv">{{ importingEnv ? 'Importing…' : 'Import' }}</button>
          </div>
        </form>
      </AppModal>

      <AppModal v-if="showDelete" @close="showDelete = false">
        <div class="modal-header">
          <h3>Delete stack</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDelete = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p>Delete <strong>{{ stack.name }}</strong>?</p>
          <label class="checkbox-label" style="margin-top: 8px">
            <input type="checkbox" v-model="deleteWithApps" />
            Also delete its {{ stack.apps?.length ?? 0 }} application(s) and their containers
          </label>
          <p class="text-muted text-sm">{{ deleteWithApps ? 'Applications and their containers will be permanently removed.' : 'Applications are detached and keep running.' }}</p>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="showDelete = false">Cancel</button>
          <button type="button" class="btn btn-danger" @click="confirmDelete">{{ deleteWithApps ? 'Delete stack & apps' : 'Delete stack' }}</button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingRemove"
      title="Remove application"
      :message="pendingRemove ? `Remove &quot;${pendingRemove.name}&quot; from this stack?` : ''"
      confirm-label="Remove"
      variant="danger"
      :busy="removing"
      @confirm="confirmRemoveApp"
      @cancel="pendingRemove = null"
    />
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.header-divider { width: 1px; height: 22px; background: var(--border-secondary); margin: 0 4px; }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; }
code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; }
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
.event-type { font-size: 11px; color: var(--text-muted); font-family: monospace; }
</style>
