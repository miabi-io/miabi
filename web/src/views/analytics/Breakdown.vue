<script setup lang="ts">
import { computed } from 'vue'
import type { Category } from '@/api/analytics'
import { fmtNum, countryName, countryFlag } from './format'

// A ranked bar-list breakdown (top pages, referrers, countries, …). `kind`
// 'country' renders a flag + resolved country name from the alpha-2 label.
const props = defineProps<{
  title: string
  items: Category[]
  kind?: 'country' | 'plain'
  emptyHint?: string
}>()

const max = computed(() => Math.max(1, ...props.items.map((i) => i.count)))
function label(c: Category): string {
  if (props.kind === 'country') return `${countryFlag(c.label)}  ${countryName(c.label)}`
  return c.label || 'direct'
}
</script>

<template>
  <div class="card">
    <div class="a-card-header"><h3>{{ title }}</h3></div>
    <div class="card-body">
      <div v-for="c in items" :key="c.label" class="brow">
        <span class="brow-label" :title="label(c)">{{ label(c) }}</span>
        <span class="brow-track"><span class="brow-fill" :style="{ width: (c.count / max) * 100 + '%' }"></span></span>
        <span class="brow-count">{{ fmtNum(c.count) }}</span>
      </div>
      <p v-if="!items.length" class="a-muted">{{ emptyHint || 'No data.' }}</p>
    </div>
  </div>
</template>
