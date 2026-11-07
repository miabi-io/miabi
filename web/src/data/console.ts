// Console identity and the return path between the two of them.

export const ADMIN_HOME = '/admin'

type Console = 'workspace' | 'admin'

const RETURN_KEY: Record<Console, string> = {
  workspace: 'mb_return_workspace',
  admin: 'mb_return_admin',
}

// Per-device by design: "where was I" is not a preference worth syncing.
export function rememberConsoleRoute(console: Console, path: string) {
  try {
    localStorage.setItem(RETURN_KEY[console], path)
  } catch {
    // A browser refusing storage costs the user a landing spot, nothing more.
  }
}

function storedRoute(console: Console): string | null {
  try {
    return localStorage.getItem(RETURN_KEY[console])
  } catch {
    return null
  }
}

// A stored /admin path would bounce straight back, so it is refused.
export function workspaceReturnPath(): string {
  const stored = storedRoute('workspace')
  if (stored && stored.startsWith('/') && !stored.startsWith('/admin')) return stored
  return '/'
}

// adminReturnPath is the mirror, used when entering the admin console.
export function adminReturnPath(): string {
  const stored = storedRoute('admin')
  if (stored && stored.startsWith('/admin/')) return stored
  return ADMIN_HOME
}

// The router decides which shell renders; this answers the same for code that only has a path.
export function isAdminPath(path: string): boolean {
  return path === '/admin' || path.startsWith('/admin/')
}
