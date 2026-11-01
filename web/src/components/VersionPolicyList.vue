<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { versioningApi, defaultSettings } from '@/api/versioning'
import { pipelineApi } from '@/api/pipelines'
import AppModal from '@/components/AppModal.vue'
import VersioningSettingsForm from '@/components/VersioningSettingsForm.vue'
import { relativeTime } from '@/utils/time'
import type { PipelineDefinition, VersionPolicy, VersionSettings } from '@/api/types'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<VersionPolicy[]>([])
const pipelines = ref<PipelineDefinition[]>([])
const loading = ref(false)

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await versioningApi.list(wid)).data.data ?? []
    pipelines.value = (await pipelineApi.list(wid, 0, 100)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

// A pipeline is already covered when its repository (its app's, or its own) has a policy.
function covered(p: PipelineDefinition) {
  return items.value.some((v) => (p.application_id ? v.application_id === p.application_id : v.pipeline_id === p.id))
}
const uncovered = computed(() => pipelines.value.filter((p) => !covered(p) && (p.application_id || p.git_repository)))

const showCreate = ref(false)
const creating = ref(false)
const createPipeline = ref<number | null>(null)
const createSettings = ref<VersionSettings>(defaultSettings())
function openCreate() {
  createPipeline.value = uncovered.value[0]?.id ?? null
  createSettings.value = defaultSettings()
  showCreate.value = true
}
async function create() {
  const wid = currentWorkspaceId.value
  if (!wid || !createPipeline.value) return
  creating.value = true
  try {
    const p = (await versioningApi.create(wid, createPipeline.value, createSettings.value)).data.data
    notify.success(t('notify.versioning.created'))
    showCreate.value = false
    router.push({ name: 'version-policy', params: { uid: p.uid } })
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

function schemeLabel(p: VersionPolicy) {
  if (p.scheme === 'semver') return `semver · ${p.series}`
  if (p.scheme === 'calver') return `calver · ${p.calver_format}.N`
  return t('versioning.schemes.run')
}
function open(p: VersionPolicy) { router.push({ name: 'version-policy', params: { uid: p.uid } }) }
</script>

<template>
  <div>
    <div class="toolbar">
      <p class="subtitle">{{ $t('versioning.subtitle') }}</p>
      <button v-if="ws.isWorkspaceAdmin && uncovered.length" class="btn btn-secondary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('versioning.enable') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-numeric" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('versioning.emptyTitle') }}</h3>
        <p>{{ $t('versioning.emptyHint') }}</p>
        <button v-if="ws.isWorkspaceAdmin && uncovered.length" class="btn btn-primary mt-4" @click="openCreate">{{ $t('versioning.enable') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr>
            <th>{{ $t('versioning.repository') }}</th><th>{{ $t('versioning.scheme') }}</th>
            <th>{{ $t('versioning.current') }}</th><th>{{ $t('versioning.next') }}</th><th>{{ $t('versioning.updated') }}</th>
          </tr></thead>
          <tbody>
            <tr v-for="p in items" :key="p.id" class="row-link" @click="open(p)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi" :class="p.owner_kind === 'application' ? 'mdi-application-outline' : 'mdi-pipe'" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ p.repository || `#${p.id}` }}
                      <span v-if="p.managed_by" class="chip" :title="$t('versioning.managedHint', { pipeline: p.managed_by })">
                        <span class="mdi mdi-file-code-outline"></span>{{ $t('versioning.fromFile') }}</span>
                    </span>
                    <span class="cell-sub">{{ p.owner_kind === 'application' ? $t('versioning.ownerApp') : $t('versioning.ownerPipeline') }}</span>
                  </span>
                </div>
              </td>
              <td class="mono">{{ schemeLabel(p) }}</td>
              <td><code v-if="p.current" class="tag">{{ p.current }}</code><span v-else class="cell-sub">—</span></td>
              <td><code v-if="p.next" class="tag next">{{ p.next }}</code><span v-else class="cell-sub">run-N</span></td>
              <td class="cell-sub">{{ relativeTime(p.updated_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" dialog-class="modal-lg" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('versioning.enable') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('pipelines.pipeline') }}</label>
              <select v-model="createPipeline" class="form-select" required :aria-label="$t('pipelines.pipeline')">
                <option v-for="p in uncovered" :key="p.id" :value="p.id">{{ p.display_name || p.name }}</option>
              </select>
              <p class="form-hint">{{ $t('versioning.enableHint') }}</p>
            </div>
            <VersioningSettingsForm v-model="createSettings" :pipeline="createPipeline ? String(createPipeline) : undefined" />
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creating || !createPipeline">{{ creating ? $t('action.saving') : $t('action.create') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 12px; }
.subtitle { font-size: 13px; color: var(--text-muted); margin: 0; }
.row-link { cursor: pointer; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.tag { font-family: 'JetBrains Mono', monospace; font-size: 12px; padding: 1px 7px; border-radius: 4px; background: var(--bg-tertiary, rgba(127, 127, 127, 0.12)); }
.tag.next { background: var(--primary-50, rgba(99, 102, 241, 0.12)); color: var(--primary-600, inherit); }
.chip {
  display: inline-flex; align-items: center; gap: 4px; font-size: 11px; font-weight: 500; padding: 1px 7px;
  border-radius: 20px; margin-left: 6px; background: var(--bg-tertiary, rgba(127, 127, 127, 0.12)); color: var(--text-secondary, var(--text-muted));
}
</style>
