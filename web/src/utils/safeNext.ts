// safeNext returns raw when it is a path on this site ("/apps?workspace=prod"), else "": a
// post-login destination must never send the browser to another origin. Mirrors the server's
// handlers.SafeNext.
export function safeNext(raw: unknown): string {
  if (typeof raw !== 'string') return ''
  const s = raw.trim()
  if (!s || s.length > 2048 || s[0] !== '/' || s.startsWith('//') || /[\\\r\n\t]/.test(s)) return ''
  try {
    if (new URL(s, window.location.origin).origin !== window.location.origin) return ''
  } catch {
    return ''
  }
  return s
}
