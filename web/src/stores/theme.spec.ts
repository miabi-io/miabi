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

import { describe, expect, it } from 'vitest'
import { themeAdoptAction } from './theme'

describe('themeAdoptAction', () => {
  it('does nothing when the device and the account already agree', () => {
    expect(themeAdoptAction('dark', 'dark', false)).toEqual({ kind: 'none' })
  })

  // A device set to dark must not be reverted by a stale cached /me still reading "system".
  it('takes the account value when it genuinely differs', () => {
    expect(themeAdoptAction('dark', 'light', false)).toEqual({ kind: 'take', mode: 'light' })
  })

  it('pushes the device value up when the account has none stored', () => {
    expect(themeAdoptAction('dark', undefined, false)).toEqual({ kind: 'push', mode: 'dark' })
  })

  it('leaves a default device with no account value alone', () => {
    expect(themeAdoptAction('system', undefined, false)).toEqual({ kind: 'none' })
  })

  // A sign-in-screen pick or a failed save is the most recent intent, so it is pushed, not reverted.
  it('keeps and saves a pick the account has not accepted', () => {
    expect(themeAdoptAction('dark', 'system', true)).toEqual({ kind: 'push', mode: 'dark' })
    expect(themeAdoptAction('light', 'dark', true)).toEqual({ kind: 'push', mode: 'light' })
    expect(themeAdoptAction('system', undefined, true)).toEqual({ kind: 'push', mode: 'system' })
  })
})
