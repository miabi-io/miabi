// Shared API types.

export interface ApiResponse<T> {
  success: boolean
  data: T
  error?: { status_code?: number; code: string; message: string; error?: string }
}

export interface AppInfo {
  name: string
  version: string
  commit_id: string
  openapi_docs: boolean
}

// Plan is an admin-defined per-workspace quota + capability template.
// Numeric limits: -1 = unlimited, 0 = none.
export interface Plan {
  id: number
  name: string
  description: string
  is_default: boolean
  is_active: boolean
  max_apps: number
  max_database_instances: number
  max_cron_jobs: number
  max_volumes: number
  max_networks: number
  max_api_keys: number
  max_members: number
  max_databases_per_instance: number
  max_cpu_cores: number
  max_memory_mb: number
  max_database_instance_size_mb: number
  max_storage_mb: number
  max_runners: number
  max_gpus: number
  allow_custom_tls: boolean
  allow_privileged_host_mounts: boolean
  allow_shell_exec: boolean
  allow_shared_storage: boolean
  allow_dns_providers: boolean
  allow_custom_labels: boolean
  allow_platform_runners: boolean
  allow_gpu: boolean
  security_profile: SecurityProfile
  // Let apps installed from an official marketplace template keep the image's own
  // default user even under the "restricted" security profile.
  allow_official_image_user: boolean
  created_at?: string
  updated_at?: string
}

// SecurityProfile hardens how a workspace's app/job containers run. "restricted"
// forces a non-root platform UID (OpenShift-style); "default" is unchanged.
export type SecurityProfile = 'default' | 'restricted'

export type PlanInput = Omit<Plan, 'id' | 'created_at' | 'updated_at'>

// Per-workspace overrides: each field null = inherit the plan.
export interface WorkspaceQuotaOverride {
  workspace_id?: number
  max_apps: number | null
  max_database_instances: number | null
  max_cron_jobs: number | null
  max_volumes: number | null
  max_networks: number | null
  max_api_keys: number | null
  max_members: number | null
  max_databases_per_instance: number | null
  max_cpu_cores: number | null
  max_memory_mb: number | null
  max_database_instance_size_mb: number | null
  max_storage_mb: number | null
  max_runners: number | null
  max_gpus: number | null
  allow_custom_tls: boolean | null
  allow_privileged_host_mounts: boolean | null
  allow_shell_exec: boolean | null
  allow_shared_storage: boolean | null
  allow_dns_providers: boolean | null
  allow_custom_labels: boolean | null
  allow_platform_runners: boolean | null
  allow_gpu: boolean | null
  security_profile: SecurityProfile | null
  allow_official_image_user: boolean | null
}

export interface ResourceUsage {
  used: number
  limit: number // -1 = unlimited
}

// The resolved effective limits (numbers + capability flags; no plan metadata).
export type WorkspaceLimits = Pick<Plan,
  | 'max_apps' | 'max_database_instances' | 'max_cron_jobs' | 'max_volumes' | 'max_networks'
  | 'max_api_keys' | 'max_members' | 'max_databases_per_instance' | 'max_cpu_cores' | 'max_memory_mb'
  | 'max_database_instance_size_mb' | 'max_storage_mb' | 'max_runners' | 'max_gpus' | 'allow_custom_tls' | 'allow_privileged_host_mounts'
  | 'allow_shell_exec' | 'allow_shared_storage' | 'allow_dns_providers' | 'allow_custom_labels' | 'allow_platform_runners' | 'allow_gpu' | 'security_profile'
  | 'allow_official_image_user'>

export interface WorkspaceUsage {
  enforced: boolean
  plan_name: string
  limits: WorkspaceLimits
  apps: ResourceUsage
  database_instances: ResourceUsage
  cron_jobs: ResourceUsage
  volumes: ResourceUsage
  networks: ResourceUsage
  api_keys: ResourceUsage
  members: ResourceUsage
  runners: ResourceUsage
  cpu_cores: ResourceUsage
  memory_mb: ResourceUsage
  storage_mb: ResourceUsage
  capabilities: {
    custom_tls: boolean
    privileged_host_mounts: boolean
    shell_exec: boolean
    shared_storage: boolean
    dns_providers: boolean
    custom_labels: boolean
    // Cluster (Swarm) mode is on — offer the replicated "service" runtime on
    // app create. A platform-level flag exposed to every workspace member here.
    cluster_enabled?: boolean
  }
}

// WorkspaceLiveSample is the actual, live consumption aggregated across a
// workspace's running app + database containers (from GET /usage/live and its SSE
// stream). Distinct from WorkspaceUsage, which reports declared quota vs limits.
export interface WorkspaceLiveSample {
  at: string
  containers: number       // running containers sampled
  cpu_percent: number      // summed; 100% == one core fully used
  cpu_cores: number        // cpu_percent / 100
  memory_bytes: number     // actual resident memory in use
  memory_limit_bytes: number
  net_rx_bytes: number
  net_tx_bytes: number
}

// WorkspaceHistoryPoint is one time-bucketed aggregate of the workspace's stored
// metric samples, summed across its apps (from GET /usage/history). Powers the
// dashboard sparkline.
export interface WorkspaceHistoryPoint {
  at: string
  cpu_percent: number
  cpu_cores: number
  memory_bytes: number
}

// DNSProviderType is a connectable managed DNS host.
export type DNSProviderType = 'cloudflare' | 'route53' | 'digitalocean'

// DNSProvider is a workspace's connection to a DNS host. Credentials are
// write-only and never returned.
export interface DNSProvider {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  type: DNSProviderType
  status: 'ok' | 'error'
  last_error?: string
  created_at?: string
  updated_at?: string
}

// DNSProviderCredentials carries the per-type credential fields (write-only).
export interface DNSProviderCredentials {
  api_token?: string // cloudflare, digitalocean
  access_key_id?: string // route53
  secret_access_key?: string // route53
  region?: string // route53
}

export interface ConnectDNSProviderInput {
  name: string
  type: DNSProviderType
  credentials: DNSProviderCredentials
  test_zone?: string
}

export interface Pageable {
  current_page: number
  size: number
  total_pages: number
  total_elements: number
  empty: boolean
}

export interface PageableResponse<T> {
  success: boolean
  data: T[]
  pageable: Pageable
}

export interface User {
  id: number
  name: string
  /** Unique handle (lowercase [a-z0-9-]); the directory-friendly identifier. */
  username?: string
  email: string
  role: string
  two_factor_enabled?: boolean
  // True once the user dismissed or completed the getting-started checklist.
  onboarding_dismissed?: boolean
  recovery_codes_remaining?: number
  // Populated on /me only: the credential behind the request. For a
  // workspace-bound API key, auth.workspace_id is the workspace the token manages.
  auth?: AuthContext
}

