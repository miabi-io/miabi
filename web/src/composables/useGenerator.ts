// Secret generation: passwords, passphrases and tokens, entirely in the browser.
//
// Nothing here talks to the API on purpose. A value the user discards should
// never have crossed the network, entered an audit log, or sat in the server's
// memory — and a client-side generator has nothing to rate-limit and keeps
// working when the API does not.

/** Character classes a password may draw from. */
export const UPPER = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'
export const LOWER = 'abcdefghijklmnopqrstuvwxyz'
export const DIGITS = '0123456789'
// Deliberately excludes quotes, backslash and backtick: those are what break a
// value when it is pasted into a shell, a YAML file or a connection string, and
// dropping them costs about 0.2 bits per character.
export const SYMBOLS = '!#$%&()*+,-./:;<=>?@[]^_{|}~'

export interface PasswordOptions {
  length: number
  upper: boolean
  lower: boolean
  digits: boolean
  symbols: boolean
  /** Guarantee at least this many digits appear. */
  minDigits: number
  /** Guarantee at least this many symbols appear. */
  minSymbols: number
}

export interface PassphraseOptions {
  words: number
  separator: string
  capitalize: boolean
  /** Append a digit to one random word, for policies that demand a number. */
  includeNumber: boolean
}

export type TokenEncoding = 'hex' | 'base64url'

export interface TokenOptions {
  bytes: number
  encoding: TokenEncoding
}

export const PASSWORD_MIN_LENGTH = 5
export const PASSWORD_MAX_LENGTH = 128
export const PASSPHRASE_MIN_WORDS = 3
export const PASSPHRASE_MAX_WORDS = 12
export const TOKEN_MIN_BYTES = 8
export const TOKEN_MAX_BYTES = 64

export const DEFAULT_PASSWORD_OPTIONS: PasswordOptions = {
  length: 20,
  upper: true,
  lower: true,
  digits: true,
  symbols: true,
  minDigits: 1,
  minSymbols: 1,
}

export const DEFAULT_PASSPHRASE_OPTIONS: PassphraseOptions = {
  words: 6,
  separator: '-',
  capitalize: false,
  includeNumber: false,
}

export const DEFAULT_TOKEN_OPTIONS: TokenOptions = { bytes: 32, encoding: 'base64url' }

function clamp(n: number, lo: number, hi: number): number {
  if (!Number.isFinite(n)) return lo
  return Math.min(hi, Math.max(lo, Math.floor(n)))
}

/**
 * A uniformly-random integer in [0, max) via rejection sampling.
 *
 * The obvious `getRandomValues()[0] % max` is biased whenever max does not
 * divide 2^32: the first (2^32 mod max) values become measurably more likely,
 * which for a 26-letter alphabet means the early letters appear more often than
 * the late ones. Drawing again when the value falls in the ragged tail costs a
 * negligible number of extra draws and removes the bias entirely.
 */
export function randomInt(max: number): number {
  if (max <= 0) throw new RangeError('randomInt: max must be positive')
  if (max === 1) return 0
  // The largest multiple of max that fits in 2^32; anything at or above it is
  // the ragged tail and gets rejected.
  const limit = Math.floor(0x1_0000_0000 / max) * max
  const buf = new Uint32Array(1)
  for (;;) {
    crypto.getRandomValues(buf)
    if (buf[0] < limit) return buf[0] % max
  }
}

/** One uniformly-random character from a set. */
function randomChar(charset: string): string {
  return charset[randomInt(charset.length)]
}

/**
 * Fisher–Yates, using crypto randomness.
 *
 * This is how the minimum-count constraints are satisfied: place the required
 * characters, fill the rest, then shuffle. The tempting alternative —
 * regenerate until the constraint happens to hold — silently skews the
 * distribution toward the strings that satisfy it most easily, and the more
 * demanding the policy the worse the skew.
 */
export function shuffle<T>(items: T[]): T[] {
  const out = [...items]
  for (let i = out.length - 1; i > 0; i--) {
    const j = randomInt(i + 1)
    ;[out[i], out[j]] = [out[j], out[i]]
  }
  return out
}

/** The alphabet a set of password options draws from. */
export function passwordCharset(o: PasswordOptions): string {
  return (
    (o.upper ? UPPER : '') + (o.lower ? LOWER : '') + (o.digits ? DIGITS : '') + (o.symbols ? SYMBOLS : '')
  )
}

/**
 * Normalizes password options into something satisfiable.
 *
 * Two conflicts have to be resolved before generating rather than after: a
 * length shorter than the minimums it demands, and every character class turned
 * off. Both would otherwise produce a value that quietly disobeys the options.
 */
