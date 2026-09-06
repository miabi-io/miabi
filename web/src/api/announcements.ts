import api from './client'
import type { ApiResponse, PageableResponse } from './types'
import type { AlertSeverity } from './inbox'

export type AnnouncementAudience = 'all' | 'admins' | 'owners' | 'workspaces'
export type AnnouncementStatus = 'scheduled' | 'published' | 'expired'

export interface Announcement {
  id: number
  title: string
  message: string
  link?: string
  action_text?: string
  severity: AlertSeverity
  audience: AnnouncementAudience
  workspace_ids?: number[]
  pinned: boolean
  publish_at?: string | null
  expires_at?: string | null
  published_at?: string | null
  recipients: number
  created_by: number
  author_name: string
  status: AnnouncementStatus
  created_at: string
}

export interface AnnouncementPayload {
  title: string
  message?: string
  link?: string
  action_text?: string
  severity: AlertSeverity
  audience: AnnouncementAudience
  workspace_ids?: number[]
  pinned?: boolean
  publish_at?: string
  expires_at?: string
}

// Platform announcements (Enterprise; gated announcements → 402 in Community).
export const announcementApi = {
  list: (page = 0, size = 50) =>
    api.get<PageableResponse<Announcement>>('/admin/announcements', { params: { page, size } }),
  audience: (audience: AnnouncementAudience, workspaceIds: number[] = []) =>
    api.get<ApiResponse<{ recipients: number }>>('/admin/announcements/audience', {
      params: { audience, workspaces: workspaceIds.join(',') },
    }),
  create: (payload: AnnouncementPayload) =>
    api.post<ApiResponse<Announcement>>('/admin/announcements', payload),
  update: (id: number, payload: AnnouncementPayload) =>
    api.put<ApiResponse<Announcement>>(`/admin/announcements/${id}`, payload),
  publish: (id: number) => api.post<ApiResponse<Announcement>>(`/admin/announcements/${id}/publish`),
  retract: (id: number) => api.delete<ApiResponse<{ message: string }>>(`/admin/announcements/${id}`),
}
