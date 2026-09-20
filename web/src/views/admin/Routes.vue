<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, onBeforeUnmount } from 'vue'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { AdminRoute, RouteSyncStatus, ResyncSummary } from '@/api/types'

const notify = useNotificationStore()
const { t } = useI18n()

const routes = ref<AdminRoute[]>([])
const loading = ref(false)
const search = ref('')
const status = ref<RouteSyncStatus | ''>('')

const confirmResync = ref(false)
const resyncing = ref(false)
const lastSummary = ref<ResyncSummary | null>(null)

let searchTimer: ReturnType<typeof setTimeout> | undefined

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listRoutes(page, pageable.value.size, search.value.trim(), status.value)
    routes.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

function reload() {
  goToPage(pageable.value.current_page)
}

function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}

function setStatus(s: RouteSyncStatus | '') {
  status.value = s
  goToPage(0)
}

async function runResync() {
  resyncing.value = true
  try {
    lastSummary.value = (await adminApi.resyncRoutes()).data.data
    confirmResync.value = false
    const s = lastSummary.value
    if (s.failed > 0) {
      notify.error(t('notify.routes.resyncedWorkspaceSFailed', { workspaces: s.workspaces, failed: s.failed }))
    } else {
      notify.success(`Resynced ${s.workspaces} workspace(s) · ${s.live} live, ${s.offline} offline`)
    }
    reload()
  } catch (e) {
    notify.apiError(e, 'Resync failed')
  } finally {
    resyncing.value = false
  }
}

// Config-sync status: live = served; offline = synced but not served (disabled or
// host not verified); error = gateway write failed; pending = never synced.
function statusBadge(s: RouteSyncStatus): string {
  if (s === 'live') return 'badge-success'
  if (s === 'error') return 'badge-danger'
  if (s === 'offline') return 'badge-warning'
  return 'badge-neutral'
}

function statusIcon(s: RouteSyncStatus): string {
  if (s === 'live') return 'mdi-check-circle'
  if (s === 'error') return 'mdi-alert-circle-outline'
  if (s === 'offline') return 'mdi-pause-circle-outline'
  return 'mdi-clock-outline'
}

function fmtDate(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Routes</h1>
        <p class="text-muted">Every workspace's gateway routes and their sync status with Goma.</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" :disabled="loading" @click="reload">
          <span class="mdi mdi-refresh"></span> Refresh
        </button>
        <button class="btn btn-primary" :disabled="resyncing" @click="confirmResync = true">
          <span class="mdi" :class="resyncing ? 'mdi-loading mdi-spin' : 'mdi-sync'"></span>
          {{ resyncing ? 'Resyncing…' : 'Resync all routes' }}
        </button>
      </div>
    </div>

    <!-- Last resync summary -->
    <div v-if="lastSummary" class="card summary-card">
      <div class="card-body summary">
        <span class="mdi mdi-information-outline summary-icon"></span>
        <div class="summary-text">
          Reconciled <strong>{{ lastSummary.workspaces }}</strong> workspace(s) ·
          <strong>{{ lastSummary.routes }}</strong> route(s):
          {{ lastSummary.live }} live, {{ lastSummary.offline }} offline,
          {{ lastSummary.errors }} error, {{ lastSummary.pending }} pending.
          <template v-if="lastSummary.failed > 0">
            <span class="summary-failed">{{ lastSummary.failed }} workspace(s) failed to reconcile.</span>
          </template>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input
            v-model="search"
            class="form-input"
            type="search"
            placeholder="Search routes by name or host…"
            aria-label="Search routes"
            @input="onSearchInput"
          />
        </div>
        <div class="filters">
          <button class="chip" :class="{ active: status === '' }" @click="setStatus('')">All</button>
          <button class="chip" :class="{ active: status === 'live' }" @click="setStatus('live')">Live</button>
          <button class="chip" :class="{ active: status === 'offline' }" @click="setStatus('offline')">Offline</button>
          <button class="chip" :class="{ active: status === 'error' }" @click="setStatus('error')">Error</button>
          <button class="chip" :class="{ active: status === 'pending' }" @click="setStatus('pending')">Pending</button>
        </div>
        <span class="text-muted">{{ pageable.total_elements }} route{{ pageable.total_elements === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && routes.length === 0" class="card-body"><span class="spinner"></span></div>

      <div v-else-if="routes.length === 0" class="empty-state">
        <span class="mdi mdi-routes" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ search.trim() || status ? 'No routes match your filters.' : 'No routes.' }}</h3>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Route</th>
              <th>Workspace</th>
              <th>Application</th>
              <th>Status</th>
              <th>TLS</th>
              <th>Last synced</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in routes" :key="r.id">
              <td>
                <span class="cell-text">
                  <span class="cell-title">
                    {{ r.name }}
                    <span v-if="r.generated" class="badge badge-neutral" title="Platform-generated">auto</span>
                  </span>
                  <span class="cell-sub">{{ (r.hosts && r.hosts.length ? r.hosts.join(', ') : '—') }}{{ r.path && r.path !== '/' ? r.path : '' }}</span>
                </span>
              </td>
              <td class="cell-sub">{{ r.workspace_name || ('#' + r.workspace_id) }}</td>
              <td class="cell-sub">{{ r.app_name || ('#' + r.application_id) }}</td>
              <td>
                <span class="badge" :class="statusBadge(r.status)" :title="r.status_reason || ''">
                  <span class="mdi" :class="statusIcon(r.status)"></span>
                  {{ r.status }}
                </span>
                <span v-if="!r.enabled" class="badge badge-neutral" style="margin-left: 4px">disabled</span>
              </td>
              <td><span class="badge badge-neutral">{{ r.tls_mode }}</span></td>
              <td class="cell-sub">{{ fmtDate(r.synced_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <ConfirmDialog
      :open="confirmResync"
      :title="$t('confirm.title.resyncAllRoutes')"
      :message="$t('confirm.message.routes.reRenderEveryWorkspace')"
      :confirm-label="$t('action.resync')"
      variant="primary"
      :busy="resyncing"
      @confirm="runResync"
      @cancel="confirmResync = false"
    />
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  gap: 10px;
}
.summary-card {
  margin-bottom: 16px;
}
.summary {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}
.summary-icon {
  font-size: 18px;
  color: var(--primary-600);
  flex-shrink: 0;
}
.summary-text {
  font-size: 13px;
  color: var(--text-secondary);
}
.summary-failed {
  color: var(--danger-600);
  margin-left: 4px;
}
.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.search {
  position: relative;
  flex: 1;
  max-width: 360px;
}
.search .mdi {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  pointer-events: none;
}
.search .form-input {
  padding-left: 32px;
}
.filters {
  display: flex;
  gap: 6px;
}
.chip {
  padding: 4px 12px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text-muted);
  font-size: 13px;
  cursor: pointer;
}
.chip.active {
  background: var(--primary-500);
  border-color: var(--primary-500);
  color: #fff;
}
.badge .mdi {
  font-size: 13px;
}
.mdi-spin {
  animation: mdi-spin 0.9s linear infinite;
}
@keyframes mdi-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
