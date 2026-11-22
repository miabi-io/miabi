<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { routeApi } from '@/api/routes'
import { appApi } from '@/api/apps'
import type { Route, Application } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import RouteFormModal from '@/components/RouteFormModal.vue'
import { routeStatusBadge, routeStatusLabel } from '@/utils/routes'
import {
  ROUTES_PAGE_SIZE,
  filterRoutes,
  parseRouteFilters,
  routeFiltersToQuery,
  type RouteFilters,
  type RouteSort,
} from '@/utils/routeFilters'

const ws = useWorkspaceStore()
const { t } = useI18n()
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

// Not upstream health.
const statusBadge = routeStatusBadge
const statusLabel = routeStatusLabel

// The URL holds the filters, so a filtered list can be shared and Back from a route restores it.
const filters = computed(() => parseRouteFilters(route.query))
function setFilters(patch: Partial<RouteFilters>) {
  const next = { ...filters.value, page: 0, ...patch }
  router.replace({ query: routeFiltersToQuery(next) })
}
const search = ref(filters.value.q)
let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => setFilters({ q: search.value.trim() }), 250)
}
watch(() => filters.value.q, (q) => { if (q !== search.value.trim()) search.value = q })

const filtered = computed(() => filterRoutes(items.value, filters.value, appName))
const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / ROUTES_PAGE_SIZE)))
const page = computed(() => Math.min(filters.value.page, pageCount.value - 1))
const visible = computed(() => filtered.value.slice(page.value * ROUTES_PAGE_SIZE, (page.value + 1) * ROUTES_PAGE_SIZE))
const filtering = computed(() => !!(filters.value.q || filters.value.app || filters.value.status || filters.value.kind))
const failingCount = computed(() => items.value.filter((r) => r.status === 'error' || r.status === 'offline').length)

