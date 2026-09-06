// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest'
import { themeAdoptAction } from './theme'

describe('themeAdoptAction', () => {
  it('does nothing when the device and the account already agree', () => {
    expect(themeAdoptAction('dark', 'dark', false)).toEqual({ kind: 'none' })
  })

  // The regression: a device set to dark must not be reverted by a profile that
  // still reads "system" — which is what a stale cached /me hands back.
  it('takes the account value when it genuinely differs', () => {
    expect(themeAdoptAction('dark', 'light', false)).toEqual({ kind: 'take', mode: 'light' })
  })

  it('pushes the device value up when the account has none stored', () => {
    expect(themeAdoptAction('dark', undefined, false)).toEqual({ kind: 'push', mode: 'dark' })
  })

  it('leaves a default device with no account value alone', () => {
    expect(themeAdoptAction('system', undefined, false)).toEqual({ kind: 'none' })
  })

  // Picking a theme on the sign-in screen is the most recent intent, so signing in
  // must save it rather than revert to the account's untouched "system" default.
  // The same holds for a pick whose save failed: it is retried, not discarded.
  it('keeps and saves a pick the account has not accepted', () => {
    expect(themeAdoptAction('dark', 'system', true)).toEqual({ kind: 'push', mode: 'dark' })
    expect(themeAdoptAction('light', 'dark', true)).toEqual({ kind: 'push', mode: 'light' })
    expect(themeAdoptAction('system', undefined, true)).toEqual({ kind: 'push', mode: 'system' })
  })
})
