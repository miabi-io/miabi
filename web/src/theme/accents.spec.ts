// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest'
import data from './accents.json'
import { ACCENTS } from './accents'

// sRGB relative luminance, per WCAG 2.1.
function luminance(hex: string): number {
  const v = hex.replace('#', '')
  const channels = [0, 2, 4].map((i) => {
    const c = parseInt(v.slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

// The grounds the console actually paints: --bg-primary in each theme.
const LIGHT_BG = '#ffffff'
const DARK_BG = '#1a1a2e'
const ON_PRIMARY = '#ffffff' // --text-on-primary

describe('accent contrast', () => {
  // The pair that matters most: every primary button is white text on primary-600.
  // A theme that fails here ships an unreadable button, which no other test sees.
  it.each(data.accents)('$label: white text on primary-600 meets 4.5:1', (accent) => {
    expect(contrast(ON_PRIMARY, accent.ramp['600'])).toBeGreaterThanOrEqual(4.5)
  })

  // Focus rings and links use primary-500 on both grounds. 3:1 is the WCAG
  // threshold for a non-text indicator.
  it.each(data.accents)('$label: primary-500 is visible on both grounds', (accent) => {
    expect(contrast(accent.ramp['500'], LIGHT_BG)).toBeGreaterThanOrEqual(3)
    expect(contrast(accent.ramp['500'], DARK_BG)).toBeGreaterThanOrEqual(3)
  })
})

describe('accent selection', () => {
  // State is not decoration. An accent that reads as "danger" or "success" is a
  // hazard for anyone who takes colour before text, so accents must stay clear of
  // the semantic hues.
  // The three that demand action. --info (#0ea5e9) is deliberately absent: it
  // signals nothing to act on, and excluding every blue would rule out the accent
  // people most expect a console to offer.
  const SEMANTIC = { success: '#22c55e', warning: '#f59e0b', danger: '#ef4444' }

  function hue(hex: string): number {
    const v = hex.replace('#', '')
    const [r, g, b] = [0, 2, 4].map((i) => parseInt(v.slice(i, i + 2), 16) / 255)
    const max = Math.max(r, g, b)
    const min = Math.min(r, g, b)
    if (max === min) return -1 // achromatic: no hue to collide
    const d = max - min
    let h = 0
    if (max === r) h = ((g - b) / d) % 6
    else if (max === g) h = (b - r) / d + 2
    else h = (r - g) / d + 4
    return (h * 60 + 360) % 360
  }

  function hueDistance(a: number, b: number): number {
    const raw = (((a - b) % 360) + 360) % 360
    return Math.min(raw, 360 - raw)
  }

  // An accent may sit on a state hue — orange, green and red were asked for — but
  // it has to say so. The declaration is checked against reality rather than
  // trusted: it must name the state the accent is actually nearest, and an accent
  // that has moved away from every state must drop it. So this still catches the
  // case it exists for, which is a colliding accent added without noticing.
  it.each(data.accents)('$label: any state-hue overlap is declared, not accidental', (accent) => {
    const h = hue(accent.ramp['500'])
    if (h < 0) return // an achromatic accent reads as grey, not as a hue
    const declared = (accent as { nearState?: string }).nearState

    let nearest = ''
    let delta = 360
    for (const [name, hex] of Object.entries(SEMANTIC)) {
      const d = hueDistance(h, hue(hex))
      if (d < delta) [nearest, delta] = [name, d]
    }

    if (delta > 25) {
      expect(declared, `${accent.label} is clear of every state hue — remove nearState`).toBeUndefined()
      return
    }
    expect(
      declared,
      `${accent.label} sits ${delta.toFixed(0)}° from ${nearest}: declare "nearState": "${nearest}" if that is intended`,
    ).toBe(nearest)
  })

  // Not a limit for its own sake: every accent is a permanent contrast and
  // screenshot obligation on every future change. Eight is a decision, so raising
  // it should be one too rather than something that drifts.
  it('keeps the set to a reviewed size', () => {
    expect(data.accents.length).toBeLessThanOrEqual(6)
  })

  it('exposes every accent to the picker', () => {
    expect(ACCENTS.map((a) => a.code)).toEqual(data.accents.map((a) => a.code))
  })
})
