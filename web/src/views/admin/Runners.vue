<script setup lang="ts">
import RunnersPanel from '@/components/RunnersPanel.vue'
import { adminRunnerApi, type RunnerAdapter } from '@/api/runners'

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
  <RunnersPanel
    :adapter="adapter"
    :can-edit="true"
    shared
    detail-route-name="admin-runner-detail"
    title="Shared Runners"
    subtitle="Platform-shared build machines any capable workspace can use. Managed by admins."
  />
</template>
