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

// Only the caller dismisses: a backdrop click does nothing, since an errant click used to discard a typed form.
import { computed, nextTick, ref, watch } from 'vue'
import { useModal } from '@/composables/useModal'

const props = withDefaults(
  defineProps<{
    /** Usually left unset: v-if keeps the dialog out of the DOM and preserves its content's type narrowing. */
    open?: boolean
    /** Extra classes for the dialog, e.g. "modal-lg" or a per-view class. */
    dialogClass?: string
    /** Width override, e.g. "560px", for dialogs wider than the default. */
    maxWidth?: string
    /** Stack above another open modal (a confirm raised from inside a form). */
    elevated?: boolean
    /** Set false while an action is in flight, so Escape cannot interrupt it. */
    escapable?: boolean
    /** Set false when the content places initial focus itself. */
    autoFocus?: boolean
    /** Drop the base .modal box, for a dialog that brings its own frame. */
    bare?: boolean
  }>(),
  {
    open: true,
    dialogClass: '',
    maxWidth: '',
    elevated: false,
    escapable: true,
    autoFocus: true,
    bare: false,
  },
)

const emit = defineEmits<{ (e: 'close'): void }>()

const dialog = ref<HTMLElement | null>(null)
const labelledBy = ref<string | undefined>(undefined)

let seq = 0
const fallbackId = `app-modal-title-${seq++}`

const style = computed(() => (props.maxWidth ? { maxWidth: props.maxWidth, width: '100%' } : undefined))

useModal(
  () => props.open,
  {
    onRequestClose: () => emit('close'),
    container: dialog,
    escapable: () => props.escapable,
    autoFocus: props.autoFocus,
  },
)

// Every modal has a header heading, so labelling here spares each caller from inventing an id.
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    await nextTick()
    const heading = dialog.value?.querySelector<HTMLElement>('.modal-header h3, .modal-header h2')
    if (!heading) {
      labelledBy.value = undefined
      return
    }
    if (!heading.id) heading.id = fallbackId
    labelledBy.value = heading.id
  },
  { immediate: true },
)
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" :class="{ 'modal-overlay-elevated': elevated }">
      <div
        ref="dialog"
        :class="[bare ? '' : 'modal', dialogClass]"
        :style="style"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="labelledBy"
      >
        <slot />
      </div>
    </div>
  </Teleport>
</template>
