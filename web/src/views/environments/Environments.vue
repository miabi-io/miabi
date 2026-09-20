<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { environmentApi, type EnvironmentInput } from '@/api/environments'
import type { Environment } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Environment[]>([])
const loading = ref(false)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<Environment | null>(null)
const form = ref<EnvironmentInput>(emptyForm())

function emptyForm(): EnvironmentInput {
  return { name: '', description: '', rank: 0, required_approvals: 0 }
}

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await environmentApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  form.value.rank = items.value.length
  showModal.value = true
}
function openEdit(e: Environment) {
  editing.value = e
  form.value = { name: e.name, description: e.description, rank: e.rank, required_approvals: e.required_approvals }
  showModal.value = true
}

async function save() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    if (editing.value) {
      await environmentApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success(t('notify.environments.updated'))
    } else {
      await environmentApi.create(currentWorkspaceId.value, form.value)
      notify.success(t('notify.environments.created'))
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const pendingDelete = ref<Environment | null>(null)
const deleting = ref(false)

async function confirmDelete() {
  if (!currentWorkspaceId.value || !pendingDelete.value) return
  deleting.value = true
  try {
    await environmentApi.remove(currentWorkspaceId.value, pendingDelete.value.id)
    notify.success(t('notify.environments.deleted'))
    pendingDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e2) {
    notify.apiError(e2)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('environments.environments') }}</h1>
        <p class="subtitle">{{ $t('environments.subtitle') }}</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('environments.newEnvironment') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-layers-triple-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('environments.noEnvironmentsYet') }}</h3>
        <p>{{ $t('environments.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('environments.createAnEnvironment') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('environments.environment') }}</th><th>{{ $t('environments.order') }}</th><th>{{ $t('environments.requiredApprovals') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="e in items" :key="e.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-layers-triple-outline" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ e.display_name || e.name }}</span>
                    <span v-if="e.description" class="cell-sub">{{ e.description }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ e.rank }}</td>
              <td>
                <span class="badge" :class="e.required_approvals > 0 ? 'badge-warning' : 'badge-neutral'">
                  <span class="mdi mdi-account-check-outline"></span>
                  {{ e.required_approvals > 0 ? `${e.required_approvals} required` : 'no gate' }}
                </span>
              </td>
              <td class="text-right table-actions">
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(e)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="pendingDelete = e"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit environment' : 'New environment' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" :placeholder="$t('gitops.namePlaceholder')" :aria-label="$t('apps.form.name')" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('plans.description') }}<span class="text-muted">{{ $t('environments.optional') }}</span></label>
              <input v-model="form.description" class="form-input" placeholder="Customer-facing production stage" :aria-label="$t('plans.description')" />
            </div>
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">{{ $t('environments.order') }}</label>
                <input v-model.number="form.rank" type="number" min="0" class="form-input" :aria-label="$t('environments.order')" />
                <p class="hint">{{ $t('environments.orderHint') }}</p>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('environments.requiredApprovals') }}</label>
                <input v-model.number="form.required_approvals" type="number" min="0" class="form-input" :aria-label="$t('environments.requiredApprovals')" />
                <p class="hint">{{ $t('environments.approvalsHint') }}</p>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Create') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      :title="$t('confirm.title.deleteEnvironment')"
      :message="pendingDelete ? $t('confirm.message.environments.deleteEnvironmentName', { name: pendingDelete.name }) : ''"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.badge .mdi { font-size: 13px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.hint { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
</style>