export interface AuthContext {
  method: 'jwt' | 'api_key'
  api_key_id?: number
  workspace_id?: number
  scopes?: string[]
}

// Session is one active sign-in for the current user. `current` flags the
// session the running browser is authenticated with.
export interface Session {
  id: number
  ip_address: string
  user_agent: string
  current: boolean
  created_at: string
  expires_at: string
}

export interface AuthResponse {
  token?: string
  user?: User
  two_factor_required?: boolean
  // Set when the credentials were valid but the account has an admin-set/reset
  // password it must replace: no session is issued, and reset_token is a
  // short-lived token exchanged (with a new password) for a real session.
  must_change_password?: boolean
  reset_token?: string
}

// LoginTokenResponse is the "Copy login command" payload: a short-lived personal
// API token plus the ready-to-paste CLI and curl commands (shown once).
// two_factor_required mirrors login when the account has 2FA.
export interface LoginTokenResponse {
  token?: string
  sha256?: string
  expires_at?: string
  scopes?: string[]
  server_url?: string
  login_command?: string
  curl_example?: string
  two_factor_required?: boolean
  // redirect_to is set only for the loopback CLI-login flow (`miabi login`): the
  // browser navigates here to hand a single-use code back to the CLI's local
  // callback. The raw token is omitted in that case.
  redirect_to?: string
}

export interface TwoFactorSetup {
  secret: string
  url: string
  qr_code: string
}

export interface RecoveryCodes {
  recovery_codes: string[]
}

export interface AuthStatus {
  password_reset_enabled: boolean
}

// --- Platform admin ---

export interface AdminUser {
  id: number
  name: string
  username?: string
  email: string
  role: 'admin' | 'user'
  active: boolean
  two_factor_enabled?: boolean
  email_verified_at?: string | null
  last_login_at?: string | null
  scheduled_deletion_at?: string | null
  created_at?: string
  // Enterprise per-user overrides (null = inherit the platform default; -1 = unlimited).
  workspace_limit?: number | null
  workspace_membership_limit?: number | null
}

export interface WorkspaceMemberSummary {
  user_id: number
  name: string
  email: string
  role: string
}

export interface WorkspaceSummary {
  id: number
  name: string
  privileged: boolean
  apps: number
  databases: number
  stacks: number
  members: WorkspaceMemberSummary[]
}

export interface OwnershipTransfer {
  workspace_id: number
  new_owner_id: number
}

export interface AdminUserDetail extends AdminUser {
  workspaces_owned: number
  workspaces_member: number
  apps_total: number
  apps_running: number
  apps_failed: number
  databases: number
  stacks: number
  owned_workspaces: WorkspaceSummary[]
  recent_events: AdminEvent[]
}

export interface AdminWorkspace {
  id: number
  /** Unique handle. */
  name: string
  /** Free-text label shown in the UI. */
  display_name?: string
  owner_id: number
  owner_name: string
  owner_email: string
  privileged: boolean
  apps_count: number
  databases_count: number
  stacks_count: number
  members_count: number
  created_at: string
}

export interface AdminWorkspaceMember {
  id: number
  workspace_id: number
  user_id: number
  role: WorkspaceRole
  created_at?: string
  user: User
}

export interface AdminWorkspaceDetail {
  id: number
  name: string
  display_name?: string
  description?: string
  owner_id: number
  privileged: boolean
  system: boolean
  plan_id?: number | null
  created_at: string
  owner_name: string
  owner_email: string
  apps_count: number
  databases_count: number
  stacks_count: number
  volumes_count: number
  networks_count: number
  members_count: number
  members: AdminWorkspaceMember[]
  recent_events: AdminEvent[]
}

export interface PlatformMetrics {
  total_users: number
  active_users: number
  admin_users: number
  total_workspaces: number
  total_applications: number
  total_databases: number
  total_stacks: number
  total_volumes: number
  active_sessions: number
  running_containers: number
  total_containers: number
  connected_workers: number
  workers: WorkerInfo[] | null
  shared_runners: number
  shared_runners_online: number
  workspace_runners: number
  workspace_runners_online: number
  storage_declared_bytes: number
  storage_used_bytes: number
  uptime_seconds: number
  goroutines: number
  memory_alloc_bytes: number
  version: string
  commit: string
  network_pool?: NetworkPoolStats | null
}

// NetworkPoolStats is the managed network subnet pool's utilization.
export interface NetworkPoolStats {
  used: number
  available: number
  total: number
}

export interface WorkerInfo {
  host: string
  pid: number
  type: 'embedded' | 'standalone'
  concurrency: number
  queues: Record<string, number>
  active_tasks: number
  status: string
  started: string
}

export interface AdminEvent {
  id: number
  actor_id?: number | null
  workspace_id?: number | null
  action: string
  target_type: string
  target_id: string
  ip_address: string
  metadata?: Record<string, unknown>
  created_at: string
}

export interface PlatformSetting {
  id: number
  key: string
  value: string
  type: 'string' | 'int' | 'bool' | 'json'
  updated_at?: string
}

// UpdateInfo is the cached result of the daily release check (GET /admin/update).
export interface UpdateInfo {
  current_version: string
  latest_version?: string
  release_url?: string
  published_at?: string
  /** False when up to date, when checks are disabled, or when this exact version was dismissed. */
  update_available: boolean
  enabled: boolean
  checked_at?: string
  /** Set when the last check failed — "cannot reach GitHub" must not read as "up to date". */
  last_error?: string
}

export interface JobStatus {
  kind?: string // backup | cronjob
  id: number
  name: string
  schedule: string
  running: boolean
  last_run_at?: string | null
  last_error?: string
  next_run_at?: string | null
}

// JobStats is the scheduled-jobs dashboard summary (computed over all jobs).
export interface JobStats {
  total: number
  running: number
  failed: number
  ok: number
  by_kind: Record<string, number>
}

export type OAuthProviderType = 'google' | 'oidc'

export interface OAuthProvider {
  id: number
  name: string
  display_name: string
  type: OAuthProviderType
  issuer?: string
  auth_url?: string
  token_url?: string
  userinfo_url?: string
  scopes?: string
  enabled: boolean
  hidden: boolean
  auto_register: boolean
  allowed_domains?: string
  email_claim?: string
  name_claim?: string
  default_workspace_id?: number | null
  default_role?: string
  created_at?: string
}

export interface PublicProvider {
  name: string
  display_name: string
  type: OAuthProviderType
}

// ProvidersResponse is the login screen's SSO payload: buttoned providers plus
// whether any hidden provider exists (reachable via "Continue with SSO").
export interface ProvidersResponse {
  providers: PublicProvider[]
  sso_available: boolean
}

