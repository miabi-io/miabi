<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { pipelineApi, type PipelineInput } from '@/api/pipelines'
import { appApi } from '@/api/apps'
import { gitRepositoryApi } from '@/api/gitRepositories'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PipelineWebhookModal from './PipelineWebhookModal.vue'
import { relativeTime } from '@/utils/time'
import { fmtDateTime } from '@/utils/datetime'
import { statusMeta } from './status'
import type { PipelineDefinition, Application, GitRepository, PipelineRunEvent } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<PipelineDefinition[]>([])
const apps = ref<Application[]>([])
const repos = ref<GitRepository[]>([])
// Which kind of source the form is binding to. The two are mutually exclusive —
// each supplies the checkout, so together there is no answer to what a run clones.
type SourceKind = 'none' | 'application' | 'repository'
const sourceKind = ref<SourceKind>('none')
const loading = ref(false)
const triggering = ref<number | null>(null)

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    const res = await pipelineApi.list(id, page, pageable.value.size)
    items.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})
// Reload the current page after a mutation.
function reload() { goToPage(pageable.value.current_page) }

// The source pickers load once per workspace, independent of the page.
async function loadApps(id: number | null) {
  if (!id) { apps.value = []; repos.value = []; return }
  try {
    apps.value = (await appApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  }
  try {
    repos.value = (await gitRepositoryApi.list(id)).data.data ?? []
  } catch {
    // A workspace with no repositories, or no permission to list them, simply
    // offers no repository option — it must not break the app picker.
    repos.value = []
  }
}
watch(currentWorkspaceId, (id) => { loadApps(id); goToPage(0) }, { immediate: true })

// Live run transitions keep each row's last-run badge current.
let es: EventSource | null = null
function openStream() {
  es?.close()
  const wid = currentWorkspaceId.value
  if (!wid) return
  es = new EventSource(pipelineApi.runsStreamUrl(wid))
  es.onmessage = (e) => {
    let ev: { type?: string; data?: PipelineRunEvent }
    try { ev = JSON.parse(e.data) } catch { return }
    if (ev.type !== 'run' || !ev.data) return
    const d = ev.data
    const row = items.value.find((p) => p.id === d.pipeline_id)
    if (!row) return
    if (!row.last_run || row.last_run.id === d.run_id || d.number >= row.last_run.number) {
      row.last_run = {
        ...(row.last_run ?? { created_at: new Date().toISOString() }),
        id: d.run_id, number: d.number, status: d.status,
        started_at: d.started_at, finished_at: d.finished_at,
      } as PipelineDefinition['last_run']
    }
  }
}
openStream()
watch(currentWorkspaceId, openStream)
onBeforeUnmount(() => { es?.close(); es = null })

const showModal = ref(false)
const saving = ref(false)
const editing = ref<PipelineDefinition | null>(null)
const form = ref<PipelineInput>(emptyForm())

const sampleSpec = `apiVersion: miabi.io/v1
kind: Pipeline
metadata: { name: web }
on:
  push: { branches: [main] }
  manual: true
env:                                # applies to every step
  NODE_ENV: production
  NPM_TOKEN: \${{ secrets.NPM_TOKEN }}   # from the workspace vault
steps:
  - name: test
    image: node:20
    env:                            # step env wins over the pipeline's
      CI: "true"
    run: "npm ci && npm test"
  - name: build
    uses: build
    dockerfile: Dockerfile
  - name: scan
    image: aquasec/trivy:latest
    continue-on-error: true
    run: "TRIVY_USERNAME=$MIABI_REGISTRY_USER TRIVY_PASSWORD=$MIABI_REGISTRY_TOKEN trivy image --exit-code 1 --severity HIGH,CRITICAL $MIABI_IMAGE"
  - name: deploy
    uses: deploy
`

// Written as a constant: `${{ … }}` in a template is a Vue interpolation.
const secretRefExample = '${{ secrets.NAME }}'

function emptyForm(): PipelineInput {
  return { name: '', application_id: null, git_repository: null, branch: '', spec: sampleSpec, enabled: true }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  sourceKind.value = 'none'
  showModal.value = true
}
function openEdit(p: PipelineDefinition) {
  editing.value = p
  form.value = {
    name: p.name,
    application_id: p.application_id ?? null,
    // By name, not id: it is what the API takes, and what round-trips unchanged.
    git_repository: p.git_repository ?? null,
    branch: p.branch ?? '',
    spec: p.spec,
    enabled: p.enabled,
  }
  sourceKind.value = p.application_id ? 'application' : p.git_repository ? 'repository' : 'none'
  showModal.value = true
}

// Switching kind clears the other binding, so the payload can never carry both —
// which the API refuses, and which has no meaning anyway.
watch(sourceKind, (kind) => {
  if (kind !== 'application') form.value.application_id = null
  if (kind !== 'repository') form.value.git_repository = null
})

// Mirrors the worker's naming (ws_<id>/pl_<name>) so the form says where the
// image lands before the first run does.
const pipelineImageHint = computed(() => `ws_${currentWorkspaceId.value ?? 0}/pl_${form.value.name || 'name'}`)

// The definition carries the repository's name; show its friendlier display name
// when the workspace's repositories are loaded, and the name itself otherwise.
function repoLabel(name?: string | null) {
  if (!name) return null
  return repos.value.find((x) => x.name === name)?.display_name || name
}

// The push webhook is setup information, not configuration — it gets its own
// dialog rather than a corner of the edit form, which a repo-owned pipeline
// can't open anyway.
const webhookFor = ref<PipelineDefinition | null>(null)

/** A repo-owned pipeline mirrors a file in git; its spec is read-only here. */
function isRepoOwned(p: PipelineDefinition | null) { return p?.source === 'repo' }
const editingRepoOwned = computed(() => isRepoOwned(editing.value))
function shortCommit(sha?: string) { return sha ? sha.slice(0, 7) : '' }

async function save() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    if (editing.value) {
      // A repo-owned pipeline only accepts its enabled flag; everything else is
      // derived from the repository and the app, and the backend rejects a change
      // to it. Send just that, so the one editable control still works.
      const payload: PipelineInput = editingRepoOwned.value
        ? { name: editing.value.name, spec: '', enabled: form.value.enabled }
        : form.value
      await pipelineApi.update(currentWorkspaceId.value, editing.value.id, payload)
      notify.success(t(editingRepoOwned.value ? (form.value.enabled ? 'notify.pipelines.enabled' : 'notify.pipelines.disabled') : 'notify.pipelines.updated'))
    } else {
      await pipelineApi.create(currentWorkspaceId.value, form.value)
      notify.success(t('notify.pipelines.created'))
    }
    showModal.value = false
    reload()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function trigger(p: PipelineDefinition) {
  if (!currentWorkspaceId.value) return
  triggering.value = p.id
  try {
    const run = (await pipelineApi.trigger(currentWorkspaceId.value, p.id)).data.data
    // Show it immediately and stay put; the stream takes over from here.
    p.last_run = { ...run } as PipelineDefinition['last_run']
    notify.success(t('notify.pipelines.runQueued', { name: p.name, number: run.number }), { detail: t('notify.pipelines.runQueuedDetail') })
  } catch (e) {
    notify.apiError(e, 'Could not trigger run')
  } finally {
    triggering.value = null
  }
}

const toDelete = ref<PipelineDefinition | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !toDelete.value) return
  deleting.value = true
  try {
    await pipelineApi.remove(currentWorkspaceId.value, toDelete.value.id)
    notify.success(t('notify.pipelines.deleted'))
    toDelete.value = null
    reload()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

function appName(id?: number | null) {
  if (!id) return null
  return apps.value.find((a) => a.id === id)?.name ?? `app #${id}`
}
function openRuns(p: PipelineDefinition) {
  router.push({ name: 'pipeline-runs', params: { id: p.id } })
}
function openLastRun(p: PipelineDefinition) {
  if (!p.last_run) return
  router.push({ name: 'pipeline-run', params: { id: p.id, runId: p.last_run.id } })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('pipelines.pipelines') }}</h1>
        <i18n-t keypath="pipelines.subtitle" tag="p" class="subtitle"><template #kind><code>kind: Pipeline</code></template></i18n-t>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('pipelines.newPipeline') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-pipe" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('pipelines.noPipelinesYet') }}</h3>
        <p>{{ $t('pipelines.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('pipelines.createAPipeline') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('pipelines.pipeline') }}</th><th>{{ $t('pipelines.source') }}</th><th>{{ $t('jobs.lastRun') }}</th><th>{{ $t('pipelines.state') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="p in items" :key="p.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-pipe" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title link" @click="openRuns(p)">
                      {{ p.name }}
                      <span v-if="isRepoOwned(p)" class="repo-chip" :title="$t('pipelines.specReadFrom', { path: p.source_path, ref: p.source_ref || $t('pipelines.defaultBranch') })">
                        <span class="mdi mdi-source-branch"></span>{{ $t('pipelines.fromRepo') }}</span>
                    </span>
                    <span class="cell-sub" :title="fmtDateTime(p.created_at)">{{ $t('pipelines.createdAgo', { time: relativeTime(p.created_at) }) }}</span>
                  </span>
                </div>
              </td>
              <td>
                <!-- Which source the pipeline clones: an application, a
                     repository, or nothing (commands only). -->
                <span v-if="appName(p.application_id)" class="target-chip" :title="$t('pipelines.appSourceNote')">
                  <span class="mdi mdi-application-outline"></span> {{ appName(p.application_id) }}
                </span>
                <span
                  v-else-if="p.git_repository"
                  class="target-chip"
                  :title="`Clones this repository${p.branch ? ' (' + p.branch + ')' : ''} and pushes an image — no deploy target`"
                >
                  <span class="mdi mdi-git"></span> {{ repoLabel(p.git_repository) }}
                  <span v-if="p.branch" class="branch-chip">{{ p.branch }}</span>
                </span>
                <span v-else class="cell-sub" :title="$t('pipelines.noSourceNote')">—</span>
              </td>
              <td>
                <button v-if="p.last_run" class="last-run" :title="`Run #${p.last_run.number} · open`" @click="openLastRun(p)">
                  <span class="badge" :class="statusMeta(p.last_run.status).badge">
                    <span class="mdi" :class="statusMeta(p.last_run.status).icon"></span> {{ statusMeta(p.last_run.status).label }}
                  </span>
                  <span class="last-run-time">{{ relativeTime(p.last_run.started_at || p.last_run.created_at) }}</span>
                </button>
                <span v-else class="cell-sub">{{ $t('pipelines.neverRun') }}</span>
              </td>
              <td>
                <span class="badge" :class="p.enabled ? 'badge-success' : 'badge-neutral'">{{ p.enabled ? 'enabled' : 'disabled' }}</span>
              </td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" :title="$t('pipelines.viewRuns')" :aria-label="$t('pipelines.viewRuns')" @click="openRuns(p)"><span class="mdi mdi-history"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('jobs.runNow')" :aria-label="$t('jobs.runNow')" :disabled="triggering === p.id || !p.enabled" @click="trigger(p)">
                  <span class="mdi" :class="triggering === p.id ? 'mdi-loading mdi-spin' : 'mdi-play'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('pipelines.pushWebhook')" :aria-label="$t('pipelines.pushWebhook')" @click="webhookFor = p">
                  <span class="mdi mdi-webhook"></span>
                </button>
                <button
                  v-if="ws.canEdit"
                  class="btn-icon btn-icon-muted"
                  :title="isRepoOwned(p) ? $t('pipelines.viewManaged') : $t('action.edit')"
                  :aria-label="isRepoOwned(p) ? $t('pipelines.view') : $t('action.edit')"
                  @click="openEdit(p)"
                >
                  <span class="mdi" :class="isRepoOwned(p) ? 'mdi-eye-outline' : 'mdi-pencil-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = p"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <Teleport to="body">
      <AppModal v-if="showModal" dialog-class="modal-lg" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? (editingRepoOwned ? 'Pipeline' : 'Edit pipeline') : 'New pipeline' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div v-if="editingRepoOwned" class="repo-notice">
              <span class="mdi mdi-source-branch"></span>
              <div>
                <strong>{{ $t('pipelines.managedByItsRepository') }}</strong>
                <i18n-t keypath="pipelines.repoNotice" tag="span">
                  <template #path><code>{{ editing?.source_path }}</code></template>
                  <template #ref><code>{{ editing?.source_ref || $t('pipelines.defaultBranch') }}</code></template>
                  <template #app>{{ appName(editing?.application_id) || $t('pipelines.itsApplication') }}</template>
                </i18n-t>
              </div>
            </div>
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.name') }}</label>
                <input v-model="form.name" class="form-input" :placeholder="$t('pipelines.namePlaceholder')" required autofocus :aria-label="$t('apps.form.name')" :disabled="editingRepoOwned" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('pipelines.source') }}</label>
                <select v-model="sourceKind" class="form-select" :aria-label="$t('pipelines.source')" :disabled="editingRepoOwned">
                  <option value="none">{{ $t('pipelines.noneCommandsOnly') }}</option>
                  <option value="application">{{ $t('pipelines.application') }}</option>
                  <option value="repository">{{ $t('pipelines.repository') }}</option>
                </select>
              </div>
            </div>

            <!-- One binding or the other. An application supplies the checkout,
                 the image repository and the deploy target; a repository supplies
                 the checkout and the image only. -->
            <div v-if="sourceKind === 'application'" class="form-group">
              <label class="form-label">{{ $t('pipelines.application') }} <span class="text-muted">{{ $t('pipelines.appHint') }}</span></label>
              <select v-model="form.application_id" class="form-select" :aria-label="$t('pipelines.application')" :disabled="editingRepoOwned">
                <option :value="null">{{ $t('pipelines.selectAnApplication') }}</option>
                <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.display_name || a.name }}</option>
              </select>
            </div>

            <template v-else-if="sourceKind === 'repository'">
              <div class="form-row">
                <div class="form-group">
                  <label class="form-label">{{ $t('pipelines.repository') }}</label>
                  <!-- Bound by name: the name is immutable and unique per
                       workspace, and it is the form a manifest or a CLI would
                       use — an id means nothing on another install. -->
                  <select v-model="form.git_repository" class="form-select" :aria-label="$t('pipelines.repository')" :disabled="editingRepoOwned">
                    <option :value="null">{{ $t('gitops.selectARepository') }}</option>
                    <option v-for="r in repos" :key="r.id" :value="r.name">{{ r.display_name || r.name }}</option>
                  </select>
                  <i18n-t v-if="!repos.length" keypath="pipelines.noRepos" tag="p" class="form-hint"><template #link><RouterLink to="/git-repositories">{{ $t('pipelines.gitRepositories') }}</RouterLink></template></i18n-t>
                </div>
                <div class="form-group">
                  <label class="form-label">{{ $t('pipelines.branch') }} <span class="text-muted">{{ $t('pipelines.optional') }}</span></label>
                  <input v-model="form.branch" class="form-input" placeholder="main" :aria-label="$t('pipelines.branch')" :disabled="editingRepoOwned" />
                  <p class="form-hint">{{ $t('pipelines.branchHint') }}</p>
                </div>
              </div>
              <i18n-t keypath="pipelines.repoSourceNote" tag="p" class="form-hint source-note">
                <template #image><code>{{ pipelineImageHint }}</code></template>
                <template #step><code>uses: deploy</code></template>
              </i18n-t>
            </template>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('pipelines.pipelineSpec') }} <span class="text-muted">(kind: Pipeline)</span>
                <span v-if="editingRepoOwned" class="repo-chip">
                  <span class="mdi mdi-source-branch"></span>{{ $t('pipelines.managedByRepository') }}</span>
              </label>
              <textarea
                v-model="form.spec"
                class="form-textarea code"
                rows="16"
                spellcheck="false"
                required
                :aria-label="$t('pipelines.pipelineSpec')"
                :readonly="editingRepoOwned"
              ></textarea>
              <p v-if="editingRepoOwned" class="form-hint">
                {{ $t('pipelines.readOnly') }}
                <template v-if="editing?.source_commit">{{ $t('pipelines.syncedFromCommit', { commit: shortCommit(editing.source_commit) }) }}</template>
              </p>
              <p v-else class="form-hint">
                <i18n-t keypath="pipelines.envHint" tag="span">
                  <template #env><code>env</code></template>
                  <template #stepEnv><code>env</code></template>
                  <template #secret><router-link :to="{ name: 'secrets' }">{{ $t('pipelines.workspaceSecret') }}</router-link></template>
                  <template #ref><code>{{ secretRefExample }}</code></template>
                </i18n-t>
              </p>
            </div>
            <label class="check"><input type="checkbox" v-model="form.enabled" /> <span>{{ $t('jobs.enabled') }}</span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">
              {{ editingRepoOwned ? $t('shell.close') : $t('action.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? $t('action.saving') : editingRepoOwned ? $t('pipelines.saveEnabledState') : editing ? $t('action.save') : $t('action.create') }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <PipelineWebhookModal :open="!!webhookFor" :pipeline="webhookFor" @close="webhookFor = null" />

    <ConfirmDialog
      :open="!!toDelete"
      :title="$t('confirm.title.deletePipeline')"
      :message="isRepoOwned(toDelete)
        ? $t('confirm.message.pipelines.deleteRepoOwned', { name: toDelete?.name, path: toDelete?.source_path })
        : $t('confirm.message.pipelines.delete', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.branch-chip {
  margin-left: 4px;
  padding: 0 4px;
  border-radius: 4px;
  background: var(--bg-tertiary);
  font-family: monospace;
  font-size: 11px;
}
.source-note { margin-top: 4px; }

.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.subtitle code { font-family: 'JetBrains Mono', monospace; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.check { display: flex; align-items: center; gap: 8px; font-size: 13px; margin-top: 10px; cursor: pointer; }
.link { cursor: pointer; }
.link:hover { color: var(--primary-500); }
.code { font-family: 'JetBrains Mono', monospace; font-size: 12px; line-height: 1.5; }
.form-textarea[readonly] { background: var(--bg-tertiary, rgba(127, 127, 127, 0.08)); cursor: default; }
.repo-chip {
  display: inline-flex; align-items: center; gap: 4px; font-size: 11px; font-weight: 500;
  padding: 1px 7px; border-radius: 20px; margin-left: 6px; vertical-align: middle;
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.12)); color: var(--text-secondary, var(--text-muted));
}
.repo-chip .mdi { font-size: 12px; }
.repo-notice {
  display: flex; gap: 10px; align-items: flex-start; margin-bottom: 16px; padding: 12px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--bg-tertiary, rgba(127, 127, 127, 0.08));
  font-size: 13px; line-height: 1.5; color: var(--text-secondary, var(--text-muted));
}
.repo-notice .mdi { font-size: 18px; flex-shrink: 0; }
.repo-notice code { background: var(--bg-secondary); padding: 1px 6px; border-radius: 4px; font-size: 12px; }
.form-input:disabled, .form-select:disabled { opacity: 0.65; cursor: not-allowed; }
.target-chip {
  display: inline-flex; align-items: center; gap: 5px; font-size: 12px; padding: 2px 9px;
  border-radius: 20px; background: var(--bg-tertiary, rgba(127, 127, 127, 0.12));
  color: var(--text-secondary, var(--text-muted));
}
.target-chip .mdi { font-size: 13px; }
.last-run {
  display: inline-flex; align-items: center; gap: 8px; background: none; border: none;
  padding: 0; cursor: pointer; font: inherit; color: inherit;
}
.last-run .badge .mdi { font-size: 13px; }
.last-run-time { font-size: 12px; color: var(--text-muted); }
.last-run:hover .last-run-time { color: var(--primary-500); }
</style>
