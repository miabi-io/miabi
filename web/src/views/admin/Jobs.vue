<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { adminApi } from '@/api/admin'
import type { JobStatus, JobStats } from '@/api/types'
import { relativeTime } from '@/utils/time'
import { useNotificationStore } from '@/stores/notification'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

const notify = useNotificationStore()

const jobs = ref<JobStatus[]>([])
const stats = ref<JobStats | null>(null)
const loading = ref(false)
// Ticks so "last run" / "next run" stay honest between refreshes rather than ageing silently.
const now = ref(Date.now())
const lastLoadedAt = ref(0)

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const [list, summary] = await Promise.all([
      adminApi.listJobs(page, pageable.value.size),
      adminApi.jobStats(),
    ])
    jobs.value = list.data.data ?? []
    pageable.value = list.data.pageable
    stats.value = summary.data.data
    lastLoadedAt.value = Date.now()
  } catch (err) {
    notify.apiError(err, 'Failed to load jobs')
  } finally {
    loading.value = false
  }
})

function reload() {
  goToPage(pageable.value.current_page)
}

// --- Status ---------------------------------------------------------------
type JobState = 'running' | 'failed' | 'ok'
const STATES: { value: JobState | 'all'; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'failed', label: 'Failed' },
  { value: 'running', label: 'Running' },
  { value: 'ok', label: 'Healthy' },
]

function jobState(j: JobStatus): JobState {
  if (j.running) return 'running'
  return j.last_error ? 'failed' : 'ok'
}

// --- Filtering ------------------------------------------------------------
// Both filters are CLIENT-SIDE and therefore scoped to the loaded page: /admin/jobs takes only
// page and size. The stat cards count every job, so a filter that silently searched one page while
// the card said "3 failed" would read as a bug. The toolbar states the scope instead, and the
// cards stay as counters rather than becoming filters that could not honour them.
const search = ref('')
const state = ref<JobState | 'all'>('all')

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const out = jobs.value.filter((j) => {
    if (state.value !== 'all' && jobState(j) !== state.value) return false
    if (!q) return true
    return (
      j.name.toLowerCase().includes(q) ||
      (j.kind || '').toLowerCase().includes(q) ||
      j.schedule.toLowerCase().includes(q) ||
      humanCron(j.schedule).toLowerCase().includes(q)
    )
  })
  // Problems first: this is a monitoring view, and a failure buried under twenty healthy jobs is
  // the one thing it exists to surface.
  const rank: Record<JobState, number> = { failed: 0, running: 1, ok: 2 }
  return out.sort((a, b) => rank[jobState(a)] - rank[jobState(b)] || a.name.localeCompare(b.name))
})

const filtering = computed(() => state.value !== 'all' || search.value.trim() !== '')
const multiPage = computed(() => (pageable.value.total_pages ?? 1) > 1)

function clearFilters() {
  state.value = 'all'
  search.value = ''
}

// --- Formatting -----------------------------------------------------------
function fmtDate(s?: string | null, fallback = 'Never'): string {
  if (!s) return fallback
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return fallback
  return d.toLocaleString()
}

function humanCron(expr: string): string {
  const e = (expr || '').trim()
  if (!e) return ''
  const parts = e.split(/\s+/)
  if (parts.length === 5) {
    const [min, hour, dom, mon, dow] = parts
    const every = (f: string) => f === '*'

    // */N * * * *  => Every N minutes
    const stepMin = /^\*\/(\d+)$/.exec(min)
    if (stepMin && every(hour) && every(dom) && every(mon) && every(dow)) {
      const n = Number(stepMin[1])
      return `Every ${n} minute${n === 1 ? '' : 's'}`
    }

    // 0 */N * * *  => Every N hours
    const stepHour = /^\*\/(\d+)$/.exec(hour)
    if (min === '0' && stepHour && every(dom) && every(mon) && every(dow)) {
      const n = Number(stepHour[1])
      return `Every ${n} hour${n === 1 ? '' : 's'}`
    }

    // M H * * *  => Daily at HH:MM
    const numMin = /^\d+$/.test(min)
    const numHour = /^\d+$/.test(hour)
    if (numMin && numHour && every(dom) && every(mon) && every(dow)) {
      const hh = String(Number(hour)).padStart(2, '0')
      const mm = String(Number(min)).padStart(2, '0')
      return `Daily at ${hh}:${mm}`
    }

    // M H * * D  => Weekly on <day> at HH:MM
    if (numMin && numHour && every(dom) && every(mon) && /^\d+$/.test(dow)) {
      const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
      const day = days[Number(dow) % 7]
      const hh = String(Number(hour)).padStart(2, '0')
      const mm = String(Number(min)).padStart(2, '0')
      return `Weekly on ${day} at ${hh}:${mm}`
    }

    // Every minute
    if (every(min) && every(hour) && every(dom) && every(mon) && every(dow)) {
      return 'Every minute'
    }

    // M * * * *  => Hourly at minute M
    if (numMin && every(hour) && every(dom) && every(mon) && every(dow)) {
      return `Hourly at minute ${Number(min)}`
    }
  }
  return expr
}

