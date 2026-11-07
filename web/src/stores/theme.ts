import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { authApi } from '@/api/auth'
import type { AccentCode } from '@/api/types'
import { ACCENTS } from '@/theme/accents'
import { useAuthStore } from '@/stores/auth'

export type ThemeMode = 'light' | 'dark' | 'system'
export type { AccentCode } from '@/api/types'

export type AdoptAction =
  | { kind: 'take'; mode: ThemeMode }
  | { kind: 'push'; mode: ThemeMode }
  | { kind: 'none' }

// An unsynced device pick wins: it is the latest intent, and the account value is often the unchosen "system".
export function themeAdoptAction(
  local: ThemeMode,
  serverMode: ThemeMode | undefined,
  unsynced: boolean,
): AdoptAction {
  if (unsynced) return { kind: 'push', mode: local }
  if (serverMode) return serverMode === local ? { kind: 'none' } : { kind: 'take', mode: serverMode }
  return local === 'system' ? { kind: 'none' } : { kind: 'push', mode: local }
}

export const useThemeStore = defineStore('theme', () => {
  // Pre-auth cache read before /me answers, so the first paint does not flash the wrong theme.
  const stored = localStorage.getItem('miabi_theme') as ThemeMode | null
  const mode = ref<ThemeMode>(stored && ['light', 'dark', 'system'].includes(stored) ? stored as ThemeMode : 'system')
  // Off until a profile loads, so adopting the server's value does not write it straight back.
  let persist = false
  let unsynced = false

  const storedAccent = localStorage.getItem('miabi_accent') as AccentCode | null
  const accent = ref<AccentCode>(
    storedAccent && ACCENTS.some((a) => a.code === storedAccent) ? storedAccent : 'default',
  )
  let accentUnsynced = false
  // The operator enforces the brand accent, so there is nothing to pick, reconcile or save.
  const accentLocked = ref(false)

  const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')

  const isDark = computed(() => {
    if (mode.value === 'system') return mediaQuery.matches
    return mode.value === 'dark'
  })

  function apply() {
    document.documentElement.setAttribute('data-theme', isDark.value ? 'dark' : 'light')
  }

  // Separate from data-theme because accent and light/dark are independent axes.
  function applyAccent() {
    document.documentElement.setAttribute('data-accent', accent.value)
  }

  // Sign-in screens wear the operator's accent, never the cached accent of whoever signed in last.
  function applyBrandAccent(a?: AccentCode) {
    document.documentElement.setAttribute('data-accent', a || 'default')
  }

  function setAccent(a: AccentCode) {
    if (accentLocked.value) return
    if (!persist) accentUnsynced = true
    accent.value = a
  }

  // Also refresh the cached profile: adopt() reads it on boot, and a stale cache would revert the saved theme.
  function save(val: ThemeMode) {
    // A failed save is remembered as unsynced and retried at the next sign-in.
    void authApi
      .updatePreferences({ theme: val })
      .then((res) => {
        unsynced = false
        const auth = useAuthStore()
        if (auth.user) auth.setUser({ ...auth.user, preferences: res.data.data })
      })
      .catch(() => {
        unsynced = true
      })
  }

  function saveAccent(val: AccentCode) {
    void authApi
      .updatePreferences({ accent: val })
      .then((res) => {
        accentUnsynced = false
        const auth = useAuthStore()
        if (auth.user) auth.setUser({ ...auth.user, preferences: res.data.data })
      })
      .catch(() => {
        accentUnsynced = true
      })
  }

  function setMode(m: ThemeMode) {
    if (!persist) unsynced = true
    mode.value = m
  }

  function toggle() {
    setMode(isDark.value ? 'light' : 'dark')
  }

  function adopt(serverMode: ThemeMode | undefined, serverAccent?: AccentCode, serverAccentLocked = false) {
    // Taking the account's values must not echo them back: a resolved accent saved as a choice stops following the brand.
    persist = false
    const action = themeAdoptAction(mode.value, serverMode, unsynced)
    if (action.kind === 'take') {
      unsynced = false
      mode.value = action.mode
    } else if (action.kind === 'push') {
      save(action.mode)
    }
    accentLocked.value = serverAccentLocked
    if (serverAccentLocked && serverAccent) {
      accentUnsynced = false
      accent.value = serverAccent
    } else {
      // An accent chosen before the profile loaded is still the user's most recent intent.
      const accentAction = themeAdoptAction(
        accent.value as ThemeMode,
        serverAccent as ThemeMode | undefined,
        accentUnsynced,
      )
      if (accentAction.kind === 'take') {
        accentUnsynced = false
        accent.value = accentAction.mode as AccentCode
      } else if (accentAction.kind === 'push') {
        saveAccent(accentAction.mode as AccentCode)
      }
    }
    // A sign-in screen may have swapped in the brand accent, and the watcher only fires on change.
    applyAccent()
    persist = true
  }

  // Sync so the attribute lands before paint and runs while persist is still false, avoiding an echo to the server.
  watch(mode, (val) => {
    localStorage.setItem('miabi_theme', val)
    apply()
    if (persist) save(val)
  }, { immediate: true, flush: 'sync' })

  // Listen for OS theme changes when in system mode
  mediaQuery.addEventListener('change', () => {
    if (mode.value === 'system') apply()
  })

  watch(accent, (val) => {
    localStorage.setItem('miabi_accent', val)
    applyAccent()
    if (persist) saveAccent(val)
  }, { immediate: true, flush: 'sync' })

  return { mode, isDark, toggle, setMode, accent, accentLocked, setAccent, applyBrandAccent, adopt }
})
