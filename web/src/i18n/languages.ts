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


export const LANGUAGES = [
  { code: 'en', label: 'English', english: 'English' },
  { code: 'fr', label: 'Français', english: 'French' },
] as const

export type LanguageCode = (typeof LANGUAGES)[number]['code']

export const DEFAULT_LANGUAGE: LanguageCode = 'en'

function isLanguage(code: string): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code)
}

// Falls back from code to primary subtag ("fr-CA" is French) to English; mirrors models.ResolveLocale.
export function resolveLanguage(tag: string | null | undefined): LanguageCode {
  const t = (tag ?? '').trim().toLowerCase()
  if (isLanguage(t)) return t
  const primary = t.split(/[-_]/)[0]
  return isLanguage(primary) ? primary : DEFAULT_LANGUAGE
}
