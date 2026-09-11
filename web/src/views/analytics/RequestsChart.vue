<script setup lang="ts">
// Requests over time: one stacked column per bucket (success / 4xx / 5xx) on a
// real time axis. The series arrives gap-filled from the API, so column N is
// always one bucket after column N-1 and the x labels can be trusted.
//
// Hovering (or arrowing through) a column reads out every series for that
// bucket; the columns themselves stay the hit target, so there's no crosshair.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { AnalyticsSeriesPoint } from '@/api/analytics'
import { fmtNum, fmtMs, niceAxisMax } from './format'
import { buildTicks, bucketLabel, type Granularity } from './timeaxis'

const props = withDefaults(
  defineProps<{
    series: AnalyticsSeriesPoint[]
    granularity: Granularity
    height?: number
  }>(),
  { height: 200 },
)

const plot = ref<HTMLElement | null>(null)
const width = ref(720)
const hover = ref<number | null>(null)
let ro: ResizeObserver | null = null

onMounted(() => {
  if (!plot.value) return
  width.value = plot.value.clientWidth
  ro = new ResizeObserver((entries) => {
    width.value = entries[0].contentRect.width
  })
  ro.observe(plot.value)
})
onBeforeUnmount(() => ro?.disconnect())

const peak = computed(() => Math.max(0, ...props.series.map((p) => p.requests)))
const axisMax = computed(() => niceAxisMax(peak.value))
const times = computed(() => props.series.map((p) => new Date(p.t)))
const ticks = computed(() => buildTicks(times.value, props.granularity, width.value))

// Three gridlines is enough on a 200px plot; more is chart junk.
const yTicks = computed(() =>
  [1, 0.5, 0].map((f) => ({ f, pct: f * 100, label: fmtNum(axisMax.value * f) })),
)

interface Segment {
  cls: string
  h: number
}

// Columns are built top-down (5xx, 4xx, success) so the topmost non-empty
// segment can carry the rounded cap.
const columns = computed(() =>
  props.series.map((p) => {
    const e5 = Math.max(0, p.errors_5xx)
    const e4 = Math.max(0, p.errors_4xx)
    const ok = Math.max(0, p.requests - e4 - e5)
    const segs: Segment[] = [
      { cls: 'b-5xx', h: e5 },
      { cls: 'b-4xx', h: e4 },
      { cls: 'b-ok', h: ok },
    ]
      .filter((s) => s.h > 0)
      .map((s) => ({ cls: s.cls, h: (s.h / axisMax.value) * 100 }))
    return { empty: p.requests === 0, segs, ok, e4, e5 }
  }),
)

function centerPct(i: number): number {
  return ((i + 0.5) / Math.max(1, props.series.length)) * 100
}

function onMove(e: PointerEvent) {
  const el = plot.value
  if (!el || !props.series.length) return
  const r = el.getBoundingClientRect()
  const i = Math.floor(((e.clientX - r.left) / r.width) * props.series.length)
  hover.value = Math.min(props.series.length - 1, Math.max(0, i))
}

function onKey(e: KeyboardEvent) {
  const n = props.series.length
  if (!n) return
  if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
    const cur = hover.value ?? (e.key === 'ArrowLeft' ? n : -1)
    hover.value = Math.min(n - 1, Math.max(0, cur + (e.key === 'ArrowLeft' ? -1 : 1)))
    e.preventDefault()
  } else if (e.key === 'Escape') {
    hover.value = null
  }
}

const active = computed(() => {
  const i = hover.value
  if (i === null || !props.series[i]) return null
  return { i, point: props.series[i], col: columns.value[i], at: times.value[i] }
})

// Keep the tooltip inside the card: it centers on its column until it would
// run off an edge, then pins to that edge.
const tipStyle = computed(() => {
  const i = hover.value
  if (i === null) return {}
  const pct = centerPct(i)
  const shift = pct < 25 ? '0%' : pct > 75 ? '-100%' : '-50%'
  return { left: `${pct}%`, transform: `translateX(${shift})` }
})

const summary = computed(
  () =>
    `Requests per ${props.granularity} — ${props.series.length} buckets, peak ${fmtNum(peak.value)}`,
)
</script>

