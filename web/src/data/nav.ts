export interface NavItem {
  /** Catalogue key for the label; `name` is the English fallback. */
  key: string
  name: string
  path: string
  icon: string
  requiresWorkspace?: boolean
  requiresWorkspaceAdmin?: boolean
  requiresDocs?: boolean
  external?: boolean
  workspaceTab?: 'settings' | 'members' | 'audit' | 'notifications'
}

export interface NavSection {
  id: string
  key: string
  title: string
  items: NavItem[]
  defaultOpen?: boolean
}

// The workspace console's navigation. Platform administration is a separate
// console with its own shell and its own sections — see adminNavSections.
export const navSections: NavSection[] = [
  {
    id: 'overview',
    key: 'nav.overview.title', title: 'Overview',
    items: [{ key: 'nav.overview.dashboard', name: 'Dashboard', path: '/', icon: 'mdi-view-dashboard-outline' }],
  },
  {
    id: 'analytics',
    key: 'nav.analytics.title', title: 'Analytics',
    items: [
      { key: 'nav.analytics.overview', name: 'Overview', path: '/analytics', icon: 'mdi-chart-areaspline', requiresWorkspace: true },
      { key: 'nav.analytics.httpTraffic', name: 'HTTP Traffic', path: '/analytics/http', icon: 'mdi-earth', requiresWorkspace: true },
      { key: 'nav.analytics.performance', name: 'Performance', path: '/analytics/performance', icon: 'mdi-speedometer', requiresWorkspace: true },
      { key: 'nav.analytics.webAnalytics', name: 'Web Analytics', path: '/analytics/web', icon: 'mdi-account-group-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'deploy',
    key: 'nav.deploy.title', title: 'Deploy',
    items: [
      { key: 'nav.deploy.applications', name: 'Applications', path: '/apps', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { key: 'nav.deploy.stacks', name: 'Stacks', path: '/stacks', icon: 'mdi-layers-outline', requiresWorkspace: true },
      { key: 'nav.deploy.jobs', name: 'Jobs', path: '/jobs', icon: 'mdi-console-line', requiresWorkspace: true },
      { key: 'nav.deploy.marketplace', name: 'Marketplace', path: '/marketplace', icon: 'mdi-storefront-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'data',
    key: 'nav.data.title', title: 'Data',
    items: [
      { key: 'nav.data.databases', name: 'Databases', path: '/databases', icon: 'mdi-database-outline', requiresWorkspace: true },
      { key: 'nav.data.volumes', name: 'Volumes', path: '/volumes', icon: 'mdi-harddisk', requiresWorkspace: true },
    ],
  },
  {
    id: 'networking',
    key: 'nav.networking.title', title: 'Networking',
    items: [
      { key: 'nav.networking.networks', name: 'Networks', path: '/networks', icon: 'mdi-lan', requiresWorkspace: true },
      { key: 'nav.networking.domains', name: 'Domains', path: '/domains', icon: 'mdi-web', requiresWorkspace: true },
      { key: 'nav.networking.dnsProviders', name: 'DNS Providers', path: '/dns-providers', icon: 'mdi-dns', requiresWorkspace: true },
      { key: 'nav.networking.routes', name: 'Routes', path: '/routes', icon: 'mdi-routes', requiresWorkspace: true },
      { key: 'nav.networking.middlewares', name: 'Middlewares', path: '/middlewares', icon: 'mdi-tune-vertical', requiresWorkspace: true },
      { key: 'nav.networking.certificates', name: 'Certificates', path: '/certificates', icon: 'mdi-certificate', requiresWorkspace: true },
    ],
  },
  {
    id: 'sources',
    key: 'nav.sources.title', title: 'Sources',
    items: [
      { key: 'nav.sources.secrets', name: 'Secrets', path: '/secrets', icon: 'mdi-key-variant', requiresWorkspace: true },
      { key: 'nav.sources.configs', name: 'Configs', path: '/configs', icon: 'mdi-file-cog-outline', requiresWorkspace: true },
      { key: 'nav.sources.registries', name: 'Registries', path: '/registries', icon: 'mdi-database-lock-outline', requiresWorkspace: true },
      { key: 'nav.sources.gitRepositories', name: 'Git Repositories', path: '/git-repositories', icon: 'mdi-git', requiresWorkspace: true },
    ],
  },
  {
    id: 'cicd',
    key: 'nav.cicd.title', title: 'GitOps & CI/CD',
    items: [
      { key: 'nav.cicd.pipelines', name: 'Pipelines', path: '/pipelines', icon: 'mdi-pipe', requiresWorkspace: true },
      { key: 'nav.cicd.runners', name: 'Runners', path: '/runners', icon: 'mdi-cog-transfer-outline', requiresWorkspace: true },
      { key: 'nav.cicd.gitops', name: 'GitOps', path: '/gitops', icon: 'mdi-source-branch-sync', requiresWorkspace: true },
      { key: 'nav.cicd.releases', name: 'Releases', path: '/releases', icon: 'mdi-tag-outline', requiresWorkspace: true },
      { key: 'nav.cicd.environments', name: 'Environments', path: '/environments', icon: 'mdi-layers-triple-outline', requiresWorkspace: true },
    ],
  },
  {
    id: 'developers',
    key: 'nav.developers.title', title: 'Developers',
    items: [
      { key: 'nav.developers.apiKeys', name: 'API Keys', path: '/api-keys', icon: 'mdi-key-outline', requiresWorkspace: true },
      { key: 'nav.developers.containerRegistry', name: 'Container Registry', path: '/registry', icon: 'mdi-cube-outline', requiresWorkspace: true },
      { key: 'nav.developers.webhooks', name: 'Webhooks', path: '/webhooks', icon: 'mdi-webhook', requiresWorkspace: true },
      { key: 'nav.developers.generator', name: 'Generator', path: '/generator', icon: 'mdi-auto-fix', requiresWorkspace: true },
      { key: 'nav.developers.apiReference', name: 'API Reference', path: '', icon: 'mdi-book-open-page-variant-outline', external: true, requiresDocs: true },
    ],
  },
  {
    id: 'workspace',
    key: 'nav.workspace.title', title: 'Workspace',
    items: [
      { key: 'nav.workspace.allWorkspaces', name: 'All Workspaces', path: '/workspaces', icon: 'mdi-briefcase-outline' },
      { key: 'nav.workspace.members', name: 'Members', path: '', icon: 'mdi-account-group-outline', workspaceTab: 'members', requiresWorkspaceAdmin: true },
      { key: 'nav.workspace.events', name: 'Events', path: '/events', icon: 'mdi-timeline-text-outline', requiresWorkspace: true },
      { key: 'nav.workspace.auditLog', name: 'Audit Log', path: '/audit-log', icon: 'mdi-history', requiresWorkspaceAdmin: true },
      { key: 'nav.workspace.notifications', name: 'Notifications', path: '', icon: 'mdi-bell-outline', workspaceTab: 'notifications', requiresWorkspaceAdmin: true },
      { key: 'nav.workspace.settings', name: 'Settings', path: '', icon: 'mdi-cog-outline', workspaceTab: 'settings', requiresWorkspaceAdmin: true },
    ],
  },
]
