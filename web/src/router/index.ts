import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useElevationStore } from '@/stores/elevation'
import { useBrandStore } from '@/stores/brand'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { t } from '@/i18n'
import { safeNext } from '@/utils/safeNext'
import { isChunkLoadError, markUpdated, shouldReloadForChunk } from '@/composables/useUpdateNotice'

const routes = [
  { path: '/login', name: 'login', component: () => import('@/views/auth/Login.vue'), meta: { guest: true, title: 'Sign in', titleKey: 'login.signIn' } },
  { path: '/register', name: 'register', component: () => import('@/views/auth/Register.vue'), meta: { guest: true, title: 'Create account', titleKey: 'serviceAccounts.create' } },
  // Not guest-gated: a verification link from email must work even if a session exists.
  { path: '/verify-email', name: 'verify-email', component: () => import('@/views/auth/VerifyEmail.vue'), meta: { title: 'Verify email', titleKey: 'pageTitle.verifyEmail' } },
  { path: '/forgot-password', name: 'forgot-password', component: () => import('@/views/auth/ForgotPassword.vue'), meta: { guest: true, title: 'Reset password', titleKey: 'pageTitle.forgotPassword' } },
  // Not guest-gated: a reset link from email must work even if a session exists.
  { path: '/reset-password', name: 'reset-password', component: () => import('@/views/auth/ResetPassword.vue'), meta: { title: 'Reset password', titleKey: 'pageTitle.resetPassword' } },
  // Neither guest- nor auth-gated: it signs in, signs up or accepts depending on who opens it.
  { path: '/invitations/accept', name: 'accept-invitation', component: () => import('@/views/auth/AcceptInvitation.vue'), meta: { title: 'Invitation', titleKey: 'pageTitle.acceptInvitation' } },
  { path: '/oauth/callback', name: 'oauth-callback', component: () => import('@/views/auth/OAuthCallback.vue'), meta: { title: 'Signing in', titleKey: 'pageTitle.oauthCallback' } },
  // Neither guest- nor auth-gated: it forces re-auth before minting a CLI token and handles the SSO hand-off return.
  { path: '/request-token', name: 'request-token', component: () => import('@/views/auth/RequestToken.vue'), meta: { title: 'CLI login token', titleKey: 'pageTitle.requestToken' } },
  // Neither guest- nor auth-gated: forces re-auth, then hands a single-use code to the CLI's local callback.
  { path: '/cli/authorize', name: 'cli-authorize', component: () => import('@/views/auth/CliAuthorize.vue'), meta: { title: 'Authorize CLI login', titleKey: 'cliAuthorize.sessionTitle' } },
  // The user holds only a short-lived reset token, not a session, so the guard gates on a pending reset.
  { path: '/change-password', name: 'change-password', component: () => import('@/views/auth/ChangePasswordRequired.vue'), meta: { title: 'Set a new password', titleKey: 'changePassword.setANewPassword' } },
  {
    path: '/',
    component: () => import('@/layouts/WorkspaceLayout.vue'),
    meta: { auth: true },
    children: [
      { path: '', name: 'dashboard', component: () => import('@/views/dashboard/Dashboard.vue'), meta: { title: 'Dashboard', titleKey: 'nav.overview.dashboard' } },
      { path: 'apps', name: 'apps', component: () => import('@/views/apps/Apps.vue'), meta: { title: 'Applications', titleKey: 'nav.deploy.applications' } },
      { path: 'analytics', name: 'analytics', component: () => import('@/views/analytics/Overview.vue'), meta: { title: 'Analytics', titleKey: 'nav.analytics.title' } },
      { path: 'notifications', name: 'notifications-inbox', component: () => import('@/views/notifications/Inbox.vue'), meta: { title: 'Notifications', titleKey: 'nav.workspace.notifications', noWorkspace: true } },
      { path: 'analytics/http', name: 'analytics-http', component: () => import('@/views/analytics/HttpTraffic.vue'), meta: { title: 'HTTP Traffic', titleKey: 'nav.analytics.httpTraffic' } },
      { path: 'analytics/performance', name: 'analytics-performance', component: () => import('@/views/analytics/Performance.vue'), meta: { title: 'Performance', titleKey: 'nav.analytics.performance' } },
      { path: 'analytics/web', name: 'analytics-web', component: () => import('@/views/analytics/WebAnalytics.vue'), meta: { title: 'Web Analytics', titleKey: 'nav.analytics.webAnalytics' } },
      { path: 'apps/:id', name: 'app-detail', component: () => import('@/views/apps/AppDetail.vue'), meta: { title: 'Application', titleKey: 'pageTitle.appDetail' } },
      { path: 'stacks', name: 'stacks', component: () => import('@/views/stacks/Stacks.vue'), meta: { title: 'Stacks', titleKey: 'nav.deploy.stacks' } },
      { path: 'stacks/:id', name: 'stack-detail', component: () => import('@/views/stacks/StackDetail.vue'), meta: { title: 'Stack', titleKey: 'pageTitle.stackDetail' } },
      { path: 'databases', name: 'databases', component: () => import('@/views/databases/Databases.vue'), meta: { title: 'Databases', titleKey: 'nav.data.databases' } },
      { path: 'databases/:id', name: 'database-detail', component: () => import('@/views/databases/DatabaseDetail.vue'), meta: { title: 'Database', titleKey: 'pageTitle.databaseDetail' } },
      { path: 'volumes', name: 'volumes', component: () => import('@/views/volumes/Volumes.vue'), meta: { title: 'Volumes', titleKey: 'nav.data.volumes' } },
      { path: 'volumes/:id', name: 'volume-detail', component: () => import('@/views/volumes/VolumeDetail.vue'), meta: { title: 'Volume', titleKey: 'pageTitle.volumeDetail' } },
      { path: 'jobs', name: 'jobs', component: () => import('@/views/jobs/Jobs.vue'), meta: { title: 'Jobs', titleKey: 'nav.deploy.jobs' } },
      { path: 'marketplace', name: 'marketplace', component: () => import('@/views/marketplace/Marketplace.vue'), meta: { title: 'Marketplace', titleKey: 'nav.deploy.marketplace' } },
      { path: 'marketplace/:slug', name: 'template-install', component: () => import('@/views/marketplace/TemplateInstall.vue'), meta: { title: 'Install template', titleKey: 'pageTitle.templateInstall' } },
      { path: 'gitops', name: 'gitops', component: () => import('@/views/gitops/GitOps.vue'), meta: { title: 'GitOps', titleKey: 'nav.cicd.gitops' } },
      { path: 'gitops/:id', name: 'gitops-detail', component: () => import('@/views/gitops/GitOpsDetail.vue'), meta: { title: 'GitOps Project', titleKey: 'pageTitle.gitopsDetail' } },
      { path: 'pipelines', name: 'pipelines', component: () => import('@/views/pipelines/Pipelines.vue'), meta: { title: 'Pipelines', titleKey: 'nav.cicd.pipelines' } },
      { path: 'pipelines/:id/runs', name: 'pipeline-runs', component: () => import('@/views/pipelines/PipelineRuns.vue'), meta: { title: 'Pipeline Runs', titleKey: 'pageTitle.pipelineRuns' } },
      { path: 'pipelines/:id/runs/:runId', name: 'pipeline-run', component: () => import('@/views/pipelines/PipelineRunDetail.vue'), meta: { title: 'Pipeline Run', titleKey: 'pageTitle.pipelineRun' } },
      { path: 'pipelines/versioning/:uid', name: 'version-policy', component: () => import('@/views/versioning/VersionPolicyDetail.vue'), meta: { title: 'Version policy', titleKey: 'versioning.policy' } },
      { path: 'runners', name: 'runners', component: () => import('@/views/runners/Runners.vue'), meta: { title: 'Runners', titleKey: 'nav.cicd.runners' } },
      { path: 'runners/:id', name: 'runner-detail', component: () => import('@/views/runners/RunnerDetail.vue'), meta: { title: 'Runner', titleKey: 'pageTitle.runnerDetail' } },
      { path: 'releases', name: 'releases', component: () => import('@/views/releases/Releases.vue'), meta: { title: 'Releases', titleKey: 'nav.cicd.releases' } },
      { path: 'environments', name: 'environments', component: () => import('@/views/environments/Environments.vue'), meta: { title: 'Environments', titleKey: 'nav.cicd.environments' } },
      { path: 'networks', name: 'networks', component: () => import('@/views/networking/Networks.vue'), meta: { title: 'Networks', titleKey: 'nav.networking.networks' } },
      { path: 'networks/:id', name: 'network-detail', component: () => import('@/views/networking/NetworkDetail.vue'), meta: { title: 'Network', titleKey: 'pageTitle.networkDetail' } },
      { path: 'domains', name: 'domains', component: () => import('@/views/networking/Domains.vue'), meta: { title: 'Domains', titleKey: 'nav.networking.domains' } },
      { path: 'domains/:id', name: 'domain-detail', component: () => import('@/views/networking/DomainDetail.vue'), meta: { title: 'Domain', titleKey: 'pageTitle.domainDetail' } },
      { path: 'dns-providers', name: 'dns-providers', component: () => import('@/views/networking/DnsProviders.vue'), meta: { title: 'DNS Providers', titleKey: 'nav.networking.dnsProviders' } },
      { path: 'routes', name: 'routes', component: () => import('@/views/networking/Routes.vue'), meta: { title: 'Routes', titleKey: 'nav.networking.routes' } },
      { path: 'routes/:id', name: 'route-detail', component: () => import('@/views/networking/RouteDetail.vue'), meta: { title: 'Route', titleKey: 'pageTitle.routeDetail' } },
      { path: 'middlewares', name: 'middlewares', component: () => import('@/views/networking/Middlewares.vue'), meta: { title: 'Middlewares', titleKey: 'nav.networking.middlewares' } },
      { path: 'middlewares/:id', name: 'middleware-detail', component: () => import('@/views/networking/MiddlewareDetail.vue'), meta: { title: 'Middleware', titleKey: 'pageTitle.middlewareDetail' } },
      { path: 'certificates', name: 'certificates', component: () => import('@/views/networking/Certificates.vue'), meta: { title: 'Certificates', titleKey: 'nav.networking.certificates' } },
      { path: 'certificates/:id', name: 'certificate-detail', component: () => import('@/views/networking/CertificateDetail.vue'), meta: { title: 'Certificate', titleKey: 'pageTitle.certificateDetail' } },
      { path: 'secrets', name: 'secrets', component: () => import('@/views/secrets/Secrets.vue'), meta: { title: 'Secrets', titleKey: 'nav.sources.secrets' } },
      { path: 'configs', name: 'configs', component: () => import('@/views/configs/Configs.vue'), meta: { title: 'Configs', titleKey: 'nav.sources.configs' } },
      { path: 'registries', name: 'registries', component: () => import('@/views/sources/Registries.vue'), meta: { title: 'Registries', titleKey: 'nav.sources.registries' } },
      { path: 'git-repositories', name: 'git-repositories', component: () => import('@/views/sources/GitRepositories.vue'), meta: { title: 'Git Repositories', titleKey: 'nav.sources.gitRepositories' } },
      { path: 'git-repositories/:id', name: 'git-repository', component: () => import('@/views/sources/GitRepositoryDetail.vue'), meta: { title: 'Git Repository', titleKey: 'pageTitle.gitRepository' } },
      { path: 'api-keys', name: 'api-keys', component: () => import('@/views/apikeys/ApiKeys.vue'), meta: { title: 'API Keys', titleKey: 'nav.developers.apiKeys' } },
      { path: 'registry', name: 'registry', component: () => import('@/views/registry/Registry.vue'), meta: { title: 'Container Registry', titleKey: 'nav.developers.containerRegistry' } },
      // The :repo param is a catch-all: an image name may contain slashes.
      { path: 'registry/images/:repo(.*)', name: 'registry-image', component: () => import('@/views/registry/ImageDetail.vue'), meta: { title: 'Image', titleKey: 'pageTitle.registryImage' } },
      { path: 'account/profile', name: 'account-profile', component: () => import('@/views/account/Profile.vue'), meta: { title: 'Profile', titleKey: 'shell.menu.profile', noWorkspace: true } },
      { path: 'account/preferences', name: 'account-preferences', component: () => import('@/views/account/Preferences.vue'), meta: { title: 'Preferences', titleKey: 'shell.menu.preferences', noWorkspace: true } },
      { path: 'account/security', name: 'account-security', component: () => import('@/views/account/Security.vue'), meta: { title: 'Security', titleKey: 'shell.menu.security', noWorkspace: true } },
      { path: 'account/cli', name: 'account-cli', component: () => import('@/views/account/CliAccess.vue'), meta: { title: 'CLI access', titleKey: 'shell.menu.cli', noWorkspace: true } },
      { path: 'about', name: 'about', component: () => import('@/views/About.vue'), meta: { title: 'About', titleKey: 'shell.menu.about' } },
      { path: 'webhooks', name: 'webhooks', component: () => import('@/views/notifications/Webhooks.vue'), meta: { title: 'Webhooks', titleKey: 'nav.developers.webhooks' } },
      { path: 'generator', name: 'generator', component: () => import('@/views/generator/Generator.vue'), meta: { title: 'Generator', titleKey: 'nav.developers.generator' } },
      { path: 'workspaces', name: 'workspaces', component: () => import('@/views/workspaces/Workspaces.vue'), meta: { title: 'Workspaces', titleKey: 'workspaces.workspaces' } },
      { path: 'workspaces/import', name: 'workspace-import', component: () => import('@/views/workspaces/ImportWorkspace.vue'), meta: { title: 'Import from another Miabi', titleKey: 'switcher.import', noWorkspace: true } },
      { path: 'workspaces/:id', name: 'workspace-detail', component: () => import('@/views/workspaces/WorkspaceSettings.vue'), meta: { title: 'Workspace', titleKey: 'nav.workspace.title' } },
      { path: 'events', name: 'workspace-events', component: () => import('@/views/workspaces/Events.vue'), meta: { title: 'Events', titleKey: 'nav.workspace.events' } },
      { path: 'audit-log', name: 'audit-log', component: () => import('@/views/workspaces/AuditLog.vue'), meta: { title: 'Audit Log', titleKey: 'nav.workspace.auditLog' } },
      { path: 'audit-log/:id', name: 'audit-log-detail', component: () => import('@/views/workspaces/AuditLogDetail.vue'), meta: { title: 'Audit Entry', titleKey: 'audit.auditEntry' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
  {
    path: '/admin',
    component: () => import('@/layouts/AdminLayout.vue'),
    meta: { auth: true, admin: true },
    children: [
      // The console switcher and a typed URL both need somewhere to land.
      { path: '', redirect: { name: 'admin-dashboard' } },
      { path: 'users', name: 'admin-users', component: () => import('@/views/admin/Users.vue'), meta: { title: 'Users', admin: true } },
      { path: 'users/:id', name: 'admin-user-detail', component: () => import('@/views/admin/UserDetail.vue'), meta: { title: 'User', admin: true } },
      { path: 'organizations', name: 'admin-organizations', component: () => import('@/views/admin/Organizations.vue'), meta: { title: 'Organizations', admin: true } },
      { path: 'organizations/:id', name: 'admin-organization-detail', component: () => import('@/views/admin/OrganizationDetail.vue'), meta: { title: 'Organization', admin: true } },
      { path: 'workspaces', name: 'admin-workspaces', component: () => import('@/views/admin/Workspaces.vue'), meta: { title: 'Workspaces', admin: true } },
      { path: 'workspaces/:id', name: 'admin-workspace-detail', component: () => import('@/views/admin/WorkspaceDetail.vue'), meta: { title: 'Workspace', admin: true } },
      { path: 'domains', name: 'admin-domains', component: () => import('@/views/admin/Domains.vue'), meta: { title: 'Domains', admin: true } },
      { path: 'routes', name: 'admin-routes', component: () => import('@/views/admin/Routes.vue'), meta: { title: 'Routes', admin: true } },
      { path: 'networks', name: 'admin-networks', component: () => import('@/views/admin/Networks.vue'), meta: { title: 'Networks', admin: true } },
      { path: 'domains/:id', name: 'admin-domain-detail', component: () => import('@/views/admin/DomainDetail.vue'), meta: { title: 'Domain', admin: true } },
      { path: 'clusters', name: 'admin-clusters', component: () => import('@/views/admin/Clusters.vue'), meta: { title: 'Clusters', admin: true } },
      { path: 'clusters/:id', name: 'admin-cluster-detail', component: () => import('@/views/admin/ClusterDetail.vue'), meta: { title: 'Cluster', admin: true } },
      { path: 'nodes', name: 'admin-nodes', component: () => import('@/views/admin/Nodes.vue'), meta: { title: 'Nodes', admin: true } },
      { path: 'grants', name: 'admin-grants', component: () => import('@/views/admin/Grants.vue'), meta: { title: 'Kernel grants', admin: true } },
      { path: 'ports', redirect: { name: 'admin-security', query: { tab: 'ports' } } },
      { path: 'security', name: 'admin-security', component: () => import('@/views/admin/Security.vue'), meta: { title: 'Security Center', admin: true } },
      { path: 'runners', name: 'admin-runners', component: () => import('@/views/admin/Runners.vue'), meta: { title: 'Shared Runners', admin: true } },
      { path: 'runners/:id', name: 'admin-runner-detail', component: () => import('@/views/runners/RunnerDetail.vue'), meta: { title: 'Runner', admin: true } },
      { path: 'nodes/:id', name: 'admin-node-detail', component: () => import('@/views/admin/NodeDetail.vue'), meta: { title: 'Node', admin: true } },
      { path: 'nodes/:id/import', name: 'admin-node-import', component: () => import('@/views/admin/NodeImport.vue'), meta: { title: 'Import resources', admin: true } },
      { path: 'nodes/:id/housekeeping', name: 'admin-node-housekeeping', component: () => import('@/views/admin/NodeHousekeeping.vue'), meta: { title: 'Housekeeping', admin: true } },
      { path: 'nodes/:id/containers/:cid', name: 'admin-node-container', component: () => import('@/views/admin/NodeContainerDetail.vue'), meta: { title: 'Container', admin: true } },
      { path: 'dashboard', name: 'admin-dashboard', component: () => import('@/views/admin/Dashboard.vue'), meta: { title: 'Admin Dashboard', admin: true } },
      // Old bookmarks and links.
      { path: 'metrics', redirect: { name: 'admin-dashboard' } },
      { path: 'events', name: 'admin-events', component: () => import('@/views/admin/Events.vue'), meta: { title: 'Events', admin: true } },
      { path: 'jobs', name: 'admin-jobs', component: () => import('@/views/admin/Jobs.vue'), meta: { title: 'Jobs', admin: true } },
      { path: 'reconciliation', name: 'admin-reconciliation', component: () => import('@/views/admin/Reconciliation.vue'), meta: { title: 'Reconciliation', admin: true } },
      { path: 'oauth', name: 'admin-oauth', component: () => import('@/views/admin/OAuthProviders.vue'), meta: { title: 'OAuth Providers', admin: true } },
      { path: 'ldap', name: 'admin-ldap', component: () => import('@/views/admin/LdapDirectory.vue'), meta: { title: 'LDAP / Active Directory', admin: true } },
      { path: 'plans', name: 'admin-plans', component: () => import('@/views/admin/Plans.vue'), meta: { title: 'Plans & Quotas', admin: true } },
      { path: 'plans/:id', name: 'admin-plan-detail', component: () => import('@/views/admin/PlanDetail.vue'), meta: { title: 'Plan', admin: true } },
      { path: 'database-sizes', name: 'admin-database-sizes', component: () => import('@/views/admin/DatabaseSizes.vue'), meta: { title: 'Database sizes', admin: true } },
      { path: 'storage-classes', name: 'admin-storage-classes', component: () => import('@/views/admin/StorageClasses.vue'), meta: { title: 'Storage classes', admin: true } },
      { path: 'storage-classes/:id', name: 'admin-storage-class-detail', component: () => import('@/views/admin/StorageClassDetail.vue'), meta: { title: 'Storage class', admin: true } },
      { path: 'license', name: 'admin-license', component: () => import('@/views/admin/License.vue'), meta: { title: 'License', admin: true } },
      { path: 'billing', name: 'admin-billing', component: () => import('@/views/admin/Billing.vue'), meta: { title: 'Billing', admin: true } },
      { path: 'siem', name: 'admin-siem', component: () => import('@/views/admin/SIEM.vue'), meta: { title: 'SIEM Streaming', admin: true } },
      { path: 'announcements', name: 'admin-announcements', component: () => import('@/views/admin/Announcements.vue'), meta: { title: 'Announcements', admin: true } },
      { path: 'migration', name: 'admin-migration', component: () => import('@/views/admin/Migration.vue'), meta: { title: 'Migration', admin: true } },
      { path: 'platform-backup', name: 'admin-platform-backup', component: () => import('@/views/admin/PlatformBackup.vue'), meta: { title: 'Platform Backup', admin: true } },
      { path: 'registry', name: 'admin-registry', component: () => import('@/views/admin/Registry.vue'), meta: { title: 'Container Registry', admin: true } },
      { path: 'settings', name: 'admin-settings', component: () => import('@/views/admin/Settings.vue'), meta: { title: 'Platform Settings', admin: true } },
      { path: 'branding', name: 'admin-branding', component: () => import('@/views/admin/Branding.vue'), meta: { title: 'Branding', admin: true } },
      { path: 'deployment-config', name: 'admin-deployment-config', component: () => import('@/views/admin/DeploymentConfig.vue'), meta: { title: 'Deployment Config', admin: true } },
    ],
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// pendingWorkspaceKey keeps a ?workspace= link across sign-in, which may leave the site for SSO.
const pendingWorkspaceKey = 'mb_pending_workspace'

function pendingWorkspace(set?: string | null): string {
  try {
    if (set === null) sessionStorage.removeItem(pendingWorkspaceKey)
    else if (set) sessionStorage.setItem(pendingWorkspaceKey, set)
    return sessionStorage.getItem(pendingWorkspaceKey) ?? ''
  } catch {
    return set ?? ''
  }
}

router.beforeEach(async (to, from) => {
  const auth = useAuthStore()
  const linked = typeof to.query.workspace === 'string' ? to.query.workspace.trim() : ''
  const wanted = pendingWorkspace(linked || undefined)
  if (wanted && auth.isAuthenticated && !auth.pendingReset) {
    pendingWorkspace(null)
    if (!(await useWorkspaceStore().selectByRef(wanted).catch(() => false))) {
      useNotificationStore().error(t('notify.common.workspaceLinkNotFound', { name: wanted }))
    }
    if (linked) {
      const { workspace: _, ...query } = to.query
      return { path: to.path, query, hash: to.hash, replace: true }
    }
  }
  // A pending reset holds no session: change-password is the only reachable route, and unreachable otherwise.
  if (auth.pendingReset) {
    return to.name === 'change-password' ? true : { name: 'change-password' }
  }
  if (to.name === 'change-password') return { name: auth.isAuthenticated ? 'dashboard' : 'login' }
  if (to.meta.auth && !auth.isAuthenticated) {
    return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  }
  // The SSO second-factor step may finish a CLI login from a signed-in browser; redirecting would strand the CLI.
  if (to.meta.guest && auth.isAuthenticated && !to.query.sso_two_factor) return safeNext(to.query.next) || { name: 'dashboard' }
  if (to.meta.admin && !auth.isAdmin) return { name: 'dashboard' }
  if (to.meta.admin && !(await useElevationStore().ensure())) {
    // Declined the unlock: stay where they were, or land on the workspace side on a fresh load.
    return from.name ? false : { name: 'dashboard' }
  }
  useBrandStore().setTitle(to.meta.title as string | undefined, to.meta.titleKey as string | undefined)
  return true
})

// After an upgrade the old page's lazy chunks are gone: load the target page fresh instead of failing silently.
router.onError((err, to) => {
  if (!isChunkLoadError(err)) return
  const key = 'miabi.chunk-reload'
  let last = 0
  try { last = Number(sessionStorage.getItem(key) ?? 0) } catch { /* storage unavailable */ }
  if (!shouldReloadForChunk(last)) { markUpdated(); return }
  try { sessionStorage.setItem(key, String(Date.now())) } catch { /* storage unavailable */ }
  window.location.assign(to.fullPath)
})

export default router
