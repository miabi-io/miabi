// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { authApi } from '@/api/auth'
import { setLanguage } from '@/i18n'
import { DEFAULT_LANGUAGE, resolveLanguage, type LanguageCode } from '@/i18n/languages'
import { useAuthStore } from '@/stores/auth'

const STORAGE_KEY = 'miabi_language'

// AdoptAction reconciles this device's language with the account's.
export type AdoptAction =
  | { kind: 'take'; code: LanguageCode }
  | { kind: 'push'; code: LanguageCode }
  | { kind: 'none' }

// languageAdoptAction mirrors themeAdoptAction (stores/theme.ts). English is never
// pushed: it is the never-chosen default, and writing it up would overwrite a real pick
// made on another device.
export function languageAdoptAction(
  local: LanguageCode,
  server: LanguageCode | undefined,
  unsynced: boolean,
): AdoptAction {
  if (unsynced) return { kind: 'push', code: local }
  if (server) return server === local ? { kind: 'none' } : { kind: 'take', code: server }
  return local === DEFAULT_LANGUAGE ? { kind: 'none' } : { kind: 'push', code: local }
}

// browserLanguage is the first browser preference we ship, for a visitor with no account.
export function browserLanguage(tags: readonly string[] = navigator.languages ?? []): LanguageCode {
  for (const tag of tags) {
    const code = resolveLanguage(tag)
    // resolveLanguage answers English for anything unshipped, so an unknown first tag
    // would otherwise win over a shipped later one.
    if (code !== DEFAULT_LANGUAGE || /^en\b/i.test(tag)) return code
  }
  return DEFAULT_LANGUAGE
}

export const useLanguageStore = defineStore('language', () => {
  // Read before /me can answer, so a returning user is not shown the wrong language.
  const cached = localStorage.getItem(STORAGE_KEY)
  const current = ref<LanguageCode>(cached ? resolveLanguage(cached) : browserLanguage())

  // Off until the profile loads, so adopting the server's value is not written back.
  let persist = false
  // A pick the account has not accepted: made before sign-in, or saved unsuccessfully.
  let unsynced = false

  // Caches what setLanguage reports, not what was asked: a catalogue that fails to load
  // stays on the previous language.
  async function apply(code: LanguageCode) {
    const effective = await setLanguage(code)
    current.value = effective
    localStorage.setItem(STORAGE_KEY, effective)
  }

  // Already applied locally, so a failed save costs nothing now and is retried at the
  // next sign-in.
  function save(code: LanguageCode) {
    void authApi
      .updatePreferences({ locale: code })
      .then((res) => {
        unsynced = false
        const auth = useAuthStore()
        if (auth.user) auth.setUser({ ...auth.user, preferences: res.data.data })
      })
      .catch(() => {
        unsynced = true
      })
  }

  async function set(code: LanguageCode) {
    if (!persist) unsynced = true
    await apply(code)
    if (persist) save(code)
  }

  // adopt reconciles with the account once the profile is known.
  async function adopt(server: string | null | undefined) {
    persist = false
    const stored = server ? resolveLanguage(server) : undefined
    const action = languageAdoptAction(current.value, stored, unsynced)
    if (action.kind === 'take') {
      unsynced = false
      await apply(action.code)
    } else {
      await apply(current.value)
      if (action.kind === 'push') save(action.code)
    }
    persist = true
  }

  // Applies the cached language before the profile loads, so the first paint is right.
  async function init() {
    await apply(current.value)
  }

  return { current, set, adopt, init }
})
