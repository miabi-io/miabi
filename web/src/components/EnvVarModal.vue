<script setup lang="ts">
// Add/update one environment variable. Shared by the application Environment tab
// and the stack's shared environment, so both spell the same rules the same way:
// the key is immutable on update, a secret's value is never sent back to the
// browser and has to be re-entered, and either kind can reference a workspace
// secret.
//
// The parent owns the API call and the `saving` flag; this only collects values.
import { nextTick, ref, watch } from 'vue'
import { useDirtyGuard } from '@/composables/useModal'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{
  open: boolean
  // Key being updated, or null when adding. Also the "is this an edit?" signal.
  editingKey: string | null
  // Seed values; a secret's value arrives blank because the API never returns it.
  initial: { key: string; value: string; secret: boolean }
  saving: boolean
  // What happens after saving, e.g. "Applies on the next deploy." Empty when the
  // change takes effect immediately.
  applyNote?: string
}>()

const emit = defineEmits<{
  close: []
  save: [value: { key: string; value: string; secret: boolean }]
}>()

const secretRefHint = '${{ secrets.NAME }}'
const form = ref({ key: '', value: '', secret: false })

const confirmDiscard = ref(false)

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    form.value = { ...props.initial }
    await nextTick()
    resetDirty()
  },
  { immediate: true },
)

const { dirty, reset: resetDirty } = useDirtyGuard(() => form.value)

function requestClose() {
  if (props.saving) return
  if (dirty.value) {
    confirmDiscard.value = true
    return
  }
  emit('close')
}

function discard() {
  confirmDiscard.value = false
  emit('close')
}


function submit() {
  const key = form.value.key.trim()
  if (!key) return
  emit('save', { key, value: form.value.value, secret: form.value.secret })
}
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" max-width="480px" :escapable="!confirmDiscard" @close="requestClose">
        <div class="modal-header">
          <h3 id="envvar-form-title">{{ editingKey ? 'Update variable' : 'Add variable' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" data-modal-skip-focus @click="requestClose">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <form @submit.prevent="submit">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Key</label>
              <input
                v-model="form.key"
                class="form-input mono-input"
                :readonly="!!editingKey"
                spellcheck="false"
                autocapitalize="off"
                autocomplete="off"
                placeholder="DATABASE_URL"
                required
                :autofocus="!editingKey"
              />
              <p v-if="editingKey" class="form-hint">The key can't be changed. Delete and re-add to rename.</p>
            </div>
            <div class="form-group">
              <label class="form-label">
                Value
                <span v-if="editingKey && form.secret" class="text-muted">— re-enter (secret values aren't shown)</span>
              </label>
              <textarea
                v-model="form.value"
                class="form-input mono-input"
                rows="3"
                spellcheck="false"
                :placeholder="form.secret ? 'super-secret-value' : `value or ${secretRefHint}`"
              ></textarea>
            </div>
            <label class="checkbox-label" style="margin-bottom: 0">
              <input v-model="form.secret" type="checkbox" />
              <span><span class="mdi mdi-lock-outline"></span> Secret — encrypted at rest and masked in the UI</span>
            </label>
            <p class="form-hint">
              Reference a workspace secret with <code>{{ secretRefHint }}</code>.{{ applyNote ? ` ${applyNote}` : '' }}
            </p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="requestClose">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.key.trim()">
              {{ saving ? 'Saving…' : editingKey ? 'Update' : 'Add variable' }}
            </button>
          </div>
        </form>
    </AppModal>

    <ConfirmDialog
      :open="confirmDiscard"
      title="Discard changes?"
      message="This variable has unsaved changes. Closing now loses them."
      confirm-label="Discard"
      cancel-label="Keep editing"
      variant="danger"
      @confirm="discard"
      @cancel="confirmDiscard = false"
    />
  </Teleport>
</template>

<style scoped>
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; color: var(--text-secondary); }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mono-input { font-family: 'JetBrains Mono', monospace; font-size: 13px; }
</style>
