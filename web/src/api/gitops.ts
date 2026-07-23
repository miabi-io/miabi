import api from './client'
import type { ApiResponse, GitSource, GitSyncPolicy, ApplyPlan, ApplyResult, Topology, GitOpsDeleteResult } from './types'

export interface GitSourceInput {
  name: string
  repo_url: string
  ref?: string
  path?: string
  git_repository_id?: number | null
  sync_policy?: GitSyncPolicy
  prune?: boolean
  self_heal?: boolean
  allow_empty?: boolean
}

const base = (ws: number) => `/workspaces/${ws}/gitops`

export const gitopsApi = {
  list: (ws: number) => api.get<ApiResponse<GitSource[]>>(base(ws)),
  get: (ws: number, id: number) => api.get<ApiResponse<GitSource>>(`${base(ws)}/${id}`),
  create: (ws: number, input: GitSourceInput) => api.post<ApiResponse<GitSource>>(base(ws), input),
  update: (ws: number, id: number, input: GitSourceInput) =>
    api.patch<ApiResponse<GitSource>>(`${base(ws)}/${id}`, input),
  sync: (ws: number, id: number) => api.post<ApiResponse<GitSource>>(`${base(ws)}/${id}/sync`),
  // Reconcile a single managed resource (kind/name) without touching the rest of
  // the project.
  syncResource: (ws: number, id: number, kind: string, name: string) =>
    api.post<ApiResponse<ApplyResult>>(
      `${base(ws)}/${id}/resources/${encodeURIComponent(kind)}/${encodeURIComponent(name)}/sync`,
    ),
  // Delete a single live resource. Under auto-sync it is recreated on the next
  // reconcile — callers warn the user first.
  deleteResource: (ws: number, id: number, kind: string, name: string) =>
    api.delete<ApiResponse<ApplyResult>>(
      `${base(ws)}/${id}/resources/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`,
    ),
  diff: (ws: number, id: number) => api.get<ApiResponse<ApplyPlan>>(`${base(ws)}/${id}/diff`),
  topology: (ws: number, id: number) => api.get<ApiResponse<Topology>>(`${base(ws)}/${id}/topology`),
  status: (ws: number, id: number) =>
    api.get<ApiResponse<{ statuses: Record<string, string> }>>(`${base(ws)}/${id}/status`),
  remove: (ws: number, id: number, cascade = false) =>
    api.delete<ApiResponse<GitOpsDeleteResult>>(`${base(ws)}/${id}${cascade ? '?cascade=true' : ''}`),
}
