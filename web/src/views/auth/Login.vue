<script setup lang="ts">
import { ref, onMounted, watch, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { apiErrorMessage } from '@/api/client'
import { authApi } from '@/api/auth'
import { oauthApi, authorizeUrl } from '@/api/oauth'
import type { PublicProvider } from '@/api/types'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const identifier = ref('')
const password = ref('')
const showPassword = ref(false)
const capsOn = ref(false)
const error = ref('')
const loading = ref(false)

// Two-step login: once credentials are accepted for a 2FA account, the form
// switches to collecting the authenticator (or recovery) code.
const step = ref<'credentials' | 'twofactor'>('credentials')
const twoFactorCode = ref('')

// Focus the relevant field on load and on step change — the `autofocus`
// attribute only fires on the initial document render, not when the 2FA step is
// mounted dynamically.
const identifierInput = ref<HTMLInputElement | null>(null)
const twoFactorInput = ref<HTMLInputElement | null>(null)
watch(step, (s) => {
  nextTick(() => (s === 'twofactor' ? twoFactorInput : identifierInput).value?.focus())
})

// Surface a Caps Lock warning while typing the password — a common cause of
// "invalid credentials" on the first try.
function onCaps(e: KeyboardEvent) {
  capsOn.value = e.getModifierState?.('CapsLock') ?? false
}

const providers = ref<PublicProvider[]>([])

// "Continue with SSO": some providers are hidden from the login buttons and are
// reached by typing an email whose domain the platform maps to a provider.
// ssoAvailable is true when at least one hidden provider exists; ssoMode switches
// the form to the email-discovery step.
const ssoAvailable = ref(false)
const ssoMode = ref(false)
const ssoEmail = ref('')
const ssoLoading = ref(false)
const ssoEmailInput = ref<HTMLInputElement | null>(null)

// Self-service password reset is admin-gated; the "Forgot password?" link only
// shows when the platform advertises it as enabled.
const passwordResetEnabled = ref(false)

// Friendly messages for error codes handed back by the OAuth callback redirect.
const oauthErrors: Record<string, string> = {
  oauth_failed: 'Single sign-on failed. Please try again.',
  invalid_state: 'Your sign-in session expired. Please try again.',
  domain_not_allowed: 'Your email domain is not allowed for this provider.',
  registration_closed: 'This account does not exist and sign-up is disabled.',
  account_disabled: 'Your account has been disabled.',
  no_email: 'The provider did not share an email address.',
  provider_unavailable: 'That sign-in provider is unavailable.',
  missing_code: 'Single sign-on was cancelled.',
  token_error: 'Could not start your session. Please try again.',
}

onMounted(async () => {
  identifierInput.value?.focus()

  const code = route.query.error as string | undefined
  if (code) error.value = oauthErrors[code] || 'Sign in failed. Please try again.'

  try {
    const { data } = await oauthApi.providers()
    providers.value = data.data?.providers ?? []
    ssoAvailable.value = !!data.data?.sso_available
  } catch {
    // Status endpoints are best-effort; the form still works without them.
  }

  try {
    const { data } = await authApi.status()
    passwordResetEnabled.value = data.data?.password_reset_enabled ?? false
  } catch {
    // Best-effort: leave the reset link hidden if status can't be read.
  }
})

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const code = step.value === 'twofactor' ? twoFactorCode.value.trim() : undefined
    const result = await auth.login(identifier.value.trim(), password.value, code)
    if (result === 'twofactor') {
      // Account has 2FA — collect the code and resubmit.
      step.value = 'twofactor'
      return
    }
    if (result === 'reset') {
      // Admin-set/reset password — must set a new one before entering the console.
      router.push({ name: 'change-password' })
      return
    }
    router.push('/')
  } catch (e) {
    error.value = apiErrorMessage(e, step.value === 'twofactor' ? 'Invalid code' : 'Invalid credentials')
  } finally {
    loading.value = false
  }
}

function backToCredentials() {
  step.value = 'credentials'
  twoFactorCode.value = ''
  error.value = ''
}

function signInWith(slug: string) {
  window.location.href = authorizeUrl(slug)
}

// Switch to the "Continue with SSO" email step, prefilling the email if the user
// already typed one into the identifier field.
function enterSSOMode() {
  error.value = ''
  ssoEmail.value = identifier.value.includes('@') ? identifier.value.trim() : ''
  ssoMode.value = true
  nextTick(() => ssoEmailInput.value?.focus())
}

function exitSSOMode() {
  ssoMode.value = false
  error.value = ''
}

