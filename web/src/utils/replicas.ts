interface ReplicaCounts {
  runtime_kind?: string
  replicas?: number
  running_replicas?: number
  desired_replicas?: number
}

/** Running is null when the swarm could not be asked, so the badge shows the configured count alone. */
export function serviceReplicas(app: ReplicaCounts): { running: number | null; desired: number; degraded: boolean } | null {
  if (app.runtime_kind !== 'service') return null
  const desired = app.desired_replicas ?? app.replicas ?? 1
  const running = app.running_replicas ?? null
  return { running, desired, degraded: running !== null && running < desired }
}
