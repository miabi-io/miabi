<script setup lang="ts">
import { ref, watch, computed, nextTick, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { pipelineApi } from '@/api/pipelines'
import { imageApi } from '@/api/images'
import { copyText } from '@/utils/clipboard'
import { relativeTime, formatDuration } from '@/utils/time'
import { statusMeta } from './status'
import type { Image, PipelineRun, PipelineRunStatus, PipelineStepLogHistory } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const route = useRoute()
const { currentWorkspaceId } = storeToRefs(ws)

const pipelineId = computed(() => Number(route.params.id))
const runId = computed(() => Number(route.params.runId))
const run = ref<PipelineRun | null>(null)
const image = ref<Image | null>(null)
const loading = ref(false)

// Logs: `liveLines` is the SSE stream (running run); `historySteps` is the
// stored per-step output of a finished run, which unlocks per-step filtering.
const liveLines = ref<string[]>([])
const historySteps = ref<PipelineStepLogHistory[]>([])
const mode = ref<'live' | 'history'>('live')
const streaming = ref(false)
const selectedOrdinal = ref<number | null>(null) // null = all steps
const wrap = ref(true)
const autoScroll = ref(true)
const atBottom = ref(true)
const copied = ref(false)
const now = ref(Date.now())
const logBox = ref<HTMLElement | null>(null)
let es: EventSource | null = null
let ticker: ReturnType<typeof setInterval> | undefined

const steps = computed(() => run.value?.steps ?? [])

// The lines actually rendered — a single step when one is selected (history
// mode), otherwise the whole run (with per-step separators when there's more
// than one step).
const displayLines = computed<string[]>(() => {
  if (mode.value === 'history') {
    const all = historySteps.value
    if (selectedOrdinal.value != null) {
      return all.find((s) => s.ordinal === selectedOrdinal.value)?.lines ?? []
    }
    const out: string[] = []
    for (const s of all) {
      if (all.length > 1) out.push(`── ${s.name} (${s.status}) ──`)
      out.push(...(s.lines ?? []))
    }
    return out
  }
  return liveLines.value
})

// Per-step filtering only makes sense once we have stored per-step logs.
const canFilterSteps = computed(() => mode.value === 'history' && historySteps.value.length > 1)

const runDuration = computed(() => formatDuration(run.value?.started_at, run.value?.finished_at, now.value))
function stepDuration(s: { started_at?: string | null; finished_at?: string | null }) {
  return formatDuration(s.started_at, s.finished_at, now.value)
}

async function loadRun() {
  const wid = currentWorkspaceId.value
  if (!wid || !runId.value) return
  try {
    run.value = (await pipelineApi.run(wid, runId.value)).data.data
    await loadImage()
  } catch (e) {
    notify.apiError(e)
  }
}

// loadImage resolves the artifact a run built (run.image_id).
async function loadImage() {
  const wid = currentWorkspaceId.value
  const id = run.value?.image_id
  if (!wid || !id) { image.value = null; return }
  if (image.value?.id === id) return
  try {
    image.value = (await imageApi.get(wid, id)).data.data
  } catch { image.value = null }
}

function fmtBytes(n: number): string {
  if (!n || n <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}

function scrollToBottom() {
  nextTick(() => { if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight })
}

function openStream() {
  const wid = currentWorkspaceId.value
  if (!wid || !runId.value) return
  es?.close()
  mode.value = 'live'
  streaming.value = true
  es = new EventSource(pipelineApi.logsUrl(wid, runId.value))
  es.onmessage = (ev) => {
    try {
      const e = JSON.parse(ev.data) as { type: string; data: unknown }
      if (e.type === 'log') {
        liveLines.value.push(String(e.data))
        if (autoScroll.value) scrollToBottom()
      } else if (e.type === 'step') {
        // "<ordinal>:<status>" — update the stepper live without a refresh.
        const [ord, status] = String(e.data).split(':')
        const s = run.value?.steps?.find((x) => x.ordinal === Number(ord))
        if (s && status) s.status = status as PipelineRunStatus
      } else if (e.type === 'status') {
        loadRun()
        if (e.data === 'succeeded' || e.data === 'failed' || e.data === 'canceled') {
          es?.close()
          streaming.value = false
          // Swap to the stored per-step logs so step filtering works even for a
          // run we watched live from start to finish.
          loadHistory()
        }
      }
    } catch { /* ignore malformed frame */ }
  }
  es.onerror = () => { es?.close(); streaming.value = false }
}

// loadHistory fetches a finished run's full per-step logs (load-once, no SSE).
async function loadHistory() {
  const wid = currentWorkspaceId.value
  if (!wid || !runId.value) return
  try {
    const hist = (await pipelineApi.runLogsHistory(wid, runId.value)).data.data
    historySteps.value = hist.steps ?? []
    mode.value = 'history'
    if (autoScroll.value) scrollToBottom()
  } catch (e) { notify.apiError(e) }
}

async function init() {
  loading.value = true
  liveLines.value = []
  historySteps.value = []
  selectedOrdinal.value = null
  autoScroll.value = true
  atBottom.value = true
  await loadRun()
  loading.value = false
  if (!run.value) return
  if (['succeeded', 'failed', 'canceled'].includes(run.value.status)) {
    await loadHistory()
  } else {
    openStream()
  }
}

function onLogScroll() {
  const el = logBox.value
  if (!el) return
  const near = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  atBottom.value = near
  // Reaching the bottom re-arms auto-follow; scrolling up pauses it.
  autoScroll.value = near
}
function jumpToBottom() {
  autoScroll.value = true
  scrollToBottom()
}

function selectStep(ordinal: number | null) {
  if (!canFilterSteps.value && ordinal != null) return
  selectedOrdinal.value = selectedOrdinal.value === ordinal ? null : ordinal
  jumpToBottom()
}

async function copyLogs() {
  const ok = await copyText(displayLines.value.join('\n'))
  if (!ok) return
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}
function downloadLogs() {
  const blob = new Blob([displayLines.value.join('\n') + '\n'], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  const suffix = selectedOrdinal.value != null ? `-step${selectedOrdinal.value + 1}` : ''
  a.download = `pipeline-run-${run.value?.number ?? runId.value}${suffix}.log`
  a.click()
  URL.revokeObjectURL(url)
}
async function copyVal(v?: string | null) {
  if (v) await copyText(v)
}

watch([currentWorkspaceId, runId], () => { es?.close(); init() }, { immediate: true })
// Tick once a second so live durations (run + running step) count up.
ticker = setInterval(() => { now.value = Date.now() }, 1000)
onBeforeUnmount(() => { es?.close(); if (ticker) clearInterval(ticker) })
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link :to="{ name: 'pipeline-runs', params: { id: pipelineId } }" class="back-link">
          <span class="mdi mdi-arrow-left"></span> Runs
        </router-link>
        <h1 v-if="run">Run #{{ run.number }}</h1>
        <p v-if="run" class="subtitle">
          <span class="meta-item"><span class="mdi mdi-source-branch"></span> {{ run.trigger }}</span>
          <span class="meta-item"><span class="mdi mdi-timer-outline"></span> {{ runDuration }}</span>
          <span v-if="run.commit" class="meta-item mono" :title="run.commit">
            <span class="mdi mdi-source-commit"></span> {{ run.commit.slice(0, 7) }}
          </span>
          <span v-if="run.started_at" class="meta-item" :title="new Date(run.started_at).toLocaleString()">
            <span class="mdi mdi-clock-outline"></span> {{ relativeTime(run.started_at, now) }}
          </span>
          <span v-if="run.no_cache" class="meta-item" title="Every layer was rebuilt; the build cache was ignored">
            <span class="mdi mdi-cached"></span> no cache
          </span>
        </p>
      </div>
      <span v-if="run" class="badge badge-lg" :class="statusMeta(run.status).badge">
        <span class="mdi" :class="statusMeta(run.status).icon"></span> {{ statusMeta(run.status).label }}
      </span>
    </div>

    <p v-if="run?.commit_message" class="commit-message">{{ run.commit_message }}</p>

    <div v-if="loading && !run" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="run">
      <div v-if="run.error" class="run-error"><span class="mdi mdi-alert-circle-outline"></span> {{ run.error }}</div>

      <div class="detail-grid">
        <!-- Steps as a status stepper -->
        <div class="card steps-card">
          <div class="card-header"><h3>Steps</h3><span class="muted-count">{{ steps.length }}</span></div>
          <div class="stepper">
            <button
              v-for="s in steps"
              :key="s.id"
              class="step"
              :class="[
                `step-${s.status}`,
                { active: selectedOrdinal === s.ordinal, clickable: canFilterSteps },
              ]"
              :disabled="!canFilterSteps"
              @click="selectStep(s.ordinal)"
            >
              <span class="step-rail">
                <span class="step-dot" :class="statusMeta(s.status).badge">
                  <span class="mdi" :class="statusMeta(s.status).icon"></span>
                </span>
              </span>
              <span class="step-main">
                <span class="step-name">
                  {{ s.name }}
                  <span v-if="s.uses" class="badge badge-neutral step-uses">{{ s.uses }}</span>
                  <span v-if="s.continue_on_error" class="badge step-allow" title="A failure here doesn't fail the run">continue-on-error</span>
                  <span v-if="s.no_cache" class="badge badge-neutral step-uses" title="Built without cache">no cache</span>
                </span>
                <span class="step-sub">
                  <span>{{ statusMeta(s.status).label }}</span>
                  <span v-if="s.status === 'failed' && s.continue_on_error" class="allow-note">· failure ignored</span>
                  <span v-if="s.started_at">· {{ stepDuration(s) }}</span>
                  <span v-if="s.status === 'succeeded' || s.status === 'failed'" class="mono">· exit {{ s.exit_code }}</span>
                </span>
              </span>
            </button>
          </div>
          <p v-if="canFilterSteps" class="stepper-hint">
            <span class="mdi mdi-cursor-default-click-outline"></span> Click a step to filter its logs
          </p>
        </div>

        <!-- Logs -->
        <div class="card logs-card">
          <div class="card-header logs-header">
            <div class="logs-title">
              <h3>Logs</h3>
              <span v-if="streaming" class="live"><span class="live-dot"></span> Live</span>
              <button v-if="selectedOrdinal != null" class="chip chip-clear" @click="selectStep(null)">
                {{ steps.find((s) => s.ordinal === selectedOrdinal)?.name }}
                <span class="mdi mdi-close"></span>
              </button>
              <span class="line-count">{{ displayLines.length }} lines</span>
            </div>
            <div class="logs-actions">
              <button class="btn-icon btn-icon-muted" :class="{ on: wrap }" :title="wrap ? 'Disable wrap' : 'Wrap lines'" @click="wrap = !wrap">
                <span class="mdi mdi-wrap"></span>
              </button>
              <button class="btn-icon btn-icon-muted" :class="{ on: autoScroll }" title="Auto-scroll" @click="autoScroll = !autoScroll">
                <span class="mdi mdi-arrow-down-bold-box-outline"></span>
              </button>
              <button class="btn-icon btn-icon-muted" title="Copy logs" :disabled="!displayLines.length" @click="copyLogs">
                <span class="mdi" :class="copied ? 'mdi-check' : 'mdi-content-copy'"></span>
              </button>
              <button class="btn-icon btn-icon-muted" title="Download logs" :disabled="!displayLines.length" @click="downloadLogs">
                <span class="mdi mdi-download"></span>
              </button>
            </div>
          </div>
          <div class="logs-wrap">
            <div ref="logBox" class="logs" :class="{ nowrap: !wrap }" @scroll="onLogScroll">
              <div v-if="displayLines.length === 0" class="logs-empty">
                {{ streaming ? 'Waiting for output…' : 'No log output.' }}
              </div>
              <div v-for="(l, i) in displayLines" :key="i" class="log-line" :class="{ sep: l.startsWith('─') }">
                <span class="ln">{{ i + 1 }}</span><span class="lt">{{ l }}</span>
              </div>
            </div>
            <button v-if="!atBottom && displayLines.length" class="jump" title="Jump to latest" @click="jumpToBottom">
              <span class="mdi mdi-arrow-down"></span>
            </button>
          </div>
        </div>
      </div>

      <div v-if="image" class="card mt-4">
        <div class="card-header"><h3>Built image</h3></div>
        <div class="card-body image-grid">
          <div class="image-field">
            <span class="image-label">Reference</span>
            <span class="image-val">
              <span class="mono">{{ image.repository }}<template v-if="image.tag">:{{ image.tag }}</template></span>
              <button class="btn-icon btn-icon-muted copy-inline" title="Copy" @click="copyVal(`${image.repository}${image.tag ? ':' + image.tag : ''}`)"><span class="mdi mdi-content-copy"></span></button>
            </span>
          </div>
          <div class="image-field">
            <span class="image-label">Digest</span>
            <span class="image-val">
              <span class="mono">{{ image.digest }}</span>
              <button class="btn-icon btn-icon-muted copy-inline" title="Copy" @click="copyVal(image.digest)"><span class="mdi mdi-content-copy"></span></button>
            </span>
          </div>
          <div class="image-field">
            <span class="image-label">Commit</span>
            <span class="mono">{{ image.commit ? image.commit.slice(0, 12) : '—' }}</span>
          </div>
          <div class="image-field">
            <span class="image-label">Size</span>
            <span class="mono">{{ fmtBytes(image.size_bytes) }}</span>
          </div>
          <div class="image-field">
            <span class="image-label">Runner</span>
            <span class="mono">{{ image.runner || '—' }}</span>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 4px; display: flex; flex-wrap: wrap; gap: 12px; }
.meta-item { display: inline-flex; align-items: center; gap: 4px; }
.meta-item .mdi { font-size: 14px; opacity: 0.8; }
.back-link { font-size: 13px; color: var(--text-muted); text-decoration: none; display: inline-flex; align-items: center; gap: 4px; }
.back-link:hover { color: var(--primary-500); }
.mono { font-family: 'JetBrains Mono', monospace; }
.badge .mdi { font-size: 13px; }
.badge-lg { font-size: 13px; padding: 5px 12px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.commit-message {
  font-size: 14px; color: var(--text-secondary, var(--text-muted)); margin: -4px 0 16px;
  padding-left: 12px; border-left: 3px solid var(--border-primary); line-height: 1.5;
}

.run-error {
  display: flex; align-items: center; gap: 8px;
  background: var(--danger-50); color: var(--danger-600); border: 1px solid var(--danger-600);
  border-radius: 8px; padding: 10px 14px; font-size: 13px; margin-bottom: 16px;
  font-family: 'JetBrains Mono', monospace;
}
.run-error .mdi { font-size: 16px; }

/* Steps + logs side by side, stacking on narrow screens. */
.detail-grid { display: grid; grid-template-columns: minmax(240px, 320px) 1fr; gap: 16px; align-items: start; }
@media (max-width: 900px) { .detail-grid { grid-template-columns: 1fr; } }

.card-header { display: flex; align-items: center; justify-content: space-between; }
.muted-count {
  font-size: 12px; color: var(--text-muted); background: var(--bg-tertiary, rgba(127,127,127,0.12));
  border-radius: 20px; padding: 1px 8px; min-width: 20px; text-align: center;
}

/* Stepper */
.stepper { display: flex; flex-direction: column; padding: 6px; }
.step {
  display: flex; gap: 10px; align-items: flex-start; text-align: left; width: 100%;
  background: none; border: none; border-radius: 8px; padding: 8px 10px; cursor: default;
  color: inherit; font: inherit; position: relative;
}
.step.clickable { cursor: pointer; }
.step.clickable:hover { background: var(--bg-tertiary, rgba(127,127,127,0.08)); }
.step.active { background: var(--accent-bg, rgba(99,102,241,0.12)); }
.step-rail { display: flex; flex-direction: column; align-items: center; }
.step-dot {
  width: 24px; height: 24px; border-radius: 50%; display: inline-flex; align-items: center;
  justify-content: center; flex-shrink: 0;
}
.step-dot .mdi { font-size: 14px; }
/* Connector line between step dots. */
.step:not(:last-child) .step-rail::after {
  content: ''; width: 2px; flex: 1; min-height: 10px; margin: 2px 0;
  background: var(--border-primary);
}
.step-main { display: flex; flex-direction: column; gap: 2px; min-width: 0; padding-bottom: 4px; }
.step-name { font-size: 13px; font-weight: 600; display: flex; align-items: center; gap: 6px; }
.step-uses { font-size: 10px; padding: 1px 6px; }
.step-allow {
  font-size: 10px; padding: 1px 6px; font-weight: 600;
  background: var(--warning-bg, rgba(217, 119, 6, 0.14));
  color: var(--warning-600, #b45309);
}
.step-sub { font-size: 12px; color: var(--text-muted); display: flex; flex-wrap: wrap; gap: 4px; }
.allow-note { color: var(--warning-600, #b45309); font-weight: 600; }
.stepper-hint {
  font-size: 11px; color: var(--text-muted); padding: 4px 12px 10px; display: flex; align-items: center; gap: 4px;
}

/* Logs */
.logs-card { display: flex; flex-direction: column; }
.logs-header { gap: 10px; flex-wrap: wrap; }
.logs-title { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.live { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; font-weight: 600; color: var(--success-600, #16a34a); }
.live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--success-600, #16a34a);
  box-shadow: 0 0 0 0 rgba(22,163,74,0.5); animation: pulse 1.6s infinite;
}
@keyframes pulse {
  0% { box-shadow: 0 0 0 0 rgba(22,163,74,0.5); }
  70% { box-shadow: 0 0 0 6px rgba(22,163,74,0); }
  100% { box-shadow: 0 0 0 0 rgba(22,163,74,0); }
}
.line-count { font-size: 12px; color: var(--text-muted); }
.chip {
  display: inline-flex; align-items: center; gap: 4px; font-size: 12px; padding: 2px 8px;
  border-radius: 20px; background: var(--accent-bg, rgba(99,102,241,0.12)); color: var(--accent, #6366f1);
  border: none; cursor: pointer;
}
.chip .mdi { font-size: 13px; }
.logs-actions { display: flex; gap: 2px; }
.logs-actions .btn-icon.on { color: var(--accent, #6366f1); }

.logs-wrap { position: relative; }
.logs {
  background: #0b1020; color: #d6deeb; font-family: 'JetBrains Mono', monospace; font-size: 12px;
  line-height: 1.6; padding: 10px 0; max-height: 520px; min-height: 160px; overflow: auto;
  border-radius: 0 0 8px 8px;
}
.logs-empty { color: #5b6b8c; padding: 4px 16px; }
.log-line { display: flex; padding: 0 14px 0 0; }
.log-line:hover { background: rgba(255,255,255,0.03); }
.ln {
  flex: 0 0 44px; text-align: right; padding-right: 14px; color: #47597e; user-select: none;
  -webkit-user-select: none;
}
.lt { white-space: pre-wrap; word-break: break-word; flex: 1; min-width: 0; }
.logs.nowrap .lt { white-space: pre; }
.logs.nowrap { overflow-x: auto; }
.log-line.sep .lt { color: #8aa0c6; font-weight: 600; }

.jump {
  position: absolute; right: 14px; bottom: 12px; width: 34px; height: 34px; border-radius: 50%;
  background: var(--accent, #6366f1); color: #fff; border: none; cursor: pointer;
  display: flex; align-items: center; justify-content: center; box-shadow: 0 4px 12px rgba(0,0,0,0.3);
}
.jump .mdi { font-size: 18px; }

.image-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 14px; }
.image-field { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.image-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.image-field .mono { font-size: 12px; word-break: break-all; }
.image-val { display: flex; align-items: center; gap: 4px; min-width: 0; }
.copy-inline { flex-shrink: 0; }
.copy-inline .mdi { font-size: 13px; }
</style>
