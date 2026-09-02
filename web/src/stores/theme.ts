import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { authApi } from '@/api/auth'

export type ThemeMode = 'light' | 'dark' | 'system'

export const useThemeStore = defineStore('theme', () => {
  // localStorage is the pre-auth cache: it is read before /me can answer, so the
  // first paint does not flash the wrong theme. The user's stored preference is the
  // source of truth and overwrites it once the profile loads (see adopt).
  const stored = localStorage.getItem('miabi_theme') as ThemeMode | null
  const mode = ref<ThemeMode>(stored && ['light', 'dark', 'system'].includes(stored) ? stored as ThemeMode : 'system')
  // persist is off until a profile is loaded, so adopting the server's value does not
  // immediately write it back.
  let persist = false

  const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')

  const isDark = computed(() => {
    if (mode.value === 'system') return mediaQuery.matches
    return mode.value === 'dark'
  })

  function apply() {
    document.documentElement.setAttribute('data-theme', isDark.value ? 'dark' : 'light')
  }

  function setMode(m: ThemeMode) {
    mode.value = m
  }

  function toggle() {
    mode.value = isDark.value ? 'light' : 'dark'
  }

  // adopt takes the theme stored against the user's account, so it follows them to a
  // new browser. Called once the profile is known. A user who has never saved one
  // keeps whatever this device was already showing, and that value is written back —
  // otherwise upgrading would silently reset everyone to "system".
  function adopt(serverMode: ThemeMode | undefined) {
    if (serverMode && serverMode !== mode.value) {
      mode.value = serverMode
    } else if (!serverMode && mode.value !== 'system') {
      void authApi.updatePreferences({ theme: mode.value }).catch(() => {})
    }
    persist = true
  }

  watch(mode, (val) => {
    localStorage.setItem('miabi_theme', val)
    apply()
    if (persist) {
      // Best-effort: the theme is already applied locally, so a failed save costs a
      // preference on the next device, never the current view.
      void authApi.updatePreferences({ theme: val }).catch(() => {})
    }
  }, { immediate: true })

  // Listen for OS theme changes when in system mode
  mediaQuery.addEventListener('change', () => {
    if (mode.value === 'system') apply()
  })

  return { mode, isDark, toggle, setMode, adopt }
})
