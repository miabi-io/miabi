<script setup lang="ts">
// Status-code split as a pie, with the counts beside it.
//
// The pie answers "is this workspace healthy at a glance"; it cannot answer
// "how many 4xx" — a real distribution is something like 460/9/1/0, where the
// last two classes are sub-degree slivers. So the legend beside it is not
// decoration: it is where the numbers actually live, and every slice is named
// there whether or not it is visible in the ring.
//
// `hole` is the inner radius as a fraction of the outer one; 0 draws a solid
// pie instead of a ring.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AnalyticsStatus } from '@/api/analytics'
import { fmtNum } from './format'

const { t } = useI18n()

// The legend is a column of percentages, so they all carry the same precision —
// the shared fmtPct switches decimals below 10% and would set 97.9% next to
// 1.91%. A class with traffic never rounds away to "0.0%".
function pct(frac: number): string {
  if (frac > 0 && frac < 0.001) return '<0.1%'
  return (frac * 100).toFixed(1) + '%'
}

const props = withDefaults(
  defineProps<{
    status: AnalyticsStatus
    hole?: number
    size?: number
  }>(),
  { hole: 0.62, size: 148 },
)

const R = 46 // outer radius in viewBox units (viewBox is 0 0 100 100)
const C = 50 // center

const hover = ref<string | null>(null)

interface Slice {
  key: string
  label: string
  hint: string
  count: number
}

const slices = computed<Slice[]>(() => [
  { key: '2xx', label: '2xx', hint: t('analytics.status.success'), count: props.status.s2xx },
  { key: '3xx', label: '3xx', hint: t('analytics.status.redirects'), count: props.status.s3xx },
  { key: '4xx', label: '4xx', hint: t('analytics.status.clientErrors'), count: props.status.s4xx },
  { key: '5xx', label: '5xx', hint: t('analytics.status.serverErrors'), count: props.status.s5xx },
])

const total = computed(() => slices.value.reduce((a, s) => a + s.count, 0))
const rows = computed(() =>
  slices.value.map((s) => ({ ...s, frac: total.value > 0 ? s.count / total.value : 0 })),
)
// Only non-zero classes get a wedge; the zero ones still get a legend row.
const drawn = computed(() => rows.value.filter((r) => r.count > 0))

function pt(angle: number, r: number): string {
  return `${(C + r * Math.cos(angle)).toFixed(3)},${(C + r * Math.sin(angle)).toFixed(3)}`
}

// arc builds one wedge (or ring segment when hole > 0) from a0 to a1 radians.
function arc(a0: number, a1: number): string {
  const ri = R * props.hole
  const large = a1 - a0 > Math.PI ? 1 : 0
  if (ri <= 0) {
    return `M${C},${C} L${pt(a0, R)} A${R},${R} 0 ${large} 1 ${pt(a1, R)} Z`
  }
  return (
    `M${pt(a0, R)} A${R},${R} 0 ${large} 1 ${pt(a1, R)}` +
    ` L${pt(a1, ri)} A${ri},${ri} 0 ${large} 0 ${pt(a0, ri)} Z`
  )
}

// A 2px gap of surface between neighbours, in radians at the outer edge. Slices
// too thin to survive the padding keep their full angle — a sliver that would
// vanish is worse than one that touches its neighbour.
const PAD = 2 / R

const wedges = computed(() => {
  const out: { key: string; d: string }[] = []
  let a = -Math.PI / 2 // 12 o'clock
  for (const r of drawn.value) {
    const span = r.frac * Math.PI * 2
    const pad = span > PAD * 3 && drawn.value.length > 1 ? PAD / 2 : 0
    out.push({ key: r.key, d: arc(a + pad, a + span - pad) })
    a += span
  }
  return out
})

// One class holding everything is a full circle: an arc whose ends coincide
// draws nothing, so it needs the circle primitive instead.
const solo = computed(() => (drawn.value.length === 1 ? drawn.value[0] : null))
const ringWidth = computed(() => R - R * props.hole)
const ringRadius = computed(() => (R + R * props.hole) / 2)

const active = computed(() => rows.value.find((r) => r.key === hover.value) ?? null)

const summary = computed(
  () =>
    `Status codes: ${rows.value.map((r) => `${r.label} ${r.count}`).join(', ')} of ${total.value} requests`,
)
</script>

