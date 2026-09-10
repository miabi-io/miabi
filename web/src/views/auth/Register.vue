<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { authApi } from '@/api/auth'
import { apiErrorMessage } from '@/api/client'
import AuthShell from './AuthShell.vue'
import type { Brand } from '@/api/types'
import { useThemeStore } from '@/stores/theme'

const router = useRouter()
const theme = useThemeStore()

// The sign-up page is a front door like signing in, so it wears the operator's
// identity too — same source, same fallbacks.
const brand = ref<Brand>({})
const brandName = computed(() => brand.value.name?.trim() || 'Miabi')
const brandLogo = computed(() => brand.value.logo_url?.trim() || '/brand/miabi-mark.svg')

const name = ref('')
const email = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)
const done = ref(false)
const verificationRequired = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)

// Matches handlers.MinRegistrationPasswordLen. Checked here for the message, and
// again on the server, which is the one that counts.
const MIN_PASSWORD = 12

onMounted(async () => {
  // Sign-up is off by default and can be switched off at any time, so a bookmarked
  // /register must not present a form that cannot succeed.
  try {
    const { data } = await authApi.status()
    if (!data.data?.registration_enabled) {
      router.replace({ name: 'login' })
      return
    }
    brand.value = data.data?.brand ?? {}
  } catch {
    // Status is best-effort; let the form render and the request decide.
  }
  theme.applyBrandAccent(brand.value.accent)
  nameInput.value?.focus()
})

async function submit() {
  error.value = ''
  if (password.value.length < MIN_PASSWORD) {
    error.value = `Password must be at least ${MIN_PASSWORD} characters.`
    return
  }
  loading.value = true
  try {
    const { data } = await authApi.register({
      name: name.value.trim(),
      email: email.value.trim(),
      password: password.value,
    })
    verificationRequired.value = data.data?.verification_required ?? false
    done.value = true
  } catch (e) {
    error.value = apiErrorMessage(e, 'Could not create your account. Please try again.')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <AuthShell
    hero
    :brand-name="brand.name ? brandName : undefined"
    :brand-logo="brandLogo"
    :title="done ? 'Account created' : `Create your ${brandName} account`"
    :subtitle="done ? '' : 'Sign up to get started.'"
    :error="error"
  >
    <!-- Confirmation. The wording never says whether the address was already
         registered: the API answers a duplicate the same way as a new account, so
         sign-up cannot be used to find out who has one. -->
    <template v-if="done">
      <p class="register-done">
        <template v-if="verificationRequired">
          If <strong>{{ email }}</strong> can be registered, a verification link is on its way.
          Verify your address, then sign in.
        </template>
        <template v-else>
          If <strong>{{ email }}</strong> can be registered, your account is ready. You can sign
          in now.
        </template>
      </p>
      <RouterLink :to="{ name: 'login' }" class="btn btn-primary auth-submit">
        Go to sign in
      </RouterLink>
    </template>

    <form v-else class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label" for="reg-name">Name</label>
        <input
          id="reg-name"
          ref="nameInput"
          v-model="name"
          class="form-input"
          placeholder="Ada Lovelace"
          autocomplete="name"
          :disabled="loading"
          required
        />
      </div>
      <div class="form-group">
        <label class="form-label" for="reg-email">Email</label>
        <input
          id="reg-email"
          v-model="email"
          type="email"
          class="form-input"
          placeholder="you@example.com"
          autocomplete="email"
          :disabled="loading"
          required
        />
      </div>
      <div class="form-group">
        <label class="form-label" for="reg-password">Password</label>
        <input
          id="reg-password"
          v-model="password"
          type="password"
          class="form-input"
          autocomplete="new-password"
          :minlength="MIN_PASSWORD"
          :disabled="loading"
          required
        />
        <p class="form-hint">At least {{ MIN_PASSWORD }} characters.</p>
      </div>
      <button class="btn btn-primary auth-submit" :disabled="loading">
        <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
        {{ loading ? 'Creating…' : 'Create account' }}
      </button>
    </form>

    <template #footer>
      <RouterLink :to="{ name: 'login' }" class="auth-back">
        <span class="mdi mdi-arrow-left"></span> Back to sign in
      </RouterLink>
    </template>
  </AuthShell>
</template>

<style scoped>
.auth-submit {
  width: 100%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}
.register-done {
  font-size: 14px;
  color: var(--text-secondary);
  line-height: 1.6;
  margin: 0 0 16px;
}
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.auth-back {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  text-decoration: none;
}
.auth-back:hover { color: var(--primary-600); }
</style>
