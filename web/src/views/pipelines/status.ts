import type { PipelineRunStatus } from '@/api/types'
import { t } from '@/i18n'

interface StatusMeta { label: string; badge: string; icon: string }

// runStatusMeta maps a run/step status to its badge styling, label, and icon.
export const runStatusMeta: Record<PipelineRunStatus, StatusMeta> = {
  pending: { label: 'pipelines.status.pending', badge: 'badge-neutral', icon: 'mdi-clock-outline' },
  running: { label: 'pipelines.status.running', badge: 'badge-info', icon: 'mdi-loading mdi-spin' },
  succeeded: { label: 'pipelines.status.succeeded', badge: 'badge-success', icon: 'mdi-check-circle-outline' },
  failed: { label: 'pipelines.status.failed', badge: 'badge-danger', icon: 'mdi-close-circle-outline' },
  canceled: { label: 'pipelines.status.canceled', badge: 'badge-neutral', icon: 'mdi-cancel' },
}

const unknownStatusMeta: StatusMeta = { label: 'pipelines.status.unknown', badge: 'badge-neutral', icon: 'mdi-help-circle-outline' }

// Falls back to "Unknown" so a status the UI doesn't know yet can't crash the template.
export function statusMeta(status: string): StatusMeta {
  const meta = runStatusMeta[status as PipelineRunStatus] ?? unknownStatusMeta
  return { ...meta, label: t(meta.label) }
}
