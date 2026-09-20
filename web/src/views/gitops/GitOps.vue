<script setup lang="ts">
import { useI18n } from 'vue-i18n'
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
const { t } = useI18n()
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
    notify.error(t('notify.gitOps.selectAGitRepository'))
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await gitopsApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success(t('notify.gitOps.updated'))
    } else {
      await gitopsApi.create(currentWorkspaceId.value, form.value)
      notify.success(t('notify.gitOps.created'))
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
    if (updated.status === 'error') notify.error(t('notify.gitOps.syncFailed', { name: s.name, message: updated.message || t('notify.gitOps.syncFailedFallback') }))
    else notify.success(t('notify.gitOps.synced', { name: s.name }))
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
      if ((res.teardown.failures?.length ?? 0) > 0) notify.error(t('notify.gitOps.someResourcesCouldNotBe'))
      else notify.success(res.message || t('notify.gitOps.deletedWithResources'))
    } else {
      notify.success(res?.message || t('notify.gitOps.deleted'))
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

const statusMeta: Record<GitSourceStatus, { label: string; badge: string; icon: string }> = {
  synced: { label: 'gitops.status.synced', badge: 'badge-success', icon: 'mdi-check-circle-outline' },
  out_of_sync: { label: 'gitops.status.out_of_sync', badge: 'badge-warning', icon: 'mdi-alert-circle-outline' },
  progressing: { label: 'gitops.status.progressing', badge: 'badge-info', icon: 'mdi-loading mdi-spin' },
  error: { label: 'gitops.status.error', badge: 'badge-danger', icon: 'mdi-close-circle-outline' },
  unknown: { label: 'gitops.status.unknown', badge: 'badge-neutral', icon: 'mdi-help-circle-outline' },
}
const actionBadge: Record<PlanAction, string> = {
  create: 'badge-success', update: 'badge-warning', delete: 'badge-danger', noop: 'badge-neutral',
}

const planChanges = computed(() => (diffPlan.value?.changes ?? []).filter((c) => c.action !== 'noop'))
function shortSha(sha?: string) { return sha ? sha.slice(0, 7) : '—' }
// The column shows the commit that last CHANGED something, which is often behind HEAD. The tooltip is
// where that gets explained, along with the message — the detail page has room to show it inline.
function syncedTitle(s: GitSource) {
  if (!s.last_synced_commit) return 'Nothing has been applied from this repository yet'
  const parts = [`Last applied commit: ${s.last_synced_commit}`]
  if (s.last_synced_subject) parts.push(s.last_synced_subject)
  if (s.last_synced_author) parts.push(`by ${s.last_synced_author}`)
  return parts.join('\n')
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('gitops.gitops') }}</h1>
        <i18n-t keypath="gitops.subtitle" tag="p" class="subtitle"><template #kind><code>miabi.io/v1</code></template></i18n-t>
      </div>
        <div v-if="items.length" class="view-toggle" role="group" :aria-label="$t('gitops.displayAs')">
          <button class="btn-icon" :class="{ active: view === 'list' }" :title="$t('gitops.listView')" :aria-label="$t('gitops.listView')" @click="view = 'list'"><span class="mdi mdi-format-list-bulleted"></span></button>
          <button class="btn-icon" :class="{ active: view === 'grid' }" :title="$t('gitops.gridView')" :aria-label="$t('gitops.gridView')" @click="view = 'grid'"><span class="mdi mdi-view-grid"></span></button>
        </div>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
          <span class="mdi mdi-plus"></span>{{ $t('gitops.newGitSource') }}</button>
    </div>

    <div v-if="loading && items.length === 0" class="card"><div class="card-body"><span class="spinner"></span></div></div>
    <div v-else-if="items.length === 0" class="card">
      <div class="empty-state">
        <span class="mdi mdi-source-branch-sync" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('gitops.noGitSourcesYet') }}</h3>
        <p>{{ $t('gitops.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('gitops.createAGitSource') }}</button>
      </div>
    </div>

    <!-- List view -->
    <div v-else-if="view === 'list'" class="card">
      <div class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('gitops.source') }}</th><th>{{ $t('dashboard.col.status') }}</th><th>{{ $t('gitops.revision') }}</th><th>{{ $t('gitops.policy') }}</th><th></th></tr></thead>
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
              <td class="cell-sub mono" :title="syncedTitle(s)">
                <span v-if="s.last_synced_commit">{{ shortSha(s.last_synced_commit) }}</span>
                <span v-else>—</span>
              </td>
              <td>
                <span class="badge badge-neutral">{{ s.sync_policy }}</span>
                <span v-if="s.prune" class="badge badge-neutral" :title="$t('gitops.pruneTitle')">{{ $t('gitops.prune') }}</span>
                <span v-if="s.self_heal" class="badge badge-neutral" :title="$t('gitops.selfHealTitle')">{{ $t('gitops.selfHeal') }}</span>
              </td>
              <td class="text-right table-actions" @click.stop>
                <button class="btn-icon btn-icon-muted" :title="$t('gitops.openTopology')" :aria-label="$t('gitops.openTopology')" @click="openDetail(s)"><span class="mdi mdi-graph-outline"></span></button>
                <button class="btn-icon btn-icon-muted" :title="$t('gitops.viewDiff')" :aria-label="$t('gitops.viewDiff')" @click="openDiff(s)"><span class="mdi mdi-file-compare"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('gitops.syncNow')" :aria-label="$t('gitops.syncNow')" :disabled="syncing === s.id" @click="sync(s)">
                  <span class="mdi" :class="syncing === s.id ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = s"><span class="mdi mdi-delete-outline"></span></button>
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
          <span class="cell-sub mono" :title="syncedTitle(s)"><span class="mdi mdi-source-commit"></span> {{ shortSha(s.last_synced_commit) }}</span>
        </div>
        <div class="gc-badges">
          <span class="badge badge-neutral">{{ s.sync_policy }}</span>
          <span v-if="s.prune" class="badge badge-neutral" :title="$t('gitops.pruneTitle')">{{ $t('gitops.prune') }}</span>
          <span v-if="s.self_heal" class="badge badge-neutral" :title="$t('gitops.selfHealTitle')">{{ $t('gitops.selfHeal') }}</span>
        </div>
        <div class="gc-actions table-actions" @click.stop>
          <button class="btn-icon btn-icon-muted" :title="$t('gitops.openTopology')" :aria-label="$t('gitops.openTopology')" @click="openDetail(s)"><span class="mdi mdi-graph-outline"></span></button>
          <button class="btn-icon btn-icon-muted" :title="$t('gitops.viewDiff')" :aria-label="$t('gitops.viewDiff')" @click="openDiff(s)"><span class="mdi mdi-file-compare"></span></button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('gitops.syncNow')" :aria-label="$t('gitops.syncNow')" :disabled="syncing === s.id" @click="sync(s)">
            <span class="mdi" :class="syncing === s.id ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
          </button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
          <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = s"><span class="mdi mdi-delete-outline"></span></button>
        </div>
      </div>
    </div>

    <!-- Create / edit -->
    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit git source' : 'New git source' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" :placeholder="$t('gitops.namePlaceholder')" required autofocus :aria-label="$t('apps.form.name')" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.sourceGit') }}</label>
              <select v-model="form.git_repository_id" class="form-select" required :aria-label="$t('apps.form.sourceGit')">
                <option :value="null" disabled>{{ $t('gitops.selectARepository') }}</option>
                <option v-for="c in credentials" :key="c.id" :value="c.id">{{ c.name }} — {{ c.url }}</option>
              </select>
              <i18n-t v-if="credentials.length === 0" keypath="gitops.noRepos" tag="p" class="hint"><template #link><router-link to="/git-repositories">{{ $t('gitops.addOne') }}</router-link></template></i18n-t>
              <p v-else class="hint">{{ $t('gitops.repoHint') }}
                <router-link to="/git-repositories">{{ $t('apps.form.manageRepos') }}</router-link>
              </p>
            </div>
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">{{ $t('gitops.ref') }}</label>
                <input v-model="form.ref" class="form-input" placeholder="main" :aria-label="$t('gitops.ref')" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('gitops.path') }}</label>
                <input v-model="form.path" class="form-input mono" placeholder="envs/prod" :aria-label="$t('gitops.path')" />
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('gitops.syncPolicy') }}</label>
              <div class="tabs" style="margin-bottom: 0">
                <button type="button" class="tab" :class="{ active: form.sync_policy === 'manual' }" @click="form.sync_policy = 'manual'">{{ $t('gitops.manual') }}</button>
                <button type="button" class="tab" :class="{ active: form.sync_policy === 'auto' }" @click="form.sync_policy = 'auto'">{{ $t('gitops.automatic') }}</button>
              </div>
              <p class="hint">{{ $t('gitops.automaticHint') }}</p>
            </div>
            <label class="check"><input type="checkbox" v-model="form.prune" /> <span>{{ $t('gitops.pruneLabel') }}</span></label>
            <label class="check"><input type="checkbox" v-model="form.self_heal" /> <span>{{ $t('gitops.selfHealLabel') }}</span></label>
            <label class="check" :class="{ disabled: !form.prune }">
              <input type="checkbox" v-model="form.allow_empty" :disabled="!form.prune" />
              <i18n-t keypath="gitops.allowEmpty" tag="span"><template #all><strong>{{ $t('gitops.all') }}</strong></template></i18n-t>
            </label>
            <p v-if="form.allow_empty" class="hint warn">
              <span class="mdi mdi-alert-outline"></span>{{ $t('gitops.allowEmptyWarning') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? $t('action.saving') : (editing ? $t('action.save') : $t('action.create')) }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Diff viewer -->
    <Teleport to="body">
      <AppModal v-if="showDiff" dialog-class="modal-lg" @close="showDiff = false">
        <div class="modal-header">
          <h3>Diff — {{ diffSource?.name }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showDiff = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-if="diffLoading" class="card-body"><span class="spinner"></span></div>
          <div v-else-if="planChanges.length === 0" class="empty-state">
            <span class="mdi mdi-check-circle-outline" style="font-size: 40px; color: var(--success-600)"></span>
            <h3>{{ $t('gitops.inSync') }}</h3>
            <p>{{ $t('gitops.inSyncHint') }}</p>
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
          <button type="button" class="btn btn-secondary" @click="showDiff = false">{{ $t('shell.close') }}</button>
          <button v-if="ws.canEdit && diffSource && planChanges.length" class="btn btn-primary" :disabled="syncing === diffSource.id"
            @click="diffSource && sync(diffSource).then(() => { showDiff = false })">
            <span class="mdi mdi-sync"></span>{{ $t('gitops.syncNow') }}</button>
        </div>
      </AppModal>
    </Teleport>

    <!-- Teardown follow-up: what a cascade delete removed -->
    <Teleport to="body">
      <AppModal v-if="teardown" dialog-class="modal-lg" @close="teardown = null">
        <div class="modal-header">
          <h3>Resources removed — {{ teardown.name }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="teardown = null"><span class="mdi mdi-close"></span></button>
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
            <p>{{ $t('gitops.nothingToRemove') }}</p>
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
          <button type="button" class="btn btn-secondary" @click="teardown = null">{{ $t('shell.close') }}</button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      :title="$t('confirm.title.deleteGitSource')"
      :message="$t('confirm.message.gitOps.deleteGitSourceName', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    >
      <label class="cascade-option">
        <input type="checkbox" v-model="deleteResources" />
        <span>{{ $t('gitops.alsoDeleteResources') }}<small>{{ $t('gitops.deleteWarning') }}</small>
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
