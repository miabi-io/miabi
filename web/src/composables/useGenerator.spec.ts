import { describe, expect, it } from 'vitest'
import {
  DIGITS,
  LOWER,
  SYMBOLS,
  UPPER,
  generatePassphrase,
  generatePassword,
  generateToken,
  normalizePasswordOptions,
  passphraseEntropyBits,
  passwordCharset,
  passwordEntropyBits,
  randomInt,
  shuffle,
  strengthLabel,
  tokenEntropyBits,
  type PasswordOptions,
} from './useGenerator'

const base: PasswordOptions = {
  length: 20,
  upper: true,
  lower: true,
  digits: true,
  symbols: true,
  minDigits: 0,
  minSymbols: 0,
}

const count = (s: string, set: string) => [...s].filter((c) => set.includes(c)).length


const BUCKETS = 100
const DRAWS = 200_000
const CHI2_999 = 148

function chiSquare(draw: (max: number) => number, buckets: number, draws: number): number {
  const seen = new Array<number>(buckets).fill(0)
  for (let i = 0; i < draws; i++) seen[draw(buckets)]++
  const expected = draws / buckets
  return seen.reduce((acc, o) => acc + (o - expected) ** 2 / expected, 0)
}

describe('randomInt', () => {
  it('stays in range', () => {
    for (let i = 0; i < 2000; i++) {
      const v = randomInt(7)
      expect(v).toBeGreaterThanOrEqual(0)
      expect(v).toBeLessThan(7)
    }
  })

  it('rejects a non-positive bound', () => {
    expect(() => randomInt(0)).toThrow()
    expect(() => randomInt(-1)).toThrow()
  })


  it('draws uniformly (chi-square over a large sample)', () => {
    expect(chiSquare((max) => randomInt(max), BUCKETS, DRAWS)).toBeLessThan(CHI2_999)
  })

  it('never starves a value', () => {
    const buckets = 20
    const seen = new Array<number>(buckets).fill(0)
    for (let i = 0; i < 20_000; i++) seen[randomInt(buckets)]++
    expect(Math.min(...seen)).toBeGreaterThan(0)
  })


  it('would reject the naive byte-modulo implementation', () => {
    const byteModulo = (max: number) => {
      const b = new Uint8Array(1)
      crypto.getRandomValues(b)
      return b[0] % max
    }
    expect(chiSquare(byteModulo, BUCKETS, DRAWS)).toBeGreaterThan(CHI2_999)
  })
})

describe('shuffle', () => {
  it('preserves the multiset', () => {
    const input = [1, 2, 3, 4, 5, 5, 5]
    expect([...shuffle(input)].sort()).toEqual([...input].sort())
  })

  it('does not mutate its input', () => {
    const input = [1, 2, 3, 4, 5]
    shuffle(input)
    expect(input).toEqual([1, 2, 3, 4, 5])
  })

  
  it('moves elements away from their starting index', () => {
    const n = 10
    const stayed = new Array<number>(n).fill(0)
    const rounds = 5000
    for (let r = 0; r < rounds; r++) {
      const out = shuffle([...Array(n).keys()])
      out.forEach((v, i) => {
        if (v === i) stayed[i]++
      })
    }
    // Each index should hold its original element about 1/n of the time.
    for (const s of stayed) expect(Math.abs(s / rounds - 1 / n)).toBeLessThan(0.03)
  })
})

describe('generatePassword', () => {
  it('honours the requested length', () => {
    for (const length of [5, 8, 20, 64, 128]) {
      expect(generatePassword({ ...base, length })).toHaveLength(length)
    }
  })

  it('clamps a length outside the supported range', () => {
    expect(generatePassword({ ...base, length: 1 })).toHaveLength(5)
    expect(generatePassword({ ...base, length: 9999 })).toHaveLength(128)
  })

  it('uses only the enabled character classes', () => {
    const opts = { ...base, upper: true, lower: false, digits: false, symbols: false }
    for (let i = 0; i < 200; i++) {
      expect([...generatePassword(opts)].every((c) => UPPER.includes(c))).toBe(true)
    }
  })

  it('excludes the classes that are off', () => {
    const opts = { ...base, symbols: false, length: 60 }
    for (let i = 0; i < 100; i++) {
      expect(count(generatePassword(opts), SYMBOLS)).toBe(0)
    }
  })

  it('meets the minimum digit and symbol counts every time', () => {
    const opts = { ...base, length: 12, minDigits: 3, minSymbols: 2 }
    for (let i = 0; i < 500; i++) {
      const pw = generatePassword(opts)
      expect(count(pw, DIGITS)).toBeGreaterThanOrEqual(3)
      expect(count(pw, SYMBOLS)).toBeGreaterThanOrEqual(2)
    }
  })


  it('spreads required characters across the whole string', () => {
    const length = 16
    const opts = { ...base, length, minDigits: 1, minSymbols: 0, digits: true, symbols: false, upper: false, lower: true }
    const positions = new Array<number>(length).fill(0)
    const rounds = 4000
    for (let r = 0; r < rounds; r++) {
      const pw = generatePassword(opts)
      ;[...pw].forEach((c, i) => {
        if (DIGITS.includes(c)) positions[i]++
      })
    }
    // Every position should see a digit at a comparable rate; a clustered
    // implementation puts them all at index 0.
    const avg = positions.reduce((a, b) => a + b, 0) / length
    for (const p of positions) expect(Math.abs(p - avg) / avg).toBeLessThan(0.35)
  })

  it('never returns an empty alphabet when every class is disabled', () => {
    const pw = generatePassword({ ...base, upper: false, lower: false, digits: false, symbols: false })
    expect(pw).toHaveLength(20)
    expect([...pw].every((c) => LOWER.includes(c))).toBe(true)
  })

  it('produces a different value each call', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 200; i++) seen.add(generatePassword(base))
    expect(seen.size).toBe(200)
  })
})

