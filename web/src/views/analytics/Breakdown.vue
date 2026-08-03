<script setup lang="ts">
import { computed } from 'vue'
import type { Category } from '@/api/analytics'
import { fmtNum, countryName, countryFlag } from './format'

// A ranked bar-list breakdown (top pages, referrers, countries, …). `kind`
// 'country' renders a flag + resolved country name from the alpha-2 label.
//
// `flat` drops the card chrome so the list can sit inside another card (the
// dashboard's traffic panel) without nesting one card in another. `limit` shows
// only the leading N — the ranking is server-side, so this is presentation only.
const props = withDefaults(
  defineProps<{
    title: string
    items: Category[]
    kind?: 'country' | 'plain'
    emptyHint?: string
    flat?: boolean
    limit?: number
  }>(),
  { kind: 'plain', flat: false },
)

const shown = computed(() => (props.limit ? props.items.slice(0, props.limit) : props.items))
// Scaled against the visible leader, so a truncated list still fills its bar.
const max = computed(() => Math.max(1, ...shown.value.map((i) => i.count)))

function label(c: Category): string {
  if (props.kind === 'country') return `${countryFlag(c.label)}  ${countryName(c.label)}`
  return c.label || 'direct'
}
</script>

<template>
  <div :class="flat ? 'bd-flat' : 'card'">
    <div :class="flat ? 'bd-flat-header' : 'a-card-header'">
      <h3>{{ title }}</h3>
      <!-- An optional trailing action (e.g. "View map →"). -->
      <slot name="action" />
    </div>
    <div :class="flat ? 'bd-flat-body' : 'card-body'">
      <div v-for="c in shown" :key="c.label" class="brow">
        <span class="brow-label" :title="label(c)">{{ label(c) }}</span>
        <span class="brow-track"><span class="brow-fill" :style="{ width: (c.count / max) * 100 + '%' }"></span></span>
        <span class="brow-count">{{ fmtNum(c.count) }}</span>
      </div>
      <p v-if="!shown.length" class="a-muted">{{ emptyHint || 'No data.' }}</p>
    </div>
  </div>
</template>

<style scoped>
/* Flat variant: no border, no background, no padding — the host card owns those. */
.bd-flat { min-width: 0; }
.bd-flat-header {
  display: flex; align-items: baseline; justify-content: space-between; gap: 10px;
  margin-bottom: 8px;
}
.bd-flat-header h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text-secondary); }
.bd-flat-body { min-width: 0; }
</style>
