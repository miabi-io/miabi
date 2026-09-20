<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { runnerApi, adminRunnerApi, type Runner } from '@/api/runners'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const { currentWorkspaceId } = storeToRefs(ws)
const { t } = useI18n()
const notify = useNotificationStore()

const isAdmin = computed(() => !!route.meta.admin)
const rid = computed(() => Number(route.params.id))
const listPath = computed(() => (isAdmin.value ? '/admin/runners' : '/runners'))
// Managing an existing shared runner (cordon/token/delete) is available to any
// platform admin — the license only caps how many shared runners may be
// registered, not managing the ones that exist. Workspace runners use the role.
const canEdit = computed(() => (isAdmin.value ? true : ws.canEdit))

const runner = ref<Runner | null>(null)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    runner.value = isAdmin.value
      ? (await adminRunnerApi.get(rid.value)).data.data
      : (await runnerApi.get(currentWorkspaceId.value ?? 0, rid.value)).data.data
  } catch (e) {
    notify.apiError(e)
    router.replace(listPath.value)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// --- display helpers ---
function fmtDateTime(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}
function ago(s?: string | null): string {
  if (!s) return ''
  const d = Date.now() - new Date(s).getTime()
  if (d < 60_000) return 'just now'
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m ago`
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}h ago`
  return `${Math.floor(d / 86_400_000)}d ago`
}
const platform = computed(() => {
  const r = runner.value
  if (!r) return '—'
  return [r.os, r.arch].filter(Boolean).join('/') || '—'
})
const statusBadge = computed(() => {
  const r = runner.value
  if (!r) return { text: '—', cls: 'badge-neutral' }
  if (!r.enabled) return { text: 'disabled', cls: 'badge-danger' }
  if (r.cordoned) return { text: 'cordoned', cls: 'badge-warning' }
  if (r.connected || r.status === 'online') return { text: 'online', cls: 'badge-success badge-dot' }
  return { text: 'offline', cls: 'badge-neutral' }
})

// --- actions ---
const busy = ref(false)
async function toggleCordon() {
  const r = runner.value
  if (!r) return
  busy.value = true
  try {
    const updated = isAdmin.value
      ? (await adminRunnerApi.cordon(r.id, !r.cordoned)).data.data
      : (await runnerApi.cordon(currentWorkspaceId.value ?? 0, r.id, !r.cordoned)).data.data
    runner.value = updated
    notify.success(r.cordoned ? 'Runner resumed' : 'Runner cordoned')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

const confirmingDelete = ref(false)
const deleting = ref(false)
async function remove() {
  const r = runner.value
  if (!r) return
  deleting.value = true
  try {
    if (isAdmin.value) await adminRunnerApi.remove(r.id)
    else await runnerApi.remove(currentWorkspaceId.value ?? 0, r.id)
    notify.success(t('notify.runners.deleted'))
    router.replace(listPath.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
    confirmingDelete.value = false
  }
}
</script>

<template>
  <div>
    <div v-if="loading && !runner" class="loading-page"><span class="spinner"></span></div>

    <template v-else-if="runner">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" :title="$t('runners.backToRunners')" :aria-label="$t('runners.backToRunners')" @click="router.push(listPath)">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ runner.display_name || runner.name }}
              <span class="badge" :class="statusBadge.cls">{{ statusBadge.text }}</span>
            </h1>
            <span class="subline">{{ runner.name }} · {{ runner.scope === 'shared' ? 'platform-shared' : 'workspace' }} runner</span>
          </div>
        </div>
        <div v-if="canEdit" class="header-actions">
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleCordon">
            {{ runner.cordoned ? 'Resume' : 'Cordon' }}
          </button>
          <button class="btn btn-danger btn-sm" @click="confirmingDelete = true">{{ $t('action.delete') }}</button>
        </div>
      </div>

      <div class="card">
        <div class="card-body">
          <dl class="detail-grid">
            <div><dt>{{ $t('dashboard.col.status') }}</dt><dd><span class="badge" :class="statusBadge.cls">{{ statusBadge.text }}</span></dd></div>
            <div><dt>{{ $t('runners.remoteIp') }}</dt><dd><code v-if="runner.remote_ip">{{ runner.remote_ip }}</code><span v-else class="text-muted">—</span></dd></div>
            <div>
              <dt>{{ $t('runners.lastConnection') }}</dt>
              <dd>{{ fmtDateTime(runner.last_seen_at) }} <span v-if="runner.last_seen_at" class="text-muted">({{ ago(runner.last_seen_at) }})</span></dd>
            </div>
            <div><dt>{{ $t('dashboard.col.created') }}</dt><dd>{{ fmtDateTime(runner.created_at) }}</dd></div>
            <div><dt>{{ $t('runners.platform') }}</dt><dd>{{ platform }}</dd></div>
            <div><dt>{{ $t('runners.version') }}</dt><dd>{{ runner.version || '—' }}</dd></div>
            <div><dt>{{ $t('runners.concurrency') }}</dt><dd>{{ runner.concurrency }} job(s)</dd></div>
            <div><dt>{{ $t('jobs.enabled') }}</dt><dd>{{ runner.enabled ? 'Yes' : 'No' }}</dd></div>
            <div class="detail-wide">
              <dt>{{ $t('runners.labels') }}</dt>
              <dd>
                <template v-if="runner.labels && runner.labels.length">
                  <span v-for="l in runner.labels" :key="l" class="badge badge-neutral" style="margin-right: 4px">{{ l }}</span>
                </template>
                <span v-else class="text-muted">{{ $t('runners.none') }}</span>
              </dd>
            </div>
          </dl>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="confirmingDelete"
      :title="$t('confirm.title.deleteRunner')"
      :message="runner ? $t('confirm.message.runnerDetail.deleteRunnerNameIts', { name: runner.name }) : ''"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="remove"
      @cancel="confirmingDelete = false"
    />
  </div>
</template>

<style scoped>
.page-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 20px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-title h1 { display: flex; align-items: center; gap: 10px; margin: 0; }
.subline { color: var(--text-secondary); font-size: 13px; }
.header-actions { display: flex; gap: 8px; }
.detail-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 18px 32px; margin: 0; }
.detail-grid dt { color: var(--text-muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 4px; }
.detail-grid dd { margin: 0; font-size: 14px; }
.detail-wide { grid-column: 1 / -1; }
</style>
