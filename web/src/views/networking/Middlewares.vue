<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { middlewareApi } from '@/api/middlewares'
import { useMiddlewareCatalog } from '@/composables/useMiddlewareCatalog'
import MiddlewareFormModal from '@/components/MiddlewareFormModal.vue'
import type { Middleware } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)
const { ensure: ensureCatalog, typeInfo } = useMiddlewareCatalog()

const items = ref<Middleware[]>([])
const loading = ref(false)
const showCreate = ref(false)

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    void ensureCatalog(id)
    items.value = (await middlewareApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function onSaved() {
  showCreate.value = false
  load(currentWorkspaceId.value)
}

// Short preview of a middleware's rule keys for the table.
function ruleSummary(m: Middleware): string {
  const keys = Object.keys(m.rule || {})
  if (!keys.length) return '—'
  return keys.slice(0, 3).join(', ') + (keys.length > 3 ? '…' : '')
}

function open(m: Middleware) { router.push(`/middlewares/${m.id}`) }
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Middlewares</h1>
        <p class="subtitle">Goma Gateway middlewares, referenced by routes.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="showCreate = true">
        <span class="mdi mdi-plus"></span> New middleware
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-tune-vertical" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No middlewares</h3>
        <p>Add auth, rate-limiting, CORS, and more — then attach them to routes.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="showCreate = true">Create a middleware</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>Type</th><th>Paths</th><th>Rule</th></tr></thead>
          <tbody>
            <tr v-for="m in items" :key="m.id" class="row-link" @click="open(m)">
              <td>
                <span class="cell-title">{{ m.display_name || m.name }}</span>
                <div v-if="typeInfo(m.type)" class="cell-sub">{{ typeInfo(m.type)?.description }}</div>
              </td>
              <td><span class="badge badge-neutral">{{ m.type }}</span></td>
              <td class="cell-sub">{{ (m.paths || []).join(', ') || '/*' }}</td>
              <td class="cell-sub mono">{{ ruleSummary(m) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <MiddlewareFormModal :open="showCreate" :workspace-id="currentWorkspaceId" :editing="null" @close="showCreate = false" @saved="onSaved" />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.mono { font-family: monospace; }
.row-link { cursor: pointer; }
.row-link:hover { background: var(--surface-2, rgba(127, 127, 127, 0.06)); }
</style>
