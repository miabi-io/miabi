// Workspace overlays carry this suffix and bridges never do (see the network service's naming rule).
const OVERLAY_SUFFIX = '-ov'

interface Placed {
  server_id?: number | null
  cluster_id?: number | null
}

interface DatabaseNetworkInfo extends Placed {
  network_name?: string
}

/** Whether a Docker network name is a workspace overlay, reachable from every node of its cluster. */
export function isOverlayNetwork(name?: string): boolean {
  return (name ?? '').endsWith(OVERLAY_SUFFIX)
}

/** canReachDatabase: an overlay is reachable cluster-wide, a node-local bridge only from its own node. */
export function canReachDatabase(app: Placed, inst: DatabaseNetworkInfo): boolean {
  if (isOverlayNetwork(inst.network_name)) {
    return (app.cluster_id ?? 0) === (inst.cluster_id ?? 0)
  }
  return (app.server_id ?? 0) === (inst.server_id ?? 0)
}
