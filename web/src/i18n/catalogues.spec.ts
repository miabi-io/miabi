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
import { createI18n } from 'vue-i18n'
import en from './en.json'
import fr from './fr.json'
import { LANGUAGES, DEFAULT_LANGUAGE } from './languages'

// Missing or orphaned keys never break a build, which is why they need a test.
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

  // A translation that drops {name} renders "Delete ?", losing what is about to be destroyed.
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

  // The same sentence under two keys gets translated twice and drifts; shared text lives in a common namespace.
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

  // Values render via {{ }}, not v-html, so an entity shows as the literal "&quot;".
  it('has no HTML entity, which text interpolation would not decode', () => {
    for (const [code, cat] of Object.entries(catalogues)) {
      const offenders = keysOf(cat).filter((k) => {
        const v = k.split('.').reduce<unknown>((o, part) => (o as Record<string, unknown>)?.[part], cat)
        return typeof v === 'string' && /&(?:#\d+|[a-z]+);/.test(v)
      })
      expect(offenders, `HTML entities in ${code}`).toEqual([])
    }
  })

  // vue-i18n reserves '@', '|' and braces; a stray '@' once crashed the command palette at render.
  it('compiles every message', () => {
    const broken: string[] = []
    for (const [code, cat] of Object.entries(catalogues)) {
      const probe = createI18n({ legacy: false, locale: code, messages: { [code]: cat as never }, missingWarn: false, fallbackWarn: false })
      for (const key of keysOf(cat)) {
        try {
          probe.global.t(key)
        } catch (e) {
          broken.push(`${code}:${key} — ${(e as Error).message.split('\n')[0]}`)
        }
      }
    }
    expect(broken).toEqual([])
  })

  // A language without a catalogue would switch to itself and silently render English.
  it('ships a catalogue for every language the picker offers', () => {
    const shipped = Object.keys(catalogues)
    expect(LANGUAGES.map((l) => l.code).sort()).toEqual(shipped.sort())
  })

  // Parity says nothing about keys absent from both catalogues, so this walks the source instead.
  it('defines every key the app asks for', () => {
    // Vite reads the sources, so this needs no node types in the app's tsconfig.
    const sources = import.meta.glob('../**/*.{vue,ts}', {
      eager: true,
      query: '?raw',
      import: 'default',
    }) as Record<string, string>

    // Only literal dotted keys are checkable; a computed key (t(someVar)) stays the author's responsibility.
    const used = new Map<string, string>()
    for (const [file, src] of Object.entries(sources)) {
      if (file.endsWith('.spec.ts')) continue
      for (const m of src.matchAll(/\$?\bt\(\s*(['"])([a-zA-Z0-9_]+(?:\.[a-zA-Z0-9_]+)+)\1/g)) {
        if (!used.has(m[2])) used.set(m[2], file.replace(/^\.\.\//, ''))
      }
    }
    expect(Object.keys(sources).length, 'no sources were scanned').toBeGreaterThan(50)

    const missing = [...used]
      .filter(([key]) => typeof valueAt(en, key) !== 'string')
      .map(([key, file]) => `${key} (${file})`)
      .sort()
    expect(missing, 'used in the app but absent from en.json').toEqual([])
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
