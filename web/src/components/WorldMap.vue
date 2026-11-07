<script setup lang="ts">
// No tiles or network; ALPHA2_TO_NUMERIC maps Goma's alpha-2 codes onto the topojson's numeric ids.
import { computed, ref } from 'vue'
import { geoNaturalEarth1, geoPath } from 'd3-geo'
import { feature } from 'topojson-client'
import world from 'world-atlas/countries-110m.json'
import { ALPHA2_TO_NUMERIC } from '@/api/countryCodes'
import { countryName } from '@/views/analytics/format'

// highlight outlines one country (alpha-2); select fires with a clicked country's alpha-2.
const props = defineProps<{ countries: { label: string; count: number }[]; highlight?: string | null }>()
const emit = defineEmits<{ select: [alpha2: string] }>()

const NUMERIC_TO_ALPHA2 = Object.fromEntries(Object.entries(ALPHA2_TO_NUMERIC).map(([a, n]) => [n, a]))

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

// Drawn again on top of the rest, so its outline isn't covered by a neighbour's.
const highlighted = computed(() => {
  const num = props.highlight ? ALPHA2_TO_NUMERIC[props.highlight.toUpperCase()] : undefined
  return num ? paths.find((p) => p.id === num) ?? null : null
})

function onClick(p: { id: string }) {
  const alpha2 = NUMERIC_TO_ALPHA2[p.id]
  if (alpha2 && byId.value[p.id]) emit('select', alpha2)
}

function fillFor(id: string): string {
  const c = byId.value[id]
  if (!c) return '' // fall back to CSS default fill
  // sqrt scale so mid-volume countries stay visible against a few hot ones.
  const pct = 20 + 80 * Math.sqrt(c / max.value)
  return `color-mix(in srgb, var(--primary-500) ${pct.toFixed(1)}%, transparent)`
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
    <svg class="worldmap" :viewBox="`0 0 ${W} ${H}`" role="img" :aria-label="$t('analytics.requestsByCountry')">
      <path
        v-for="p in paths"
        :key="p.id"
        class="country"
        :class="{ 'has-data': !!byId[p.id] }"
        :d="p.d"
        :style="byId[p.id] ? { fill: fillFor(p.id) } : undefined"
        @mousemove="onHover($event, p)"
        @click="onClick(p)"
      />
      <path
        v-if="highlighted"
        class="country country-hl"
        :d="highlighted.d"
        :style="byId[highlighted.id] ? { fill: fillFor(highlighted.id) } : undefined"
      />
    </svg>
    <div v-if="tip" class="worldmap-tip" :style="{ left: tip.x + 'px', top: tip.y + 'px' }">{{ tip.text }}</div>
    <div class="worldmap-legend"><span>{{ $t('analytics.fewer') }}</span><span class="scale"></span><span>{{ $t('analytics.moreRequests') }}</span></div>
  </div>
</template>
