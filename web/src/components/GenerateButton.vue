<script setup lang="ts">
// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Opens the same GeneratorPanel as the page and the modal, so behaviour cannot drift between them.
import { ref } from 'vue'
import GeneratorModal from '@/components/GeneratorModal.vue'

const props = withDefaults(
  defineProps<{
    /** Shown as a tooltip; name the field so the button reads unambiguously. */
    label?: string
    disabled?: boolean
  }>(),
  { label: '', disabled: false },
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
    <button type="button" class="btn-icon btn-icon-muted" :title="label || $t('generator.generateAValue')" :aria-label="label || $t('generator.generateAValue')" :disabled="disabled"
      :aria-expanded="open" @click="open = true">
      <span class="mdi mdi-auto-fix"></span>
    </button>

    <GeneratorModal :open="open" :title="label || $t('generator.generateAValue')" @close="open = false" @use="use" />
  </span>
</template>

<style scoped>
.gen-btn-wrap { position: relative; display: inline-flex; }
</style>