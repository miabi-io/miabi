<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { workspaceEventApi } from '@/api/resources'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import type { RecentEvent } from '@/api/types'
import { eventIcon, eventSubjectLabel, eventSubjectLink } from '@/utils/eventSubject'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const events = ref<RecentEvent[]>([])
const loading = ref(false)
const order = ref<'desc' | 'asc'>('desc')
const severity = ref('')

const severities = [
  { value: '', label: 'All', icon: '', tone: '' },
  { value: 'info', label: 'Info', icon: 'mdi-information-outline', tone: 'tone-info' },
  { value: 'warning', label: 'Warning', icon: 'mdi-alert-outline', tone: 'tone-warning' },
  { value: 'error', label: 'Errors', icon: 'mdi-alert-circle-outline', tone: 'tone-error' },
]

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) {
    events.value = []
    return
  }
  loading.value = true
  try {
    const res = await workspaceEventApi.list(id, page, pageable.value.size, order.value, severity.value)
    events.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
    events.value = []
  } finally {
    loading.value = false
  }
})

watch(currentWorkspaceId, () => goToPage(0))
// Filters/sort reset to the first page.
watch([order, severity], () => goToPage(0))

function sevClass(sev: string): string {
  if (sev === 'error' || sev === 'critical') return 'sev-error'
  if (sev === 'warning') return 'sev-warning'
  if (sev === 'success') return 'sev-success'
  return ''
}

function when(ts: string): string {
  return new Date(ts).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Events</h1>
        <p class="subtitle">Application and database activity in {{ ws.contextLabel }}</p>
      </div>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="goToPage(pageable.current_page)">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span> Refresh
      </button>
    </div>

    <div class="toolbar">
      <div class="segmented" role="group" aria-label="Filter by severity">
        <button
          v-for="s in severities"
          :key="s.value"
          class="seg-btn"
          :class="[{ active: severity === s.value }, s.tone]"
          @click="severity = s.value"
        >
          <span v-if="s.icon" class="mdi" :class="s.icon"></span> {{ s.label }}
        </button>
      </div>
      <select v-model="order" class="form-select order-select" style="max-width: 180px;" title="Sort order" aria-label="Sort order">
        <option value="desc">Recent first</option>
        <option value="asc">Oldest first</option>
      </select>
    </div>

    <div class="card">
      <div v-if="loading && events.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="events.length === 0" class="empty-state">
        <span class="mdi mdi-timeline-text-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No events yet</h3>
        <p>Deploys, database lifecycle, container events, and configuration changes will appear here.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Event</th><th>Resource</th><th class="text-right">When</th></tr>
          </thead>
          <tbody>
            <tr
              v-for="e in events"
              :key="e.id"
              class="row-clickable"
              @click="router.push(eventSubjectLink(e))"
            >
              <td>
                <div class="evt">
                  <span class="evt-icon" :class="sevClass(e.severity)"><span class="mdi" :class="eventIcon(e.type)"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ e.message || e.type }}</span>
                    <span class="cell-sub">{{ e.type }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span class="cell-text">
                  <span class="cell-title">{{ eventSubjectLabel(e) }}</span>
                  <span v-if="e.subject_type === 'database'" class="cell-sub">database</span>
                  <span v-else-if="e.app_name" class="cell-sub">{{ e.app_name }}</span>
                </span>
              </td>
              <td class="text-right cell-sub">{{ when(e.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.toolbar { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; margin-bottom: 16px; }
.toolbar .order-select { min-width: 150px; flex: 0 0 auto; margin-left: auto; padding: 7px 10px; }

/* Severity segmented control */
.segmented { display: inline-flex; border: 1px solid var(--border-input); border-radius: var(--radius); overflow: hidden; }
.seg-btn {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 7px 14px; font-size: 13px; font-weight: 500;
  background: var(--bg-primary); color: var(--text-secondary);
  border: none; border-left: 1px solid var(--border-input); cursor: pointer;
  transition: background 0.12s, color 0.12s;
}
.seg-btn:first-child { border-left: none; }
.seg-btn .mdi { font-size: 15px; }
.seg-btn:hover { background: var(--bg-hover, var(--bg-tertiary)); }
.seg-btn.active { background: var(--primary-600); color: #fff; }
.seg-btn.active.tone-warning { background: var(--warning-600); }
.seg-btn.active.tone-error { background: var(--danger-600); }
.seg-btn.active.tone-info { background: var(--primary-500); }

.evt { display: flex; align-items: center; gap: 10px; min-width: 0; }
.evt-icon {
  flex-shrink: 0; width: 28px; height: 28px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center; font-size: 15px;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.evt-icon.sev-warning { background: var(--warning-50); color: var(--warning-600); }
.evt-icon.sev-error { background: var(--danger-50); color: var(--danger-600); }
.evt-icon.sev-success { background: var(--success-50); color: var(--success-600); }
@media (max-width: 639px) {
  .toolbar .order-select { width: 100%; max-width: none !important; margin-left: 0; flex: 1 1 100%;  }
  .segmented { width: 100%; overflow-x: auto; }
}
</style>
