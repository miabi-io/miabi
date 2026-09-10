<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { authApi } from '@/api/auth'
import { apiErrorMessage } from '@/api/client'
import AuthShell from './AuthShell.vue'

const route = useRoute()
const router = useRouter()
const state = ref<'working' | 'done' | 'failed'>('working')
const error = ref('')

// Arrived at from a link in an email, so no hero: the visitor already knows who
// they are and is here to finish one thing.
onMounted(async () => {
  const token = String(route.query.token ?? '')
  // Drop it from the address bar before doing anything with it: a token left in
  // the URL is copied into browser history, and into the Referer of every request
  // the page makes afterwards.
  if (token) router.replace({ name: 'verify-email', query: {} })
  if (!token) {
    state.value = 'failed'
    error.value = 'That link is missing its verification token.'
    return
  }
  try {
    await authApi.verifyEmail(token)
    state.value = 'done'
  } catch (e) {
    state.value = 'failed'
    error.value = apiErrorMessage(e, 'That verification link is invalid or has expired.')
  }
})
</script>

<template>
  <AuthShell
    :title="state === 'done' ? 'Email verified' : state === 'failed' ? 'Could not verify' : 'Verifying your email'"
    :subtitle="state === 'working' ? 'One moment…' : ''"
    :error="state === 'failed' ? error : ''"
  >
    <p v-if="state === 'working'" class="verify-note">
      <span class="mdi mdi-loading mdi-spin"></span> Checking your link.
    </p>
    <template v-else-if="state === 'done'">
      <p class="verify-note">Your address is confirmed. You can sign in now.</p>
      <RouterLink :to="{ name: 'login' }" class="btn btn-primary auth-submit">Go to sign in</RouterLink>
    </template>
    <template v-else>
      <p class="verify-note">
        Verification links expire and can only be used once. Sign in to have a new one sent, or ask
        your platform administrator to verify the address for you.
      </p>
      <RouterLink :to="{ name: 'login' }" class="btn btn-primary auth-submit">Back to sign in</RouterLink>
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
.verify-note {
  font-size: 14px;
  color: var(--text-secondary);
  line-height: 1.6;
  margin: 0 0 16px;
}
</style>
