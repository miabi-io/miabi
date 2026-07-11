<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { apiErrorMessage } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import AuthShell from './AuthShell.vue'

const router = useRouter()
const auth = useAuthStore()

const next = ref('')
const confirm = ref('')
const showPassword = ref(false)
const error = ref('')
const loading = ref(false)
const nextInput = ref<HTMLInputElement | null>(null)

const MIN_LEN = 8
const tooShort = computed(() => next.value.length > 0 && next.value.length < MIN_LEN)
const mismatch = computed(() => confirm.value.length > 0 && confirm.value !== next.value)
const canSubmit = computed(() => next.value.length >= MIN_LEN && next.value === confirm.value)

onMounted(() => {
  // No reset session (e.g. a reload dropped it) — send them back to sign in.
  if (!auth.pendingReset) {
    router.replace({ name: 'login' })
    return
  }
  nextInput.value?.focus()
})

async function submit() {
  if (!canSubmit.value) return
  error.value = ''
  loading.value = true
  try {
    await auth.completeReset(next.value)
    router.replace({ name: 'dashboard' })
  } catch (e) {
    error.value = apiErrorMessage(e, 'This password-reset session has expired. Sign in again.')
  } finally {
    loading.value = false
  }
}

function signOut() {
  auth.cancelReset()
  router.replace({ name: 'login' })
}
</script>

<template>
  <AuthShell
    title="Set a new password"
    subtitle="Your password was set by an administrator. Choose your own to continue."
    :error="error"
  >
    <form class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label">New password</label>
        <div class="password-wrap">
          <input
            ref="nextInput"
            v-model="next"
            :type="showPassword ? 'text' : 'password'"
            class="form-input"
            placeholder="At least 8 characters"
            autocomplete="new-password"
            aria-label="New password"
            :disabled="loading"
            required
          />
          <button
            type="button"
            class="password-toggle"
            :aria-label="showPassword ? 'Hide password' : 'Show password'"
            @click="showPassword = !showPassword"
          >
            <span class="mdi" :class="showPassword ? 'mdi-eye-off-outline' : 'mdi-eye-outline'"></span>
          </button>
        </div>
        <p v-if="tooShort" class="form-hint hint-warn">Use at least {{ MIN_LEN }} characters.</p>
      </div>

      <div class="form-group">
        <label class="form-label">Confirm new password</label>
        <input
          v-model="confirm"
          :type="showPassword ? 'text' : 'password'"
          class="form-input"
          placeholder="Re-enter your new password"
          autocomplete="new-password"
          aria-label="Confirm new password"
          :disabled="loading"
          required
        />
        <p v-if="mismatch" class="form-hint hint-warn">Passwords don't match.</p>
      </div>

      <button class="btn btn-primary auth-submit" :disabled="loading || !canSubmit">
        <span v-if="loading" class="mdi mdi-loading mdi-spin"></span>
        {{ loading ? 'Saving…' : 'Set password & continue' }}
      </button>
    </form>

    <template #footer>
      <button type="button" class="auth-back" :disabled="loading" @click="signOut">
        <span class="mdi mdi-logout"></span> Sign out
      </button>
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
.hint-warn {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  color: var(--warning-700, var(--text-muted));
}
.auth-back {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  text-decoration: none;
  background: none;
  border: none;
  cursor: pointer;
}
.auth-back:hover {
  color: var(--text-primary);
}
</style>
