import { describe, it, expect } from 'vitest'
import { locationLabel } from './locations'

describe('locationLabel', () => {
  it('shows the display name with its location code', () => {
    expect(locationLabel({ name: 'eu-1', display_name: 'Frankfurt', location_code: 'eu-central' })).toBe('Frankfurt (eu-central)')
  })

  it('falls back to the handle when the cluster has no display name', () => {
    expect(locationLabel({ name: 'eu-1', display_name: '  ' })).toBe('eu-1')
  })
})
