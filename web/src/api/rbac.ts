import api from './client'
import type { ApiResponse, PermissionCatalog, CustomRole, ResourcePolicy } from './types'

const w = (ws: number) => `/workspaces/${ws}`

// Static permission catalog + built-in role presets for the role-picker matrix.
export const permissionApi = {
  catalog: () => api.get<ApiResponse<PermissionCatalog>>('/permissions'),
}

export interface CustomRoleInput {
  name: string
  base_role: string
  permissions: string[]
}

// Gated by custom_roles on the server; writes return 402 in Community.
export const roleApi = {
  list: (ws: number) => api.get<ApiResponse<CustomRole[]>>(`${w(ws)}/roles`),
  create: (ws: number, body: CustomRoleInput) => api.post<ApiResponse<CustomRole>>(`${w(ws)}/roles`, body),
  update: (ws: number, id: number, body: CustomRoleInput) =>
    api.put<ApiResponse<CustomRole>>(`${w(ws)}/roles/${id}`, body),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/roles/${id}`),
  assignMember: (ws: number, userId: number, customRoleId: number) =>
    api.put<ApiResponse<{ message: string }>>(`${w(ws)}/members/${userId}/custom-role`, { custom_role_id: customRoleId }),
}

// policyApi manages per-app access grants (gated resource_policies on the server).
export const policyApi = {
  listApp: (ws: number, appId: number) =>
    api.get<ApiResponse<ResourcePolicy[]>>(`${w(ws)}/apps/${appId}/policies`),
  grantApp: (ws: number, appId: number, userId: number, permissions: string[]) =>
    api.post<ApiResponse<ResourcePolicy>>(`${w(ws)}/apps/${appId}/policies`, { user_id: userId, permissions }),
  revokeApp: (ws: number, appId: number, userId: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/apps/${appId}/policies/${userId}`),
}

// scope is 'admin' (platform-wide) or a workspace id.
export async function downloadAudit(
  scope: 'admin' | number,
  format: 'json' | 'csv',
  from?: string,
  to?: string,
): Promise<void> {
  const url = scope === 'admin' ? '/admin/audit/export' : `${w(scope)}/audit/export`
  const res = await api.get(url, {
    params: { format, from: from || undefined, to: to || undefined },
    responseType: 'blob',
  })
  const href = URL.createObjectURL(new Blob([res.data as BlobPart]))
  const a = document.createElement('a')
  a.href = href
  a.download = `audit-${scope}.${format}`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(href)
}
