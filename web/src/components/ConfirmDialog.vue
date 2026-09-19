<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import AppModal from '@/components/AppModal.vue'
import { useI18n } from 'vue-i18n'

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
    // Empty rather than English: the fallback is translated at render, where t() is
    // reactive and a language change updates an open dialog.
    confirmLabel: '',
    cancelLabel: '',
    variant: 'primary',
    busy: false,
    confirmDisabled: false,
  },
)

const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()
const { t } = useI18n()

// A stable id ties the dialog to its heading for aria-labelledby.
let seq = 0
const titleId = `confirm-title-${seq++}`
const confirmBtn = ref<HTMLButtonElement | null>(null)
const cancelBtn = ref<HTMLButtonElement | null>(null)

// Escape cancels, unless the dialog is mid-action; on open, focus lands on the
// primary action rather than on the page behind it. Only the buttons dismiss
// this dialog — clicking the backdrop does nothing, so a confirmation cannot be
// waved away by an errant click.


// Focus goes to the safe choice on a destructive prompt: Enter should not be
// able to delete something the user has only just been asked about. Everywhere
// else the primary action is the one they came for.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    nextTick(() => (props.variant === 'danger' ? cancelBtn.value : confirmBtn.value)?.focus())
  },
)
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" elevated max-width="460px" :escapable="!busy" :auto-focus="false" @close="emit('cancel')">
        <div class="modal-header">
          <h3 :id="titleId">{{ title }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close dialog" @click="emit('cancel')"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p v-if="message" class="confirm-message">{{ message }}</p>
          <slot></slot>
        </div>
        <div class="modal-footer">
          <button ref="cancelBtn" type="button" class="btn btn-secondary" :disabled="busy" @click="emit('cancel')">{{ cancelLabel || t('action.cancel') }}</button>
          <button
            ref="confirmBtn"
            type="button"
            class="btn"
            :class="variant === 'danger' ? 'btn-danger' : 'btn-primary'"
            :disabled="busy || confirmDisabled"
            @click="emit('confirm')"
          >
            {{ busy ? t('action.working') : (confirmLabel || t('action.confirm')) }}
          </button>
        </div>
    </AppModal>
  </Teleport>
</template>

<style scoped>
/* pre-line so a message can carry a second paragraph (e.g. the consequence of an
   action, which is often the part that actually needs saying). Single-line messages
   are unaffected. */
.confirm-message { color: var(--text-secondary); font-size: 14px; line-height: 1.5; margin: 0; white-space: pre-line; }
</style>
