import api, { sseUrl } from './client'
import type { ApiResponse } from './types'

// TemplateInput is an install-wizard question.
export interface TemplateInput {
  key: string
  label?: string
  help?: string
  type?: 'string' | 'password' | 'bool' | 'select' | 'number'
  default?: string
  placeholder?: string
  pattern?: string
  options?: string[]
  required?: boolean
  generate?: boolean
  length?: number
}

// TemplateAuthor credits whoever packaged the template (distinct from the
// upstream project's homepage).
export interface TemplateAuthor {
  name: string
  email?: string
  website?: string
}

// CatalogEntry is the listing view of a template (latest version). `name` is the
// stable template handle (formerly `slug`); `display_name` is the catalog label.
export interface CatalogEntry {
  name: string
  display_name: string
  description: string
  category: string
  icon?: string
  tags?: string[]
  homepage?: string
  author?: TemplateAuthor
  source: 'official' | 'custom' | 'community' | string
  version: string
  versions: string[]
  applications: number
  databases: number
  volumes: number
  configs: number
  db_only: boolean
  inputs?: TemplateInput[]
}

export interface ManifestDatabase {
  name: string
  engine: 'postgres' | 'mysql' | 'mariadb' | 'redis' | string
  version?: string
  placement?: 'auto' | 'dedicated' | 'shared'
}

// ManifestConfig is a set of configuration files a template ships and mounts
// into its applications. Content is rendered at install time.
export interface ManifestConfig {
  name: string
  files: Record<string, string>
  mode?: string
  sensitive?: boolean
  delimiters?: string[]
}

export interface TemplateManifest {
  metadata: {
    name: string
    displayName: string
    version: string
    description?: string
    category?: string
    icon?: string
    homepage?: string
    author?: TemplateAuthor
    tags?: string[]
  }
  inputs?: TemplateInput[]
  databases?: ManifestDatabase[]
  volumes?: { name: string }[]
  configs?: ManifestConfig[]
  applications?: { name: string; image: string; tag?: string }[]
}

export interface TemplateDetail {
  entry: CatalogEntry
  manifest: TemplateManifest | null
}

export interface InstallInput {
  name: string
  version?: string
  display_name?: string
  inputs?: Record<string, string>
  placements?: Record<string, number>
  placement_modes?: Record<string, string>
}

export interface InstallResult {
  template: string
  display_name: string
  version: string
  install_id?: number
  stack?: { id: number; name: string }
  apps?: { id: number; name: string }[]
  databases?: { id: number; name: string }[]
  volumes?: { id: number; name: string }[]
  configs?: { id: number; name: string }[]
}

// --- Async install progress (SSE) ---

export type InstallPhaseStatus = 'pending' | 'active' | 'done' | 'error'

// InstallPhase is one step of an install, rendered as a stepper row.
export interface InstallPhase {
  key: string
  label: string
  status: InstallPhaseStatus
}

export type InstallJobStatus = 'running' | 'succeeded' | 'failed'

// InstallJob is the live snapshot streamed while a template installs.
export interface InstallJob {
  id: string
  status: InstallJobStatus
  phases: InstallPhase[]
  message?: string
  result?: InstallResult
  error?: string
}

// InstallAppRef links an install to an application it created.
export interface InstallAppRef {
  id: number
  name: string
  display_name: string
  status: string
}

// TemplateInstallView is an install annotated with the apps it created and
// upgrade availability.
export interface TemplateInstallView {
  id: number
  workspace_id: number
  source: string
  template_name: string
  template_display_name: string
  version: string
  stack_id?: number
  apps?: InstallAppRef[]
  app_ids?: number[]
  database_ids?: number[]
  volume_ids?: number[]
  inputs?: Record<string, string>
  created_at: string
  latest_version: string
  update_available: boolean
}

// RemovedResource is one resource an uninstall attempted to delete; error is set
// (non-fatal) when removal failed.
export interface RemovedResource {
  kind: string
  name: string
  error?: string
}

