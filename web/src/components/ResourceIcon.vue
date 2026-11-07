<script setup lang="ts">
import { ref, computed } from 'vue'

// The avatar colour is hashed from the name so it is stable per resource.
const props = withDefaults(
  defineProps<{ src?: string; mdi?: string; name?: string; size?: number }>(),
  { src: '', mdi: '', name: '', size: 40 },
)

const failed = ref(false)
const showImg = computed(() => !!props.src && !failed.value)

const initials = computed(() => {
  const n = (props.name || '').trim()
  if (!n) return '?'
  const parts = n.split(/\s+/)
  return (parts.length > 1 ? parts[0][0] + parts[1][0] : n.slice(0, 2)).toUpperCase()
})

const bg = computed(() => {
  const n = props.name || ''
  let h = 0
  for (let i = 0; i < n.length; i++) h = (h * 31 + n.charCodeAt(i)) >>> 0
  return `hsl(${h % 360}, 52%, 45%)`
})

const px = computed(() => `${props.size}px`)
const fontPx = computed(() => `${Math.round(props.size * 0.42)}px`)
</script>

<template>
  <span class="resource-icon" :style="{ width: px, height: px }">
    <img v-if="showImg" :src="src" :alt="name || ''" loading="lazy" @error="failed = true" />
    <span v-else-if="mdi" class="mdi resource-icon-glyph" :class="mdi" :style="{ fontSize: fontPx }"></span>
    <span v-else class="resource-icon-initials" :style="{ background: bg, fontSize: fontPx }">{{ initials }}</span>
  </span>
</template>

<style scoped>
.resource-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  overflow: hidden;
  flex: none;
  background: var(--surface-2, rgba(127, 127, 127, 0.08));
}
.resource-icon img {
  width: 68%;
  height: 68%;
  object-fit: contain;
}
.resource-icon-glyph {
  color: var(--text-muted, #888);
}
.resource-icon-initials {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  color: #fff;
  font-weight: 600;
}
</style>
