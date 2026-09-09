import api from './client'
import type { ApiResponse } from './types'

export interface BackupSettings {
  id: number
  workspace_id: number
  s3_enabled: boolean
  s3_endpoint?: string
  s3_bucket?: string
  s3_region?: string
  s3_access_key?: string
  s3_use_ssl: boolean
  s3_force_path_style: boolean
  database_backup_path?: string
  volume_backup_path?: string
  bundle_path?: string
  s3_secret_set: boolean
  bundle_passphrase_set: boolean
  backup_passphrase_set: boolean
  created_at?: string
  updated_at?: string
}

// UpdateBackupSettingsInput mirrors the backend body. Leave s3_secret_key,
// bundle_passphrase or backup_passphrase empty to keep the stored value unchanged;
// backup_passphrase_clear is the only way to turn database encryption back off.
export interface UpdateBackupSettingsInput {
  s3_enabled: boolean
  s3_endpoint: string
  s3_bucket: string
  s3_region: string
  s3_access_key: string
  s3_secret_key: string
  s3_use_ssl: boolean
  s3_force_path_style: boolean
  database_backup_path: string
  volume_backup_path: string
  bundle_path: string
  bundle_passphrase: string
  backup_passphrase: string
  backup_passphrase_clear: boolean
}

// One prefix's result from a connection test: the probe wrote a small object
// there, read it back and tried to remove it.
export interface BackupPrefixCheck {
  prefix: string
  key?: string
  removed: boolean
  error?: string
}

export interface BackupTestResult {
  ok: boolean
  message: string
  checks: BackupPrefixCheck[]
}

export const workspaceBackupApi = {
  get(workspaceId: number) {
    return api.get<ApiResponse<BackupSettings>>(`/workspaces/${workspaceId}/backup-settings`)
  },
  update(workspaceId: number, input: UpdateBackupSettingsInput) {
    return api.put<ApiResponse<BackupSettings>>(`/workspaces/${workspaceId}/backup-settings`, input)
  },
  test(workspaceId: number, input: UpdateBackupSettingsInput) {
    return api.post<ApiResponse<BackupTestResult>>(`/workspaces/${workspaceId}/backup-settings/test`, input)
  },
}
