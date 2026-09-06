import api, { sseUrl } from './client'
import type { ApiResponse, AppEvent } from './types'

export const eventsApi = {
  // before: cursor (return events older than this id); limit: page size.
  list(ws: number, app: number, before?: number, limit?: number) {
    const p = new URLSearchParams()
    if (before) p.set('before', String(before))
    if (limit) p.set('limit', String(limit))
    const q = p.toString()
    return api.get<ApiResponse<AppEvent[]>>(`/workspaces/${ws}/apps/${app}/events${q ? `?${q}` : ''}`)
  },
  streamUrl(ws: number, app: number) {
    return sseUrl(`/workspaces/${ws}/apps/${app}/events/stream`)
  },
  // Served under /timeline because /databases/{id}/events is the pre-existing live status
  // stream, not the persisted history.
  databaseList(ws: number, instance: number, before?: number, limit?: number) {
    const p = new URLSearchParams()
    if (before) p.set('before', String(before))
    if (limit) p.set('limit', String(limit))
    const q = p.toString()
    return api.get<ApiResponse<AppEvent[]>>(`/workspaces/${ws}/databases/${instance}/timeline${q ? `?${q}` : ''}`)
  },
  databaseStreamUrl(ws: number, instance: number) {
    return sseUrl(`/workspaces/${ws}/databases/${instance}/timeline/stream`)
  },
  // Live feed of application events across the whole workspace (the dashboard).
  workspaceStreamUrl(ws: number) {
    return sseUrl(`/workspaces/${ws}/events/stream`)
  },
  logsUrl(ws: number, app: number, tail = 1000) {
    // Request a fuller history window (server-capped) so the log viewer's search
    // and resize have real backlog, not just the last 200.
    return sseUrl(`/workspaces/${ws}/apps/${app}/logs/stream?tail=${tail}`)
  },
}
