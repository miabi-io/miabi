import api from './client'
import type { ApiResponse } from './types'

// A bundle's index, as read from the info file in the bucket. It is the same
// document an operator finds beside the artifacts, so what the UI lists is what
// the bucket actually holds — not this platform's memory of writing it.
export interface BundleArtifact {
  subject: 'state' | 'database' | 'volume'
  database?: string
  instance?: string
  engine?: string
  volume?: string
  file: string
  path?: string
  size_bytes?: number
  encrypted?: boolean
  error?: string
}

export interface BundleInfo {
  schema: number
  ref: string
  workspace: string
  display_name?: string
  source_install?: string
  miabi_version?: string
  encrypted: boolean
  bucket?: string
  prefix?: string
  apps: number
  databases: number
  volumes: number
  secrets: number
  routes: number
  certificates: number
  pipelines: number
  gitops_sources: number
  artifacts: BundleArtifact[]
  created_at: string
}

export interface BundleReportItem {
  kind: string
  name: string
  action: 'captured' | 'created' | 'skipped' | 'failed'
  detail?: string
}

export interface BundleRun {
  id: number
  workspace_id: number
  target_workspace_id?: number
  kind: 'export' | 'restore'
  ref: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  phase?: string
  trigger?: string
  restore_data: boolean
  deploy_apps: boolean
  s3_bucket?: string
  s3_prefix?: string
  source_workspace?: string
  artifacts: number
  size_bytes: number
  report?: { items?: BundleReportItem[]; notes?: string[] }
  error?: string
  started_at?: string
  finished_at?: string
  created_at: string
}

export interface RestoreBundleInput {
  ref: string
  /** Restore into a new workspace of this name instead of the current one. */
  new_workspace?: string
  restore_data: boolean
  deploy_apps: boolean
}

const base = (workspaceId: number) => `/workspaces/${workspaceId}/portable-backup`

export const portableBackupApi = {
  status(workspaceId: number) {
    return api.get<ApiResponse<{ configured: boolean; encrypted: boolean; reason?: string }>>(`${base(workspaceId)}/status`)
  },
  runs(workspaceId: number) {
    return api.get<ApiResponse<BundleRun[]>>(`${base(workspaceId)}/runs`)
  },
  run(workspaceId: number, runId: number) {
    return api.get<ApiResponse<BundleRun>>(`${base(workspaceId)}/runs/${runId}`)
  },
  deleteRun(workspaceId: number, runId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`${base(workspaceId)}/runs/${runId}`)
  },
  bundles(workspaceId: number) {
    return api.get<ApiResponse<BundleInfo[]>>(`${base(workspaceId)}/bundles`)
  },
  bundle(workspaceId: number, ref: string) {
    return api.get<ApiResponse<BundleInfo>>(`${base(workspaceId)}/bundles/${encodeURIComponent(ref)}`)
  },
  deleteBundle(workspaceId: number, ref: string) {
    return api.delete<ApiResponse<{ message: string }>>(`${base(workspaceId)}/bundles/${encodeURIComponent(ref)}`)
  },
  export(workspaceId: number) {
    return api.post<ApiResponse<BundleRun>>(`${base(workspaceId)}/export`)
  },
  restore(workspaceId: number, input: RestoreBundleInput) {
    return api.post<ApiResponse<BundleRun>>(`${base(workspaceId)}/restore`, input)
  },
}
