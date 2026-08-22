import api from './client'
import type { ApiResponse, GitRepository, GitAuthType } from './types'

export interface GitRepositoryInput {
  name: string
  display_name?: string
  url: string
  auth_type?: GitAuthType
  username?: string
  secret?: string
}

/** What to probe. Either git_repo or a git_repository_id carrying a URL is required. */
export interface GitInspectInput {
  git_repo?: string
  git_ref?: string
  git_repository_id?: number | null
}

export interface GitInspectStep {
  name: string
  uses?: string
  image?: string
  continue_on_error?: boolean
}

/** What a repository holds: how it builds, and whether it carries pipeline-as-code. */
export interface GitInspectResult {
  ref: string
  commit: string
  has_dockerfile: boolean
  has_pipeline: boolean
  pipeline_path?: string
  pipeline_name?: string
  /** Set when a pipeline file exists but does not parse; has_pipeline is then false. */
  pipeline_error?: string
  steps?: GitInspectStep[]
  push_branches?: string[]
  triggers_push?: boolean
  manual?: boolean
  schedule?: string
  spec?: string
}

const base = (ws: number) => `/workspaces/${ws}/git-repositories`

export const gitRepositoryApi = {
  list: (ws: number) => api.get<ApiResponse<GitRepository[]>>(base(ws)),
  get: (ws: number, id: number) => api.get<ApiResponse<GitRepository>>(`${base(ws)}/${id}`),
  create: (ws: number, input: GitRepositoryInput) => api.post<ApiResponse<GitRepository>>(base(ws), input),
  update: (ws: number, id: number, input: GitRepositoryInput) => api.patch<ApiResponse<GitRepository>>(`${base(ws)}/${id}`, input),
  // Returns the repository with its refreshed connection status on success; a
  // failed check is a 400 carrying the reason, and is persisted just the same.
  test: (ws: number, id: number) => api.post<ApiResponse<GitRepository>>(`${base(ws)}/${id}/test`),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
  /**
   * Probe a repository before an app exists to hang the question off: does it
   * build from a Dockerfile, and does it carry a pipeline Miabi should adopt?
   * Clones the repo server-side, so it is slower than a normal call.
   */
  inspect: (ws: number, input: GitInspectInput) =>
    api.post<ApiResponse<GitInspectResult>>(`/workspaces/${ws}/git/inspect`, input),
}
