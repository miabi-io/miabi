import api from './client'
import type { ApiResponse, AuthResponse, AuthStatus, RecoveryCodes, Session, TwoFactorSetup, User } from './types'

export const authApi = {
  // Advertises which auth features are enabled (e.g. self-service password
  // reset), so the login/forgot screens can render conditionally.
  status() {
    return api.get<ApiResponse<AuthStatus>>('/auth/status')
  },
  // identifier is a username or an email address (sent as `username`).
  login(identifier: string, password: string, twoFactorCode?: string) {
    return api.post<ApiResponse<AuthResponse>>('/auth/login', {
      username: identifier,
      password,
      two_factor_code: twoFactorCode,
    })
  },
  // Request a password-reset email. Always resolves (the API returns 200
  // regardless of whether the address exists) to avoid leaking accounts.
  forgotPassword(email: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/forgot-password', { email })
  },
  // Consume a reset token (from the email link) and set a new password.
  resetPassword(token: string, password: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/reset-password', { token, password })
  },
  logout() {
    return api.post<ApiResponse<{ message: string }>>('/auth/logout')
  },
  me() {
    return api.get<ApiResponse<User>>('/me')
  },
  updateProfile(name: string, username?: string) {
    return api.put<ApiResponse<User>>('/me', { name, username })
  },
  dismissOnboarding() {
    return api.post<ApiResponse<User>>('/me/onboarding/dismiss')
  },
  listSessions() {
    return api.get<ApiResponse<Session[]>>('/me/sessions')
  },
  revokeSession(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/me/sessions/${id}`)
  },
  revokeOtherSessions() {
    return api.post<ApiResponse<{ message: string; revoked: number }>>('/me/sessions/revoke-others')
  },
  changePassword(currentPassword: string, newPassword: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/change-password', {
      current_password: currentPassword,
      new_password: newPassword,
    })
  },
  // Finish a forced password change: exchange the short-lived reset token from
  // login (plus a new password) for a real session.
  completePasswordReset(resetToken: string, newPassword: string) {
    return api.post<ApiResponse<AuthResponse>>('/auth/complete-password-reset', {
      reset_token: resetToken,
      new_password: newPassword,
    })
  },

  // --- Two-factor authentication (TOTP) ---
  setupTwoFactor() {
    return api.post<ApiResponse<TwoFactorSetup>>('/auth/2fa/setup')
  },
  verifyTwoFactor(code: string) {
    return api.post<ApiResponse<RecoveryCodes>>('/auth/2fa/verify', { code })
  },
  disableTwoFactor(code: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/2fa/disable', { code })
  },
  regenerateRecoveryCodes(code: string) {
    return api.post<ApiResponse<RecoveryCodes>>('/auth/2fa/recovery-codes', { code })
  },
}