function sortBy(col: RouteSort) {
  setFilters({ sort: col, desc: filters.value.sort === col ? !filters.value.desc : false, page: filters.value.page })
}
function sortIcon(col: RouteSort) {
  if (filters.value.sort !== col) return 'mdi-unfold-more-horizontal'
  return filters.value.desc ? 'mdi-chevron-down' : 'mdi-chevron-up'
}
function clearFilters() {
  search.value = ''
  router.replace({ query: {} })
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

// Params are consumed once; left in the URL they would re-open the form on reload and Back.
function openFromQuery() {
  if (route.query.new !== '1') return
  const wanted = Number(route.query.app)
  // Only `new` is consumed: `app` stays as the list's filter.
  router.replace({ path: route.path, query: { ...route.query, new: undefined } })

  if (!ws.canEdit || !apps.value.length) return
  // Usually a workspace switch mid-click; opening the form on another app would be worse than not opening it.
  if (Number.isFinite(wanted) && wanted > 0) {
    if (!apps.value.some((a) => a.id === wanted)) {
      notify.error(t('notify.routes.thatApplicationIsNotIn'), t('routes.notStarted'))
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
    notify.success(t('routes.deleted'))
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
        <h1>{{ $t('nav.networking.routes') }}</h1>
        <p class="subtitle">{{ $t('routes.subtitle') }}</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" :disabled="apps.length === 0" @click="openCreate()">
        <span class="mdi mdi-plus"></span> {{ $t('routes.new') }}
      </button>
    </div>

    <div v-if="items.length > 0" class="routes-toolbar">
      <div class="search">
        <span class="mdi mdi-magnify"></span>
        <input v-model="search" class="form-input" type="search" :placeholder="$t('routes.searchPlaceholder')" :aria-label="$t('routes.searchPlaceholder')" @input="onSearch" />
      </div>
      <select class="form-select" :value="filters.app ?? ''" :aria-label="$t('dashboard.col.application')" @change="setFilters({ app: Number(($event.target as HTMLSelectElement).value) || null })">
        <option value="">{{ $t('routes.filter.allApps') }}</option>
        <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
      </select>
      <select class="form-select" :value="filters.status" :aria-label="$t('dashboard.col.status')" @change="setFilters({ status: ($event.target as HTMLSelectElement).value as RouteFilters['status'] })">
        <option value="">{{ $t('routes.filter.anyStatus') }}</option>
        <option value="live">{{ $t('routes.filter.live') }}</option>
        <option value="failing">{{ $t('routes.filter.failing') }}{{ failingCount ? ` (${failingCount})` : '' }}</option>
        <option value="pending">{{ $t('routes.filter.pending') }}</option>
        <option value="disabled">{{ $t('routes.filter.disabled') }}</option>
        <option value="maintenance">{{ $t('routes.filter.maintenance') }}</option>
      </select>
      <select class="form-select" :value="filters.kind" :aria-label="$t('routes.filter.kind')" @change="setFilters({ kind: ($event.target as HTMLSelectElement).value as RouteFilters['kind'] })">
        <option value="">{{ $t('routes.filter.anyKind') }}</option>
        <option value="custom">{{ $t('routes.filter.custom') }}</option>
        <option value="generated">{{ $t('routes.filter.generated') }}</option>
      </select>
      <span class="text-muted text-sm toolbar-count">
        {{ filtering ? $t('routes.filter.countOf', { n: filtered.length, total: items.length }) : $t('routes.filter.count', { n: items.length }, items.length) }}
        <button v-if="filtering" class="link-btn" @click="clearFilters">{{ $t('routes.filter.clear') }}</button>
      </span>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-routes" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('routes.empty') }}</h3>
        <p>{{ apps.length === 0 ? $t('routes.emptyNoApps') : $t('routes.emptyHint') }}</p>
        <button v-if="ws.canEdit && apps.length" class="btn btn-primary mt-4" @click="openCreate()">{{ $t('routes.create') }}</button>
      </div>
      <div v-else-if="filtered.length === 0" class="empty-state">
        <span class="mdi mdi-filter-off-outline" style="font-size: 36px; color: var(--text-muted)"></span>
        <p>{{ $t('routes.filter.none') }}</p>
        <button class="btn btn-secondary btn-sm mt-4" @click="clearFilters">{{ $t('routes.filter.clear') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th><button class="sort-btn" @click="sortBy('name')">{{ $t('routes.col.route') }} <span class="mdi" :class="sortIcon('name')"></span></button></th>
              <th><button class="sort-btn" @click="sortBy('app')">{{ $t('dashboard.col.application') }} <span class="mdi" :class="sortIcon('app')"></span></button></th>
              <th>{{ $t('routes.col.hosts') }}</th>
              <th><button class="sort-btn" @click="sortBy('status')">{{ $t('dashboard.col.status') }} <span class="mdi" :class="sortIcon('status')"></span></button></th>
              <th>TLS</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in visible" :key="r.id" class="row-clickable" @click="router.push(`/routes/${r.id}`)">
              <td>
                <span class="cell-title">
                  <router-link :to="`/routes/${r.id}`" class="route-link" @click.stop>{{ r.display_name || r.name }}</router-link>
                  <span v-if="r.generated" class="badge badge-info" style="margin-left: 8px" :title="$t('routes.generatedHint')">{{ $t('routes.badge.auto') }}</span>
                  <span v-if="!r.enabled" class="badge badge-neutral" style="margin-left: 8px">{{ $t('routes.badge.disabled') }}</span>
                  <span v-if="r.maintenance?.enabled" class="badge badge-warning" style="margin-left: 8px" :title="$t('routes.maintenanceHint')">{{ $t('routes.maintenance') }}</span>
                </span>
                <div class="cell-sub">{{ r.path }}</div>
              </td>
              <td class="cell-sub">{{ appName(r.application_id) }}</td>
              <td class="cell-sub">{{ (r.hosts || []).join(', ') || '—' }}</td>
              <td><span class="badge" :class="statusBadge(r)" :title="r.status_reason || ''">{{ statusLabel(r) }}</span></td>
              <td><span class="badge badge-neutral">{{ r.tls_mode }}</span></td>
              <td class="text-right table-actions" @click.stop>
                <!-- Generated external-access routes are managed from the app's External Access, not here. -->
                <button v-if="ws.canEdit && !r.generated" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(r)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit && !r.generated" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = r"><span class="mdi mdi-delete-outline"></span></button>
                <span v-if="r.generated" class="mdi mdi-lock-outline cell-sub" :title="$t('routes.managedHint')"></span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="pageCount > 1" class="card-footer pager">
        <button class="btn btn-ghost btn-sm" :disabled="page === 0" @click="setFilters({ page: page - 1 })">
          <span class="mdi mdi-chevron-left"></span></button>
        <span class="text-sm text-muted">{{ $t('routes.filter.page', { n: page + 1, total: pageCount }) }}</span>
        <button class="btn btn-ghost btn-sm" :disabled="page >= pageCount - 1" @click="setFilters({ page: page + 1 })">
          <span class="mdi mdi-chevron-right"></span></button>
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
      :title="$t('confirm.title.deleteRoute')"
      :message="$t('confirm.message.routes.deleteRouteNameIts', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmRemove"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.routes-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 12px; }
.routes-toolbar .form-select { width: auto; }
.search { position: relative; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; width: 260px; max-width: 100%; }
.toolbar-count { margin-left: auto; display: flex; gap: 8px; align-items: center; }
.link-btn { background: none; border: 0; padding: 0; font: inherit; color: var(--primary-500); cursor: pointer; }
.link-btn:hover { text-decoration: underline; }
.sort-btn { background: none; border: 0; padding: 0; font: inherit; color: inherit; text-transform: inherit; letter-spacing: inherit; cursor: pointer; display: inline-flex; align-items: center; gap: 2px; }
.route-link { color: var(--text-primary); text-decoration: none; }
.route-link:hover { color: var(--primary-500); text-decoration: underline; }
.pager { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 8px; }
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
