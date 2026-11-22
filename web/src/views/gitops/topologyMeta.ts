import type { NodeStatus, EdgeType, GitSourceStatus } from '@/api/types'
import type { RouteLocationRaw } from 'vue-router'
import { t } from '@/i18n'

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
  synced: { get label() { return t('gitops.status.synced') }, badge: 'badge-success', icon: 'mdi-check-circle', color: 'var(--success-600)' },
  out_of_sync: { get label() { return t('gitops.status.out_of_sync') }, badge: 'badge-warning', icon: 'mdi-circle-edit-outline', color: 'var(--warning-600)' },
  missing: { get label() { return t('gitops.nodeStatus.missing') }, badge: 'badge-info', icon: 'mdi-plus-circle-outline', color: 'var(--info-600, #3b82f6)' },
  orphaned: { get label() { return t('gitops.nodeStatus.orphaned') }, badge: 'badge-danger', icon: 'mdi-trash-can-outline', color: 'var(--danger-600)' },
}

export const gitSourceStatusMeta: Record<GitSourceStatus, StatusMeta> = {
  synced: { get label() { return t('gitops.status.synced') }, badge: 'badge-success', icon: 'mdi-check-circle-outline', color: 'var(--success-600)' },
  out_of_sync: { get label() { return t('gitops.status.out_of_sync') }, badge: 'badge-warning', icon: 'mdi-alert-circle-outline', color: 'var(--warning-600)' },
  progressing: { get label() { return t('gitops.status.progressing') }, badge: 'badge-info', icon: 'mdi-loading mdi-spin', color: 'var(--info-600, #3b82f6)' },
  error: { get label() { return t('gitops.status.error') }, badge: 'badge-danger', icon: 'mdi-close-circle-outline', color: 'var(--danger-600)' },
  degraded: { get label() { return t('gitops.status.degraded') }, badge: 'badge-warning', icon: 'mdi-alert-outline', color: 'var(--warning-600)' },
  unknown: { get label() { return t('gitops.status.unknown') }, badge: 'badge-neutral', icon: 'mdi-help-circle-outline', color: 'var(--text-muted)' },
}

// Unknown or empty statuses get no dot (see healthOf).
export interface HealthMeta {
  label: string
  color: string
  pulse?: boolean
}
const healthMeta: Record<string, HealthMeta> = {
  running: { get label() { return t('gitops.healthState.running') }, color: 'var(--success-600)' },
  stopped: { get label() { return t('gitops.healthState.stopped') }, color: 'var(--text-muted)' },
  failed: { get label() { return t('gitops.healthState.failed') }, color: 'var(--danger-600)' },
  error: { get label() { return t('gitops.healthState.error') }, color: 'var(--danger-600)' },
  deploying: { get label() { return t('gitops.healthState.deploying') }, color: 'var(--warning-600)', pulse: true },
  provisioning: { get label() { return t('gitops.healthState.provisioning') }, color: 'var(--warning-600)', pulse: true },
  upgrading: { get label() { return t('gitops.healthState.upgrading') }, color: 'var(--warning-600)', pulse: true },
  created: { get label() { return t('gitops.healthState.created') }, color: 'var(--text-muted)' },
  scheduled: { get label() { return t('gitops.healthState.scheduled') }, color: 'var(--success-600)' },
  suspended: { get label() { return t('gitops.healthState.suspended') }, color: 'var(--text-muted)' },
  pending: { get label() { return t('gitops.healthState.pending') }, color: 'var(--warning-600)', pulse: true },
  succeeded: { get label() { return t('gitops.healthState.succeeded') }, color: 'var(--success-600)' },
  skipped: { get label() { return t('gitops.healthState.skipped') }, color: 'var(--warning-600)' },
  canceled: { get label() { return t('gitops.healthState.canceled') }, color: 'var(--text-muted)' },
}

export function healthOf(status?: string): HealthMeta | null {
  if (!status) return null
  return healthMeta[status] ?? { label: status, color: 'var(--text-muted)' }
}

// Human label per edge type, shown on hover / in the side panel.
const edgeKeys: Record<EdgeType, string> = {
  mount: 'gitops.edge.mount',
  stack: 'gitops.edge.stack',
  route: 'gitops.edge.route',
  domain: 'gitops.edge.domain',
  database: 'gitops.edge.database',
  secret: 'gitops.edge.secret',
  'app-ref': 'gitops.edge.appRef',
  schedule: 'gitops.edge.schedule',
  run: 'gitops.edge.run',
}

export function edgeLabel(type: string): string {
  const key = edgeKeys[type as EdgeType]
  return key ? t(key) : type
}

export function edgeLabelIncoming(type: string): string {
  const key = edgeKeys[type as EdgeType]
  return key ? t(`${key}Incoming`) : type
}

// Kinds with a detail page take the live id; others route to their list page.
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
