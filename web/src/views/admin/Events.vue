<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { adminApi } from '@/api/admin'
import type { AdminEvent } from '@/api/types'
import { sseUrl } from '@/api/client'
import { downloadAudit } from '@/api/rbac'
import { useEntitlement } from '@/composables/useEntitlement'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import AppModal from '@/components/AppModal.vue'

const notify = useNotificationStore()
// The audit log itself is an Enterprise feature; export is a further entitlement.
const auditLog = useEntitlement('audit_log')
const auditExport = useEntitlement('audit_export')

const exporting = ref(false)
async function exportAudit(format: 'json' | 'csv') {
  if (exporting.value) return
  exporting.value = true
  try {
    await downloadAudit('admin', format, from.value, to.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    exporting.value = false
  }
}

const events = ref<AdminEvent[]>([])
const loading = ref(false)
const search = ref('')
const action = ref('')
const order = ref<'desc' | 'asc'>('desc')
const from = ref('')
const to = ref('')
const selected = ref<AdminEvent | null>(null)

let es: EventSource | null = null
let searchTimer: ReturnType<typeof setTimeout> | null = null

const { pageable, goToPage } = usePagination(async (page) => {
  if (!auditLog.has.value) {
    events.value = []
    return
  }
  loading.value = true
  try {
    const res = await adminApi.listEvents(search.value, action.value, page, pageable.value.size, order.value, from.value, to.value)
    events.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e: unknown) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})

// Debounce filter/sort/range changes; each resets to the first page.
function scheduleReload() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}
watch([search, action, order, from, to], scheduleReload)

function clearRange() {
  from.value = ''
  to.value = ''
}

function prepend(ev: AdminEvent) {
  if (!ev || ev.id == null) return
  // Only fold live events into the default newest-first first page.
  if (order.value !== 'desc' || pageable.value.current_page !== 0) return
  if (events.value.some((e) => e.id === ev.id)) return
  events.value = [ev, ...events.value].slice(0, pageable.value.size)
  pageable.value = { ...pageable.value, total_elements: pageable.value.total_elements + 1 }
}

function startStream() {
  es = new EventSource(sseUrl('/admin/events/stream'))
  es.onmessage = (e) => {
    try {
      const parsed = JSON.parse(e.data) as { type?: string; data?: AdminEvent } | AdminEvent
      const ev = (parsed as { data?: AdminEvent }).data ?? (parsed as AdminEvent)
      prepend(ev)
    } catch {
      // Ignore malformed frames (e.g. keepalive comments).
    }
  }
}

function open(ev: AdminEvent) {
  selected.value = ev
}

function actionBadge(a: string): string {
  if (a.includes('delete')) return 'badge-danger'
  if (a.includes('create')) return 'badge-success'
  return 'badge-neutral'
}

function fmtDate(s: string): string {
  return new Date(s).toLocaleString()
}

