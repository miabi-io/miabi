<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { memberApi } from '@/api/resources'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import type { AuditLog } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const logs = ref<AuditLog[]>([])
const loading = ref(false)
const denied = ref(false)
const locked = ref(false)
const order = ref<'desc' | 'asc'>('desc')
const from = ref('')
const to = ref('')

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  denied.value = false
  locked.value = false
  if (!id) {
    logs.value = []
    return
  }
  loading.value = true
  try {
    const res = await memberApi.auditLogs(id, page, pageable.value.size, order.value, from.value, to.value)
    logs.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e: unknown) {
    // Audit log requires an Enterprise license (402) and owner/admin access (403);
    // show a friendly notice for each, otherwise surface the error.
    const status = (e as { response?: { status?: number } })?.response?.status
    if (status === 402) locked.value = true
    else if (status === 403) denied.value = true
    else notify.apiError(e)
    logs.value = []
  } finally {
    loading.value = false
  }
})

// Reset to the first page when the workspace, sort order, or date range changes.
watch(currentWorkspaceId, () => goToPage(0))
watch([order, from, to], () => goToPage(0))

function clearRange() {
  from.value = ''
  to.value = ''
}

function actionBadge(action: string): string {
  if (action.includes('delete') || action.includes('remove')) return 'badge-danger'
  if (action.includes('create') || action.includes('invite')) return 'badge-success'
  if (action.includes('update') || action.includes('role') || action.includes('set')) return 'badge-warning'
  return 'badge-neutral'
}

function when(ts: string): string {
  return new Date(ts).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('nav.workspace.auditLog') }}</h1>
        <p class="subtitle">Activity in {{ ws.contextLabel }}</p>
      </div>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="goToPage(pageable.current_page)">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>{{ $t('volumes.refresh') }}</button>
    </div>

    <div class="toolbar">
      <label class="range-field">{{ $t('audit.from') }}<input v-model="from" type="date" class="form-input" /></label>
      <label class="range-field">{{ $t('audit.to') }}<input v-model="to" type="date" class="form-input" /></label>
      <button v-if="from || to" class="btn btn-ghost btn-sm" @click="clearRange">{{ $t('audit.clear') }}</button>
      <select v-model="order" class="order-select form-select " :title="$t('audit.sortOrder')" :aria-label="$t('audit.sortOrder')">
        <option value="desc">{{ $t('audit.recentFirst') }}</option>
        <option value="asc">{{ $t('audit.oldestFirst') }}</option>
      </select>
    </div>

    <div class="card">
      <div v-if="loading && logs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="locked" class="empty-state">
        <span class="mdi mdi-shield-star-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('audit.anEnterpriseFeature') }}</h3>
        <p>{{ $t('audit.theAuditLogIsAvailable') }}</p>
      </div>
      <div v-else-if="denied" class="empty-state">
        <span class="mdi mdi-lock-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('audit.adminsOnly') }}</h3>
        <p>{{ $t('audit.youNeedOwnerOrAdmin') }}</p>
      </div>
      <div v-else-if="logs.length === 0" class="empty-state">
        <span class="mdi mdi-history" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('audit.noActivityYet') }}</h3>
        <p>{{ $t('audit.mutationsInThisWorkspaceWill') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>{{ $t('audit.action') }}</th><th>{{ $t('audit.target') }}</th><th>{{ $t('audit.ip') }}</th><th class="text-right">{{ $t('audit.when') }}</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in logs" :key="a.id" class="row-clickable" @click="router.push(`/audit-log/${a.id}`)">
              <td><span class="badge" :class="actionBadge(a.action)">{{ a.action }}</span></td>
              <td class="cell-sub">{{ a.target_type }}<template v-if="a.target_id"> #{{ a.target_id }}</template></td>
              <td class="cell-sub">{{ a.ip_address || '—' }}</td>
              <td class="text-right cell-sub">{{ when(a.created_at) }}</td>
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
.toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
.range-field { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); }
.range-field .form-input { width: auto; }
.order-select { min-width: 150px; margin-left: auto; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
