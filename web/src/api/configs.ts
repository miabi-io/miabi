import api from './client'
import type { ApiResponse, PageableResponse } from './types'

/** A config never carries content on list/get — only its keys and their sizes. */
export interface Config {
  id: number
  uid: string
  name: string
  display_name?: string
  description?: string
  digest: string
  mode: string
  sensitive: boolean
  delimiters?: string[]
  version: number
  managed: boolean
  keys: string[]
  sizes: Record<string, number>
  created_at: string
  updated_at: string
}

export interface ConfigInput {
  name?: string
  display_name?: string
  description?: string
  data?: Record<string, string>
  mode?: string
  sensitive?: boolean
  delimiters?: string[]
}

export interface ConfigUsage {
  id: number
  name: string
}

const base = (ws: number) => `/workspaces/${ws}/configs`

export const configApi = {
  list: (ws: number, search = '', page = 0, size = 20) =>
    api.get<PageableResponse<Config>>(base(ws), { params: { search: search || undefined, page, size } }),
  get: (ws: number, id: number) => api.get<ApiResponse<Config>>(`${base(ws)}/${id}`),
  create: (ws: number, input: ConfigInput) => api.post<ApiResponse<Config>>(base(ws), input),
  update: (ws: number, id: number, input: ConfigInput) => api.put<ApiResponse<Config>>(`${base(ws)}/${id}`, input),
  reveal: (ws: number, id: number) => api.get<ApiResponse<{ data: Record<string, string> }>>(`${base(ws)}/${id}/reveal`),
  usage: (ws: number, id: number) => api.get<ApiResponse<ConfigUsage[]>>(`${base(ws)}/${id}/usage`),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
