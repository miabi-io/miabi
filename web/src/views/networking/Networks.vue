<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { networkApi } from '@/api/networks'
import type { Network } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Network[]>([])
const loading = ref(false)
const showCreate = ref(false)
const toDelete = ref<Network | null>(null)
const deleting = ref(false)
const saving = ref(false)
const form = ref({ name: '', driver: 'bridge', internal: false })

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await networkApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  form.value = { name: '', driver: 'bridge', internal: false }
  showCreate.value = true
}

async function create() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    await networkApi.create(currentWorkspaceId.value, form.value)
    notify.success('Network created')
    showCreate.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function confirmRemove() {
  if (!currentWorkspaceId.value || !toDelete.value) return
  deleting.value = true
  try {
    await networkApi.remove(currentWorkspaceId.value, toDelete.value.id)
    notify.success('Network deleted')
    toDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e, 'Cannot delete (in use or default)')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Networks</h1>
        <p class="subtitle">Managed Docker networks for {{ ws.contextLabel }}.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New network
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-lan" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No networks</h3>
        <p>Create a network to group and isolate applications.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Create a network</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Network</th><th>Docker name</th><th>Driver</th><th></th></tr></thead>
          <tbody>
            <tr v-for="n in items" :key="n.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-lan" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ n.display_name || n.name }}<span v-if="n.is_default" class="badge badge-neutral" style="margin-left: 8px">default</span></span>
                    <span class="cell-sub"><template v-if="n.internal">internal</template></span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ n.docker_name }}</td>
              <td class="cell-sub">{{ n.driver }}</td>
              <td class="text-right">
                <button v-if="ws.canEdit && !n.is_default" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = n">
                  <span class="mdi mdi-delete-outline"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>New network</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. backend" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">Driver</label>
              <input v-model="form.driver" class="form-input" placeholder="bridge" />
            </div>
            <label class="checkbox-label" style="margin-bottom: 0">
              <input type="checkbox" v-model="form.internal" /> Internal (no external connectivity)
            </label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Creating…' : 'Create network' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete network"
      :message="`Delete network &quot;${toDelete?.name}&quot;? This cannot be undone.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmRemove"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
</style>
