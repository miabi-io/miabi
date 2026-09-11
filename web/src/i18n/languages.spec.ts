// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest'
import { resolveLanguage } from './languages'

describe('resolveLanguage', () => {
  it.each([
    ['en', 'en'],
    ['fr', 'fr'],
    ['FR', 'fr'],
    ['fr-CA', 'fr'],
    ['en_US', 'en'],
    ['de', 'en'],
    ['-fr', 'en'],
    ['', 'en'],
    [null, 'en'],
    [undefined, 'en'],
  ])('%s resolves to %s', (tag, want) => {
    expect(resolveLanguage(tag)).toBe(want)
  })
})
