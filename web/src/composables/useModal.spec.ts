// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest'
import { modalKeyAction } from './useModal'

// A plain dialog: no region claims Tab or Escape.
const plain = { escapable: true, inEscapeThrough: false, inTabThrough: false }
// A shell terminal: it owns both keys.
const shell = { escapable: true, inEscapeThrough: true, inTabThrough: true }

describe('modalKeyAction', () => {
  it('closes on Escape in an ordinary dialog', () => {
    expect(modalKeyAction({ key: 'Escape', shiftKey: false, ...plain })).toBe('close')
  })

  it('leaves Escape alone while the dialog is mid-action', () => {
    expect(
      modalKeyAction({ key: 'Escape', shiftKey: false, ...plain, escapable: false }),
    ).toBe('ignore')
  })


  it('gives Escape to content that owns it', () => {
    expect(modalKeyAction({ key: 'Escape', shiftKey: false, ...shell })).toBe('ignore')
  })

  it('gives Tab to content that owns it, so a shell can complete a path', () => {
    expect(modalKeyAction({ key: 'Tab', shiftKey: false, ...shell })).toBe('ignore')
  })


  it('always runs the focus trap on Shift+Tab, even inside that content', () => {
    expect(modalKeyAction({ key: 'Tab', shiftKey: true, ...shell })).toBe('trap-tab')
  })

  it('traps Tab in an ordinary dialog', () => {
    expect(modalKeyAction({ key: 'Tab', shiftKey: false, ...plain })).toBe('trap-tab')
    expect(modalKeyAction({ key: 'Tab', shiftKey: true, ...plain })).toBe('trap-tab')
  })

  it('ignores every other key', () => {
    for (const key of ['a', 'Enter', 'ArrowUp', 'Control', ':', 'w']) {
      expect(modalKeyAction({ key, shiftKey: false, ...shell })).toBe('ignore')
    }
  })
})
