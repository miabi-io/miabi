import api from './client'
import type { ApiResponse, SearchResponse } from './types'

export const searchApi = {
  search: (ws: number, q: string, limit = 20, signal?: AbortSignal) =>
    api.get<ApiResponse<SearchResponse>>(`/workspaces/${ws}/search`, {
      params: { q, limit },
      signal,
    }),
}
