import { describe, it, expect, beforeEach, vi } from 'vitest'
import { navSections } from './nav'
import { adminNavSections } from './adminNav'
import { rememberConsoleRoute, workspaceReturnPath, adminReturnPath, ADMIN_HOME, isAdminPath } from './console'

describe('console separation', () => {
  // The consoles have separate shells, so a misplaced destination navigates out of the current one.
  it('keeps admin destinations out of the workspace navigation', () => {
    const stray = navSections.flatMap((s) => s.items).filter((i) => isAdminPath(i.path))
    expect(stray).toEqual([])
  })

  it('lists only admin destinations in the admin navigation', () => {
    const stray = adminNavSections.flatMap((s) => s.items).filter((i) => !isAdminPath(i.path))
    expect(stray).toEqual([])
  })

  // Pinned as a set, not a count: adding a page is normal, losing one in a regrouping is the regression.
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

  // Guards against one section swallowing the console, not the exact size of any section.
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
  // The suite runs in node with no localStorage; provide one to exercise the stored path.
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

  // A stored admin path would send "back to the workspace" straight into the admin console.
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

// A thrown storage accessor (private mode, blocked data) must not break the console switch.
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
