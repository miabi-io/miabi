// Survives sign-in and SSO; if storage is unavailable the invitee can still accept from the in-app pending list.
const KEY = 'mb_pending_invite'

export function savePendingInvite(token: string) {
  try {
    sessionStorage.setItem(KEY, token)
  } catch { /* storage unavailable */ }
}

export function pendingInvite(): string | null {
  try {
    return sessionStorage.getItem(KEY)
  } catch {
    return null
  }
}

export function clearPendingInvite() {
  try {
    sessionStorage.removeItem(KEY)
  } catch { /* storage unavailable */ }
}
