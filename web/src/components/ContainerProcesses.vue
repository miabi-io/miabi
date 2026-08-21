<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { appApi } from '@/api/apps'
import { apiErrorMessage } from '@/api/client'
import type { ProcessList } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

// Live process table (docker top) for an app's container. Sourced from the
// host's ps, so it works even for images that ship no ps binary. Polls every 2s.
const props = defineProps<{ ws: number; appId: number; appName: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const data = ref<ProcessList | null>(null)
const error = ref('')
const loading = ref(true)
const paused = ref(false)
const updatedAt = ref(0)
let timer: number | undefined

async function refresh() {
  if (paused.value) return
  try {
    data.value = (await appApi.processes(props.ws, props.appId)).data.data
    error.value = ''
    updatedAt.value = Date.now()
  } catch (e) {
    error.value = apiErrorMessage(e, 'Failed to read processes')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 2000)
})
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <AppModal dialog-class="proc-modal" @close="emit('close')">
    <div class="modal-header">
      <h3>
        <span class="mdi mdi-format-list-bulleted"></span>
        Processes — {{ appName }}
        <span v-if="data" class="text-muted" style="font-weight: 400; font-size: 13px">{{ data.processes.length }}</span>
      </h3>
      <div class="flex items-center gap-2">
        <button class="btn btn-secondary btn-sm" :title="paused ? 'Resume live updates' : 'Pause live updates'" @click="paused = !paused">
          <span class="mdi" :class="paused ? 'mdi-play' : 'mdi-pause'"></span> {{ paused ? 'Resume' : 'Pause' }}
        </button>
        <button class="btn-icon btn-icon-muted" title="Close" aria-label="Close" @click="emit('close')"><span class="mdi mdi-close"></span></button>
      </div>
    </div>
    <div class="modal-body proc-body">
      <div v-if="loading" class="proc-state"><span class="spinner"></span></div>
      <div v-else-if="error" class="proc-state"><span class="mdi mdi-alert-circle-outline"></span> {{ error }}</div>
      <div v-else-if="data" class="table-wrapper">
        <table class="proc-table">
          <thead><tr><th v-for="(t, i) in data.titles" :key="i">{{ t }}</th></tr></thead>
          <tbody>
            <tr v-for="(row, ri) in data.processes" :key="ri">
              <td v-for="(cell, ci) in row" :key="ci" :class="{ 'proc-cmd': ci === row.length - 1 }">{{ cell }}</td>
            </tr>
            <tr v-if="data.processes.length === 0"><td :colspan="data.titles.length" class="text-muted" style="text-align: center; padding: 16px">No processes.</td></tr>
          </tbody>
        </table>
      </div>
    </div>
    <div class="modal-footer" style="justify-content: space-between">
      <span class="text-muted" style="font-size: 12px">{{ paused ? 'Paused' : 'Live' }}<span v-if="updatedAt"> · updated {{ new Date(updatedAt).toLocaleTimeString() }}</span></span>
      <span class="text-muted" style="font-size: 12px">via docker top (host ps)</span>
    </div>
  </AppModal>
</template>

<style scoped>
.proc-modal { max-width: 1040px; width: 100%; }
.proc-body { padding: 0; max-height: 64vh; overflow: auto; }
.proc-state { padding: 40px; text-align: center; color: var(--text-muted); }
.proc-table { font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; white-space: nowrap; }
.proc-table th { position: sticky; top: 0; background: var(--bg-secondary, var(--surface)); }
.proc-cmd { white-space: normal; word-break: break-all; max-width: 520px; }
</style>
