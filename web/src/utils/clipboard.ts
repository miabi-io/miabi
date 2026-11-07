// copyText never throws and falls back to execCommand where navigator.clipboard is undefined (plain-HTTP origins).
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // A secure-context write can still be rejected (permission, focus loss).
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-9999px'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// copyLoaded hands the clipboard a pending value so Safari keeps the click's user activation across the fetch.
// It rejects only when load does, so callers can report that error themselves.
export async function copyLoaded(load: () => Promise<string>): Promise<boolean> {
  const value = load()
  if (typeof ClipboardItem !== 'undefined' && navigator.clipboard?.write) {
    try {
      const blob = value.then((v) => new Blob([v], { type: 'text/plain' }))
      await navigator.clipboard.write([new ClipboardItem({ 'text/plain': blob })])
      return true
    } catch {
      // Older engines reject a promised ClipboardItem; fall back to a plain write.
    }
  }
  return copyText(await value)
}
