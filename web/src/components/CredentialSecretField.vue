<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import SecretPicker from '@/components/SecretPicker.vue'
import GenerateButton from '@/components/GenerateButton.vue'

/**
 * The secret half of a stored credential (a registry password, a Git token or
 * SSH key). Either a value typed in here, or a reference to a workspace Secret.
 *
 * v-model is what the API expects in the credential's `secret` field: the literal
 * value, or `${{ secrets.NAME }}` — the server routes the reference form to the
 * vault and resolves it at every use, so rotating that Secret rotates every
 * credential pointing at it.
 */
const props = withDefaults(
  defineProps<{
    /** Optional because the credential form types carry `secret?: string`. */
    modelValue?: string
    label: string
    /** Editing an existing credential: a blank value keeps what is stored. */
    editing?: boolean
    /** The Secret this credential already references, if any. */
    currentRef?: string
    /** Render a textarea (an SSH private key) instead of a password input. */
    multiline?: boolean
    placeholder?: string
  }>(),
  { modelValue: '', editing: false, currentRef: '', multiline: false, placeholder: '' },
)
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

type Mode = 'value' | 'secret'
// A credential that already references a secret opens on that tab, so editing it
// doesn't silently look like a blank literal.
const mode = ref<Mode>(props.currentRef ? 'secret' : 'value')
const literal = ref('')
const selected = ref(props.currentRef)

// The field owns mode/literal/selected and only ever pushes outward. It is not
// re-synced from modelValue: the parent echoes what we emit, and reacting to
// that echo would snap the mode back on every keystroke. Re-initializing is
// unnecessary anyway — the modal is v-if'd, so this component is created fresh
// each time it opens, with the props of the credential being edited.
function emitCurrent() {
  emit('update:modelValue', mode.value === 'secret'
    ? (selected.value ? `\${{ secrets.${selected.value} }}` : '')
    : literal.value)
}
watch([mode, literal, selected], emitCurrent)

// On create the field is required; on edit, blank means "keep what is stored".
const required = computed(() => !props.editing)
</script>

<template>
  <div class="form-group" style="margin-bottom: 0">
    <label class="form-label">
      {{ label }}
      <span v-if="editing" class="text-muted">(leave blank to keep current)</span>
    </label>

    <div class="tabs source-tabs">
      <button type="button" class="tab" :class="{ active: mode === 'value' }" @click="mode = 'value'">
        Enter value
      </button>
      <button type="button" class="tab" :class="{ active: mode === 'secret' }" @click="mode = 'secret'">
        Use a secret
      </button>
    </div>

    <template v-if="mode === 'value'">
      <textarea
        v-if="multiline"
        v-model="literal"
        class="form-textarea"
        :placeholder="placeholder"
        :required="required"
        :aria-label="label"
      ></textarea>
      <!-- Single-line only: an SSH private key is a keypair, not a random
           string, so offering to generate one here would be wrong. -->
      <div v-else class="cred-input-row">
        <input
          v-model="literal"
          type="password"
          class="form-input"
          :placeholder="placeholder || '••••••••'"
          autocomplete="new-password"
          :required="required"
          :aria-label="label"
        />
        <GenerateButton :label="`Generate a value for ${label}`" @generated="literal = $event" />
      </div>
    </template>

    <template v-else>
      <!-- Defaults to unmanaged: a managed secret is owned by another resource
           (a provisioned database's password) and rotates with it, so it is
           rarely the right thing to authenticate a registry or repo with. The
           filter is right there when it is. -->
      <SecretPicker v-model="selected" :label="label" default-ownership="unmanaged" />
      <p v-if="required && !selected" class="form-hint">Choose a secret to continue.</p>
      <p v-else class="form-hint">
        Read from the vault on every use — rotating the secret rotates this credential.
      </p>
    </template>
  </div>
</template>

<style scoped>
.source-tabs { margin-bottom: 10px; }
.cred-input-row { display: flex; align-items: center; gap: 6px; }
.cred-input-row .form-input { flex: 1; min-width: 0; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.form-hint { margin: 6px 0 0; font-size: 12px; color: var(--text-muted); }
</style>
