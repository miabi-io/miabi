// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { ref } from 'vue'
import api from '@/api/client'
import type { ApiResponse, ElevationState } from '@/api/types'

// Error codes the API returns when the admin console needs a (fresh) unlock.
export const ELEVATION_CODES = ['ADMIN_ELEVATION_REQUIRED', 'ADMIN_ELEVATION_STALE', 'ADMIN_2FA_SETUP_REQUIRED'] as const
export type ElevationCode = (typeof ELEVATION_CODES)[number]

// The admin console unlock. Requests that fail for want of an unlock wait on requestUnlock(),
// which opens the dialog once however many requests are waiting, and resolve together.
export const useElevationStore = defineStore('elevation', () => {
  const state = ref<ElevationState | null>(null)
  const dialogOpen = ref(false)
  const reason = ref<ElevationCode>('ADMIN_ELEVATION_REQUIRED')
  const expiringSoon = ref(false)
  let waiters: ((ok: boolean) => void)[] = []
  let expiryTimer: ReturnType<typeof setTimeout> | null = null

  async function refresh(): Promise<ElevationState | null> {
    try {
      state.value = (await api.get<ApiResponse<ElevationState>>('/admin/elevation')).data.data
    } catch {
      state.value = null
    }
    scheduleExpiryWarning()
    return state.value
  }

  function requestUnlock(code: ElevationCode = 'ADMIN_ELEVATION_REQUIRED'): Promise<boolean> {
    reason.value = code
    dialogOpen.value = true
    return new Promise((resolve) => waiters.push(resolve))
  }

  function settle(ok: boolean) {
    dialogOpen.value = false
    const pending = waiters
    waiters = []
    pending.forEach((resolve) => resolve(ok))
  }

  async function unlock(code: string) {
    state.value = (await api.post<ApiResponse<ElevationState>>('/admin/elevate', { factor: 'totp', code })).data.data
    expiringSoon.value = false
    scheduleExpiryWarning()
    settle(true)
  }

  async function lock() {
    state.value = (await api.delete<ApiResponse<ElevationState>>('/admin/elevation')).data.data
    scheduleExpiryWarning()
  }

  // Ensures the console is unlocked before an admin page loads. False means the admin declined.
  async function ensure(): Promise<boolean> {
    const st = state.value?.active ? state.value : await refresh()
    if (!st || !st.required || st.active) return true
    return requestUnlock()
  }

  // Warns a minute before the unlock lapses, rather than waiting for a failed request. Idle
  // expiry moves as the admin works, so re-read the state before warning.
  function scheduleExpiryWarning() {
    if (expiryTimer) clearTimeout(expiryTimer)
    expiryTimer = null
    const st = state.value
    if (!st?.active) return
    const ends = [st.expires_at, st.idle_expires_at].filter(Boolean).map((t) => new Date(t as string).getTime())
    if (!ends.length) return
    const delay = Math.min(...ends) - Date.now() - 60_000
    if (delay <= 0) {
      expiringSoon.value = true
      return
    }
    expiryTimer = setTimeout(async () => {
      const fresh = await refresh()
      const left = fresh?.active ? Math.min(...[fresh.expires_at, fresh.idle_expires_at].filter(Boolean).map((t) => new Date(t as string).getTime())) - Date.now() : 0
      expiringSoon.value = !!fresh?.active && left <= 70_000
    }, delay)
  }

  return { state, dialogOpen, reason, expiringSoon, refresh, requestUnlock, settle, unlock, lock, ensure }
})
