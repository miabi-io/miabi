<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { locationApi, type LocationNode } from '@/api/locations'
import { useWorkspaceStore } from '@/stores/workspace'

// Swarm ignores server_id for services, so pinning means a placement constraint; only `location`'s nodes are offered.
const props = defineProps<{ modelValue: string[]; replicas?: number; location?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const { currentWorkspaceId } = storeToRefs(useWorkspaceStore())
const nodes = ref<LocationNode[]>([])

let request = 0
watch([currentWorkspaceId, () => props.location], async ([ws, loc]) => {
  const req = ++request
  let list: LocationNode[] = []
  if (ws) {
    try {
      list = (await locationApi.nodes(ws, loc)).data.data ?? []
    } catch {
      list = []
    }
  }
  if (req !== request) return
  nodes.value = list
  if (pinned.value && !list.some((n) => n.swarm_node_id === pinned.value)) pinned.value = ''
}, { immediate: true })

// A node outside the swarm has no node.id to constrain on, and the scheduler would never place there.
const pinnable = computed(() => nodes.value.filter((n) => !!n.swarm_node_id && (n.is_local || (n.online && !n.cordoned))))

// Nothing to choose between until the cluster actually has more than one member.
const hasChoice = computed(() => pinnable.value.length > 1)

// Derived from the constraints so the control stays correct if they're set elsewhere.
const pinned = computed({
  get: () => {
    const c = props.modelValue.find((x) => x.startsWith('node.id=='))
    return c ? c.slice('node.id=='.length) : ''
  },
  set: (id: string) => {
    // Preserve any other constraints the caller set; only own the node.id one.
    const others = props.modelValue.filter((x) => !x.startsWith('node.id=='))
    emit('update:modelValue', id ? [...others, `node.id==${id}`] : others)
  },
})

function optionLabel(n: LocationNode): string {
  return n.is_local ? `${n.name} (manager)` : n.name
}
</script>

<template>
  <div v-if="hasChoice" class="form-group">
    <label class="form-label">{{ $t('placement.placement') }}</label>
    <select v-model="pinned" class="form-select" :aria-label="$t('placement.placement')">
      <option value="">{{ $t('placement.anyNodeTheSchedulerDecides') }}</option>
      <option v-for="n in pinnable" :key="n.id" :value="n.swarm_node_id">Pin to {{ optionLabel(n) }}</option>
    </select>
    <p v-if="pinned && (replicas ?? 1) > 1" class="form-hint form-hint-warn">
      All {{ replicas }} replicas will run on this one node — it stops being a spread across the cluster.
    </p>
    <p v-else class="form-hint">{{ $t('placement.pinAServiceThatMust') }}</p>
  </div>
</template>

<style scoped>
.form-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}
.form-hint-warn {
  color: var(--warning, #b45309);
}
</style>
