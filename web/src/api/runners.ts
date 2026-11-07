import api, { sseUrl } from './client'
import type { ApiResponse } from './types'

// workspace_id null means a platform-shared runner in the admin pool.
export interface Runner {
  id: number
  uid: string
  name: string
  display_name: string
  workspace_id: number | null
  scope: 'workspace' | 'shared'
  labels: string[] | null
  concurrency: number
  os?: string
  arch?: string
  version?: string
  /** Set when a newer runner release exists. */
  latest_version?: string
  // features the runner reports supporting, such as "multi-platform"
  features?: string[]
  remote_ip?: string
  status: 'online' | 'offline' | 'draining'
  cordoned: boolean
  enabled: boolean
  ephemeral: boolean
  connected: boolean
  last_seen_at?: string | null
  created_at: string
  updated_at: string
}

export interface RunnerInput {
  name: string
  display_name?: string
  labels?: string[]
  concurrency?: number
}

// The registration token is shown once.
export interface RunnerCreated {
  runner: Runner
  token: string
  image: string
}

// Binds either the workspace or admin API so RunnersPanel serves both owned runners and the shared pool.
export interface RunnerAdapter {
  list: () => Promise<Runner[]>
  get: (id: number) => Promise<Runner>
  create: (input: RunnerInput) => Promise<RunnerCreated>
  cordon: (id: number, cordoned: boolean) => Promise<Runner>
  regenerateToken: (id: number) => Promise<string>
  remove: (id: number) => Promise<void>
  // eventsUrl, when set, is the SSE stream that keeps the list's status live.
  eventsUrl?: () => string
}

const wsBase = (ws: number) => `/workspaces/${ws}/runners`

export const runnerApi = {
  list: (ws: number) => api.get<ApiResponse<Runner[]>>(wsBase(ws)),
  // Read-only view of the platform-shared pool available alongside this workspace's runners.
  listShared: (ws: number) => api.get<ApiResponse<Runner[]>>(`${wsBase(ws)}/shared`),
  get: (ws: number, id: number) => api.get<ApiResponse<Runner>>(`${wsBase(ws)}/${id}`),
  create: (ws: number, input: RunnerInput) => api.post<ApiResponse<RunnerCreated>>(wsBase(ws), input),
  update: (ws: number, id: number, input: RunnerInput) => api.put<ApiResponse<Runner>>(`${wsBase(ws)}/${id}`, input),
  cordon: (ws: number, id: number, cordoned: boolean) =>
    api.post<ApiResponse<Runner>>(`${wsBase(ws)}/${id}/cordon`, { cordoned }),
  regenerateToken: (ws: number, id: number) =>
    api.post<ApiResponse<{ token: string }>>(`${wsBase(ws)}/${id}/token`, {}),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${wsBase(ws)}/${id}`),
  statusEventsUrl: (ws: number) => sseUrl(`${wsBase(ws)}/events`),
}

const adminBase = '/admin/runners'

export const adminRunnerApi = {
  list: () => api.get<ApiResponse<Runner[]>>(adminBase),
  get: (id: number) => api.get<ApiResponse<Runner>>(`${adminBase}/${id}`),
  create: (input: RunnerInput) => api.post<ApiResponse<RunnerCreated>>(adminBase, input),
  update: (id: number, input: RunnerInput) => api.put<ApiResponse<Runner>>(`${adminBase}/${id}`, input),
  cordon: (id: number, cordoned: boolean) => api.post<ApiResponse<Runner>>(`${adminBase}/${id}/cordon`, { cordoned }),
  regenerateToken: (id: number) => api.post<ApiResponse<{ token: string }>>(`${adminBase}/${id}/token`, {}),
  remove: (id: number) => api.delete<ApiResponse<{ message: string }>>(`${adminBase}/${id}`),
  statusEventsUrl: () => sseUrl(`${adminBase}/events`),
}
