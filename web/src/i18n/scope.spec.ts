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

const sources = import.meta.glob('../**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// A template calling t() without useI18n() type-checks, then throws when it opens; $t is global.
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
