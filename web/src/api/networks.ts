import api from './client'
import type { ApiResponse, Network, NetworkDetail } from './types'

export interface NetworkInput {
  name: string
  driver?: string
  internal?: boolean
}

const base = (ws: number) => `/workspaces/${ws}/networks`

export const networkApi = {
  list: (ws: number) => api.get<ApiResponse<Network[]>>(base(ws)),
  // One network with its live addressing (IPv4, IPv6, gateways) as the engine reports it.
  get: (ws: number, id: number) => api.get<ApiResponse<NetworkDetail>>(`${base(ws)}/${id}`),
  create: (ws: number, input: NetworkInput) => api.post<ApiResponse<Network>>(base(ws), input),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
