import api from './client'
import type { ApiResponse, PageableResponse } from './types'

// RegistrySettings is the platform's built-in Docker registry config (admin).
export interface RegistrySettings {
  id: number
  enabled: boolean
  host?: string
  storage_type: 'filesystem' | 's3'
  volume_name?: string
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
  /**
   * Fields the environment pins. A locked field is read-only: the server ignores
   * it on save, so the UI renders it fixed and names the variable that owns it.
   * Everything unlocked is editable here.
   */
  locks: RegistryLocks
  /** Where the effective host came from. */
  host_source: 'env' | 'stored' | 'base_domain' | 'unset'
  /** Where the effective storage driver came from. */
  storage_source: 'env' | 'stored' | 'default'
  /**
   * Why the configured storage cannot be used (unlicensed S3 driver, missing
   * bucket) — absent when it is usable. When present the registry does not start.
   */
  storage_error?: string
}

export interface RegistryLocks {
  enabled: boolean
  host: boolean
  storage: boolean
  s3_endpoint: boolean
  s3_bucket: boolean
  s3_region: boolean
  s3_access_key: boolean
  s3_secret_key: boolean
  s3_force_path_style: boolean
}

/**
 * A settings update. The platform fields are optional: omitting one leaves it
 * alone, so a save from one tab never clobbers another's. An env-pinned field is
 * ignored by the server whatever is sent.
 */
export interface RegistrySettingsPayload {
  delete_enabled: boolean
  per_workspace_quota_mb: number
  enabled?: boolean
  host?: string
  storage_type?: 'filesystem' | 's3'
  s3_endpoint?: string
  s3_bucket?: string
  s3_region?: string
  s3_access_key?: string
  /** Blank keeps the stored secret — the API never returns it to re-send. */
  s3_secret_key?: string
  s3_force_path_style?: boolean
  /**
   * Acknowledges a change that strands data or breaks stored image references.
   * Without it the server answers 409 with the prompt to show.
   */
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

export const registryApi = {
  // Admin
  getSettings: () => api.get<ApiResponse<RegistrySettings>>('/admin/registry/settings'),
  updateSettings: (payload: RegistrySettingsPayload) =>
    api.put<ApiResponse<RegistrySettings>>('/admin/registry/settings', payload),
  runtime: () => api.get<ApiResponse<RegistryRuntime>>('/admin/registry/runtime'),
  runGc: () => api.post<ApiResponse<{ message: string }>>('/admin/registry/gc'),

  // Workspace
  info: (workspaceId: number) =>
    api.get<ApiResponse<RegistryInfo>>(`/workspaces/${workspaceId}/registry`),
  /** One page of repositories, each with its tag count and a short tag preview. */
  repositories: (workspaceId: number, page = 0, size = 20, q = '', tagLimit = 4) =>
    api.get<PageableResponse<RegistryRepository>>(`/workspaces/${workspaceId}/registry/repositories`, {
      params: { page, size, q: q || undefined, tag_limit: tagLimit },
    }),
  /**
   * A single repository's overview. The name travels as a query parameter, not a
   * path segment, because an image name may itself contain slashes.
   */
  repository: (workspaceId: number, repo: string) =>
    api.get<ApiResponse<RegistryRepositoryOverview>>(`/workspaces/${workspaceId}/registry/repository`, {
      params: { name: repo },
    }),
  /** One page of a repository's tags, newest first. */
  tags: (workspaceId: number, repo: string, page = 0, size = 20, q = '') =>
    api.get<PageableResponse<RegistryTag>>(`/workspaces/${workspaceId}/registry/repository/tags`, {
      params: { name: repo, page, size, q: q || undefined },
    }),
  deleteTag: (workspaceId: number, repo: string, tag: string) =>
    api.delete<ApiResponse<{ message: string }>>(
      `/workspaces/${workspaceId}/registry/repositories/${encodeURIComponent(repo)}/tags/${encodeURIComponent(tag)}`,
    ),
}
