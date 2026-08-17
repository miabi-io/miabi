import api, { sseUrl } from './client'
import type {
  ApiResponse, DatabaseInstance, DBLiveStatus, LogicalDatabase, ConnectionInfo, ForwardSession, DBEngine, EngineDefault, UpgradeOptions, UpgradePlan,
  Volume, VolumeDetail, VolumeFile, VolumeBackup, WorkspaceStorage, Backup, BackupSchedule, MetricSample, StatsSample, ApiKey, ApiKeyCreated, CreateApiKeyInput,
  Member, Invitation, AuditLog, AuditLogDetail, RecentEvent, PageableResponse, Job, CronJob, WorkspaceUsage, WorkspaceLiveSample, WorkspaceHistoryPoint,
} from './types'

const w = (ws: number) => `/workspaces/${ws}`

// Create/update payload for a CronJob (targets an app via application_id).
export interface CronJobInput {
  application_id?: number
  name?: string
  schedule: string
  command: string[]
  entrypoint?: string[]
  image?: string
  registry_id?: number | null
  // Pins spawned runs to an account ("1000", "1000:1000", "node"); blank inherits
  // the app's when each run fires.
  run_as_user?: string
  timeout_secs?: number
  enabled?: boolean
  concurrency_policy?: 'allow' | 'forbid' | 'replace'
  history_limit?: number
}

// Workspace-scoped one-off Jobs and CronJobs. Each targets an application
// (application_id) but is managed at the workspace level.
export const jobApi = {
  list: (ws: number, appId?: number) =>
    api.get<ApiResponse<Job[]>>(`${w(ws)}/jobs${appId ? `?app_id=${appId}` : ''}`),
  run: (ws: number, body: { application_id: number; name?: string; command: string[]; entrypoint?: string[]; image?: string; registry_id?: number | null; run_as_user?: string; timeout_secs?: number }) =>
    api.post<ApiResponse<Job>>(`${w(ws)}/jobs`, body),
  get: (ws: number, jobId: number) => api.get<ApiResponse<Job>>(`${w(ws)}/jobs/${jobId}`),
  cancel: (ws: number, jobId: number) => api.post<ApiResponse<{ message: string }>>(`${w(ws)}/jobs/${jobId}/cancel`),
  remove: (ws: number, jobId: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/jobs/${jobId}`),

  cronJobs: (ws: number, appId?: number) =>
    api.get<ApiResponse<CronJob[]>>(`${w(ws)}/cronjobs${appId ? `?app_id=${appId}` : ''}`),
  createCronJob: (ws: number, input: CronJobInput) => api.post<ApiResponse<CronJob>>(`${w(ws)}/cronjobs`, input),
  updateCronJob: (ws: number, cronId: number, input: CronJobInput) => api.put<ApiResponse<CronJob>>(`${w(ws)}/cronjobs/${cronId}`, input),
  runCronJobNow: (ws: number, cronId: number) => api.post<ApiResponse<Job>>(`${w(ws)}/cronjobs/${cronId}/run`),
  deleteCronJob: (ws: number, cronId: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/cronjobs/${cronId}`),
}

