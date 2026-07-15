<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { middlewareApi } from '@/api/middlewares'
import { routeApi } from '@/api/routes'
import { toYaml } from '@/utils/yaml'
import { useMiddlewareCatalog } from '@/composables/useMiddlewareCatalog'
import MiddlewareFormModal from '@/components/MiddlewareFormModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { Middleware, Route } from '@/api/types'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)
const { ensure: ensureCatalog, typeInfo } = useMiddlewareCatalog()

const mwId = computed(() => Number(route.params.id))
const item = ref<Middleware | null>(null)
const usedBy = ref<Route[]>([])
const loading = ref(false)

const ruleYaml = computed(() => {
  const r = item.value?.rule || {}
  return Object.keys(r).length ? toYaml(r) : ''
})

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid || !mwId.value) return
  loading.value = true
  void ensureCatalog(wid)
  try {
    item.value = (await middlewareApi.get(wid, mwId.value)).data.data
    const routes = (await routeApi.list(wid)).data.data ?? []
    usedBy.value = routes.filter((r) => (r.middlewares || []).includes(item.value!.name))
  } catch (e) {
    notify.apiError(e)
    router.replace('/middlewares')
  } finally {
    loading.value = false
  }
}
watch([mwId, currentWorkspaceId], load, { immediate: true })

const showEdit = ref(false)
function onSaved() { showEdit.value = false; load() }

const showDelete = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  deleting.value = true
  try {
    await middlewareApi.remove(wid, item.value.id)
    notify.success('Middleware deleted')
    router.replace('/middlewares')
  } catch (e) { notify.apiError(e) }
  finally { deleting.value = false }
}
</script>

<template>
  <div v-if="item">
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" title="Back" aria-label="Back" @click="router.push('/middlewares')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ item.display_name || item.name }}</h1>
          <span class="cell-sub">{{ typeInfo(item.type)?.description || 'Goma Gateway middleware' }}</span>
        </div>
        <span class="badge badge-neutral">{{ item.type }}</span>
      </div>
      <div v-if="ws.canEdit" class="flex items-center gap-2">
        <button class="btn btn-secondary" @click="showEdit = true"><span class="mdi mdi-pencil-outline"></span> Edit</button>
        <button class="btn btn-danger" @click="showDelete = true"><span class="mdi mdi-delete-outline"></span> Delete</button>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Configuration</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row"><span class="detail-key">Name</span><span class="mono">{{ item.name }}</span></div>
        <div class="detail-row"><span class="detail-key">Type</span><span><span class="badge badge-neutral">{{ item.type }}</span></span></div>
        <div class="detail-row"><span class="detail-key">Paths</span><span class="mono">{{ (item.paths || []).join(', ') || '/*' }}</span></div>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Rule</h2></div>
      <div class="card-body">
        <pre v-if="ruleYaml" class="rule-block">{{ ruleYaml }}</pre>
        <p v-else class="text-muted text-sm" style="margin: 0">No rule configuration.</p>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>Used by</h2></div>
      <div class="card-body">
        <div v-if="usedBy.length" class="used-list">
          <router-link v-for="r in usedBy" :key="r.id" :to="`/routes/${r.id}`" class="used-row">
            <span class="mdi mdi-routes"></span> {{ r.name }}
          </router-link>
        </div>
        <p v-else class="text-muted text-sm" style="margin: 0">Not used by any route.</p>
      </div>
    </div>

    <MiddlewareFormModal :open="showEdit" :workspace-id="currentWorkspaceId" :editing="item" @close="showEdit = false" @saved="onSaved" />

    <ConfirmDialog
      :open="showDelete"
      title="Delete middleware"
      :message="`Delete middleware &quot;${item.name}&quot;? Routes referencing it will lose it.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="showDelete = false"
    />
  </div>
  <div v-else-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>
</template>

<style scoped>
.title-group { display: flex; align-items: center; gap: 12px; }
.title-group h1 { margin: 0; line-height: 1.2; }
.mono { font-family: monospace; font-size: 13px; }
.text-muted { color: var(--text-muted); }
.detail-list { display: flex; flex-direction: column; }
.detail-row { display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 12px 0; border-bottom: 1px solid var(--border-primary); font-size: 13px; }
.detail-row:last-child { border-bottom: none; }
.detail-key { color: var(--text-muted); }
.rule-block { margin: 0; font-family: monospace; font-size: 12px; white-space: pre-wrap; word-break: break-word; }
.used-list { display: flex; flex-direction: column; gap: 6px; }
.used-row { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-primary); text-decoration: none; }
.used-row:hover { text-decoration: underline; }
</style>
