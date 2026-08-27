export interface NavItem {
  name: string
  path: string
  icon: string
  requiresWorkspace?: boolean
  requiresWorkspaceAdmin?: boolean
  requiresAdmin?: boolean
  requiresDocs?: boolean
  external?: boolean
  workspaceTab?: 'settings' | 'members' | 'audit' | 'notifications'
}

export interface NavSection {
  id: string
  title: string
  items: NavItem[]
  defaultOpen?: boolean
}

export const navSections: NavSection[] = [
  {
    id: 'overview',
    title: 'Overview',
    items: [{ name: 'Dashboard', path: '/', icon: 'mdi-view-dashboard-outline' }],
  },
  {
    id: 'analytics',
    title: 'Analytics',
    items: [
      { name: 'Overview', path: '/analytics', icon: 'mdi-chart-areaspline', requiresWorkspace: true },
      { name: 'HTTP Traffic', path: '/analytics/http', icon: 'mdi-earth', requiresWorkspace: true },
      { name: 'Performance', path: '/analytics/performance', icon: 'mdi-speedometer', requiresWorkspace: true },
      { name: 'Web Analytics', path: '/analytics/web', icon: 'mdi-account-group-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'deploy',
    title: 'Deploy',
    items: [
      { name: 'Applications', path: '/apps', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { name: 'Stacks', path: '/stacks', icon: 'mdi-layers-outline', requiresWorkspace: true },
      { name: 'Jobs', path: '/jobs', icon: 'mdi-console-line', requiresWorkspace: true },
      { name: 'Marketplace', path: '/marketplace', icon: 'mdi-storefront-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'data',
    title: 'Data',
    items: [
      { name: 'Databases', path: '/databases', icon: 'mdi-database-outline', requiresWorkspace: true },
      { name: 'Volumes', path: '/volumes', icon: 'mdi-harddisk', requiresWorkspace: true },
    ],
  },
  {
    id: 'networking',
    title: 'Networking',
    items: [
      { name: 'Networks', path: '/networks', icon: 'mdi-lan', requiresWorkspace: true },
      { name: 'Domains', path: '/domains', icon: 'mdi-web', requiresWorkspace: true },
      { name: 'DNS Providers', path: '/dns-providers', icon: 'mdi-dns', requiresWorkspace: true },
      { name: 'Routes', path: '/routes', icon: 'mdi-routes', requiresWorkspace: true },
      { name: 'Middlewares', path: '/middlewares', icon: 'mdi-tune-vertical', requiresWorkspace: true },
      { name: 'Certificates', path: '/certificates', icon: 'mdi-certificate', requiresWorkspace: true },
    ],
  },
  {
    id: 'sources',
    title: 'Sources',
    items: [
      { name: 'Secrets', path: '/secrets', icon: 'mdi-key-variant', requiresWorkspace: true },
      { name: 'Configs', path: '/configs', icon: 'mdi-file-cog-outline', requiresWorkspace: true },
      { name: 'Registries', path: '/registries', icon: 'mdi-database-lock-outline', requiresWorkspace: true },
      { name: 'Git Repositories', path: '/git-repositories', icon: 'mdi-git', requiresWorkspace: true },
    ],
  },
  {
    id: 'cicd',
    title: 'GitOps & CI/CD',
    items: [
      { name: 'Pipelines', path: '/pipelines', icon: 'mdi-pipe', requiresWorkspace: true },
      { name: 'Runners', path: '/runners', icon: 'mdi-cog-transfer-outline', requiresWorkspace: true },
      { name: 'GitOps', path: '/gitops', icon: 'mdi-source-branch-sync', requiresWorkspace: true },
      { name: 'Releases', path: '/releases', icon: 'mdi-tag-outline', requiresWorkspace: true },
      { name: 'Environments', path: '/environments', icon: 'mdi-layers-triple-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'developers',
    title: 'Developers',
    items: [
      { name: 'API Keys', path: '/api-keys', icon: 'mdi-key-outline', requiresWorkspace: true },
      { name: 'Container Registry', path: '/registry', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { name: 'Webhooks', path: '/webhooks', icon: 'mdi-webhook', requiresWorkspace: true },
      { name: 'Generator', path: '/generator', icon: 'mdi-auto-fix', requiresWorkspace: true },
      { name: 'API Reference', path: '', icon: 'mdi-book-open-page-variant-outline', external: true, requiresDocs: true },
    ],
  },
  {
    id: 'workspace',
    title: 'Workspace',
    items: [
      { name: 'All Workspaces', path: '/workspaces', icon: 'mdi-briefcase-outline' },
      { name: 'Members', path: '', icon: 'mdi-account-group-outline', workspaceTab: 'members', requiresWorkspaceAdmin: true },
      { name: 'Events', path: '/events', icon: 'mdi-timeline-text-outline', requiresWorkspace: true },
      { name: 'Audit Log', path: '/audit-log', icon: 'mdi-history', requiresWorkspaceAdmin: true },
      { name: 'Notifications', path: '', icon: 'mdi-bell-outline', workspaceTab: 'notifications', requiresWorkspaceAdmin: true },
      { name: 'Settings', path: '', icon: 'mdi-cog-outline', workspaceTab: 'settings', requiresWorkspaceAdmin: true },
    ],
  },
  {
    id: 'admin',
    title: 'Platform Admin',
    defaultOpen: true,
    items: [
      { name: 'Dashboard', path: '/admin/metrics', icon: 'mdi-view-dashboard-outline', requiresAdmin: true },
      { name: 'Users', path: '/admin/users', icon: 'mdi-account-group-outline', requiresAdmin: true },
      { name: 'Workspaces', path: '/admin/workspaces', icon: 'mdi-briefcase-outline', requiresAdmin: true },
      { name: 'Domains', path: '/admin/domains', icon: 'mdi-web', requiresAdmin: true },
      { name: 'Routes', path: '/admin/routes', icon: 'mdi-sitemap-outline', requiresAdmin: true },
      { name: 'Nodes', path: '/admin/nodes', icon: 'mdi-server-network', requiresAdmin: true },
      { name: 'Shared Runners', path: '/admin/runners', icon: 'mdi-cog-transfer-outline', requiresAdmin: true },
      { name: 'Events', path: '/admin/events', icon: 'mdi-pulse', requiresAdmin: true },
      { name: 'Jobs', path: '/admin/jobs', icon: 'mdi-clock-outline', requiresAdmin: true },
      { name: 'OAuth Providers', path: '/admin/oauth', icon: 'mdi-shield-key-outline', requiresAdmin: true },
      { name: 'LDAP / AD', path: '/admin/ldap', icon: 'mdi-account-key-outline', requiresAdmin: true },
      { name: 'Plans', path: '/admin/plans', icon: 'mdi-tune-variant', requiresAdmin: true },
      { name: 'License', path: '/admin/license', icon: 'mdi-license', requiresAdmin: true },
      { name: 'SIEM Streaming', path: '/admin/siem', icon: 'mdi-export-variant', requiresAdmin: true },
      { name: 'Platform Backup', path: '/admin/platform-backup', icon: 'mdi-cloud-upload-outline', requiresAdmin: true },
      { name: 'Container Registry', path: '/admin/registry', icon: 'mdi-cube-outline', requiresAdmin: true },
      { name: 'Platform Settings', path: '/admin/settings', icon: 'mdi-cog-outline', requiresAdmin: true },
      { name: 'Deployment Config', path: '/admin/deployment-config', icon: 'mdi-package-variant-closed', requiresAdmin: true },
    ],
  },
]