<template>
  <div class="sp">
    <div class="sp-chart" :style="{ width: size + 'px', height: size + 'px' }">
      <svg viewBox="0 0 100 100" role="img" :aria-label="summary" class="sp-svg">
        <template v-if="total > 0">
          <template v-if="solo">
            <circle
              v-if="hole > 0"
              :cx="C" :cy="C" :r="ringRadius"
              :stroke-width="ringWidth"
              :class="['w', 'ring', 'w-' + solo.key]"
            />
            <circle v-else :cx="C" :cy="C" :r="R" :class="['w', 'w-' + solo.key]" />
          </template>
          <path
            v-for="w in wedges"
            v-else
            :key="w.key"
            :d="w.d"
            :class="['w', 'w-' + w.key, { dim: hover !== null && hover !== w.key, on: hover === w.key }]"
            @pointerenter="hover = w.key"
            @pointerleave="hover = null"
          />
        </template>
        <!-- No traffic at all: an empty track, so the card still has a shape. -->
        <circle
          v-else
          :cx="C" :cy="C" :r="ringRadius"
          fill="none" :stroke-width="ringWidth" class="w-empty"
        />
      </svg>

      <!-- The hole doubles as the readout: hovering a slice swaps the total for
           that class, which beats a floating tooltip over a 0.8° sliver. -->
      <div v-if="hole > 0" class="sp-center">
        <template v-if="active">
          <div class="sp-c-value">{{ pct(active.frac) }}</div>
          <div class="sp-c-label">{{ active.label }} · {{ fmtNum(active.count) }}</div>
        </template>
        <template v-else>
          <div class="sp-c-value">{{ fmtNum(total) }}</div>
          <div class="sp-c-label">{{ $t('analytics.requests') }}</div>
        </template>
      </div>
    </div>

    <ul class="sp-legend">
      <li
        v-for="r in rows"
        :key="r.key"
        :class="{ on: hover === r.key, zero: r.count === 0 }"
        @pointerenter="hover = r.key"
        @pointerleave="hover = null"
      >
        <i class="sp-key" :class="'k-' + r.key"></i>
        <span class="sp-l">{{ r.label }}</span>
        <span class="sp-hint">{{ r.hint }}</span>
        <b class="sp-n">{{ fmtNum(r.count) }}</b>
        <span class="sp-p">{{ pct(r.frac) }}</span>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.sp { display: flex; align-items: center; gap: 22px; flex-wrap: wrap; }
.sp-chart { position: relative; flex: 0 0 auto; }
.sp-svg { width: 100%; height: 100%; display: block; overflow: visible; }

/* The class carries the hue via `color`; the shape decides whether that lands on
   the fill or the stroke. Setting both would put a 1-unit outline on every wedge,
   which swallows the 2px gaps between them. */
.w { fill: currentColor; stroke: none; transition: opacity 0.15s, transform 0.15s; cursor: default; }
.w.ring { fill: none; stroke: currentColor; }
.w.dim { opacity: 0.4; }
/* The hovered wedge lifts out of the ring, so the highlight reads as "this one"
   rather than "everything else faded". */
.w.on { transform: scale(1.04); transform-origin: 50px 50px; transform-box: view-box; }
.w-2xx { color: var(--chart-2xx); }
.w-3xx { color: var(--chart-3xx); }
.w-4xx { color: var(--chart-4xx); }
.w-5xx { color: var(--chart-5xx); }
.w-empty { stroke: var(--border-primary); }

.sp-center {
  position: absolute; inset: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; pointer-events: none; text-align: center;
}
.sp-c-value { font-size: 22px; font-weight: 700; color: var(--text-primary); letter-spacing: -0.02em; }
.sp-c-label { font-size: 11px; color: var(--text-muted); margin-top: 1px; }

.sp-legend { flex: 1 1 190px; min-width: 190px; list-style: none; margin: 0; padding: 0; }
.sp-legend li {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 6px; margin: 0 -6px; border-radius: 6px;
  font-size: 13px; color: var(--text-secondary);
}
.sp-legend li.on { background: var(--bg-tertiary); }
.sp-legend li.zero { color: var(--text-muted); }
.sp-key { width: 9px; height: 9px; border-radius: 2px; flex: 0 0 auto; }
.k-2xx { background: var(--chart-2xx); }
.k-3xx { background: var(--chart-3xx); }
.k-4xx { background: var(--chart-4xx); }
.k-5xx { background: var(--chart-5xx); }
.sp-l { font-weight: 600; color: var(--text-primary); }
.sp-legend li.zero .sp-l { color: var(--text-muted); font-weight: 500; }
.sp-hint { flex: 1; color: var(--text-muted); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.sp-n { font-variant-numeric: tabular-nums; color: var(--text-primary); }
.sp-legend li.zero .sp-n { color: var(--text-muted); }
.sp-p { font-variant-numeric: tabular-nums; color: var(--text-muted); font-size: 12px; min-width: 46px; text-align: right; }
</style>
