import api from './client'
import type { ApiResponse } from './types'

// Location is a cluster as a workspace sees it: where its apps, databases and volumes run.
export interface Location {
  id: number
  name: string
  display_name: string
  location_code?: string
  default: boolean
}

export const locationApi = {
  list: (ws: number) => api.get<ApiResponse<Location[]>>(`/workspaces/${ws}/locations`),
  // An empty location clears the workspace default.
  setDefault: (ws: number, location: string) =>
    api.put<ApiResponse<Location[]>>(`/workspaces/${ws}/default-location`, { location }),
}
