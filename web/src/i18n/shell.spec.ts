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

import { describe, it, expect, afterEach } from 'vitest'
import en from './en.json'
import { setLanguage, label, t } from '@/i18n'
import { navSections } from '@/data/nav'
import { adminNavSections } from '@/data/adminNav'
import { statusTitle } from '@/api/client'

function has(key: string): boolean {
  return key.split('.').reduce<unknown>((o, p) => (o as Record<string, unknown>)?.[p], en) !== undefined
}

afterEach(async () => {
  await setLanguage('en')
})

describe('nav catalogue keys', () => {
  // The label() fallback hides a missing key in production, so only a test catches it.
  it('exist for every entry in both consoles', () => {
    const missing: string[] = []
    for (const s of [...navSections, ...adminNavSections]) {
      if (!has(s.key)) missing.push(s.key)
      for (const i of s.items) if (!has(i.key)) missing.push(i.key)
    }
    expect(missing).toEqual([])
  })

  it('are unique, so no two entries share a label', () => {
    const keys = [...navSections, ...adminNavSections].flatMap((s) => s.items.map((i) => i.key))
    expect(keys.length).toBe(new Set(keys).size)
  })
})

describe('the shell follows the active language', () => {
  it('translates a nav label', async () => {
    const item = navSections.flatMap((s) => s.items).find((i) => i.key === 'nav.overview.dashboard')!
    expect(label(item.key, item.name)).toBe('Dashboard')
    await setLanguage('fr')
    expect(label(item.key, item.name)).toBe('Tableau de bord')
  })

  it('translates status headings, which are ours rather than the server\'s', async () => {
    expect(statusTitle(402)).toBe('Upgrade required')
    await setLanguage('fr')
    expect(statusTitle(402)).toBe('Mise à niveau requise')
    expect(statusTitle(500)).toBe('Une erreur est survenue')
  })

  it('interpolates instead of concatenating', async () => {
    await setLanguage('fr')
    expect(t('switcher.backTo', { where: 'Acme' })).toBe('Retour à Acme')
  })

  it('falls back to the English text when a key is missing', () => {
    expect(label('nav.nothing.here', 'Fallback')).toBe('Fallback')
  })
})