// SSODiscovery names the provider resolved from an email's domain.
export interface SSODiscovery {
  name: string
  display_name: string
  type: OAuthProviderType
}

// --- SIEM audit streaming (Enterprise) ---

export type SIEMSink = 'syslog' | 'webhook'

export interface SIEMConfig {
  id: number
  name: string
  sink: SIEMSink
  endpoint: string
  format: 'json' | 'cef'
  enabled: boolean
  last_shipped_id: number
  last_error: string
  last_shipped_at?: string | null
  created_at?: string
  updated_at?: string
}

export type WorkspaceRole = 'owner' | 'admin' | 'developer' | 'viewer'

export interface Workspace {
  id: number
  /** The unique handle (URL/CLI/docker name). Lowercase [a-z0-9-]. */
  name: string
  /** Free-text label shown in the UI. Prefer this for display. */
  display_name?: string
  description?: string
  owner_id: number
  privileged?: boolean
  /** The built-in platform workspace, which cannot be renamed or deleted. */
  system?: boolean
  /** The requesting user's role in this workspace (from the list endpoint). */
  role?: WorkspaceRole
  created_at?: string
}

export interface Registry {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  server: string
  username: string
  has_secret: boolean
  created_at?: string
}

export type GitAuthType = 'public' | 'token' | 'ssh'

export interface GitRepository {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  url: string
  auth_type: GitAuthType
  username: string
  has_secret: boolean
  created_at?: string
}

export interface Webhook {
  id: number
  workspace_id: number
  name: string
  url: string
  events: string[]
  headers?: Record<string, string>
  enabled: boolean
  has_secret: boolean
  // secret is returned in cleartext only in the create response.
  secret?: string
  created_at?: string
  updated_at?: string
}

export type WebhookDeliveryStatus = 'success' | 'failed'

export interface WebhookDelivery {
  id: number
  webhook_id: number
  workspace_id: number
  event: string
  status: WebhookDeliveryStatus
  http_status_code: number
  error_message?: string
  attempt: number
  created_at: string
}

export type NotificationChannelType = 'telegram' | 'slack' | 'discord'

