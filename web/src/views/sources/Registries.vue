<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { registryApi, type RegistryInput } from '@/api/registries'
import type { Registry } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CredentialSecretField from '@/components/CredentialSecretField.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Registry[]>([])
const loading = ref(false)
const testing = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<Registry | null>(null)
const form = ref<RegistryInput>({ name: '', server: '', username: '', secret: '' })

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await registryApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  editing.value = null
  form.value = { name: '', server: '', username: '', secret: '' }
  showModal.value = true
}
function openEdit(r: Registry) {
  editing.value = r
  form.value = { name: r.name, server: r.server, username: r.username, secret: '' }
  showModal.value = true
}

async function save() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    if (editing.value) {
      await registryApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success(t('notify.registries.updated'))
    } else {
      await registryApi.create(currentWorkspaceId.value, form.value)
      notify.success(t('notify.registries.added'))
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function test(r: Registry) {
  if (!currentWorkspaceId.value) return
  testing.value = r.id
  try {
    await registryApi.test(currentWorkspaceId.value, r.id)
    notify.success(t('notify.registries.authOk', { name: r.name }))
  } catch (e) {
    notify.apiError(e, 'Authentication failed')
  } finally {
    testing.value = null
  }
}

const pendingDelete = ref<Registry | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !pendingDelete.value) return
  deleting.value = true
  try {
    await registryApi.remove(currentWorkspaceId.value, pendingDelete.value.id)
    notify.success(t('notify.registries.deleted'))
    pendingDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('registries.registries') }}</h1>
        <p class="subtitle">{{ $t('registries.subtitle') }}</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('registries.newRegistry') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-database-lock-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('registries.noRegistriesYet') }}</h3>
        <p>{{ $t('registries.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('registries.addARegistry') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('registries.registry') }}</th><th>{{ $t('registries.server') }}</th><th>{{ $t('registries.username') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in items" :key="r.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-database-outline" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ r.display_name || r.name }}</span>
                    <span class="cell-sub">
                      <template v-if="r.secret_ref">
                        <span class="mdi mdi-key-variant"></span> {{ r.secret_ref }}
                      </template>
                      <template v-else>{{ r.has_secret ? 'secret set' : 'no secret' }}</template>
                    </span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ r.server }}</td>
              <td class="cell-sub">{{ r.username || '—' }}</td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" :title="$t('ldap.testConnection')" :aria-label="$t('ldap.testConnection')" :disabled="testing === r.id" @click="test(r)">
                  <span class="mdi" :class="testing === r.id ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(r)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="pendingDelete = r"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit registry' : 'New registry' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. GHCR (prod)" required autofocus :aria-label="$t('apps.form.name')" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('registries.server') }}<span class="text-muted">{{ $t('registries.serverHint') }}</span></label>
              <input v-model="form.server" class="form-input" placeholder="ghcr.io" :aria-label="$t('registries.server')" />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('registries.username') }}</label>
              <input v-model="form.username" class="form-input" placeholder="username" autocomplete="off" :aria-label="$t('registries.username')" />
            </div>
            <CredentialSecretField
              v-model="form.secret"
              label="Password / token"
              :editing="!!editing"
              :current-ref="editing?.secret_ref || ''"
            />
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Add registry') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      :title="$t('confirm.title.deleteRegistry')"
      :message="$t('confirm.message.registries.deleteRegistryNameApps', { name: pendingDelete?.name })"
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
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
