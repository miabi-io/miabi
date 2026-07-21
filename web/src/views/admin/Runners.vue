<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useLicenseStore } from '@/stores/license'
import RunnersPanel from '@/components/RunnersPanel.vue'
import { adminRunnerApi, type RunnerAdapter } from '@/api/runners'

// Platform-shared runners: the shared pool is available without a cap. Managing
// the pool (edit/cordon/token/delete) is always available to admins.
const license = useLicenseStore()
onMounted(() => license.load())

// Mirror of enterprise.CommunityRunnerLimit (-1 = unlimited).
const COMMUNITY_SHARED_RUNNER_LIMIT = -1
const unlimited = computed(() => COMMUNITY_SHARED_RUNNER_LIMIT < 0 || license.mutable('platform_runners'))
const createLimit = computed(() => (unlimited.value ? -1 : COMMUNITY_SHARED_RUNNER_LIMIT))
const limitNote = computed(() =>
  unlimited.value
    ? ''
    : `Community includes ${COMMUNITY_SHARED_RUNNER_LIMIT} platform-shared runner. Upgrade to Enterprise for an unlimited shared pool.`,
)

const adapter: RunnerAdapter = {
  list: async () => (await adminRunnerApi.list()).data.data ?? [],
  get: async (id) => (await adminRunnerApi.get(id)).data.data,
  create: async (input) => (await adminRunnerApi.create(input)).data.data,
  cordon: async (id, c) => (await adminRunnerApi.cordon(id, c)).data.data,
  regenerateToken: async (id) => (await adminRunnerApi.regenerateToken(id)).data.data.token,
  remove: async (id) => {
    await adminRunnerApi.remove(id)
  },
}
</script>

<template>
  <div>
    <div v-if="!unlimited" class="card" style="margin-bottom: 16px">
      <div class="card-body" style="display: flex; gap: 12px; align-items: flex-start">
        <span class="mdi mdi-information-outline" style="font-size: 22px; color: var(--text-muted)"></span>
        <div>
          <p style="margin: 0">
            Community includes <strong>one</strong> platform-shared runner — a single shared build pool
            any capable workspace can use. Enterprise unlocks an <strong>unlimited</strong> shared pool.
            Workspace-owned runners are always unlimited.
          </p>
          <router-link to="/admin/license" class="btn btn-secondary btn-sm" style="margin-top: 8px">Manage license</router-link>
        </div>
      </div>
    </div>

    <RunnersPanel
      :adapter="adapter"
      :can-edit="true"
      :create-limit="createLimit"
      :limit-note="limitNote"
      shared
      detail-route-name="admin-runner-detail"
      title="Shared Runners"
      subtitle="Platform-shared build machines any capable workspace can use. Managed by admins."
    />
  </div>
</template>
