<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import type { ControlManagerFinding, ControlManagerStatus } from '@/api/types'
import { relativeTime } from '@/utils/time'
import { useNotificationStore } from '@/stores/notification'

const notify = useNotificationStore()

const status = ref<ControlManagerStatus | null>(null)
const loading = ref(false)
const lastLoadedAt = ref(0)
// Ticks so "first seen" and "checked" age honestly between reloads instead of freezing.
const now = ref(Date.now())

async function load() {
  loading.value = true
  try {
    status.value = (await adminApi.controlManager()).data.data
    lastLoadedAt.value = Date.now()
  } catch (err) {
    notify.apiError(err, 'Failed to load the reconciliation report')
  } finally {
    loading.value = false
  }
}

const autoRefresh = ref(true)
// The sweep itself runs every minute, so polling faster would only redraw the same report.
const POLL_MS = 30000
let poll = 0
let ticker = 0

onMounted(() => {
  load()
  poll = window.setInterval(() => {
    if (autoRefresh.value && !loading.value) load()
  }, POLL_MS)
  ticker = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)
})

onBeforeUnmount(() => {
  clearInterval(poll)
  clearInterval(ticker)
})

// A finding is only reported once two consecutive checks agree; the unconfirmed ones are shown apart, so a
// container caught mid-deploy never reads as a problem.
const confirmed = computed(() => (status.value?.findings ?? []).filter((f) => f.confirmed))
const suspected = computed(() => (status.value?.findings ?? []).filter((f) => !f.confirmed))
const dataLoss = computed(() => confirmed.value.filter((f) => f.kind === 'volume'))
const swept = computed(() => !!status.value?.last_sweep_at)

const KIND_LABELS: Record<string, string> = {
  container: 'Container',
  service: 'Swarm service',
  volume: 'Data volume',
  gateway: 'Node gateway',
}

const ACTION_LABELS: Record<string, string> = {
  redeploy: 'Redeploy in place',
  restore: 'Restore from a backup',
  none: 'Nothing Miabi may do',
  remove: 'Remove',
  import: 'Import',
}

function kindLabel(f: ControlManagerFinding): string {
  return KIND_LABELS[f.kind] ?? f.kind
}

// Where the finding's subject lives, so a row is a way in rather than an id to copy.
function subjectLink(f: ControlManagerFinding): string | null {
  if (f.owner_kind === 'app' && f.owner_id) return `/apps/${f.owner_id}`
  if (f.owner_kind === 'database' && f.owner_id) return `/databases/${f.owner_id}`
  if (f.owner_kind === 'node' && f.owner_id) return `/admin/nodes/${f.owner_id}`
  return null
}

