<script setup lang="ts">
import { computed } from 'vue'
import { Handle, Position } from '@vue-flow/core'
import type { GitSourceStatus } from '@/api/types'
import { gitSourceStatusMeta } from './topologyMeta'

interface ProjectData {
  name: string
  status: GitSourceStatus
  count: number
}

const props = defineProps<{ data: ProjectData; selected?: boolean }>()
const status = computed(() => gitSourceStatusMeta[props.data.status])
</script>

<template>
  <div class="pnode" :class="{ selected }" :style="{ '--accent': status.color }">
    <Handle type="source" :position="Position.Right" />
    <span class="pnode-icon"><span class="mdi mdi-git"></span></span>
    <span class="pnode-body">
      <span class="pnode-name" :title="data.name">{{ data.name }}</span>
      <span class="pnode-sub">GitOps project · {{ data.count }} resource{{ data.count === 1 ? '' : 's' }}</span>
    </span>
    <span class="pnode-status" :title="status.label"><span class="mdi" :class="status.icon"></span></span>
  </div>
</template>

<style scoped>
.pnode {
  display: flex;
  align-items: center;
  gap: 11px;
  width: 220px;
  padding: 12px 14px;
  background: var(--bg-primary);
  border: 1px solid var(--accent);
  border-radius: 12px;
  box-shadow: 0 2px 8px color-mix(in srgb, var(--accent) 20%, transparent);
  cursor: pointer;
  transition: box-shadow 0.12s;
}
.pnode:hover { box-shadow: 0 4px 14px color-mix(in srgb, var(--accent) 28%, transparent); }
.pnode.selected { box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 40%, transparent); }
.pnode-icon {
  display: grid;
  place-items: center;
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: 9px;
  background: var(--accent);
  color: #fff;
}
.pnode-icon .mdi { font-size: 20px; }
.pnode-body { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.pnode-name {
  font-size: 14px;
  font-weight: 700;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pnode-sub { font-size: 11px; color: var(--text-muted); }
.pnode-status { color: var(--accent); flex-shrink: 0; }
.pnode-status .mdi { font-size: 17px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
:deep(.vue-flow__handle) { width: 6px; height: 6px; background: var(--border-primary); border: none; }
</style>
