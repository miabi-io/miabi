<script setup lang="ts">
import { computed } from 'vue'
import { fmtDelta } from './format'

// A headline metric tile with an optional period-over-period delta. `invert`
// flips the color meaning (up = bad, e.g. error rate / latency).
const props = defineProps<{
  label: string
  value: string
  sub?: string
  icon?: string // optional mdi class, shown muted before the label
  delta?: number | null
  invert?: boolean
  danger?: boolean
}>()

const dir = computed(() => {
  if (props.delta === null || props.delta === undefined) return 'flat'
  if (Math.abs(props.delta) < 0.001) return 'flat'
  return props.delta > 0 ? 'up' : 'down'
})
</script>

<template>
  <div class="a-tile">
    <div class="t-label"><span v-if="icon" class="mdi t-icon" :class="icon"></span>{{ label }}</div>
    <div class="t-value" :class="{ danger }">
      {{ value }}
      <span
        v-if="delta !== null && delta !== undefined && dir !== 'flat'"
        class="a-delta"
        :class="[dir, { invert }]"
      >{{ fmtDelta(delta) }}</span>
    </div>
    <div v-if="$slots.sub || sub" class="t-sub"><slot name="sub">{{ sub }}</slot></div>
  </div>
</template>

<style scoped>
.t-icon { color: var(--text-muted); font-size: 14px; margin-right: 6px; vertical-align: -1px; }
</style>