export interface NotificationChannel {
  id: number
  workspace_id: number
  type: NotificationChannelType
  name: string
  // config holds transport settings; secret values are masked by the API.
  config: Record<string, string>
  events: string[]
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export type AppEventSeverity = 'info' | 'warning' | 'error'

export interface AppEvent {
  id: number
  workspace_id: number
  application_id: number
  type: string
  severity: AppEventSeverity
  message: string
  metadata?: Record<string, string>
  actor_id?: number
  created_at: string
}

export interface AppPort {
  id?: number
  application_id?: number
  container_port: number
  protocol: 'tcp' | 'udp'
  scheme?: 'http' | 'https' // L7 protocol the container speaks (Gateway backend URL)
  name?: string
}

// NodePortUsage is one host port published on a node, with its owning container.
export interface NodePortUsage {
  host_port: number
  private_port: number
  protocol: string
  container: string
  container_id: string
  managed: boolean
}

export type PortBindingStatus = 'pending' | 'approved' | 'rejected'

export interface PortBinding {
  id: number
  workspace_id: number
  application_id: number
  container_port: number
  protocol: 'tcp' | 'udp'
  host_port: number
  status: PortBindingStatus
  server_id?: number
  managed?: boolean
  bind_ip?: string
  requested_by: number
  reviewed_by?: number
  review_note?: string
  created_at: string
}

export type AppStatus = 'created' | 'deploying' | 'running' | 'stopped' | 'failed'

// LiveStatus is the real-time container status (from Docker inspect), plus a
// stats snapshot when running. `status` is finer-grained than AppStatus.
export interface LiveStatus {
  status: string // running|restarting|unhealthy|starting|exited|stopped|paused|created|no_container
  container_state?: string
  health?: string
  running: boolean
  restarting: boolean
  restart_count: number
  exit_code: number
  started_at?: string
  uptime_seconds: number
  has_container: boolean
  stored_status: AppStatus
  stats?: StatsSample
  networks?: ContainerNetwork[]
  // Cluster (service) runtime: desired replicas and currently-running tasks.
  service_replicas?: number
  service_running_tasks?: number
}

// DBLiveStatus is the real-time status of a database instance (GET
// .../databases/{id}/status): the stored lifecycle status plus the container's
// live Docker state. Polled by the detail page so provisioning/health changes
// surface without a manual refresh.
export interface DBLiveStatus {
  status: string // stored lifecycle, or live-derived when running (running|unhealthy|starting|restarting|exited|stopped|...)
  stored_status: DBStatus
  container_state?: string
  health?: string
  running: boolean
  restarting: boolean
  restart_count: number
  exit_code: number
  started_at?: string
  uptime_seconds: number
  has_container: boolean
  upgrade?: UpgradeProgress
  stats?: StatsSample
}

// ContainerNetwork is the app container's attachment to a Docker network. IPs
// are ephemeral (change on redeploy) — prefer the stable hostname/alias.
export interface ContainerNetwork {
  name: string
  ip_address: string
  gateway?: string
}

export interface AppOverview {
  status: AppStatus
  source_type: 'image' | 'git'
  image?: string
  tag?: string
  git_repo?: string
  current_version: number
  current_image?: string
  hostname: string
  stack_hostname?: string
  redeploy_required: boolean
  volumes_count: number
  routes_count: number
  networks_count: number
  env_count: number
  created_at: string
  recent_events: AppEvent[]
}

export interface AppMount {
  volume_id: number
  docker_name: string
  path: string
  host_preset?: string
  read_only?: boolean
}

export interface HostMountPreset {
  key: string
  label: string
  description: string
  source: string
  default_target: string
  default_read_only: boolean
  allow_read_only: boolean
  danger: string
}

export interface NodePlacement {
  name: string
  tasks: number
}

export interface Application {
  id: number
  workspace_id: number
  server_id?: number
  server_name?: string
  // Real per-node replica placement for a cluster ("service") app, populated on
  // the app-detail read: where Swarm actually scheduled the running tasks.
  nodes?: NodePlacement[]
  display_name: string
  name: string
  alias?: string
  icon?: string
  source_type: 'image' | 'git'
  image: string
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
  stack?: Stack | null
  command?: string[]
  metadata?: Record<string, string>
  annotations?: Record<string, string>
  network_ids?: number[]
  networks?: Network[]
  ports?: AppPort[]
  port?: number
  status: AppStatus
  current_release_id?: number | null
  redeploy_required?: boolean
  deploy_strategy?: DeployStrategy
  canary_initial_weight?: number
  canary_step_weight?: number
  canary_step_interval_seconds?: number
  canary_release_id?: number | null
  canary_weight?: number
  memory_bytes?: number
  nano_cpus?: number
  // GPU request (gated by the plan's allow_gpu). gpu_count = whole devices,
  // gpu_kind narrows to a vendor/model.
  gpu_count?: number
  gpu_kind?: string
  restart_policy?: RestartPolicy
  image_pull_policy?: ImagePullPolicy
  // Cluster runtime (cluster mode). "service" runs the app as a replicated Swarm
  // service; otherwise a single container.
  runtime_kind?: RuntimeKind
  replicas?: number
  placement_constraints?: string[]
  update_config?: ServiceUpdateConfig
  healthcheck_type?: HealthcheckType
  healthcheck_http_path?: string
  healthcheck_port?: number
  healthcheck_command?: string
  healthcheck_interval_seconds?: number
  healthcheck_timeout_seconds?: number
  healthcheck_retries?: number
  healthcheck_start_period_seconds?: number
  mounts?: AppMount[]
  created_at?: string
}

// RuntimeKind is how an app runs: a single container (default) or a replicated
// Swarm service (cluster mode).
export type RuntimeKind = 'container' | 'service'

// ServiceUpdateConfig tunes the Swarm rolling update for a service app.
export interface ServiceUpdateConfig {
  parallelism?: number
  delay_seconds?: number
}

export type DeployStrategy = 'recreate' | 'rolling' | 'canary'

export type RestartPolicy = 'no' | 'always' | 'unless-stopped' | 'on-failure'
export type ImagePullPolicy = 'always' | 'if-not-present' | 'never'
// BuildMethod selects how a git app's image is built: auto (Dockerfile if
// present, else Cloud Native Buildpacks), or a forced dockerfile/buildpack.
export type BuildMethod = 'auto' | 'dockerfile' | 'buildpack'
export type HealthcheckType = 'none' | 'http' | 'command'

export interface ResourceLimits {
  max_cpu_cores: number
  max_memory_mb: number
}

export type DeploymentStatus = 'pending' | 'building' | 'deploying' | 'succeeded' | 'running' | 'failed'

// Full stored logs of a finished deployment (load-once, JSON — not the SSE tail).
export interface DeploymentLogHistory {
  status: string
  lines: string[]
  truncated: boolean
}

export interface Deployment {
  id: number
  // Per-application sequential deployment number (1, 2, 3…), independent of the
  // global id — shown to users as "#<number>". Mirrors Release.version.
  number: number
  application_id: number
  status: DeploymentStatus
  image: string
  trigger: string
  error?: string
  current?: boolean
  created_at: string
}

export interface Release {
  id: number
  version: number
  image: string
  container_id?: string
  active: boolean
  pinned: boolean
  created_at: string
}

export interface AppHealth {
  id: number
  name: string
  display_name: string
  status: AppStatus
  health: 'healthy' | 'unhealthy' | 'unknown'
  server_id: number
  server_name?: string
  created_at?: string
}

export interface RecentEvent extends AppEvent {
  app_name: string
  app_display_name: string
}

export interface Overview {
  apps: AppHealth[] | null
  total_apps: number
  running: number
  failed: number
  databases: number
  stacks: number
  recent_events: RecentEvent[] | null
}

export interface AppEnvVar {
  id: number
  key: string
  value: string
  is_secret: boolean
}

export interface Network {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  docker_name: string
  driver: string
  internal: boolean
  is_default: boolean
  created_at?: string
}

export interface StackStatusCounts {
  total: number
  running: number
  stopped: number
  failed: number
  other: number
}

export interface StackEnvVar {
  id: number
  stack_id: number
  key: string
  value: string
  is_secret: boolean
}

export interface Stack {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  docker_name: string
  description?: string
  apps?: Application[]
  app_count?: number
  status?: StackStatusCounts
  metadata?: Record<string, string>
  annotations?: Record<string, string>
  created_at?: string
}

export type RouteTLSMode = 'none' | 'acme' | 'custom'

// RouteStatus is the route's config-sync status with the gateway: live (served),
// offline (disabled or a host's domain is unverified), error (last sync failed),
// or pending (not yet synced).
export type RouteStatus = 'pending' | 'live' | 'offline' | 'error'

export interface Route {
  id: number
  workspace_id: number
  application_id: number
  name: string
  display_name?: string
  path: string
  hosts?: string[]
  methods?: string[]
  middlewares?: string[]
  rewrite?: string
  target_port: number
  tls_mode: RouteTLSMode
  certificate_id?: number | null
  advanced_config?: string
  enabled: boolean
  // Generated marks a platform-managed external-access route. It is created and
  // reconciled from the application's External Access card; the Routes UI shows it
  // read-only (no edit/toggle/delete).
  generated?: boolean
  // Config-sync status with the gateway (whether Goma is serving the route — not
  // upstream health). Set whenever the workspace proxy config is reconciled.
  status?: RouteStatus
  status_reason?: string
  synced_at?: string | null
  has_custom_cert: boolean
  dns_target?: string
  dns_hostname?: string
  // Actual upstream endpoints the gateway uses (alias, or a port-forward node's
  // address:hostPort). Populated on read.
  backends?: string[]
  created_at?: string
}

export interface Certificate {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  common_name: string
  dns_names?: string[]
  issuer: string
  not_before: string
  not_after: string
  serial_hex: string
  // Managed (ACME) fields. source is 'imported' (default) or 'acme'.
  source?: 'imported' | 'acme'
  dns_provider_id?: number | null
  auto_renew?: boolean
  status?: 'active' | 'issuing' | 'failed'
  last_error?: string
  created_at?: string
  updated_at?: string
}

export interface IssueCertificateInput {
  domain_id: number
  name?: string
  include_wildcard?: boolean
  auto_renew?: boolean
}

export interface Middleware {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  type: string
  paths?: string[]
  rule?: Record<string, unknown>
  created_at?: string
}

// --- Middleware catalog (curated security policies) ---

export type MiddlewareFieldType =
  | 'string'
  | 'int'
  | 'bool'
  | 'string[]'
  | 'int[]'
  | 'duration'
  | 'enum'
  | 'users'
  | 'map'
  | 'object'
  | 'list'

export interface MiddlewareField {
  key: string
  label: string
  type: MiddlewareFieldType
  required?: boolean
  secret?: boolean
  default?: unknown
  options?: string[]
  help?: string
  // Sub-schema for `list` rows and structured `object` groups.
  fields?: MiddlewareField[]
}

export interface MiddlewareDescriptor {
  type: string
  display_name: string
  description: string
  category: 'access' | 'security' | 'traffic' | 'transform' | 'observability'
  fields: MiddlewareField[]
}

export interface MiddlewarePreset {
  key: string
  display_name: string
  description: string
  type: string
  rule: Record<string, unknown>
}

export interface MiddlewareCatalog {
  types: MiddlewareDescriptor[]
  presets: MiddlewarePreset[]
}

export type DBEngine = 'postgres' | 'mysql' | 'mariadb' | 'redis' | 'mongodb' | 'libsql'

export type DBStatus = 'provisioning' | 'running' | 'stopped' | 'failed' | 'upgrading'

export interface EngineDefault {
  engine: DBEngine
  image: string
  version: string
}

export interface DatabaseInstance {
  id: number
  name: string
  server_id?: number
  server_name?: string
  display_name: string
  engine: DBEngine
  version: string
  status: DBStatus
  host: string
  port: number
  admin_user: string
  volume_name?: string
  volume_size_bytes?: number
  mount_path?: string
  size_bytes?: number
  size_synced_at?: string | null
  network_name?: string
  networks?: Network[]
  metadata?: Record<string, string>
  annotations?: Record<string, string>
  upgrade?: UpgradeProgress
}

export interface UpgradeProgress {
  from_version: string
  to_version: string
  path: 'in-place' | 'dump-restore'
  phase: string
  error?: string
}

export interface UpgradeOptions {
  current_version: string
  engine: DBEngine
  suggestions: string[]
  affected_app_ids: number[]
}

export interface UpgradePlan {
  from_version: string
  to_version: string
  path: 'in-place' | 'dump-restore'
  major: boolean
  affected_app_ids: number[]
}

export interface LogicalDatabase {
  id: number
  workspace_id: number
  instance_id: number
  name: string
  username: string
  status: DBStatus
  application_id?: number | null
  env_prefix?: string
  size_bytes?: number
  size_synced_at?: string | null
  created_at?: string
}

export interface AppDatabase extends LogicalDatabase {
  instance_name: string
  engine: DBEngine
  host: string
  port: number
}

export interface ConnectionInfo {
  host: string
  port: number
  username: string
  password: string
  database: string
  uri: string
}

export type JobRunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'

export interface Job {
  id: number
  workspace_id: number
  application_id: number
  server_id: number
  cronjob_id?: number
  app_name?: string
  name: string
  command: string[]
  entrypoint?: string[]
  image: string
  registry_id?: number
  pull?: boolean
  status: JobRunStatus
  exit_code?: number
  logs?: string
  error?: string
  timeout_secs: number
  source: string
  started_at?: string
  finished_at?: string
  created_at: string
}

export interface CronJob {
  id: number
  workspace_id: number
  application_id: number
  app_name?: string
  name: string
  schedule: string
  command: string[]
  entrypoint?: string[]
  image?: string
  registry_id?: number
  timeout_secs: number
  enabled: boolean
  concurrency_policy: 'allow' | 'forbid' | 'replace'
  history_limit: number
  last_run_at?: string
  created_at: string
}

export interface Secret {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  description?: string
  version: number
  managed?: boolean
  owner_kind?: string
  owner_id?: number
  created_at?: string
  updated_at?: string
}

export interface ForwardSession {
  id: string
  instance_id: number
  workspace_id: number
  host: string
  port: number
  created_at: string
  expires_at: string
}

export interface Volume {
  id: number
  name: string
  server_id?: number
  server_name?: string
  display_name: string
  docker_name: string
  mountpoint?: string
  size_bytes?: number
  // Measured on-disk usage (vs declared size_bytes); absent = never measured.
  used_bytes?: number
  used_measured_at?: string
  // Shared storage: driver is "local" (node-local, rwo), "nfs"/"cifs" (shared,
  // rwx — usable by a replicated cluster app across nodes), or "host" (a bind to
  // an operator-managed host path under /mnt/*, present on every node).
  driver?: string
  access_mode?: 'rwo' | 'rwx'
  // Bind source for a "host" driver volume (the /mnt/* path); empty otherwise.
  host_path?: string
  metadata?: Record<string, string>
  annotations?: Record<string, string>
  created_at?: string
}

export interface VolumeUsage {
  app_id: number
  app_name: string
  app_display_name: string
  path: string
}

// WorkspaceStorage is the declared-vs-measured storage summary (GET .../storage).
export interface WorkspaceStorage {
  declared_bytes: number // sum of declared volume sizes
  used_bytes: number // sum of measured on-disk usage
  limit_mb: number // effective plan MaxStorageMB (-1 = unlimited)
  measured_at?: string // oldest measurement across the workspace's volumes
  volume_count: number
}

export interface VolumeDetail extends Volume {
  driver?: string
  exists: boolean
  in_use: boolean
  used_by: VolumeUsage[]
}

export interface VolumeFile {
  path: string
  size: number
  mod_time: number
  is_dir: boolean
}

export interface Backup {
  id: number
  status: 'pending' | 'running' | 'completed' | 'failed'
  trigger: string
  destination: string
  filename?: string
  error?: string
  created_at: string
}

export interface BackupSchedule {
  id: number
  cron: string
  destination: string
  enabled: boolean
  max_backups?: number
  retention_days?: number
  last_run_at?: string | null
}

export interface VolumeBackup {
  id: number
  volume_id: number
  server_id: number
  volume_name: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  trigger: string
  s3_bucket?: string
  s3_path?: string
  filename?: string
  size_bytes: number
  error?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}


export interface MetricSample {
  recorded_at: string
  cpu_percent: number
  memory_bytes: number
  memory_percent: number
  net_rx_bytes: number
  net_tx_bytes: number
}

// ProcessList is the running processes in a container (the "docker top" view).
export interface ProcessList {
  titles: string[]
  processes: string[][]
}

// StatsSample is the live container stats snapshot (GET .../metrics).
export interface StatsSample {
  cpu_percent: number
  memory_usage_bytes: number
  memory_limit_bytes: number
  memory_percent: number
  network_rx_bytes: number
  network_tx_bytes: number
}

export interface ApiKey {
  id: number
  name: string
  key_prefix: string
  scopes: string[]
  allowed_ips: string[] | null
  last_used_at: string | null
  expires_at: string | null
  revoked: boolean
  created_at: string
  // null = account-wide (all the user's workspaces); set = scoped to one workspace.
  workspace_id?: number | null
}

export interface ApiKeyCreated {
  id: number
  name: string
  key: string
  key_prefix: string
  scopes: string[]
  allowed_ips: string[] | null
  expires_at: string | null
  message: string
  // Set for a workspace-scoped key; absent for account-wide.
  workspace_id?: number | null
}

export interface CreateApiKeyInput {
  name: string
  scopes?: string[]
  allowed_ips?: string[]
  expires_in_days?: number
  // Omit / null for an account-wide key; set to scope the key to one workspace.
  workspace_id?: number | null
}

export interface Member {
  id: number
  workspace_id: number
  user_id: number
  role: 'owner' | 'admin' | 'developer' | 'viewer'
  custom_role_id?: number | null
  user: User
}

export interface Invitation {
  id: number
  email: string
  role: string
  status: string
  created_at: string
}

// PendingInvitation is an invitation addressed to the current user, enriched
// with the workspace name and inviter so they can act on it before joining.
export interface PendingInvitation {
  id: number
  workspace_id: number
  workspace_name: string
  role: string
  invited_by_name: string
  expires_at: string
  created_at: string
}

export interface AuditLog {
  id: number
  actor_id?: number
  action: string
  target_type: string
  target_id: string
  ip_address: string
  created_at: string
}

// AuditLogDetail is a single entry enriched with the actor's name/email and the
// full metadata, returned by the audit-log detail endpoint.
export interface AuditLogDetail extends AuditLog {
  workspace_id?: number | null
  metadata?: Record<string, unknown>
  actor_name?: string
  actor_email?: string
}

export type ServerConnectivity = 'port-forward' | 'edge-gateway'

export type ServerRole = 'manager' | 'node'

export interface Server {
  id: number
  name: string
  slug?: string
  role?: ServerRole
  connectivity?: ServerConnectivity
  access_mode?: 'socket' | 'agent' | 'api'
  docker_endpoint: string
  tls_enabled?: boolean
  is_local: boolean
  status: 'online' | 'offline' | 'unknown'
  last_seen_at?: string | null
  address?: string
  public_ip?: string
  public_hostname?: string
  agent_connected?: boolean
  agent_version?: string
  cordoned?: boolean
  labels?: Record<string, string>
  gateway_deployed_at?: string | null
  // Cluster (Docker Swarm). Populated only when cluster mode is on; otherwise
  // these stay empty and the node is shown as standalone.
  swarm_node_id?: string
  // True when the CLUSTER brought this node in rather than an admin: the global agent
  // service landed on a swarm member and the agent registered itself. It distinguishes
  // a machine an operator chose from one that simply exists because it is in the swarm.
  auto_joined?: boolean
  swarm_role?: SwarmRole
  swarm_availability?: 'active' | 'pause' | 'drain'
  swarm_state?: 'ready' | 'down' | 'unknown' | 'disconnected'
  in_swarm?: boolean
  created_at?: string
  updated_at?: string
}

// SwarmRole is a node's role within the cluster's swarm; "standalone" means
// cluster mode is on but the node is not a swarm member.
export type SwarmRole = 'leader' | 'manager' | 'worker' | 'standalone'

// ClusterStatus is the manager's swarm capability + state.
export interface ClusterStatus {
  enabled: boolean
  // The operator's label for this cluster. Swarm identifies it by an unreadable id and
  // a manager address that moves, so without this the UI can only say "the cluster".
  name?: string
  local_node_state: string
  manager_addr?: string
  node_id?: string
  managers: number
  nodes: number
  // Shared overlay the reverse proxy joins to reach clustered apps' service VIPs.
  // Set only when cluster mode is on; used to tell an admin running their own
  // proxy how to attach it (docker network connect <ingress_network> <proxy>).
  ingress_network?: string
  // How many workspace networks are still node-local bridges. Non-zero while
  // cluster mode is on means cross-node east-west does NOT work for those
  // workspaces — their apps and databases sit on per-node islands. Normal for an
  // install that was already clustered when it upgraded, since the conversion only
  // runs on the enable transition; the admin applies it explicitly.
  networks_pending?: number
  // Whether the global agent service is deployed — i.e. whether swarm workers are
  // MANAGED (metrics, stats, shell, housekeeping) or merely running tasks Miabi
  // cannot see into. Swarm carries the agent to every worker, including ones that
  // join later, so this is all-or-nothing rather than per-node.
  agents_deployed?: boolean
  agent_tasks?: number
  // True when the agents do NOT verify the control plane's TLS certificate. Surfaced
  // so a setting made once, to get a self-signed cert working, cannot quietly become
  // permanent.
  agent_insecure_tls?: boolean
  // True when the agents verify against an operator-supplied CA. This is the HEALTHY
  // state for a private control plane: verification still happens, anchored on their CA.
  agent_custom_ca?: boolean
  // Set when that CA is a file on the nodes rather than inline PEM — a dependency on
  // the host filesystem, so it must exist on every node including future ones.
  agent_ca_cert_path?: string
  error?: string
}

// ClusterJoinInstructions is the manual `docker swarm join` command + worker
// token for joining a host that isn't connected to the manager.
export interface ClusterJoinInstructions {
  worker_token: string
  manager_addr: string
  command: string
}

// ClusterMember is one swarm node (docker node ls), annotated with the Miabi
// node it maps to (or unmanaged when it has no Miabi record).
// Preflight: what this host can and cannot do BEFORE cluster mode is turned on.
// The blocker case is a Docker engine running inside a VM (Docker Desktop, OrbStack):
// the swarm forms and DNS resolves, then every cross-node connection times out,
// because VXLAN and IPSec cannot be forwarded into the VM.
export interface ClusterFinding {
  severity: 'blocker' | 'warning' | 'info'
  title: string
  detail: string
}
export interface ClusterFirewallRule {
  port: string
  purpose: string
}
export interface ClusterPreflight {
  engine_os: string
  // False when this engine cannot carry the overlay data plane to other hosts at
  // all. Single-node cluster mode still works.
  multi_node_capable: boolean
  findings: ClusterFinding[]
  firewall: ClusterFirewallRule[]
}

// NetCheck probes the real overlay between every pair of nodes, separating the three
// failures that look identical from inside an app: a name that will not resolve, a
// connection that never completes, and a payload that dies silently at the MTU.
export interface NetCheckProbe {
  server_id: number
  node_name: string
  reachable: boolean
  error?: string
}
export interface NetCheckResult {
  from: string
  to: string
  dns: boolean
  tcp: boolean
  payload: boolean // a 1400-byte body round-tripped
  ip?: string
  error?: string
  verdict: string
}
export interface NetCheck {
  network: string
  probes: NetCheckProbe[]
  results: NetCheckResult[]
  ok: boolean
  summary: string
}

// A service task the scheduler placed on a node. Only the manager can enumerate
// these — the container lives on the node, which Miabi may have no client for.
export interface SwarmTask {
  id: string
  service_name: string
  node_id: string
  image?: string
  slot?: number
  state: string
  desired_state: string
  message?: string
  error?: string
  updated_at?: string
}

// The certificate the control plane currently serves, offered so agents can be pinned
// to it instead of skipping verification. Miabi fetches it so nobody has to hunt for a
// PEM — the alternative being that they pick "skip" and never look back.
export interface ControlPlaneCert {
  pem: string
  subject: string
  issuer: string
  not_after: string
  fingerprint: string
  self_signed: boolean
  // Already verifies against the system pool: no CA needs distributing at all.
  publicly_trusted: boolean
  // The names the certificate actually vouches for (its SANs). Empty means none.
  hosts?: string[]
  // Whether it names the address the agents dial. This is the trap in "just trust the
  // CA": adding a certificate to the trust pool does NOT skip the hostname check, so a
  // certificate with no SANs (Goma's default has none) fails however well it is trusted.
  matches_host: boolean
  dial_host: string
  // Whether what we can offer is a real certificate AUTHORITY, or merely the server's
  // own leaf certificate. Pinning a leaf works — until the certificate is renewed, at
  // which point every agent drops off at once. Then the right answer is to paste the
  // actual CA instead.
  anchor_is_ca: boolean
}

export interface ClusterMember {
  id: string
  hostname: string
  role: string // manager | worker
  availability: string // active | pause | drain
  state: string // ready | down | unknown | disconnected
  leader: boolean
  reachability?: string
  addr?: string
  engine_version?: string
  // Capacity as the swarm scheduler sees it — what it packs tasks against. Reported
  // by the node over the swarm control plane, so it is known even for an unmanaged
  // member (no Miabi agent), where host metrics are unavailable.
  nano_cpus?: number // 1e9 == one core
  memory_bytes?: number // total, not used
  os?: string
  arch?: string
  // How many service tasks the scheduler currently runs on this node.
  tasks: number
  managed: boolean
  server_id?: number
  server_name?: string
  is_manager: boolean
}

export interface DockerInfo {
  version: string
  os: string
  arch: string
  containers: number
  containers_running: number
  images: number
  cpus: number
  mem_total: number
}

export interface NodeStats extends DockerInfo {
  volumes: number
  networks: number
  // Miabi's own runtime container on this node (manager or agent), if known.
  self_container?: { id: string; name: string }
}

export interface ContainerStat extends StatsSample {
  id: string
}

// NodeHostMetrics is the real host CPU/memory usage for the local node (read from
// procfs). available is false for remote nodes or when no procfs is readable.
export interface NodeHostMetrics {
  available: boolean
  reason?: string
  cpu_percent: number
  mem_total_bytes: number
  mem_used_bytes: number
  mem_percent: number
}

export interface ContainerPort {
  private_port: number
  public_port?: number
  protocol: string
}

export interface Container {
  id: string
  names: string[]
  image: string
  state: string
  status: string
  health?: string
  created?: number
  started_at?: string
  ports?: ContainerPort[]
  labels?: Record<string, string>
}

export interface DockerVolume {
  name: string
  driver: string
  mountpoint?: string
  labels?: Record<string, string>
}

export interface DockerNetwork {
  id: string
  name: string
  driver: string
  scope: string
  labels?: Record<string, string>
}

export interface GatewayStatus {
  connectivity: string
  deployed: boolean
  running: boolean
  imported?: boolean
  container?: string
  image?: string
  image_effective?: string
  image_override?: string
  health?: string
  status?: string
  // Redis powering shared cache + distributed rate limiting. redis_shared = the
  // gateway reuses the platform Redis (manager) vs a per-node Redis (edge nodes).
  redis_enabled?: boolean
  redis_shared?: boolean
  // In-flight safe-update progress, if any.
  update?: GatewayUpdateProgress
}

// GatewayUpdateProgress is the live state of a safe gateway update (test →
// promote), mirroring the database UpgradeProgress.
export interface GatewayUpdateProgress {
  from_image: string
  to_image: string
  // queued | pulling | testing | observing | promoting | verifying | done | failed
  phase: string
  error?: string
}

export interface GatewayCandidate {
  id: string
  name: string
  image: string
  state: string
}

// --- GitOps & CI/CD ---

export type GitSyncPolicy = 'manual' | 'auto'
export type GitSourceStatus = 'unknown' | 'synced' | 'out_of_sync' | 'progressing' | 'error'

export interface GitSource {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  repo_url: string
  ref: string
  path: string
  git_repository_id?: number | null
  sync_policy: GitSyncPolicy
  prune: boolean
  self_heal: boolean
  allow_empty: boolean
  status: GitSourceStatus
  message?: string
  last_synced_commit?: string
  last_synced_author?: string
  last_synced_subject?: string
  last_synced_at?: string | null
  created_at: string
  updated_at: string
}

export type PlanAction = 'create' | 'update' | 'delete' | 'noop'

export interface PlanFieldDiff {
  field: string
  from: string
  to: string
}

export interface PlanChange {
  action: PlanAction
  kind: string
  name: string
  reason?: string
  fields?: PlanFieldDiff[]
}

export interface ApplyPlan {
  changes: PlanChange[] | null
}

// Response to deleting a GitOps project. teardown is present only for a cascade
// delete and lists the resources that were removed (and any that failed).
// ApplyResult/ApplyFailure are defined below.
export interface GitOpsDeleteResult {
  message: string
  teardown?: ApplyResult | null
}

// Resource topology graph for a GitOps project (project-detail view).
export type NodeStatus = 'synced' | 'out_of_sync' | 'missing' | 'orphaned'
export type EdgeType = 'mount' | 'stack' | 'route' | 'domain' | 'database' | 'secret' | 'app-ref'

export interface TopologyNode {
  key: string // "<Kind>/<name>" — matches edge endpoints
  kind: string // Application, Database, Volume, Route, Stack, Secret, Domain
  name: string
  status: NodeStatus
  live_id?: number // 0/absent when not yet created
  slug?: string
  health?: string // kind-specific runtime status (e.g. an app's "running")
  url?: string // public address (e.g. a route's host) when the resource has one
}

export interface TopologyEdge {
  from: string
  to: string
  type: EdgeType
}

export interface Topology {
  nodes: TopologyNode[] | null
  edges: TopologyEdge[] | null
  counts: Partial<Record<NodeStatus, number>>
  // Set when the desired manifests could not be loaded; nodes/edges then reflect
  // the last deployed (live) state and `live` is true.
  error?: string
  live?: boolean
}

export interface ApplyFailure {
  kind: string
  name: string
  action: string
  error: string
}

export interface ApplyResult {
  plan: ApplyPlan
  applied: number
  dry_run: boolean
  failures?: ApplyFailure[]
  workspace_id: number
}

export type PipelineRunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'

// Lightweight summary of a pipeline's most recent run, attached to the list
// response so the table can show an at-a-glance status without loading runs.
export interface PipelineRunSummary {
  id: number
  number: number
  status: PipelineRunStatus
  trigger?: string
  commit?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}

export interface PipelineDefinition {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  application_id?: number | null
  spec: string
  enabled: boolean
  created_at: string
  updated_at: string
  last_run?: PipelineRunSummary | null
}

export interface PipelineStepRun {
  id: number
  pipeline_run_id: number
  ordinal: number
  name: string
  status: PipelineRunStatus
  image?: string
  uses?: string
  exit_code: number
  // continue_on_error: a failure here doesn't fail the run (the step still shows failed).
  continue_on_error?: boolean
  logs?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}

// Full stored per-step logs of a finished pipeline run (load-once, JSON).
export interface PipelineStepLogHistory {
  ordinal: number
  name: string
  status: string
  lines: string[]
  truncated: boolean
}
export interface PipelineRunLogHistory {
  status: string
  steps: PipelineStepLogHistory[]
}

export interface PipelineRun {
  id: number
  workspace_id: number
  pipeline_id: number
  number: number
  status: PipelineRunStatus
  trigger: string
  commit?: string
  commit_message?: string
  image_id?: number | null
  error?: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
  steps?: PipelineStepRun[]
}

// --- Built-image catalog ---

export interface Image {
  id: number
  workspace_id: number
  repository: string
  digest: string
  tag?: string
  size_bytes: number
  pipeline_run_id?: number | null
  application_id?: number | null
  commit?: string
  runner?: string
  built_at?: string | null
  created_at: string
}

// --- Environments & Releases ---

export interface Environment {
  id: number
  workspace_id: number
  name: string
  display_name?: string
  description?: string
  rank: number
  required_approvals: number
  git_source_id?: number | null
  created_at: string
  updated_at: string
}

export interface WorkspaceRelease {
  id: number
  application_id: number
  application_name: string
  application_display_name: string
  version: number
  image: string
  digest?: string
  commit?: string
  active: boolean
  pinned: boolean
  environment_id?: number | null
  created_at: string
}

export interface ReleaseApproval {
  id: number
  workspace_id: number
  release_id: number
  environment_id?: number | null
  approver_id: number
  approved: boolean
  comment?: string
  created_at: string
}

export interface EnvApproval {
  environment_id: number
  environment_name: string
  required_approvals: number
  approvals: number
  satisfied: boolean
}

export interface ReleaseApprovalStatus {
  environments: EnvApproval[] | null
  approvals: ReleaseApproval[] | null
}

// --- RBAC: permissions, custom roles, per-resource policies ---

export interface PermissionInfo {
  id: string
  resource: string
  action: string
}
export interface RolePreset {
  role: string
  permissions: string[]
}
export interface PermissionCatalog {
  permissions: PermissionInfo[]
  roles: RolePreset[]
}

export interface CustomRole {
  id: number
  workspace_id: number | null
  name: string
  base_role: WorkspaceRole
  permissions: string[]
  created_at?: string
  updated_at?: string
}

export interface ResourcePolicy {
  id: number
  workspace_id: number
  user_id: number
  resource_type: string
  resource_id: number
  permissions: string[]
  user?: User
}

// --- Commercial license (Enterprise) ---

export type LicenseEdition = 'community' | 'enterprise'
export type LicenseState = 'valid' | 'grace' | 'degraded' | 'none' | 'binding_mismatch'

export interface LicenseEntitlements {
  edition: LicenseEdition
  tier?: string // commercial plan label (professional | business | enterprise)
  install_id?: string // Install ID the license is bound to (empty = not bound)
  url?: string // deployment URL the license is bound to (empty = not bound)
  binding_error?: 'install_id' | 'url' // which binding failed, when state is binding_mismatch
  state: LicenseState
  customer?: string
  license_id?: string
  flags: Record<string, boolean>
  limits: Record<string, number>
  not_after?: string | null
  grace_ends?: string | null
}

export interface LicenseNodeUsage {
  used: number
  limit: number // -1 = unlimited
}

// LicensePlanUsage reports the plan-catalog size against the edition cap.
export interface LicensePlanUsage {
  used: number
  limit: number // -1 = unlimited
}

export interface LicenseView extends LicenseEntitlements {
  instance_install_id: string // THIS deployment's Install ID ("Your Install ID")
  node_usage: LicenseNodeUsage
  plan_usage: LicensePlanUsage
  warnings: string[]
}

export interface LicenseHealth {
  edition: LicenseEdition
  state: LicenseState
  warnings: string[]
}

// --- Domains (owned hostnames) ---

export type DomainTLSMode = 'acme' | 'custom'

export interface Domain {
  id: number
  workspace_id: number
  name: string
  tls_mode: DomainTLSMode
  wildcard: boolean
  verified: boolean
  verified_at?: string | null
  verification_token: string
  // Last ownership check (whether it passed or failed) and the last failure reason.
  verification_checked_at?: string | null
  verification_error?: string
  // Banned blocks a domain platform-wide (admin action): its routes are forced
  // offline and it cannot be verified.
  banned?: boolean
  banned_at?: string | null
  ban_reason?: string
  challenge_host: string
  challenge_value: string
  // dns_provider_id links a connected DNS provider (Miabi automates the records);
  // automated mirrors it (provider linked).
  dns_provider_id?: number | null
  automated?: boolean
  created_at: string
  updated_at: string
}

// --- Admin: platform-wide domains ---

export type DomainStatus = 'pending' | 'verified' | 'failed' | 'banned'

export interface AdminDomain {
  id: number
  workspace_id: number
  workspace_name: string
  name: string
  status: DomainStatus
  verified: boolean
  verified_at?: string | null
  tls_mode: DomainTLSMode
  wildcard: boolean
  automated: boolean
  created_at: string
}

// RouteSyncStatus is a route's config-sync status with the gateway.
export type RouteSyncStatus = 'pending' | 'live' | 'offline' | 'error'

export interface AdminRoute {
  id: number
  workspace_id: number
  workspace_name: string
  application_id: number
  app_name: string
  name: string
  path: string
  hosts?: string[]
  tls_mode: string
  enabled: boolean
  generated: boolean
  status: RouteSyncStatus
  status_reason?: string
  synced_at?: string | null
  created_at: string
}

// ResyncSummary reports the outcome of a platform-wide route resync.
export interface ResyncSummary {
  workspaces: number
  failed: number
  routes: number
  live: number
  offline: number
  errors: number
  pending: number
  failed_list?: { workspace_id: number; name: string; error: string }[]
}

export interface AdminDomainRoute {
  id: number
  name: string
  hosts: string[]
  status: RouteStatus
  status_reason?: string
  enabled: boolean
}

export interface AdminDomainDetail extends Domain {
  workspace_name: string
  workspace_privileged: boolean
  routes: AdminDomainRoute[]
  recent_events: AdminEvent[]
}
