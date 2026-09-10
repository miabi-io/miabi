import api from './client'
import type { ApiResponse, PortBinding, PortBindingStatus, PortOverview } from './types'

export interface PortBindingRequest {
  application_id: number
  container_port: number
  protocol?: 'tcp' | 'udp'
  host_port: number
}

const base = (ws: number) => `/workspaces/${ws}/port-bindings`

export const portBindingApi = {
  listByApp: (ws: number, appId: number) => api.get<ApiResponse<PortBinding[]>>(`${base(ws)}?application_id=${appId}`),
  list: (ws: number) => api.get<ApiResponse<PortBinding[]>>(base(ws)),
  request: (ws: number, input: PortBindingRequest) => api.post<ApiResponse<PortBinding>>(base(ws), input),
  // Suggest a free host port on the app's node (to remap a conflicting one).
  suggest: (ws: number, appId: number, protocol: 'tcp' | 'udp' = 'tcp', preferred = 0) =>
    api.get<ApiResponse<{ host_port: number }>>(`${base(ws)}/suggest?application_id=${appId}&protocol=${protocol}&preferred=${preferred}`),
  cancel: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),

  // Platform-admin review queue.
  overview: () => api.get<ApiResponse<PortOverview>>('/system/ports'),
  adminList: (status: PortBindingStatus = 'pending') => api.get<ApiResponse<PortBinding[]>>(`/system/port-bindings?status=${status}`),
  approve: (id: number, note = '') => api.post<ApiResponse<PortBinding>>(`/system/port-bindings/${id}/approve`, { note }),
  reject: (id: number, note = '') => api.post<ApiResponse<PortBinding>>(`/system/port-bindings/${id}/reject`, { note }),
}
