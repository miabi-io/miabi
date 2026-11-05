import type { WorkspaceRestriction } from '@/api/types'

export function restrictionBadge(r: WorkspaceRestriction | undefined): { cls: string; label: string } {
  switch (r) {
    case 'suspended':
      return { cls: 'badge-danger', label: 'operator.restriction.badgeSuspended' }
    case 'frozen':
      return { cls: 'badge-info', label: 'operator.restriction.badgeFrozen' }
    default:
      return { cls: 'badge-warning', label: 'operator.restriction.badgeReadOnly' }
  }
}
