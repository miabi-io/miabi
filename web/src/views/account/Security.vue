<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted, ref } from 'vue'
import { authApi } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import type { TwoFactorSetup } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const auth = useAuthStore()
const { t } = useI18n()
const notify = useNotificationStore()

const loading = ref(false)
const busy = ref(false)

const enabled = computed(() => !!auth.user?.two_factor_enabled)
const codesRemaining = computed(() => auth.user?.recovery_codes_remaining ?? 0)

// Modal state machine: which dialog (if any) is open.
type Modal = null | 'setup' | 'codes' | 'disable' | 'regenerate'
const modal = ref<Modal>(null)

const setup = ref<TwoFactorSetup | null>(null)
const code = ref('')
const recoveryCodes = ref<string[]>([])

onMounted(async () => {
  loading.value = true
  await auth.fetchUser()
  loading.value = false
})

// --- Change password ---
const pw = ref({ current: '', next: '', confirm: '' })
const pwBusy = ref(false)
const pwError = computed(() => {
  if (pw.value.next && pw.value.next.length < 8) return 'New password must be at least 8 characters.'
  if (pw.value.confirm && pw.value.next !== pw.value.confirm) return 'New passwords do not match.'
  return ''
})
const pwValid = computed(
  () => !!pw.value.current && pw.value.next.length >= 8 && pw.value.next === pw.value.confirm,
)
async function changePassword() {
  if (!pwValid.value || pwBusy.value) return
  pwBusy.value = true
  try {
    await authApi.changePassword(pw.value.current, pw.value.next)
    pw.value = { current: '', next: '', confirm: '' }
    notify.success(t('notify.security.passwordChanged'))
  } catch (e) {
    notify.apiError(e, 'Could not change password')
  } finally {
    pwBusy.value = false
  }
}

function closeModal() {
  modal.value = null
  setup.value = null
  code.value = ''
  recoveryCodes.value = []
}

