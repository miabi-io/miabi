export type WorkspaceTraitKey = 'system' | 'privileged'

export interface WorkspaceTrait {
  key: WorkspaceTraitKey
  icon: string
  label: string
  title: string
  badgeClass: string
}

// The system workspace is always privileged too, so it gets the one trait that says the most:
// "system" already implies the relaxed rules that "privileged" names.
export function workspaceTrait(
  w: { system?: boolean; privileged?: boolean } | null | undefined,
  t: (key: string) => string,
): WorkspaceTrait | null {
  if (!w) return null
  if (w.system) {
    return {
      key: 'system',
      icon: 'mdi-cog',
      label: t('switcher.systemBadge'),
      title: t('switcher.systemTitle'),
      badgeClass: 'badge-danger',
    }
  }
  if (w.privileged) {
    return {
      key: 'privileged',
      icon: 'mdi-shield-alert-outline',
      label: t('switcher.privilegedBadge'),
      title: t('switcher.privilegedTitle'),
      badgeClass: 'badge-warning',
    }
  }
  return null
}
