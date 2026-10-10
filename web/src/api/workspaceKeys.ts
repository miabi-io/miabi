import api from './client'
import type { ApiResponse } from './types'

export interface SealingKeyInfo {
  version: number
  public_key: string
  active: boolean
  created_at: string
  sealed_secrets: number
}

export interface WorkspaceKeysStatus {
  encryption_enabled: boolean
  data_key_version: number
  last_rotated_at?: string
  next_rotation_at?: string
  can_rotate: boolean
  rotation_months: number
  sealing_keys: SealingKeyInfo[]
  sealed_secrets: number
  outdated_sealed_secrets: number
}

export interface RotateKeysResult {
  data_key_version: number
  reencrypted: number
  stale_columns?: string[]
  sealing_key_version: number
}

const base = (workspaceId: number) => `/workspaces/${workspaceId}/encryption`

export const workspaceKeysApi = {
  status: (workspaceId: number) => api.get<ApiResponse<WorkspaceKeysStatus>>(base(workspaceId)),
  rotate: (workspaceId: number) => api.post<ApiResponse<RotateKeysResult>>(`${base(workspaceId)}/rotate`),
}
