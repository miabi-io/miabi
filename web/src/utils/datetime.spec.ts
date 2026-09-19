// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'vitest'
import { setLanguage } from '@/i18n'
import { fmtDate, fmtDateTime, fmtNumber } from './datetime'

describe('locale-aware formatting', () => {
  const iso = '2026-09-09T14:03:00Z'

  beforeEach(async () => {
    await setLanguage('en')
  })

  it('formats a date in the active language', async () => {
    const english = fmtDate(iso)
    await setLanguage('fr')
    const french = fmtDate(iso)

    expect(english).not.toBe(french)
    expect(english).toMatch(/Sep/)
    expect(french).toMatch(/sept/)
  })

  it('formats a number in the active language', async () => {
    expect(fmtNumber(1234567.5)).toBe('1,234,567.5')
    await setLanguage('fr')
    // French groups with a narrow no-break space, which a literal in source would hide.
    expect(fmtNumber(1234567.5)).not.toContain(',234')
    expect(fmtNumber(1234567.5)).toContain('5')
  })

  it('renders an em dash for empty or unparseable input', async () => {
    for (const bad of [null, undefined, '', 'not-a-date']) {
      expect(fmtDate(bad)).toBe('—')
      expect(fmtDateTime(bad)).toBe('—')
    }
    expect(fmtNumber(null)).toBe('—')
    expect(fmtNumber(Number.NaN)).toBe('—')
  })

  it('falls back to English for a language we do not ship', async () => {
    const english = fmtDate(iso)
    await setLanguage('de-DE')
    expect(fmtDate(iso)).toBe(english)
  })
})
