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

  // Every destination the old flat "Platform Admin" section offered must still be
  // reachable. Pinned as a set rather than a count: adding a page is normal,
  // losing one in a regrouping is the regression.
  const ORIGINAL_ADMIN_PATHS = [
    '/admin/dashboard', '/admin/users', '/admin/workspaces', '/admin/domains', '/admin/routes',
    '/admin/nodes', '/admin/runners', '/admin/events', '/admin/jobs', '/admin/oauth',
    '/admin/ldap', '/admin/plans', '/admin/license', '/admin/siem', '/admin/announcements',
    '/admin/platform-backup', '/admin/registry', '/admin/settings', '/admin/deployment-config',
  ]

  it('keeps every destination the flat section used to offer', () => {
    const paths = adminNavSections.flatMap((s) => s.items.map((i) => i.path))
    expect(new Set(paths).size).toBe(paths.length)
    for (const p of ORIGINAL_ADMIN_PATHS) expect(paths).toContain(p)
  })

  // The point of the split was that nineteen items in one list is a scroll, not a
  // menu. What this guards is a slide back toward that — one section swallowing the
  // console — not the exact size of any section, which grows as pages are added.
  it('keeps sections short enough to scan', () => {
    expect(adminNavSections.length).toBeGreaterThanOrEqual(5)
    const total = adminNavSections.reduce((n, s) => n + s.items.length, 0)
    for (const s of adminNavSections) {
      expect(s.items.length, `section "${s.title}" holds too much of the console`)
        .toBeLessThanOrEqual(Math.ceil(total / 2))
      expect(s.items.length, `section "${s.title}" has grown past a scannable list`)
        .toBeLessThanOrEqual(8)
    }
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
