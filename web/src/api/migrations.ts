import api, { sseUrl } from './client'
import type { ApiResponse } from './types'

// Live location migration (Enterprise): move an app, its volumes and its databases to another location.

export type MigrationStatus =
  | 'running'
  | 'awaiting_cutover'
  | 'cut_over'
  | 'finalized'
  | 'rolled_back'
  | 'failed'
  | 'cancelled'

export type DBStrategy = 'move' | 'new_instance' | 'existing_instance'

export interface MigrationIssue {
  code: string
  message: string
  resource?: string
}

export interface MigrationVolume {
  volume_id: number
  name: string
  driver: string
  action: 'copy' | 'redeclare'
  used_bytes: number
  storage_class: string
}

export interface MigrationDBItem {
  instance_id: number
  instance_name: string
  engine: string
  version: string
  exclusive: boolean
  strategies: DBStrategy[]
  strategy: DBStrategy | ''
  target_instance_id?: number
  databases: { id: number; name: string; env_prefix?: string }[]
  size_bytes: number
}

export interface MigrationPlan {
  location: string
  location_label?: string
  target_cluster_id: number
  target_server_id: number
  volumes: MigrationVolume[]
  databases: MigrationDBItem[]
  blockers: MigrationIssue[]
  warnings: MigrationIssue[]
  generated_urls?: string[]
  custom_domains?: string[]
  copy_bytes: number
  service: boolean
}

export interface MigrationProgressItem {
  kind: 'volume' | 'database'
  name: string
  status: 'pending' | 'copying' | 'synced' | 'done' | 'failed'
  bytes: number
  total?: number
  delta?: number
  passes?: number
  detail?: string
}

export interface Migration {
  id: number
  workspace_id: number
  application_id: number
  app_name: string
  source_cluster_id: number
  target_cluster_id: number
  status: MigrationStatus
  phase: string
  cutover_mode: 'auto' | 'manual'
  bandwidth_kbps: number
  cutover_requested: boolean
  cancel_requested: boolean
  plan: MigrationPlan
  progress: {
    items?: MigrationProgressItem[]
    pass?: number
    last_delta_bytes: number
    bytes_per_second: number
    estimated_downtime_seconds: number
    message?: string
  }
  report: {
    items?: { kind: string; name: string; action: string; detail?: string }[]
    notes?: string[]
  }
  error?: string
  downtime_at?: string
  cutover_at?: string
  finalize_after?: string
  finished_at?: string
  created_at: string
}

export interface DBChoice {
  instance_id: number
  strategy: DBStrategy
  target_instance_id?: number
}

export interface StartMigrationBody {
  location: string
  databases?: DBChoice[]
  cutover_mode?: 'auto' | 'manual'
  bandwidth_kbps?: number
}

// An open migration still holds the source copy, including one that cut over and is waiting to be finalized.
export function isOpen(m: Migration): boolean {
  return m.status === 'running' || m.status === 'awaiting_cutover' || m.status === 'cut_over'
}

const appBase = (ws: number, app: number) => `/workspaces/${ws}/apps/${app}/migrations`
const one = (ws: number, id: number) => `/workspaces/${ws}/migrations/${id}`

export const migrationApi = {
  plan: (ws: number, app: number, body: { location: string; databases?: DBChoice[] }) =>
    api.post<ApiResponse<MigrationPlan>>(`${appBase(ws, app)}/plan`, body),
  start: (ws: number, app: number, body: StartMigrationBody) =>
    api.post<ApiResponse<Migration>>(appBase(ws, app), body),
  listForApp: (ws: number, app: number) => api.get<ApiResponse<Migration[]>>(appBase(ws, app)),
  get: (ws: number, id: number) => api.get<ApiResponse<Migration>>(one(ws, id)),
  cutover: (ws: number, id: number) => api.post<ApiResponse<Migration>>(`${one(ws, id)}/cutover`),
  cancel: (ws: number, id: number) => api.post<ApiResponse<Migration>>(`${one(ws, id)}/cancel`),
  rollback: (ws: number, id: number) => api.post<ApiResponse<Migration>>(`${one(ws, id)}/rollback`),
  finalize: (ws: number, id: number) => api.post<ApiResponse<Migration>>(`${one(ws, id)}/finalize`),
  eventsUrl: (ws: number, id: number) => sseUrl(`${one(ws, id)}/events`),
}
