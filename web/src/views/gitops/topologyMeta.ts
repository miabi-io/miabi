// Shared visual metadata for the GitOps topology graph: per-kind icon/label,
// per-status colour, and the deep-link target for each resource kind.
import type { NodeStatus, EdgeType, GitSourceStatus } from '@/api/types'
import type { RouteLocationRaw } from 'vue-router'

export interface KindMeta {
  label: string
  icon: string // mdi glyph
}

// Icon + label per declarative Kind. Falls back to a generic cube.
export const kindMeta: Record<string, KindMeta> = {
  Application: { label: 'Application', icon: 'mdi-cube-outline' },
  Database: { label: 'Database', icon: 'mdi-database' },
  Volume: { label: 'Volume', icon: 'mdi-harddisk' },
  Route: { label: 'Route', icon: 'mdi-sitemap-outline' },
  Stack: { label: 'Stack', icon: 'mdi-layers-outline' },
  Secret: { label: 'Secret', icon: 'mdi-key-variant' },
  SealedSecret: { label: 'SealedSecret', icon: 'mdi-lock-outline' },
  Domain: { label: 'Domain', icon: 'mdi-web' },
  CronJob: { label: 'CronJob', icon: 'mdi-calendar-clock' },
  Job: { label: 'Job', icon: 'mdi-play-circle-outline' },
}

export function kindOf(kind: string): KindMeta {
  return kindMeta[kind] ?? { label: kind, icon: 'mdi-cube-outline' }
}

export interface StatusMeta {
  label: string
  badge: string // badge-* class
  icon: string
  color: string // CSS var, used for the node accent + edge tint
}

// Per-node sync status. Mirrors ArgoCD's sync semantics.
export const nodeStatusMeta: Record<NodeStatus, StatusMeta> = {
  synced: { label: 'Synced', badge: 'badge-success', icon: 'mdi-check-circle', color: 'var(--success-600)' },
  out_of_sync: { label: 'Out of sync', badge: 'badge-warning', icon: 'mdi-circle-edit-outline', color: 'var(--warning-600)' },
  missing: { label: 'Missing', badge: 'badge-info', icon: 'mdi-plus-circle-outline', color: 'var(--info-600, #3b82f6)' },
  orphaned: { label: 'Orphaned', badge: 'badge-danger', icon: 'mdi-trash-can-outline', color: 'var(--danger-600)' },
}

// Per git-source (project) sync status, used for the root project node and the
// header badge.
export const gitSourceStatusMeta: Record<GitSourceStatus, StatusMeta> = {
  synced: { label: 'Synced', badge: 'badge-success', icon: 'mdi-check-circle-outline', color: 'var(--success-600)' },
  out_of_sync: { label: 'Out of sync', badge: 'badge-warning', icon: 'mdi-alert-circle-outline', color: 'var(--warning-600)' },
  progressing: { label: 'Progressing', badge: 'badge-info', icon: 'mdi-loading mdi-spin', color: 'var(--info-600, #3b82f6)' },
  error: { label: 'Error', badge: 'badge-danger', icon: 'mdi-close-circle-outline', color: 'var(--danger-600)' },
  degraded: { label: 'Degraded', badge: 'badge-warning', icon: 'mdi-alert-outline', color: 'var(--warning-600)' },
  unknown: { label: 'Never synced', badge: 'badge-neutral', icon: 'mdi-help-circle-outline', color: 'var(--text-muted)' },
}

// Runtime health (live status) shown as a dot on stateful nodes (apps,
// databases). Keyed by the resource's status string; `pulse` animates for
// in-progress states. Unknown/empty statuses get no dot (see healthOf).
export interface HealthMeta {
  label: string
  color: string
  pulse?: boolean
}
const healthMeta: Record<string, HealthMeta> = {
  running: { label: 'Running', color: 'var(--success-600)' },
  stopped: { label: 'Stopped', color: 'var(--text-muted)' },
  failed: { label: 'Failed', color: 'var(--danger-600)' },
  error: { label: 'Error', color: 'var(--danger-600)' },
  deploying: { label: 'Deploying', color: 'var(--warning-600)', pulse: true },
  provisioning: { label: 'Provisioning', color: 'var(--warning-600)', pulse: true },
  upgrading: { label: 'Upgrading', color: 'var(--warning-600)', pulse: true },
  created: { label: 'Created', color: 'var(--text-muted)' },
  scheduled: { label: 'Scheduled', color: 'var(--success-600)' },
  suspended: { label: 'Suspended', color: 'var(--text-muted)' },
  pending: { label: 'Pending', color: 'var(--warning-600)', pulse: true },
  succeeded: { label: 'Succeeded', color: 'var(--success-600)' },
  skipped: { label: 'Skipped', color: 'var(--warning-600)' },
  canceled: { label: 'Canceled', color: 'var(--text-muted)' },
}

// healthOf returns the display meta for a runtime status, or null when the
// status is empty/unknown (kinds without runtime state, or not yet live).
export function healthOf(status?: string): HealthMeta | null {
  if (!status) return null
  return healthMeta[status] ?? { label: status, color: 'var(--text-muted)' }
}

// Human label per edge type, shown on hover / in the side panel.
export const edgeLabel: Record<EdgeType, string> = {
  mount: 'mounts',
  stack: 'in stack',
  route: 'routes to',
  domain: 'under domain',
  database: 'connects to',
  secret: 'reads secret',
  'app-ref': 'links to',
  schedule: 'runs on schedule in',
  run: 'runs in',
}

// Deep-link target for a live resource of the given kind. Kinds with a detail
// page take the live id; kinds that only have a list page route there.
export function resourceRoute(kind: string, liveId?: number): RouteLocationRaw | null {
  switch (kind) {
    case 'Application':
      return liveId ? { name: 'app-detail', params: { id: liveId } } : null
    case 'Database':
      return liveId ? { name: 'database-detail', params: { id: liveId } } : null
    case 'Volume':
      return liveId ? { name: 'volume-detail', params: { id: liveId } } : null
    case 'Stack':
      return liveId ? { name: 'stack-detail', params: { id: liveId } } : null
    case 'Route':
      return liveId ? { name: 'route-detail', params: { id: liveId } } : null
    case 'Domain':
      return { name: 'domains' }
    case 'Secret':
    case 'SealedSecret':
      return { name: 'secrets' }
    case 'CronJob':
    case 'Job':
      return { name: 'jobs' }
    default:
      return null
  }
}
