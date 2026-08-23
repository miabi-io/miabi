import { describe, expect, it } from 'vitest'
import { displayFor, resourceName, slugify } from './installNames'

// These cases mirror TestResourceNameIsInstallScoped in
// internal/services/marketplace/marketplace_test.go. If one side changes the
// rule, one of the two suites should fail.
describe('resourceName', () => {
  it('names resources after the install, not the template', () => {
    expect(resourceName('Smart', 'content')).toBe('smart-content')
    expect(resourceName('Smart', 'db')).toBe('smart-db')
    expect(resourceName('Shop', 'content')).toBe('shop-content')
  })

  it('leaves a default install with the names it always had', () => {
    // The install label defaults to the template's display name, so someone who
    // does not rename the install sees no change.
    expect(resourceName('WordPress', 'content')).toBe('wordpress-content')
    expect(resourceName('Goma Gateway', 'config')).toBe('goma-gateway-config')
  })

  it('produces a valid workspace name from an awkward label', () => {
    const valid = /^[a-z0-9][a-z0-9-]*$/
    for (const label of ['Smart Shop!', '  Édition  ', 'a/b\\c', 'UPPER CASE']) {
      const got = resourceName(label, 'content')
      expect(got, `${label} → ${got}`).toMatch(valid)
    }
    expect(resourceName('Smart Shop!', 'content')).toBe('smart-shop-content')
    expect(resourceName('  Édition  ', 'data')).toBe('dition-data')
  })

  it('falls back to the template-local name when the label slugifies to nothing', () => {
    expect(resourceName('!!!', 'content')).toBe('content')
  })
})

describe('displayFor', () => {
  it('gives a lone app or database the install name', () => {
    expect(displayFor('Smart', 'web', 1)).toBe('Smart')
  })

  it('suffixes each one when a template has several', () => {
    expect(displayFor('Smart', 'web', 2)).toBe('Smart web')
    expect(displayFor('Smart', 'worker', 2)).toBe('Smart worker')
  })
})

describe('slugify', () => {
  it('collapses runs of non-alphanumerics and trims the ends', () => {
    expect(slugify('  Hello -- World!! ')).toBe('hello-world')
  })

  it('returns the fallback when nothing survives', () => {
    expect(slugify('!!!', 'fallback')).toBe('fallback')
    expect(slugify('!!!')).toBe('')
  })
})
