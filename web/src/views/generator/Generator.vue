<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The deliberate path: "give me a passphrase for something", as opposed to the
// inline button, which is the common one. Same panel underneath.
import { computed, ref } from 'vue'
import { secretApi } from '@/api/secrets'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import GeneratorPanel from '@/components/GeneratorPanel.vue'
import SaveAsSecret from '@/components/SaveAsSecret.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()

const saving = ref(false)

const canSave = computed(() => !!ws.currentWorkspaceId && ws.canEdit)

async function saveSecret(name: string, description: string, value: string): Promise<boolean> {
  if (!ws.currentWorkspaceId) return false
  saving.value = true
  try {
    await secretApi.create(ws.currentWorkspaceId, { name, value, description })
    notify.success(`Secret “${name}” created.`)
    return true
  } catch (e) {
    notify.apiError(e)
    return false
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Generator</h1>
        <div class="text-muted text-sm subtitle">
          Passwords, passphrases and tokens, generated in your browser. Nothing is sent to the server unless you save
          it to the vault.
        </div>
      </div>
    </div>

    <div class="gen-layout">
      <div class="card">
        <div class="card-body">
          <GeneratorPanel ref="panel">
            <template #actions="{ value }">
              <SaveAsSecret v-if="canSave" :value="value" :saving="saving" :save="saveSecret" />
            </template>
          </GeneratorPanel>
        </div>
      </div>

      <div class="card gen-aside">
        <div class="card-body">
          <h2 class="aside-title">How this works</h2>
          <p>
            Values come from your browser's <code>crypto.getRandomValues</code>, the same source used for TLS keys.
            Nothing is generated on the server, so a value you discard never crossed the network, never entered an
            audit log, and was never in the platform's memory.
          </p>

          <h2 class="aside-title">Reading the bits</h2>
          <p>
            The number beside the value is entropy: how many equally-likely values the generator could have produced,
            as a power of two. Each extra bit doubles the work of guessing it.
          </p>
          <ul class="aside-list">
            <li><strong>Under 45</strong> — too weak for anything reachable from a network.</li>
            <li><strong>70–110</strong> — comfortable for a service password or an app credential.</li>
            <li><strong>128+</strong> — the value stops being the weakest part of the system.</li>
          </ul>
          <p>
            A passphrase from the {{ 7776 }}-word EFF list is worth 12.9 bits per word, so six words is about 77 bits —
            comparable to a 13-character random password, and far easier to read down a phone line.
          </p>

          <h2 class="aside-title">What isn't kept</h2>
          <p>
            Your options are remembered between visits. The values are not: a generator history would be a list of
            plaintext secrets in browser storage, which is worse than wherever you were about to put them.
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.gen-layout { display: grid; gap: 16px; grid-template-columns: minmax(0, 1fr); align-items: start; }
@media (min-width: 900px) {
  .gen-layout { grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); }
}
.gen-aside { font-size: 13px; color: var(--text-secondary, var(--text-muted)); }
.gen-aside p { margin: 0 0 14px; line-height: 1.6; }
.aside-title { font-size: 13px; font-weight: 600; color: var(--text-primary); margin: 0 0 6px; }
.aside-title + p { margin-top: 0; }
.aside-list { margin: 0 0 14px; padding-left: 18px; line-height: 1.7; }
</style>
