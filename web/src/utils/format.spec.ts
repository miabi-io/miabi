import { describe, it, expect } from 'vitest'
import { fmtSize } from './format'

describe('fmtSize', () => {
  it('scales past KB instead of reporting everything in one unit', () => {
    expect(fmtSize(512)).toBe('512 B')
    expect(fmtSize(1536)).toBe('1.5 KB')
    expect(fmtSize(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(fmtSize(3 * 1024 * 1024 * 1024)).toBe('3.0 GB')
    expect(fmtSize(2 * 1024 ** 4)).toBe('2.0 TB')
  })

  it('drops the decimal once the number is large enough to carry the precision', () => {
    expect(fmtSize(42 * 1024 * 1024)).toBe('42 MB')
    expect(fmtSize(750 * 1024)).toBe('750 KB')
  })

  it('handles the values a container reports before it has moved any traffic', () => {
    expect(fmtSize(0)).toBe('0 B')
    expect(fmtSize(undefined)).toBe('0 B')
    expect(fmtSize(null)).toBe('0 B')
  })

  it('stops at the largest unit it knows', () => {
    expect(fmtSize(1024 ** 6)).toBe('1024 PB')
  })
})
