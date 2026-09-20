// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest'
import en from './en.json'
import fr from './fr.json'
import { LANGUAGES, DEFAULT_LANGUAGE } from './languages'

// A missing key renders English, and an orphaned one never renders at all. Neither
// breaks a build, which is why they need a test.
const catalogues: Record<string, unknown> = { en, fr }

function valueAt(cat: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((o, p) => (o as Record<string, unknown>)?.[p], cat)
}

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

  // A translation that drops {name} renders "Delete ?" — fluent, and missing the one
  // detail that tells the user what they are about to destroy.
  it('keeps every interpolation the reference declares', () => {
    const placeholders = (s: string) => new Set(s.match(/\{\w+\}/g) ?? [])
    const mismatched: string[] = []
    for (const l of LANGUAGES.filter((x) => x.code !== DEFAULT_LANGUAGE)) {
      for (const k of reference) {
        const a = placeholders(String(valueAt(en, k) ?? ''))
        const b = placeholders(String(valueAt(catalogues[l.code], k) ?? ''))
        if (a.size !== b.size || [...a].some((x) => !b.has(x))) mismatched.push(`${l.code}:${k}`)
      }
    }
    expect(mismatched).toEqual([])
  })

  // The same sentence under two keys gets translated twice and drifts. Shared text lives
  // under a common namespace instead; this catches the next copy before it is translated.
  it('does not repeat a string across feature namespaces', () => {
    const seen = new Map<string, string>()
    const dupes: string[] = []
    for (const k of reference) {
      const v = String(valueAt(en, k) ?? '')
      // Short labels legitimately repeat: "Delete" is a title, a button and a menu item.
      if (v.length < 25) continue
      const first = seen.get(v)
      if (first && first.split('.')[1] !== k.split('.')[1]) dupes.push(`${first} / ${k}`)
      else if (!first) seen.set(v, k)
    }
    expect(dupes).toEqual([])
  })

  // Catalogue values reach the page through {{ }}, not v-html, so an entity carried over
  // from the original template renders as the literal "&quot;" rather than a quote.
  it('has no HTML entity, which text interpolation would not decode', () => {
    for (const [code, cat] of Object.entries(catalogues)) {
      const offenders = keysOf(cat).filter((k) => {
        const v = k.split('.').reduce<unknown>((o, part) => (o as Record<string, unknown>)?.[part], cat)
        return typeof v === 'string' && /&(?:#\d+|[a-z]+);/.test(v)
      })
      expect(offenders, `HTML entities in ${code}`).toEqual([])
    }
  })

  // The sign-in picker offers every entry in LANGUAGES. One without a catalogue file
  // would switch to itself and render English, with nothing to say why.
  it('ships a catalogue for every language the picker offers', () => {
    const shipped = Object.keys(catalogues)
    expect(LANGUAGES.map((l) => l.code).sort()).toEqual(shipped.sort())
  })

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