export const databaseApi = {
  // Resolved engine defaults (image/version) from the deployment-config catalog.
  engines: (ws: number) => api.get<ApiResponse<EngineDefault[]>>(`${w(ws)}/database-engines`),
  // Instances (the server).
  list: (ws: number) => api.get<ApiResponse<DatabaseInstance[]>>(`${w(ws)}/databases`),
  get: (ws: number, id: number) => api.get<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases/${id}`),
  // Lightweight live status (one-shot) — a REST fallback for the SSE stream below.
  status: (ws: number, id: number) => api.get<ApiResponse<DBLiveStatus>>(`${w(ws)}/databases/${id}/status`),
  // SSE stream of live status: provisioning progress, upgrade phases, start/stop.
  eventsUrl: (ws: number, id: number) => sseUrl(`${w(ws)}/databases/${id}/events`),
  // SSE stream of live status for every instance in the workspace (list page).
  workspaceEventsUrl: (ws: number) => sseUrl(`${w(ws)}/databases/events`),
  create: (ws: number, name: string, engine: DBEngine, version?: string, serverId?: number, sizeMb?: number) =>
    api.post<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases`, { name, engine, version, server_id: serverId, size_mb: sizeMb }),
  credentials: (ws: number, id: number) => api.get<ApiResponse<ConnectionInfo>>(`${w(ws)}/databases/${id}/credentials`),
  start: (ws: number, id: number) => api.post<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}/start`),
  stop: (ws: number, id: number) => api.post<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}/stop`),
  restart: (ws: number, id: number) => api.post<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}/restart`),
  syncSizes: (ws: number, id: number) => api.post<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases/${id}/sync-sizes`),
  logsUrl: (ws: number, id: number) => sseUrl(`${w(ws)}/databases/${id}/logs`),
  upgradeOptions: (ws: number, id: number) => api.get<ApiResponse<UpgradeOptions>>(`${w(ws)}/databases/${id}/upgrade`),
  upgradePlan: (ws: number, id: number, version: string) =>
    api.get<ApiResponse<UpgradePlan>>(`${w(ws)}/databases/${id}/upgrade`, { params: { version } }),
  upgrade: (ws: number, id: number, version: string, stopApps: boolean) =>
    api.post<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases/${id}/upgrade`, { version, stop_apps: stopApps }),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}`),

  // Networks the instance is attached to (the default is always attached).
  attachNetwork: (ws: number, id: number, networkId: number) =>
    api.post<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases/${id}/networks/${networkId}`),
  detachNetwork: (ws: number, id: number, networkId: number) =>
    api.delete<ApiResponse<DatabaseInstance>>(`${w(ws)}/databases/${id}/networks/${networkId}`),

  // On-demand external port-forward.
  listForwards: (ws: number, id: number) => api.get<ApiResponse<ForwardSession[]>>(`${w(ws)}/databases/${id}/forward`),
  openForward: (ws: number, id: number) => api.post<ApiResponse<ForwardSession>>(`${w(ws)}/databases/${id}/forward`, {}),
  closeForward: (ws: number, id: number, sessionId: string) =>
    api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}/forward/${sessionId}`),

  // Logical databases hosted on an instance.
  listDatabases: (ws: number, id: number) => api.get<ApiResponse<LogicalDatabase[]>>(`${w(ws)}/databases/${id}/databases`),
  createDatabase: (ws: number, id: number, name: string, applicationId?: number | null) =>
    api.post<ApiResponse<{ database: LogicalDatabase; env_injected: boolean }>>(`${w(ws)}/databases/${id}/databases`, { name, application_id: applicationId ?? null }),
  databaseConnection: (ws: number, id: number, dbId: number) =>
    api.get<ApiResponse<ConnectionInfo>>(`${w(ws)}/databases/${id}/databases/${dbId}/connection`),
  removeDatabase: (ws: number, id: number, dbId: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/databases/${id}/databases/${dbId}`),
}

export const volumeApi = {
  list: (ws: number) => api.get<ApiResponse<Volume[]>>(`${w(ws)}/volumes`),
  // Declared-vs-measured storage summary; served from cached columns (no live df).
  storage: (ws: number) => api.get<ApiResponse<WorkspaceStorage>>(`${w(ws)}/storage`),
  get: (ws: number, id: number) => api.get<ApiResponse<VolumeDetail>>(`${w(ws)}/volumes/${id}`),
  create: (ws: number, name: string, serverId?: number, sizeMb?: number, driver?: string, driverOpts?: Record<string, string>) =>
    api.post<ApiResponse<Volume>>(`${w(ws)}/volumes`, { name, server_id: serverId, size_mb: sizeMb, driver, driver_opts: driverOpts }),
  remove: (ws: number, id: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/volumes/${id}`),

  // Files stored inside a volume.
  listFiles: (ws: number, id: number) => api.get<ApiResponse<VolumeFile[]>>(`${w(ws)}/volumes/${id}/files`),
  uploadFile: (ws: number, id: number, file: File, path?: string) => {
    const form = new FormData()
    form.append('file', file)
    if (path) form.append('path', path)
    return api.post<ApiResponse<{ path: string }>>(`${w(ws)}/volumes/${id}/files`, form, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
  },
  downloadFile: (ws: number, id: number, path: string) =>
    api.get<Blob>(`${w(ws)}/volumes/${id}/files/download?path=${encodeURIComponent(path)}`, { responseType: 'blob' }),
  deleteFile: (ws: number, id: number, path: string) =>
    api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/volumes/${id}/files?path=${encodeURIComponent(path)}`),

  // Volume backups to the workspace S3 target.
  backupStatus: (ws: number, id: number) =>
    api.get<ApiResponse<{ s3_configured: boolean }>>(`${w(ws)}/volumes/${id}/backups/status`),
  listBackups: (ws: number, id: number) =>
    api.get<ApiResponse<VolumeBackup[]>>(`${w(ws)}/volumes/${id}/backups`),
  runBackup: (ws: number, id: number) =>
    api.post<ApiResponse<VolumeBackup>>(`${w(ws)}/volumes/${id}/backups`),
  restoreBackup: (ws: number, id: number, backupId: number) =>
    api.post<ApiResponse<{ message: string }>>(`${w(ws)}/volumes/${id}/backups/${backupId}/restore`),
  deleteBackup: (ws: number, id: number, backupId: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/volumes/${id}/backups/${backupId}`),
}

// Backups belong to a logical database on an instance.
const b = (ws: number, inst: number, db: number) => `${w(ws)}/databases/${inst}/databases/${db}`

