<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  values: number[]
  width?: number
  height?: number
  stroke?: string
}>(), {
  width: 140,
  height: 36,
  stroke: 'var(--primary-500)',
})

const pad = 2

const line = computed(() => {
  const v = props.values
  if (v.length < 2) return ''
  const max = Math.max(...v)
  const min = Math.min(...v)
  const range = max - min || 1
  const step = (props.width - pad * 2) / (v.length - 1)
  return v.map((val, i) => {
    const x = pad + i * step
    const y = pad + (props.height - pad * 2) * (1 - (val - min) / range)
    return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
})

const area = computed(() => {
  if (!line.value) return ''
  const w = props.width, h = props.height
  return `${line.value} L${w - pad},${h - pad} L${pad},${h - pad} Z`
})
</script>

<template>
  <svg
    :width="width"
    :height="height"
    :viewBox="`0 0 ${width} ${height}`"
    class="sparkline"
    preserveAspectRatio="none"
    aria-hidden="true"
  >
    <path v-if="area" :d="area" class="spark-area" :style="{ fill: stroke }" />
    <path v-if="line" :d="line" class="spark-line" :style="{ stroke }" fill="none" />
    <text v-if="values.length < 2" :x="width / 2" :y="height / 2 + 3" class="spark-empty">no data yet</text>
  </svg>
</template>

<style scoped>
.sparkline { display: block; overflow: visible; }
.spark-line { stroke-width: 1.5; vector-effect: non-scaling-stroke; stroke-linejoin: round; stroke-linecap: round; }
.spark-area { opacity: 0.12; }
.spark-empty { fill: var(--text-muted); font-size: 10px; text-anchor: middle; }
</style>
