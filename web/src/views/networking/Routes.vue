<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { routeApi } from '@/api/routes'
import { appApi } from '@/api/apps'
import type { Route, Application } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import RouteFormModal from '@/components/RouteFormModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const route = useRoute()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Route[]>([])
const apps = ref<Application[]>([])
const loading = ref(false)
const showModal = ref(false)
const editing = ref<Route | null>(null)
// Preselected application when the create form is opened from a deep link.
const presetAppId = ref<number | null>(null)
const toDelete = ref<Route | null>(null)
const deleting = ref(false)

// Config-sync status with the gateway (live = Goma is serving it; offline = a
// host's domain isn't verified or the route is disabled). Not upstream health.
function statusBadge(r: Route): string {
  const s = r.status ?? 'pending'
  if (s === 'live') return 'badge-success'
  if (s === 'error') return 'badge-danger'
  if (s === 'offline') return 'badge-warning'
  return 'badge-neutral'
}
function statusLabel(r: Route): string {
  const s = r.status ?? 'pending'
  return s === 'live' ? 'live' : s === 'offline' ? 'offline' : s
}

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await routeApi.list(id)).data.data ?? []
    apps.value = (await appApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(
  currentWorkspaceId,
  async (id) => {
    await load(id)
    openFromQuery()
  },
  { immediate: true },
)

// Deep link: /routes?app=<id>&new=1 opens the create form with that app already
// chosen. The params are consumed once — left in the URL they would re-open the
// form on every reload and on Back.
function openFromQuery() {
  if (route.query.new !== '1') return
  const wanted = Number(route.query.app)
  router.replace({ path: route.path })

  if (!ws.canEdit || !apps.value.length) return
  // An app id that isn't in this workspace usually means the workspace was
  // switched between clicking and landing. Opening the form on some other app
  // would be worse than not opening it.
  if (Number.isFinite(wanted) && wanted > 0) {
    if (!apps.value.some((a) => a.id === wanted)) {
      notify.error('That application is not in this workspace.', 'Route not started')
      return
    }
    openCreate(wanted)
    return
  }
  openCreate()
}

function appName(id: number) {
  return apps.value.find((a) => a.id === id)?.name ?? `#${id}`
}

function openCreate(appId?: number) {
  editing.value = null
  presetAppId.value = appId ?? null
  showModal.value = true
}
function openEdit(r: Route) {
  editing.value = r
  presetAppId.value = null
  showModal.value = true
}
function onSaved() {
  showModal.value = false
  load(currentWorkspaceId.value)
}

async function confirmRemove() {
  if (!currentWorkspaceId.value || !toDelete.value) return
  deleting.value = true
  try {
    await routeApi.remove(currentWorkspaceId.value, toDelete.value.id)
    notify.success('Route deleted')
    toDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Routes</h1>
        <p class="subtitle">Goma Gateway routes for your applications.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" :disabled="apps.length === 0" @click="openCreate()">
        <span class="mdi mdi-plus"></span> New route
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-routes" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No routes</h3>
        <p>{{ apps.length === 0 ? 'Create an application first, then expose it with a route.' : 'Expose an application on a hostname and path.' }}</p>
        <button v-if="ws.canEdit && apps.length" class="btn btn-primary mt-4" @click="openCreate()">Create a route</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Route</th><th>Application</th><th>Hosts</th><th>Status</th><th>TLS</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in items" :key="r.id" class="row-clickable" @click="router.push(`/routes/${r.id}`)">
              <td>
                <span class="cell-title">
                  {{ r.name }}
                  <span v-if="r.generated" class="badge badge-info" style="margin-left: 8px" title="Auto-generated for external access; managed from the app's External Access">auto</span>
                  <span v-if="!r.enabled" class="badge badge-neutral" style="margin-left: 8px">disabled</span>
                  <span v-if="r.maintenance?.enabled" class="badge badge-warning" style="margin-left: 8px" title="The gateway answers this route itself; the backend is never reached">maintenance</span>
                </span>
                <div class="cell-sub">{{ r.path }}</div>
              </td>
              <td class="cell-sub">{{ appName(r.application_id) }}</td>
              <td class="cell-sub">{{ (r.hosts || []).join(', ') || '—' }}</td>
              <td><span class="badge" :class="statusBadge(r)" :title="r.status_reason || ''">{{ statusLabel(r) }}</span></td>
              <td><span class="badge badge-neutral">{{ r.tls_mode }}</span></td>
              <td class="text-right table-actions" @click.stop>
                <!-- Generated external-access routes are managed from the app's External Access, not here. -->
                <button v-if="ws.canEdit && !r.generated" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(r)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit && !r.generated" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = r"><span class="mdi mdi-delete-outline"></span></button>
                <span v-if="r.generated" class="mdi mdi-lock-outline cell-sub" title="Managed from the app's External Access"></span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <RouteFormModal
      :open="showModal"
      :workspace-id="currentWorkspaceId"
      :editing="editing"
      :apps="apps"
      :preset-app-id="presetAppId"
      @close="showModal = false"
      @saved="onSaved"
    />

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete route"
      :message="`Delete route &quot;${toDelete?.name}&quot;? Its hosts will stop routing to the app.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmRemove"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.form-row { display: flex; gap: 12px; }
.mode-tabs { display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; margin-bottom: 12px; }
.mode-tab { padding: 6px 18px; background: var(--bg-secondary); border: none; cursor: pointer; font-size: 13px; color: var(--text-muted); }
.mode-tab.active { background: var(--primary-600); color: #fff; }
.form-warning { display: flex; align-items: center; gap: 8px; padding: 8px 12px; margin-bottom: 12px; border-radius: 8px; background: color-mix(in srgb, var(--warning, #d97706) 14%, transparent); color: var(--warning, #d97706); font-size: 13px; }
.form-error { color: var(--danger, #dc2626); font-size: 13px; margin-top: 6px; }
.yaml-editor { width: 100%; font-family: 'JetBrains Mono', monospace; font-size: 13px; line-height: 1.5; white-space: pre; overflow-wrap: normal; overflow-x: auto; tab-size: 2; }
code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 5px; border-radius: 4px; }
.hint { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.host-row { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
.host-sub { flex: 1; }
.host-dot { color: var(--text-muted); font-weight: 600; }
.host-domain { flex: 0 0 auto; max-width: 55%; font-family: 'JetBrains Mono', monospace; }

/* Methods: horizontal selectable chips. */
.info-icon { font-size: 14px; color: var(--text-muted); cursor: help; margin-left: 2px; vertical-align: middle; }
.info-icon:hover { color: var(--primary-500); }
.methods-grid { display: flex; flex-wrap: wrap; gap: 8px; }
.method-chip {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  padding: 5px 12px; border: 1px solid var(--border-primary); border-radius: 999px;
  font-size: 12px; font-weight: 600; color: var(--text-secondary); user-select: none;
  transition: background 0.12s, border-color 0.12s, color 0.12s;
}
.method-chip:hover { border-color: var(--primary-500); }
.method-chip.active { background: var(--primary-600); border-color: var(--primary-600); color: #fff; }
.method-chip input { display: none; }

/* Middlewares: a selectable, scrollable zone. */
.middleware-select {
  display: flex; flex-direction: column; gap: 4px;
  max-height: 180px; overflow-y: auto;
  padding: 6px; border: 1px solid var(--border-primary);
  border-radius: var(--radius, 8px); background: var(--bg-secondary);
}
.middleware-option {
  display: flex; align-items: center; gap: 8px; cursor: pointer;
  padding: 6px 8px; border-radius: 6px; font-size: 13px;
}
.middleware-option:hover { background: var(--bg-tertiary); }
.middleware-option.active { background: color-mix(in srgb, var(--primary-500) 12%, transparent); }
.middleware-option-name { font-weight: 500; }
.middleware-option-type { font-size: 10px; margin-left: auto; }
</style>
