import api from './client'
import type { ApiResponse, PageableResponse } from './types'

// PlatformBackupSubject mirrors the backend. "identity" is the sealed envelope
// carrying the platform's encryption key — the artifact that makes a recovery
// point restorable onto a *fresh* host rather than only back onto this one.
export type PlatformBackupSubject =
  | 'database'
  | 'volume'
  | 'identity'
  | 'tenant-database'
  | 'tenant-volume'
export type PlatformBackupStatus = 'pending' | 'running' | 'completed' | 'failed'

export interface PlatformBackup {
  id: number
  set_id?: number
  subject: PlatformBackupSubject
  volume_name?: string
  workspace_id?: number
  workspace_slug?: string
  database_name?: string
  engine?: string
  status: PlatformBackupStatus
  trigger: string
  destination: string
  encrypted?: boolean
  s3_bucket?: string
  s3_path?: string
  filename?: string
  size_bytes: number
  logs?: string
  error?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}

// PlatformBackupSet is a recovery point: the identity envelope, the
// control-plane dump and every volume/tenant artifact from one run, retained and
// restored as a unit.
export interface PlatformBackupSet {
  id: number
  ref: string
  trigger: string
  status: PlatformBackupStatus
  install_id?: string
  miabi_version?: string
  schema_version?: string
  kek_fingerprint?: string
  encrypted: boolean
  identity_sealed: boolean
  destination: string
  s3_bucket?: string
  s3_path?: string
  size_bytes: number
  error?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
  items?: PlatformBackup[]
}

export interface VerifyFinding {
  severity: 'error' | 'warning'
  message: string
}

export interface VerifyReport {
  ref: string
  restorable: boolean
  findings?: VerifyFinding[]
  identity_opened: boolean
  kek_matches: boolean
  install_id?: string
  miabi_version?: string
  artifacts_found: number
  artifacts_total: number
}

export interface PlatformBackupSettings {
  id: number
  s3_enabled: boolean
  s3_endpoint?: string
  s3_bucket?: string
  s3_region?: string
  s3_access_key?: string
  s3_use_ssl: boolean
  s3_force_path_style: boolean
  root_path?: string
  database_backup_path?: string
  volume_backup_path?: string
  encrypt_backups: boolean
  include_identity: boolean
  include_tenant_data: boolean
  schedule_enabled: boolean
  schedule_cron?: string
  max_backups: number
  retention_days: number
  volumes: string[]
  created_at: string
  updated_at: string
  s3_secret_set: boolean
  passphrase_set: boolean
  // env_locked names the fields this deployment takes from environment
  // variables. They are read-only here: the process configuration wins, so the
  // form disables them rather than accepting an edit that would be ignored.
  env_locked?: string[]
}

export interface PlatformBackupSettingsPayload {
  s3_enabled: boolean
  s3_endpoint: string
  s3_bucket: string
  s3_region: string
  s3_access_key: string
  s3_secret_key?: string
  s3_use_ssl: boolean
  s3_force_path_style: boolean
  root_path: string
  database_backup_path: string
  volume_backup_path: string
  encrypt_backups: boolean
  backup_passphrase?: string
  include_identity: boolean
  include_tenant_data: boolean
  schedule_enabled: boolean
  schedule_cron: string
  max_backups: number
  retention_days: number
  volumes: string[]
}

// A recovery point found in the bucket — including ones this platform has no
// record of, which is what makes this a recovery tool rather than a history view.
export interface DiscoveredArtifact {
  subject: PlatformBackupSubject
  workspace?: string
  database?: string
  volume?: string
  engine?: string
  file: string
  key: string
  size_bytes?: number
  encrypted: boolean
  restorable: boolean
  reason?: string
  present: boolean
}

export interface DiscoveredSet {
  ref: string
  install_id?: string
  miabi_version?: string
  encrypted: boolean
  identity_sealed: boolean
  created_at: string
  artifacts?: DiscoveredArtifact[]
  known: boolean
  set_id?: number
  foreign: boolean
  kek_matches: boolean
}

export interface SelectiveRestoreResult {
  artifact_id: number
  label: string
  ok: boolean
  error?: string
}

