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
    title: 'Overview',
    items: [
      { name: 'Dashboard', path: '/admin/metrics', icon: 'mdi-view-dashboard-outline' },
      { name: 'Events', path: '/admin/events', icon: 'mdi-pulse' },
      { name: 'Jobs', path: '/admin/jobs', icon: 'mdi-clock-outline' },
    ],
  },
  {
    id: 'admin-identity',
    title: 'Identity',
    items: [
      { name: 'Users', path: '/admin/users', icon: 'mdi-account-group-outline' },
      { name: 'OAuth Providers', path: '/admin/oauth', icon: 'mdi-shield-key-outline' },
      { name: 'LDAP / AD', path: '/admin/ldap', icon: 'mdi-account-key-outline' },
    ],
  },
  {
    id: 'admin-tenants',
    title: 'Tenants',
    items: [
      { name: 'Workspaces', path: '/admin/workspaces', icon: 'mdi-briefcase-outline' },
      { name: 'Plans', path: '/admin/plans', icon: 'mdi-tune-variant' },
      { name: 'Announcements', path: '/admin/announcements', icon: 'mdi-bullhorn-outline' },
    ],
  },
  {
    id: 'admin-infrastructure',
    title: 'Infrastructure',
    items: [
      { name: 'Nodes', path: '/admin/nodes', icon: 'mdi-server-network' },
      { name: 'Ports', path: '/admin/ports', icon: 'mdi-lan-connect' },
      { name: 'Shared Runners', path: '/admin/runners', icon: 'mdi-cog-transfer-outline' },
      { name: 'Container Registry', path: '/admin/registry', icon: 'mdi-cube-outline' },
      { name: 'Domains', path: '/admin/domains', icon: 'mdi-web' },
      { name: 'Routes', path: '/admin/routes', icon: 'mdi-sitemap-outline' },
    ],
  },
  {
    id: 'admin-platform',
    title: 'Platform',
    items: [
      { name: 'Platform Settings', path: '/admin/settings', icon: 'mdi-cog-outline' },
      { name: 'Deployment Config', path: '/admin/deployment-config', icon: 'mdi-package-variant-closed' },
      { name: 'Platform Backup', path: '/admin/platform-backup', icon: 'mdi-cloud-upload-outline' },
    ],
  },
  {
    id: 'admin-enterprise',
    title: 'Enterprise',
    items: [
      { name: 'License', path: '/admin/license', icon: 'mdi-license' },
      { name: 'SIEM Streaming', path: '/admin/siem', icon: 'mdi-export-variant' },
    ],
  },
]
