import api from './client'
import type {
  ApiResponse,
  PageableResponse,
  AdminUser,
  AdminUserDetail,
  OwnershipTransfer,
  AdminWorkspace,
  AdminWorkspaceDetail,
  AdminDomain,
  AdminDomainDetail,
  AdminRoute,
  RouteSyncStatus,
  ResyncSummary,
  Domain,
  DomainStatus,
  PlatformMetrics,
  AdminEvent,
  PlatformSetting,
  UpdateInfo,
  JobStatus,
  JobStats,
  OAuthProvider,
  Plan,
  PlanInput,
  WorkspaceQuotaOverride,
  LicenseView,
  LicenseHealth,
  SIEMConfig,
} from './types'

export interface CreateUserPayload {
  name: string
  // Optional unique handle; auto-derived from the email local-part when omitted.
  username?: string
  email: string
  password: string
  role: 'admin' | 'user'
  // notify emails the new user a welcome with a sign-in link (needs system SMTP).
  notify?: boolean
}

// Deployment Config — platform image catalog.
export interface ImageCatalogItem {
  key: string
  label: string
  category: string
  default: string
  description: string
  override: string
  effective: string
}
export interface DeploymentConfig {
  images: ImageCatalogItem[]
  mirror: string
}


export interface UpdateUserPayload {
  role?: 'admin' | 'user'
  active?: boolean
  // Optional change to the unique handle (an admin action).
  username?: string
}

export interface SettingInput {
  key: string
  value: string
  type: 'string' | 'int' | 'bool' | 'json'
}

export interface OAuthProviderPayload {
  display_name: string
  name?: string
  type: 'google' | 'oidc'
  client_id: string
  client_secret?: string
  issuer?: string
  auth_url?: string
  token_url?: string
  userinfo_url?: string
  scopes?: string
  enabled?: boolean
  hidden?: boolean
  auto_register?: boolean
  allowed_domains?: string
  email_claim?: string
  name_claim?: string
  default_workspace_id?: number
  default_role?: string
}

// --- LDAP / Active Directory (Enterprise) ---

export type LdapTLSMode = 'none' | 'starttls' | 'ldaps'

export interface LdapGroupMapping {
  id: number
  ldap_config_id: number
  group_dn: string
  system_admin: boolean
  workspace_id?: number | null
  workspace_role?: string
}

export interface LdapConfig {
  id: number
  name: string
  display_name: string
  host: string
  port: number
  tls_mode: LdapTLSMode
  ca_cert_pem?: string
  insecure_skip_tls: boolean
  timeout_seconds: number
  bind_dn: string
  bind_password_set: boolean
  user_base_dn: string
  user_filter: string
  attr_email: string
  attr_name: string
  attr_username: string
  group_base_dn: string
  group_filter: string
  member_attr: string
  nested_groups: boolean
  enabled: boolean
  mappings?: LdapGroupMapping[]
  created_at: string
  updated_at: string
}

export interface LdapConfigPayload {
  display_name: string
  name?: string
  host: string
  port?: number
  tls_mode?: LdapTLSMode
  ca_cert_pem?: string
  insecure_skip_tls?: boolean
  timeout_seconds?: number
  bind_dn?: string
  bind_password?: string
  user_base_dn?: string
  user_filter?: string
  attr_email?: string
  attr_name?: string
  attr_username?: string
  group_base_dn?: string
  group_filter?: string
  member_attr?: string
  nested_groups?: boolean
  enabled?: boolean
}

export interface LdapMappingPayload {
  group_dn: string
  system_admin?: boolean
  workspace_id?: number | null
  workspace_role?: string
}

export interface LdapTestResult {
  ok: boolean
  message?: string
  error?: string
}

