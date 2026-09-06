import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { authApi } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'

export type ThemeMode = 'light' | 'dark' | 'system'

// AdoptAction is what to do once the account's stored theme is known: take the
// server's value, push this device's value up to it, or leave both alone.
export type AdoptAction =
  | { kind: 'take'; mode: ThemeMode }
  | { kind: 'push'; mode: ThemeMode }
  | { kind: 'none' }

// themeAdoptAction decides how a device's current theme and the account's stored
// one are reconciled at sign-in. Extracted so the rule is testable without a DOM.
//
// unsynced means this device holds a pick the account has not accepted — one made
// on the sign-in screen, or one whose save failed. It wins over the stored value:
// it is the user's most recent intent, and the account's value is very often just
// the "system" default nobody chose.
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
  // localStorage is the pre-auth cache: it is read before /me can answer, so the
  // first paint does not flash the wrong theme. The user's stored preference is the
  // source of truth and overwrites it once the profile loads (see adopt).
  const stored = localStorage.getItem('miabi_theme') as ThemeMode | null
  const mode = ref<ThemeMode>(stored && ['light', 'dark', 'system'].includes(stored) ? stored as ThemeMode : 'system')
  // persist is off until a profile is loaded, so adopting the server's value does not
  // immediately write it back.
  let persist = false
  // unsynced marks a pick the account has not accepted: made before the profile was
  // known, or saved unsuccessfully. See themeAdoptAction.
  let unsynced = false

  const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')

  const isDark = computed(() => {
    if (mode.value === 'system') return mediaQuery.matches
    return mode.value === 'dark'
  })

  function apply() {
    document.documentElement.setAttribute('data-theme', isDark.value ? 'dark' : 'light')
  }

  // save persists the mode against the account AND refreshes the cached profile.
  // Both matter: adopt() reads the cached profile on the next boot, so a cache left
  // holding the old theme would overwrite the value just saved — which is exactly
  // how a chosen theme used to survive until the next refresh and then revert.
  function save(val: ThemeMode) {
    // The theme is already applied locally, so a failed save never costs the current
    // view; it is remembered as unsynced and retried at the next sign-in.
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

  function setMode(m: ThemeMode) {
    if (!persist) unsynced = true
    mode.value = m
  }

  function toggle() {
    setMode(isDark.value ? 'light' : 'dark')
  }

  // adopt reconciles this device's theme with the one stored against the account,
  // so a preference follows the user to a new browser. Called once the profile is
  // known.
  function adopt(serverMode: ThemeMode | undefined) {
    const action = themeAdoptAction(mode.value, serverMode, unsynced)
    if (action.kind === 'take') {
      unsynced = false
      mode.value = action.mode
    } else if (action.kind === 'push') {
      save(action.mode)
    }
    persist = true
  }

  // flush: 'sync' so the attribute lands before the next paint, and so adopting the
  // account's value runs this while persist is still false — an async watcher would
  // fire after adopt() had flipped it and echo the value straight back to the server.
  watch(mode, (val) => {
    localStorage.setItem('miabi_theme', val)
    apply()
    if (persist) save(val)
  }, { immediate: true, flush: 'sync' })

  // Listen for OS theme changes when in system mode
  mediaQuery.addEventListener('change', () => {
    if (mode.value === 'system') apply()
  })

  return { mode, isDark, toggle, setMode, adopt }
})
