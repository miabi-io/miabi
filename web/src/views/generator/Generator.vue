<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// The deliberate path: "give me a passphrase for something", as opposed to the
// inline button, which is the common one. Same panel underneath.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { secretApi } from '@/api/secrets'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import GeneratorPanel from '@/components/GeneratorPanel.vue'
import SaveAsSecret from '@/components/SaveAsSecret.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()

const saving = ref(false)

const canSave = computed(() => !!ws.currentWorkspaceId && ws.canEdit)

async function saveSecret(name: string, description: string, value: string): Promise<boolean> {
  if (!ws.currentWorkspaceId) return false
  saving.value = true
  try {
    await secretApi.create(ws.currentWorkspaceId, { name, value, description })
    notify.success(t('notify.secrets.namedCreated', { name }))
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
        <h1>{{ $t('generator.generator') }}</h1>
        <div class="text-muted text-sm subtitle">{{ $t('generator.passwordsPassphrasesAndTokensGenerated') }}</div>
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
          <h2 class="aside-title">{{ $t('generator.howThisWorks') }}</h2>
          <i18n-t keypath="generator.howHint" tag="p">
            <template #api><code>crypto.getRandomValues</code></template>
          </i18n-t>

          <h2 class="aside-title">{{ $t('generator.readingTheBits') }}</h2>
          <p>{{ $t('generator.theNumberBesideTheValue') }}</p>
          <ul class="aside-list">
            <li><i18n-t keypath="generator.entropyLow" tag="span"><template #range><strong>{{ $t('generator.under45') }}</strong></template></i18n-t></li>
            <li><i18n-t keypath="generator.entropyMid" tag="span"><template #range><strong>70–110</strong></template></i18n-t></li>
            <li><i18n-t keypath="generator.entropyHigh" tag="span"><template #range><strong>128+</strong></template></i18n-t></li>
          </ul>
          <p>
            A passphrase from the {{ 7776 }}-word EFF list is worth 12.9 bits per word, so six words is about 77 bits —
            comparable to a 13-character random password, and far easier to read down a phone line.
          </p>

          <h2 class="aside-title">{{ $t('generator.whatIsnTKept') }}</h2>
          <p>{{ $t('generator.yourOptionsAreRememberedBetween') }}</p>
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