// Resolve the provider for the entered email's domain and hand off to its
// authorize redirect. A 404 means no provider matches — fall back to password.
async function continueWithSSO() {
  error.value = ''
  ssoLoading.value = true
  try {
    const res = await oauthApi.discoverSSO(ssoEmail.value.trim())
    const name = res.data.data?.name
    if (!name) throw new Error('no provider')
    window.location.href = authorizeUrl(name)
  } catch (e) {
    error.value = apiErrorMessage(e, 'No single sign-on provider was found for that email.')
  } finally {
    ssoLoading.value = false
  }
}

function providerIcon(type: string): string {
  return type === 'google' ? 'mdi-google' : 'mdi-shield-key-outline'
}
</script>

<template>
  <div class="auth">
    <!-- Brand / marketing panel -->
    <aside class="auth-hero">
      <div class="auth-hero-inner">
        <!-- Brand lockup: the new pinwheel mark + "Miabi.io" wordmark; the
             trailing ".io" carries the brand accent. -->
        <div class="auth-hero-wordmark">
          <img src="/brand/miabi-mark-white.svg" alt="" class="auth-hero-mark" />
          <span class="auth-hero-name">Miabi<span class="wm-io">.io</span></span>
        </div>

        <div class="auth-hero-body">
          <h2 class="auth-hero-title">Self-hosting,<br />reimagined.</h2>
          <p class="auth-hero-lead">
            Deploy, scale, and manage applications from one intuitive platform.
          </p>
          <ul class="auth-hero-features">
            <li><span class="mdi mdi-package-variant-closed"></span> Built-in Container Registry</li>
            <li><span class="mdi mdi-lock-check-outline"></span> Secrets &amp; Automatic TLS</li>
            <li><span class="mdi mdi-infinity"></span> GitOps &amp; Canary Deployments</li>
            <li><span class="mdi mdi-database-outline"></span> Managed databases, backups &amp; volumes</li>
            <li><span class="mdi mdi-chart-areaspline"></span> Monitoring &amp; Release History</li>
            <li><span class="mdi mdi-account-group-outline"></span> Multi-tenant Workspaces &amp; RBAC</li>
          </ul>
        </div>

        <p class="auth-hero-foot">Open-source · Self-hosted PaaS for Docker</p>
      </div>
    </aside>

    <!-- Form panel -->
    <main class="auth-main">
      <div class="auth-card">
        <div class="auth-head">
          <img src="/brand/miabi-mark.svg" alt="Miabi" class="auth-logo" />
          <h1 class="auth-title">
            {{ step === 'twofactor' ? 'Two-factor authentication' : ssoMode ? 'Continue with SSO' : 'Welcome to Miabi' }}
          </h1>
          <p class="auth-subtitle">
            {{ step === 'twofactor' ? 'Enter the code from your authenticator app' : ssoMode ? 'Enter your email to find your sign-in provider' : 'Sign in to your Miabi workspace' }}
          </p>
        </div>

        <Transition name="fade">
          <div v-if="error" class="auth-alert" role="alert" aria-live="assertive">
            <span class="mdi mdi-alert-circle-outline"></span>
            <span>{{ error }}</span>
          </div>
        </Transition>

        <!-- Step 2: two-factor code -->
        <form v-if="step === 'twofactor'" class="auth-form" @submit.prevent="submit">
          <div class="form-group">
            <label class="form-label">Authentication code</label>
            <input
              ref="twoFactorInput"
              v-model="twoFactorCode"
              type="text"
              inputmode="numeric"
              class="form-input totp-input"
              placeholder="123456"
              autocomplete="one-time-code"
              aria-label="Authentication code"
              :disabled="loading"
              required
            />
            <p class="form-hint">Enter the 6-digit code from your authenticator app, or a recovery code.</p>
          </div>
          <button class="btn btn-primary auth-submit" :disabled="loading">
            <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
            {{ loading ? 'Verifying…' : 'Verify' }}
          </button>
          <button type="button" class="auth-link" :disabled="loading" @click="backToCredentials">
            <span class="mdi mdi-arrow-left"></span> Use a different account
          </button>
        </form>

        <!-- Continue with SSO: email → provider discovery -->
        <form v-else-if="ssoMode" class="auth-form" @submit.prevent="continueWithSSO">
          <div class="form-group">
            <label class="form-label">Work email</label>
            <input
              ref="ssoEmailInput"
              v-model="ssoEmail"
              type="email"
              class="form-input"
              placeholder="you@company.com"
              autocomplete="email"
              aria-label="Work email"
              :disabled="ssoLoading"
              required
            />
            <p class="form-hint">We'll redirect you to your organization's sign-in provider.</p>
          </div>
          <button class="btn btn-primary auth-submit" :disabled="ssoLoading">
            <span v-if="ssoLoading" class="mdi mdi-loading mdi-spin"></span>
            {{ ssoLoading ? 'Finding provider…' : 'Continue' }}
          </button>
          <button type="button" class="auth-link" :disabled="ssoLoading" @click="exitSSOMode">
            <span class="mdi mdi-arrow-left"></span> Back to sign in
          </button>
        </form>

        <!-- Step 1: credentials -->
        <form v-else class="auth-form" @submit.prevent="submit">
          <div class="form-group">
            <label class="form-label">Email or username</label>
            <input
              ref="identifierInput"
              v-model="identifier"
              type="text"
              class="form-input"
              placeholder="Email or username"
              autocomplete="username"
              aria-label="Email or username"
              :disabled="loading"
              required
            />
          </div>

          <div class="form-group">
            <div class="label-row">
              <label class="form-label">Password</label>
              <RouterLink
                v-if="passwordResetEnabled"
                :to="{ name: 'forgot-password' }"
                class="forgot-link"
              >
                Forgot password?
              </RouterLink>
            </div>
            <div class="password-wrap">
              <input
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                class="form-input"
                placeholder="Enter your password"
                autocomplete="current-password"
                aria-label="Password"
                :disabled="loading"
                required
                @keyup="onCaps"
                @keydown="onCaps"
              />
              <button
                type="button"
                class="password-toggle"
                :aria-label="showPassword ? 'Hide password' : 'Show password'"
                :aria-pressed="showPassword"
                :title="showPassword ? 'Hide password' : 'Show password'"
                @click="showPassword = !showPassword"
              >
                <span class="mdi" :class="showPassword ? 'mdi-eye-off-outline' : 'mdi-eye-outline'"></span>
              </button>
            </div>
            <Transition name="fade">
              <p v-if="capsOn" class="form-hint caps-hint">
                <span class="mdi mdi-apple-keyboard-caps"></span> Caps Lock is on
              </p>
            </Transition>
          </div>

          <button class="btn btn-primary auth-submit" :disabled="loading">
            <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
            {{ loading ? 'Signing in…' : 'Sign in' }}
          </button>
        </form>

        <div v-if="step === 'credentials' && !ssoMode && (providers.length || ssoAvailable)" class="auth-oauth">
          <div class="auth-divider"><span>or continue with</span></div>
          <button
            v-for="p in providers"
            :key="p.name"
            type="button"
            class="btn btn-secondary oauth-btn"
            @click="signInWith(p.name)"
          >
            <span class="mdi" :class="providerIcon(p.type)"></span> {{ p.display_name || p.name }}
          </button>
          <button
            v-if="ssoAvailable"
            type="button"
            class="btn btn-secondary oauth-btn"
            @click="enterSSOMode"
          >
            <span class="mdi mdi-shield-key-outline"></span> Continue with SSO
          </button>
        </div>

        <p v-if="step === 'credentials' && !ssoMode" class="auth-footer">
          Don't have an account? Contact your platform administrator.
        </p>
      </div>
    </main>
  </div>
