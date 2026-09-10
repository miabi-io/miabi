// Console identity and the return path between the two of them.

export const ADMIN_HOME = '/admin'

type Console = 'workspace' | 'admin'

const RETURN_KEY: Record<Console, string> = {
  workspace: 'mb_return_workspace',
  admin: 'mb_return_admin',
}

// rememberConsoleRoute stores where the user was in a console so switching back
// lands there rather than on its dashboard. Per-device by design: this is "where
// was I", not a preference worth syncing.
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

// workspaceReturnPath is where "back to the workspace console" goes. A stored
// /admin path would bounce straight back, so it is refused rather than trusted.
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

// isAdminPath reports whether a route belongs to the admin console. The router is
// the authority on which shell renders; this answers the same question for code
// that only has a path.
export function isAdminPath(path: string): boolean {
  return path === '/admin' || path.startsWith('/admin/')
}
