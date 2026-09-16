// Notifiable application events shared by webhooks and notification channels.
// Mirrors models.NotifiableEvents on the backend.
export interface NotifiableEvent {
  value: string
  label: string
}

export const NOTIFIABLE_EVENTS: NotifiableEvent[] = [
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
]

const labels: Record<string, string> = Object.fromEntries(
  NOTIFIABLE_EVENTS.map((e) => [e.value, e.label]),
)

export function eventLabel(value: string): string {
  return labels[value] ?? value
}
