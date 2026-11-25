<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { networkApi } from '@/api/networks'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { NetworkDetail } from '@/api/types'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const networkId = computed(() => Number(route.params.id))
const detail = ref<NetworkDetail | null>(null)
const loading = ref(false)
const anyDrifted = computed(() => detail.value?.nodes.some((n) => n.drifted) ?? false)
const attachedCount = computed(() => (detail.value ? detail.value.apps.length + detail.value.databases.length : 0))

// Presence is read from each node's engine, so this is fetched on open rather than carried in the list.
async function load() {
  const wid = currentWorkspaceId.value
  if (!wid || !networkId.value) return
  loading.value = true
  try {
    detail.value = (await networkApi.get(wid, networkId.value)).data.data
  } catch (e) {
    notify.apiError(e)
    void router.push('/networks')
  } finally {
    loading.value = false
  }
}
watch([currentWorkspaceId, networkId], load, { immediate: true })

const showDelete = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !detail.value) return
  deleting.value = true
  try {
    await networkApi.remove(currentWorkspaceId.value, detail.value.id)
    notify.success(t('notify.networks.deleted'))
    void router.push('/networks')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
    showDelete.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" :title="$t('networks.back')" :aria-label="$t('networks.back')" @click="router.push('/networks')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ detail?.display_name || detail?.name || $t('networks.network') }}</h1>
          <span v-if="detail" class="cell-sub mono">{{ detail.docker_name }}</span>
        </div>
        <span v-if="detail?.is_default" class="badge badge-neutral">{{ $t('networks.default') }}</span>
      </div>
      <div class="flex items-center gap-2">
        <button class="btn btn-secondary" :disabled="loading" @click="load"><span class="mdi mdi-refresh"></span></button>
        <button
          v-if="ws.canEdit && detail && !detail.is_default"
          class="btn btn-danger"
          :disabled="attachedCount > 0"
          :title="attachedCount > 0 ? $t('networks.detachBeforeDelete') : ''"
          @click="showDelete = true"
        ><span class="mdi mdi-delete-outline"></span>{{ $t('action.delete') }}</button>
      </div>
    </div>

    <div v-if="loading && !detail" class="card"><div class="card-body"><span class="spinner"></span>{{ $t('networks.readingTheNetwork') }}</div></div>

    <template v-else-if="detail">
      <div class="grid-2 mb-4">
        <div class="card">
          <div class="card-header"><h3>{{ $t('networks.addressing') }}</h3></div>
          <div class="card-body">
            <div class="detail-grid">
              <span class="text-muted">{{ $t('networks.reach') }}</span>
              <span class="text-sm">
                {{ detail.driver === 'overlay' ? $t('networks.reachLocation') : $t('networks.reachNode') }}<span v-if="detail.internal">{{ $t('appDetail.net.internal') }}</span>
              </span>
              <template v-if="detail.subnet">
                <span class="text-muted">{{ $t('networks.ipv4Subnet') }}</span>
                <span>
                  <span class="mono text-sm">{{ detail.subnet }}</span>
                  <div v-if="detail.pool_managed" class="cell-sub">{{ $t('networks.poolSubnetNote') }}</div>
                </span>
                <span class="text-muted">{{ $t('networks.ipv4Gateway') }}</span>
                <span class="mono text-sm">{{ detail.gateway || '—' }}</span>
              </template>
              <template v-if="detail.exists">
                <span class="text-muted">{{ $t('networks.ipv6') }}</span>
                <span>
                  <span class="badge" :class="detail.enable_ipv6 ? 'badge-success' : 'badge-neutral'">
                    {{ detail.enable_ipv6 ? $t('networks.enabled') : $t('networks.disabled') }}
                  </span>
                </span>
                <template v-if="detail.enable_ipv6">
                  <span class="text-muted">{{ $t('networks.ipv6Subnet') }}</span>
                  <span class="mono text-sm">{{ detail.ipv6_subnet || '—' }}</span>
                  <span class="text-muted">{{ $t('networks.ipv6Gateway') }}</span>
                  <span class="mono text-sm">{{ detail.ipv6_gateway || '—' }}</span>
                </template>
              </template>
            </div>
            <!-- An overlay exists on a node only once one of its containers attaches, so absence is not an error. -->
            <p v-if="!detail.exists" class="form-hint text-muted" style="margin-top: 10px">
              {{ $t('networks.notOnAnyNode') }} <template v-if="detail.driver === 'overlay'">{{ $t('networks.anOverlayAppearsOnA') }}</template>
            </p>
          </div>
        </div>

        <div class="card">
          <div class="card-header"><h3>{{ $t('networks.whereItExists') }}</h3></div>
          <div class="card-body">
            <ul class="node-list">
              <li v-for="n in detail.nodes" :key="n.node_id">
                <span>{{ n.node_name }}</span>
                <span v-if="!n.reachable" class="badge badge-neutral">{{ $t('networks.nodeUnreachable') }}</span>
                <span v-else-if="n.drifted" class="badge badge-warning">{{ $t('networks.nodeDrifted', { subnet: n.subnet }) }}</span>
                <span v-else-if="n.exists" class="badge badge-success badge-dot">{{ $t('networks.nodePresent') }}</span>
                <span v-else class="cell-sub">{{ $t('networks.nodeNotYet') }}</span>
              </li>
            </ul>
            <p v-if="anyDrifted" class="form-hint text-warning" style="margin-top: 10px">{{ $t('networks.driftedHint') }}</p>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h3>{{ $t('networks.attached') }}</h3></div>
        <div v-if="!attachedCount" class="card-body cell-sub">{{ $t('networks.noneAttached') }}</div>
        <div v-else class="table-wrapper">
          <table>
            <tbody>
              <tr v-for="a in detail.apps" :key="'a' + a.id" class="row-clickable" @click="router.push(`/apps/${a.id}`)">
                <td><span class="mdi mdi-application-outline"></span> {{ a.label }}</td>
                <td class="cell-sub">{{ $t('networks.attachedApps') }}</td>
              </tr>
              <tr v-for="d in detail.databases" :key="'d' + d.id" class="row-clickable" @click="router.push(`/databases/${d.id}`)">
                <td><span class="mdi mdi-database-outline"></span> {{ d.label }}</td>
                <td class="cell-sub">{{ $t('networks.attachedDatabases') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="showDelete"
      :title="$t('confirm.title.deleteNetwork')"
      :message="$t('confirm.message.networks.deleteNetworkNameThis', { name: detail?.name })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="showDelete = false"
    />
  </div>
</template>

<style scoped>
.detail-grid { display: grid; grid-template-columns: max-content 1fr; gap: 10px 16px; align-items: center; }
.node-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.node-list li { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 14px; }
</style>