export interface SelectiveRestoreReport {
  ref: string
  requested: number
  restored: number
  results?: SelectiveRestoreResult[]
  started_at: string
  finished_at: string
}

export interface PlatformVolume {
  name: string
  role?: string
}

// Recovery: the state a platform is in between `miabi restore` and an operator
// confirming the recovery is complete.
export interface TenantRestoreSummary {
  ref: string
  databases_restored: number
  volumes_restored: number
  skipped?: string[]
  failures?: string[]
}

export interface RecoveryReport {
  started_at: string
  finished_at: string
  nodes_reset: number
  networks_ensured: number
  databases_started: number
  apps_redeployed: number
  routes_synced: number
  tenant_data?: TenantRestoreSummary
  // Optional on purpose: Go marshals a nil slice as null, so a client that
  // assumes an array here crashes on a clean reconcile — which is the common
  // case, not the edge one.
  unrecoverable?: string[]
  manual?: string[]
  failures?: string[]
}

export interface RecoveryStatus {
  pending: boolean
  note?: string
  report?: RecoveryReport
}

export const platformBackupApi = {
  getSettings: () => api.get<ApiResponse<PlatformBackupSettings>>('/admin/platform-backup/settings'),
  updateSettings: (payload: PlatformBackupSettingsPayload) =>
    api.put<ApiResponse<PlatformBackupSettings>>('/admin/platform-backup/settings', payload),
  testSettings: (payload: PlatformBackupSettingsPayload) =>
    api.post<ApiResponse<{ message: string }>>('/admin/platform-backup/settings/test', payload),

  list: (page = 0, size = 20) =>
    api.get<PageableResponse<PlatformBackup>>('/admin/platform-backup/backups', { params: { page, size } }),
  create: (payload: { database: boolean; volumes: string[] }) =>
    api.post<ApiResponse<PlatformBackup[]>>('/admin/platform-backup/backups', payload),
  restore: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/platform-backup/backups/${id}/restore`, { confirm: true }),
  remove: (id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/platform-backup/backups/${id}`),

  // Recovery points.
  listSets: (page = 0, size = 20) =>
    api.get<PageableResponse<PlatformBackupSet>>('/admin/platform-backup/sets', { params: { page, size } }),
  getSet: (id: number) => api.get<ApiResponse<PlatformBackupSet>>(`/admin/platform-backup/sets/${id}`),
  createSet: () => api.post<ApiResponse<PlatformBackupSet>>('/admin/platform-backup/sets', {}),
  verifySet: (id: number, passphrase = '') =>
    api.post<ApiResponse<VerifyReport>>(`/admin/platform-backup/sets/${id}/verify`, { passphrase }),
  retrySet: (id: number) =>
    api.post<ApiResponse<PlatformBackupSet>>(`/admin/platform-backup/sets/${id}/retry`, {}),
  removeSet: (id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/platform-backup/sets/${id}`),

  volumes: () => api.get<ApiResponse<PlatformVolume[]>>('/admin/platform-backup/volumes'),

  // Browse the bucket, adopt a recovery point, restore selected artifacts.
  discover: () => api.get<ApiResponse<DiscoveredSet[]>>('/admin/platform-backup/discover'),
  importSet: (ref: string) =>
    api.post<ApiResponse<PlatformBackupSet>>('/admin/platform-backup/discover/import', { ref }),
  restoreSelected: (
    id: number,
    payload: { artifact_ids: number[]; passphrase?: string; stop_apps: boolean },
  ) =>
    api.post<ApiResponse<SelectiveRestoreReport>>(
      `/admin/platform-backup/sets/${id}/restore-selected`,
      { ...payload, confirm: true },
    ),

  recoveryStatus: () => api.get<ApiResponse<RecoveryStatus>>('/admin/recovery'),
  reconcile: () => api.post<ApiResponse<RecoveryReport>>('/admin/recovery/reconcile', {}),
  completeRecovery: () =>
    api.post<ApiResponse<{ message: string }>>('/admin/recovery/complete', { confirm: true }),
}
