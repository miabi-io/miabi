import api from './client'
import type { ApiResponse, Cluster } from './types'

// The admin cluster inventory: every node belongs to exactly one cluster.
export const clustersApi = {
  list: () => api.get<ApiResponse<Cluster[]>>('/admin/clusters'),
  get: (id: number | string) => api.get<ApiResponse<Cluster>>(`/admin/clusters/${id}`),
  update: (id: number, body: { display_name: string; location_code: string }) =>
    api.patch<ApiResponse<Cluster>>(`/admin/clusters/${id}`, body),
}
