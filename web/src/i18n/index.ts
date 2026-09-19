// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createI18n } from 'vue-i18n'
import en from './en.json'
import { datetimeFormats, numberFormats } from './formats'
import { DEFAULT_LANGUAGE, resolveLanguage, type LanguageCode } from './languages'

type Catalogue = typeof en
const catalogues = import.meta.glob<{ default: Partial<Catalogue> }>('./*.json')

export const i18n = createI18n({
  legacy: false,
  locale: DEFAULT_LANGUAGE,
  fallbackLocale: DEFAULT_LANGUAGE,
  messages: { en },
  datetimeFormats,
  numberFormats,
  missingWarn: false,
  fallbackWarn: false,
})

const loaded = new Set<LanguageCode>([DEFAULT_LANGUAGE])

// setLanguage loads a catalogue if it is not yet in memory, then switches to it. Safe to
// call with an unknown or stale tag: it resolves through the shipped list first.
export async function setLanguage(tag: string | null | undefined): Promise<LanguageCode> {
  const code = resolveLanguage(tag)
  if (!loaded.has(code)) {
    const load = catalogues[`./${code}.json`]
    if (load) {
      try {
        const mod = await load()
        i18n.global.setLocaleMessage(code, mod.default as Catalogue)
        loaded.add(code)
      } catch {
        return i18n.global.locale.value as LanguageCode
      }
    }
  }
  i18n.global.locale.value = code
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('lang', code)
  }
  return code
}

// t is the translator for code outside a component — stores, API helpers, utils — where
// useI18n() has no instance to attach to.
export function t(key: string, named?: Record<string, unknown>): string {
  return named ? i18n.global.t(key, named) : i18n.global.t(key)
}

// activeLanguage is the language in force, for Intl calls that do not go through $d/$n.
export function activeLanguage(): LanguageCode {
  return i18n.global.locale.value as LanguageCode
}
