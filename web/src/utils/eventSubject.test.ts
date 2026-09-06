import { describe, it, expect } from 'vitest'
import { eventSubjectLabel, eventSubjectLink } from './eventSubject'
import type { AppEvent } from '@/api/types'

const base = {
  id: 1, workspace_id: 1, type: 'container.oom', severity: 'error',
  message: '', created_at: '', application_id: 0,
} as unknown as AppEvent

const dbEvent = (over: Partial<AppEvent> = {}) =>
  ({ ...base, subject_type: 'database', database_id: 12, ...over }) as AppEvent
const appEvent = (over: Record<string, unknown> = {}) =>
  ({ ...base, subject_type: 'app', application_id: 5, ...over }) as AppEvent

describe('eventSubjectLabel', () => {
  // application_id is 0 on a non-app event, so the app branch would render "app #0".
  it('names a database event without falling back to app #0', () => {
    expect(eventSubjectLabel(dbEvent({ database_name: 'orders-db' }))).toBe('orders-db')
    expect(eventSubjectLabel(dbEvent())).toBe('database #12')
    expect(eventSubjectLabel(dbEvent())).not.toContain('app')
  })

  it('prefers display name then slug for app events', () => {
    expect(eventSubjectLabel(appEvent({ app_display_name: 'Web', app_name: 'web' }))).toBe('Web')
    expect(eventSubjectLabel(appEvent({ app_name: 'web' }))).toBe('web')
    expect(eventSubjectLabel(appEvent())).toBe('app #5')
  })

  // Rows written before subject_type existed carry no discriminator.
  it('treats an event with no subject_type as an app event', () => {
    const legacy = { ...base, application_id: 5, app_name: 'web' } as unknown as AppEvent
    expect(eventSubjectLabel(legacy)).toBe('web')
  })
})

describe('eventSubjectLink', () => {
  // A database event must not link into /apps, where its id means something else.
  it('routes each subject to its own detail page', () => {
    expect(eventSubjectLink(dbEvent())).toBe('/databases/12')
    expect(eventSubjectLink(appEvent())).toBe('/apps/5')
  })
})
