import { describe, expect, it } from 'vitest'
import { wirePath } from './flowWires'

describe('wirePath', () => {
  const a = { x: 0, y: 0, w: 100, h: 40 }
  const b = { x: 160, y: 100, w: 100, h: 60 }

  it('joins right edge to left edge side by side', () => {
    const w = wirePath(a, b, false)
    expect(w.d).toBe('M100 20 C129 20 129 130 158 130')
    expect(w.mid).toEqual({ x: 129, y: 75 })
  })

  it('joins bottom edge to top edge when stacked', () => {
    const w = wirePath(a, { x: 0, y: 100, w: 100, h: 40 }, true)
    expect(w.d).toBe('M50 40 C50 69 50 69 50 98')
    expect(w.mid).toEqual({ x: 50, y: 69 })
  })
})