onMounted(() => {
  if (auditLog.has.value) startStream()
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  if (es) {
    es.close()
    es = null
  }
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Events</h1>
        <p class="text-muted">Platform audit feed</p>
      </div>
      <div v-if="auditLog.has.value" class="header-actions">
        <div v-if="auditExport.has.value" class="export-group">
          <button class="btn btn-secondary btn-sm" :disabled="exporting" @click="exportAudit('json')">
            <span class="mdi mdi-download"></span> JSON
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="exporting" @click="exportAudit('csv')">
            <span class="mdi mdi-download"></span> CSV
          </button>
        </div>
        <router-link
          v-else
          to="/admin/license"
          class="btn btn-secondary btn-sm"
          title="Audit log export requires an Enterprise license"
        >
          <span class="mdi mdi-lock-outline"></span> Export
        </router-link>
        <div class="live">
          <span class="live-dot"></span>
          <span class="text-muted">Live</span>
        </div>
      </div>
    </div>

    <div v-if="!auditLog.has.value" class="card mt-4">
      <div class="empty-state">
        <span class="mdi mdi-shield-star-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>An Enterprise feature</h3>
        <p>The platform audit log is available with a Miabi Enterprise license.</p>
        <router-link to="/admin/license" class="btn btn-secondary btn-sm">View license</router-link>
      </div>
    </div>

    <template v-else>
    <div class="toolbar">
      <input v-model="search" class="form-input" placeholder="Search action / target…" aria-label="Search action / target" />
      <input v-model="action" class="form-input" placeholder="Filter by action…" aria-label="Filter by action" />
      <label class="range-field">From <input v-model="from" type="date" class="form-input" /></label>
      <label class="range-field">To <input v-model="to" type="date" class="form-input" /></label>
      <button v-if="from || to" class="btn btn-ghost btn-sm" @click="clearRange">Clear</button>
      <select v-model="order" class="form-select order-select" title="Sort order" aria-label="Sort order">
        <option value="desc">Recent first</option>
        <option value="asc">Oldest first</option>
      </select>
    </div>

    <div class="card mt-4">
      <div v-if="loading && events.length === 0" class="card-body">
        <span class="spinner"></span>
      </div>
      <div v-else-if="events.length === 0" class="empty-state">
        <span class="mdi mdi-pulse" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No events yet</h3>
        <p>Platform activity will stream in here as it happens.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Action</th>
              <th>Actor</th>
              <th>Workspace</th>
              <th>IP</th>
              <th>Time</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="ev in events" :key="ev.id" class="row-clickable" @click="open(ev)">
              <td>
                <span class="cell-title">
                  <span class="badge" :class="actionBadge(ev.action)">{{ ev.action }}</span>
                </span>
                <span v-if="ev.target_type" class="cell-sub">
                  {{ ev.target_type }}<template v-if="ev.target_id"> {{ ev.target_id }}</template>
                </span>
              </td>
              <td class="cell-sub">{{ ev.actor_id ?? '—' }}</td>
              <td class="cell-sub">{{ ev.workspace_id ?? 'platform' }}</td>
              <td class="cell-sub">{{ ev.ip_address || '—' }}</td>
              <td class="cell-sub">{{ fmtDate(ev.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />
    </template>

    <Teleport to="body">
      <AppModal v-if="selected" @close="selected = null">
        <div class="modal-header">
          <h3>
            <span class="badge" :class="actionBadge(selected.action)">{{ selected.action }}</span>
          </h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="selected = null">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="modal-body">
          <dl class="detail-list">
            <dt class="text-muted">ID</dt>
            <dd>{{ selected.id }}</dd>
            <dt class="text-muted">Action</dt>
            <dd>{{ selected.action }}</dd>
            <dt class="text-muted">Target</dt>
            <dd>{{ selected.target_type }}<template v-if="selected.target_id"> {{ selected.target_id }}</template></dd>
            <dt class="text-muted">Actor</dt>
            <dd>{{ selected.actor_id ?? '—' }}</dd>
            <dt class="text-muted">Workspace</dt>
            <dd>{{ selected.workspace_id ?? 'platform' }}</dd>
            <dt class="text-muted">IP</dt>
            <dd>{{ selected.ip_address || '—' }}</dd>
            <dt class="text-muted">Time</dt>
            <dd>{{ fmtDate(selected.created_at) }}</dd>
          </dl>
          <div v-if="selected.metadata" class="meta-block">
            <p class="text-muted">Metadata</p>
            <pre>{{ JSON.stringify(selected.metadata, null, 2) }}</pre>
          </div>
        </div>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.toolbar .form-input {
  flex: 1;
  min-width: 160px;
}
.range-field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  flex: 0 0 auto;
}
.range-field .form-input {
  flex: 0 0 auto;
  min-width: 0;
  width: auto;
}
.order-select {
  flex: 0 0 auto;
  min-width: 150px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 14px;
}
.export-group {
  display: flex;
  gap: 6px;
}
.live {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}
.live-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #22c55e;
  box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.6);
  animation: live-pulse 1.6s ease-out infinite;
}
@keyframes live-pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.6);
  }
  70% {
    box-shadow: 0 0 0 7px rgba(34, 197, 94, 0);
  }
  100% {
    box-shadow: 0 0 0 0 rgba(34, 197, 94, 0);
  }
}

.detail-list {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px 16px;
  margin: 0 0 16px;
  font-size: 13px;
}
.detail-list dt {
  font-size: 12px;
}
.detail-list dd {
  margin: 0;
  word-break: break-all;
}

.meta-block p {
  margin: 0 0 6px;
  font-size: 12px;
}
.meta-block pre,
pre {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  background: var(--bg-tertiary);
  padding: 12px;
  border-radius: 8px;
  overflow: auto;
  margin: 0;
}
</style>