export const adminApi = {
  // Users
  listUsers: (search = '', page = 0, size = 20) =>
    api.get<PageableResponse<AdminUser>>('/admin/users', { params: { search: search || undefined, page, size } }),
  getUser: (id: number) => api.get<ApiResponse<AdminUserDetail>>(`/admin/users/${id}`),
  createUser: (payload: CreateUserPayload) =>
    api.post<ApiResponse<AdminUser>>('/admin/users', payload),
  updateUser: (id: number, payload: UpdateUserPayload) =>
    api.put<ApiResponse<AdminUser>>(`/admin/users/${id}`, payload),
  // Enterprise per-user override of how many workspaces the user may own
  // (null clears it → inherit the platform default; -1 = unlimited).
  setWorkspaceLimit: (id: number, limit: number | null) =>
    api.put<ApiResponse<AdminUser>>(`/admin/users/${id}/workspace-limit`, { limit }),
  // Enterprise per-user override of how many workspaces the user may join as a member.
  setWorkspaceMembershipLimit: (id: number, limit: number | null) =>
    api.put<ApiResponse<AdminUser>>(`/admin/users/${id}/workspace-membership-limit`, { limit }),
  // Schedule a disabled account for deletion after the grace period, optionally
  // transferring ownership of some of its workspaces first.
  scheduleDeletion: (id: number, transfers: OwnershipTransfer[] = []) =>
    api.post<ApiResponse<AdminUser>>(`/admin/users/${id}/schedule-deletion`, { transfers }),
  cancelDeletion: (id: number) =>
    api.post<ApiResponse<AdminUser>>(`/admin/users/${id}/cancel-deletion`),
  // Permanently delete an account that is already pending deletion, skipping the
  // remaining grace period. Only valid while scheduled_deletion_at is set.
  forceDeletion: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/users/${id}/force-deletion`),
  revokeSessions: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/users/${id}/revoke-sessions`),
  disableTwoFactor: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/users/${id}/disable-2fa`),
  // Reset a user's password; the platform generates a new one and returns it once.
  resetUserPassword: (id: number) =>
    api.post<ApiResponse<{ password: string }>>(`/admin/users/${id}/reset-password`),
  verifyEmail: (id: number) =>
    api.post<ApiResponse<AdminUser>>(`/admin/users/${id}/verify-email`),

  // Workspaces
  listWorkspaces: (page = 0, size = 20, search = '') =>
    api.get<PageableResponse<AdminWorkspace>>('/admin/workspaces', { params: { page, size, search: search || undefined } }),
  getWorkspace: (id: number) => api.get<ApiResponse<AdminWorkspaceDetail>>(`/admin/workspaces/${id}`),

  // Domains (platform-wide). status filters by verification state.
  listDomains: (page = 0, size = 20, search = '', status: DomainStatus | '' = '', workspace = 0) =>
    api.get<PageableResponse<AdminDomain>>('/admin/domains', {
      params: { page, size, search: search || undefined, status: status || undefined, workspace: workspace || undefined },
    }),
  getDomain: (id: number) => api.get<ApiResponse<AdminDomainDetail>>(`/admin/domains/${id}`),
  // Validate ownership via DNS (the standard check).
  verifyDomain: (id: number) => api.post<ApiResponse<Domain>>(`/admin/domains/${id}/verify`),
  // Force-mark verified without a DNS check (admin override; use sparingly).
  forceVerifyDomain: (id: number) => api.post<ApiResponse<Domain>>(`/admin/domains/${id}/force-verify`),
  // Ban a domain platform-wide (forces its routes offline) / lift the ban.
  banDomain: (id: number, reason: string) => api.post<ApiResponse<Domain>>(`/admin/domains/${id}/ban`, { reason }),
  unbanDomain: (id: number) => api.post<ApiResponse<Domain>>(`/admin/domains/${id}/unban`),

  // Routes (platform-wide). status filters by gateway sync status.
  listRoutes: (page = 0, size = 20, search = '', status: RouteSyncStatus | '' = '', workspace = 0) =>
    api.get<PageableResponse<AdminRoute>>('/admin/routes', {
      params: { page, size, search: search || undefined, status: status || undefined, workspace: workspace || undefined },
    }),
  // Re-render every workspace's gateway config from the database.
  resyncRoutes: () => api.post<ApiResponse<ResyncSummary>>('/admin/routes/resync'),
  setWorkspacePrivileged: (id: number, privileged: boolean) =>
    api.patch<ApiResponse<AdminWorkspace>>(`/admin/workspaces/${id}`, { privileged }),
  // Rotate the workspace's encryption key (re-encrypts its secrets under a new
  // DEK version). Returns { version, reencrypted }.
  rotateWorkspaceKey: (id: number) =>
    api.post<ApiResponse<{ version: number; reencrypted: number }>>(`/admin/workspaces/${id}/rotate-key`),
  // Read-only encryption posture (per-workspace keys, auto-rotation, gateway config encryption).
  getEncryptionInfo: () =>
    api.get<ApiResponse<{ encryption_enabled: boolean; per_workspace_keys: boolean; auto_rotate: boolean; rotate_months: number; gateway_config_encryption: boolean }>>('/admin/encryption'),

  // Metrics
  metrics: () => api.get<ApiResponse<PlatformMetrics>>('/admin/metrics'),

  // Events
  listEvents: (search = '', action = '', page = 0, size = 20, order = 'desc', from = '', to = '') =>
    api.get<PageableResponse<AdminEvent>>('/admin/events', {
      params: { search, action, page, size, order, from: from || undefined, to: to || undefined },
    }),
  getEvent: (id: number) => api.get<ApiResponse<AdminEvent>>(`/admin/events/${id}`),

  // Update notice. Served from the cache the daily check writes — this request
  // never reaches GitHub, so refreshing the dashboard costs no API quota.
  getUpdate: () => api.get<ApiResponse<UpdateInfo>>('/admin/update'),
  dismissUpdate: (version: string) =>
    api.post<ApiResponse<{ message: string }>>('/admin/update/dismiss', { version }),

  // Settings
  listSettings: () => api.get<ApiResponse<PlatformSetting[]>>('/admin/settings'),
  updateSettings: (settings: SettingInput[]) =>
    api.put<ApiResponse<PlatformSetting[]>>('/admin/settings', { settings }),

  // Deployment config (platform image catalog)
  getDeploymentConfig: () => api.get<ApiResponse<DeploymentConfig>>('/admin/deployment-config'),
  updateDeploymentConfig: (mirror: string, images: Record<string, string>) =>
    api.put<ApiResponse<DeploymentConfig>>('/admin/deployment-config', { mirror, images }),

  // Jobs
  listJobs: (page = 0, size = 20) =>
    api.get<PageableResponse<JobStatus>>('/admin/jobs', { params: { page, size } }),
  jobStats: () => api.get<ApiResponse<JobStats>>('/admin/jobs/stats'),

  // OAuth providers
  listProviders: () => api.get<ApiResponse<OAuthProvider[]>>('/admin/oauth/providers'),
  createProvider: (payload: OAuthProviderPayload) =>
    api.post<ApiResponse<OAuthProvider>>('/admin/oauth/providers', payload),
  updateProvider: (id: number, payload: Partial<OAuthProviderPayload>) =>
    api.put<ApiResponse<OAuthProvider>>(`/admin/oauth/providers/${id}`, payload),
  deleteProvider: (id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/oauth/providers/${id}`),

  // LDAP / Active Directory connections (Enterprise; gated sso_ldap)
  listLdap: () => api.get<ApiResponse<LdapConfig[]>>('/admin/sso/ldap'),
  createLdap: (payload: LdapConfigPayload) =>
    api.post<ApiResponse<LdapConfig>>('/admin/sso/ldap', payload),
  updateLdap: (id: number, payload: Partial<LdapConfigPayload>) =>
    api.put<ApiResponse<LdapConfig>>(`/admin/sso/ldap/${id}`, payload),
  deleteLdap: (id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/sso/ldap/${id}`),
  testLdap: (id: number) =>
    api.post<ApiResponse<LdapTestResult>>(`/admin/sso/ldap/${id}/test`),
  createLdapMapping: (id: number, payload: LdapMappingPayload) =>
    api.post<ApiResponse<LdapGroupMapping>>(`/admin/sso/ldap/${id}/mappings`, payload),
  deleteLdapMapping: (id: number, mappingId: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/sso/ldap/${id}/mappings/${mappingId}`),

  // Plans (per-workspace resource limits & capabilities)
  listPlans: (search = '', page = 0, size = 20) =>
    api.get<PageableResponse<Plan>>('/admin/plans', { params: { search: search || undefined, page, size } }),
  getPlan: (id: number) => api.get<ApiResponse<Plan>>(`/admin/plans/${id}`),
  createPlan: (payload: PlanInput) => api.post<ApiResponse<Plan>>('/admin/plans', payload),
  updatePlan: (id: number, payload: PlanInput) => api.put<ApiResponse<Plan>>(`/admin/plans/${id}`, payload),
  deletePlan: (id: number, force = false) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/plans/${id}`, { params: { force: force || undefined } }),
  setDefaultPlan: (id: number) => api.post<ApiResponse<Plan>>(`/admin/plans/${id}/default`),
  assignWorkspacePlan: (workspaceId: number, planId: number | null) =>
    api.put<ApiResponse<{ message: string }>>(`/admin/workspaces/${workspaceId}/plan`, { plan_id: planId }),
  getWorkspaceQuota: (workspaceId: number) =>
    api.get<ApiResponse<WorkspaceQuotaOverride>>(`/admin/workspaces/${workspaceId}/quota`),
  setWorkspaceQuota: (workspaceId: number, override: WorkspaceQuotaOverride) =>
    api.put<ApiResponse<WorkspaceQuotaOverride>>(`/admin/workspaces/${workspaceId}/quota`, override),
  clearWorkspaceQuota: (workspaceId: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/workspaces/${workspaceId}/quota`),

  // Commercial license (Enterprise)
  getLicense: () => api.get<ApiResponse<LicenseView>>('/admin/license'),
  installLicense: (token: string) => api.post<ApiResponse<LicenseView>>('/admin/license', { token }),
  removeLicense: () => api.delete<ApiResponse<{ message: string }>>('/admin/license'),
  licenseHealth: () => api.get<ApiResponse<LicenseHealth>>('/admin/license/health'),

  // SIEM audit streaming targets (Enterprise; gated siem_stream)
  listSIEM: () => api.get<ApiResponse<SIEMConfig[]>>('/admin/siem'),
  createSIEM: (payload: SIEMConfigPayload) => api.post<ApiResponse<SIEMConfig>>('/admin/siem', payload),
  updateSIEM: (id: number, payload: Partial<SIEMConfigPayload>) =>
    api.put<ApiResponse<SIEMConfig>>(`/admin/siem/${id}`, payload),
  deleteSIEM: (id: number) => api.delete<ApiResponse<{ message: string }>>(`/admin/siem/${id}`),
  testSIEM: (id: number) => api.post<ApiResponse<{ message: string }>>(`/admin/siem/${id}/test`),
}

export interface SIEMConfigPayload {
  name: string
  sink: string
  endpoint: string
  format?: string
  auth_header?: string
  enabled?: boolean
}
