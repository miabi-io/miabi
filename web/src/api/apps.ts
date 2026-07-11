import api from './client'
import type { ApiResponse, Application, AppOverview, AppPort, Deployment, DeploymentLogHistory, Release, AppEnvVar, AppDatabase, ConnectionInfo, DeployStrategy, RestartPolicy, ImagePullPolicy, BuildMethod, HealthcheckType, ResourceLimits, LiveStatus, HostMountPreset, ProcessList, RuntimeKind, ServiceUpdateConfig } from './types'

// Cluster runtime fields shared by create/update payloads (cluster mode).
export interface AppRuntimeInput {
  runtime_kind?: RuntimeKind
  replicas?: number
  placement_constraints?: string[]
  update_config?: ServiceUpdateConfig
}

// Resource + healthcheck fields shared by create/update payloads.
export interface AppResourceInput {
  memory_bytes?: number
  nano_cpus?: number
  // GPU request (gated by the plan's allow_gpu capability).
  gpu_count?: number
  gpu_kind?: string
  restart_policy?: RestartPolicy
  image_pull_policy?: ImagePullPolicy
  healthcheck_type?: HealthcheckType
  healthcheck_http_path?: string
  healthcheck_port?: number
  healthcheck_command?: string
  healthcheck_interval_seconds?: number
  healthcheck_timeout_seconds?: number
  healthcheck_retries?: number
  healthcheck_start_period_seconds?: number
}

export interface CreateAppInput extends AppRuntimeInput {
  // Free-text label; the backend derives the unique slug handle from it.
  display_name: string
  // Optional explicit slug handle (derived from display_name when omitted).
  name?: string
  server_id?: number
  source_type?: 'image' | 'git'
  image?: string
  tag?: string
  git_repo?: string
  git_ref?: string
  build_method?: BuildMethod
  builder?: string
  buildpacks?: string[]
  build_env?: Record<string, string>
  registry_id?: number | null
  git_repository_id?: number | null
  stack_id?: number | null
  network_ids?: number[]
  ports?: AppPort[]
  port?: number
  command?: string[]
}

export interface UpdateAppInput extends AppResourceInput, AppRuntimeInput {
  name?: string
  image?: string
  tag?: string
  git_repo?: string
  git_ref?: string
  build_method?: BuildMethod
  builder?: string
  buildpacks?: string[]
  build_env?: Record<string, string>
  registry_id?: number | null
  git_repository_id?: number | null
  stack_id?: number | null
  network_ids?: number[]
  ports?: AppPort[]
  port?: number
  command?: string[]
  deploy_strategy?: DeployStrategy
  canary_initial_weight?: number
  canary_step_weight?: number
  canary_step_interval_seconds?: number
}

export interface DeployOptions {
  registry_id?: number | null
  tag?: string
  strategy?: DeployStrategy
}

export interface ExternalPort {
  port: number
  host: string
  url: string
}

export interface ExternalAccess {
  enabled: boolean // platform base domain configured
  base_domain: string
  label: string
  ports: ExternalPort[] // currently exposed
}

