// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
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
  // $t/$d/$n in templates without a useI18n() call in every component.
  globalInjection: true,
  missingWarn: false,
  fallbackWarn: false,
})

const loaded = new Set<LanguageCode>([DEFAULT_LANGUAGE])

// Safe with an unknown or stale tag: it resolves through the shipped list first.
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

// For code outside a component, where useI18n() has no instance to attach to.
export function t(key: string, named?: Record<string, unknown>): string {
  return named ? i18n.global.t(key, named) : i18n.global.t(key)
}

// activeLanguage is the language in force, for Intl calls that do not go through $d/$n.
export function activeLanguage(): LanguageCode {
  return i18n.global.locale.value as LanguageCode
}

// Data files carry English too so they stay readable and keep working if a key is missed.
export function label(key: string, fallback: string): string {
  return i18n.global.te(key) ? i18n.global.t(key) : fallback
}
