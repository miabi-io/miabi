import { describe, it, expect, beforeEach, vi } from 'vitest'
import { navSections } from './nav'
import { adminNavSections } from './adminNav'
import { rememberConsoleRoute, workspaceReturnPath, adminReturnPath, ADMIN_HOME, isAdminPath } from './console'

describe('console separation', () => {
  // The two consoles have separate shells, so a destination listed in the wrong
  // menu navigates out of the shell the user is looking at.
  it('keeps admin destinations out of the workspace navigation', () => {
    const stray = navSections.flatMap((s) => s.items).filter((i) => isAdminPath(i.path))
    expect(stray).toEqual([])
  })

  it('lists only admin destinations in the admin navigation', () => {
    const stray = adminNavSections.flatMap((s) => s.items).filter((i) => !isAdminPath(i.path))
    expect(stray).toEqual([])
  })

  // Every route the old flat "Platform Admin" section offered must still be
  // reachable — regrouping is not an excuse to lose one.
  it('still offers all nineteen admin destinations', () => {
    const paths = adminNavSections.flatMap((s) => s.items.map((i) => i.path))
    expect(new Set(paths).size).toBe(paths.length)
    expect(paths).toHaveLength(19)
    for (const p of ['/admin/metrics', '/admin/users', '/admin/nodes', '/admin/license', '/admin/settings']) {
      expect(paths).toContain(p)
    }
  })

  it('groups the admin console rather than listing it flat', () => {
    expect(adminNavSections.length).toBeGreaterThanOrEqual(5)
    for (const s of adminNavSections) expect(s.items.length).toBeLessThanOrEqual(5)
  })
})

describe('return path', () => {
  // The suite runs in node, where there is no localStorage; the module is written
  // to tolerate its absence, so give it one to exercise the stored path.
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
      clear: () => store.clear(),
    })
  })

  it('returns to where the user was in the workspace console', () => {
    rememberConsoleRoute('workspace', '/secrets')
    expect(workspaceReturnPath()).toBe('/secrets')
  })

  it('falls back to the dashboard with nothing stored', () => {
    expect(workspaceReturnPath()).toBe('/')
  })

  // A stored admin path would send "back to the workspace" straight into the
  // console the user just asked to leave.
  it('refuses an admin path as the way out of the admin console', () => {
    rememberConsoleRoute('workspace', '/admin/nodes')
    expect(workspaceReturnPath()).toBe('/')
  })

  it('refuses a path that is not one', () => {
    rememberConsoleRoute('workspace', 'https://evil.example/x')
    expect(workspaceReturnPath()).toBe('/')
  })

  it('returns to the last admin page, or the admin home', () => {
    expect(adminReturnPath()).toBe(ADMIN_HOME)
    rememberConsoleRoute('admin', '/admin/users')
    expect(adminReturnPath()).toBe('/admin/users')
    rememberConsoleRoute('admin', '/secrets')
    expect(adminReturnPath()).toBe(ADMIN_HOME)
  })
})

// The helpers run in a browser that may refuse storage entirely (private mode,
// blocked site data). A thrown accessor must not take the console switch with it.
describe('without storage', () => {
  it('still resolves both return paths', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
    })
    expect(() => rememberConsoleRoute('workspace', '/secrets')).not.toThrow()
    expect(workspaceReturnPath()).toBe('/')
    expect(adminReturnPath()).toBe(ADMIN_HOME)
  })
})
