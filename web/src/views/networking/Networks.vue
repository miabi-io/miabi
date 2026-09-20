<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { networkApi } from '@/api/networks'
import type { Network } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'
import NetworkDetailModal from '@/components/NetworkDetailModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Network[]>([])
const loading = ref(false)
const showCreate = ref(false)
const toDelete = ref<Network | null>(null)
const deleting = ref(false)
const saving = ref(false)
const form = ref({ name: '', driver: 'bridge', internal: false })

// The detail modal reads the addressing itself; this page only chooses which network to show.
const detailFor = ref<Network | null>(null)

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
    notify.success(t('notify.networks.created'))
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
    notify.success(t('notify.networks.deleted'))
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
        <h1>{{ $t('networks.networks') }}</h1>
        <p class="subtitle">Managed Docker networks for {{ ws.contextLabel }}.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('networks.newNetwork') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-lan" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('networks.noNetworks') }}</h3>
        <p>{{ $t('networks.createANetworkToGroup') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('networks.createANetwork') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('networks.network') }}</th><th>{{ $t('db.dockerName') }}</th><th>{{ $t('networks.driver') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="n in items" :key="n.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-lan" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ n.display_name || n.name }}<span v-if="n.is_default" class="badge badge-neutral" style="margin-left: 8px">{{ $t('networks.default') }}</span></span>
                    <span class="cell-sub"><template v-if="n.internal">{{ $t('networks.internal') }}</template></span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ n.docker_name }}</td>
              <td class="cell-sub">{{ n.driver }}</td>
              <td class="text-right">
                <button class="btn-icon btn-icon-sm btn-icon-accent" :title="$t('appDetail.networkDetails')" :aria-label="$t('appDetail.networkDetails')" @click="detailFor = n">
                  <span class="mdi mdi-ip-network-outline"></span>
                </button>
                <button v-if="ws.canEdit && !n.is_default" class="btn-icon btn-icon-sm btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = n">
                  <span class="mdi mdi-delete-outline"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <NetworkDetailModal
        v-if="detailFor"
        :workspace-id="currentWorkspaceId"
        :network="detailFor"
        @close="detailFor = null"
      />

      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('networks.newNetwork') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" placeholder="e.g. backend" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('networks.driver') }}</label>
              <input v-model="form.driver" class="form-input" :placeholder="$t('networks.bridge')" />
            </div>
            <label class="checkbox-label" style="margin-bottom: 0">
              <input type="checkbox" v-model="form.internal" />{{ $t('networks.internalNoExternalConnectivity') }}</label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Creating…' : 'Create network' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      :title="$t('confirm.title.deleteNetwork')"
      :message="$t('confirm.message.networks.deleteNetworkNameThis', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')"
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
