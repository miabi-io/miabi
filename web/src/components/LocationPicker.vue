<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { locationApi, type Location } from '@/api/locations'
import { nodesApi, type PlaceableNode } from '@/api/nodes'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { locationLabel } from '@/utils/locations'

// v-model is the location name ('' = the workspace default); v-model:server-id pins a node, which only
// platform admins may do.
const props = defineProps<{ modelValue: string; serverId?: number; allowPin?: boolean }>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'update:serverId', v: number): void
}>()

const { currentWorkspaceId } = storeToRefs(useWorkspaceStore())
const auth = useAuthStore()
const locations = ref<Location[]>([])
const nodes = ref<PlaceableNode[]>([])

watch(currentWorkspaceId, async (id) => {
  locations.value = []
  if (!id) return
  try {
    locations.value = (await locationApi.list(id)).data.data ?? []
  } catch {
    locations.value = []
  }
}, { immediate: true })

const canPin = computed(() => props.allowPin !== false && auth.isAdmin)
watch(canPin, async (on) => {
  if (!on || nodes.value.length) return
  try {
    nodes.value = (await nodesApi.placeable()).data.data ?? []
  } catch {
    nodes.value = []
  }
}, { immediate: true })

const defaultLocation = computed(() => locations.value.find((l) => l.default))
const pinned = computed(() => canPin.value && (props.serverId ?? 0) > 0)

const location = computed({
  get: () => props.modelValue,
  set: (v: string) => emit('update:modelValue', v),
})
const node = computed({
  get: () => props.serverId ?? 0,
  set: (v: number) => {
    emit('update:serverId', Number(v))
    if (Number(v) > 0) emit('update:modelValue', '')
  },
})

function placeable(n: PlaceableNode): boolean {
  return n.is_local || (n.online && !n.cordoned)
}
function nodeLabel(n: PlaceableNode): string {
  if (n.is_local) return `${n.name} (manager)`
  if (!n.online) return `${n.name} — offline`
  if (n.cordoned) return `${n.name} — cordoned`
  return n.name
}
</script>

<template>
  <div v-if="locations.length > 1 && !pinned" class="form-group">
    <label class="form-label">Location</label>
    <select v-model="location" class="form-select" aria-label="Location">
      <option value="">{{ defaultLocation ? `Workspace default — ${locationLabel(defaultLocation)}` : 'Workspace default' }}</option>
      <option v-for="l in locations" :key="l.id" :value="l.name">{{ locationLabel(l) }}</option>
    </select>
    <p class="form-hint">Private networks don't span locations: keep an app with the databases and volumes it uses.</p>
  </div>
  <div v-if="canPin && nodes.length > 1" class="form-group">
    <label class="form-label">Node <span class="badge badge-muted">admin</span></label>
    <select v-model="node" class="form-select" aria-label="Node">
      <option :value="0">Any node in the location</option>
      <option v-for="n in nodes" :key="n.id" :value="n.id" :disabled="!placeable(n)">{{ nodeLabel(n) }}</option>
    </select>
    <p class="form-hint">Pinning a node also decides the location.</p>
  </div>
</template>

<style scoped>
.form-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}
</style>
