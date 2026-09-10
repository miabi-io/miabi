// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AccentCode } from '@/api/types'
import data from './accents.json'

export interface Accent {
  code: AccentCode
  label: string
  /** The mid-tone, for the swatch in the picker. */
  swatch: string
}

// Read from the same JSON the stylesheets are generated from, so the picker cannot
// offer an accent that has no CSS and cannot miss one that does.
export const ACCENTS: Accent[] = data.accents.map((a) => ({
  code: a.code as AccentCode,
  label: a.label,
  swatch: a.ramp['500'],
}))
