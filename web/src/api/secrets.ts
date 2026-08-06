import api from './client'
import type { ApiResponse, PageableResponse, Secret } from './types'

export interface SecretInput {
  name?: string
  value?: string
  description?: string
}

const base = (ws: number) => `/workspaces/${ws}/secrets`

/** Ownership filter: platform-owned secrets, hand-created ones, or both. */
export type SecretOwnership = 'all' | 'managed' | 'unmanaged'

/** The API takes a tri-state `managed`; omitting it means "both". */
function managedParam(o: SecretOwnership): boolean | undefined {
  return o === 'all' ? undefined : o === 'managed'
}

export const secretApi = {
  list: (ws: number, search = '', page = 0, size = 20, ownership: SecretOwnership = 'all') =>
    api.get<PageableResponse<Secret>>(base(ws), {
      params: { search: search || undefined, page, size, managed: managedParam(ownership) },
    }),
  create: (ws: number, input: SecretInput) => api.post<ApiResponse<Secret>>(base(ws), input),
  update: (ws: number, id: number, input: SecretInput) => api.put<ApiResponse<Secret>>(`${base(ws)}/${id}`, input),
  reveal: (ws: number, id: number) => api.get<ApiResponse<{ value: string }>>(`${base(ws)}/${id}/reveal`),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
