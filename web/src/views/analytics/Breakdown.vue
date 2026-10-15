<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Category } from '@/api/analytics'
import { fmtNum, fmtPct, countryName, countryFlag } from './format'

// A ranked bar-list breakdown (top pages, referrers, countries, …). `kind`
// 'country' renders a flag + resolved country name from the alpha-2 label.
//
// `flat` drops the card chrome so the list can sit inside another card (the
// dashboard's traffic panel) without nesting one card in another. `limit` shows
// only the leading N — the ranking is server-side, so this is presentation only.
// `scroll` keeps the card at a fixed height and scrolls a long list inside it.
// `share` adds each row's percentage of `total` (default: the items' sum), and
// `searchable` adds a filter box once the list is longer than SEARCH_MIN rows.
const props = withDefaults(
  defineProps<{
    title: string
    items: Category[]
    kind?: 'country' | 'plain'
    emptyHint?: string
    flat?: boolean
    limit?: number
    scroll?: boolean
    share?: boolean
    total?: number
    searchable?: boolean
  }>(),
  { kind: 'plain', flat: false, scroll: false, share: false, searchable: false },
)

const SEARCH_MIN = 15
const query = ref('')

const shown = computed(() => (props.limit ? props.items.slice(0, props.limit) : props.items))
// Scaled against the visible leader, so a truncated list still fills its bar. Filtering
// keeps that scale, so a row's bar doesn't jump while typing.
const max = computed(() => Math.max(1, ...shown.value.map((i) => i.count)))
const total = computed(() => props.total || shown.value.reduce((n, i) => n + i.count, 0) || 1)
const showSearch = computed(() => props.searchable && shown.value.length > SEARCH_MIN)
const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!showSearch.value || !q) return shown.value
  return shown.value.filter((c) => label(c).toLowerCase().includes(q) || c.label.toLowerCase() === q)
})

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
    <div v-if="showSearch" class="bd-search">
      <span class="mdi mdi-magnify"></span>
      <input v-model="query" type="search" class="form-input" :placeholder="$t('analytics.filterList', { n: shown.length })" :aria-label="$t('analytics.filterList', { n: shown.length })" />
    </div>
    <div :class="[flat ? 'bd-flat-body' : 'card-body', { 'bd-scroll': scroll && shown.length }]">
      <div v-for="c in rows" :key="c.label" class="brow">
        <span class="brow-label" :title="label(c)">{{ label(c) }}</span>
        <span class="brow-track"><span class="brow-fill" :style="{ width: (c.count / max) * 100 + '%' }"></span></span>
        <span class="brow-count">{{ fmtNum(c.count) }}</span>
        <span v-if="share" class="bd-share">{{ fmtPct(c.count / total) }}</span>
      </div>
      <p v-if="!shown.length" class="a-muted">{{ emptyHint || 'No data.' }}</p>
      <p v-else-if="!rows.length" class="a-muted">{{ $t('analytics.noMatches', { q: query.trim() }) }}</p>
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
.bd-search { position: relative; padding: 16px 24px 0; }
.bd-flat .bd-search { padding: 0 0 8px; }
.bd-search .mdi { position: absolute; left: 34px; top: 22px; font-size: 15px; color: var(--text-muted); pointer-events: none; }
.bd-flat .bd-search .mdi { left: 10px; top: 6px; }
.bd-search + .card-body { padding-top: 12px; }
.bd-search .form-input { width: 100%; height: 30px; padding-left: 30px; font-size: 13px; }
.bd-share { flex: 0 0 auto; min-width: 46px; text-align: right; font-size: 12px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.bd-scroll {
  max-height: 320px; overflow-y: auto; overscroll-behavior: contain;
  /* Fades the last row so a cut-off list reads as scrollable even with overlay scrollbars. */
  mask-image: linear-gradient(to bottom, #000 calc(100% - 28px), transparent);
  scrollbar-width: thin; scrollbar-color: var(--border-primary) transparent;
}
.bd-scroll::-webkit-scrollbar { width: 6px; }
.bd-scroll::-webkit-scrollbar-thumb { background: var(--border-primary); border-radius: 3px; }
</style>
