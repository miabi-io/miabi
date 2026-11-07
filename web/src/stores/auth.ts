import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi } from '@/api/auth'
import type { User, AuthResponse } from '@/api/types'
import { getPasskey } from '@/utils/webauthn'

export const useAuthStore = defineStore('auth', () => {
  // The JWT is an HttpOnly cookie so XSS can't read it; only the profile is cached, and the 401 interceptor fixes staleness.
  const user = ref<User | null>(JSON.parse(localStorage.getItem('mb_user') || 'null'))

  const isAuthenticated = computed(() => !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')
  // Mirrors the server's creation policy so the console hides an action that would be refused.
  const canCreateWorkspace = computed(() => user.value?.workspace_creation?.allowed ?? true)
  // Set when workspaces are created in Miabi Console instead (MIABI_WORKSPACE_CREATION=console).
  const consoleCreateUrl = computed(() => {
    const p = user.value?.workspace_creation
    return p?.mode === 'console' ? p.console_url || '' : ''
  })

  // The reset token is held in memory only, so a reload just sends the user back to sign in.
  const pendingReset = ref<string | null>(null)

  function setAuth(data: AuthResponse) {
    user.value = data.user || null
    localStorage.setItem('mb_user', JSON.stringify(user.value))
  }

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

  // A passkey is both factors, so there is no 'twofactor' step.
  async function loginWithPasskey(): Promise<'ok' | 'reset'> {
    const begin = (await authApi.passkeyLoginBegin()).data.data
    const credential = await getPasskey(begin.options.publicKey)
    const data = (await authApi.passkeyLogin(begin.token, credential)).data.data
    if (data?.must_change_password && data.reset_token) {
      pendingReset.value = data.reset_token
      return 'reset'
    }
    if (!data?.user) throw new Error('Login failed: unexpected server response')
    setAuth(data)
    return 'ok'
  }

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

  // Used by the 401 interceptor, so it must never make a request (that would loop); logout() clears the cookie.
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
      // A 401 is already handled by the API interceptor, which redirects to /login.
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

  function setUser(u: User) {
    user.value = u
    localStorage.setItem('mb_user', JSON.stringify(u))
  }

  // Hides the getting-started checklist permanently; refreshes the cache so it survives a reload.
  async function dismissOnboarding() {
    const res = await authApi.dismissOnboarding()
    user.value = res.data.data
    localStorage.setItem('mb_user', JSON.stringify(res.data.data))
  }

  return { user, isAuthenticated, isAdmin, canCreateWorkspace,
    consoleCreateUrl, pendingReset, login, loginWithPasskey, completeReset, cancelReset, logout, clearSession, fetchUser, setUser, updateName, updateProfile, dismissOnboarding }
})
