// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest'

const sources = import.meta.glob('../**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// A template calling t() without useI18n() in its script compiles and type-checks, then
// throws at runtime when that dialog opens. $t is injected globally and needs nothing.
describe('translation calls are in scope', () => {
  it('no template uses bare t() without useI18n()', () => {
    const offenders = Object.entries(sources)
      .filter(([, s]) => {
        const tpl = s.slice(s.indexOf('<template'))
        return /[^$.\w]t\(['"]/.test(tpl) && !s.includes('useI18n()')
      })
      .map(([f]) => f)
    expect(offenders).toEqual([])
  })
})
