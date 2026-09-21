// Notifiable events shared by webhooks and notification channels.
//
// Mirrors models.NotifiableEvents on the backend, in the same order, and
// internal/models/notifiable_events_test.go fails if the two drift. They did drift once: the backend
// accepted every database event while this list offered none of them, so a workspace could not
// subscribe a channel to a failed backup at all — the option simply was not on the page.
export interface NotifiableEvent {
  value: string
  label: string
}

// A group is a heading in the picker. Twenty flat checkboxes is a wall; the split is what makes
// "where do I turn on backup alerts" answerable by looking.
export interface NotifiableEventGroup {
  label: string
  events: NotifiableEvent[]
}

export const NOTIFIABLE_EVENT_GROUPS: NotifiableEventGroup[] = [
  {
    label: 'Applications',
    events: [
      { value: 'deploy.started', label: 'Deployment started' },
      { value: 'deploy.succeeded', label: 'Deployment succeeded' },
      { value: 'deploy.failed', label: 'Deployment failed' },
      { value: 'container.started', label: 'Container started' },
      { value: 'container.stopped', label: 'Container stopped' },
      { value: 'container.died', label: 'Container exited' },
      { value: 'container.oom', label: 'Container out of memory' },
      { value: 'container.removed', label: 'Container removed' },
      { value: 'drift.detected', label: 'Workload missing' },
      { value: 'drift.resolved', label: 'Workload back' },
      { value: 'reconcile.redeploy', label: 'Redeployed automatically' },
      { value: 'reconcile.breaker_open', label: 'Automatic redeploy gave up' },
    ],
  },
  {
    // Labels match notify.eventTitle, so the picker and the message it delivers agree.
    label: 'Databases & backups',
    events: [
      { value: 'database.provisioned', label: 'Database provisioned' },
      { value: 'database.provision_failed', label: 'Database provisioning failed' },
      { value: 'database.upgraded', label: 'Database upgraded' },
      { value: 'database.upgrade_failed', label: 'Database upgrade failed' },
      { value: 'backup.succeeded', label: 'Backup completed' },
      { value: 'backup.failed', label: 'Backup failed' },
      { value: 'restore.succeeded', label: 'Restore completed' },
      { value: 'restore.failed', label: 'Restore failed' },
    ],
  },
]

// The flat list, in the backend's order. Kept for callers that only need the values.
export const NOTIFIABLE_EVENTS: NotifiableEvent[] = NOTIFIABLE_EVENT_GROUPS.flatMap((g) => g.events)

const labels: Record<string, string> = Object.fromEntries(
  NOTIFIABLE_EVENTS.map((e) => [e.value, e.label]),
)

export function eventLabel(value: string): string {
  return labels[value] ?? value
}