<template>
  <div class="rc">
    <!-- Height rides a custom property rather than grid-template-rows directly,
         so the narrow-screen clamp below still wins over the prop. -->
    <div class="rc-frame" :style="{ '--rc-height': height + 'px' }">
      <div class="rc-yaxis">
        <span v-for="t in yTicks" :key="t.f" class="rc-ylabel" :style="{ bottom: t.pct + '%' }">{{ t.label }}</span>
      </div>

      <div
        ref="plot"
        class="rc-plot"
        tabindex="0"
        role="img"
        :aria-label="summary"
        @pointermove="onMove"
        @pointerleave="hover = null"
        @blur="hover = null"
        @keydown="onKey"
      >
        <div class="rc-grid" aria-hidden="true">
          <i v-for="t in yTicks" :key="t.f" :style="{ bottom: t.pct + '%' }"></i>
        </div>

        <div class="rc-cols">
          <div
            v-for="(c, i) in columns"
            :key="i"
            class="rc-col"
            :class="{ on: hover === i }"
          >
            <div class="rc-stack">
              <div
                v-for="(s, si) in c.segs"
                :key="si"
                class="rc-bar"
                :class="[s.cls, { cap: si === 0, gap: si < c.segs.length - 1 }]"
                :style="{ height: s.h + '%' }"
              ></div>
              <div v-if="c.empty" class="rc-zero"></div>
            </div>
          </div>
        </div>

        <div v-if="active" class="rc-tip" :style="tipStyle">
          <div class="rc-tip-time">{{ bucketLabel(active.at, granularity) }}</div>
          <template v-if="!active.col.empty">
            <div class="rc-tip-row">
              <i class="k k-ok"></i><span>Success</span><b>{{ fmtNum(active.col.ok) }}</b>
            </div>
            <div class="rc-tip-row">
              <i class="k k-4xx"></i><span>Client errors</span><b>{{ fmtNum(active.col.e4) }}</b>
            </div>
            <div class="rc-tip-row">
              <i class="k k-5xx"></i><span>Server errors</span><b>{{ fmtNum(active.col.e5) }}</b>
            </div>
            <div class="rc-tip-foot">
              {{ fmtNum(active.point.requests) }} requests · p95 {{ fmtMs(active.point.p95_latency_ms) }}
            </div>
          </template>
          <!-- A quiet bucket has no percentiles to report — zeroes would read as data. -->
          <div v-else class="rc-tip-foot no-top">No traffic</div>
        </div>
      </div>

      <div class="rc-xaxis" aria-hidden="true">
        <span
          v-for="t in ticks"
          :key="t.i"
          class="rc-xlabel"
          :class="{ major: t.major, 'pin-l': centerPct(t.i) < 6, 'pin-r': centerPct(t.i) > 94 }"
          :style="{ left: centerPct(t.i) + '%' }"
        >{{ t.text }}</span>
      </div>
    </div>

    <div class="a-legend">
      <span><i class="dot dot-ok"></i> Success</span>
      <span><i class="dot dot-4xx"></i> Client errors (4xx)</span>
      <span><i class="dot dot-5xx"></i> Server errors (5xx)</span>
    </div>
  </div>
</template>

<style scoped>
/* y labels | plot, with the x axis under the plot only. */
.rc-frame { display: grid; grid-template-columns: auto 1fr; grid-template-rows: var(--rc-height, 200px) auto; column-gap: 8px; }

.rc-yaxis { position: relative; width: 38px; }
.rc-ylabel {
  position: absolute; right: 0; transform: translateY(50%);
  font-size: 11px; color: var(--text-muted); font-variant-numeric: tabular-nums; white-space: nowrap;
}

.rc-plot { position: relative; outline: none; }
.rc-plot:focus-visible { box-shadow: var(--shadow-focus); border-radius: 6px; }

.rc-grid { position: absolute; inset: 0; }
.rc-grid i { position: absolute; left: 0; right: 0; height: 1px; background: var(--border-primary); }

.rc-cols { position: absolute; inset: 0; display: flex; align-items: flex-end; gap: 2px; }
.rc-col { flex: 1 1 0; min-width: 2px; height: 100%; display: flex; align-items: flex-end; justify-content: center; border-radius: 3px; }
.rc-col.on { background: var(--bg-tertiary); }

/* The column keeps its share of the width for hovering, but the bar itself is
   capped — a 7-day range would otherwise draw 70px slabs. overflow guards the
   couple of px the inter-segment gaps add to a full-height column. */
.rc-stack { width: 100%; max-width: 26px; height: 100%; display: flex; flex-direction: column; justify-content: flex-end; overflow: hidden; }
.rc-bar { width: 100%; min-height: 2px; }
.rc-bar.cap { border-radius: 3px 3px 0 0; }
/* 2px of card surface separates touching segments — no strokes. */
.rc-bar.gap { margin-bottom: 2px; }
.b-ok { background: var(--chart-2xx); }
.b-4xx { background: var(--chart-4xx); }
.b-5xx { background: var(--chart-5xx); }
/* A bucket with no traffic keeps a baseline mark, so "zero" never reads as "missing". */
.rc-zero { height: 2px; background: var(--border-primary); border-radius: 1px; }

.rc-xaxis { position: relative; grid-column: 2; height: 20px; margin-top: 8px; }
.rc-xlabel {
  position: absolute; top: 0; transform: translateX(-50%);
  font-size: 11px; color: var(--text-muted); white-space: nowrap; font-variant-numeric: tabular-nums;
}
.rc-xlabel.major { color: var(--text-secondary); font-weight: 600; }
.rc-xlabel.pin-l { transform: none; }
.rc-xlabel.pin-r { transform: translateX(-100%); }

.rc-tip {
  position: absolute; top: 6px; z-index: 2; pointer-events: none;
  min-width: 168px; padding: 8px 10px;
  background: var(--bg-sidebar); color: #fff;
  border-radius: 8px; box-shadow: var(--shadow-md); font-size: 12px;
}
.rc-tip-time { font-size: 11px; opacity: 0.72; margin-bottom: 6px; white-space: nowrap; }
.rc-tip-row { display: flex; align-items: center; gap: 7px; line-height: 1.7; }
.rc-tip-row span { flex: 1; opacity: 0.72; }
.rc-tip-row b { font-variant-numeric: tabular-nums; }
.rc-tip-foot { margin-top: 6px; padding-top: 6px; border-top: 1px solid rgba(255, 255, 255, 0.14); font-size: 11px; opacity: 0.72; white-space: nowrap; }
.rc-tip-foot.no-top { margin-top: 0; padding-top: 0; border-top: none; }
.k { width: 10px; height: 2px; border-radius: 1px; flex: 0 0 auto; }
.k-ok { background: var(--chart-2xx); }
.k-4xx { background: var(--chart-4xx); }
.k-5xx { background: var(--chart-5xx); }

@media (max-width: 639px) {
  .rc-frame { grid-template-rows: min(var(--rc-height, 200px), 160px) auto; }
}
</style>
