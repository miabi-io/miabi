<script setup lang="ts">
import { ref, watch } from 'vue'
import { networkApi } from '@/api/networks'
import type { Network, NetworkDetail } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import AppModal from '@/components/AppModal.vue'

// The stored row says what Miabi asked for; the addressing comes from the engine, so it is fetched
// when the modal opens rather than carried in the list.
const props = defineProps<{ workspaceId: number | null; network: Network | null }>()
const emit = defineEmits<{ close: [] }>()

const notify = useNotificationStore()
const detail = ref<NetworkDetail | null>(null)
const loading = ref(false)

watch(
  () => [props.workspaceId, props.network?.id] as const,
  async ([ws, id]) => {
    if (!ws || !id) return
    detail.value = null
    loading.value = true
    try {
      detail.value = (await networkApi.get(ws, id)).data.data
    } catch (e) {
      notify.apiError(e)
      emit('close')
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)
</script>

<template>
  <AppModal @close="emit('close')">
    <div class="modal-header">
      <h3>{{ detail?.display_name || detail?.name || network?.name || 'Network' }}</h3>
      <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="emit('close')">
        <span class="mdi mdi-close"></span>
      </button>
    </div>
    <div class="modal-body">
      <div v-if="loading" class="text-muted text-sm"><span class="spinner"></span>{{ $t('networks.readingTheNetwork') }}</div>
      <template v-else-if="detail">
        <div class="detail-grid">
          <span class="text-muted">{{ $t('db.dockerName') }}</span>
          <span class="mono text-sm">{{ detail.docker_name }}</span>
          <span class="text-muted">{{ $t('networks.driver') }}</span>
          <span class="text-sm">
            {{ detail.driver }}<span v-if="detail.internal">{{ $t('appDetail.net.internal') }}</span>
            <span v-if="detail.scope"> · {{ detail.scope }}</span>
          </span>
          <template v-if="detail.exists">
            <span class="text-muted">{{ $t('networks.ipv4Subnet') }}</span>
            <span class="mono text-sm">{{ detail.subnet || '—' }}</span>
            <span class="text-muted">{{ $t('networks.ipv4Gateway') }}</span>
            <span class="mono text-sm">{{ detail.gateway || '—' }}</span>
            <span class="text-muted">{{ $t('networks.ipv6') }}</span>
            <span>
              <span class="badge" :class="detail.enable_ipv6 ? 'badge-success' : 'badge-neutral'">
                {{ detail.enable_ipv6 ? 'enabled' : 'disabled' }}
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
        <!-- An overlay exists on a node only once one of its containers attaches there, so "not on
             this node" is ordinary rather than an error. -->
        <p v-if="!detail.exists" class="form-hint text-muted" style="margin-top: 10px">{{ $t('networks.thisNetworkIsNotPresent') }}<template v-if="detail.driver === 'overlay'">{{ $t('networks.anOverlayAppearsOnA') }}</template>
        </p>
      </template>
    </div>
    <div class="modal-footer">
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ $t('shell.close') }}</button>
    </div>
  </AppModal>
</template>

<style scoped>
.detail-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 8px 16px;
  align-items: center;
}
</style>
