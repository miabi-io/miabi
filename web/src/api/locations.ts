import api from './client'
import type { ApiResponse } from './types'

// Location is a cluster as a workspace sees it: where its apps, databases and volumes run.
export interface Location {
  id: number
  name: string
  display_name: string
  location_code?: string
  default: boolean
  // The location runs a swarm, so an app there may run as a replicated service.
  swarm?: boolean
}

/**
 * The locations a workspace may use, and whether the choice is still its own.
 *
 * `pinned` means the workspace's organization runs its own clusters: the set is fixed and the
 * default follows the organization, so the UI explains that instead of offering a picker whose
 * every other value the API would refuse.
 */
export interface LocationSet {
  locations: Location[]
  pinned: boolean
  pinned_to?: string
}

// A node of a location, for pinning a create to it. swarm_node_id is what a service pin names.
export interface LocationNode {
  id: number
  name: string
  is_local: boolean
  online: boolean
  cordoned: boolean
  swarm_node_id?: string
}

export const locationApi = {
  list: (ws: number) => api.get<ApiResponse<LocationSet>>(`/workspaces/${ws}/locations`),
  // An empty location lists the nodes of the location a create naming none would land in.
  nodes: (ws: number, location?: string) =>
    api.get<ApiResponse<LocationNode[]>>(`/workspaces/${ws}/location-nodes`, { params: location ? { location } : {} }),
  // An empty location clears the workspace default.
  setDefault: (ws: number, location: string) =>
    api.put<ApiResponse<LocationSet>>(`/workspaces/${ws}/default-location`, { location }),
}
