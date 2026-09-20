<script setup lang="ts">
// p95 latency over time, on the same time axis as the requests chart.
//
// Buckets with no traffic have no latency to report, so the line breaks there
// instead of diving to zero — a quiet hour is missing data, not a fast one.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { AnalyticsSeriesPoint } from '@/api/analytics'
import { fmtMs, fmtNum, niceAxisMax } from './format'
import { buildTicks, bucketLabel, type Granularity } from './timeaxis'

const props = defineProps<{
  series: AnalyticsSeriesPoint[]
  granularity: Granularity
  height?: number
}>()

const H = computed(() => props.height ?? 180)
const plot = ref<HTMLElement | null>(null)
const width = ref(600)
const hover = ref<number | null>(null)
let ro: ResizeObserver | null = null

onMounted(() => {
  if (!plot.value) return
  width.value = plot.value.clientWidth
  ro = new ResizeObserver((entries) => {
    width.value = Math.max(1, entries[0].contentRect.width)
  })
  ro.observe(plot.value)
})
onBeforeUnmount(() => ro?.disconnect())

// null = no traffic in that bucket, so no latency to plot.
const values = computed(() => props.series.map((p) => (p.requests > 0 ? p.p95_latency_ms : null)))
const axisMax = computed(() => niceAxisMax(Math.max(0, ...values.value.filter((v): v is number => v !== null))))
const times = computed(() => props.series.map((p) => new Date(p.t)))
const ticks = computed(() => buildTicks(times.value, props.granularity, width.value))
const yTicks = computed(() =>
  [1, 0.5, 0].map((f) => ({ f, pct: f * 100, label: fmtMs(axisMax.value * f) })),
)

const n = computed(() => Math.max(1, props.series.length))
function x(i: number): number {
  return ((i + 0.5) / n.value) * width.value
}
function y(v: number): number {
  return H.value - (v / axisMax.value) * (H.value - 4) - 2
}
function centerPct(i: number): number {
  return ((i + 0.5) / n.value) * 100
}

// One subpath per run of consecutive buckets that have traffic.
const runs = computed(() => {
  const out: { i: number; v: number }[][] = []
  let cur: { i: number; v: number }[] = []
  values.value.forEach((v, i) => {
    if (v === null) {
      if (cur.length) out.push(cur)
      cur = []
    } else {
      cur.push({ i, v })
    }
  })
  if (cur.length) out.push(cur)
  return out
})

const lines = computed(() =>
  runs.value
    .filter((r) => r.length > 1)
    .map((r) => r.map((p, k) => `${k ? 'L' : 'M'}${x(p.i).toFixed(1)},${y(p.v).toFixed(1)}`).join(' ')),
)
// Single-bucket runs would draw nothing, so they get a dot instead.
const dots = computed(() => runs.value.filter((r) => r.length === 1).map((r) => ({ x: x(r[0].i), y: y(r[0].v) })))
const areas = computed(() =>
  lines.value.map((d, k) => {
    const r = runs.value.filter((rr) => rr.length > 1)[k]
    return `${d} L${x(r[r.length - 1].i).toFixed(1)},${H.value} L${x(r[0].i).toFixed(1)},${H.value} Z`
  }),
)

function onMove(e: PointerEvent) {
  const el = plot.value
  if (!el || !props.series.length) return
  const r = el.getBoundingClientRect()
  const i = Math.floor(((e.clientX - r.left) / r.width) * props.series.length)
  hover.value = Math.min(props.series.length - 1, Math.max(0, i))
}

function onKey(e: KeyboardEvent) {
  const len = props.series.length
  if (!len) return
  if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
    const cur = hover.value ?? (e.key === 'ArrowLeft' ? len : -1)
    hover.value = Math.min(len - 1, Math.max(0, cur + (e.key === 'ArrowLeft' ? -1 : 1)))
    e.preventDefault()
  } else if (e.key === 'Escape') {
    hover.value = null
  }
}

const active = computed(() => {
  const i = hover.value
  if (i === null || !props.series[i]) return null
  return { i, point: props.series[i], at: times.value[i], v: values.value[i] }
})

const tipStyle = computed(() => {
  const i = hover.value
  if (i === null) return {}
  const pct = centerPct(i)
  const shift = pct < 25 ? '0%' : pct > 75 ? '-100%' : '-50%'
  return { left: `${pct}%`, transform: `translateX(${shift})` }
})

const summary = computed(() => `p95 latency per ${props.granularity} over ${props.series.length} buckets`)
</script>

