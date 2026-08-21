<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { releaseApi } from '@/api/releases'
import { environmentApi } from '@/api/environments'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import type { WorkspaceRelease, Environment, EnvApproval } from '@/api/types'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<WorkspaceRelease[]>([])
const environments = ref<Environment[]>([])
const loading = ref(false)

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    const res = await releaseApi.list(id, page, pageable.value.size)
    items.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})
function reload() { goToPage(pageable.value.current_page) }

// Promotion targets — loaded per workspace, independent of paging.
async function loadEnvironments(id: number | null) {
  if (!id) { environments.value = []; return }
  try {
    environments.value = (await environmentApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  }
}
watch(currentWorkspaceId, (id) => { loadEnvironments(id); goToPage(0) }, { immediate: true })

// Promote modal
const showModal = ref(false)
const target = ref<WorkspaceRelease | null>(null)
const selectedEnv = ref<number | null>(null)
const envStatus = ref<EnvApproval[]>([])
const statusLoading = ref(false)
const working = ref(false)

function openPromote(r: WorkspaceRelease) {
  target.value = r
  selectedEnv.value = environments.value[0]?.id ?? null
  envStatus.value = []
  showModal.value = true
  refreshStatus()
}

async function refreshStatus() {
  if (!currentWorkspaceId.value || !target.value) return
  statusLoading.value = true
  try {
    const s = await releaseApi.approvals(currentWorkspaceId.value, target.value.id)
    envStatus.value = s.data.data.environments ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    statusLoading.value = false
  }
}

const currentStatus = computed<EnvApproval | null>(() =>
  envStatus.value.find((e) => e.environment_id === selectedEnv.value) ?? null,
)
const canPromote = computed(() => {
  const s = currentStatus.value
  if (!selectedEnv.value) return false
  if (!s) return true
  return s.required_approvals === 0 || s.satisfied
})

async function approve() {
  if (!currentWorkspaceId.value || !target.value || !selectedEnv.value) return
  working.value = true
  try {
    await releaseApi.approve(currentWorkspaceId.value, target.value.id, { environment_id: selectedEnv.value, approved: true })
    notify.success('Approval recorded')
    await refreshStatus()
  } catch (e) {
    notify.apiError(e)
  } finally {
    working.value = false
  }
}

async function promote() {
  if (!currentWorkspaceId.value || !target.value || !selectedEnv.value) return
  working.value = true
  try {
    await releaseApi.promote(currentWorkspaceId.value, target.value.id, selectedEnv.value)
    notify.success(`${target.value.application_name} promoted`)
    showModal.value = false
    reload()
  } catch (e) {
    notify.apiError(e, 'Promotion failed')
  } finally {
    working.value = false
  }
}

function short(s?: string) { return s ? s.replace(/^sha256:/, '').slice(0, 12) : '' }
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Releases</h1>
        <p class="subtitle">Promotable release artifacts — promote across environments with approval gates.</p>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-tag-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No releases yet</h3>
        <p>Every successful deployment records a release here, ready to promote.</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Application</th><th>Version</th><th>Image</th><th>Provenance</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in items" :key="r.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-tag-outline" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ r.application_name }}</span>
                    <span class="cell-sub">{{ new Date(r.created_at).toLocaleString() }}</span>
                  </span>
                </div>
              </td>
              <td>
                v{{ r.version }}
                <span v-if="r.active" class="badge badge-success" style="margin-left:6px">active</span>
                <span v-if="r.pinned" class="badge badge-neutral" style="margin-left:4px">pinned</span>
              </td>
              <td class="cell-sub mono">{{ r.image }}</td>
              <td class="cell-sub mono">
                <span v-if="r.digest" title="digest">@{{ short(r.digest) }}</span>
                <span v-if="r.commit" title="commit"> · {{ r.commit.slice(0, 7) }}</span>
                <span v-if="!r.digest && !r.commit">—</span>
              </td>
              <td class="text-right">
                <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" :disabled="environments.length === 0" @click="openPromote(r)">
                  <span class="mdi mdi-rocket-launch-outline"></span> Promote
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <p v-if="environments.length === 0 && items.length > 0" class="hint-block">
      Create an <router-link to="/environments">environment</router-link> to enable promotion.
    </p>

    <Teleport to="body">
      <div v-if="showModal" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>Promote {{ target?.application_name }} v{{ target?.version }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Target environment</label>
              <select v-model="selectedEnv" class="form-select">
                <option v-for="e in environments" :key="e.id" :value="e.id">{{ e.name }}</option>
              </select>
            </div>

            <div v-if="statusLoading" class="card-body"><span class="spinner"></span></div>
            <div v-else-if="currentStatus" class="gate" :class="canPromote ? 'gate-ok' : 'gate-block'">
              <span class="mdi" :class="canPromote ? 'mdi-lock-open-check-outline' : 'mdi-lock-outline'"></span>
              <span>
                <strong>{{ currentStatus.approvals }}/{{ currentStatus.required_approvals }}</strong>
                approvals
                <template v-if="currentStatus.required_approvals === 0"> — no approval required</template>
                <template v-else-if="canPromote"> — gate satisfied</template>
                <template v-else> — needs {{ currentStatus.required_approvals - currentStatus.approvals }} more</template>
              </span>
            </div>

            <p class="note">Promotion re-points <strong>{{ target?.application_name }}</strong> at this release and deploys it.</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" :disabled="working || !selectedEnv" @click="approve">
              <span class="mdi mdi-account-check-outline"></span> Approve
            </button>
            <button type="button" class="btn btn-primary" :disabled="working || !canPromote" @click="promote">
              <span class="mdi mdi-rocket-launch-outline"></span> Promote
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.btn-sm { padding: 4px 10px; font-size: 12px; }
.hint-block { font-size: 13px; color: var(--text-muted); margin-top: 12px; }
.note { font-size: 12px; color: var(--text-muted); margin-top: 12px; }
.gate { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-radius: 8px; font-size: 13px; margin-top: 4px; }
.gate .mdi { font-size: 18px; }
.gate-ok { background: var(--success-50); color: var(--success-600); }
.gate-block { background: var(--warning-50); color: var(--warning-600); }
</style>
