<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { pipelineApi, type PipelineInput } from '@/api/pipelines'
import { appApi } from '@/api/apps'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PipelineWebhookModal from './PipelineWebhookModal.vue'
import { relativeTime } from '@/utils/time'
import { statusMeta } from './status'
import type { PipelineDefinition, Application, PipelineRunEvent } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<PipelineDefinition[]>([])
const apps = ref<Application[]>([])
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

// Apps (deploy-target picker) load once per workspace, independent of the page.
async function loadApps(id: number | null) {
  if (!id) { apps.value = []; return }
  try {
    apps.value = (await appApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
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
  return { name: '', application_id: null, spec: sampleSpec, enabled: true }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showModal.value = true
}
function openEdit(p: PipelineDefinition) {
  editing.value = p
  form.value = { name: p.name, application_id: p.application_id ?? null, spec: p.spec, enabled: p.enabled }
  showModal.value = true
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
      notify.success(editingRepoOwned.value ? (form.value.enabled ? 'Pipeline enabled' : 'Pipeline disabled') : 'Pipeline updated')
    } else {
      await pipelineApi.create(currentWorkspaceId.value, form.value)
      notify.success('Pipeline created')
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
    notify.success(`${p.name}: run #${run.number} queued`, { detail: 'Open the run to follow its logs.' })
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
    notify.success('Pipeline deleted')
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
        <h1>Pipelines</h1>
        <p class="subtitle">Build, test, and deploy on the internal runner with <code>kind: Pipeline</code>.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New pipeline
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-pipe" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No pipelines yet</h3>
        <p>Define a pipeline-as-code to turn a commit into an image and a release.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Create a pipeline</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Pipeline</th><th>Target</th><th>Last run</th><th>State</th><th></th></tr></thead>
          <tbody>
            <tr v-for="p in items" :key="p.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-pipe" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title link" @click="openRuns(p)">
                      {{ p.name }}
                      <span v-if="isRepoOwned(p)" class="repo-chip" :title="`Spec read from ${p.source_path} on ${p.source_ref || 'the default branch'}`">
                        <span class="mdi mdi-source-branch"></span> from repo
                      </span>
                    </span>
                    <span class="cell-sub" :title="new Date(p.created_at).toLocaleString()">created {{ relativeTime(p.created_at) }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span v-if="appName(p.application_id)" class="target-chip">
                  <span class="mdi mdi-application-outline"></span> {{ appName(p.application_id) }}
                </span>
                <span v-else class="cell-sub">—</span>
              </td>
              <td>
                <button v-if="p.last_run" class="last-run" :title="`Run #${p.last_run.number} · open`" @click="openLastRun(p)">
                  <span class="badge" :class="statusMeta(p.last_run.status).badge">
                    <span class="mdi" :class="statusMeta(p.last_run.status).icon"></span> {{ statusMeta(p.last_run.status).label }}
                  </span>
                  <span class="last-run-time">{{ relativeTime(p.last_run.started_at || p.last_run.created_at) }}</span>
                </button>
                <span v-else class="cell-sub">Never run</span>
              </td>
              <td>
                <span class="badge" :class="p.enabled ? 'badge-success' : 'badge-neutral'">{{ p.enabled ? 'enabled' : 'disabled' }}</span>
              </td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" title="View runs" aria-label="View runs" @click="openRuns(p)"><span class="mdi mdi-history"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Run now" aria-label="Run now" :disabled="triggering === p.id || !p.enabled" @click="trigger(p)">
                  <span class="mdi" :class="triggering === p.id ? 'mdi-loading mdi-spin' : 'mdi-play'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Push webhook" aria-label="Push webhook" @click="webhookFor = p">
                  <span class="mdi mdi-webhook"></span>
                </button>
                <button
                  v-if="ws.canEdit"
                  class="btn-icon btn-icon-muted"
                  :title="isRepoOwned(p) ? 'View (managed by repository)' : 'Edit'"
                  :aria-label="isRepoOwned(p) ? 'View' : 'Edit'"
                  @click="openEdit(p)"
                >
                  <span class="mdi" :class="isRepoOwned(p) ? 'mdi-eye-outline' : 'mdi-pencil-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = p"><span class="mdi mdi-delete-outline"></span></button>
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
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div v-if="editingRepoOwned" class="repo-notice">
              <span class="mdi mdi-source-branch"></span>
              <div>
                <strong>Managed by its repository.</strong>
                Miabi re-reads <code>{{ editing?.source_path }}</code> on
                <code>{{ editing?.source_ref || 'the default branch' }}</code> before every run, so this pipeline
                can't be edited here — change the file in git and push. You can still disable it, which makes
                <template v-if="appName(editing?.application_id)">{{ appName(editing?.application_id) }}</template>
                <template v-else>its application</template>
                build and deploy directly again.
              </div>
            </div>
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="e.g. web" required autofocus aria-label="Name" :disabled="editingRepoOwned" />
              </div>
              <div class="form-group">
                <label class="form-label">Deploy target <span class="text-muted">(for the deploy step)</span></label>
                <select v-model="form.application_id" class="form-select" aria-label="Deploy target" :disabled="editingRepoOwned">
                  <option :value="null">None</option>
                  <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
                </select>
              </div>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">
                Pipeline spec <span class="text-muted">(kind: Pipeline)</span>
                <span v-if="editingRepoOwned" class="repo-chip">
                  <span class="mdi mdi-source-branch"></span> managed by repository
                </span>
              </label>
              <textarea
                v-model="form.spec"
                class="form-textarea code"
                rows="16"
                spellcheck="false"
                required
                aria-label="Pipeline spec"
                :readonly="editingRepoOwned"
              ></textarea>
              <p v-if="editingRepoOwned" class="form-hint">
                Read-only.
                <template v-if="editing?.source_commit">Synced from commit {{ shortCommit(editing.source_commit) }}.</template>
              </p>
              <p v-else class="form-hint">
                <code>env</code> applies to every step; a step&rsquo;s own <code>env</code> wins. Values may reference a
                <router-link :to="{ name: 'secrets' }">workspace secret</router-link>
                as <code>{{ secretRefExample }}</code> &mdash; resolved when the run starts and masked in the logs.
              </p>
            </div>
            <label class="check"><input type="checkbox" v-model="form.enabled" /> <span>Enabled</span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">
              {{ editingRepoOwned ? 'Close' : 'Cancel' }}
            </button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? 'Saving…' : editingRepoOwned ? 'Save enabled state' : editing ? 'Save' : 'Create' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <PipelineWebhookModal :open="!!webhookFor" :pipeline="webhookFor" @close="webhookFor = null" />

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete pipeline"
      :message="isRepoOwned(toDelete)
        ? `Delete pipeline &quot;${toDelete?.name}&quot;? Its run history is removed, and its application goes back to building and deploying directly — skipping the steps in ${toDelete?.source_path}.`
        : `Delete pipeline &quot;${toDelete?.name}&quot;? Its run history is removed.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
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
