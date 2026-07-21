<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { nodesApi, type PlaceableNode } from '@/api/nodes'

// The service-runtime counterpart of NodePicker.
//
// A container app is placed by `server_id`. A *service* app is placed by the
// Swarm scheduler, which ignores `server_id` entirely — so offering a node picker
// there would silently discard the choice. Pinning a service to a node instead
// means emitting a Swarm placement constraint, which is what this v-models:
// [] (let the scheduler decide) or ["node.id==<swarm node id>"].
const props = defineProps<{ modelValue: string[]; replicas?: number }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string[]): void }>()

const nodes = ref<PlaceableNode[]>([])

onMounted(async () => {
  try {
    nodes.value = (await nodesApi.placeable()).data.data ?? []
  } catch {
    nodes.value = []
  }
})

// Only swarm members can be pinned: a node outside the swarm has no node.id for a
// constraint to name, and the scheduler would never place a task there anyway.
const pinnable = computed(() => nodes.value.filter((n) => !!n.swarm_node_id && (n.is_local || (n.online && !n.cordoned))))

// Nothing to choose between until the cluster actually has more than one member.
const hasChoice = computed(() => pinnable.value.length > 1)

// The picker's value is the pinned node's swarm id ('' = any node). It is derived
// from the constraints so the control stays correct if they're set elsewhere.
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

function optionLabel(n: PlaceableNode): string {
  return n.is_local ? `${n.name} (manager)` : n.name
}
</script>

<template>
  <div v-if="hasChoice" class="form-group">
    <label class="form-label">Placement</label>
    <select v-model="pinned" class="form-input" aria-label="Placement">
      <option value="">Any node — the scheduler decides</option>
      <option v-for="n in pinnable" :key="n.id" :value="n.swarm_node_id">Pin to {{ optionLabel(n) }}</option>
    </select>
    <p v-if="pinned && (replicas ?? 1) > 1" class="form-hint form-hint-warn">
      All {{ replicas }} replicas will run on this one node — it stops being a spread across the cluster.
    </p>
    <p v-else class="form-hint">
      Pin a service that must run where its data lives, or to place it on a specific node.
    </p>
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
