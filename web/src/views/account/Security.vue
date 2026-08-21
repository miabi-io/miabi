<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { authApi } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import type { TwoFactorSetup } from '@/api/types'

const auth = useAuthStore()
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
    notify.success('Password changed')
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
    notify.success('Two-factor authentication enabled')
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
    notify.success('Two-factor authentication disabled')
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
    notify.success('Recovery codes regenerated')
    modal.value = 'codes'
  } catch (e) {
    notify.apiError(e, 'Invalid code')
  } finally {
    busy.value = false
  }
}

// --- Recovery code helpers ---
async function copyCodes() {
  if (await copyText(recoveryCodes.value.join('\n'))) notify.success('Copied')
  else notify.error('Copy failed — select and copy them manually')
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
      <h1>Security</h1>
    </div>

    <div class="card">
      <div class="card-header">
        <h2>Password</h2>
      </div>
      <div class="card-body">
        <p class="sec-desc">Change the password you use to sign in to Miabi.</p>
        <form class="pw-form" @submit.prevent="changePassword">
          <div class="form-group">
            <label class="form-label">Current password</label>
            <input v-model="pw.current" type="password" class="form-input" autocomplete="current-password" aria-label="Current password" required />
          </div>
          <div class="form-group">
            <label class="form-label">New password</label>
            <input v-model="pw.next" type="password" class="form-input" autocomplete="new-password" minlength="8" aria-label="New password" required />
            <p class="form-hint">At least 8 characters.</p>
          </div>
          <div class="form-group">
            <label class="form-label">Confirm new password</label>
            <input v-model="pw.confirm" type="password" class="form-input" autocomplete="new-password" aria-label="Confirm new password" required />
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
        <h2>Two-factor authentication</h2>
        <span v-if="enabled" class="badge badge-success badge-dot">enabled</span>
        <span v-else class="badge badge-neutral">disabled</span>
      </div>

      <div v-if="loading" class="card-body"><span class="spinner"></span></div>

      <div v-else class="card-body">
        <p class="sec-desc">
          Add an extra layer of security to your account by requiring a time-based code from an
          authenticator app (Google Authenticator, 1Password, Authy…) in addition to your password.
        </p>

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
              Two-factor authentication is on.
              <strong>{{ codesRemaining }}</strong> recovery code{{ codesRemaining === 1 ? '' : 's' }} remaining.
            </span>
          </div>
          <div v-if="codesRemaining > 0 && codesRemaining <= 3" class="app-banner app-banner--warning sec-low">
            <span class="mdi mdi-alert-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">You're running low on recovery codes</p>
              <p class="app-banner-text">Regenerate a fresh set so you don't get locked out.</p>
            </div>
          </div>
          <div class="sec-actions">
            <button class="btn btn-secondary" @click="openRegenerate">
              <span class="mdi mdi-refresh"></span> Regenerate recovery codes
            </button>
            <button class="btn btn-danger" @click="openDisable">
              <span class="mdi mdi-shield-off-outline"></span> Disable
            </button>
          </div>
        </template>
      </div>
    </div>

    <Teleport to="body">
      <!-- Setup: QR + verify -->
      <div v-if="modal === 'setup'" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>Set up two-factor authentication</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="closeModal"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="confirmSetup">
            <div class="modal-body">
              <ol class="sec-steps">
                <li>Scan this QR code with your authenticator app.</li>
                <li>Enter the 6-digit code it shows to confirm.</li>
              </ol>
              <div class="sec-qr">
                <img v-if="setup" :src="setup.qr_code" alt="TOTP QR code" width="200" height="200" />
              </div>
              <p class="sec-manual">
                Can't scan? Enter this key manually:
                <code class="sec-secret">{{ setup?.secret }}</code>
              </p>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Verification code</label>
                <input
                  v-model="code"
                  class="form-input totp-input"
                  inputmode="numeric"
                  placeholder="123456"
                  autocomplete="one-time-code"
                  aria-label="Verification code"
                  required
                  autofocus
                />
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="closeModal">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="busy">
                {{ busy ? 'Verifying…' : 'Verify & enable' }}
              </button>
            </div>
          </form>
        </div>
      </div>

      <!-- Recovery codes display -->
      <div v-else-if="modal === 'codes'" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>Save your recovery codes</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="closeModal"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="app-banner app-banner--warning">
              <span class="mdi mdi-alert-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-title">Store these somewhere safe</p>
                <p class="app-banner-text">
                  Each code works once if you lose access to your authenticator. They won't be shown again.
                </p>
              </div>
            </div>
            <div class="sec-codes">
              <code v-for="rc in recoveryCodes" :key="rc">{{ rc }}</code>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copyCodes">Copy</button>
            <button type="button" class="btn btn-secondary" @click="downloadCodes">Download</button>
            <button type="button" class="btn btn-primary" @click="closeModal">Done</button>
          </div>
        </div>
      </div>

      <!-- Disable / Regenerate: ask for a code -->
      <div v-else-if="modal === 'disable' || modal === 'regenerate'" class="modal-overlay">
        <div class="modal" style="max-width: 460px">
          <div class="modal-header">
            <h3>{{ modal === 'disable' ? 'Disable two-factor' : 'Regenerate recovery codes' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="closeModal"><span class="mdi mdi-close"></span></button>
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
              <button type="button" class="btn btn-secondary" @click="closeModal">Cancel</button>
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
        </div>
      </div>
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
