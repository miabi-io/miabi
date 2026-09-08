import api, { sseUrl } from './client'
import type { ApiResponse } from './types'

export type AlertSeverity = 'info' | 'warning' | 'critical'

// How a pinned notice retires for the reader: 'once' closes it for good, 'never'
// withholds the close control until the notice expires or is retracted.
export type AnnouncementDismissal = 'once' | 'never'

// InboxNotification is one per-user bell/inbox item — the delivery of an alert or
// a platform announcement. workspace_id is 0 for platform-scoped items.
export interface InboxNotification {
  id: number
  workspace_id: number
  alert_id?: number
  announcement_id?: number
  kind: 'alert' | 'info' | 'announcement'
  category: string
  severity: AlertSeverity
  title: string
  body: string
  subject_link?: string
  action_text?: string
  pinned: boolean
  dismissal: AnnouncementDismissal
  expires_at?: string | null
  dismissed_at?: string | null
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
  banners: () => api.get<ApiResponse<InboxNotification[]>>('/notifications/banners'),
  dismiss: (ids: number[]) => api.post<ApiResponse<{ message: string }>>('/notifications/dismiss', { ids }),
  streamUrl: () => sseUrl('/notifications/stream'),
}
