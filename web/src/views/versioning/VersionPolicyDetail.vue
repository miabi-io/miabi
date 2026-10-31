<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { versioningApi, settingsOf } from '@/api/versioning'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import VersioningSettingsForm from '@/components/VersioningSettingsForm.vue'
import { relativeTime } from '@/utils/time'
import { fmtDateTime } from '@/utils/datetime'
import type { VersionAllocation, VersionPolicy, VersionSettings } from '@/api/types'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const route = useRoute()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const uid = computed(() => String(route.params.uid))
const policy = ref<VersionPolicy | null>(null)
const settings = ref<VersionSettings | null>(null)
const allocations = ref<VersionAllocation[]>([])
const saving = ref(false)

// Settings the file owns are shown, never edited here.
const editable = computed(() => ws.isWorkspaceAdmin && !policy.value?.managed_by)
const dirty = computed(() => !!policy.value && !!settings.value && JSON.stringify(settingsOf(policy.value)) !== JSON.stringify(settings.value))

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid) return
  try {
    policy.value = (await versioningApi.get(wid, uid.value)).data.data
    settings.value = settingsOf(policy.value)
  } catch (e) {
    notify.apiError(e)
  }
}

const { pageable, goToPage } = usePagination(async (page) => {
  const wid = currentWorkspaceId.value
  if (!wid) return
  try {
    const res = await versioningApi.allocations(wid, uid.value, page, pageable.value.size)
    allocations.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  }
})
watch([currentWorkspaceId, uid], () => { load(); goToPage(0) }, { immediate: true })

