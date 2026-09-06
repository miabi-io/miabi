// Helpers for rendering a timeline event's subject. The workspace feed carries both
// application and database-instance events, so callers must not assume an application:
// reading application_id on a database event yields 0 ("app #0", a link to /apps/0).
import type { AppEvent } from '@/api/types'

// A workspace-feed event also carries the app's resolved names.
type SubjectEvent = AppEvent & { app_name?: string; app_display_name?: string }

// eventSubjectLabel names the resource an event is about, falling back to "<kind> #<id>"
// when it has been deleted.
export function eventSubjectLabel(e: SubjectEvent): string {
  if (e.subject_type === 'database') {
    return e.database_name || `database #${e.database_id ?? 0}`
  }
  return e.app_display_name || e.app_name || e.application_name || `app #${e.application_id}`
}

// eventSubjectLink is the detail page for the event's subject.
export function eventSubjectLink(e: SubjectEvent): string {
  if (e.subject_type === 'database') return `/databases/${e.database_id ?? 0}`
  return `/apps/${e.application_id}`
}

// eventIcon picks an mdi glyph for an event type, across both subjects.
export function eventIcon(type: string): string {
  if (type === 'app.created') return 'mdi-cube-outline'
  if (type === 'app.deleted') return 'mdi-delete-outline'
  if (type.startsWith('deploy')) return 'mdi-rocket-launch-outline'
  if (type.startsWith('rollback')) return 'mdi-backup-restore'
  if (type.startsWith('release')) return 'mdi-tag-outline'
  if (type === 'container.died' || type === 'container.oom') return 'mdi-alert-circle-outline'
  if (type === 'container.health') return 'mdi-heart-pulse'
  if (type.startsWith('container')) return 'mdi-cube-outline'
  if (type.startsWith('backup') || type.startsWith('restore')) return 'mdi-backup-restore'
  if (type === 'database.upgraded' || type === 'database.upgrade_failed') return 'mdi-arrow-up-bold-box-outline'
  if (type.startsWith('database')) return 'mdi-database-outline'
  if (type.startsWith('domain') || type.startsWith('route')) return 'mdi-web'
  if (type.startsWith('env')) return 'mdi-tune-variant'
  if (type.startsWith('volume')) return 'mdi-harddisk'
  if (type.startsWith('settings')) return 'mdi-cog-outline'
  return 'mdi-circle-small'
}
