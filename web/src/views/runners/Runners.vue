<script setup lang="ts">
import { computed, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import RunnersPanel from '@/components/RunnersPanel.vue'
import { runnerApi, type RunnerAdapter } from '@/api/runners'

const ws = useWorkspaceStore()
const { currentWorkspaceId } = storeToRefs(ws)

type TabKey = 'mine' | 'platform'
const tab = ref<TabKey>('mine')

// The panel renders with can-edit=false, so only list() is ever called.
const readOnly = (): never => {
  throw new Error('platform runners are read-only')
}

// The workspace's own runners — fully managed by workspace admins.
const ownedAdapter = computed<RunnerAdapter>(() => {
  const id = currentWorkspaceId.value ?? 0
  return {
    list: async () => (await runnerApi.list(id)).data.data ?? [],
    get: async (rid) => (await runnerApi.get(id, rid)).data.data,
    create: async (input) => (await runnerApi.create(id, input)).data.data,
    cordon: async (rid, c) => (await runnerApi.cordon(id, rid, c)).data.data,
    regenerateToken: async (rid) => (await runnerApi.regenerateToken(id, rid)).data.data.token,
    remove: async (rid) => {
      await runnerApi.remove(id, rid)
    },
    eventsUrl: () => runnerApi.statusEventsUrl(id),
  }
})

// The platform-shared pool visible to this workspace — read-only.
const platformAdapter = computed<RunnerAdapter>(() => {
  const id = currentWorkspaceId.value ?? 0
  return {
    list: async () => (await runnerApi.listShared(id)).data.data ?? [],
    get: readOnly,
    create: readOnly,
    cordon: readOnly,
    regenerateToken: readOnly,
    remove: readOnly,
  }
})
</script>

<template>
  <div>
    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'mine' }" @click="tab = 'mine'">My runners</button>
      <button class="tab" :class="{ active: tab === 'platform' }" @click="tab = 'platform'">Platform runners</button>
    </div>

    <RunnersPanel
      v-if="tab === 'mine'"
      :key="`mine-${currentWorkspaceId ?? 0}`"
      :adapter="ownedAdapter"
      :can-edit="ws.canEdit"
      detail-route-name="runner-detail"
      title="Runners"
      :subtitle="`Dedicated build machines for ${ws.contextLabel}. Builds and pipelines run here instead of on your app nodes.`"
    />

    <RunnersPanel
      v-else
      :key="`platform-${currentWorkspaceId ?? 0}`"
      :adapter="platformAdapter"
      :can-edit="false"
      shared
      title="Platform runners"
      subtitle="Shared build machines provided by the platform. Any capable workspace can run builds on these; they are managed by platform admins."
    />
  </div>
</template>