// --- Enable flow ---
async function startSetup() {
  busy.value = true
  try {
    setup.value = (await authApi.setupTwoFactor()).data.data
    code.value = ''
    modal.value = 'setup'
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function confirmSetup() {
  if (!code.value.trim()) return
  busy.value = true
  try {
    recoveryCodes.value = (await authApi.verifyTwoFactor(code.value.trim())).data.data.recovery_codes
    await auth.fetchUser()
    notify.success(t('notify.security.twoFactorEnabled'))
    modal.value = 'codes'
  } catch (e) {
    notify.apiError(e, 'Invalid code')
  } finally {
    busy.value = false
  }
}

// --- Disable flow ---
function openDisable() {
  code.value = ''
  modal.value = 'disable'
}

async function confirmDisable() {
  if (!code.value.trim()) return
  busy.value = true
  try {
    await authApi.disableTwoFactor(code.value.trim())
    await auth.fetchUser()
    notify.success(t('notify.security.twoFactorDisabled'))
    closeModal()
  } catch (e) {
    notify.apiError(e, 'Invalid code')
  } finally {
    busy.value = false
  }
}

// --- Regenerate recovery codes ---
function openRegenerate() {
  code.value = ''
  modal.value = 'regenerate'
}

async function confirmRegenerate() {
  if (!code.value.trim()) return
  busy.value = true
  try {
    recoveryCodes.value = (await authApi.regenerateRecoveryCodes(code.value.trim())).data.data.recovery_codes
    await auth.fetchUser()
    notify.success(t('notify.security.codesRegenerated'))
    modal.value = 'codes'
  } catch (e) {
    notify.apiError(e, 'Invalid code')
  } finally {
    busy.value = false
  }
}

// --- Recovery code helpers ---
async function copyCodes() {
  if (await copyText(recoveryCodes.value.join('\n'))) notify.success(t('notify.common.copied'))
  else notify.error(t('notify.security.copyFailedSelectAndCopy'))
}

function downloadCodes() {
  const blob = new Blob([recoveryCodes.value.join('\n') + '\n'], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'miabi-recovery-codes.txt'
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>{{ $t('security.security') }}</h1>
    </div>

    <div class="card">
      <div class="card-header">
        <h2>{{ $t('security.password') }}</h2>
      </div>
      <div class="card-body">
        <p class="sec-desc">{{ $t('security.passwordSubtitle') }}</p>
        <form class="pw-form" @submit.prevent="changePassword">
          <div class="form-group">
            <label class="form-label">{{ $t('security.currentPassword') }}</label>
            <input v-model="pw.current" type="password" class="form-input" autocomplete="current-password" :aria-label="$t('security.currentPassword')" required />
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('security.newPassword') }}</label>
            <input v-model="pw.next" type="password" class="form-input" autocomplete="new-password" minlength="8" :aria-label="$t('security.newPassword')" required />
            <p class="form-hint">{{ $t('security.passwordHint') }}</p>
          </div>
          <div class="form-group">
            <label class="form-label">{{ $t('security.confirmNewPassword') }}</label>
            <input v-model="pw.confirm" type="password" class="form-input" autocomplete="new-password" :aria-label="$t('security.confirmNewPassword')" required />
          </div>
          <p v-if="pwError" class="pw-error"><span class="mdi mdi-alert-circle-outline"></span> {{ pwError }}</p>
          <div>
            <button type="submit" class="btn btn-primary" :disabled="!pwValid || pwBusy">
              <span class="mdi mdi-lock-reset"></span> {{ pwBusy ? 'Changing…' : 'Change password' }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <h2>{{ $t('security.twoFactorAuthentication') }}</h2>
        <span v-if="enabled" class="badge badge-success badge-dot">{{ $t('security.enabled') }}</span>
        <span v-else class="badge badge-neutral">{{ $t('security.disabled') }}</span>
      </div>

      <div v-if="loading" class="card-body"><span class="spinner"></span></div>

      <div v-else class="card-body">
        <p class="sec-desc">{{ $t('security.twoFactorHint') }}</p>

        <template v-if="!enabled">
          <button class="btn btn-primary" :disabled="busy" @click="startSetup">
            <span class="mdi mdi-shield-plus-outline"></span>
            {{ busy ? 'Preparing…' : 'Enable two-factor' }}
          </button>
        </template>

        <template v-else>
          <div class="sec-status">
            <span class="mdi mdi-shield-check" style="color: var(--success-600)"></span>
            <span>
              {{ $t('security.twoFactorOn') }}
              <i18n-t keypath="security.codesRemaining" :plural="codesRemaining" tag="span">
                <template #n><strong>{{ codesRemaining }}</strong></template>
              </i18n-t>
            </span>
          </div>
          <div v-if="codesRemaining > 0 && codesRemaining <= 3" class="app-banner app-banner--warning sec-low">
            <span class="mdi mdi-alert-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">{{ $t('security.lowOnCodes') }}</p>
              <p class="app-banner-text">{{ $t('security.regenerateHint') }}</p>
            </div>
          </div>
          <div class="sec-actions">
            <button class="btn btn-secondary" @click="openRegenerate">
              <span class="mdi mdi-refresh"></span>{{ $t('security.regenerateRecoveryCodes') }}</button>
            <button class="btn btn-danger" @click="openDisable">
              <span class="mdi mdi-shield-off-outline"></span>{{ $t('security.disable') }}</button>
          </div>
        </template>
      </div>
    </div>

    <Teleport to="body">
      <!-- Setup: QR + verify -->
      <AppModal v-if="modal === 'setup'" @close="closeModal">
        <div class="modal-header">
          <h3>{{ $t('security.setUpTwoFactorAuthentication') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="closeModal"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="confirmSetup">
          <div class="modal-body">
            <ol class="sec-steps">
              <li>{{ $t('security.scanHint') }}</li>
              <li>{{ $t('security.enterCodeHint') }}</li>
            </ol>
            <div class="sec-qr">
              <img v-if="setup" :src="setup.qr_code" :alt="$t('security.totpQrCode')" width="200" height="200" />
            </div>
            <i18n-t keypath="security.manualKey" tag="p" class="sec-manual">
              <template #key><code class="sec-secret">{{ setup?.secret }}</code></template>
            </i18n-t>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('security.verificationCode') }}</label>
              <input
                v-model="code"
                class="form-input totp-input"
                inputmode="numeric"
                placeholder="123456"
                autocomplete="one-time-code"
                :aria-label="$t('security.verificationCode')"
                required
                autofocus
              />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="closeModal">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="busy">
              {{ busy ? 'Verifying…' : 'Verify & enable' }}
            </button>
          </div>
        </form>
      </AppModal>

      <!-- Recovery codes display -->
      <AppModal v-if="modal === 'codes'" @close="closeModal">
        <div class="modal-header">
          <h3>{{ $t('security.saveCodes') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="closeModal"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div class="app-banner app-banner--warning">
            <span class="mdi mdi-alert-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">{{ $t('security.storeSafely') }}</p>
              <p class="app-banner-text">{{ $t('security.recoveryCodesHint') }}</p>
            </div>
          </div>
          <div class="sec-codes">
            <code v-for="rc in recoveryCodes" :key="rc">{{ rc }}</code>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="copyCodes">{{ $t('security.copy') }}</button>
          <button type="button" class="btn btn-secondary" @click="downloadCodes">{{ $t('security.download') }}</button>
          <button type="button" class="btn btn-primary" @click="closeModal">{{ $t('security.done') }}</button>
        </div>
      </AppModal>

      <!-- Disable / Regenerate: ask for a code -->
      <AppModal v-if="modal === 'disable' || modal === 'regenerate'" max-width="460px" @close="closeModal">
        <div class="modal-header">
          <h3>{{ modal === 'disable' ? 'Disable two-factor' : 'Regenerate recovery codes' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="closeModal"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="modal === 'disable' ? confirmDisable() : confirmRegenerate()">
          <div class="modal-body">
            <p class="sec-desc" style="margin-bottom: 14px">
              {{ modal === 'disable'
                ? 'Enter a code from your authenticator app (or a recovery code) to turn off two-factor authentication.'
                : 'Enter a code from your authenticator app. This invalidates your existing recovery codes.' }}
            </p>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ modal === 'disable' ? 'Authentication or recovery code' : 'Authentication code' }}</label>
              <input
                v-model="code"
                class="form-input totp-input"
                placeholder="123456"
                autocomplete="one-time-code"
                :aria-label="modal === 'disable' ? 'Authentication or recovery code' : 'Authentication code'"
                required
                autofocus
              />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="closeModal">{{ $t('action.cancel') }}</button>
            <button
              type="submit"
              class="btn"
              :class="modal === 'disable' ? 'btn-danger' : 'btn-primary'"
              :disabled="busy"
            >
              {{ busy ? 'Working…' : modal === 'disable' ? 'Disable' : 'Regenerate' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sec-desc {
  color: var(--text-secondary);
  font-size: 14px;
  line-height: 1.5;
  margin-bottom: 18px;
  max-width: 640px;
}
.pw-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-width: 420px;
}
.pw-error {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--danger-600);
  margin: -4px 0 0;
}
.sec-status {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  color: var(--text-secondary);
  margin-bottom: 16px;
}
.sec-status .mdi {
  font-size: 20px;
}
.sec-low {
  margin-bottom: 16px;
  max-width: 640px;
}
.sec-actions {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.sec-steps {
  margin: 0 0 16px;
  padding-left: 20px;
  color: var(--text-secondary);
  font-size: 14px;
  line-height: 1.7;
}
.sec-qr {
  display: flex;
  justify-content: center;
  padding: 14px;
  background: #fff;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  width: max-content;
  margin: 0 auto 16px;
}
.sec-manual {
  font-size: 13px;
  color: var(--text-muted);
  text-align: center;
  margin-bottom: 18px;
}
.sec-secret {
  display: inline-block;
  margin-top: 6px;
  padding: 4px 10px;
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  font-family: monospace;
  letter-spacing: 2px;
  color: var(--text-primary);
  user-select: all;
}
.totp-input {
  text-align: center;
  font-size: 20px;
  letter-spacing: 4px;
  font-variant-numeric: tabular-nums;
}
.sec-codes {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
  margin-top: 16px;
}
.sec-codes code {
  padding: 8px 12px;
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  font-family: monospace;
  font-size: 14px;
  text-align: center;
  letter-spacing: 1px;
  color: var(--text-primary);
  user-select: all;
}
</style>