export const appApi = {
  list(ws: number) {
    return api.get<ApiResponse<Application[]>>(`/workspaces/${ws}/apps`)
  },
  resourceLimits(ws: number) {
    return api.get<ApiResponse<ResourceLimits>>(`/workspaces/${ws}/resource-limits`)
  },
  get(ws: number, id: number) {
    return api.get<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}`)
  },
  overview(ws: number, id: number) {
    return api.get<ApiResponse<AppOverview>>(`/workspaces/${ws}/apps/${id}/overview`)
  },
  status(ws: number, id: number) {
    return api.get<ApiResponse<LiveStatus>>(`/workspaces/${ws}/apps/${id}/status`)
  },
  externalAccess(ws: number, id: number) {
    return api.get<ApiResponse<ExternalAccess>>(`/workspaces/${ws}/apps/${id}/external-access`)
  },
  setExternalAccess(ws: number, id: number, ports: number[]) {
    return api.put<ApiResponse<ExternalAccess>>(`/workspaces/${ws}/apps/${id}/external-access`, { ports })
  },
  create(ws: number, input: CreateAppInput) {
    return api.post<ApiResponse<Application>>(`/workspaces/${ws}/apps`, input)
  },
  update(ws: number, id: number, input: UpdateAppInput) {
    return api.patch<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}`, input)
  },
  remove(ws: number, id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}`)
  },
  deploy(ws: number, id: number, opts: DeployOptions = {}) {
    return api.post<ApiResponse<Deployment>>(`/workspaces/${ws}/apps/${id}/deploy`, {
      registry_id: opts.registry_id ?? null,
      tag: opts.tag ?? '',
      strategy: opts.strategy ?? '',
    })
  },
  rollback(ws: number, id: number, releaseId: number) {
    return api.post<ApiResponse<Deployment>>(`/workspaces/${ws}/apps/${id}/rollback`, { release_id: releaseId })
  },
  // --- Canary deployment ---
  startCanary(ws: number, id: number, opts: DeployOptions = {}) {
    return api.post<ApiResponse<Deployment>>(`/workspaces/${ws}/apps/${id}/canary`, {
      registry_id: opts.registry_id ?? null,
      tag: opts.tag ?? '',
    })
  },
  setCanaryWeight(ws: number, id: number, weight: number) {
    return api.patch<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/canary`, { weight })
  },
  promoteCanary(ws: number, id: number) {
    return api.post<ApiResponse<Deployment>>(`/workspaces/${ws}/apps/${id}/canary/promote`)
  },
  abortCanary(ws: number, id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/canary`)
  },
  // start/restart return a Deployment when they apply pending changes (redeploy),
  // otherwise a message.
  start(ws: number, id: number) {
    return api.post<ApiResponse<Deployment | { message: string }>>(`/workspaces/${ws}/apps/${id}/start`)
  },
  stop(ws: number, id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/stop`)
  },
  restart(ws: number, id: number) {
    return api.post<ApiResponse<Deployment | { message: string }>>(`/workspaces/${ws}/apps/${id}/restart`)
  },
  // Scale a cluster (service) app to the given replica count (applied live).
  scale(ws: number, id: number, replicas: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/scale`, { replicas })
  },
  deployments(ws: number, id: number) {
    return api.get<ApiResponse<Deployment[]>>(`/workspaces/${ws}/apps/${id}/deployments`)
  },
  // Full stored logs of a (usually finished) deployment — load-once, no SSE.
  deploymentLogsHistory(ws: number, id: number, deploymentId: number) {
    return api.get<ApiResponse<DeploymentLogHistory>>(`/workspaces/${ws}/apps/${id}/deployments/${deploymentId}/logs/history`)
  },
  // Running processes in the active container (docker top). Read-only; poll for live.
  processes(ws: number, id: number, args = 'aux') {
    return api.get<ApiResponse<ProcessList>>(`/workspaces/${ws}/apps/${id}/processes?args=${encodeURIComponent(args)}`)
  },
  releases(ws: number, id: number) {
    return api.get<ApiResponse<Release[]>>(`/workspaces/${ws}/apps/${id}/releases`)
  },
  pinRelease(ws: number, id: number, releaseId: number, pinned: boolean) {
    return api.patch<ApiResponse<Release>>(`/workspaces/${ws}/apps/${id}/releases/${releaseId}`, { pinned })
  },
  deleteRelease(ws: number, id: number, releaseId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/releases/${releaseId}`)
  },
  envVars(ws: number, id: number) {
    return api.get<ApiResponse<AppEnvVar[]>>(`/workspaces/${ws}/apps/${id}/env`)
  },
  setEnvVar(ws: number, id: number, key: string, value: string, isSecret: boolean) {
    return api.put<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/env`, { key, value, is_secret: isSecret })
  },
  importEnvVars(ws: number, id: number, content: string, isSecret: boolean) {
    return api.post<ApiResponse<{ imported: number; redeploying: boolean }>>(`/workspaces/${ws}/apps/${id}/env/import`, { content, is_secret: isSecret })
  },
  deleteEnvVar(ws: number, id: number, key: string) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/env/${encodeURIComponent(key)}`)
  },
  // Reveal a secret env var's decrypted value (admin only, audited).
  revealEnvVar(ws: number, id: number, key: string) {
    return api.get<ApiResponse<{ key: string; value: string }>>(`/workspaces/${ws}/apps/${id}/env/${encodeURIComponent(key)}/reveal`)
  },
  // Custom container labels (Traefik &c.). Gated by the AllowCustomLabels plan
  // capability + global kill-switch; reserved keys (io.miabi.*, com.docker.*)
  // are rejected. Changes apply on the next deploy.
  labels(ws: number, id: number) {
    return api.get<ApiResponse<Record<string, string>>>(`/workspaces/${ws}/apps/${id}/labels`)
  },
  setLabels(ws: number, id: number, labels: Record<string, string>) {
    return api.put<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/labels`, { labels })
  },
  attachVolume(ws: number, id: number, volumeId: number, path: string) {
    return api.put<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/volumes`, { volume_id: volumeId, path })
  },
  detachVolume(ws: number, id: number, volumeId: number) {
    return api.delete<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/volumes/${volumeId}`)
  },
  hostMountPresets(ws: number) {
    return api.get<ApiResponse<HostMountPreset[]>>(`/workspaces/${ws}/host-mount-presets`)
  },
  attachHostMount(ws: number, id: number, preset: string, path: string, readOnly: boolean) {
    return api.put<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/host-mounts`, { preset, path, read_only: readOnly })
  },
  detachHostMount(ws: number, id: number, preset: string) {
    return api.delete<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/host-mounts/${preset}`)
  },
  databases(ws: number, id: number) {
    return api.get<ApiResponse<AppDatabase[]>>(`/workspaces/${ws}/apps/${id}/databases`)
  },
  attachDatabase(ws: number, id: number, dbId: number, envPrefix = '') {
    return api.put<ApiResponse<{ database: AppDatabase; env_injected: boolean }>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}`, { env_prefix: envPrefix })
  },
  detachDatabase(ws: number, id: number, dbId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}`)
  },
  databaseConnection(ws: number, id: number, dbId: number) {
    return api.get<ApiResponse<ConnectionInfo>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}/connection`)
  },
}
