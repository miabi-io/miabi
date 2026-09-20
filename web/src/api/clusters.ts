import api from './client'
import type { ApiResponse, Cluster } from './types'

export interface ClusterUpdate {
  display_name?: string
  location_code?: string
  visibility?: Cluster['visibility']
  /** Dedicates the location to one organization; 0 releases it back to shared. */
  organization_id?: number
  /**
   * Confirms the caller accepts what a change of organization does to the tenants already here.
   * The API refuses a change of owner without it (409).
   */
  acknowledge?: boolean
  cordoned?: boolean
  external_base_domain?: string
  external_cert_provider?: string
  service_endpoint_mode?: 'vip' | 'dnsrr'
  ingress_ip?: string
  ingress_hostname?: string
}

/** One organization's share of a location, for the warning shown before its owner changes. */
export interface TenantWorkloads {
  organization_id: number
  /** Empty for the workspaces that belong to no organization. */
  name?: string
  workloads: number
}

/** What a change of owning organization would affect in a location. */
export interface DedicationImpact {
  cluster_id: number
  workloads: number
  /** Organizations with workloads here, largest share first. */
  tenants: TenantWorkloads[]
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
  // What a change of owning organization would affect here. Read before asking for one.
  dedication: (id: number) => api.get<ApiResponse<DedicationImpact>>(`/admin/clusters/${id}/dedication`),
  setGateway: (id: number, body: ClusterGatewayInput) =>
    api.put<ApiResponse<Cluster>>(`/admin/clusters/${id}/gateway`, body),
  convertIngress: (id: number, body: ConvertIngressInput) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/clusters/${id}/convert`, body),
}