// --- Auto-refresh ---------------------------------------------------------
// The page polls, so it can move under the reader. Pausing is offered because the most likely
// reason to be staring at this page is reading a failure, and that is exactly when a reload that
// reorders the grid is least welcome.
const autoRefresh = ref(true)
const POLL_MS = 30000

const poll = setInterval(() => {
  if (autoRefresh.value && !loading.value) reload()
}, POLL_MS)
const ticker = setInterval(() => { now.value = Date.now() }, 1000)

onBeforeUnmount(() => {
  clearInterval(poll)
  clearInterval(ticker)
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Jobs</h1>
        <p class="page-subtitle">
          Scheduled background work — database backups and cron jobs — across the whole platform.
        </p>
      </div>
      <div class="header-actions">
        <span v-if="lastLoadedAt" class="refreshed text-muted">
          Updated {{ relativeTime(new Date(lastLoadedAt).toISOString(), now) }}
        </span>
        <label class="auto-toggle" :title="`Reload every ${POLL_MS / 1000}s`">
          <input type="checkbox" v-model="autoRefresh" />
          Auto-refresh
        </label>
        <button class="btn btn-secondary" :disabled="loading" @click="reload">
          <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
          Refresh
        </button>
      </div>
    </div>

    <!-- Summary across ALL jobs, not just this page. Counters, not filters: the list is
         paginated server-side, so a card that filtered could not honour its own number. -->
    <div v-if="stats" class="stats-grid stats-compact">
      <div class="stat-card">
        <div class="stat-header">
          <span class="stat-label">Total jobs</span>
          <span class="stat-icon stat-icon-primary"><span class="mdi mdi-clock-outline"></span></span>
        </div>
        <div class="stat-value">{{ stats.total }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-header">
          <span class="stat-label">Running</span>
          <span class="stat-icon stat-icon-info"><span class="mdi mdi-play-circle-outline"></span></span>
        </div>
        <div class="stat-value">{{ stats.running }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-header">
          <span class="stat-label">Healthy</span>
          <span class="stat-icon stat-icon-success"><span class="mdi mdi-check-circle-outline"></span></span>
        </div>
        <div class="stat-value">{{ stats.ok }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-header">
          <span class="stat-label">Failed</span>
          <span class="stat-icon" :class="stats.failed > 0 ? 'stat-icon-danger' : 'stat-icon-success'"><span class="mdi mdi-alert-circle-outline"></span></span>
        </div>
        <div class="stat-value">{{ stats.failed }}</div>
      </div>
    </div>

    <div v-if="jobs.length" class="jobs-toolbar">
      <div class="search-field">
        <span class="mdi mdi-magnify"></span>
        <input
          v-model="search"
          class="form-input"
          type="search"
          placeholder="Filter by name, kind or schedule"
          aria-label="Filter jobs"
        />
      </div>
      <div class="state-filter" role="group" aria-label="Filter by status">
        <button
          v-for="s in STATES"
          :key="s.value"
          type="button"
          class="state-chip"
          :class="{ active: state === s.value }"
          :aria-pressed="state === s.value"
          @click="state = s.value"
        >
          {{ s.label }}
        </button>
      </div>
      <span class="toolbar-count text-muted">
        {{ filtered.length }} of {{ jobs.length }} on this page
        <template v-if="filtering && multiPage"> · other pages are not searched</template>
      </span>
    </div>

    <div v-if="loading && jobs.length === 0" class="card">
      <div class="card-body" style="display: flex; justify-content: center; padding: 48px 0">
        <span class="spinner"></span>
      </div>
    </div>

    <div v-else-if="jobs.length === 0" class="empty-state">
      <span class="mdi mdi-clock-outline" style="font-size: 44px; color: var(--text-muted)"></span>
      <h3>No scheduled jobs</h3>
      <p class="text-muted">
        Scheduled background jobs, like database backups, appear here once they are configured.
      </p>
    </div>

    <div v-else-if="filtered.length === 0" class="empty-state">
      <span class="mdi mdi-filter-remove-outline" style="font-size: 44px; color: var(--text-muted)"></span>
      <h3>No jobs match</h3>
      <p class="text-muted">
        Nothing on this page matches the current filter.
        <template v-if="multiPage">Other pages are not searched — clear the filter and page through, or widen it.</template>
      </p>
      <button class="btn btn-secondary mt-4" @click="clearFilters">Clear filter</button>
    </div>

    <div v-else class="jobs-grid">
      <div
        v-for="job in filtered"
        :key="`${job.kind}-${job.id}`"
        class="card job-card"
        :class="`job-${jobState(job)}`"
      >
        <div class="card-body">
          <div class="job-head">
            <div class="job-title">
              <span class="job-name" :title="job.name">{{ job.name }}</span>
              <!-- Kind lives here now rather than as a breakdown under the total: it describes
                   the job, and this is where you are already looking at one. -->
              <span v-if="job.kind" class="kind-chip">{{ job.kind }}</span>
            </div>
            <span v-if="job.running" class="badge badge-dot badge-warning">Running</span>
            <span v-else-if="job.last_error" class="badge badge-dot badge-danger">Failed</span>
            <span v-else class="badge badge-dot badge-success">OK</span>
          </div>

          <div class="job-schedule">
            <span class="schedule-human">{{ humanCron(job.schedule) || job.schedule }}</span>
            <span v-if="humanCron(job.schedule) !== job.schedule" class="cron-chip" :title="job.schedule">
              {{ job.schedule }}
            </span>
          </div>

          <dl class="job-meta">
            <dt>Last run</dt>
            <dd :title="fmtDate(job.last_run_at, 'Never')">
              {{ job.last_run_at ? relativeTime(job.last_run_at, now) : 'Never' }}
            </dd>
            <dt>Next run</dt>
            <dd :title="fmtDate(job.next_run_at, '—')">
              {{ job.next_run_at ? relativeTime(job.next_run_at, now) : '—' }}
            </dd>
          </dl>

          <!-- The error is the reason an admin opened this page. It wraps instead of being
               truncated to a tooltip nobody hovers. -->
          <div v-if="job.last_error" class="job-error">
            <span class="mdi mdi-alert-circle-outline"></span>
            <span class="job-error-text">{{ job.last_error }}</span>
          </div>
        </div>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.page-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--text-muted);
}
.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.refreshed {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.auto-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
  user-select: none;
}

/* Compact summary cards — smaller than the global stat-card. */
.stats-compact {
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.stats-compact :deep(.stat-card) {
  padding: 12px 14px;
  border-radius: var(--radius);
}
.stats-compact :deep(.stat-card:hover) {
  transform: none;
  box-shadow: var(--shadow-sm);
}
.stats-compact :deep(.stat-header) {
  margin-bottom: 4px;
}
.stats-compact :deep(.stat-value) {
  font-size: 20px;
}
.stats-compact :deep(.stat-icon) {
  width: 26px;
  height: 26px;
  font-size: 15px;
}

/* Toolbar */
.jobs-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.search-field {
  position: relative;
  flex: 1 1 260px;
  max-width: 360px;
}
.search-field .mdi {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  pointer-events: none;
}
.search-field .form-input {
  padding-left: 32px;
}
.state-filter {
  display: inline-flex;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  overflow: hidden;
}
.state-chip {
  border: none;
  background: var(--bg-secondary);
  color: var(--text-muted);
  padding: 7px 14px;
  font-size: 13px;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
}
.state-chip + .state-chip {
  border-left: 1px solid var(--border-primary);
}
.state-chip:hover {
  color: var(--text-primary);
}
.state-chip.active {
  background: var(--primary-600);
  color: #fff;
}
.toolbar-count {
  font-size: 12px;
  margin-left: auto;
}

/* Job cards */
.jobs-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
}
.job-card {
  border-left: 3px solid transparent;
}
/* A failing job is findable at a glance, before reading a single word. */
.job-card.job-failed {
  border-left-color: var(--danger-500, #ef4444);
}
.job-card.job-running {
  border-left-color: var(--warning-500, #f59e0b);
}

.job-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}
.job-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.job-name {
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.kind-chip {
  flex-shrink: 0;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.02em;
  color: var(--text-muted);
  background: var(--bg-tertiary);
  border-radius: 5px;
  padding: 2px 6px;
}

.job-schedule {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
/* The human reading leads; the raw expression is the footnote, not the headline. */
.schedule-human {
  font-size: 13px;
  color: var(--text-primary);
}
.cron-chip {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  background: var(--bg-tertiary);
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 11px;
  color: var(--text-muted);
}

.job-meta {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 12px;
  margin: 12px 0 0;
  font-size: 13px;
}
.job-meta dt {
  color: var(--text-muted);
}
.job-meta dd {
  margin: 0;
  text-align: right;
  color: var(--text-primary);
  font-variant-numeric: tabular-nums;
}

.job-error {
  margin-top: 12px;
  padding: 8px 10px;
  border-radius: var(--radius);
  background: var(--danger-50, rgba(239, 68, 68, 0.08));
  font-size: 12px;
  color: var(--danger-600, #dc2626);
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.job-error .mdi {
  flex-shrink: 0;
  line-height: 1.5;
}
/* Wrap rather than truncate: this is the sentence the page exists to show. Bounded so one
   pathological stack trace cannot push every other card off the screen. */
.job-error-text {
  overflow-wrap: anywhere;
  max-height: 7.5em;
  overflow-y: auto;
  line-height: 1.5;
}

@media (max-width: 640px) {
  .page-header {
    flex-direction: column;
  }
  .toolbar-count {
    margin-left: 0;
  }
}
</style>
