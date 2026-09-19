// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest'
import en from './en.json'
import fr from './fr.json'
import { LANGUAGES, DEFAULT_LANGUAGE } from './languages'

// A missing key renders English, and an orphaned one never renders at all. Neither
// breaks a build, which is why they need a test.
const catalogues: Record<string, unknown> = { en, fr }

function keysOf(obj: unknown, prefix = ''): string[] {
  if (obj === null || typeof obj !== 'object') return [prefix]
  return Object.entries(obj as Record<string, unknown>).flatMap(([k, v]) =>
    keysOf(v, prefix ? `${prefix}.${k}` : k),
  )
}

describe('translation catalogues', () => {
  const reference = keysOf(en).sort()

  it('ships a catalogue for every language in the picker', () => {
    for (const l of LANGUAGES) {
      expect(catalogues[l.code], `no catalogue for ${l.code} — the picker offers it`).toBeTruthy()
    }
  })

  for (const l of LANGUAGES.filter((x) => x.code !== DEFAULT_LANGUAGE)) {
    describe(l.code, () => {
      const keys = keysOf(catalogues[l.code]).sort()

      it('translates every reference key', () => {
        expect(reference.filter((k) => !keys.includes(k))).toEqual([])
      })

      it('carries no key the reference has dropped', () => {
        expect(keys.filter((k) => !reference.includes(k))).toEqual([])
      })
    })
  }

  it('has no empty string, which renders as a blank label', () => {
    for (const [code, cat] of Object.entries(catalogues)) {
      const empties = keysOf(cat).filter((k) => {
        const v = k.split('.').reduce<unknown>((o, part) => (o as Record<string, unknown>)?.[part], cat)
        return typeof v === 'string' && v.trim() === ''
      })
      expect(empties, `empty values in ${code}`).toEqual([])
    }
  })
})
