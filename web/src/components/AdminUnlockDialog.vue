<script setup lang="ts">
// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppModal from '@/components/AppModal.vue'
import { useElevationStore } from '@/stores/elevation'
import { apiError } from '@/api/client'

const { t } = useI18n()
const router = useRouter()
const elevation = useElevationStore()

const code = ref('')
const busy = ref(false)
const error = ref('')
const setupNeeded = computed(() => elevation.reason === 'ADMIN_2FA_SETUP_REQUIRED' || elevation.state?.has_two_factor === false)

watch(() => elevation.dialogOpen, (open) => {
  if (open) {
    code.value = ''
    error.value = ''
    void elevation.refresh()
  }
})

async function submit() {
  if (!code.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await elevation.unlock(code.value.trim())
  } catch (e) {
    error.value = apiError(e).message
    code.value = ''
  } finally {
    busy.value = false
  }
}

function cancel() {
  elevation.settle(false)
}

function goSetup() {
  elevation.settle(false)
  void router.push({ name: 'account-security' })
}
</script>

<template>
  <AppModal v-if="elevation.dialogOpen" :escapable="!busy" max-width="420px" @close="cancel">
    <div class="modal-header">
      <h2><span class="mdi mdi-shield-lock-outline"></span> {{ t('adminUnlock.title') }}</h2>
    </div>
    <div v-if="setupNeeded" class="modal-body">
      <p>{{ t('adminUnlock.setupNeeded') }}</p>
    </div>
    <form v-else class="modal-body" @submit.prevent="submit">
      <p class="text-muted">{{ elevation.reason === 'ADMIN_ELEVATION_STALE' ? t('adminUnlock.staleHint') : t('adminUnlock.hint') }}</p>
      <label class="form-label" for="admin-unlock-code">{{ t('adminUnlock.codeLabel') }}</label>
      <input
        id="admin-unlock-code"
        v-model="code"
        class="form-input"
        inputmode="numeric"
        autocomplete="one-time-code"
        maxlength="64"
        autofocus
        :disabled="busy"
      />
      <p class="form-hint">{{ t('adminUnlock.recoveryHint') }}</p>
      <p v-if="elevation.state?.locked_until" class="text-danger">{{ t('adminUnlock.locked', { until: new Date(elevation.state.locked_until).toLocaleTimeString() }) }}</p>
      <p v-if="error" class="text-danger" role="alert">{{ error }}</p>
    </form>
    <div class="modal-footer">
      <button class="btn btn-secondary" :disabled="busy" @click="cancel">{{ t('action.cancel') }}</button>
      <button v-if="setupNeeded" class="btn btn-primary" @click="goSetup">{{ t('adminUnlock.setUp') }}</button>
      <button v-else class="btn btn-primary" :disabled="busy || !code.trim()" @click="submit">
        {{ busy ? t('adminUnlock.unlocking') : t('adminUnlock.unlock') }}
      </button>
    </div>
  </AppModal>
</template>
