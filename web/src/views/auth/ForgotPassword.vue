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

// Reached from the sign-in page, so it wears the operator's identity like the
// other front doors. Status is already fetched below; the brand rides along.
const brand = ref<Brand>({})
const brandName = computed(() => brand.value.name?.trim() || 'Miabi')
const brandLogo = computed(() => brand.value.logo_url?.trim() || '/brand/miabi-mark.svg')

const email = ref('')
const error = ref('')
const loading = ref(false)
const sent = ref(false)
const emailInput = ref<HTMLInputElement | null>(null)

onMounted(async () => {
  // Self-service reset is admin-gated; if it's off, there's nothing to do here —
  // send the user back to the login screen.
  try {
    const { data } = await authApi.status()
    if (!data.data?.password_reset_enabled) {
      router.replace({ name: 'login' })
      return
    }
    brand.value = data.data?.brand ?? {}
  } catch {
    // Status is best-effort; let the form render and the request decide.
  }
  theme.applyBrandAccent(brand.value.accent)
  emailInput.value?.focus()
})

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await authApi.forgotPassword(email.value.trim())
    // The API always returns 200 (it never reveals whether the address exists),
    // so we always show the same confirmation.
    sent.value = true
  } catch (e) {
    error.value = apiErrorMessage(e, 'Could not send the reset email. Please try again.')
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
    :title="sent ? 'Check your email' : 'Reset your password'"
    :subtitle="sent ? '' : 'Enter your account email and we\'ll send you a reset link.'"
    :error="error"
  >
    <!-- Confirmation state -->
    <template v-if="sent">
      <p class="forgot-sent">
        If an account exists for <strong>{{ email }}</strong>, a password reset
        link is on its way. The link expires in one hour.
      </p>
      <RouterLink :to="{ name: 'login' }" class="btn btn-primary auth-submit">
        Back to sign in
      </RouterLink>
    </template>

    <!-- Request form -->
    <form v-else class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label">Email</label>
        <input
          ref="emailInput"
          v-model="email"
          type="email"
          class="form-input"
          placeholder="you@example.com"
          autocomplete="email"
          aria-label="Email"
          :disabled="loading"
          required
        />
      </div>
      <button class="btn btn-primary auth-submit" :disabled="loading">
        <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
        {{ loading ? 'Sending…' : 'Send reset link' }}
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
  margin-top: 4px;
  text-decoration: none;
}
.forgot-sent {
  font-size: 14px;
  line-height: 1.6;
  color: var(--text-secondary, var(--text-muted));
  margin: 0 0 18px;
}
.auth-back {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  text-decoration: none;
}
.auth-back:hover {
  color: var(--text-primary);
}
</style>
