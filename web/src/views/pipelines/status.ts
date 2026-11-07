import type { PipelineRunStatus } from '@/api/types'

interface StatusMeta { label: string; badge: string; icon: string }

// runStatusMeta maps a run/step status to its badge styling, label, and icon.
export const runStatusMeta: Record<PipelineRunStatus, StatusMeta> = {
  pending: { label: 'Pending', badge: 'badge-neutral', icon: 'mdi-clock-outline' },
  running: { label: 'Running', badge: 'badge-info', icon: 'mdi-loading mdi-spin' },
  succeeded: { label: 'Succeeded', badge: 'badge-success', icon: 'mdi-check-circle-outline' },
  failed: { label: 'Failed', badge: 'badge-danger', icon: 'mdi-close-circle-outline' },
  canceled: { label: 'Canceled', badge: 'badge-neutral', icon: 'mdi-cancel' },
}

const unknownStatusMeta: StatusMeta = { label: 'Unknown', badge: 'badge-neutral', icon: 'mdi-help-circle-outline' }

// Falls back to "Unknown" so a status the UI doesn't know yet can't crash the template.
export function statusMeta(status: string): StatusMeta {
  return runStatusMeta[status as PipelineRunStatus] ?? unknownStatusMeta
}
