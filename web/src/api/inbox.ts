import api, { sseUrl } from './client'
import type { ApiResponse } from './types'

export type AlertSeverity = 'info' | 'warning' | 'critical'

// InboxNotification is one per-user bell/inbox item (delivery of an alert).
export interface InboxNotification {
  id: number
  workspace_id: number
  alert_id?: number
  kind: 'alert' | 'info'
  category: string
  severity: AlertSeverity
  title: string
  body: string
  subject_link?: string
  read_at?: string | null
  created_at: string
}

export interface InboxListParams {
  workspace?: number
  unread?: boolean
  before?: number
  limit?: number
}

// The per-user notification inbox (dashboard bell + Notifications page).
export const inboxApi = {
  list: (params: InboxListParams = {}) =>
    api.get<ApiResponse<InboxNotification[]>>('/notifications', {
      params: {
        ...(params.workspace ? { workspace: params.workspace } : {}),
        ...(params.unread ? { unread: 'true' } : {}),
        ...(params.before ? { before: params.before } : {}),
        ...(params.limit ? { limit: params.limit } : {}),
      },
    }),
  unreadCount: () => api.get<ApiResponse<{ unread: number }>>('/notifications/unread-count'),
  markRead: (ids: number[]) => api.post<ApiResponse<{ message: string }>>('/notifications/read', { ids }),
  markAllRead: (workspace?: number) =>
    api.post<ApiResponse<{ message: string }>>('/notifications/read-all', null, {
      params: workspace ? { workspace } : {},
    }),
  streamUrl: () => sseUrl('/notifications/stream'),
}