</template>

<style scoped>
.auth {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 1.05fr 1fr;
  background: var(--bg-primary);
}

/* ─── Brand / marketing panel ─── */
.auth-hero {
  position: relative;
  overflow: hidden;
  display: flex;
  color: #fff;
  background:
    radial-gradient(120% 80% at 100% 0%, rgba(255, 255, 255, 0.16), transparent 55%),
    radial-gradient(90% 70% at 0% 100%, rgba(13, 20, 36, 0.5), transparent 60%),
    linear-gradient(150deg, var(--primary-600) 0%, var(--primary-800) 70%, #2a0f4d 100%);
}
/* faint glyph watermark */
.auth-hero::after {
  content: '';
  position: absolute;
  right: -8%;
  bottom: -12%;
  width: 520px;
  height: 520px;
  background: url('/brand/miabi-mark-white.svg') center / contain no-repeat;
  opacity: 0.06;
  pointer-events: none;
}
.auth-hero-inner {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-width: 460px;
  margin: auto;
  padding: 56px 52px;
}
.auth-hero-wordmark {
  display: flex;
  align-items: center;
  gap: 12px;
  align-self: flex-start;
  margin-bottom: auto;
}
.auth-hero-mark {
  height: 40px;
  width: 40px;
}
.auth-hero-name {
  font-size: 1.6rem;
  font-weight: 800;
  letter-spacing: -0.02em;
  color: #fff;
}
.auth-hero-name .wm-io {
  color: var(--primary-400); /* the ".io" accent */
}
.auth-hero-body {
  margin: 48px 0;
}
.auth-hero-title {
  font-size: clamp(1.9rem, 2.6vw, 2.6rem);
  font-weight: 800;
  line-height: 1.1;
  letter-spacing: -0.02em;
  margin: 0 0 16px;
}
.auth-hero-lead {
  font-size: 15px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.82);
  max-width: 40ch;
  margin: 0 0 28px;
}
.auth-hero-features {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.auth-hero-features li {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 14px;
  color: rgba(255, 255, 255, 0.92);
}
.auth-hero-features .mdi {
  font-size: 20px;
  flex-shrink: 0;
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius);
  background: rgba(255, 255, 255, 0.12);
}
.auth-hero-foot {
  margin: 0;
  font-size: 12.5px;
  color: rgba(255, 255, 255, 0.6);
}

