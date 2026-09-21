<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useLicenseStore } from '@/stores/license'
import RunnersPanel from '@/components/RunnersPanel.vue'
import { adminRunnerApi, type RunnerAdapter } from '@/api/runners'

// Platform-shared runners. The cap comes from the license view rather than a constant mirrored
// from the backend: the mirror drifted to -1 while the backend enforced 2, so the register button
// stayed enabled and the third runner failed at submit. Managing the pool (edit/cordon/token/
// delete) is always available to admins.
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
