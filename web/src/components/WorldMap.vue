<script setup lang="ts">
// Self-contained world choropleth: renders the world-atlas topojson via d3-geo
// (no tiles, no network) and shades each country by request volume. Goma emits
// ISO alpha-2 codes; ALPHA2_TO_NUMERIC maps them onto the topojson's numeric ids.
import { computed, ref } from 'vue'
import { geoNaturalEarth1, geoPath } from 'd3-geo'
import { feature } from 'topojson-client'
import world from 'world-atlas/countries-110m.json'
import { ALPHA2_TO_NUMERIC } from '@/api/countryCodes'
import { countryName } from '@/views/analytics/format'

const props = defineProps<{ countries: { label: string; count: number }[] }>()

const W = 960
const H = 480

// Project the country geometry once — it never changes, only the coloring does.
const paths = (() => {
  const fc = feature(world as never, (world as never as { objects: { countries: unknown } }).objects.countries as never) as unknown as {
    features: Array<{ id: string; properties: { name: string } }>
  }
  const projection = geoNaturalEarth1().fitSize([W, H], fc as never)
  const path = geoPath(projection)
  return fc.features.map((f) => ({
    id: String(f.id),
    name: f.properties?.name ?? '',
    d: path(f as never) ?? '',
  }))
})()

// numeric-id -> request count, and the max, from the alpha-2 breakdown.
const byId = computed(() => {
  const m: Record<string, number> = {}
  for (const c of props.countries) {
    const num = ALPHA2_TO_NUMERIC[(c.label || '').toUpperCase()]
    if (num) m[num] = (m[num] || 0) + c.count
  }
  return m
})
const max = computed(() => Math.max(1, ...Object.values(byId.value)))

function fillFor(id: string): string {
  const c = byId.value[id]
  if (!c) return '' // fall back to CSS default fill
  // sqrt scale so mid-volume countries stay visible against a few hot ones.
  const alpha = 0.2 + 0.8 * Math.sqrt(c / max.value)
  return `rgba(168, 85, 247, ${alpha.toFixed(3)})`
}

const tip = ref<{ x: number; y: number; text: string } | null>(null)
function onHover(e: MouseEvent, p: { id: string; name: string }) {
  const c = byId.value[p.id]
  if (!c) {
    tip.value = null
    return
  }
  const host = (e.currentTarget as SVGElement).ownerSVGElement?.parentElement
  const rect = host?.getBoundingClientRect()
  tip.value = {
    x: e.clientX - (rect?.left ?? 0),
    y: e.clientY - (rect?.top ?? 0),
    text: `${p.name || countryName(p.id)}: ${c.toLocaleString()}`,
  }
}
</script>

<template>
  <div class="worldmap-wrap" @mouseleave="tip = null">
    <svg class="worldmap" :viewBox="`0 0 ${W} ${H}`" role="img" aria-label="Requests by country">
      <path
        v-for="p in paths"
        :key="p.id"
        class="country"
        :class="{ 'has-data': !!byId[p.id] }"
        :d="p.d"
        :style="byId[p.id] ? { fill: fillFor(p.id) } : undefined"
        @mousemove="onHover($event, p)"
      />
    </svg>
    <div v-if="tip" class="worldmap-tip" :style="{ left: tip.x + 'px', top: tip.y + 'px' }">{{ tip.text }}</div>
    <div class="worldmap-legend"><span>Fewer</span><span class="scale"></span><span>More requests</span></div>
  </div>
</template>