function restoreHint(f: ControlManagerFinding): string {
  const r = f.restore
  if (!r) return ''
  if (!r.available) {
    return r.from === 'recovery-point'
      ? 'No completed recovery point to restore'
      : 'No completed backup to restore'
  }
  const taken = r.created_at ? relativeTime(r.created_at, now.value) : ''
  const what = r.from === 'recovery-point' ? `recovery point ${r.ref ?? ''}` : 'backup'
  return `Restore ${what}${taken ? ` from ${taken}` : ''}`.trim()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Reconciliation</h1>
        <p class="page-subtitle">
          What Miabi expects to be running, and what is actually there — across every node and cluster.
        </p>
      </div>
      <div class="header-actions">
        <span v-if="lastLoadedAt" class="refreshed text-muted">
          Updated {{ relativeTime(new Date(lastLoadedAt).toISOString(), now) }}
        </span>
        <label class="auto-toggle" :title="`Reload every ${POLL_MS / 1000}s`">
          <input v-model="autoRefresh" type="checkbox" />
          Auto-refresh
        </label>
        <button class="btn btn-secondary" :disabled="loading" @click="load">
          <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
          Refresh
        </button>
      </div>
    </div>

    <div v-if="loading && !status" class="card">
      <div class="card-body" style="display: flex; justify-content: center; padding: 48px 0">
        <span class="spinner"></span>
      </div>
    </div>

    <template v-else-if="status">
      <!-- Mode first: everything below means something different in observe than in enforce. -->
      <div class="card mb-4">
        <div class="card-body mode-row">
          <div>
            <span class="badge" :class="status.mode === 'enforce' ? 'badge-warning' : status.mode === 'off' ? 'badge-danger' : 'badge-info'">
              {{ status.mode }}
            </span>
            <span class="mode-text text-muted">
              <template v-if="status.mode === 'observe'">
                Reporting only — nothing is redeployed or restored.
              </template>
              <template v-else-if="status.mode === 'enforce'">
                A missing container or service is redeployed in place, when the app is otherwise whole.
                Lost data is never restored.
              </template>
              <template v-else>Checking is switched off, so nothing below is being watched.</template>
            </span>
          </div>
          <router-link class="btn btn-secondary btn-sm" to="/admin/settings">Change mode</router-link>
        </div>
      </div>

      <div class="stats-grid stats-compact">
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Missing</span>
            <span class="stat-icon" :class="confirmed.length ? 'stat-icon-danger' : 'stat-icon-success'">
              <span class="mdi mdi-alert-circle-outline"></span>
            </span>
          </div>
          <div class="stat-value">{{ confirmed.length }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Data lost</span>
            <span class="stat-icon" :class="dataLoss.length ? 'stat-icon-danger' : 'stat-icon-success'">
              <span class="mdi mdi-harddisk"></span>
            </span>
          </div>
          <div class="stat-value">{{ dataLoss.length }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Blocked apps</span>
            <span class="stat-icon" :class="status.blocked.length ? 'stat-icon-danger' : 'stat-icon-success'">
              <span class="mdi mdi-hand-back-left-outline"></span>
            </span>
          </div>
          <div class="stat-value">{{ status.blocked.length }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Not checked</span>
            <span class="stat-icon" :class="status.skipped.length ? 'stat-icon-warning' : 'stat-icon-success'">
              <span class="mdi mdi-eye-off-outline"></span>
            </span>
          </div>
          <div class="stat-value">{{ status.skipped.length }}</div>
        </div>
      </div>

      <!-- A standby control plane never sweeps, and an empty report on it would read as "all clear". -->
      <div v-if="!swept" class="empty-state">
        <span class="mdi mdi-cloud-off-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No check has run here</h3>
        <p class="text-muted">
          Reconciliation runs on one control plane at a time. This process has not swept — either it is
          standing by while another holds the lease, or it has only just started.
        </p>
      </div>

      <template v-else>
        <!-- Data loss leads: it is the only class nothing can undo. -->
        <div v-if="dataLoss.length" class="card mb-4 card-danger">
          <div class="card-header">
            <h2>Data is gone</h2>
            <span class="text-muted">{{ dataLoss.length }} volume(s)</span>
          </div>
          <div class="card-body">
            <p class="text-muted mb-4" style="font-size: 13px">
              Miabi never recreates or restores a volume: which recovery point to accept is your call, and
              starting a workload on an empty volume would turn the loss into an empty database nobody
              notices. Restore the backup below, then start the app.
            </p>
            <div class="table-wrapper">
              <table>
                <thead>
                  <tr><th>Volume</th><th>Owner</th><th>Condition</th><th>Restore</th><th>First seen</th></tr>
                </thead>
                <tbody>
                  <tr v-for="f in dataLoss" :key="f.ref">
                    <td class="trunc" :title="f.name">{{ f.name }}</td>
                    <td class="cell-sub">
                      <router-link v-if="subjectLink(f)" :to="subjectLink(f)!">
                        {{ f.owner_kind }} #{{ f.owner_id }}
                      </router-link>
                      <template v-else>{{ f.owner_kind }}</template>
                    </td>
                    <td>
                      <span class="badge" :class="f.class === 'replaced' ? 'badge-warning' : 'badge-danger'">
                        {{ f.class }}
                      </span>
                    </td>
                    <td class="cell-sub" :class="{ 'text-danger': f.restore && !f.restore.available }">
                      {{ restoreHint(f) || '—' }}
                    </td>
                    <td class="cell-sub" :title="f.first_seen_at">{{ relativeTime(f.first_seen_at, now) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>

        <div v-if="status.blocked.length" class="card mb-4">
          <div class="card-header">
            <h2>Blocked from starting</h2>
            <span class="text-muted">{{ status.blocked.length }} app(s)</span>
          </div>
          <div class="table-wrapper">
            <table>
              <thead><tr><th>Application</th><th>Why</th></tr></thead>
              <tbody>
                <tr v-for="b in status.blocked" :key="b.app_id">
                  <td><router-link :to="`/apps/${b.app_id}`">{{ b.name || `app #${b.app_id}` }}</router-link></td>
                  <td class="cell-sub">{{ b.reason }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="card mb-4">
          <div class="card-header">
            <h2>Missing workloads</h2>
            <span class="text-muted">{{ confirmed.length }} confirmed</span>
          </div>
          <div v-if="confirmed.length === 0" class="empty-state" style="padding: 24px">
            <p class="text-muted">Everything Miabi expects to be running is running. ✓</p>
          </div>
          <div v-else class="table-wrapper">
            <table>
              <thead>
                <tr><th>Workload</th><th>Type</th><th>Where</th><th>Recommended action</th><th>First seen</th></tr>
              </thead>
              <tbody>
                <tr v-for="f in confirmed" :key="f.ref" :class="{ 'row-breaker': f.breaker_open }">
                  <td class="trunc" :title="f.name">
                    <router-link v-if="subjectLink(f)" :to="subjectLink(f)!">{{ f.name }}</router-link>
                    <template v-else>{{ f.name }}</template>
                    <div v-if="f.blocked_reason" class="cell-sub text-danger">{{ f.blocked_reason }}</div>
                  </td>
                  <td class="cell-sub">{{ kindLabel(f) }}</td>
                  <td class="cell-sub">
                    <template v-if="f.kind === 'service'">cluster #{{ f.cluster_id }}</template>
                    <router-link v-else-if="f.server_id" :to="`/admin/nodes/${f.server_id}`">
                      node #{{ f.server_id }}
                    </router-link>
                    <template v-else>local node</template>
                  </td>
                  <td class="cell-sub">
                    {{ ACTION_LABELS[f.action] ?? f.action }}
                    <span v-if="f.attempts" class="text-muted"> · {{ f.attempts }} attempt(s)</span>
                  </td>
                  <td class="cell-sub" :title="f.first_seen_at">
                    {{ relativeTime(f.first_seen_at, now) }}
                    <div v-if="f.breaker_open" class="cell-sub text-danger">gave up — needs a look</div>
                    <div v-else-if="f.next_attempt_at" class="cell-sub">
                      retry {{ relativeTime(f.next_attempt_at, now) }}
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div v-if="suspected.length" class="card mb-4">
          <div class="card-header">
            <h2>Waiting for a second check</h2>
            <span class="text-muted">{{ suspected.length }} item(s)</span>
          </div>
          <div class="card-body">
            <p class="text-muted" style="font-size: 13px">
              Seen missing once. Nothing is reported or acted on until the next check agrees, so a container
              caught between remove and start during a deploy never counts.
            </p>
            <ul class="plain-list">
              <li v-for="f in suspected" :key="f.ref">
                <span class="mono">{{ f.name }}</span>
                <span class="text-muted"> · {{ kindLabel(f) }}</span>
              </li>
            </ul>
          </div>
        </div>

        <div v-if="status.skipped.length" class="card">
          <div class="card-header">
            <h2>Not checked</h2>
            <span class="text-muted">{{ status.skipped.length }} scope(s)</span>
          </div>
          <div class="card-body">
            <p class="text-muted mb-4" style="font-size: 13px">
              These could not be looked at, so their workloads are unknown rather than missing. An offline
              node is not an empty node.
            </p>
            <ul class="plain-list">
              <li v-for="s in status.skipped" :key="`${s.scope}-${s.id}`">
                <router-link v-if="s.scope === 'node'" :to="`/admin/nodes/${s.id}`">node #{{ s.id }}</router-link>
                <span v-else class="mono">cluster #{{ s.id }}</span>
                <span class="text-muted"> · {{ s.reason }}</span>
              </li>
            </ul>
          </div>
        </div>

        <p class="footnote text-muted">
          Last check {{ relativeTime(status.last_sweep_at!, now) }}
          <template v-if="status.sweep_ms"> · took {{ status.sweep_ms }} ms</template>
        </p>
      </template>
    </template>
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
.mode-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.mode-text {
  margin-left: 10px;
  font-size: 13px;
}
.stats-compact {
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.stats-compact :deep(.stat-card) {
  padding: 12px 14px;
  border-radius: var(--radius);
}
.stats-compact :deep(.stat-value) {
  font-size: 20px;
}
/* Lost data is the one thing on this page nothing can undo. */
.card-danger {
  border-color: var(--danger, #dc2626);
}
.row-breaker td {
  background: color-mix(in srgb, var(--danger, #dc2626) 6%, transparent);
}
.plain-list {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
}
.plain-list li + li {
  margin-top: 4px;
}
.footnote {
  font-size: 12px;
  margin-top: 4px;
}
</style>
