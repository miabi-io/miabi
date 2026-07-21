<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { authApi } from '@/api/auth'
import { oauthApi, authorizeUrl } from '@/api/oauth'
import { useAuthStore } from '@/stores/auth'
import { apiErrorMessage } from '@/api/client'
import AuthShell from './AuthShell.vue'
import type { LoginTokenResponse, PublicProvider } from '@/api/types'

// "Copy login command" — OpenShift-style. This view is reachable while signed in
// (opened from the user menu) but never mints off the session: the user must
// re-authenticate here, then lands on a one-time token + CLI command display.
// It also handles the SSO return (?handoff=…), where the token was minted by the
// OAuth callback and stashed for a single-use claim.
type Step = 'confirm' | 'twofactor' | 'display'

const route = useRoute()
const auth = useAuthStore()

const step = ref<Step>('confirm')
const loading = ref(false)
const error = ref('')

const username = ref(auth.user?.username || auth.user?.email || '')
const password = ref('')
const twoFactorCode = ref('')

const providers = ref<PublicProvider[]>([])
const token = ref<LoginTokenResponse | null>(null)

const title = computed(() =>
  step.value === 'display'
    ? 'Your CLI login token'
    : step.value === 'twofactor'
      ? 'Two-factor authentication'
      : 'Confirm your identity',
)
const subtitle = computed(() =>
  step.value === 'display'
    ? 'Use this token with the Miabi CLI or the API. It is shown once.'
    : step.value === 'twofactor'
      ? 'Enter the code from your authenticator app'
      : 'Re-authenticate to generate a short-lived API token for the CLI.',
)

onMounted(async () => {
  // SSO hand-off: the OAuth callback minted the token and redirected here with a
  // single-use reference. Claim it and go straight to the display.
  const handoff = route.query.handoff as string | undefined
  if (handoff) {
    loading.value = true
    try {
      token.value = (await authApi.claimLoginToken(handoff)).data.data
      step.value = 'display'
    } catch (e) {
      error.value = apiErrorMessage(e, 'This login token is no longer available. Request a new one.')
    } finally {
      loading.value = false
    }
    return
  }
  // Offer SSO re-auth buttons when providers exist.
  try {
    providers.value = (await oauthApi.providers()).data.data?.providers ?? []
  } catch {
    /* password-only is fine */
  }
})

async function submit() {
  if (loading.value) return
  error.value = ''
  loading.value = true
  try {
    const res = await authApi.loginToken(username.value.trim(), password.value, twoFactorCode.value || undefined)
    const data = res.data.data
    if (data.two_factor_required) {
      step.value = 'twofactor'
      return
    }
    token.value = data
    step.value = 'display'
  } catch (e) {
    error.value = apiErrorMessage(e, step.value === 'twofactor' ? 'Invalid code' : 'Invalid credentials')
  } finally {
    loading.value = false
  }
}

function ssoLogin(slug: string) {
  // Full-page redirect into the IdP with intent=login_token (forces fresh auth).
  window.location.href = authorizeUrl(slug, 'login_token')
}

function requestAnother() {
  token.value = null
  password.value = ''
  twoFactorCode.value = ''
  step.value = 'confirm'
  error.value = ''
}

const copied = ref('')
async function copy(text: string | undefined, what: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = what
    setTimeout(() => (copied.value = ''), 1500)
  } catch {
    /* clipboard unavailable */
  }
}

// baseUrl is the panel root the CLI/curl target: the server-provided URL
// (MIABI_WEB_URL) when configured, otherwise the browser's current origin — so
// the commands always show the URL the user actually reached the console at.
function baseUrl(t: LoginTokenResponse): string {
  return (t.server_url?.trim() || window.location.origin).replace(/\/+$/, '')
}
function loginCommand(t: LoginTokenResponse): string {
  return `miabi login --token ${t.token} --server ${baseUrl(t)}`
}
function curlExample(t: LoginTokenResponse): string {
  return `curl -H "Authorization: Bearer ${t.token}" ${baseUrl(t)}/api/v1/me`
}
function envExport(t: LoginTokenResponse): string {
  return `export MIABI_URL=${baseUrl(t)}\nexport MIABI_TOKEN=${t.token}`
}

function expiryLabel(t: LoginTokenResponse): string {
  if (!t.expires_at) return ''
  return new Date(t.expires_at).toLocaleString()
}
</script>

