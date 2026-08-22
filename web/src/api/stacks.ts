import api from './client'
import type { ApiResponse, Stack, StackEnvVar, AppEvent } from './types'

export interface StackInput {
  name: string
  description?: string
}

export interface StackUpdateInput {
  // No `name`: a stack's name is its identity — the Compose project name, the
  // per-stack network and the GitOps match key are all derived from it — so the
  // API refuses to change it. Edit display_name instead.
  display_name?: string
  description?: string
}

export interface StackActionResult {
  app_id: number
  app_name: string
  status: 'ok' | 'skipped' | 'failed'
  error?: string
}

export interface StackDeployResult {
  app_id: number
  app_name: string
  status: 'queued' | 'failed'
  deployment_id?: number
  error?: string
}

export interface StackImportResult {
  stack: Stack
  created: string[]
  volumes: string[]
  port_requests: number
  port_conflicts: { service: string; host_port: number; protocol: string; used_by: string }[]
  skipped: { service: string; reason: string }[]
}

const base = (ws: number) => `/workspaces/${ws}/stacks`

export const stackApi = {
  list: (ws: number) => api.get<ApiResponse<Stack[]>>(base(ws)),
  get: (ws: number, id: number) => api.get<ApiResponse<Stack>>(`${base(ws)}/${id}`),
  create: (ws: number, input: StackInput) => api.post<ApiResponse<Stack>>(base(ws), input),
  update: (ws: number, id: number, input: StackUpdateInput) => api.patch<ApiResponse<Stack>>(`${base(ws)}/${id}`, input),
  addApp: (ws: number, id: number, appId: number) => api.post<ApiResponse<Stack>>(`${base(ws)}/${id}/apps/${appId}`),
  removeApp: (ws: number, id: number, appId: number) => api.delete<ApiResponse<Stack>>(`${base(ws)}/${id}/apps/${appId}`),
  start: (ws: number, id: number) => api.post<ApiResponse<StackActionResult[]>>(`${base(ws)}/${id}/start`),
  stop: (ws: number, id: number) => api.post<ApiResponse<StackActionResult[]>>(`${base(ws)}/${id}/stop`),
  restart: (ws: number, id: number, rolling = false) =>
    api.post<ApiResponse<StackActionResult[]>>(`${base(ws)}/${id}/restart${rolling ? '?rolling=true' : ''}`),
  deploy: (ws: number, id: number) => api.post<ApiResponse<StackDeployResult[]>>(`${base(ws)}/${id}/deploy`),
  events: (ws: number, id: number) => api.get<ApiResponse<AppEvent[]>>(`${base(ws)}/${id}/events`),
  envVars: (ws: number, id: number) => api.get<ApiResponse<StackEnvVar[]>>(`${base(ws)}/${id}/env`),
  setEnvVar: (ws: number, id: number, key: string, value: string, isSecret: boolean) =>
    api.put<ApiResponse<{ message: string }>>(`${base(ws)}/${id}/env`, { key, value, is_secret: isSecret }),
  importEnvVars: (ws: number, id: number, content: string, isSecret: boolean) =>
    api.post<ApiResponse<{ imported: number }>>(`${base(ws)}/${id}/env/import`, { content, is_secret: isSecret }),
  deleteEnvVar: (ws: number, id: number, key: string) =>
    api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}/env/${encodeURIComponent(key)}`),
  remove: (ws: number, id: number, withApps = false) =>
    api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}${withApps ? '?with_apps=true' : ''}`),
  import: (ws: number, name: string, compose: string) =>
    api.post<ApiResponse<StackImportResult>>(`${base(ws)}/import`, { name, compose }),
}
