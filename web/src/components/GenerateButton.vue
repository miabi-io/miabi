<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The inline affordance beside a field that already collects a secret. A page is
// a tool you go to; this is the tool already being where the value is needed,
// which is the common case.
//
// It opens the same GeneratorPanel the page and the modal use, so options and
// behaviour cannot drift between the three.
import { ref } from 'vue'
import GeneratorModal from '@/components/GeneratorModal.vue'

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

function use(value: string) {
  if (!value) return
  emit('generated', value)
  open.value = false
}
</script>

<template>
  <span class="gen-btn-wrap">
    <button type="button" class="btn-icon btn-icon-muted" :title="label" :aria-label="label" :disabled="disabled"
      :aria-expanded="open" @click="open = true">
      <span class="mdi mdi-auto-fix"></span>
    </button>

    <GeneratorModal :open="open" :title="label" @close="open = false" @use="use" />
  </span>
</template>

<style scoped>
.gen-btn-wrap { position: relative; display: inline-flex; }
</style>