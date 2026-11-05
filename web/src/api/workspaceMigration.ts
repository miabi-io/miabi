import axios, { type AxiosResponse } from 'axios'
import api from './client'
import type { ApiResponse } from './types'
import type { BundleRun } from './portableBackup'

// Miabi → Miabi migration: the source's owner starts a migration and hands over its code; the
// target pastes the code and pulls the workspace directly.

export interface MigrationStatus {
  enabled: boolean
  url?: string
}

export interface MigrationWindow {
  started_at: string
  expires_at: string | null
  last_used_at: string | null
}

export interface MigrationExportSummary {
  id: number
  workspace: string
  status: BundleRun['status']
  phase?: string
  error?: string
  freeze: boolean
  data: boolean
  completed: boolean
  imported_by?: string
  created_at: string
}

export interface MigrationOverview {
  enabled: boolean
  window: MigrationWindow | null
  frozen?: boolean
  frozen_reason?: string
  exports: MigrationExportSummary[]
  url?: string
}

/** Returned once: the code cannot be read again, only replaced by starting anew. */
export interface MigrationStarted {
  code: string
  source_url: string
  workspace?: string
  expires_at: string
}

export interface MigrationSource {
  code: string
  insecure_skip_tls_verify: boolean
  ca_bundle: string
}

export interface MigrationWorkspace {
  name: string
  display_name?: string
  frozen?: boolean
  apps: number
  databases: number
  volumes: number
  size_bytes: number
}

export interface MigrationSourceInfo {
  install_id: string
  version: string
  schema: number
  /** The workspace a workspace code was started for; empty for an instance code. */
  code_workspace?: string
  workspaces: MigrationWorkspace[]
}

export interface MigrationImportInput extends MigrationSource {
  source_workspace?: string
  new_workspace: string
  restore_data: boolean
  deploy_apps: boolean
  freeze: boolean
  rehearse: boolean
  retire_source: boolean
}

export interface MigrationBatchInput extends MigrationSource {
  /** Source workspace names; every workspace the code opens when empty. */
  workspaces: string[]
  restore_data: boolean
  deploy_apps: boolean
  freeze: boolean
  retire_source: boolean
}

export interface MigrationBatch {
  batch_id: string
  runs: BundleRun[]
  errors?: Record<string, string>
}

export const MIGRATION_EXPIRY_HOURS = [1, 24, 72, 168] as const

export function isRunActive(r: Pick<BundleRun, 'status'>): boolean {
  return r.status === 'pending' || r.status === 'running'
}

export function emptySource(): MigrationSource {
  return { code: '', insecure_skip_tls_verify: false, ca_bundle: '' }
}

export function trimSource(s: MigrationSource): MigrationSource {
  return { ...s, code: s.code.replace(/\s+/g, ''), ca_bundle: s.ca_bundle.trim() }
}

export const migrationApi = {
  // With MIABI_MIGRATION_ENABLED unset the server registers no migration route at all, so a 404 is "off".
  async status(): Promise<AxiosResponse<ApiResponse<MigrationStatus>>> {
    try {
      return await api.get<ApiResponse<MigrationStatus>>('/migrations/status')
    } catch (e) {
      if (axios.isAxiosError(e) && e.response?.status === 404) {
        return { data: { success: true, data: { enabled: false } } } as AxiosResponse<ApiResponse<MigrationStatus>>
      }
      throw e
    }
  },

  workspace(ws: number) {
    return api.get<ApiResponse<MigrationOverview>>(`/workspaces/${ws}/migration`)
  },
  startWorkspace(ws: number, expiresInHours: number) {
    return api.post<ApiResponse<MigrationStarted>>(`/workspaces/${ws}/migration/start`, { expires_in_hours: expiresInHours })
  },
  cancelWorkspace(ws: number) {
    return api.delete<ApiResponse<{ cancelled: boolean }>>(`/workspaces/${ws}/migration`)
  },
  unfreeze(ws: number) {
    return api.post<ApiResponse<{ unfrozen: boolean }>>(`/workspaces/${ws}/migration/unfreeze`)
  },

  probe(source: MigrationSource) {
    return api.post<ApiResponse<MigrationSourceInfo>>('/migrations/probe', source)
  },
  start(input: MigrationImportInput) {
    return api.post<ApiResponse<BundleRun>>('/migrations', input)
  },
  list() {
    return api.get<ApiResponse<BundleRun[]>>('/migrations')
  },
  get(id: number) {
    return api.get<ApiResponse<BundleRun>>(`/migrations/${id}`)
  },

  instance() {
    return api.get<ApiResponse<MigrationOverview>>('/admin/migration')
  },
  startInstance(expiresInHours: number) {
    return api.post<ApiResponse<MigrationStarted>>('/admin/migration/start', { expires_in_hours: expiresInHours })
  },
  cancelInstance() {
    return api.delete<ApiResponse<{ cancelled: boolean }>>('/admin/migration')
  },
  listAll() {
    return api.get<ApiResponse<BundleRun[]>>('/admin/workspace-migrations')
  },
  startBatch(input: MigrationBatchInput) {
    return api.post<ApiResponse<MigrationBatch>>('/admin/workspace-migrations', input)
  },
}
