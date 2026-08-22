<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Puts a generated value straight into the vault: name it, save it, done.
//
// This is the point of having a generator inside a secrets platform. The flow it
// replaces routes the value through the clipboard, which is the weakest link —
// readable by any other app, and left sitting there afterwards.
import { computed, ref } from 'vue'

// save is a prop rather than an emit because the result matters: Vue emits
// return void, so awaiting one would read every save as a success and close the
// form over a failure, discarding the name that was just typed.
const props = defineProps<{
  value: string
  saving?: boolean
  save: (name: string, description: string, value: string) => Promise<boolean>
}>()

const open = ref(false)
const name = ref('')
const description = ref('')


const NAME_RE = /^[A-Za-z0-9_-]+$/
const nameError = computed(() => {
  const n = name.value.trim()
  if (!n) return ''
  return NAME_RE.test(n) ? '' : 'Letters, digits, underscore or hyphen only.'
})
const canSave = computed(() => !!name.value.trim() && !nameError.value && !!props.value && !props.saving)

async function submit() {
  if (!canSave.value) return
  const ok = await props.save(name.value.trim(), description.value.trim(), props.value)

  if (!ok) return
  open.value = false
  name.value = ''
  description.value = ''
}
</script>

<template>
  <div class="save-secret">
    <button v-if="!open" type="button" class="btn btn-primary btn-sm" :disabled="!value" @click="open = true">
      <span class="mdi mdi-content-save-outline"></span> Save as secret
    </button>

    <form v-else class="save-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label">Secret name</label>
        <input v-model="name" class="form-input" placeholder="db_password" style="font-family: monospace" autofocus
          aria-label="Secret name" />
        <p v-if="nameError" class="form-hint save-error">{{ nameError }}</p>
        <p v-else class="form-hint">Stored in this workspace's vault; the value is written once and never shown again.</p>
      </div>
      <div class="form-group">
        <label class="form-label">Description <span class="text-muted">(optional)</span></label>
        <input v-model="description" class="form-input" placeholder="e.g. Postgres app password"
          aria-label="Description" />
      </div>
      <div class="save-actions">
        <button type="submit" class="btn btn-primary btn-sm" :disabled="!canSave">
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="open = false">Cancel</button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.save-secret { width: 100%; }
.save-form { width: 100%; border-top: 1px solid var(--border-primary); padding-top: 12px; margin-top: 4px; }
.save-actions { display: flex; gap: 8px; }
.save-error { color: var(--danger-500, #ef4444); }
</style>
