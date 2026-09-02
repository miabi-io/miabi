import api from './client'
import type { ApiResponse, Domain, DomainDetail, DomainTLSMode } from './types'

export interface DomainInput {
  name: string
  tls_mode?: DomainTLSMode
  wildcard?: boolean
}

const base = (ws: number) => `/workspaces/${ws}/domains`

export const domainApi = {
  list: (ws: number) => api.get<ApiResponse<Domain[]>>(base(ws)),
  get: (ws: number, id: number) => api.get<ApiResponse<DomainDetail>>(`${base(ws)}/${id}`),
  create: (ws: number, input: DomainInput) => api.post<ApiResponse<Domain>>(base(ws), input),
  update: (ws: number, id: number, input: DomainInput) =>
    api.patch<ApiResponse<Domain>>(`${base(ws)}/${id}`, input),
  verify: (ws: number, id: number) => api.post<ApiResponse<Domain>>(`${base(ws)}/${id}/verify`),
  // setDnsProvider links (id) or unlinks (null) a DNS provider for automated DNS.
  setDnsProvider: (ws: number, id: number, providerId: number | null) =>
    api.put<ApiResponse<Domain>>(`${base(ws)}/${id}/dns-provider`, { dns_provider_id: providerId }),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
