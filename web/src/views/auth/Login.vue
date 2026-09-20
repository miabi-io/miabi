<script setup lang="ts">
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useBrandStore } from '@/stores/brand'
import type { Brand } from '@/api/types'
import AuthHero from './AuthHero.vue'
import AuthLanguagePicker from '@/components/AuthLanguagePicker.vue'
import AuthFooter from '@/components/AuthFooter.vue'
import { apiErrorMessage } from '@/api/client'
import { authApi } from '@/api/auth'
import { oauthApi, authorizeUrl } from '@/api/oauth'
import type { PublicProvider } from '@/api/types'

const { t } = useI18n()
const auth = useAuthStore()
const theme = useThemeStore()
const brandStore = useBrandStore()
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
// Sign-up is off by default, so the link only exists where an operator opened it.
const registrationEnabled = ref(false)
const brand = ref<Brand>({})
// Empty fields fall back to Miabi's own identity, so Community and an Enterprise
// install that has set nothing look identical.
const brandName = computed(() => brand.value.name?.trim() || 'Miabi')
const brandLogo = computed(() => (theme.isDark && brand.value.logo_dark_url?.trim()) || brand.value.logo_url?.trim() || '/brand/miabi-mark.svg')
const brandLinks = computed(() => brand.value.links ?? [])

// Friendly messages for error codes handed back by the OAuth callback redirect.
const oauthErrors: Record<string, string> = {
  oauth_failed: 'login.oauthError.failed',
  invalid_state: 'login.oauthError.invalidState',
  domain_not_allowed: 'login.oauthError.domainNotAllowed',
  registration_closed: 'login.oauthError.registrationClosed',
  account_disabled: 'login.oauthError.accountDisabled',
  no_email: 'login.oauthError.noEmail',
  provider_unavailable: 'login.oauthError.providerUnavailable',
  missing_code: 'login.oauthError.cancelled',
  token_error: 'login.oauthError.tokenError',
}