export function normalizePasswordOptions(input: PasswordOptions): PasswordOptions {
  const o = { ...input }
  o.length = clamp(o.length, PASSWORD_MIN_LENGTH, PASSWORD_MAX_LENGTH)

  // A password of nothing is not a password: fall back to lowercase.
  if (!o.upper && !o.lower && !o.digits && !o.symbols) o.lower = true

  // A minimum for a class that is switched off is not a constraint.
  o.minDigits = o.digits ? clamp(o.minDigits, 0, o.length) : 0
  o.minSymbols = o.symbols ? clamp(o.minSymbols, 0, o.length) : 0

  // The minimums cannot exceed the length. Trim symbols first: a policy is more
  // often "at least N digits" than "at least N symbols".
  const over = o.minDigits + o.minSymbols - o.length
  if (over > 0) {
    const takeFromSymbols = Math.min(o.minSymbols, over)
    o.minSymbols -= takeFromSymbols
    o.minDigits -= over - takeFromSymbols
  }
  return o
}

/** Generates a password obeying the given options. */
export function generatePassword(input: PasswordOptions): string {
  const o = normalizePasswordOptions(input)
  const charset = passwordCharset(o)

  const chars: string[] = []
  for (let i = 0; i < o.minDigits; i++) chars.push(randomChar(DIGITS))
  for (let i = 0; i < o.minSymbols; i++) chars.push(randomChar(SYMBOLS))
  while (chars.length < o.length) chars.push(randomChar(charset))

  return shuffle(chars).join('')
}

/** Generates a passphrase from a wordlist. */
export function generatePassphrase(input: PassphraseOptions, wordlist: readonly string[]): string {
  if (wordlist.length === 0) throw new Error('generatePassphrase: empty wordlist')
  const count = clamp(input.words, PASSPHRASE_MIN_WORDS, PASSPHRASE_MAX_WORDS)

  const words: string[] = []
  for (let i = 0; i < count; i++) {
    let w = wordlist[randomInt(wordlist.length)]
    if (input.capitalize) w = w.charAt(0).toUpperCase() + w.slice(1)
    words.push(w)
  }
  // The digit goes on one random word rather than always the last, so it does
  // not become a predictable position an attacker can strip.
  if (input.includeNumber) {
    const at = randomInt(words.length)
    words[at] += String(randomInt(10))
  }
  return words.join(input.separator)
}

/** Generates a random token of N bytes, hex- or base64url-encoded. */
export function generateToken(input: TokenOptions): string {
  const n = clamp(input.bytes, TOKEN_MIN_BYTES, TOKEN_MAX_BYTES)
  const bytes = new Uint8Array(n)
  crypto.getRandomValues(bytes)

  if (input.encoding === 'hex') {
    return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  }
  // base64url: URL- and header-safe, and unpadded so it survives being pasted
  // into a query string or an env file without escaping.
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// --- Entropy -------------------------------------------------------------
//
// Reported in bits, as log2(possibilities). A number is honest in a way a
// green "strong" bar is not, and it is the only figure that lets someone
// compare a passphrase with a password.
//
// These measure the GENERATOR, which is the correct thing to measure: how many
// equally-likely values it could have produced. The minimum-count constraints
// technically shrink that space slightly — they are ignored here rather than
// overstated, and the difference is well under a bit at any usable length.

export function passwordEntropyBits(o: PasswordOptions): number {
  const n = normalizePasswordOptions(o)
  const size = passwordCharset(n).length
  if (size <= 1) return 0
  return n.length * Math.log2(size)
}

export function passphraseEntropyBits(o: PassphraseOptions, wordlistSize: number): number {
  if (wordlistSize <= 1) return 0
  const count = clamp(o.words, PASSPHRASE_MIN_WORDS, PASSPHRASE_MAX_WORDS)
  // The appended digit adds log2(10) bits of value and log2(count) bits of
  // position — but an attacker who knows the scheme knows both, so only the
  // digit itself is counted.
  return count * Math.log2(wordlistSize) + (o.includeNumber ? Math.log2(10) : 0)
}

export function tokenEntropyBits(o: TokenOptions): number {
  return clamp(o.bytes, TOKEN_MIN_BYTES, TOKEN_MAX_BYTES) * 8
}


export function strengthLabel(bits: number): { label: string; tone: 'weak' | 'fair' | 'good' | 'strong' } {
  if (bits < 45) return { label: 'Weak', tone: 'weak' }
  if (bits < 70) return { label: 'Fair', tone: 'fair' }
  if (bits < 110) return { label: 'Good', tone: 'good' }
  return { label: 'Strong', tone: 'strong' }
}

/** Lazily fetches the passphrase wordlist, so it is not in the main bundle. */
let wordlistPromise: Promise<readonly string[]> | null = null
export function loadWordlist(): Promise<readonly string[]> {
  if (!wordlistPromise) {
    wordlistPromise = import('../data/effLongWordlist').then((m) => m.EFF_LONG_WORDLIST)
  }
  return wordlistPromise
}
