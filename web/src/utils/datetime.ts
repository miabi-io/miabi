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

import { activeLanguage } from '@/i18n'

// A bare toLocaleDateString() uses the browser's language, not the chosen one; views should prefer $d/$n.

const EM_DASH = '—'

function parse(iso?: string | null): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

/** A calendar date: "9 Sept 2026". Empty or unparseable input renders an em dash. */
export function fmtDate(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleDateString(activeLanguage(), { year: 'numeric', month: 'short', day: 'numeric' })
}

/** A date with clock time. */
export function fmtDateTime(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleString(activeLanguage(), {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}

/** Clock time alone. */
export function fmtTime(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleTimeString(activeLanguage(), { hour: '2-digit', minute: '2-digit' })
}

/** A grouped number: "1,024" in English, "1 024" in French. */
export function fmtNumber(n?: number | null, options?: Intl.NumberFormatOptions): string {
  if (n === null || n === undefined || Number.isNaN(n)) return EM_DASH
  return n.toLocaleString(activeLanguage(), options)
}
