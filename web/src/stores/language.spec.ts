// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
    expect(browserLanguage(['de-DE', 'fr-FR'])).toBe('fr')
  })

  it('falls back to English for an empty or entirely unknown list', () => {
    expect(browserLanguage([])).toBe('en')
    expect(browserLanguage(['de', 'ja'])).toBe('en')
  })
})
