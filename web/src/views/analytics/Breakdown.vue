<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Category } from '@/api/analytics'
import { fmtNum, fmtPct, countryName, countryFlag } from './format'

// `limit` is presentation only (ranking is server-side); `otherKey` pins an "Other" row covering hidden plus `untracked`.
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
    otherKey?: string
    untracked?: number
    linked?: boolean
    active?: string | null
  }>(),
  { kind: 'plain', flat: false, scroll: false, share: false, searchable: false, linked: false },
)
const emit = defineEmits<{ hover: [label: string | null]; select: [label: string] }>()
const { t } = useI18n()

const SEARCH_MIN = 15
const query = ref('')

const shown = computed(() => (props.limit ? props.items.slice(0, props.limit) : props.items))
// Scaled against the visible leader and kept while filtering so bars don't jump.
const max = computed(() => Math.max(1, ...shown.value.map((i) => i.count)))
const total = computed(() => props.total || shown.value.reduce((n, i) => n + i.count, 0) || 1)
const showSearch = computed(() => props.searchable && shown.value.length > SEARCH_MIN)
const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!showSearch.value || !q) return shown.value
  return shown.value.filter((c) => label(c).toLowerCase().includes(q) || c.label.toLowerCase() === q)
})

const other = computed(() => {
  if (!props.otherKey || query.value.trim()) return null
  const hidden = props.limit ? props.items.slice(props.limit) : []
  const count = hidden.reduce((n, i) => n + i.count, 0) + Math.max(0, props.untracked ?? 0)
  if (count <= 0) return null
  return { count, label: hidden.length ? t(props.otherKey, hidden.length) : t('analytics.otherLongTail') }
})

const body = ref<HTMLElement | null>(null)
const flashing = ref<string | null>(null)
let flashTimer: ReturnType<typeof setTimeout> | undefined

// Centres the row inside the list only; scrollIntoView would also scroll the page.
watch(() => props.active, async (active) => {
  if (!active) return
  if (!rows.value.some((c) => c.label === active)) query.value = ''
  await nextTick()
  const box = body.value
  const el = box?.querySelector<HTMLElement>(`[data-label="${CSS.escape(active)}"]`)
  if (el && box) {
    const offset = el.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop
    const smooth = !window.matchMedia('(prefers-reduced-motion: reduce)').matches
    box.scrollTo({ top: offset - box.clientHeight / 2 + el.offsetHeight / 2, behavior: smooth ? 'smooth' : 'auto' })
  }
  flashing.value = active
  clearTimeout(flashTimer)
  flashTimer = setTimeout(() => (flashing.value = null), 1200)
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
    <div
      ref="body"
      :class="[flat ? 'bd-flat-body' : 'card-body', { 'bd-scroll': scroll && shown.length, 'bd-has-other': other }]"
      @mouseleave="linked && emit('hover', null)"
    >
      <div
        v-for="c in rows"
        :key="c.label"
        class="brow"
        :class="{ 'bd-linked': linked, 'bd-active': linked && active === c.label, 'bd-flash': flashing === c.label }"
        :data-label="c.label"
        @mouseenter="linked && emit('hover', c.label)"
        @click="linked && emit('select', c.label)"
      >
        <span class="brow-label" :title="label(c)">{{ label(c) }}</span>
        <span class="brow-track"><span class="brow-fill" :style="{ width: (c.count / max) * 100 + '%' }"></span></span>
        <span class="brow-count">{{ fmtNum(c.count) }}</span>
        <span v-if="share" class="bd-share">{{ fmtPct(c.count / total) }}</span>
      </div>
      <p v-if="!shown.length" class="a-muted">{{ emptyHint || 'No data.' }}</p>
      <p v-else-if="!rows.length" class="a-muted">{{ $t('analytics.noMatches', { q: query.trim() }) }}</p>
    </div>
    <div v-if="other" class="brow bd-other" :class="flat ? '' : 'bd-other-card'">
      <span class="brow-label" :title="other.label">{{ other.label }}</span>
      <span class="brow-track"><span class="brow-fill" :style="{ width: Math.min(100, (other.count / max) * 100) + '%' }"></span></span>
      <span class="brow-count">{{ fmtNum(other.count) }}</span>
      <span v-if="share" class="bd-share">{{ fmtPct(other.count / total) }}</span>
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
.bd-has-other.card-body { padding-bottom: 8px; }
.bd-other { border-top: 1px dashed var(--border-primary); color: var(--text-muted); font-style: italic; }
.bd-other-card { margin: 0 24px; padding: 8px 0 18px; }
.bd-other .brow-fill { background: var(--text-muted); opacity: 0.5; }
.bd-linked { cursor: pointer; border-radius: 6px; margin: 0 -6px; padding-left: 6px; padding-right: 6px; transition: background 0.15s; }
.bd-linked:hover { background: var(--bg-secondary); }
.bd-active { background: color-mix(in srgb, var(--primary-500) 12%, transparent); }
.bd-active .brow-label { color: var(--text-primary); font-weight: 600; }
.bd-flash { animation: bd-flash 1.2s ease-out; }
@keyframes bd-flash { from { background: color-mix(in srgb, var(--primary-500) 22%, transparent); } }
@media (prefers-reduced-motion: reduce) { .bd-flash { animation: none; } }
.bd-scroll::-webkit-scrollbar { width: 6px; }
.bd-scroll::-webkit-scrollbar-thumb { background: var(--border-primary); border-radius: 3px; }
</style>
