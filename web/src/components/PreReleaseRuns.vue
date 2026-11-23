<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'
import { jobApi } from '@/api/resources'
import type { Job } from '@/api/types'

const props = defineProps<{ wsId: number; deploymentId: number | null; active: boolean }>()

const runs = ref<Job[]>([])
const loaded = ref(false)
const open = ref<Record<number, boolean>>({})
const output = ref<Record<number, string>>({})
let poll: ReturnType<typeof setInterval> | null = null

async function load() {
  if (!props.wsId || !props.deploymentId) {
    runs.value = []
    return
  }
  try {
    runs.value = (await jobApi.forDeployment(props.wsId, props.deploymentId)).data.data ?? []
    // Newest attempt first, and opened, since it is the one that decided the deploy.
    runs.value.reverse()
    const latest = runs.value[0]
    if (latest && !Object.keys(open.value).length) toggle(latest.id)
    for (const id of Object.keys(open.value).map(Number)) if (open.value[id]) fetchOutput(id)
  } catch {
    runs.value = []
  } finally {
    loaded.value = true
  }
}

async function fetchOutput(id: number) {
  try {
    output.value[id] = (await jobApi.get(props.wsId, id)).data.data?.logs ?? ''
  } catch {
    output.value[id] = ''
  }
}

function toggle(id: number) {
  open.value[id] = !open.value[id]
  if (open.value[id]) fetchOutput(id)
}

function stopPoll() {
  if (poll) clearInterval(poll)
  poll = null
}

watch(() => [props.wsId, props.deploymentId], () => {
  open.value = {}
  output.value = {}
  loaded.value = false
}, { immediate: true })

watch(() => [props.wsId, props.deploymentId, props.active], () => {
  load()
  stopPoll()
  if (props.active) poll = setInterval(load, 3000)
}, { immediate: true })
onBeforeUnmount(stopPoll)

function badge(s: string) {
  return s === 'succeeded' ? 'badge-success' : s === 'failed' ? 'badge-danger' : s === 'canceled' || s === 'skipped' ? 'badge-warning' : 'badge-info'
}

function duration(j: Job): string {
  if (!j.started_at) return '—'
  const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now()
  const secs = Math.max(0, Math.round((end - new Date(j.started_at).getTime()) / 1000))
  return secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`
}
</script>

<template>
  <div class="pre-runs">
    <p class="form-hint">{{ $t('preRelease.stepHint') }}</p>
    <p v-if="loaded && !runs.length" class="text-muted">{{ $t('preRelease.noRuns') }}</p>
    <div v-for="j in runs" :key="j.id" class="pre-run" :class="{ open: open[j.id] }">
      <div class="pre-run-head" role="button" tabindex="0" :aria-expanded="!!open[j.id]" @click="toggle(j.id)" @keydown.enter.prevent="toggle(j.id)">
        <span class="mdi" :class="open[j.id] ? 'mdi-chevron-down' : 'mdi-chevron-right'"></span>
        <span class="pre-run-name">{{ j.name }}</span>
        <span class="cell-sub">{{ $t('preRelease.attemptN', { n: j.attempt ?? 1 }) }}</span>
        <span class="cell-sub">{{ duration(j) }}</span>
        <span v-if="j.exit_code != null" class="cell-sub">{{ $t('preRelease.exit', { code: j.exit_code }) }}</span>
        <span class="badge badge-dot" :class="badge(j.status)">{{ j.status }}</span>
        <router-link :to="{ name: 'jobs', query: { job: j.id } }" class="pre-run-link" @click.stop>
          {{ $t('preRelease.openInJobs') }} <span class="mdi mdi-open-in-new"></span>
        </router-link>
      </div>
      <div v-if="open[j.id]" class="pre-run-body">
        <p v-if="j.error" class="pre-run-error"><span class="mdi mdi-alert-circle-outline"></span> {{ j.error }}</p>
        <pre class="pre-run-output">{{ output[j.id] ?? '…' }}</pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pre-runs { display: flex; flex-direction: column; gap: 8px; }
.pre-run { border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; }
.pre-run-head { display: flex; align-items: center; gap: 10px; padding: 8px 12px; cursor: pointer; }
.pre-run-head:hover { background: var(--bg-hover); }
.pre-run-name { font-weight: 600; }
.pre-run-link { margin-left: auto; font-size: 12px; white-space: nowrap; }
.pre-run-body { padding: 0 12px 12px; }
.pre-run-error { margin: 0 0 8px; font-size: 12px; color: var(--danger-600); }
.pre-run-output { margin: 0; max-height: 320px; overflow: auto; padding: 10px; border-radius: 6px; background: var(--bg-code); color: #d4d4f8; font-family: 'JetBrains Mono', 'Fira Code', monospace; font-size: 12px; white-space: pre-wrap; word-break: break-word; }
</style>