describe('normalizePasswordOptions', () => {
  it('drops minimums for disabled classes', () => {
    const o = normalizePasswordOptions({ ...base, symbols: false, minSymbols: 4 })
    expect(o.minSymbols).toBe(0)
  })

  it('trims minimums that exceed the length', () => {
    const o = normalizePasswordOptions({ ...base, length: 6, minDigits: 5, minSymbols: 5 })
    expect(o.minDigits + o.minSymbols).toBeLessThanOrEqual(6)
    // Digits are preferred over symbols when something has to give.
    expect(o.minDigits).toBe(5)
    expect(o.minSymbols).toBe(1)
  })

  it('falls back to lowercase when every class is off', () => {
    expect(passwordCharset(normalizePasswordOptions({ ...base, upper: false, lower: false, digits: false, symbols: false }))).toBe(LOWER)
  })
})

describe('generatePassphrase', () => {
  const list = ['alpha', 'bravo', 'charlie', 'delta', 'echo', 'foxtrot']

  it('emits the requested number of words', () => {
    const p = generatePassphrase({ words: 5, separator: '-', capitalize: false, includeNumber: false }, list)
    expect(p.split('-')).toHaveLength(5)
  })

  it('draws only from the wordlist', () => {
    const p = generatePassphrase({ words: 6, separator: ' ', capitalize: false, includeNumber: false }, list)
    for (const w of p.split(' ')) expect(list).toContain(w)
  })

  it('capitalizes each word when asked', () => {
    const p = generatePassphrase({ words: 4, separator: '.', capitalize: true, includeNumber: false }, list)
    for (const w of p.split('.')) expect(w[0]).toBe(w[0].toUpperCase())
  })

  it('appends exactly one digit when asked', () => {
    for (let i = 0; i < 200; i++) {
      const p = generatePassphrase({ words: 4, separator: '-', capitalize: false, includeNumber: true }, list)
      expect(count(p, DIGITS)).toBe(1)
    }
  })

  // The digit must not always land on the last word, or it is trivially strippable.
  it('varies which word carries the digit', () => {
    const carriers = new Set<number>()
    for (let i = 0; i < 300; i++) {
      const words = generatePassphrase({ words: 4, separator: '-', capitalize: false, includeNumber: true }, list).split('-')
      words.forEach((w, idx) => {
        if (/\d$/.test(w)) carriers.add(idx)
      })
    }
    expect(carriers.size).toBe(4)
  })

  it('rejects an empty wordlist', () => {
    expect(() => generatePassphrase({ words: 4, separator: '-', capitalize: false, includeNumber: false }, [])).toThrow()
  })
})

describe('generateToken', () => {
  it('encodes hex at two characters per byte', () => {
    expect(generateToken({ bytes: 32, encoding: 'hex' })).toMatch(/^[0-9a-f]{64}$/)
    expect(generateToken({ bytes: 16, encoding: 'hex' })).toHaveLength(32)
  })

  it('encodes base64url without padding or unsafe characters', () => {
    for (let i = 0; i < 100; i++) {
      expect(generateToken({ bytes: 32, encoding: 'base64url' })).toMatch(/^[A-Za-z0-9_-]+$/)
    }
  })

  it('clamps the byte count', () => {
    expect(generateToken({ bytes: 1, encoding: 'hex' })).toHaveLength(16) // 8 bytes
    expect(generateToken({ bytes: 999, encoding: 'hex' })).toHaveLength(128) // 64 bytes
  })
})

describe('entropy', () => {
  it('computes log2(charset^length) for a password', () => {
    const opts = { ...base, upper: false, digits: false, symbols: false, length: 20 }
    expect(passwordEntropyBits(opts)).toBeCloseTo(20 * Math.log2(26), 6)
  })

  it('grows with the alphabet and the length', () => {
    const short = passwordEntropyBits({ ...base, length: 10 })
    const long = passwordEntropyBits({ ...base, length: 20 })
    const narrow = passwordEntropyBits({ ...base, length: 20, symbols: false })
    expect(long).toBeGreaterThan(short)
    expect(long).toBeGreaterThan(narrow)
  })

  it('computes log2(wordlist^words) for a passphrase', () => {
    const bits = passphraseEntropyBits({ words: 6, separator: '-', capitalize: false, includeNumber: false }, 7776)
    expect(bits).toBeCloseTo(6 * Math.log2(7776), 6)
    expect(Math.log2(7776)).toBeCloseTo(12.925, 3)
  })

  it('adds the appended digit to a passphrase', () => {
    const without = passphraseEntropyBits({ words: 5, separator: '-', capitalize: false, includeNumber: false }, 7776)
    const with_ = passphraseEntropyBits({ words: 5, separator: '-', capitalize: false, includeNumber: true }, 7776)
    expect(with_ - without).toBeCloseTo(Math.log2(10), 6)
  })

  it('reports a token as eight bits per byte', () => {
    expect(tokenEntropyBits({ bytes: 32, encoding: 'hex' })).toBe(256)
  })

  it('labels bit counts in ascending order', () => {
    expect(strengthLabel(30).tone).toBe('weak')
    expect(strengthLabel(60).tone).toBe('fair')
    expect(strengthLabel(90).tone).toBe('good')
    expect(strengthLabel(256).tone).toBe('strong')
  })
})
