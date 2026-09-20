<script setup lang="ts">
// Live CPU / memory / network across the workspace's running containers.
import Sparkline from '@/components/Sparkline.vue'
import type { WorkspaceLiveSample } from '@/api/types'
import { fmtBytes } from './format'

defineProps<{
  sample: WorkspaceLiveSample | null
  cpuSeries: number[]
  memSeries: number[]
}>()
</script>

<template>
  <div class="card resources-card">
    <div class="card-header">
      <h2>{{ $t('dashboard.resources.title') }}</h2>
      <span class="live-pill" :class="{ 'is-live': !!sample }" :title="sample ? $t('dashboard.resources.live') : $t('dashboard.resources.connecting')">
        <span class="live-dot"></span>
        {{ sample ? $t('dashboard.resources.containers', sample.containers) : $t('dashboard.resources.connecting') }}
      </span>
    </div>
    <div class="resources-body">
      <div class="resource">
        <div class="resource-head">
          <span class="resource-label"><span class="mdi mdi-chip"></span> CPU</span>
          <span class="resource-value">{{ (sample?.cpu_cores ?? 0).toFixed(2) }} <small>{{ $t('dashboard.resources.cores') }}</small></span>
        </div>
        <Sparkline :values="cpuSeries" :width="220" :height="40" stroke="var(--primary-500)" />
      </div>
      <div class="resource">
        <div class="resource-head">
          <span class="resource-label"><span class="mdi mdi-memory"></span> {{ $t('dashboard.resources.memory') }}</span>
          <span class="resource-value">{{ fmtBytes(sample?.memory_bytes) }}</span>
        </div>
        <Sparkline :values="memSeries" :width="220" :height="40" stroke="var(--info-500, #0ea5e9)" />
      </div>
      <div class="resource">
        <div class="resource-head">
          <span class="resource-label"><span class="mdi mdi-swap-vertical"></span> {{ $t('dashboard.resources.network') }}</span>
        </div>
        <div class="net-figures">
          <span class="net-figure"><span class="mdi mdi-arrow-down"></span> {{ fmtBytes(sample?.net_rx_bytes) }} <small>RX</small></span>
          <span class="net-figure"><span class="mdi mdi-arrow-up"></span> {{ fmtBytes(sample?.net_tx_bytes) }} <small>TX</small></span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.resources-card { margin-bottom: 20px; }
.resources-body {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 20px; padding: 16px 20px;
}
.resource { display: flex; flex-direction: column; gap: 8px; }
/* The sparkline is fixed-width by default; let it fill its column so it doesn't
   sit stranded on the left of a wide card. */
.resource :deep(.sparkline) { width: 100%; }
.resource-head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.resource-label { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); }
.resource-label .mdi { font-size: 16px; }
.resource-value { font-size: 20px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
.resource-value small { font-size: 12px; font-weight: 400; color: var(--text-muted); }
.net-figures { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; }
.net-figure { display: inline-flex; align-items: center; gap: 6px; font-size: 16px; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
.net-figure .mdi { font-size: 16px; color: var(--text-muted); }
.net-figure small { font-size: 11px; font-weight: 400; color: var(--text-muted); }

.live-pill { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; font-weight: 600; color: var(--text-muted); }
.live-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--text-muted); flex-shrink: 0; }
.live-pill.is-live { color: var(--success-600); }
.live-pill.is-live .live-dot { background: var(--success-500, #16a34a); animation: live-blink 1.6s ease-in-out infinite; }
@keyframes live-blink {
  0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(22, 163, 74, 0.5); }
  50% { opacity: 0.5; box-shadow: 0 0 0 4px rgba(22, 163, 74, 0); }
}
@media (prefers-reduced-motion: reduce) {
  .live-pill.is-live .live-dot { animation: none; }
}
</style>
