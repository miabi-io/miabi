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
   * `enabled` and `host` are read-only: they come from MIABI_REGISTRY_ENABLED /
   * MIABI_REGISTRY_HOST and take a restart to change. Always true — the flag
   * exists so the UI explains the fixed fields rather than offering inputs the
   * server discards.
   */
  host_locked: boolean
  /** Where the effective host came from. */
  host_source: 'env' | 'stored' | 'base_domain' | 'unset'
  /**
   * The storage driver and its S3 settings are read-only: they come from
   * MIABI_REGISTRY_STORAGE / MIABI_REGISTRY_S3_* and take a restart to change.
   * Always true, for the same reason as `host_locked`.
   */
  storage_locked: boolean
  /** Where the effective storage driver came from. */
  storage_source: 'env' | 'stored' | 'default'
  /**
   * Why the configured storage cannot be used (unlicensed S3 driver, missing
   * bucket) — absent when it is usable. When present the registry does not start.
   */
  storage_error?: string
}

/**
 * Only the two runtime-mutable settings. Enablement, the host, and the whole
 * storage configuration are environment-only — see `host_locked` /
 * `storage_locked` on RegistrySettings.
 */
export interface RegistrySettingsPayload {
  delete_enabled: boolean
  per_workspace_quota_mb: number
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
