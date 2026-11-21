import api from './client'
import type { ApiResponse, PageableResponse } from './types'

// RegistrySettings is the platform's built-in Docker registry config (admin).
export interface RegistrySettings {
  id: number
  enabled: boolean
  host?: string
  storage_type: 'filesystem' | 's3'
  volume_name?: string
  /** Storage class the local volume lives on; absent means Docker's data root. */
  storage_class?: string
  s3_endpoint?: string
  s3_bucket?: string
  s3_region?: string
  s3_access_key?: string
  s3_force_path_style: boolean
  delete_enabled: boolean
  per_workspace_quota_mb: number
  s3_secret_set: boolean
  effective_host: string
  /** Whether S3/MinIO storage is licensed (Enterprise); local storage is free. */
  s3_entitled: boolean
  /** The server ignores a locked field on save, so the UI renders it fixed and names the owning variable. */
  locks: RegistryLocks
  /** Where the effective host came from. */
  host_source: 'env' | 'stored' | 'base_domain' | 'unset'
  /** Where the effective storage driver came from. */
  storage_source: 'env' | 'stored' | 'default'
  /** Why the configured storage is unusable; when present the registry does not start. */
  storage_error?: string
}

export interface RegistryLocks {
  enabled: boolean
  host: boolean
  storage: boolean
  storage_class: boolean
  s3_endpoint: boolean
  s3_bucket: boolean
  s3_region: boolean
  s3_access_key: boolean
  s3_secret_key: boolean
  s3_force_path_style: boolean
}

/** Omitted platform fields are left alone so one tab's save never clobbers another's; env-pinned fields are ignored. */
export interface RegistrySettingsPayload {
  delete_enabled: boolean
  per_workspace_quota_mb: number
  enabled?: boolean
  host?: string
  storage_type?: 'filesystem' | 's3'
  /** Empty moves the local volume back to Docker's data root. */
  storage_class?: string
  s3_endpoint?: string
  s3_bucket?: string
  s3_region?: string
  s3_access_key?: string
  /** Blank keeps the stored secret — the API never returns it to re-send. */
  s3_secret_key?: string
  s3_force_path_style?: boolean
  /** Acknowledges a change that strands data or breaks image references; without it the server answers 409. */
  confirm?: boolean
}

/** Live state and resource usage of the registry container. */
export interface RegistryRuntime {
  running: boolean
  state?: string
  status?: string
  health?: string
  restart_count?: number
  started_at?: string
  image?: string
  stats?: {
    cpu_percent: number
    memory_usage_bytes: number
    memory_limit_bytes: number
    memory_percent: number
    network_rx_bytes: number
    network_tx_bytes: number
  }
  /** Set when a sample could not be taken — show "unavailable", not a fake 0%. */
  stats_error?: string
}

// RegistryInfo is the per-workspace docker-login guidance.
export interface RegistryInfo {
  enabled: boolean
  host: string
  namespace: string
  image_prefix: string
  login_example: string
  /** The platform's registry delete switch. False ⇒ tag deletion always fails. */
  delete_enabled: boolean
}

/** A repository in the browse list: full tag count, preview of the newest tags. */
export interface RegistryRepository {
  name: string
  tag_count: number
  /** Preview only — the newest few. Ordered 'latest' first, then newest version. */
  tags: string[]
}

/** One tag, enriched with what the platform knows about it. */
export interface RegistryTag {
  name: string
  digest?: string
  size_bytes?: number
  /** Held by a live deployment or pinned release — deletion is refused. */
  in_use: boolean
  /** Provenance; absent for images pushed by hand rather than built by a pipeline. */
  built_at?: string
  commit?: string
  application_id?: number
  pipeline_run_id?: number
}

export interface RegistryRepositoryOverview {
  name: string
  tag_count: number
  tags: string[]
  latest_tag?: RegistryTag
}

export type TagDeleteResult = 'deleted' | 'would_delete' | 'in_use' | 'not_found' | 'failed'

export interface TagDeleteOutcome {
  tag: string
  result: TagDeleteResult
  /** Tags on the same image, which the registry removes with this one. */
  siblings?: string[]
  error?: string
}

export interface TagDeleteResponse {
  dry_run: boolean
  tags: TagDeleteOutcome[]
  /** Every tag gone afterwards (or that would go), siblings included. */
  removed: string[]
  /** Freed once garbage collection runs; layers other images still use are not counted. */
  freed_bytes: number
}

export interface RegistryRepoUsage {
  name: string
  bytes: number
  /** Referenced by no other repository in the workspace: what deleting this one frees. */
  exclusive_bytes: number
  tags: number
}

export interface RegistryUsage {
  total_bytes: number
  /** 0 = no quota. */
  quota_bytes: number
  repositories: RegistryRepoUsage[]
  computed_at?: string
  /** A refresh is running; the numbers are from computed_at. */
  stale: boolean
}

export interface AdminRegistryUsage {
  quota_bytes: number
  workspaces: {
    workspace_id: number
    workspace: string
    total_bytes: number
    repositories: number
    /** Being computed for the first time. */
    pending: boolean
    computed_at?: string
  }[]
}

export const registryApi = {
  // Admin
  getSettings: () => api.get<ApiResponse<RegistrySettings>>('/admin/registry/settings'),
  updateSettings: (payload: RegistrySettingsPayload) =>
    api.put<ApiResponse<RegistrySettings>>('/admin/registry/settings', payload),
  runtime: () => api.get<ApiResponse<RegistryRuntime>>('/admin/registry/runtime'),
  runGc: () => api.post<ApiResponse<{ message: string }>>('/admin/registry/gc'),
  adminUsage: () => api.get<ApiResponse<AdminRegistryUsage>>('/admin/registry/usage'),

  // Workspace
  info: (workspaceId: number) =>
    api.get<ApiResponse<RegistryInfo>>(`/workspaces/${workspaceId}/registry`),
  /** One page of repositories, each with its tag count and a short tag preview. */
  repositories: (workspaceId: number, page = 0, size = 20, q = '', tagLimit = 4) =>
    api.get<PageableResponse<RegistryRepository>>(`/workspaces/${workspaceId}/registry/repositories`, {
      params: { page, size, q: q || undefined, tag_limit: tagLimit },
    }),
  /** The name travels as a query parameter because an image name may contain slashes. */
  repository: (workspaceId: number, repo: string) =>
    api.get<ApiResponse<RegistryRepositoryOverview>>(`/workspaces/${workspaceId}/registry/repository`, {
      params: { name: repo },
    }),
  /** One page of a repository's tags, newest first. */
  tags: (workspaceId: number, repo: string, page = 0, size = 20, q = '') =>
    api.get<PageableResponse<RegistryTag>>(`/workspaces/${workspaceId}/registry/repository/tags`, {
      params: { name: repo, page, size, q: q || undefined },
    }),
  /** One call for one tag or many; dry_run previews what goes, siblings included. */
  deleteTags: (workspaceId: number, repo: string, tags: string[], dryRun: boolean) =>
    api.post<ApiResponse<TagDeleteResponse>>(`/workspaces/${workspaceId}/registry/repository/tags/delete`, {
      repository: repo, tags, dry_run: dryRun,
    }),
  usage: (workspaceId: number) =>
    api.get<ApiResponse<RegistryUsage>>(`/workspaces/${workspaceId}/registry/usage`),
}
