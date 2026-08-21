<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { gitopsApi, type GitSourceInput } from '@/api/gitops'
import { gitRepositoryApi } from '@/api/gitRepositories'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { GitSource, GitSourceStatus, GitRepository, ApplyPlan, PlanAction, ApplyResult } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const route = useRoute()
const { currentWorkspaceId } = storeToRefs(ws)

// Display disposition (list/grid), persisted across sessions. Grid is the default: a project's
// sync state, last commit and error are what you come here to see, and the cards show them without
// a row-to-column scan. The test is for 'list' rather than 'grid' so that anyone who previously
// chose list keeps it — only an unset preference moves.
type ViewMode = 'list' | 'grid'
const VIEW_KEY = 'mb_gitops_view'
const view = ref<ViewMode>(localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'grid')
watch(view, (v) => localStorage.setItem(VIEW_KEY, v))

const items = ref<GitSource[]>([])
const credentials = ref<GitRepository[]>([])
const loading = ref(false)
const syncing = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<GitSource | null>(null)
const form = ref<GitSourceInput>(emptyForm())

// Diff viewer
const showDiff = ref(false)
const diffLoading = ref(false)
const diffSource = ref<GitSource | null>(null)
const diffPlan = ref<ApplyPlan | null>(null)

function emptyForm(): GitSourceInput {
  return { name: '', repo_url: '', ref: 'main', path: '.', git_repository_id: null, sync_policy: 'manual', prune: false, self_heal: false, allow_empty: false }
}

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    const [sources, repos] = await Promise.all([gitopsApi.list(id), gitRepositoryApi.list(id)])
    items.value = sources.data.data ?? []
    credentials.value = repos.data.data ?? []
    // The detail page's Edit action deep-links here with ?edit=<id>.
    const editId = Number(route.query.edit)
    if (editId) {
      const target = items.value.find((i) => i.id === editId)
      if (target) openEdit(target)
      router.replace({ query: {} })
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

// allow_empty only makes sense with prune; clear it when prune is turned off.
watch(() => form.value.prune, (on) => { if (!on) form.value.allow_empty = false })

function openDetail(s: GitSource) {
  router.push({ name: 'gitops-detail', params: { id: s.id } })
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showModal.value = true
}
function openEdit(s: GitSource) {
  editing.value = s
  form.value = {
    name: s.name, repo_url: s.repo_url, ref: s.ref, path: s.path,
    git_repository_id: s.git_repository_id ?? null, sync_policy: s.sync_policy,
    prune: s.prune, self_heal: s.self_heal, allow_empty: s.allow_empty,
  }
  showModal.value = true
}

async function save() {
  if (!currentWorkspaceId.value) return
  if (!form.value.git_repository_id) {
    notify.error('Select a git repository')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await gitopsApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success('Git source updated')
    } else {
      await gitopsApi.create(currentWorkspaceId.value, form.value)
      notify.success('Git source created')
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function sync(s: GitSource) {
  if (!currentWorkspaceId.value) return
  syncing.value = s.id
  try {
    const res = await gitopsApi.sync(currentWorkspaceId.value, s.id)
    const updated = res.data.data
    const idx = items.value.findIndex((i) => i.id === s.id)
    if (idx >= 0) items.value[idx] = updated
    if (updated.status === 'error') notify.error(`${s.name}: ${updated.message || 'sync failed'}`)
    else notify.success(`${s.name}: synced`)
  } catch (e) {
    notify.apiError(e, 'Sync failed')
    load(currentWorkspaceId.value)
  } finally {
    syncing.value = null
  }
}

async function openDiff(s: GitSource) {
  if (!currentWorkspaceId.value) return
  diffSource.value = s
  diffPlan.value = null
  showDiff.value = true
  diffLoading.value = true
  try {
    diffPlan.value = (await gitopsApi.diff(currentWorkspaceId.value, s.id)).data.data
  } catch (e) {
    notify.apiError(e, 'Could not compute diff')
    showDiff.value = false
  } finally {
    diffLoading.value = false
  }
}

const toDelete = ref<GitSource | null>(null)
const deleteResources = ref(false)
const deleting = ref(false)
// Reset the opt-in each time the dialog opens so a destructive cascade is never
// pre-checked from a previous deletion.
watch(toDelete, (v) => { if (v) deleteResources.value = false })

// Teardown follow-up: after a cascade delete, show which resources were removed
// (and any that failed). Each delete change is paired with its failure (if any).
const teardown = ref<{ name: string; result: ApplyResult } | null>(null)
const teardownItems = computed(() => {
  const t = teardown.value?.result
  if (!t) return []
  const fails = new Map((t.failures ?? []).map((f) => [`${f.kind}/${f.name}`, f.error]))
  return (t.plan?.changes ?? [])
    .filter((c) => c.action === 'delete')
    .map((c) => ({ kind: c.kind, name: c.name, error: fails.get(`${c.kind}/${c.name}`) }))
})
const teardownFailed = computed(() => (teardown.value?.result.failures?.length ?? 0) > 0)

async function confirmDelete() {
  if (!currentWorkspaceId.value || !toDelete.value) return
  const cascade = deleteResources.value
  const name = toDelete.value.name
  deleting.value = true
  try {
    const res = (await gitopsApi.remove(currentWorkspaceId.value, toDelete.value.id, cascade)).data.data
    toDelete.value = null
    load(currentWorkspaceId.value)
    if (cascade && res?.teardown) {
      // Surface the per-resource outcome in a follow-up dialog.
      teardown.value = { name, result: res.teardown }
      if ((res.teardown.failures?.length ?? 0) > 0) notify.error('Some resources could not be removed')
      else notify.success(res.message || 'Git source and its resources deleted')
    } else {
      notify.success(res?.message || 'Git source deleted')
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

const statusMeta: Record<GitSourceStatus, { label: string; badge: string; icon: string }> = {
  synced: { label: 'Synced', badge: 'badge-success', icon: 'mdi-check-circle-outline' },
  out_of_sync: { label: 'Out of sync', badge: 'badge-warning', icon: 'mdi-alert-circle-outline' },
  progressing: { label: 'Progressing', badge: 'badge-info', icon: 'mdi-loading mdi-spin' },
  error: { label: 'Error', badge: 'badge-danger', icon: 'mdi-close-circle-outline' },
  unknown: { label: 'Never synced', badge: 'badge-neutral', icon: 'mdi-help-circle-outline' },
}
const actionBadge: Record<PlanAction, string> = {
  create: 'badge-success', update: 'badge-warning', delete: 'badge-danger', noop: 'badge-neutral',
}

const planChanges = computed(() => (diffPlan.value?.changes ?? []).filter((c) => c.action !== 'noop'))
function shortSha(sha?: string) { return sha ? sha.slice(0, 7) : '—' }
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>GitOps</h1>
        <p class="subtitle">Continuously deploy from Git repositories of <code>miabi.io/v1</code> manifests.</p>
      </div>
        <div v-if="items.length" class="view-toggle" role="group" aria-label="Display as">
          <button class="btn-icon" :class="{ active: view === 'list' }" title="List view" aria-label="List view" @click="view = 'list'"><span class="mdi mdi-format-list-bulleted"></span></button>
          <button class="btn-icon" :class="{ active: view === 'grid' }" title="Grid view" aria-label="Grid view" @click="view = 'grid'"><span class="mdi mdi-view-grid"></span></button>
        </div>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
          <span class="mdi mdi-plus"></span> New git source
        </button>
    </div>

    <div v-if="loading && items.length === 0" class="card"><div class="card-body"><span class="spinner"></span></div></div>
    <div v-else-if="items.length === 0" class="card">
      <div class="empty-state">
        <span class="mdi mdi-source-branch-sync" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No git sources yet</h3>
        <p>Point Miabi at a repository of manifests and it will keep your workspace in sync.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Create a git source</button>
      </div>
    </div>

    <!-- List view -->
    <div v-else-if="view === 'list'" class="card">
      <div class="table-wrapper">
        <table>
          <thead><tr><th>Source</th><th>Status</th><th>Revision</th><th>Policy</th><th></th></tr></thead>
          <tbody>
            <tr v-for="s in items" :key="s.id" class="row-clickable" @click="openDetail(s)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-git" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title link">{{ s.name }} <span class="mdi mdi-graph-outline open-hint"></span></span>
                    <span class="cell-sub">{{ s.repo_url }} · {{ s.ref }}<span v-if="s.path && s.path !== '.'">/{{ s.path }}</span></span>
                  </span>
                </div>
              </td>
              <td>
                <span class="badge" :class="statusMeta[s.status].badge">
                  <span class="mdi" :class="statusMeta[s.status].icon"></span> {{ statusMeta[s.status].label }}
                </span>
                <div v-if="s.status === 'error' && s.message" class="cell-sub err">{{ s.message }}</div>
              </td>
              <td class="cell-sub mono">
                <span v-if="s.last_synced_commit">{{ shortSha(s.last_synced_commit) }}</span>
                <span v-else>—</span>
              </td>
              <td>
                <span class="badge badge-neutral">{{ s.sync_policy }}</span>
                <span v-if="s.prune" class="badge badge-neutral" title="Removes resources deleted from Git">prune</span>
                <span v-if="s.self_heal" class="badge badge-neutral" title="Re-applies on drift">self-heal</span>
              </td>
              <td class="text-right table-actions" @click.stop>
                <button class="btn-icon btn-icon-muted" title="Open topology" aria-label="Open topology" @click="openDetail(s)"><span class="mdi mdi-graph-outline"></span></button>
                <button class="btn-icon btn-icon-muted" title="View diff" aria-label="View diff" @click="openDiff(s)"><span class="mdi mdi-file-compare"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Sync now" aria-label="Sync now" :disabled="syncing === s.id" @click="sync(s)">
                  <span class="mdi" :class="syncing === s.id ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = s"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Grid view -->
    <div v-else class="gitops-grid">
      <div v-for="s in items" :key="s.id" class="gitops-card card row-clickable" @click="openDetail(s)">
        <div class="gc-head">
          <span class="avatar avatar-sm"><span class="mdi mdi-git" style="font-size: 14px"></span></span>
          <span class="cell-text">
            <span class="cell-title link">{{ s.name }} <span class="mdi mdi-graph-outline open-hint"></span></span>
            <span class="cell-sub" :title="s.repo_url">{{ s.repo_url }}</span>
          </span>
          <span class="badge gc-status" :class="statusMeta[s.status].badge">
            <span class="mdi" :class="statusMeta[s.status].icon"></span> {{ statusMeta[s.status].label }}
          </span>
        </div>
        <div v-if="s.status === 'error' && s.message" class="cell-sub err">{{ s.message }}</div>
        <div class="gc-meta">
          <span class="cell-sub mono">{{ s.ref }}<span v-if="s.path && s.path !== '.'">/{{ s.path }}</span></span>
          <span class="cell-sub mono" title="Last synced commit"><span class="mdi mdi-source-commit"></span> {{ shortSha(s.last_synced_commit) }}</span>
        </div>
        <div class="gc-badges">
          <span class="badge badge-neutral">{{ s.sync_policy }}</span>
          <span v-if="s.prune" class="badge badge-neutral" title="Removes resources deleted from Git">prune</span>
          <span v-if="s.self_heal" class="badge badge-neutral" title="Re-applies on drift">self-heal</span>
        </div>
        <div class="gc-actions table-actions" @click.stop>
          <button class="btn-icon btn-icon-muted" title="Open topology" aria-label="Open topology" @click="openDetail(s)"><span class="mdi mdi-graph-outline"></span></button>
          <button class="btn-icon btn-icon-muted" title="View diff" aria-label="View diff" @click="openDiff(s)"><span class="mdi mdi-file-compare"></span></button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Sync now" aria-label="Sync now" :disabled="syncing === s.id" @click="sync(s)">
            <span class="mdi" :class="syncing === s.id ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
          </button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = s"><span class="mdi mdi-delete-outline"></span></button>
        </div>
      </div>
    </div>

    <!-- Create / edit -->
    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit git source' : 'New git source' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. production" required autofocus aria-label="Name" />
            </div>
            <div class="form-group">
              <label class="form-label">Git repository</label>
              <select v-model="form.git_repository_id" class="form-select" required aria-label="Git repository">
                <option :value="null" disabled>Select a repository…</option>
                <option v-for="c in credentials" :key="c.id" :value="c.id">{{ c.name }} — {{ c.url }}</option>
              </select>
              <p v-if="credentials.length === 0" class="hint">
                No git repositories yet — <router-link to="/git-repositories">add one</router-link> (public or private) first.
              </p>
              <p v-else class="hint">
                The repository URL and credentials come from the selected git repository.
                <router-link to="/git-repositories">Manage repositories →</router-link>
              </p>
            </div>
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Ref</label>
                <input v-model="form.ref" class="form-input" placeholder="main" aria-label="Ref" />
              </div>
              <div class="form-group">
                <label class="form-label">Path</label>
                <input v-model="form.path" class="form-input mono" placeholder="envs/prod" aria-label="Path" />
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">Sync policy</label>
              <div class="tabs" style="margin-bottom: 0">
                <button type="button" class="tab" :class="{ active: form.sync_policy === 'manual' }" @click="form.sync_policy = 'manual'">Manual</button>
                <button type="button" class="tab" :class="{ active: form.sync_policy === 'auto' }" @click="form.sync_policy = 'auto'">Automatic</button>
              </div>
              <p class="hint">Automatic sources reconcile on the 3-minute sweep and on push webhook.</p>
            </div>
            <label class="check"><input type="checkbox" v-model="form.prune" /> <span>Prune — delete resources removed from Git</span></label>
            <label class="check"><input type="checkbox" v-model="form.self_heal" /> <span>Self-heal — re-apply when live state drifts</span></label>
            <label class="check" :class="{ disabled: !form.prune }">
              <input type="checkbox" v-model="form.allow_empty" :disabled="!form.prune" />
              <span>Allow empty — let an empty repo prune <strong>all</strong> resources (intentional teardown)</span>
            </label>
            <p v-if="form.allow_empty" class="hint warn">
              <span class="mdi mdi-alert-outline"></span>
              With this on, a commit that removes every manifest will delete all managed resources. A missing path is still always an error.
            </p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Create') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Diff viewer -->
    <Teleport to="body">
      <AppModal v-if="showDiff" dialog-class="modal-lg" @close="showDiff = false">
        <div class="modal-header">
          <h3>Diff — {{ diffSource?.name }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDiff = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-if="diffLoading" class="card-body"><span class="spinner"></span></div>
          <div v-else-if="planChanges.length === 0" class="empty-state">
            <span class="mdi mdi-check-circle-outline" style="font-size: 40px; color: var(--success-600)"></span>
            <h3>In sync</h3>
            <p>The workspace matches the desired state in Git.</p>
          </div>
          <div v-else class="diff-list">
            <div v-for="(c, i) in planChanges" :key="i" class="diff-item">
              <div class="diff-head">
                <span class="badge" :class="actionBadge[c.action]">{{ c.action }}</span>
                <span class="mono diff-name">{{ c.kind }}/{{ c.name }}</span>
                <span v-if="c.reason" class="cell-sub">{{ c.reason }}</span>
              </div>
              <table v-if="c.fields && c.fields.length" class="diff-fields">
                <tr v-for="(f, j) in c.fields" :key="j">
                  <td class="mono diff-field">{{ f.field }}</td>
                  <td class="mono diff-from">{{ f.from || '∅' }}</td>
                  <td class="diff-arrow"><span class="mdi mdi-arrow-right"></span></td>
                  <td class="mono diff-to">{{ f.to || '∅' }}</td>
                </tr>
              </table>
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="showDiff = false">Close</button>
          <button v-if="ws.canEdit && diffSource && planChanges.length" class="btn btn-primary" :disabled="syncing === diffSource.id"
            @click="diffSource && sync(diffSource).then(() => { showDiff = false })">
            <span class="mdi mdi-sync"></span> Sync now
          </button>
        </div>
      </AppModal>
    </Teleport>

    <!-- Teardown follow-up: what a cascade delete removed -->
    <Teleport to="body">
      <AppModal v-if="teardown" dialog-class="modal-lg" @close="teardown = null">
        <div class="modal-header">
          <h3>Resources removed — {{ teardown.name }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="teardown = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p v-if="teardownFailed" class="teardown-summary failed">
            <span class="mdi mdi-alert-circle-outline"></span>
            {{ teardown.result.applied }} removed, {{ teardown.result.failures?.length }} failed — the failed resources may need manual cleanup.
          </p>
          <p v-else class="teardown-summary ok">
            <span class="mdi mdi-check-circle-outline"></span>
            {{ teardownItems.length }} resource{{ teardownItems.length === 1 ? '' : 's' }} removed.
          </p>
          <div v-if="teardownItems.length === 0" class="empty-state">
            <p>This project had no managed resources to remove.</p>
          </div>
          <div v-else class="diff-list">
            <div v-for="(it, i) in teardownItems" :key="i" class="diff-item">
              <div class="diff-head">
                <span class="badge" :class="it.error ? 'badge-danger' : 'badge-neutral'">{{ it.error ? 'failed' : 'removed' }}</span>
                <span class="mono diff-name">{{ it.kind }}/{{ it.name }}</span>
                <span v-if="it.error" class="cell-sub teardown-err">{{ it.error }}</span>
              </div>
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="teardown = null">Close</button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete git source"
      :message="`Delete git source &quot;${toDelete?.name}&quot;?`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    >
      <label class="cascade-option">
        <input type="checkbox" v-model="deleteResources" />
        <span>
          Also delete the resources this project created
          <small>Removes its apps, databases, volumes and stacks. This cannot be undone.</small>
        </span>
      </label>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.subtitle code, .mono { font-family: 'JetBrains Mono', monospace; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.badge .mdi { font-size: 13px; }
.err { color: var(--danger-600); max-width: 320px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.hint { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
.check { display: flex; align-items: center; gap: 8px; font-size: 13px; margin-top: 10px; cursor: pointer; }
.check.disabled { opacity: 0.5; cursor: not-allowed; }
.hint.warn { color: var(--warning-700, var(--warning-600)); display: flex; align-items: center; gap: 6px; }
.modal-lg { max-width: 720px; }
.table-actions .badge + .badge { margin-left: 4px; }
.diff-list { display: flex; flex-direction: column; gap: 12px; }
.diff-item { border: 1px solid var(--border-primary); border-radius: 8px; padding: 10px 12px; }
.diff-head { display: flex; align-items: center; gap: 10px; }
.diff-name { font-size: 13px; font-weight: 600; }
.diff-fields { width: 100%; margin-top: 8px; border-collapse: collapse; }
.diff-fields td { padding: 3px 6px; font-size: 12px; vertical-align: top; }
.diff-field { color: var(--text-muted); white-space: nowrap; }
.diff-from { color: var(--danger-600); }
.diff-to { color: var(--success-600); }
.diff-arrow { color: var(--text-muted); width: 20px; text-align: center; }
.row-clickable { cursor: pointer; }
.row-clickable:hover { background: var(--surface-hover, rgba(0, 0, 0, 0.025)); }

/* Display disposition (list/grid) */
.header-actions { display: flex; align-items: center; gap: 10px; }
.view-toggle { display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; background: transparent; }
.view-toggle .btn-icon { border: none; outline: none; border-radius: 0; height: 34px; width: 34px; background: var(--bg-secondary); color: var(--text-muted); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.view-toggle .btn-icon+.btn-icon { border-left: 1px solid var(--border-primary); }
.view-toggle .btn-icon.active { background: var(--primary-600); color: #fff; }
.gitops-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.gitops-card { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; }
.gitops-card:hover { border-color: var(--border-strong, var(--primary-300, var(--border-primary))); }
.gc-head { display: flex; align-items: flex-start; gap: 10px; }
.gc-head .cell-text { flex: 1; min-width: 0; }
.gc-head .cell-sub { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.gc-status { flex-shrink: 0; }
.gc-meta { display: flex; justify-content: space-between; gap: 8px; }
.gc-badges { display: flex; flex-wrap: wrap; gap: 4px; }
.gc-actions { display: flex; gap: 2px; border-top: 1px solid var(--border-primary); padding-top: 8px; margin-top: auto; }
.cell-title.link { display: inline-flex; align-items: center; gap: 6px; }
.open-hint { font-size: 13px; color: var(--text-muted); opacity: 0; transition: opacity 0.12s; }
.row-clickable:hover .open-hint { opacity: 1; }
.cascade-option { display: flex; gap: 8px; align-items: flex-start; margin-top: 14px; cursor: pointer; font-size: 14px; color: var(--text-primary); }
.cascade-option input { margin-top: 2px; }
.cascade-option small { display: block; margin-top: 2px; color: var(--text-secondary); font-size: 12px; }
.teardown-summary { display: flex; align-items: center; gap: 8px; font-size: 14px; margin: 0 0 14px; }
.teardown-summary.ok { color: var(--success-600); }
.teardown-summary.failed { color: var(--danger-500); }
.teardown-err { color: var(--danger-500); }
</style>
