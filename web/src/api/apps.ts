import api from './client'
import type { ApiResponse, Application, AppOverview, AppPort, Deployment, DeploymentLogHistory, Release, AppEnvVar, AppDatabase, ConnectionInfo, DeployStrategy, RestartPolicy, ImagePullPolicy, ReconcilePolicy, BuildMethod, HealthcheckType, ResourceLimits, LiveStatus, HostMountPreset, ProcessList, RuntimeKind, ServiceUpdateConfig, PipelineRun, PipelineDefinition, CanaryRoutingPayload, CanaryRoutingResult, CanaryPreview, ChangeRuntimeResult } from './types'

export interface PipelineRunAccepted {
  kind: 'pipeline_run'
  run: PipelineRun
}

/** Narrow a deploy response to the pipeline-run case. */
export function isPipelineRun(d: Deployment | PipelineRunAccepted): d is PipelineRunAccepted {
  return (d as PipelineRunAccepted)?.kind === 'pipeline_run'
}

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
  // Empty keeps the image's user; the restricted profile requires a non-root numeric uid.
  run_as_user?: string
  /** Extra Linux capabilities and host devices. An empty array revokes. */
  add_capabilities?: string[]
  devices?: string[]
  /** Hardening on top of the workspace's security profile; ALL drops every capability. */
  read_only_root_filesystem?: boolean
  no_new_privileges?: boolean
  drop_capabilities?: string[]
  restart_policy?: RestartPolicy
  image_pull_policy?: ImagePullPolicy
  /** Omitted leaves it unchanged. */
  reconcile_policy?: ReconcilePolicy
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
  // Location is where the app runs ('' = the workspace default); server_id pins a node, admins only.
  location?: string
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
  /** A repository that turns out to carry none still creates the app. */
  use_pipeline?: boolean
  registry_id?: number | null
  git_repository_id?: number | null
  stack_id?: number | null
  network_ids?: number[]
  ports?: AppPort[]
  port?: number
  command?: string[]
}

