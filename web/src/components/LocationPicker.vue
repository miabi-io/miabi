<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { locationApi, type Location, type LocationNode } from '@/api/locations'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { locationLabel } from '@/utils/locations'

// '' means the workspace default. A bound nodeName pins by the node's fixed name for anyone allowed; serverId is the admin-only form.
const props = defineProps<{ modelValue: string; serverId?: number; nodeName?: string; allowPin?: boolean; nodeHint?: string }>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'update:serverId', v: number): void
  (e: 'update:nodeName', v: string): void
}>()

const { currentWorkspaceId } = storeToRefs(useWorkspaceStore())
const auth = useAuthStore()
const locations = ref<Location[]>([])
const nodes = ref<LocationNode[]>([])

watch(currentWorkspaceId, async (id) => {
  locations.value = []
  if (!id) return
  try {
    locations.value = (await locationApi.list(id)).data.data?.locations ?? []
  } catch {
    locations.value = []
  }
}, { immediate: true })

const byName = computed(() => props.nodeName !== undefined)
const canPin = computed(() => props.allowPin !== false && (byName.value || auth.isAdmin))

// Only the chosen location's nodes are offered, so a pin can never point outside it.
let nodesRequest = 0
watch([canPin, currentWorkspaceId, () => props.modelValue], async ([on, ws, loc]) => {
  const req = ++nodesRequest
  if (!on || !ws) {
    nodes.value = []
    return
  }
  let list: LocationNode[] = []
  try {
    list = (await locationApi.nodes(ws, loc)).data.data ?? []
  } catch {
    list = []
  }
  if (req !== nodesRequest) return
  nodes.value = list
  if ((props.serverId ?? 0) > 0 && !list.some((n) => n.id === props.serverId)) emit('update:serverId', 0)
  if (props.nodeName && !list.some((n) => n.name === props.nodeName && n.pinnable)) emit('update:nodeName', '')
}, { immediate: true })

watch(() => props.allowPin, (on) => { if (on === false && props.nodeName) emit('update:nodeName', '') })

const defaultLocation = computed(() => locations.value.find((l) => l.default))

const location = computed({
  get: () => props.modelValue,
  set: (v: string) => emit('update:modelValue', v),
})
const node = computed({
  get: () => props.serverId ?? 0,
  set: (v: number) => emit('update:serverId', Number(v)),
})
const nodeName = computed({
  get: () => props.nodeName ?? '',
  set: (v: string) => emit('update:nodeName', v),
})
const showNodes = computed(() => canPin.value && nodes.value.length > 1 && (!byName.value || nodes.value.some((n) => n.pinnable)))

function placeable(n: LocationNode): boolean {
  return n.is_local || (n.online && !n.cordoned)
}
function nodeLabel(n: LocationNode): string {
  if (n.is_local) return `${n.display_name} (manager)`
  if (!n.online) return `${n.display_name} — offline`
  if (n.cordoned) return `${n.display_name} — cordoned`
  return n.display_name
}
</script>

<template>
  <div v-if="locations.length > 1" class="form-group">
    <label class="form-label">{{ $t('locationPicker.location') }}</label>
    <select v-model="location" class="form-select" :aria-label="$t('locationPicker.location')">
      <option value="">{{ defaultLocation ? `Workspace default — ${locationLabel(defaultLocation)}` : 'Workspace default' }}</option>
      <option v-for="l in locations" :key="l.id" :value="l.name">{{ locationLabel(l) }}</option>
    </select>
    <p class="form-hint">{{ $t('locationPicker.privateNetworksDonTSpan') }}</p>
  </div>
  <div v-if="showNodes && byName" class="form-group">
    <label class="form-label">{{ $t('locationPicker.node') }}</label>
    <select v-model="nodeName" class="form-select" :aria-label="$t('locationPicker.node')">
      <option value="">{{ $t('locationPicker.anyNodeInTheLocation') }}</option>
      <option v-for="n in nodes" :key="n.id" :value="n.name" :disabled="!n.pinnable">{{ nodeLabel(n) }}</option>
    </select>
    <p class="form-hint">{{ nodeHint ?? $t('locationPicker.nodeIsFixed') }}</p>
  </div>
  <div v-else-if="showNodes" class="form-group">
    <label class="form-label">{{ $t('locationPicker.node') }} <span class="badge badge-muted">{{ $t('locationPicker.admin') }}</span></label>
    <select v-model="node" class="form-select" :aria-label="$t('locationPicker.node')">
      <option :value="0">{{ $t('locationPicker.anyNodeInTheLocation') }}</option>
      <option v-for="n in nodes" :key="n.id" :value="n.id" :disabled="!placeable(n)">{{ nodeLabel(n) }}</option>
    </select>
    <p class="form-hint">{{ $t('locationPicker.nodesOfTheChosenLocation') }}</p>
  </div>
</template>

<style scoped>
.form-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--text-muted);
}
</style>