async function save() {
  const wid = currentWorkspaceId.value
  if (!wid || !settings.value) return
  saving.value = true
  try {
    policy.value = (await versioningApi.update(wid, uid.value, settings.value)).data.data
    settings.value = settingsOf(policy.value)
    notify.success(t('notify.versioning.updated'))
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

// Switching series is its own action rather than a settings edit: it is the decision to start a new
// version line (2.x.x), or to ship a hotfix on an old one, and it is audited as such.
const showSwitch = ref(false)
const nextSeries = ref('')
const switching = ref(false)
function openSwitch() {
  const cur = policy.value?.series ?? '1.x.x'
  const major = Number(cur.split('.')[0])
  nextSeries.value = Number.isFinite(major) ? `${major + 1}.x.x` : cur
  showSwitch.value = true
}
async function switchSeries() {
  const wid = currentWorkspaceId.value
  if (!wid) return
  switching.value = true
  try {
    policy.value = (await versioningApi.switchSeries(wid, uid.value, nextSeries.value)).data.data
    settings.value = settingsOf(policy.value)
    notify.success(t('notify.versioning.seriesSwitched', { series: policy.value.series, next: policy.value.next }))
    showSwitch.value = false
  } catch (e) {
    notify.apiError(e)
  } finally {
    switching.value = false
  }
}

const confirmDelete = ref(false)
const deleting = ref(false)
async function remove() {
  const wid = currentWorkspaceId.value
  if (!wid) return
  deleting.value = true
  try {
    await versioningApi.remove(wid, uid.value)
    notify.success(t('notify.versioning.deleted'))
    router.push({ name: 'versioning' })
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
    confirmDelete.value = false
  }
}

const statusBadge: Record<string, string> = { reserved: 'badge-info', published: 'badge-success', abandoned: 'badge-neutral' }
function openRun(a: VersionAllocation) {
  if (a.pipeline_id && a.pipeline_run_id) router.push({ name: 'pipeline-run', params: { id: a.pipeline_id, runId: a.pipeline_run_id } })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link :to="{ name: 'versioning' }" class="back-link"><span class="mdi mdi-arrow-left"></span>{{ $t('versioning.title') }}</router-link>
        <h1>{{ policy?.repository || $t('versioning.policy') }}</h1>
        <p v-if="policy" class="subtitle">
          {{ policy.owner_kind === 'application' ? $t('versioning.ownerApp') : $t('versioning.ownerPipeline') }}
          <span v-if="policy.managed_by"> · {{ $t('versioning.managedHint', { pipeline: policy.managed_by }) }}</span>
        </p>
      </div>
      <div v-if="policy && editable" class="header-actions">
        <button v-if="policy.scheme === 'semver'" class="btn btn-secondary" @click="openSwitch">
          <span class="mdi mdi-source-branch-plus"></span>{{ $t('versioning.switchSeries') }}</button>
        <button class="btn btn-secondary danger-text" @click="confirmDelete = true"><span class="mdi mdi-delete-outline"></span>{{ $t('action.delete') }}</button>
      </div>
    </div>

    <div v-if="policy" class="stats">
      <div class="stat card">
        <span class="stat-label">{{ $t('versioning.current') }}</span>
        <code class="stat-val">{{ policy.current || '—' }}</code>
      </div>
      <div class="stat card">
        <span class="stat-label">{{ $t('versioning.next') }}</span>
        <code class="stat-val next">{{ policy.next || 'run-N' }}</code>
      </div>
      <div class="stat card">
        <span class="stat-label">{{ $t('versioning.series') }}</span>
        <code class="stat-val">{{ policy.scheme === 'semver' ? policy.series : policy.scheme === 'calver' ? `${policy.calver_format}.N` : '—' }}</code>
      </div>
    </div>

    <div v-if="settings" class="card mt-4">
      <div class="card-header"><h3>{{ $t('versioning.settings') }}</h3></div>
      <div class="card-body">
        <div v-if="policy?.managed_by" class="notice">
          <span class="mdi mdi-file-code-outline"></span>
          <i18n-t keypath="versioning.managedNotice" tag="span">
            <template #pipeline><strong>{{ policy.managed_by }}</strong></template>
            <template #block><code>versioning:</code></template>
          </i18n-t>
        </div>
        <VersioningSettingsForm v-model="settings" :readonly="!editable" :policy="uid" />
        <div v-if="editable" class="form-actions">
          <button class="btn btn-secondary" :disabled="!dirty || saving" @click="policy && (settings = settingsOf(policy))">{{ $t('action.cancel') }}</button>
          <button class="btn btn-primary" :disabled="!dirty || saving" @click="save">{{ saving ? $t('action.saving') : $t('action.save') }}</button>
        </div>
      </div>
    </div>

    <div class="card mt-4">
      <div class="card-header"><h3>{{ $t('versioning.history') }}</h3></div>
      <div v-if="allocations.length === 0" class="card-body cell-sub">{{ $t('versioning.noHistory') }}</div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr>
            <th>{{ $t('versioning.tag') }}</th><th>{{ $t('dashboard.col.status') }}</th><th>{{ $t('pipelines.run') }}</th>
            <th>{{ $t('pipelines.branch') }}</th><th>{{ $t('pipelines.digest') }}</th><th>{{ $t('versioning.when') }}</th>
          </tr></thead>
          <tbody>
            <tr v-for="a in allocations" :key="a.id" :class="{ 'row-link': a.pipeline_id }" @click="openRun(a)">
              <td>
                <code class="tag" :class="{ pre: a.prerelease }">{{ a.tag }}</code>
                <span v-if="a.exact" class="chip">{{ $t('versioning.setByHand') }}</span>
              </td>
              <td>
                <span class="badge" :class="statusBadge[a.status]" :title="a.note">{{ $t(`versioning.status.${a.status}`) }}</span>
              </td>
              <td class="cell-sub">{{ a.run_number ? `#${a.run_number}` : '—' }}</td>
              <td class="cell-sub mono">{{ a.branch || '—' }}</td>
              <td class="cell-sub mono" :title="a.digest">{{ a.digest ? a.digest.replace('sha256:', '').slice(0, 12) : '—' }}</td>
              <td class="cell-sub" :title="fmtDateTime(a.created_at)">{{ relativeTime(a.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <Pagination :pageable="pageable" @page="goToPage" />

    <ConfirmDialog
      :open="showSwitch"
      :title="$t('versioning.switchSeries')"
      :message="$t('versioning.switchSeriesHint', { current: policy?.series })"
      :confirm-label="$t('versioning.switch')"
      :busy="switching"
      @confirm="switchSeries"
      @cancel="showSwitch = false"
    >
      <input v-model="nextSeries" class="form-input mono switch-input" placeholder="2.x.x" :aria-label="$t('versioning.series')" />
    </ConfirmDialog>

    <ConfirmDialog
      :open="confirmDelete"
      :title="$t('versioning.deleteTitle')"
      :message="$t('versioning.deleteMessage', { repository: policy?.repository })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="remove"
      @cancel="confirmDelete = false"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.back-link { font-size: 13px; color: var(--text-muted); text-decoration: none; display: inline-flex; align-items: center; gap: 4px; }
.back-link:hover { color: var(--primary-500); }
.header-actions { display: flex; gap: 8px; flex-wrap: wrap; }
.stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
@media (max-width: 640px) { .stats { grid-template-columns: 1fr; } }
.stat { padding: 14px 16px; display: flex; flex-direction: column; gap: 4px; }
.stat-label { font-size: 12px; color: var(--text-muted); }
.stat-val { font-family: 'JetBrains Mono', monospace; font-size: 18px; font-weight: 600; background: none; }
.stat-val.next { color: var(--primary-600, inherit); }
.form-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
.notice {
  display: flex; gap: 8px; align-items: flex-start; margin-bottom: 14px; padding: 10px 12px; font-size: 13px;
  border: 1px solid var(--border); border-radius: 8px; background: var(--bg-tertiary, rgba(127, 127, 127, 0.08));
}
.notice code { font-size: 12px; }
.row-link { cursor: pointer; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.tag { font-family: 'JetBrains Mono', monospace; font-size: 12px; padding: 1px 7px; border-radius: 4px; background: var(--bg-tertiary, rgba(127, 127, 127, 0.12)); }
.tag.pre { opacity: 0.8; font-style: italic; }
.chip { font-size: 11px; margin-left: 6px; color: var(--text-muted); }
.switch-input { margin-top: 10px; width: 100%; }
.danger-text { color: var(--danger-600, #dc2626); }
</style>
