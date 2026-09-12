import api from './client'
import type { ApiResponse, Cluster } from './types'

export interface ClusterUpdate {
  display_name?: string
  location_code?: string
  visibility?: Cluster['visibility']
  cordoned?: boolean
  external_base_domain?: string
  external_cert_provider?: string
}

export interface ConvertIngressInput {
  action: 'gateway' | 'join'
  target_cluster_id?: number
}

export interface ClusterGatewayInput {
  server_id: number
  ingress_ip?: string
  ingress_hostname?: string
}

// The admin cluster inventory: every node belongs to exactly one cluster.
export const clustersApi = {
  list: () => api.get<ApiResponse<Cluster[]>>('/admin/clusters'),
  get: (id: number | string) => api.get<ApiResponse<Cluster>>(`/admin/clusters/${id}`),
  update: (id: number, body: ClusterUpdate) => api.patch<ApiResponse<Cluster>>(`/admin/clusters/${id}`, body),
  setGateway: (id: number, body: ClusterGatewayInput) =>
    api.put<ApiResponse<Cluster>>(`/admin/clusters/${id}/gateway`, body),
  convertIngress: (id: number, body: ConvertIngressInput) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/clusters/${id}/convert`, body),
}