export interface UpdateAppInput extends AppResourceInput, AppRuntimeInput {
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
  /** Rebuild every layer for this deploy only (git-source apps). */
  no_cache?: boolean
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

/** A whole-source replacement: switching image/git changes which fields mean anything, so the server clears them. */
export interface SetSourceInput {
  source_type: 'image' | 'git'
  // Image source (required when source_type is "image").
  image?: string
  tag?: string
  registry_id?: number | null
  // Git source (one of git_repo / git_repository_id required when source_type is "git").
  git_repo?: string
  git_ref?: string
  git_repository_id?: number | null
  build_method?: BuildMethod
  builder?: string
  buildpacks?: string[]
  build_env?: Record<string, string>
}

/** What else moved when the source changed — none of it is visible in the app record afterwards. */
export interface SourceChange {
  from: 'image' | 'git'
  to: 'image' | 'git'
  /** false when only the details changed (a new tag, a different branch). */
  switched: boolean
  /** a repo-owned pipeline was dropped because the app left git */
  pipeline_removed: boolean
  /** the running container no longer matches the source */
  redeploy_required: boolean
}

export interface SetSourceResult {
  application: Application
  change: SourceChange
}

/** changed is false when the repository's document already matched — a successful no-op. */
export interface ResyncPipelineResult {
  pipeline: PipelineDefinition | null
  changed: boolean
  adopted: boolean
}

// Absent fields read as empty at the gateway.
export interface CanaryPreviewInput {
  headers?: Record<string, string>
  query?: Record<string, string>
  cookies?: Record<string, string>
  ip?: string
}

export const appApi = {
  list(ws: number) {
    return api.get<ApiResponse<Application[]>>(`/workspaces/${ws}/apps`)
  },
  resourceLimits(ws: number) {
    return api.get<ApiResponse<ResourceLimits>>(`/workspaces/${ws}/resource-limits`)
  },
  get(ws: number, id: number | string) {
    return api.get<ApiResponse<Application>>(`/workspaces/${ws}/apps/${encodeURIComponent(id)}`)
  },
  // Returns the raw YAML bundle, not the envelope, since it is meant to be saved to a file.
  manifest(ws: number, id: number) {
    return api.get<string>(`/workspaces/${ws}/apps/${id}/manifest`, { responseType: 'text' })
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
  setSource(ws: number, id: number, input: SetSourceInput) {
    return api.put<ApiResponse<SetSourceResult>>(`/workspaces/${ws}/apps/${id}/source`, input)
  },
  /** Adopts a pipeline when the app has none, else re-syncs the stored spec. */
  resyncPipeline(ws: number, id: number) {
    return api.post<ApiResponse<ResyncPipelineResult>>(`/workspaces/${ws}/apps/${id}/pipeline/resync`)
  },
  remove(ws: number, id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}`)
  },
  /** Returns a Deployment or a PipelineRunAccepted; use `isPipelineRun` to tell them apart. */
  deploy(ws: number, id: number, opts: DeployOptions = {}) {
    return api.post<ApiResponse<Deployment | PipelineRunAccepted>>(`/workspaces/${ws}/apps/${id}/deploy`, {
      registry_id: opts.registry_id ?? null,
      tag: opts.tag ?? '',
      strategy: opts.strategy ?? '',
      no_cache: opts.no_cache ?? false,
    })
  },
  /** Name a new build cache generation: the next build rebuilds every layer and repopulates it. */
  invalidateBuildCache(ws: number, id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/build-cache/invalidate`)
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
  pauseCanary(ws: number, id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/canary/pause`)
  },
  resumeCanary(ws: number, id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/canary/resume`)
  },
  promoteCanary(ws: number, id: number) {
    return api.post<ApiResponse<Deployment>>(`/workspaces/${ws}/apps/${id}/canary/promote`)
  },
  abortCanary(ws: number, id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/canary`)
  },
  setCanaryRouting(ws: number, id: number, payload: CanaryRoutingPayload) {
    return api.put<ApiResponse<CanaryRoutingResult>>(`/workspaces/${ws}/apps/${id}/canary/routing`, payload)
  },
  // Side-effect free, so a rule set can be understood before production is pointed at it.
  previewCanaryRouting(ws: number, id: number, req: CanaryPreviewInput) {
    return api.post<ApiResponse<CanaryPreview>>(`/workspaces/${ws}/apps/${id}/canary/preview`, req)
  },
  // Return a Deployment when they apply pending changes, otherwise a message.
  start(ws: number, id: number) {
    return api.post<ApiResponse<Deployment | { message: string }>>(`/workspaces/${ws}/apps/${id}/start`)
  },
  stop(ws: number, id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/stop`)
  },
  restart(ws: number, id: number) {
    return api.post<ApiResponse<Deployment | { message: string }>>(`/workspaces/${ws}/apps/${id}/restart`)
  },
  // Switch between the container and service runtimes; dry_run only reports the preflight.
  changeRuntime(ws: number, id: number, body: { runtime: RuntimeKind; replicas?: number; dry_run?: boolean }) {
    return api.post<ApiResponse<ChangeRuntimeResult>>(`/workspaces/${ws}/apps/${id}/runtime`, body)
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
  processes(ws: number, id: number) {
    return api.get<ApiResponse<ProcessList>>(`/workspaces/${ws}/apps/${id}/processes`)
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
  // Reserved keys (io.miabi.*, com.docker.*) are rejected; changes apply on the next deploy.
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
  // key selects one file from the config; omit it to project every file under path.
  attachConfig(ws: number, id: number, configId: number, path: string, key = '', mode = '') {
    return api.put<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/configs`, {
      config_id: configId, key, path, mode,
    })
  },
  detachConfig(ws: number, id: number, configId: number, key = '') {
    return api.delete<ApiResponse<Application>>(`/workspaces/${ws}/apps/${id}/configs/${configId}`, {
      params: key ? { key } : undefined,
    })
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
  attachDatabase(ws: number, id: number, dbId: number, envPrefix = '', envMap?: Record<string, string>) {
    return api.put<ApiResponse<{ database: AppDatabase; env_injected: boolean; env_vars: string[] }>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}`, { env_prefix: envPrefix, env_map: envMap })
  },
  linkDatabaseInstance(ws: number, id: number, instId: number, envPrefix = '', envMap?: Record<string, string>) {
    return api.put<ApiResponse<{ env_injected: boolean; env_vars: string[] }>>(`/workspaces/${ws}/apps/${id}/database-instances/${instId}`, { env_prefix: envPrefix, env_map: envMap })
  },
  unlinkDatabaseInstance(ws: number, id: number, instId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/database-instances/${instId}`)
  },
  databaseInstanceConnection(ws: number, id: number, instId: number) {
    return api.get<ApiResponse<ConnectionInfo>>(`/workspaces/${ws}/apps/${id}/database-instances/${instId}/connection`)
  },
  detachDatabase(ws: number, id: number, dbId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}`)
  },
  databaseConnection(ws: number, id: number, dbId: number) {
    return api.get<ApiResponse<ConnectionInfo>>(`/workspaces/${ws}/apps/${id}/databases/${dbId}/connection`)
  },
}
