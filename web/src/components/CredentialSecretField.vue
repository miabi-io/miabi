<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { secretApi } from '@/api/secrets'
import type { Secret } from '@/api/types'

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

const ws = useWorkspaceStore()
const { currentWorkspaceId } = storeToRefs(ws)

type Mode = 'value' | 'secret'
// A credential that already references a secret opens on that tab, so editing it
// doesn't silently look like a blank literal.
const mode = ref<Mode>(props.currentRef ? 'secret' : 'value')
const literal = ref('')
const selected = ref(props.currentRef)

const secrets = ref<Secret[]>([])
const loading = ref(false)
const loadFailed = ref(false)

async function loadSecrets(wsId: number | null) {
  if (!wsId) { secrets.value = []; return }
  loading.value = true
  loadFailed.value = false
  try {
    // One page is plenty for a picker; the vault is a small, named set.
    secrets.value = (await secretApi.list(wsId, '', 0, 200)).data.data ?? []
  } catch {
    loadFailed.value = true
    secrets.value = []
  } finally {
    loading.value = false
  }
}
onMounted(() => loadSecrets(currentWorkspaceId.value))
watch(currentWorkspaceId, loadSecrets)

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
      <input
        v-else
        v-model="literal"
        type="password"
        class="form-input"
        :placeholder="placeholder || '••••••••'"
        autocomplete="new-password"
        :required="required"
        :aria-label="label"
      />
    </template>

    <template v-else>
      <select v-model="selected" class="form-select" :required="required" :aria-label="`${label} — secret`">
        <option value="">Select a secret…</option>
        <option v-for="s in secrets" :key="s.id" :value="s.name">
          {{ s.name }}{{ s.managed ? ' (managed)' : '' }}
        </option>
      </select>
      <p v-if="loading" class="form-hint">Loading secrets…</p>
      <p v-else-if="loadFailed" class="form-hint">Could not load secrets — enter the value instead.</p>
      <p v-else-if="secrets.length === 0" class="form-hint">
        No secrets in this workspace yet. Add one under Secrets, or enter the value here.
      </p>
      <p v-else class="form-hint">
        Read from the vault on every use — rotating the secret rotates this credential.
      </p>
    </template>
  </div>
</template>

<style scoped>
.source-tabs { margin-bottom: 10px; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.form-hint { margin: 6px 0 0; font-size: 12px; color: var(--text-muted); }
</style>
