<script setup lang="ts">
import { ref } from 'vue'
import type { MigrationSource } from '@/api/workspaceMigration'

const source = defineModel<MigrationSource>({ required: true })
defineProps<{ disabled?: boolean }>()

const showAdvanced = ref(source.value.insecure_skip_tls_verify || !!source.value.ca_bundle)
</script>

<template>
  <fieldset class="code-fields" :disabled="disabled">
    <div class="form-group">
      <label class="form-label" for="mc-code">{{ $t('wsMigration.code') }}</label>
      <textarea id="mc-code" v-model="source.code" class="form-input mono" rows="3" required autocomplete="off"
        spellcheck="false" placeholder="miabi-migrate:…"></textarea>
    </div>

    <button type="button" class="advanced-toggle" :aria-expanded="showAdvanced" @click="showAdvanced = !showAdvanced">
      <span class="mdi" :class="showAdvanced ? 'mdi-chevron-down' : 'mdi-chevron-right'"></span>
      {{ $t('wsMigration.advanced') }}
    </button>
    <div v-if="showAdvanced" class="advanced">
      <label class="check-row">
        <input v-model="source.insecure_skip_tls_verify" type="checkbox" />
        <span>{{ $t('wsMigration.skipTls') }}</span>
      </label>
      <div v-if="source.insecure_skip_tls_verify" class="app-banner app-banner--danger">
        <span class="mdi mdi-shield-alert-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-text">{{ $t('wsMigration.skipTlsWarning') }}</p>
        </div>
      </div>
      <div v-else class="form-group">
        <label class="form-label" for="mc-ca">{{ $t('wsMigration.caBundle') }}</label>
        <textarea id="mc-ca" v-model="source.ca_bundle" class="form-input mono" rows="4"
          placeholder="-----BEGIN CERTIFICATE-----"></textarea>
        <small class="form-hint">{{ $t('wsMigration.caBundleHint') }}</small>
      </div>
    </div>
  </fieldset>
</template>

<style scoped>
.code-fields {
  border: 0;
  padding: 0;
  margin: 0 0 12px;
  min-width: 0;
}
.advanced-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--text-muted);
  font-size: 13px;
  cursor: pointer;
}
.advanced {
  margin-top: 8px;
  padding-left: 18px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 6px 0 10px;
  font-size: 14px;
}
.mono {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 12px;
  word-break: break-all;
}
</style>
