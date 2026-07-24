import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes = [
  { path: '/login', name: 'login', component: () => import('@/views/auth/Login.vue'), meta: { guest: true, title: 'Sign in' } },
  { path: '/forgot-password', name: 'forgot-password', component: () => import('@/views/auth/ForgotPassword.vue'), meta: { guest: true, title: 'Reset password' } },
  // Not guest-gated: a reset link from email must work even if a session exists.
  { path: '/reset-password', name: 'reset-password', component: () => import('@/views/auth/ResetPassword.vue'), meta: { title: 'Reset password' } },
  { path: '/oauth/callback', name: 'oauth-callback', component: () => import('@/views/auth/OAuthCallback.vue'), meta: { title: 'Signing in' } },
  // "Copy login command": intentionally neither guest- nor auth-gated. It forces a
  // fresh re-authentication (even for a signed-in user) before minting a CLI token,
  // and also handles the SSO hand-off return (?handoff=…).
  { path: '/request-token', name: 'request-token', component: () => import('@/views/auth/RequestToken.vue'), meta: { title: 'CLI login token' } },
  // `miabi login` loopback flow: like request-token, forces a fresh re-auth (even
  // when signed in), then hands a single-use code back to the CLI's local callback
  // (?redirect_uri=…&state=…). Neither guest- nor auth-gated.
  { path: '/cli/authorize', name: 'cli-authorize', component: () => import('@/views/auth/CliAuthorize.vue'), meta: { title: 'Authorize CLI login' } },
  // Forced password change (admin-set/reset credential). The user holds only a
  // short-lived reset token here, not a session; the guard gates it on a pending
  // reset rather than on auth.
  { path: '/change-password', name: 'change-password', component: () => import('@/views/auth/ChangePasswordRequired.vue'), meta: { title: 'Set a new password' } },
  {
    path: '/',
    component: () => import('@/layouts/DashboardLayout.vue'),
    meta: { auth: true },
    children: [
      { path: '', name: 'dashboard', component: () => import('@/views/dashboard/Dashboard.vue'), meta: { title: 'Dashboard' } },
      { path: 'apps', name: 'apps', component: () => import('@/views/apps/Apps.vue'), meta: { title: 'Applications' } },
      { path: 'analytics', name: 'analytics', component: () => import('@/views/analytics/Overview.vue'), meta: { title: 'Analytics' } },
      { path: 'notifications', name: 'notifications-inbox', component: () => import('@/views/notifications/Inbox.vue'), meta: { title: 'Notifications', noWorkspace: true } },
      { path: 'analytics/http', name: 'analytics-http', component: () => import('@/views/analytics/HttpTraffic.vue'), meta: { title: 'HTTP Traffic' } },
      { path: 'analytics/performance', name: 'analytics-performance', component: () => import('@/views/analytics/Performance.vue'), meta: { title: 'Performance' } },
      { path: 'analytics/web', name: 'analytics-web', component: () => import('@/views/analytics/WebAnalytics.vue'), meta: { title: 'Web Analytics' } },
      { path: 'apps/:id', name: 'app-detail', component: () => import('@/views/apps/AppDetail.vue'), meta: { title: 'Application' } },
      { path: 'stacks', name: 'stacks', component: () => import('@/views/stacks/Stacks.vue'), meta: { title: 'Stacks' } },
      { path: 'stacks/:id', name: 'stack-detail', component: () => import('@/views/stacks/StackDetail.vue'), meta: { title: 'Stack' } },
      { path: 'databases', name: 'databases', component: () => import('@/views/databases/Databases.vue'), meta: { title: 'Databases' } },
      { path: 'databases/:id', name: 'database-detail', component: () => import('@/views/databases/DatabaseDetail.vue'), meta: { title: 'Database' } },
      { path: 'volumes', name: 'volumes', component: () => import('@/views/volumes/Volumes.vue'), meta: { title: 'Volumes' } },
      { path: 'volumes/:id', name: 'volume-detail', component: () => import('@/views/volumes/VolumeDetail.vue'), meta: { title: 'Volume' } },
      { path: 'jobs', name: 'jobs', component: () => import('@/views/jobs/Jobs.vue'), meta: { title: 'Jobs' } },
      { path: 'marketplace', name: 'marketplace', component: () => import('@/views/marketplace/Marketplace.vue'), meta: { title: 'Marketplace' } },
      { path: 'marketplace/:slug', name: 'template-install', component: () => import('@/views/marketplace/TemplateInstall.vue'), meta: { title: 'Install template' } },
      { path: 'gitops', name: 'gitops', component: () => import('@/views/gitops/GitOps.vue'), meta: { title: 'GitOps' } },
      { path: 'gitops/:id', name: 'gitops-detail', component: () => import('@/views/gitops/GitOpsDetail.vue'), meta: { title: 'GitOps Project' } },
      { path: 'pipelines', name: 'pipelines', component: () => import('@/views/pipelines/Pipelines.vue'), meta: { title: 'Pipelines' } },
      { path: 'pipelines/:id/runs', name: 'pipeline-runs', component: () => import('@/views/pipelines/PipelineRuns.vue'), meta: { title: 'Pipeline Runs' } },
      { path: 'pipelines/:id/runs/:runId', name: 'pipeline-run', component: () => import('@/views/pipelines/PipelineRunDetail.vue'), meta: { title: 'Pipeline Run' } },
      { path: 'runners', name: 'runners', component: () => import('@/views/runners/Runners.vue'), meta: { title: 'Runners' } },
      { path: 'runners/:id', name: 'runner-detail', component: () => import('@/views/runners/RunnerDetail.vue'), meta: { title: 'Runner' } },
      { path: 'releases', name: 'releases', component: () => import('@/views/releases/Releases.vue'), meta: { title: 'Releases' } },
      { path: 'environments', name: 'environments', component: () => import('@/views/environments/Environments.vue'), meta: { title: 'Environments' } },
      { path: 'networks', name: 'networks', component: () => import('@/views/networking/Networks.vue'), meta: { title: 'Networks' } },
      { path: 'domains', name: 'domains', component: () => import('@/views/networking/Domains.vue'), meta: { title: 'Domains' } },
      { path: 'dns-providers', name: 'dns-providers', component: () => import('@/views/networking/DnsProviders.vue'), meta: { title: 'DNS Providers' } },
      { path: 'routes', name: 'routes', component: () => import('@/views/networking/Routes.vue'), meta: { title: 'Routes' } },
      { path: 'routes/:id', name: 'route-detail', component: () => import('@/views/networking/RouteDetail.vue'), meta: { title: 'Route' } },
      { path: 'middlewares', name: 'middlewares', component: () => import('@/views/networking/Middlewares.vue'), meta: { title: 'Middlewares' } },
      { path: 'middlewares/:id', name: 'middleware-detail', component: () => import('@/views/networking/MiddlewareDetail.vue'), meta: { title: 'Middleware' } },
      { path: 'certificates', name: 'certificates', component: () => import('@/views/networking/Certificates.vue'), meta: { title: 'Certificates' } },
      { path: 'certificates/:id', name: 'certificate-detail', component: () => import('@/views/networking/CertificateDetail.vue'), meta: { title: 'Certificate' } },
      { path: 'secrets', name: 'secrets', component: () => import('@/views/secrets/Secrets.vue'), meta: { title: 'Secrets' } },
      { path: 'registries', name: 'registries', component: () => import('@/views/sources/Registries.vue'), meta: { title: 'Registries' } },
      { path: 'git-repositories', name: 'git-repositories', component: () => import('@/views/sources/GitRepositories.vue'), meta: { title: 'Git Repositories' } },
      { path: 'api-keys', name: 'api-keys', component: () => import('@/views/apikeys/ApiKeys.vue'), meta: { title: 'API Keys' } },
      { path: 'registry', name: 'registry', component: () => import('@/views/registry/Registry.vue'), meta: { title: 'Container Registry' } },
      { path: 'account/profile', name: 'account-profile', component: () => import('@/views/account/Profile.vue'), meta: { title: 'Profile', noWorkspace: true } },
      { path: 'account/security', name: 'account-security', component: () => import('@/views/account/Security.vue'), meta: { title: 'Security', noWorkspace: true } },
      { path: 'about', name: 'about', component: () => import('@/views/About.vue'), meta: { title: 'About' } },
      { path: 'webhooks', name: 'webhooks', component: () => import('@/views/notifications/Webhooks.vue'), meta: { title: 'Webhooks' } },
      { path: 'workspaces', name: 'workspaces', component: () => import('@/views/workspaces/Workspaces.vue'), meta: { title: 'Workspaces' } },
      { path: 'workspaces/:id', name: 'workspace-detail', component: () => import('@/views/workspaces/WorkspaceSettings.vue'), meta: { title: 'Workspace' } },
      { path: 'events', name: 'workspace-events', component: () => import('@/views/workspaces/Events.vue'), meta: { title: 'Events' } },
      { path: 'audit-log', name: 'audit-log', component: () => import('@/views/workspaces/AuditLog.vue'), meta: { title: 'Audit Log' } },
      { path: 'audit-log/:id', name: 'audit-log-detail', component: () => import('@/views/workspaces/AuditLogDetail.vue'), meta: { title: 'Audit Entry' } },
      { path: 'admin/users', name: 'admin-users', component: () => import('@/views/admin/Users.vue'), meta: { title: 'Users', admin: true } },
      { path: 'admin/users/:id', name: 'admin-user-detail', component: () => import('@/views/admin/UserDetail.vue'), meta: { title: 'User', admin: true } },
      { path: 'admin/workspaces', name: 'admin-workspaces', component: () => import('@/views/admin/Workspaces.vue'), meta: { title: 'Workspaces', admin: true } },
      { path: 'admin/workspaces/:id', name: 'admin-workspace-detail', component: () => import('@/views/admin/WorkspaceDetail.vue'), meta: { title: 'Workspace', admin: true } },
      { path: 'admin/domains', name: 'admin-domains', component: () => import('@/views/admin/Domains.vue'), meta: { title: 'Domains', admin: true } },
      { path: 'admin/routes', name: 'admin-routes', component: () => import('@/views/admin/Routes.vue'), meta: { title: 'Routes', admin: true } },
      { path: 'admin/domains/:id', name: 'admin-domain-detail', component: () => import('@/views/admin/DomainDetail.vue'), meta: { title: 'Domain', admin: true } },
      { path: 'admin/nodes', name: 'admin-nodes', component: () => import('@/views/admin/Nodes.vue'), meta: { title: 'Nodes', admin: true } },
      { path: 'admin/runners', name: 'admin-runners', component: () => import('@/views/admin/Runners.vue'), meta: { title: 'Shared Runners', admin: true } },
      { path: 'admin/runners/:id', name: 'admin-runner-detail', component: () => import('@/views/runners/RunnerDetail.vue'), meta: { title: 'Runner', admin: true } },
      { path: 'admin/nodes/:id', name: 'admin-node-detail', component: () => import('@/views/admin/NodeDetail.vue'), meta: { title: 'Node', admin: true } },
      { path: 'admin/nodes/:id/import', name: 'admin-node-import', component: () => import('@/views/admin/NodeImport.vue'), meta: { title: 'Import resources', admin: true } },
      { path: 'admin/nodes/:id/housekeeping', name: 'admin-node-housekeeping', component: () => import('@/views/admin/NodeHousekeeping.vue'), meta: { title: 'Housekeeping', admin: true } },
      { path: 'admin/nodes/:id/containers/:cid', name: 'admin-node-container', component: () => import('@/views/admin/NodeContainerDetail.vue'), meta: { title: 'Container', admin: true } },
      { path: 'admin/metrics', name: 'admin-metrics', component: () => import('@/views/admin/Metrics.vue'), meta: { title: 'Admin Dashboard', admin: true } },
      { path: 'admin/events', name: 'admin-events', component: () => import('@/views/admin/Events.vue'), meta: { title: 'Events', admin: true } },
      { path: 'admin/jobs', name: 'admin-jobs', component: () => import('@/views/admin/Jobs.vue'), meta: { title: 'Jobs', admin: true } },
      { path: 'admin/oauth', name: 'admin-oauth', component: () => import('@/views/admin/OAuthProviders.vue'), meta: { title: 'OAuth Providers', admin: true } },
      { path: 'admin/ldap', name: 'admin-ldap', component: () => import('@/views/admin/LdapDirectory.vue'), meta: { title: 'LDAP / Active Directory', admin: true } },
      { path: 'admin/plans', name: 'admin-plans', component: () => import('@/views/admin/Plans.vue'), meta: { title: 'Plans', admin: true } },
      { path: 'admin/plans/:id', name: 'admin-plan-detail', component: () => import('@/views/admin/PlanDetail.vue'), meta: { title: 'Plan', admin: true } },
      { path: 'admin/license', name: 'admin-license', component: () => import('@/views/admin/License.vue'), meta: { title: 'License', admin: true } },
      { path: 'admin/siem', name: 'admin-siem', component: () => import('@/views/admin/SIEM.vue'), meta: { title: 'SIEM Streaming', admin: true } },
      { path: 'admin/platform-backup', name: 'admin-platform-backup', component: () => import('@/views/admin/PlatformBackup.vue'), meta: { title: 'Platform Backup', admin: true } },
      { path: 'admin/registry', name: 'admin-registry', component: () => import('@/views/admin/Registry.vue'), meta: { title: 'Container Registry', admin: true } },
      { path: 'admin/settings', name: 'admin-settings', component: () => import('@/views/admin/Settings.vue'), meta: { title: 'Platform Settings', admin: true } },
      { path: 'admin/deployment-config', name: 'admin-deployment-config', component: () => import('@/views/admin/DeploymentConfig.vue'), meta: { title: 'Deployment Config', admin: true } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  // A pending forced password change holds only a short-lived reset token (no
  // session): the change-password screen is the only route reachable until it's
  // done, and it's unreachable otherwise.
  if (auth.pendingReset) {
    return to.name === 'change-password' ? true : { name: 'change-password' }
  }
  if (to.name === 'change-password') return { name: auth.isAuthenticated ? 'dashboard' : 'login' }
  if (to.meta.auth && !auth.isAuthenticated) return { name: 'login' }
  if (to.meta.guest && auth.isAuthenticated) return { name: 'dashboard' }
  if (to.meta.admin && !auth.isAdmin) return { name: 'dashboard' }
  const title = to.meta.title as string | undefined
  document.title = title ? `${title} — Miabi` : 'Miabi'
  return true
})

export default router
