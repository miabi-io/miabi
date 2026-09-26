import type { NavSection } from './nav'

// The platform admin console's navigation.
//
// Grouped rather than listed: the same nineteen destinations were one flat
// section appended to the workspace menu, which made the sidebar a single scroll
// from "Applications" to "Deployment Config" with no boundary between a tenant's
// resources and the whole server's.
export const adminNavSections: NavSection[] = [
  {
    id: 'admin-overview',
    key: 'adminNav.overview.title', title: 'Overview',
    items: [
      { key: 'adminNav.overview.dashboard', name: 'Dashboard', path: '/admin/dashboard', icon: 'mdi-view-dashboard-outline' },
      { key: 'adminNav.overview.events', name: 'Events', path: '/admin/events', icon: 'mdi-pulse' },
      { key: 'adminNav.overview.jobs', name: 'Jobs', path: '/admin/jobs', icon: 'mdi-clock-outline' },
      { key: 'adminNav.overview.reconciliation', name: 'Reconciliation', path: '/admin/reconciliation', icon: 'mdi-autorenew' },
    ],
  },
  {
    id: 'admin-identity',
    key: 'adminNav.identity.title', title: 'Identity',
    items: [
      { key: 'adminNav.identity.users', name: 'Users', path: '/admin/users', icon: 'mdi-account-group-outline' },
      { key: 'adminNav.identity.oauthProviders', name: 'OAuth Providers', path: '/admin/oauth', icon: 'mdi-shield-key-outline' },
      { key: 'adminNav.identity.ldapAd', name: 'LDAP / AD', path: '/admin/ldap', icon: 'mdi-account-key-outline' },
    ],
  },
  {
    id: 'admin-tenants',
    key: 'adminNav.tenants.title', title: 'Tenants',
    items: [
      { key: 'adminNav.tenants.organizations', name: 'Organizations', path: '/admin/organizations', icon: 'mdi-domain' },
      { key: 'adminNav.tenants.workspaces', name: 'Workspaces', path: '/admin/workspaces', icon: 'mdi-briefcase-outline' },
      { key: 'adminNav.tenants.plans', name: 'Plans & Quotas', path: '/admin/plans', icon: 'mdi-tune-variant' },
      { key: 'adminNav.tenants.databaseSizes', name: 'Database sizes', path: '/admin/database-sizes', icon: 'mdi-database-cog-outline' },
      { key: 'adminNav.tenants.announcements', name: 'Announcements', path: '/admin/announcements', icon: 'mdi-bullhorn-outline' },
    ],
  },
  {
    id: 'admin-infrastructure',
    key: 'adminNav.infrastructure.title', title: 'Infrastructure',
    items: [
      { key: 'adminNav.infrastructure.clusters', name: 'Clusters', path: '/admin/clusters', icon: 'mdi-lan' },
      { key: 'adminNav.infrastructure.nodes', name: 'Nodes', path: '/admin/nodes', icon: 'mdi-server-network' },
      { key: 'adminNav.infrastructure.storageClasses', name: 'Storage classes', path: '/admin/storage-classes', icon: 'mdi-harddisk' },
      { key: 'adminNav.infrastructure.kernelGrants', name: 'Kernel grants', path: '/admin/grants', icon: 'mdi-shield-key-outline' },
      { key: 'adminNav.infrastructure.sharedRunners', name: 'Shared Runners', path: '/admin/runners', icon: 'mdi-cog-transfer-outline' },
      { key: 'adminNav.infrastructure.containerRegistry', name: 'Container Registry', path: '/admin/registry', icon: 'mdi-cube-outline' },
    ],
  },
  {
    // How traffic reaches what the infrastructure runs. Grouped apart from the machines because an
    // admin arrives here asking about a hostname, not about a node — and because Infrastructure had
    // grown past a list anyone scans.
    id: 'admin-networking',
    key: 'adminNav.networking.title', title: 'Networking',
    items: [
      { key: 'adminNav.networking.domains', name: 'Domains', path: '/admin/domains', icon: 'mdi-web' },
      { key: 'adminNav.networking.routes', name: 'Routes', path: '/admin/routes', icon: 'mdi-sitemap-outline' },
    ],
  },
  {
    id: 'admin-platform',
    key: 'adminNav.platform.title', title: 'Platform',
    items: [
      { key: 'adminNav.platform.security', name: 'Security Center', path: '/admin/security', icon: 'mdi-shield-lock-outline' },
      { key: 'adminNav.platform.platformSettings', name: 'Platform Settings', path: '/admin/settings', icon: 'mdi-cog-outline' },
      { key: 'adminNav.platform.branding', name: 'Branding', path: '/admin/branding', icon: 'mdi-palette-outline' },
      { key: 'adminNav.platform.deploymentConfig', name: 'Deployment Config', path: '/admin/deployment-config', icon: 'mdi-package-variant-closed' },
      { key: 'adminNav.platform.platformBackup', name: 'Platform Backup', path: '/admin/platform-backup', icon: 'mdi-cloud-upload-outline' },
    ],
  },
  {
    id: 'admin-enterprise',
    key: 'adminNav.enterprise.title', title: 'Enterprise',
    items: [
      { key: 'adminNav.enterprise.license', name: 'License', path: '/admin/license', icon: 'mdi-license' },
      { key: 'adminNav.enterprise.siemStreaming', name: 'SIEM Streaming', path: '/admin/siem', icon: 'mdi-export-variant' },
    ],
  },
]
