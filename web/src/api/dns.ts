import api from './client'
import type {
  ApiResponse,
  DNSProvider,
  DNSProviderDescriptor,
  ConnectDNSProviderInput,
  UpdateDNSProviderInput,
} from './types'

const base = (ws: number) => `/workspaces/${ws}/dns/providers`

export const dnsProviderApi = {
  list: (ws: number) => api.get<ApiResponse<DNSProvider[]>>(base(ws)),
  get: (ws: number, id: number) => api.get<ApiResponse<DNSProvider>>(`${base(ws)}/${id}`),
  connect: (ws: number, input: ConnectDNSProviderInput) =>
    api.post<ApiResponse<DNSProvider>>(base(ws), input),
  update: (ws: number, id: number, input: UpdateDNSProviderInput) =>
    api.put<ApiResponse<DNSProvider>>(`${base(ws)}/${id}`, input),
  catalog: (ws: number) =>
    api.get<ApiResponse<DNSProviderDescriptor[]>>(`/workspaces/${ws}/dns-provider-catalog`),
  test: (ws: number, id: number, zone: string) =>
    api.post<ApiResponse<DNSProvider>>(`${base(ws)}/${id}/test`, { zone }),
  remove: (ws: number, id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${base(ws)}/${id}`),
}
