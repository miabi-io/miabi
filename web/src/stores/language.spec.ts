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

import { describe, it, expect } from 'vitest'
import { languageAdoptAction, browserLanguage } from './language'

describe('languageAdoptAction', () => {
  it('pushes an unsynced pick, whatever the account says', () => {
    expect(languageAdoptAction('fr', 'en', true)).toEqual({ kind: 'push', code: 'fr' })
    expect(languageAdoptAction('fr', undefined, true)).toEqual({ kind: 'push', code: 'fr' })
  })

  it('takes the account value when this device disagrees', () => {
    expect(languageAdoptAction('en', 'fr', false)).toEqual({ kind: 'take', code: 'fr' })
  })

  it('does nothing when they already agree', () => {
    expect(languageAdoptAction('fr', 'fr', false)).toEqual({ kind: 'none' })
  })

  it('pushes a real local pick up to an account that has none', () => {
    expect(languageAdoptAction('fr', undefined, false)).toEqual({ kind: 'push', code: 'fr' })
  })

  it('does not push the English default as if it were a choice', () => {
    expect(languageAdoptAction('en', undefined, false)).toEqual({ kind: 'none' })
  })
})

describe('browserLanguage', () => {
  it('takes the first preference we ship', () => {
    expect(browserLanguage(['fr-CA', 'en-US'])).toBe('fr')
    expect(browserLanguage(['en-GB', 'fr'])).toBe('en')
  })

  it('skips languages we do not ship rather than letting the first tag win', () => {
    expect(browserLanguage(['it-IT', 'fr-FR'])).toBe('fr')
  })

  it('falls back to English for an empty or entirely unknown list', () => {
    expect(browserLanguage([])).toBe('en')
    expect(browserLanguage(['it', 'ja'])).toBe('en')
  })
})