/* ─── Form panel ─── */
.auth-main {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 24px;
}
.auth-card {
  width: 100%;
  max-width: 380px;
}
.auth-head {
  text-align: center;
  margin-bottom: 24px;
}
.auth-logo {
  width: 64px;
  height: 64px;
  margin-bottom: 16px;
}
.auth-title {
  font-size: 1.5rem;
  font-weight: 800;
  letter-spacing: -0.01em;
  margin: 0 0 6px;
  color: var(--text-primary);
}
.auth-subtitle {
  color: var(--text-muted);
  font-size: 14px;
  margin: 0;
}
.auth-alert {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  margin-bottom: 16px;
  border-radius: var(--radius);
  background: var(--danger-50);
  color: var(--danger-700);
  border: 1px solid var(--danger-100, var(--danger-50));
  font-size: 13px;
}
.auth-alert .mdi {
  font-size: 18px;
  flex-shrink: 0;
}
.auth-form .form-group {
  margin-bottom: 16px;
}
.label-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}
.forgot-link {
  font-size: 12.5px;
  color: var(--primary-600);
  text-decoration: none;
}
.forgot-link:hover {
  text-decoration: underline;
}
.password-wrap {
  position: relative;
}
.password-wrap .form-input {
  padding-right: 40px;
}
.password-toggle {
  position: absolute;
  top: 50%;
  right: 8px;
  transform: translateY(-50%);
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text-muted);
  font-size: 18px;
  display: flex;
  align-items: center;
  padding: 4px;
}
.password-toggle:hover {
  color: var(--text-primary);
}
.auth-submit {
  width: 100%;
  margin-top: 4px;
}
.auth-submit + .auth-submit {
  margin-top: 10px;
}
.totp-input {
  text-align: center;
  font-size: 20px;
  letter-spacing: 4px;
  font-variant-numeric: tabular-nums;
}
.caps-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  color: var(--warning-700, var(--text-muted));
}
.caps-hint .mdi {
  font-size: 15px;
}
/* Subtle, full-width text action (e.g. "Use a different account" on 2FA). */
.auth-link {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  margin-top: 12px;
  padding: 6px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 13px;
  color: var(--text-muted);
  transition: color 120ms ease;
}
.auth-link:hover:not(:disabled) {
  color: var(--text-primary);
}
.auth-link:disabled {
  opacity: 0.6;
  cursor: default;
}
.auth-link .mdi {
  font-size: 16px;
}
.auth-oauth {
  margin-top: 18px;
}
.auth-divider {
  display: flex;
  align-items: center;
  text-align: center;
  color: var(--text-muted);
  font-size: 12px;
  margin: 4px 0 14px;
}
.auth-divider::before,
.auth-divider::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border-primary);
}
.auth-divider span {
  padding: 0 12px;
}
.oauth-btn {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}
.oauth-btn + .oauth-btn {
  margin-top: 8px;
}
.oauth-btn .mdi {
  font-size: 18px;
}
.auth-footer {
  margin-top: 22px;
  text-align: center;
  font-size: 14px;
  color: var(--text-muted);
}
.fade-enter-active,
.fade-leave-active {
  transition: opacity 150ms ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
.auth-submit {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}

/* ─── Responsive: collapse to a single centered form ─── */
@media (max-width: 900px) {
  .auth {
    grid-template-columns: 1fr;
  }
  .auth-hero {
    display: none;
  }
}
</style>
