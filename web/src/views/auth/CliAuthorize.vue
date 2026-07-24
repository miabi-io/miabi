<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { authApi } from '@/api/auth'
import { oauthApi, cliAuthorizeUrl } from '@/api/oauth'
import { useAuthStore } from '@/stores/auth'
import { apiErrorMessage } from '@/api/client'
import AuthShell from './AuthShell.vue'
import type { PublicProvider } from '@/api/types'

// `miabi login` loopback flow. The CLI opened this page with a local callback
// (?redirect_uri=http://127.0.0.1:PORT/…&state=…). We re-authenticate the user
// here — even if a console session exists (the OpenShift request-token property,
// same as /request-token) — then hand a single-use code back to the CLI's local
// server via a redirect. The token itself never rides the redirect URL.
type Step = 'confirm' | 'twofactor' | 'done' | 'invalid'

const route = useRoute()
const auth = useAuthStore()

const step = ref<Step>('confirm')
const loading = ref(false)
const error = ref('')

const username = ref(auth.user?.username || auth.user?.email || '')
const password = ref('')
const twoFactorCode = ref('')

const providers = ref<PublicProvider[]>([])

const redirectUri = (route.query.redirect_uri as string | undefined)?.trim() || ''
const state = (route.query.state as string | undefined)?.trim() || ''

// isLoopback mirrors the server's check: only an http URL pointing at the local
// machine may receive the login code. A mismatched target here means the link
// wasn't produced by the CLI — refuse rather than send credentials anywhere.
function isLoopback(uri: string): boolean {
  try {
    const u = new URL(uri)
    return u.protocol === 'http:' && ['127.0.0.1', 'localhost', '::1'].includes(u.hostname)
  } catch {
    return false
  }
}

const title = computed(() =>
  step.value === 'done'
    ? 'Signing you in…'
    : step.value === 'invalid'
      ? 'Invalid CLI login link'
      : step.value === 'twofactor'
        ? 'Two-factor authentication'
        : 'Authorize CLI login',
)
const subtitle = computed(() =>
  step.value === 'done'
    ? 'Return to your terminal — the Miabi CLI is finishing sign-in.'
    : step.value === 'invalid'
      ? 'This link is missing a valid local callback. Re-run `miabi login`.'
      : step.value === 'twofactor'
        ? 'Enter the code from your authenticator app'
        : 'Re-authenticate to sign the Miabi CLI in on this machine.',
)

onMounted(async () => {
  if (!isLoopback(redirectUri)) {
    step.value = 'invalid'
    return
  }
  try {
    providers.value = (await oauthApi.providers()).data.data?.providers ?? []
  } catch {
    /* password-only is fine */
  }
})

function deliver(to: string | undefined) {
  if (!to) {
    error.value = 'The server did not return a callback URL. Re-run `miabi login`.'
    return
  }
  // Hand the code back to the CLI's local server. It responds with a small
  // "you can close this window" page; we show 'done' meanwhile.
  step.value = 'done'
  window.location.href = to
}

async function submit() {
  if (loading.value) return
  error.value = ''
  loading.value = true
  try {
    const res = await authApi.loginToken(username.value.trim(), password.value, twoFactorCode.value || undefined, {
      redirectUri,
      state,
    })
    const data = res.data.data
    if (data.two_factor_required) {
      step.value = 'twofactor'
      return
    }
    deliver(data.redirect_to)
  } catch (e) {
    error.value = apiErrorMessage(e, step.value === 'twofactor' ? 'Invalid code' : 'Invalid credentials')
  } finally {
    loading.value = false
  }
}

function ssoLogin(slug: string) {
  // Full-page redirect into the IdP; the callback delivers the code to the CLI's
  // loopback callback directly (no return to this page).
  window.location.href = cliAuthorizeUrl(slug, redirectUri, state)
}
</script>

<template>
  <AuthShell :title="title" :subtitle="subtitle" :error="error">
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
        {{ loading ? 'Signing in…' : 'Authorize CLI' }}
      </button>

      <template v-if="providers.length">
        <div class="auth-divider"><span>or continue with</span></div>
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
        {{ loading ? 'Verifying…' : 'Verify & authorize' }}
      </button>
    </form>

    <!-- Delivering to the CLI -->
    <div v-else-if="step === 'done'" class="cli-status">
      <span class="mdi mdi-check-circle cli-status-icon"></span>
      <p>You can close this window and return to your terminal.</p>
    </div>

    <!-- Bad / missing loopback target -->
    <div v-else class="cli-status">
      <span class="mdi mdi-alert-circle-outline cli-status-icon warn"></span>
      <p>Re-run <code>miabi login</code> from your terminal to start a new sign-in.</p>
    </div>
  </AuthShell>
</template>

<style scoped>
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
.cli-status {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  text-align: center;
  color: var(--text-secondary, var(--text-muted));
}
.cli-status-icon {
  font-size: 44px;
  color: var(--primary-600);
}
.cli-status-icon.warn {
  color: var(--warning-600, #b45309);
}
.cli-status code {
  font-family: monospace;
  font-size: 13px;
}
</style>
