<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'
import { jobApi } from '@/api/resources'
import type { Job } from '@/api/types'

const props = defineProps<{ wsId: number; deploymentId: number | null; active: boolean }>()

const runs = ref<Job[]>([])
let poll: ReturnType<typeof setInterval> | null = null

async function load() {
  if (!props.wsId || !props.deploymentId) {
    runs.value = []
    return
  }
  try {
    runs.value = (await jobApi.forDeployment(props.wsId, props.deploymentId)).data.data ?? []
  } catch {
    runs.value = []
  }
}

function stopPoll() {
  if (poll) clearInterval(poll)
  poll = null
}

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
  <div v-if="runs.length" class="card">
    <div class="card-header">
      <div>
        <h2>{{ $t('preRelease.stepTitle') }}</h2>
        <p class="card-subtitle">{{ $t('preRelease.stepHint') }}</p>
      </div>
    </div>
    <div class="table-wrapper">
      <table>
        <thead>
          <tr>
            <th>{{ $t('preRelease.job') }}</th>
            <th>{{ $t('preRelease.attempt') }}</th>
            <th>{{ $t('preRelease.duration') }}</th>
            <th class="text-right">{{ $t('dashboard.col.status') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="j in runs" :key="j.id">
            <td>
              <router-link :to="{ name: 'jobs', query: { job: j.id } }" class="cell-title">{{ j.name }}</router-link>
              <div v-if="j.error" class="cell-sub dep-err" :title="j.error"><span class="mdi mdi-alert-circle-outline"></span> {{ j.error }}</div>
            </td>
            <td class="cell-sub">{{ j.attempt ?? 1 }}</td>
            <td class="cell-sub">{{ duration(j) }}</td>
            <td class="text-right"><span class="badge badge-dot" :class="badge(j.status)">{{ j.status }}</span></td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
