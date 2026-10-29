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
import { createUpdateNotice, isChunkLoadError, shouldReloadForChunk } from './useUpdateNotice'

function setup(versions: string[]) {
  let t = 0
  const queue = [...versions]
  const notice = createUpdateNotice(async () => queue.shift() ?? versions[versions.length - 1], () => t)
  return { notice, advance: (ms: number) => { t += ms } }
}

describe('update notice', () => {
  it('flags a version change after the page loaded', async () => {
    const { notice, advance } = setup(['1.10.10', '1.10.10', '1.11.0'])
    await notice.check()
    advance(60_000)
    await notice.check()
    expect(notice.available.value).toBe(false)
    advance(60_000)
    await notice.check()
    expect(notice.available.value).toBe(true)
  })

  it('checks at most once a minute', async () => {
    const { notice, advance } = setup(['1.10.10', '1.11.0'])
    await notice.check()
    advance(1_000)
    await notice.check()
    expect(notice.available.value).toBe(false)
  })

  it('never flags a dev build', async () => {
    const { notice, advance } = setup(['dev', 'dev'])
    await notice.check()
    advance(60_000)
    await notice.check()
    expect(notice.available.value).toBe(false)
  })

  it('ignores a failed check', async () => {
    let t = 0
    let calls = 0
    const notice = createUpdateNotice(async () => {
      calls++
      if (calls === 2) throw new Error('restarting')
      return calls === 1 ? '1.10.10' : '1.10.10'
    }, () => t)
    await notice.check()
    t += 60_000
    await notice.check()
    t += 60_000
    await notice.check()
    expect(notice.available.value).toBe(false)
  })
})

describe('stale chunk handling', () => {
  it('recognises the browsers\' failed lazy-import messages', () => {
    expect(isChunkLoadError(new TypeError('Failed to fetch dynamically imported module: /assets/Apps-1a2b.js'))).toBe(true)
    expect(isChunkLoadError(new TypeError('Importing a module script failed.'))).toBe(true)
    expect(isChunkLoadError(new Error('error loading dynamically imported module'))).toBe(true)
    expect(isChunkLoadError(new Error('Request failed with status code 500'))).toBe(false)
  })

  it('reloads at most once a minute', () => {
    expect(shouldReloadForChunk(0, 1_000_000)).toBe(true)
    expect(shouldReloadForChunk(1_000_000 - 30_000, 1_000_000)).toBe(false)
    expect(shouldReloadForChunk(1_000_000 - 60_000, 1_000_000)).toBe(true)
  })
})