<template>
  <AuthShell :class="{ wide: step === 'display' }" :title="title" :subtitle="subtitle" :error="error">
    <!-- Confirm identity (password) -->
    <form v-if="step === 'confirm'" class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label">Username or email</label>
        <input v-model="username" type="text" class="form-input" autocomplete="username" required autofocus />
      </div>
      <div class="form-group">
        <label class="form-label">Password</label>
        <input v-model="password" type="password" class="form-input" autocomplete="current-password" required />
      </div>
      <button type="submit" class="btn btn-primary auth-submit" :disabled="loading">
        <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
        {{ loading ? 'Verifying…' : 'Generate token' }}
      </button>

      <template v-if="providers.length">
        <div class="auth-divider"><span>or re-authenticate with</span></div>
        <button
          v-for="p in providers"
          :key="p.name"
          type="button"
          class="btn btn-secondary auth-submit sso-btn"
          @click="ssoLogin(p.name)"
        >
          <span class="mdi mdi-login-variant"></span> {{ p.display_name }}
        </button>
      </template>
    </form>

    <!-- Two-factor -->
    <form v-else-if="step === 'twofactor'" class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label">Authentication code</label>
        <input
          v-model="twoFactorCode"
          type="text"
          inputmode="numeric"
          class="form-input totp-input"
          placeholder="000000"
          maxlength="6"
          autocomplete="one-time-code"
          autofocus
        />
      </div>
      <button type="submit" class="btn btn-primary auth-submit" :disabled="loading">
        <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
        {{ loading ? 'Verifying…' : 'Verify & generate' }}
      </button>
    </form>

    <!-- Display token + commands -->
    <div v-else-if="step === 'display' && token" class="token-display">
      <div class="token-field">
        <div class="token-label">API token <span class="token-once">shown once</span></div>
        <div class="token-value">
          <code>{{ token.token }}</code>
          <button type="button" class="copy-btn" title="Copy token" @click="copy(token.token, 'token')">
            <span class="mdi" :class="copied === 'token' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
        <div v-if="token.sha256" class="token-fingerprint">sha256 · {{ token.sha256.slice(0, 16) }}…</div>
      </div>

      <div class="token-field">
        <div class="token-label">Log in with the Miabi CLI</div>
        <div class="token-value">
          <code>{{ loginCommand(token) }}</code>
          <button type="button" class="copy-btn" title="Copy command" @click="copy(loginCommand(token), 'cmd')">
            <span class="mdi" :class="copied === 'cmd' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
      </div>

      <div class="token-field">
        <div class="token-label">CI / scripts</div>
        <div class="token-value">
          <code class="pre">{{ envExport(token) }}</code>
          <button type="button" class="copy-btn" title="Copy" @click="copy(envExport(token), 'env')">
            <span class="mdi" :class="copied === 'env' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
      </div>

      <div class="token-field">
        <div class="token-label">Call the API directly</div>
        <div class="token-value">
          <code>{{ curlExample(token) }}</code>
          <button type="button" class="copy-btn" title="Copy" @click="copy(curlExample(token), 'curl')">
            <span class="mdi" :class="copied === 'curl' ? 'mdi-check' : 'mdi-content-copy'"></span>
          </button>
        </div>
      </div>

      <p class="token-meta">
        <span class="mdi mdi-clock-outline"></span>
        Expires {{ expiryLabel(token) }}<span v-if="token.scopes?.length"> · scopes: {{ token.scopes.join(', ') }}</span>
      </p>

      <div class="token-actions">
        <button type="button" class="btn btn-secondary" @click="requestAnother">Request another token</button>
        <RouterLink to="/api-keys" class="btn btn-link">Manage API keys</RouterLink>
      </div>
    </div>
  </AuthShell>
</template>

<style scoped>
/* Widen the shared card for the multi-line token display. Vue adds this
   component's scope id to the AuthShell root, so :deep reaches its .auth-card. */
.wide :deep(.auth-card) {
  max-width: 560px;
}

.auth-submit {
  width: 100%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin-top: 4px;
  text-decoration: none;
}
.sso-btn {
  margin-top: 8px;
}
.auth-divider {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 18px 0 10px;
  color: var(--text-muted);
  font-size: 12px;
}
.auth-divider::before,
.auth-divider::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border-primary);
}

.token-display {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.token-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.token-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary, var(--text-muted));
  text-transform: uppercase;
  letter-spacing: 0.03em;
}
.token-once {
  margin-left: 8px;
  color: var(--warning-600, #b45309);
  font-weight: 500;
  text-transform: none;
  letter-spacing: 0;
}
.token-value {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  background: var(--bg-secondary);
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  padding: 10px 12px;
}
.token-value code {
  flex: 1;
  font-family: monospace;
  font-size: 13px;
  word-break: break-all;
  color: var(--text-primary);
}
.token-value code.pre {
  white-space: pre-wrap;
}
.copy-btn {
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text-muted);
  padding: 2px;
  flex-shrink: 0;
}
.copy-btn:hover {
  color: var(--primary-600);
}
.token-fingerprint {
  font-family: monospace;
  font-size: 11px;
  color: var(--text-muted);
}
.token-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  margin: 4px 0 0;
}
.token-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 4px;
}
.btn-link {
  background: none;
  border: none;
  color: var(--primary-600);
}
</style>
