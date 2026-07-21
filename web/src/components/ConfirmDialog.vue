<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    message?: string
    confirmLabel?: string
    cancelLabel?: string
    variant?: 'danger' | 'primary'
    busy?: boolean
    confirmDisabled?: boolean
  }>(),
  {
    message: '',
    confirmLabel: 'Confirm',
    cancelLabel: 'Cancel',
    variant: 'primary',
    busy: false,
    confirmDisabled: false,
  },
)

const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()

// A stable id ties the dialog to its heading for aria-labelledby.
let seq = 0
const titleId = `confirm-title-${seq++}`
const confirmBtn = ref<HTMLButtonElement | null>(null)

// Escape cancels; on open, move focus into the dialog so keyboard users land
// on the primary action rather than being stranded on the page behind it.
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && !props.busy) emit('cancel')
}
watch(
  () => props.open,
  (open) => {
    if (open) nextTick(() => confirmBtn.value?.focus())
  },
)
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay modal-overlay-elevated" @click.self="emit('cancel')" @keydown="onKeydown">
      <div
        class="modal"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        style="max-width: 460px; width: 100%"
      >
        <div class="modal-header">
          <h3 :id="titleId">{{ title }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close dialog" @click="emit('cancel')"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p v-if="message" class="confirm-message">{{ message }}</p>
          <slot></slot>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="emit('cancel')">{{ cancelLabel }}</button>
          <button
            ref="confirmBtn"
            type="button"
            class="btn"
            :class="variant === 'danger' ? 'btn-danger' : 'btn-primary'"
            :disabled="busy || confirmDisabled"
            @click="emit('confirm')"
          >
            {{ busy ? 'Working…' : confirmLabel }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* pre-line so a message can carry a second paragraph (e.g. the consequence of an
   action, which is often the part that actually needs saying). Single-line messages
   are unaffected. */
.confirm-message { color: var(--text-secondary); font-size: 14px; line-height: 1.5; margin: 0; white-space: pre-line; }
</style>
