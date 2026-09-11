<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { locationApi, type Location } from '@/api/locations'
import { useWorkspaceStore } from '@/stores/workspace'
import { locationLabel } from '@/utils/locations'

const props = defineProps<{ clusterId?: number }>()

const { currentWorkspaceId } = storeToRefs(useWorkspaceStore())
const locations = ref<Location[]>([])

watch(currentWorkspaceId, async (id) => {
  locations.value = []
  if (!id) return
  try {
    locations.value = (await locationApi.list(id)).data.data ?? []
  } catch {
    locations.value = []
  }
}, { immediate: true })

const label = computed(() => {
  if (locations.value.length < 2) return ''
  const l = locations.value.find((x) => x.id === props.clusterId)
  return l ? locationLabel(l) : ''
})
</script>

<template>
  <template v-if="label"> · <span class="mdi mdi-map-marker-outline"></span> {{ label }}</template>
</template>
