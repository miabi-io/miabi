<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useLicenseStore } from '@/stores/license'
import RunnersPanel from '@/components/RunnersPanel.vue'
import { adminRunnerApi, type RunnerAdapter } from '@/api/runners'

// The cap comes from the license view; a mirrored constant drifted from the backend and broke registration.
const license = useLicenseStore()
onMounted(() => license.load())

const usage = computed(() => license.view?.shared_runner_usage)
const limit = computed(() => usage.value?.limit ?? -1)
const unlimited = computed(() => limit.value < 0)
const createLimit = computed(() => limit.value)
const limitNote = computed(() =>
  unlimited.value
    ? ''
    : `Your edition allows ${limit.value} platform-shared runners. Upgrade your license for an unlimited shared pool.`,
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
  eventsUrl: () => adminRunnerApi.statusEventsUrl(),
}
</script>

<template>
  <div>
    <div v-if="!unlimited" class="card" style="margin-bottom: 16px">
      <div class="card-body" style="display: flex; gap: 12px; align-items: flex-start">
        <span class="mdi mdi-information-outline" style="font-size: 22px; color: var(--text-muted)"></span>
        <div>
          <p style="margin: 0">
            Your edition includes <strong>{{ limit }}</strong> platform-shared runners — a shared build
            pool any capable workspace can use ({{ usage?.used ?? 0 }} registered). Enterprise unlocks an
            <strong>unlimited</strong> shared pool. Workspace-owned runners are bounded by each
            workspace's plan, not by this cap.
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