// UninstallResult is the teardown follow-up: every resource touched and how many
// failed.
export interface UninstallResult {
  removed: RemovedResource[] | null
  failed: number
}

export interface UpgradeEnvChange {
  key: string
  kind: 'added' | 'removed' | 'changed'
  secret: boolean
  templated: boolean
}

export interface UpgradeAppChange {
  app_id: number
  name: string
  old_image?: string
  new_image?: string
  image_changed: boolean
  env: UpgradeEnvChange[]
  new_mounts: string[]
}

export interface UpgradeInput {
  key: string
  label?: string
  help?: string
  required?: boolean
}

export interface UpgradePlan {
  from_version: string
  to_version: string
  apps: UpgradeAppChange[]
  new_volumes: string[]
  new_configs: string[]
  new_databases: string[]
  added_apps: string[]
  removed_apps: string[]
  new_inputs: UpgradeInput[]
  warnings: string[]
}

export interface UpgradeApplyResult {
  from_version: string
  to_version: string
  apps_bumped: string[]
  env_applied: string[]
  new_volumes: string[]
  new_configs: string[]
  warnings: string[]
}

const w = (ws: number) => `/workspaces/${ws}`

export const marketplaceApi = {
  // Catalog (official + this workspace's custom imports).
  templates: (ws: number) => api.get<ApiResponse<CatalogEntry[]>>(`${w(ws)}/marketplace/templates`),
  template: (ws: number, slug: string, version = '') =>
    api.get<ApiResponse<TemplateDetail>>(`${w(ws)}/marketplace/templates/${slug}${version ? `?version=${encodeURIComponent(version)}` : ''}`),
  install: (ws: number, input: InstallInput) =>
    api.post<ApiResponse<InstallResult>>(`${w(ws)}/marketplace/install`, input),
  // Async install: start a job, then stream its progress over SSE.
  startInstall: (ws: number, input: InstallInput) =>
    api.post<ApiResponse<InstallJob>>(`${w(ws)}/marketplace/install/jobs`, input),
  installJob: (ws: number, id: string) =>
    api.get<ApiResponse<InstallJob>>(`${w(ws)}/marketplace/install/jobs/${id}`),
  installJobEventsUrl: (ws: number, id: string) =>
    sseUrl(`${w(ws)}/marketplace/install/jobs/${id}/events`),
  import: (ws: number, yaml: string) =>
    api.post<ApiResponse<CatalogEntry>>(`${w(ws)}/marketplace/templates/import`, { yaml }),
  // Custom-template editing (official templates are read-only).
  templateRaw: (ws: number, slug: string, version = '') =>
    api.get<ApiResponse<{ name: string; yaml: string }>>(
      `${w(ws)}/marketplace/templates/${slug}/raw${version ? `?version=${encodeURIComponent(version)}` : ''}`,
    ),
  updateTemplate: (ws: number, slug: string, yaml: string) =>
    api.put<ApiResponse<CatalogEntry>>(`${w(ws)}/marketplace/templates/${slug}`, { yaml }),
  deleteTemplate: (ws: number, slug: string) =>
    api.delete<ApiResponse<{ deleted: boolean }>>(`${w(ws)}/marketplace/templates/${slug}`),
  installs: (ws: number) => api.get<ApiResponse<TemplateInstallView[]>>(`${w(ws)}/marketplace/installs`),
  upgradePlan: (ws: number, id: number, version = '') =>
    api.get<ApiResponse<UpgradePlan>>(
      `${w(ws)}/marketplace/installs/${id}/upgrade/plan${version ? `?version=${encodeURIComponent(version)}` : ''}`,
    ),
  upgrade: (ws: number, id: number, version = '', inputs: Record<string, string> = {}) =>
    api.post<ApiResponse<UpgradeApplyResult>>(`${w(ws)}/marketplace/installs/${id}/upgrade`, { version, inputs }),
  uninstall: (ws: number, id: number) =>
    api.delete<ApiResponse<UninstallResult>>(`${w(ws)}/marketplace/installs/${id}`),
}
