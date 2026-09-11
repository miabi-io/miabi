// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later


export const LANGUAGES = [
  { code: 'en', label: 'English', english: 'English' },
  { code: 'fr', label: 'Français', english: 'French' },
] as const

export type LanguageCode = (typeof LANGUAGES)[number]['code']

export const DEFAULT_LANGUAGE: LanguageCode = 'en'

function isLanguage(code: string): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code)
}

// resolveLanguage maps a stored or browser tag onto a shipped language: the code, then
// its primary subtag ("fr-CA" is French), then English. Mirrors models.ResolveLocale.
export function resolveLanguage(tag: string | null | undefined): LanguageCode {
  const t = (tag ?? '').trim().toLowerCase()
  if (isLanguage(t)) return t
  const primary = t.split(/[-_]/)[0]
  return isLanguage(primary) ? primary : DEFAULT_LANGUAGE
}
