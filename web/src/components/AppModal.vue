<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The overlay and dialog frame every modal shares. It owns the behaviour rather
// than the layout: callers keep their own .modal-header / .modal-body /
// .modal-footer markup in the default slot, so adopting it is a two-line change
// at each site.
//
// Nothing here dismisses the dialog except the caller. Clicking the backdrop
// does nothing on purpose — an errant click beside a form used to discard
// everything typed into it.
import { computed, nextTick, ref, watch } from 'vue'
import { useModal } from '@/composables/useModal'

const props = withDefaults(
  defineProps<{
    /** Usually left unset: put v-if on the component instead, which keeps the
     * dialog out of the DOM until it is shown and preserves the type narrowing
     * its content relies on. */
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

// Name the dialog after its own heading. Every modal in the app puts one in its
// header, and wiring it here spares each caller from inventing an id.
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
