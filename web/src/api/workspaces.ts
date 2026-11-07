import api, { sseUrl } from './client'
import type { ApiResponse, Workspace, Overview, PendingInvitation } from './types'

export interface CreateWorkspaceInput {
  /** Free-text label. The unique handle is derived from it when `name` is blank. */
  display_name?: string
  /** Desired unique handle; derived from the display name when omitted. */
  name?: string
  description?: string
}

export interface UpdateWorkspaceInput {
  /** Free-text label. `name` is accepted as a legacy alias. */
  display_name?: string
  name?: string
  description?: string
}

// DeletionPhase is one step of a workspace teardown, rendered as a stepper row.
export interface DeletionPhase {
  key: string
  label: string
  status: 'pending' | 'active' | 'done' | 'error'
}

// DeletionJob is the live snapshot streamed while a workspace is being deleted.
export interface DeletionJob {
  id: string
  status: 'running' | 'succeeded' | 'failed'
  phases: DeletionPhase[]
  message?: string
  error?: string
}

export const workspaceApi = {
  list() {
    return api.get<ApiResponse<Workspace[]>>('/workspaces')
  },
  get(id: number) {
    return api.get<ApiResponse<Workspace>>(`/workspaces/${id}`)
  },
  create(input: CreateWorkspaceInput | string) {
    const body = typeof input === 'string' ? { display_name: input } : input
    return api.post<ApiResponse<Workspace>>('/workspaces', body)
  },
  update(id: number, input: UpdateWorkspaceInput) {
    return api.patch<ApiResponse<Workspace>>(`/workspaces/${id}`, input)
  },
  // `name` is the unique URL/CLI/docker handle, so renaming changes its URLs.
  updateName(id: number, name: string) {
    return api.patch<ApiResponse<Workspace>>(`/workspaces/${id}/name`, { name })
  },
  remove(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${id}`)
  },
  // Async deletion: start a job, then stream its teardown progress over SSE.
  startDeletion(id: number) {
    return api.post<ApiResponse<DeletionJob>>(`/workspaces/${id}/deletion/jobs`)
  },
  deletionJob(id: number, jobId: string) {
    return api.get<ApiResponse<DeletionJob>>(`/workspaces/${id}/deletion/jobs/${jobId}`)
  },
  deletionJobEventsUrl(id: number, jobId: string) {
    return sseUrl(`/workspaces/${id}/deletion/jobs/${jobId}/events`)
  },
  overview(workspaceId: number) {
    return api.get<ApiResponse<Overview>>(`/workspaces/${workspaceId}/overview`)
  },
  // Invitations addressed to the current user.
  myInvitations() {
    return api.get<ApiResponse<PendingInvitation[]>>('/workspaces/invitations')
  },
  acceptInvitationToken(token: string) {
    return api.post<ApiResponse<Workspace>>('/workspaces/invitations/accept', { token })
  },
  acceptInvitation(id: number) {
    return api.post<ApiResponse<Workspace>>(`/workspaces/invitations/${id}/accept`)
  },
}
