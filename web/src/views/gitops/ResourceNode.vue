<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import type { NodeStatus } from '@/api/types'
import { kindOf, nodeStatusMeta, healthOf } from './topologyMeta'

// Data carried on each Vue Flow node (set in GitOpsDetail). Mirrors a
// TopologyNode plus presentation flags.
interface NodeData {
  kind: string
  name: string
  status: NodeStatus
  health?: string
  liveId?: number
  // dimmed is set when a status filter is active and this node doesn't match.
  dimmed?: boolean
}

const props = defineProps<{ data: NodeData; selected?: boolean }>()

const kind = computed(() => kindOf(props.data.kind))
const status = computed(() => nodeStatusMeta[props.data.status])
const health = computed(() => healthOf(props.data.health))
</script>

<template>
  <div class="rnode" :class="{ selected, dimmed: data.dimmed }" :style="{ '--accent': status.color }">
    <!-- Left = incoming (this resource is a dependency of another); right =
         outgoing (this resource depends on another). Both present so a node can
         be either end of an edge. -->
    <Handle type="target" :position="Position.Left" />
    <Handle type="source" :position="Position.Right" />

    <span class="rnode-icon">
      <span class="mdi" :class="kind.icon"></span>
      <!-- live runtime health (apps/databases) -->
      <span
        v-if="health"
        class="rnode-health"
        :class="{ pulse: health.pulse }"
        :style="{ background: health.color }"
        :title="health.label"
      ></span>
    </span>
    <span class="rnode-body">
      <span class="rnode-name" :title="data.name">{{ data.name }}</span>
      <span class="rnode-kind">{{ kind.label }}</span>
    </span>
    <span class="rnode-status" :title="status.label">
      <span class="mdi" :class="status.icon"></span>
    </span>
  </div>
</template>

<style scoped>
.rnode {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 200px;
  padding: 10px 12px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-left: 3px solid var(--accent);
  border-radius: 10px;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.06);
  cursor: pointer;
  transition: box-shadow 0.12s, border-color 0.12s;
}
.rnode:hover { box-shadow: 0 3px 10px rgba(0, 0, 0, 0.12); }
.rnode.dimmed { opacity: 0.28; }
.rnode.dimmed.selected { opacity: 1; }
.rnode.selected {
  border-color: var(--accent);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 35%, transparent);
}
.rnode-icon {
  position: relative;
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 8px;
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  color: var(--accent);
}
.rnode-icon .mdi { font-size: 18px; }
.rnode-health {
  position: absolute;
  right: -3px;
  bottom: -3px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  border: 2px solid var(--bg-primary);
  box-sizing: content-box;
}
.rnode-health.pulse { animation: health-pulse 1.4s ease-in-out infinite; }
@keyframes health-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }
.rnode-body { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.rnode-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rnode-kind { font-size: 11px; color: var(--text-muted); }
.rnode-status { color: var(--accent); flex-shrink: 0; }
.rnode-status .mdi { font-size: 16px; }
:deep(.vue-flow__handle) {
  width: 6px;
  height: 6px;
  background: var(--border-primary);
  border: none;
}
</style>