onMounted(async () => {
  identifierInput.value?.focus()

  const code = route.query.error as string | undefined
  if (code) error.value = t(oauthErrors[code] ?? 'login.oauthError.generic')

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
    registrationEnabled.value = data.data?.registration_enabled ?? false
    brand.value = data.data?.brand ?? {}
    brandStore.set(brand.value)
  } catch {
    // Best-effort: leave the reset link hidden if status can't be read.
  }
  theme.applyBrandAccent(brand.value.accent)
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
    error.value = apiErrorMessage(e, t(step.value === 'twofactor' ? 'login.invalidCode' : 'login.invalidCredentials'))
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
    error.value = apiErrorMessage(e, t('login.noSsoProvider'))
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
    <AuthHero :brand-name="brand.name ? brandName : undefined" />

    <!-- Form panel -->
    <main class="auth-main">
      <div class="auth-card">
        <div class="auth-head">
          <img :src="brandLogo" :alt="brandName" class="auth-logo" />
          <h1 class="auth-title">
            {{ step === 'twofactor' ? $t('security.twoFactorAuthentication') : ssoMode ? $t('login.title.sso') : $t('login.title.welcome', { brand: brandName }) }}
          </h1>
          <p class="auth-subtitle">
            {{ step === 'twofactor' ? $t('login.subtitle.twoFactor') : ssoMode ? $t('login.subtitle.sso') : $t('login.subtitle.welcome', { brand: brandName }) }}
          </p>
        </div>

        <div v-if="brand.signin_notice" class="auth-brand-notice" role="note">
          <span class="mdi mdi-information-outline"></span>
          <span>{{ brand.signin_notice }}</span>
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
            <label class="form-label">{{ $t('login.authenticationCode') }}</label>
            <input
              ref="twoFactorInput"
              v-model="twoFactorCode"
              type="text"
              inputmode="numeric"
              class="form-input totp-input"
              placeholder="123456"
              autocomplete="one-time-code"
              :aria-label="$t('login.authenticationCode')"
              :disabled="loading"
              required
            />
            <p class="form-hint">{{ $t('login.totpHint') }}</p>
          </div>
          <button class="btn btn-primary auth-submit" :disabled="loading">
            <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
            {{ loading ? $t('login.verifying') : $t('login.verify') }}
          </button>
          <button type="button" class="auth-link" :disabled="loading" @click="backToCredentials">
            <span class="mdi mdi-arrow-left"></span>{{ $t('login.useADifferentAccount') }}</button>
        </form>

        <!-- Continue with SSO: email → provider discovery -->
        <form v-else-if="ssoMode" class="auth-form" @submit.prevent="continueWithSSO">
          <div class="form-group">
            <label class="form-label">{{ $t('login.workEmail') }}</label>
            <input
              ref="ssoEmailInput"
              v-model="ssoEmail"
              type="email"
              class="form-input"
              placeholder="you@company.com"
              autocomplete="email"
              :aria-label="$t('login.workEmail')"
              :disabled="ssoLoading"
              required
            />
            <p class="form-hint">{{ $t('login.ssoHint') }}</p>
          </div>
          <button class="btn btn-primary auth-submit" :disabled="ssoLoading">
            <span v-if="ssoLoading" class="mdi mdi-loading mdi-spin"></span>
            {{ ssoLoading ? $t('login.findingProvider') : $t('action.continue') }}
          </button>
          <button type="button" class="auth-link" :disabled="ssoLoading" @click="exitSSOMode">
            <span class="mdi mdi-arrow-left"></span>{{ $t('login.backToSignIn') }}</button>
        </form>

        <!-- Step 1: credentials -->
        <form v-else class="auth-form" @submit.prevent="submit">
          <div class="form-group">
            <label class="form-label">{{ $t('login.emailOrUsername') }}</label>
            <input
              ref="identifierInput"
              v-model="identifier"
              type="text"
              class="form-input"
              :placeholder="$t('login.emailOrUsername')"
              autocomplete="username"
              :aria-label="$t('login.emailOrUsername')"
              :disabled="loading"
              required
            />
          </div>

          <div class="form-group">
            <div class="label-row">
              <label class="form-label">{{ $t('login.password') }}</label>
              <RouterLink
                v-if="passwordResetEnabled"
                :to="{ name: 'forgot-password' }"
                class="forgot-link"
              >{{ $t('login.forgotPassword') }}</RouterLink>
            </div>
            <div class="password-wrap">
              <input
                v-model="password"
                :type="showPassword ? 'text' : 'password'"
                class="form-input"
                :placeholder="$t('login.enterYourPassword')"
                autocomplete="current-password"
                :aria-label="$t('login.password')"
                :disabled="loading"
                required
                @keyup="onCaps"
                @keydown="onCaps"
              />
              <button
                type="button"
                class="password-toggle"
                :aria-label="$t(showPassword ? 'auth.hidePassword' : 'auth.showPassword')"
                :aria-pressed="showPassword"
                :title="$t(showPassword ? 'auth.hidePassword' : 'auth.showPassword')"
                @click="showPassword = !showPassword"
              >
                <span class="mdi" :class="showPassword ? 'mdi-eye-off-outline' : 'mdi-eye-outline'"></span>
              </button>
            </div>
            <Transition name="fade">
              <p v-if="capsOn" class="form-hint caps-hint">
                <span class="mdi mdi-apple-keyboard-caps"></span>{{ $t('login.capsLockIsOn') }}</p>
            </Transition>
          </div>

          <button class="btn btn-primary auth-submit" :disabled="loading">
            <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
            {{ loading ? $t('login.signingIn') : $t('login.signIn') }}
          </button>
        </form>

        <div v-if="step === 'credentials' && !ssoMode && (providers.length || ssoAvailable)" class="auth-oauth">
          <div class="auth-divider"><span>{{ $t('login.orContinueWith') }}</span></div>
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
            <span class="mdi mdi-shield-key-outline"></span>{{ $t('login.continueWithSso') }}</button>
        </div>

        <p v-if="step === 'credentials' && !ssoMode" class="auth-footer">
          <i18n-t v-if="registrationEnabled" keypath="login.noAccount" tag="span">
            <template #link><RouterLink :to="{ name: 'register' }">{{ $t('login.createOne') }}</RouterLink></template>
          </i18n-t>
          <template v-else>{{ $t('login.noAccountContactAdmin') }}</template>
        </p>

        <!-- Operator links. rel="noopener noreferrer" on every one: these are
             admin-supplied URLs on a page shown to unauthenticated visitors. -->
        <nav v-if="brandLinks.length" class="auth-links" :aria-label="$t('login.siteLinks')">
          <a
            v-for="l in brandLinks"
            :key="l.url"
            :href="l.url"
            target="_blank"
            rel="noopener noreferrer"
          >{{ l.label }}</a>
        </nav>

        <AuthFooter />
      </div>
    </main>

    <button
      class="auth-theme-btn"
      type="button"
      :title="$t(theme.isDark ? 'auth.lightMode' : 'auth.darkMode')"
      :aria-label="$t(theme.isDark ? 'auth.switchToLight' : 'auth.switchToDark')"
      @click="theme.toggle()"
    >
      <span class="mdi" :class="theme.isDark ? 'mdi-weather-sunny' : 'mdi-weather-night'"></span>
    </button>

    <AuthLanguagePicker />
  </div>
</template>

<style scoped>
.auth {
  min-height: 100vh;
  display: grid;
  grid-template-columns: 1.05fr 1fr;
  background: var(--bg-primary);
}

/* Sits over the form panel, and stays put once the hero collapses. */
.auth-theme-btn {
  position: fixed;
  top: 20px;
  right: 20px;
  z-index: 10;
  display: flex;
  align-items: center;
  padding: 9px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  color: var(--text-tertiary);
  box-shadow: var(--shadow-sm);
  cursor: pointer;
  transition: all var(--transition);
}
.auth-theme-btn:hover {
  color: var(--text-primary);
  border-color: var(--border-input);
}
.auth-theme-btn .mdi {
  font-size: 18px;
  line-height: 1;
}

/* ─── Brand / marketing panel ─── */
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
.auth-brand-notice {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 10px 12px;
  margin-bottom: 16px;
  border-radius: var(--radius);
  background: var(--bg-secondary);
  color: var(--text-secondary, var(--text-primary));
  border: 1px solid var(--border-primary);
  font-size: 13px;
  white-space: pre-line;
}
.auth-brand-notice .mdi {
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
.auth-links {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 6px 16px;
  margin-top: 14px;
  font-size: 12px;
}
.auth-links a { color: var(--text-muted); text-decoration: none; }
.auth-links a:hover { color: var(--primary-600); text-decoration: underline; }

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
}
</style>
