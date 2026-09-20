<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const error = ref('')

onMounted(async () => {
  const err = route.query.error as string | undefined
  if (err) {
    router.replace({ name: 'login', query: { error: err } })
    return
  }
  // The server set the session cookie on the redirect here; hydrate the profile
  // (the cookie authenticates this request) to confirm sign-in.
  try {
    await auth.fetchUser()
    if (!auth.isAuthenticated) throw new Error('no session')
    router.replace('/')
  } catch {
    error.value = 'Failed to complete sign in.'
    router.replace({ name: 'login', query: { error: 'oauth_failed' } })
  }
})
</script>

<template>
  <div class="auth-callback">
    <span class="spinner"></span>
    <p>{{ error || $t('login.signingYouIn') }}</p>
  </div>
</template>

<style scoped>
.auth-callback {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 16px;
  color: var(--text-muted);
}
</style>
