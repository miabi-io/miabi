import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi } from '@/api/auth'
import type { User, AuthResponse } from '@/api/types'

export const useAuthStore = defineStore('auth', () => {
  // The JWT lives only in an HttpOnly cookie set by the server, so it is never
  // readable from JavaScript (XSS can't exfiltrate it). We cache only the profile
  // for instant UI; the cookie authenticates every request, and a stale cache is
  // corrected by the 401 interceptor.
  const user = ref<User | null>(JSON.parse(localStorage.getItem('mb_user') || 'null'))

  const isAuthenticated = computed(() => !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')

  // A pending forced password change: the credentials were valid but the account
  // has an admin-set/reset password. We hold the short-lived reset token (in
  // memory only — not a session) until the user sets their own password. Lost on
  // reload, which just sends them back to sign in.
  const pendingReset = ref<string | null>(null)

  function setAuth(data: AuthResponse) {
    user.value = data.user || null
    localStorage.setItem('mb_user', JSON.stringify(user.value))
  }

  // LoginResult: 'ok' = signed in; 'twofactor' = a TOTP/recovery code is still
  // required (resubmit with the code); 'reset' = the account must set a new
  // password first (routed to the change-password screen with the reset token).
  type LoginResult = 'ok' | 'twofactor' | 'reset'
  async function login(identifier: string, password: string, twoFactorCode?: string): Promise<LoginResult> {
    const res = await authApi.login(identifier, password, twoFactorCode)
    const data = res.data.data
    if (data?.two_factor_required) return 'twofactor'
    if (data?.must_change_password && data.reset_token) {
      pendingReset.value = data.reset_token
      return 'reset'
    }
    if (!data?.user) {
      throw new Error('Login failed: unexpected server response')
    }
    setAuth(data)
    return 'ok'
  }

  // completeReset finishes the forced change: exchanges the reset token for a real
  // session (the server sets the cookie) and clears the pending state.
  async function completeReset(newPassword: string) {
    if (!pendingReset.value) throw new Error('No pending password reset')
    const res = await authApi.completePasswordReset(pendingReset.value, newPassword)
    if (!res.data.data?.user) throw new Error('Password reset failed: unexpected server response')
    setAuth(res.data.data)
    pendingReset.value = null
  }
  function cancelReset() {
    pendingReset.value = null
  }

  // clearSession drops all local auth state WITHOUT calling the API. Safe to call
  // repeatedly — used by the 401 interceptor, so it must never itself make a
  // request (that would 401 and re-trigger the interceptor in a loop). The cookie
  // is cleared server-side by logout(); here we only drop the cached profile.
  function clearSession() {
    user.value = null
    pendingReset.value = null
    localStorage.removeItem('mb_user')
    localStorage.removeItem('mb_workspace_id')
  }

  function logout() {
    // Best-effort server-side revoke, then clear local state regardless.
    authApi.logout().catch(() => {})
    clearSession()
  }

  async function fetchUser() {
    try {
      const res = await authApi.me()
      user.value = res.data.data
      localStorage.setItem('mb_user', JSON.stringify(res.data.data))
    } catch {
      // A failed /me means the session is gone; drop local state only (a 401 is
      // already handled by the API interceptor, which redirects to /login).
      clearSession()
    }
  }

  // Updates display name (and optionally the username handle), refreshing the cache.
  async function updateProfile(name: string, username?: string) {
    const res = await authApi.updateProfile(name, username)
    user.value = res.data.data
    localStorage.setItem('mb_user', JSON.stringify(res.data.data))
  }
  // updateName is a thin back-compat wrapper over updateProfile.
  async function updateName(name: string) {
    await updateProfile(name)
  }

  // Hides the getting-started checklist permanently; refreshes the cache so it survives a reload.
  async function dismissOnboarding() {
    const res = await authApi.dismissOnboarding()
    user.value = res.data.data
    localStorage.setItem('mb_user', JSON.stringify(res.data.data))
  }

  return { user, isAuthenticated, isAdmin, pendingReset, login, completeReset, cancelReset, logout, clearSession, fetchUser, updateName, updateProfile, dismissOnboarding }
})
