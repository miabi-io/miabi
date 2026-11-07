<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import SecretPicker from '@/components/SecretPicker.vue'
import GenerateButton from '@/components/GenerateButton.vue'

/** v-model is the literal or `${{ secrets.NAME }}`; the server resolves references at use, so rotating the Secret rotates all. */
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
// Open on the secret tab so an existing reference doesn't look like a blank literal.
const mode = ref<Mode>(props.currentRef ? 'secret' : 'value')
const literal = ref('')
const selected = ref(props.currentRef)


function emitCurrent() {
  emit('update:modelValue', mode.value === 'secret'
    ? (selected.value ? `\${{ secrets.${selected.value} }}` : '')
    : literal.value)
}
watch([mode, literal, selected], emitCurrent)

const required = computed(() => !props.editing)
</script>

<template>
  <div class="form-group" style="margin-bottom: 0">
    <label class="form-label">
      {{ label }}
      <span v-if="editing" class="text-muted">{{ $t('secrets.keepCurrent') }}</span>
    </label>

    <div class="tabs source-tabs">
      <button type="button" class="tab" :class="{ active: mode === 'value' }" @click="mode = 'value'">{{ $t('credentialField.enterValue') }}</button>
      <button type="button" class="tab" :class="{ active: mode === 'secret' }" @click="mode = 'secret'">{{ $t('credentialField.useASecret') }}</button>
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
      <!-- Single-line only: an SSH private key is a keypair, so offering to generate one would be wrong. -->
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
      <SecretPicker v-model="selected" :label="label" default-ownership="unmanaged" />
      <p v-if="required && !selected" class="form-hint">{{ $t('credentialField.chooseASecretToContinue') }}</p>
      <p v-else class="form-hint">{{ $t('credentialField.readFromTheVaultOn') }}</p>
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
