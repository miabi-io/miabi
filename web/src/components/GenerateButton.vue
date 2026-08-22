<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The inline affordance beside a field that already collects a secret. A page is
// a tool you go to; this is the tool already being where the value is needed,
// which is the common case.
//
// It opens the same GeneratorPanel the page and the modal use, so options and
// behaviour cannot drift between the three.
import { onBeforeUnmount, onMounted, ref } from 'vue'
import GeneratorPanel from '@/components/GeneratorPanel.vue'

const props = withDefaults(
  defineProps<{
    /** Shown as a tooltip; name the field so the button reads unambiguously. */
    label?: string
    disabled?: boolean
  }>(),
  { label: 'Generate a value', disabled: false },
)

const emit = defineEmits<{ (e: 'generated', value: string): void }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

function use(value: string) {
  if (!value) return
  emit('generated', value)
  open.value = false
}

// Dismiss on an outside click or Escape. A popover that can only be closed by
// the button that opened it traps someone who clicked it by accident.
function onDocumentClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    open.value = false
    e.stopPropagation()
  }
}
onMounted(() => {
  document.addEventListener('click', onDocumentClick)
  document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocumentClick)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <span ref="root" class="gen-btn-wrap">
    <button type="button" class="btn-icon btn-icon-muted" :title="label" :aria-label="label" :disabled="disabled"
      :aria-expanded="open" @click="open = !open">
      <span class="mdi mdi-auto-fix"></span>
    </button>

    <div v-if="open" class="gen-pop" role="dialog" :aria-label="label">
      <GeneratorPanel compact @use="use">
        <template #actions="{ value, regenerate }">
          <button type="button" class="btn btn-primary btn-sm" :disabled="!value" @click="use(value)">
            Use this value
          </button>
          <button type="button" class="btn btn-secondary btn-sm" @click="regenerate">Another</button>
        </template>
      </GeneratorPanel>
    </div>
  </span>
</template>

<style scoped>
.gen-btn-wrap { position: relative; display: inline-flex; }
.gen-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: 60; width: 340px; max-width: 90vw;
  padding: 14px; border: 1px solid var(--border-primary); border-radius: 10px;
  background: var(--bg-secondary, #111827); box-shadow: 0 12px 32px rgba(0, 0, 0, 0.45);
}
</style>