<template>
  <div class="lc">
    <div class="lc-frame" :style="{ gridTemplateRows: H + 'px auto' }">
      <div class="lc-yaxis">
        <span v-for="t in yTicks" :key="t.f" class="lc-ylabel" :style="{ bottom: t.pct + '%' }">{{ t.label }}</span>
      </div>

      <div
        ref="plot"
        class="lc-plot"
        tabindex="0"
        role="img"
        :aria-label="summary"
        @pointermove="onMove"
        @pointerleave="hover = null"
        @blur="hover = null"
        @keydown="onKey"
      >
        <div class="lc-grid" aria-hidden="true">
          <i v-for="t in yTicks" :key="t.f" :style="{ bottom: t.pct + '%' }"></i>
        </div>

        <svg class="lc-svg" :width="width" :height="H" :viewBox="`0 0 ${width} ${H}`" aria-hidden="true">
          <path v-for="(d, k) in areas" :key="'a' + k" :d="d" class="lc-area" />
          <path v-for="(d, k) in lines" :key="'l' + k" :d="d" class="lc-line" />
          <circle v-for="(p, k) in dots" :key="'d' + k" :cx="p.x" :cy="p.y" r="2.5" class="lc-dot" />
          <template v-if="active">
            <line class="lc-cross" :x1="x(active.i)" :x2="x(active.i)" y1="0" :y2="H" />
            <circle v-if="active.v !== null" class="lc-marker" :cx="x(active.i)" :cy="y(active.v)" r="4" />
          </template>
        </svg>

        <div v-if="active" class="lc-tip" :style="tipStyle">
          <div class="lc-tip-time">{{ bucketLabel(active.at, granularity) }}</div>
          <template v-if="active.v !== null">
            <div class="lc-tip-row"><i class="k"></i><span>p95</span><b>{{ fmtMs(active.v) }}</b></div>
            <div class="lc-tip-row"><i class="k k-avg"></i><span>{{ $t('analytics.average') }}</span><b>{{ fmtMs(active.point.avg_latency_ms) }}</b></div>
            <div class="lc-tip-foot">{{ fmtNum(active.point.requests) }} requests</div>
          </template>
          <div v-else class="lc-tip-foot no-top">{{ $t('analytics.noTraffic') }}</div>
        </div>
      </div>

      <div class="lc-xaxis" aria-hidden="true">
        <span
          v-for="t in ticks"
          :key="t.i"
          class="lc-xlabel"
          :class="{ major: t.major, 'pin-l': centerPct(t.i) < 6, 'pin-r': centerPct(t.i) > 94 }"
          :style="{ left: centerPct(t.i) + '%' }"
        >{{ t.text }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.lc-frame { display: grid; grid-template-columns: auto 1fr; column-gap: 8px; }
.lc-yaxis { position: relative; width: 44px; }
.lc-ylabel {
  position: absolute; right: 0; transform: translateY(50%);
  font-size: 11px; color: var(--text-muted); white-space: nowrap; font-variant-numeric: tabular-nums;
}

.lc-plot { position: relative; outline: none; }
.lc-plot:focus-visible { box-shadow: var(--shadow-focus); border-radius: 6px; }
.lc-grid { position: absolute; inset: 0; }
.lc-grid i { position: absolute; left: 0; right: 0; height: 1px; background: var(--border-primary); }
.lc-svg { position: relative; display: block; max-width: 100%; }

.lc-line { fill: none; stroke: var(--warning-600); stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
.lc-area { fill: var(--warning-600); opacity: 0.1; }
.lc-dot { fill: var(--warning-600); }
.lc-cross { stroke: var(--border-primary); stroke-width: 1; }
.lc-marker { fill: var(--warning-600); stroke: var(--bg-primary); stroke-width: 2; }

.lc-xaxis { position: relative; grid-column: 2; height: 20px; margin-top: 8px; }
.lc-xlabel {
  position: absolute; top: 0; transform: translateX(-50%);
  font-size: 11px; color: var(--text-muted); white-space: nowrap; font-variant-numeric: tabular-nums;
}
.lc-xlabel.major { color: var(--text-secondary); font-weight: 600; }
.lc-xlabel.pin-l { transform: none; }
.lc-xlabel.pin-r { transform: translateX(-100%); }

.lc-tip {
  position: absolute; top: 6px; z-index: 2; pointer-events: none;
  min-width: 150px; padding: 8px 10px;
  background: var(--bg-sidebar); color: #fff; border-radius: 8px;
  box-shadow: var(--shadow-md); font-size: 12px;
}
.lc-tip-time { font-size: 11px; opacity: 0.72; margin-bottom: 6px; white-space: nowrap; }
.lc-tip-row { display: flex; align-items: center; gap: 7px; line-height: 1.7; }
.lc-tip-row span { flex: 1; opacity: 0.72; }
.lc-tip-row b { font-variant-numeric: tabular-nums; }
.lc-tip-foot { margin-top: 6px; padding-top: 6px; border-top: 1px solid rgba(255, 255, 255, 0.14); font-size: 11px; opacity: 0.72; }
.lc-tip-foot.no-top { margin-top: 0; padding-top: 0; border-top: none; }
.k { width: 10px; height: 2px; border-radius: 1px; flex: 0 0 auto; background: var(--warning-600); }
.k-avg { opacity: 0.45; }
</style>
