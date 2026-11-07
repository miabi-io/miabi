<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'

withDefaults(
  defineProps<{
    // Each item describes one option of the field.
    items: { label: string; description: string }[]
    title?: string
    placement?: 'top' | 'bottom'
  }>(),
  { placement: 'bottom' },
)

const open = ref(false)
const root = ref<HTMLElement | null>(null)

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) close()
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

function toggle() {
  open.value ? close() : openPopover()
}
function openPopover() {
  open.value = true
  document.addEventListener('click', onDocClick, true)
  document.addEventListener('keydown', onKey)
}
function close() {
  open.value = false
  document.removeEventListener('click', onDocClick, true)
  document.removeEventListener('keydown', onKey)
}

onBeforeUnmount(close)
</script>

<template>
  <span ref="root" class="field-info">
    <button
      type="button"
      class="info-btn"
      :class="{ active: open }"
      :title="title || 'What do these mean?'"
      :aria-label="title || 'What do these mean?'"
      :aria-expanded="open"
      @click.stop="toggle"
    >
      <span class="mdi mdi-information-outline"></span>
    </button>
    <Transition name="info-fade">
      <div v-if="open" class="info-pop" :class="`info-pop--${placement}`" @click.stop>
        <div v-for="it in items" :key="it.label" class="info-row">
          <span class="info-label">{{ it.label }}</span>
          <span class="info-desc">{{ it.description }}</span>
        </div>
      </div>
    </Transition>
  </span>
</template>

<style scoped>
.field-info {
  position: relative;
  display: inline-flex;
  vertical-align: middle;
}
.info-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  transition: color 0.12s, background 0.12s;
}
.info-btn:hover,
.info-btn.active {
  color: var(--primary-600);
  background: var(--primary-50);
}
.info-btn .mdi {
  font-size: 16px;
}
.info-pop {
  position: absolute;
  /* The icon sits near the form's left, so centering would push the card off-screen. */
  left: 0;
  z-index: 20;
  width: 300px;
  max-width: min(300px, 80vw);
  padding: 12px 14px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg, var(--shadow-md));
  text-align: left;
  white-space: normal;
}
.info-pop--bottom {
  top: calc(100% + 6px);
}
.info-pop--top {
  bottom: calc(100% + 6px);
}
.info-row {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.info-row + .info-row {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--border-secondary);
}
.info-label {
  font-size: 12px;
  font-weight: 700;
  color: var(--text-primary);
}
.info-desc {
  font-size: 12px;
  line-height: 1.45;
  color: var(--text-secondary);
}

.info-fade-enter-active,
.info-fade-leave-active {
  transition: opacity 0.12s ease, transform 0.12s ease;
}
.info-fade-enter-from,
.info-fade-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}
</style>
