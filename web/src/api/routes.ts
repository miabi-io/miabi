import api from './client'
import type { ApiResponse, Route, RouteTLSMode } from './types'

export interface RouteInput {
  name: string
  application_id?: number
  path?: string
  hosts?: string[]
  methods?: string[]
  middlewares?: string[]
  rewrite?: string
  target_port?: number
  tls_mode?: RouteTLSMode
  certificate_id?: number | null
  advanced_config?: string
  enabled?: boolean
}

const base = (ws: number) => `/workspaces/${ws}/routes`

export const routeApi = {
  list: (ws: number) => api.get<ApiResponse<Route[]>>(base(ws)),
  listByApp: (ws: number, appId: number) => api.get<ApiResponse<Route[]>>(`${base(ws)}?application_id=${appId}`),
  get: (ws: number, id: number) => api.get<ApiResponse<Route>>(`${base(ws)}/${id}`),
  create: (ws: number, input: RouteInput) => api.post<ApiResponse<Route>>(base(ws), input),
  update: (ws: number, id: number, input: RouteInput) => api.patch<ApiResponse<Route>>(`${base(ws)}/${id}`, input),
  // Partial update: flips only `enabled`, preserving all other route fields.
  setEnabled: (ws: number, id: number, enabled: boolean) => api.patch<ApiResponse<Route>>(`${base(ws)}/${id}/enabled`, { enabled }),
  setSecurity: (ws: number, id: number, body: { exploit_protection?: boolean }) =>
    api.patch<ApiResponse<Route>>(`${base(ws)}/${id}/security`, body),
  setMaintenance: (ws: number, id: number, body: { enabled: boolean; status_code?: number; message?: string }) =>
    api.patch<ApiResponse<Route>>(`${base(ws)}/${id}/maintenance`, body),
  // Attach/detach a middleware without editing the route. Works on generated
  // routes too, so users can layer auth/rate-limit/etc. onto managed routes.
  setMiddlewares: (ws: number, id: number, middlewares: string[]) =>
    api.put<ApiResponse<Route>>(`${base(ws)}/${id}/middlewares`, { middlewares }),
  attachMiddleware: (ws: number, id: number, name: string) => api.post<ApiResponse<Route>>(`${base(ws)}/${id}/middlewares`, { name }),
  detachMiddleware: (ws: number, id: number, name: string) => api.delete<ApiResponse<Route>>(`${base(ws)}/${id}/middlewares/${encodeURIComponent(name)}`),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
