export interface NavItem {
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
  title: string
  items: NavItem[]
  defaultOpen?: boolean
}

// The workspace console's navigation. Platform administration is a separate
// console with its own shell and its own sections — see adminNavSections.
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
]
