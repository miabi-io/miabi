import { describe, expect, it } from 'vitest'
import { timezoneOptions } from './timezones'

describe('timezoneOptions', () => {
  it('lists UTC first, then every zone with its offset', () => {
    const opts = timezoneOptions()
    expect(opts[0]).toEqual({ value: 'UTC', label: 'UTC' })
    const kinshasa = opts.find((o) => o.value === 'Africa/Kinshasa')
    expect(kinshasa?.label).toMatch(/^Africa\/Kinshasa \(UTC[+-]\d\d:\d\d\)$/)
    expect(opts.length).toBeGreaterThan(300)
  })

  it('keeps a saved zone the browser does not list, so opening the form never changes it', () => {
    const opts = timezoneOptions('Asia/Calcutta')
    expect(opts.some((o) => o.value === 'Asia/Calcutta')).toBe(true)
  })
})
