// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LanguageCode } from './languages'

// Named Intl formats behind vue-i18n's $d and $n. Months are text, not digits: 9/10 is
// the ninth of October to half the world and the tenth of September to the other half.
const datetime = {
  date: { year: 'numeric', month: 'short', day: 'numeric' },
  datetime: {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  },
  time: { hour: '2-digit', minute: '2-digit' },
  long: {
    year: 'numeric', month: 'long', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  },
} as const

const number = {
  decimal: { style: 'decimal' },
  integer: { style: 'decimal', maximumFractionDigits: 0 },
  percent: { style: 'percent', maximumFractionDigits: 1 },
} as const

export const datetimeFormats = {
  en: datetime,
  fr: datetime,
} satisfies Record<LanguageCode, typeof datetime>

export const numberFormats = {
  en: number,
  fr: number,
} satisfies Record<LanguageCode, typeof number>
