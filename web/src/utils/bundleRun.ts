import type { BundleRun } from '@/api/portableBackup'

export function bundleStatusClass(status: string): string {
  switch (status) {
    case 'completed':
      return 'badge-success'
    case 'failed':
      return 'badge-danger'
    case 'partial':
    case 'running':
    case 'pending':
      return 'badge-warning'
    default:
      return 'badge-neutral'
  }
}

export function reportActionClass(action: string): string {
  switch (action) {
    case 'failed':
      return 'badge-danger'
    case 'skipped':
      return 'badge-warning'
    default:
      return 'badge-success'
  }
}

/** The i18n key naming a run's kind. */
export function bundleKindLabel(kind: BundleRun['kind']): string {
  return `wsMigration.kind.${kind}`
}
