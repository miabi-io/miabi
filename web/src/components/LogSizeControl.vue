<script setup lang="ts">
import { LOG_SIZES, type LogSize } from '@/composables/useLogSize'

// The glyph is the control's whole label, so it shows the shape it produces: one
// block, same width, growing upward off a shared baseline. A framed "panel with
// fill" was the first attempt and turned to mush at 15px — at this size the
// silhouette has to carry the meaning on its own.
const BAR: Record<LogSize, number> = { small: 4, medium: 8, large: 12 }
const BASELINE = 14

const size = defineModel<LogSize>({ required: true })
</script>

<template>
  <div class="log-size" role="group" aria-label="Log panel size">
    <button
      v-for="s in LOG_SIZES"
      :key="s.value"
      type="button"
      class="log-size-opt"
      :class="{ active: size === s.value }"
      :aria-pressed="size === s.value"
      :aria-label="s.title"
      :title="s.title"
      @click="size = s.value"
    >
      <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
        <rect x="2" :y="BASELINE - BAR[s.value]" width="12" :height="BAR[s.value]" rx="1.5" fill="currentColor" />
      </svg>
    </button>
  </div>
</template>

<style scoped>
/* An inset track with a raised chip for the active option, rather than three
   adjacent bordered buttons — it reads as one control at toolbar size. */
.log-size {
  display: inline-flex;
  gap: 2px;
  padding: 2px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}
.log-size-opt {
  display: grid;
  place-items: center;
  width: 28px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}
.log-size-opt:hover:not(.active) {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.log-size-opt.active {
  background: var(--bg-primary);
  color: var(--primary-600);
  box-shadow: var(--shadow-sm);
}
/* The lift comes from a shadow, which reads as nothing on a dark surface — the
   raised chip needs an edge there instead. */
[data-theme='dark'] .log-size-opt.active {
  box-shadow: inset 0 0 0 1px var(--border-primary);
}
.log-size-opt:focus-visible {
  outline: 2px solid var(--border-focus);
  outline-offset: 1px;
}
</style>