export const backupApi = {
  list: (ws: number, inst: number, db: number) => api.get<ApiResponse<Backup[]>>(`${b(ws, inst, db)}/backups`),
  // Destination is decided server-side: the workspace S3 target when configured, else local.
  run: (ws: number, inst: number, db: number) => api.post<ApiResponse<Backup>>(`${b(ws, inst, db)}/backups`, {}),
  restore: (ws: number, inst: number, db: number, id: number, method: 'normal' | 'force' = 'normal') =>
    api.post<ApiResponse<{ message: string }>>(`${b(ws, inst, db)}/backups/${id}/restore`, { method }),
  restoreFile: (ws: number, inst: number, db: number, file: File, method: 'normal' | 'force' = 'normal') => {
    const form = new FormData()
    form.append('file', file)
    form.append('method', method)
    return api.post<ApiResponse<{ message: string }>>(`${b(ws, inst, db)}/restore-file`, form, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
  },
  remove: (ws: number, inst: number, db: number, id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${b(ws, inst, db)}/backups/${id}`),
  download: (ws: number, inst: number, db: number, id: number) =>
    api.get<Blob>(`${b(ws, inst, db)}/backups/${id}/download`, { responseType: 'blob' }),
  schedules: (ws: number, inst: number, db: number) => api.get<ApiResponse<BackupSchedule[]>>(`${b(ws, inst, db)}/backup-schedules`),
  createSchedule: (ws: number, inst: number, db: number, cron: string, maxBackups = 0, retentionDays = 0) =>
    api.post<ApiResponse<BackupSchedule>>(`${b(ws, inst, db)}/backup-schedules`, {
      cron, destination: 'local', max_backups: maxBackups, retention_days: retentionDays,
    }),
  deleteSchedule: (ws: number, inst: number, db: number, id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`${b(ws, inst, db)}/backup-schedules/${id}`),
}


export const monitoringApi = {
  metrics: (ws: number, app: number) => api.get<ApiResponse<StatsSample>>(`${w(ws)}/apps/${app}/metrics`),
  history: (ws: number, app: number, since = '1h') =>
    api.get<ApiResponse<MetricSample[]>>(`${w(ws)}/apps/${app}/metrics/history?since=${since}`),
}

export const apiKeyApi = {
  list: () => api.get<ApiResponse<ApiKey[]>>('/api-keys'),
  get: (id: number) => api.get<ApiResponse<ApiKey>>(`/api-keys/${id}`),
  create: (input: CreateApiKeyInput) => api.post<ApiResponse<ApiKeyCreated>>('/api-keys', input),
  revoke: (id: number) => api.post<ApiResponse<{ message: string }>>(`/api-keys/${id}/revoke`),
  // Permanently removes a revoked/expired key; active keys must be revoked first.
  remove: (id: number) => api.delete<ApiResponse<{ message: string }>>(`/api-keys/${id}`),
}

export const usageApi = {
  get: (ws: number) => api.get<ApiResponse<WorkspaceUsage>>(`${w(ws)}/usage`),
  // Live, aggregated actual consumption across the workspace's running
  // app/database containers (one-shot fallback for the SSE stream below).
  getLive: (ws: number) => api.get<ApiResponse<WorkspaceLiveSample>>(`${w(ws)}/usage/live`),
  liveStreamUrl: (ws: number) => sseUrl(`${w(ws)}/usage/live/stream`),
  // Time-bucketed workspace usage aggregated from stored per-app samples (sparkline).
  history: (ws: number, since = '1h') =>
    api.get<ApiResponse<WorkspaceHistoryPoint[]>>(`${w(ws)}/usage/history`, { params: { since } }),
}

export const memberApi = {
  list: (ws: number) => api.get<ApiResponse<Member[]>>(`${w(ws)}/members`),
  updateRole: (ws: number, userId: number, role: string) =>
    api.patch<ApiResponse<{ message: string }>>(`${w(ws)}/members/${userId}`, { role }),
  remove: (ws: number, userId: number) => api.delete<ApiResponse<{ message: string }>>(`${w(ws)}/members/${userId}`),
  invitations: (ws: number) => api.get<ApiResponse<Invitation[]>>(`${w(ws)}/invitations`),
  invite: (ws: number, email: string, role: string) =>
    api.post<ApiResponse<{ id: number; email: string; role: string; token: string }>>(`${w(ws)}/invitations`, { email, role }),
  acceptInvite: (token: string) => api.post<ApiResponse<unknown>>('/workspaces/invitations/accept', { token }),
  auditLogs: (ws: number, page = 0, size = 20, order = 'desc', from = '', to = '') =>
    api.get<PageableResponse<AuditLog>>(`${w(ws)}/audit-logs`, {
      params: { page, size, order, from: from || undefined, to: to || undefined },
    }),
  auditLog: (ws: number, id: number) =>
    api.get<ApiResponse<AuditLogDetail>>(`${w(ws)}/audit-logs/${id}`),
}

// Workspace-wide application events (deploys, container lifecycle, config changes).
export const workspaceEventApi = {
  list: (ws: number, page = 0, size = 20, order = 'desc', severity = '') =>
    api.get<PageableResponse<RecentEvent>>(`${w(ws)}/events`, {
      params: { page, size, order, severity: severity || undefined },
    }),
}
