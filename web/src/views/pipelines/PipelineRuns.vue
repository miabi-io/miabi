<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { pipelineApi } from '@/api/pipelines'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import { relativeTime, formatDuration } from '@/utils/time'
import { statusMeta } from './status'
import PipelineWebhookModal from './PipelineWebhookModal.vue'
import type { PipelineDefinition, PipelineRun, PipelineRunEvent } from '@/api/types'

const ws = useWorkspaceStore()
const isTerminal = (s: string) => ['succeeded', 'failed', 'canceled'].includes(s)

const notify = useNotificationStore()
const route = useRoute()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const pipelineId = computed(() => Number(route.params.id))
const pipeline = ref<PipelineDefinition | null>(null)
const runs = ref<PipelineRun[]>([])
const loading = ref(false)
const triggering = ref(false)
const showWebhook = ref(false)
const now = ref(Date.now())

const { pageable, goToPage } = usePagination(async (page) => {
  const wid = currentWorkspaceId.value
  if (!wid || !pipelineId.value) { runs.value = []; return }
  loading.value = true
  try {
    const res = await pipelineApi.runs(wid, pipelineId.value, page, pageable.value.size)
    runs.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

// The pipeline header loads once; the runs list is paged independently.
async function loadPipeline() {
  const wid = currentWorkspaceId.value
  if (!wid || !pipelineId.value) { pipeline.value = null; return }
  try {
    pipeline.value = (await pipelineApi.get(wid, pipelineId.value)).data.data
  } catch (e) {
    notify.apiError(e)
  }
}
loadPipeline()
watch([currentWorkspaceId, pipelineId], () => { loadPipeline(); goToPage(0) })

// noCache is the one-off "rebuild everything" run: it overrides the spec's cache
// setting for this run alone, so nothing in the repository has to change.
async function trigger(noCache = false) {
  const wid = currentWorkspaceId.value
  if (!wid || !pipeline.value) return
  triggering.value = true
  try {
    const run = (await pipelineApi.trigger(wid, pipeline.value.id, { no_cache: noCache })).data.data
    // Prepend and stay on the list; the stream drives it from pending onwards.
    if (!runs.value.some((r) => r.id === run.id)) runs.value.unshift(run)
    notify.success(`Run #${run.number} queued`, {
      detail: noCache ? 'Building without cache — expect a slower run.' : 'Open it to follow the logs.',
    })
  } catch (e) {
    notify.apiError(e, 'Could not trigger run')
  } finally {
    triggering.value = false
  }
}

function openRun(r: PipelineRun) {
  router.push({ name: 'pipeline-run', params: { id: pipelineId.value, runId: r.id } })
}

// Tick so in-progress run durations count up live.
const ticker = setInterval(() => { now.value = Date.now() }, 1000)

// Live run transitions: patch the row in place, refetch only when a run appears
// that this page has never seen.
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
    if (pipelineId.value && d.pipeline_id !== pipelineId.value) return
    const row = runs.value.find((r) => r.id === d.run_id)
    if (!row) { goToPage(pageable.value.current_page); return }
    row.status = d.status
    row.started_at = d.started_at ?? row.started_at
    row.finished_at = d.finished_at ?? row.finished_at
  }
}
openStream()
watch(currentWorkspaceId, openStream)

onBeforeUnmount(() => {
  clearInterval(ticker)
  es?.close()
  es = null
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link to="/pipelines" class="back-link"><span class="mdi mdi-arrow-left"></span> Pipelines</router-link>
        <div class="title-row">
          <h1>{{ pipeline?.display_name || pipeline?.name || 'Pipeline' }}</h1>
          <span v-if="pipeline?.last_run" class="badge" :class="statusMeta(pipeline.last_run.status).badge">
            <span class="mdi" :class="statusMeta(pipeline.last_run.status).icon"></span> {{ statusMeta(pipeline.last_run.status).label }}
          </span>
        </div>
        <p class="subtitle">
          Run history
          <span v-if="pageable.total_elements"> · {{ pageable.total_elements }} run{{ pageable.total_elements === 1 ? '' : 's' }}</span>
          <span v-if="pipeline?.last_run" :title="pipeline.last_run.started_at ? new Date(pipeline.last_run.started_at).toLocaleString() : ''">
            · last run #{{ pipeline.last_run.number }} {{ relativeTime(pipeline.last_run.started_at || pipeline.last_run.created_at, now) }}
          </span>
        </p>
      </div>
      <div class="header-actions">
        <button v-if="ws.canEdit" class="btn btn-secondary" @click="showWebhook = true">
          <span class="mdi mdi-webhook"></span> Push webhook
        </button>
        <button
          v-if="ws.canEdit && pipeline?.enabled"
          class="btn btn-secondary"
          :disabled="triggering"
          title="Rebuild every layer, ignoring the build cache. Slower — use it when a cached layer has gone stale."
          @click="trigger(true)"
        >
          <span class="mdi mdi-cached"></span> Run without cache
        </button>
        <button v-if="ws.canEdit && pipeline?.enabled" class="btn btn-primary" :disabled="triggering" @click="trigger()">
          <span class="mdi" :class="triggering ? 'mdi-loading mdi-spin' : 'mdi-play'"></span> Run now
        </button>
      </div>
    </div>

    <PipelineWebhookModal :open="showWebhook" :pipeline="pipeline" @close="showWebhook = false" />

    <div class="card">
      <div v-if="loading && runs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="runs.length === 0" class="empty-state">
        <span class="mdi mdi-history" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No runs yet</h3>
        <p>Trigger this pipeline to see its run history here.</p>
        <button v-if="ws.canEdit && pipeline?.enabled" class="btn btn-primary mt-4" :disabled="triggering" @click="trigger()">Run now</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Run</th><th>Status</th><th>Commit</th><th>Trigger</th><th>Duration</th><th>Started</th></tr></thead>
          <tbody>
            <tr v-for="r in runs" :key="r.id" class="row-link" :class="`rail-${statusMeta(r.status).badge}`" @click="openRun(r)">
              <td class="cell-title">
                #{{ r.number }}
                <span v-if="r.no_cache" class="badge badge-neutral no-cache" title="Built without cache">no cache</span>
              </td>
              <td>
                <span class="badge" :class="statusMeta(r.status).badge">
                  <span class="mdi" :class="statusMeta(r.status).icon"></span> {{ statusMeta(r.status).label }}
                </span>
              </td>
              <td>
                <div v-if="r.commit" class="commit-cell">
                  <span class="mono commit-sha">{{ r.commit.slice(0, 7) }}</span>
                  <span v-if="r.commit_message" class="commit-msg">{{ r.commit_message }}</span>
                </div>
                <span v-else class="cell-sub">—</span>
              </td>
              <td class="cell-sub">{{ r.trigger }}</td>
              <td class="cell-sub">{{ formatDuration(r.started_at, r.finished_at, now, r.created_at, isTerminal(r.status)) }}</td>
              <td class="cell-sub" :title="r.started_at ? new Date(r.started_at).toLocaleString() : ''">
                {{ relativeTime(r.started_at, now) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />
  </div>
</template>

<style scoped>
.title-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.header-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.title-row .badge .mdi { font-size: 13px; }
.no-cache { margin-left: 6px; font-size: 11px; font-weight: 500; }
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.back-link { font-size: 13px; color: var(--text-muted); text-decoration: none; display: inline-flex; align-items: center; gap: 4px; }
.back-link:hover { color: var(--primary-500); }
.mono { font-family: 'JetBrains Mono', monospace; }
.row-link { cursor: pointer; }
.badge .mdi { font-size: 13px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

/* Colored status rail down the left edge of each run row. */
.row-link td:first-child { position: relative; }
.row-link td:first-child::before {
  content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 3px; background: var(--border-primary);
}
.rail-badge-success td:first-child::before { background: var(--success-600, #16a34a); }
.rail-badge-danger td:first-child::before { background: var(--danger-600, #dc2626); }
.rail-badge-info td:first-child::before { background: var(--info-600, var(--primary-500, #6366f1)); }

.commit-cell { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.commit-sha { font-size: 12px; color: var(--text-secondary, var(--text-muted)); flex-shrink: 0; }
.commit-msg {
  font-size: 13px; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; max-width: 320px;
}
</style>
