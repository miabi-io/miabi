import api, { sseUrl } from './client'
import type { ApiResponse, Server, ServerConnectivity, Container, ContainerStat, NodeStats, NodeHostMetrics, NodePortUsage, DockerVolume, DockerNetwork, GatewayStatus, GatewayCandidate, GatewayUpdateProgress } from './types'

// --- import of existing Docker resources ---

export interface ImportablePort {
  container_port: number
  protocol: string
  host_port?: number
}

export interface ImportableContainer {
  id: string
  name: string
  image: string
  tag: string
  state: string
  ports?: ImportablePort[]
  env_count: number
  secret_env_keys?: string[]
  volumes?: string[]
  networks?: string[]
  restart_policy: string
  memory_bytes: number
  nano_cpus: number
  command?: string[]
  compose_project?: string
  compose_service?: string
  suggested_name: string
  already_imported: boolean
}

export interface ImportableVolume {
  name: string
  driver: string
  used_by?: string[]
  already_imported: boolean
}

export interface ImportableNetwork {
  name: string
  driver: string
  used_by?: string[]
  already_imported: boolean
}

export interface ImportableResources {
  containers: ImportableContainer[]
  volumes: ImportableVolume[]
  networks: ImportableNetwork[]
}

export type ImportMode = 'adopt' | 'reconcile'

export interface ImportItem {
  kind: 'container' | 'volume' | 'network'
  ref: string
  app_name?: string
  mode?: ImportMode
  // stack_name groups a container under a Stack (typically its compose project).
  stack_name?: string
}

export interface ImportPayload {
  workspace_id: number
  stack_name?: string
  items: ImportItem[]
}

export interface ImportItemResult {
  kind: string
  ref: string
  status: 'imported' | 'skipped' | 'failed'
  message?: string
  app_id?: number
}

export interface ImportResult {
  stack_ids?: Record<string, number>
  items: ImportItemResult[]
}

export interface NodeGatewayConfig {
  config: string
  default: string
  is_default: boolean
}

// --- housekeeping (reclaim, drift & sync) ---

export interface DiskUsageCategory {
  count: number
  active: number
  total_bytes: number
  reclaimable_bytes: number
}

export interface NodeDiskUsage {
  images: DiskUsageCategory
  containers: DiskUsageCategory
  volumes: DiskUsageCategory
  build_cache: DiskUsageCategory
}

export interface ReclaimCategoryStat {
  count: number
  bytes: number
}

export interface DriftItem {
  class: 'orphan' | 'missing' | 'untracked'
  kind: 'container' | 'volume'
  ref: string
  name: string
  image?: string
  state?: string
  owner_kind?: string
  owner_id?: number
  action: 'remove' | 'redeploy' | 'import'
}

export interface HousekeepingReport {
  node_id: number
  disk: NodeDiskUsage
  reclaim: {
    dangling_images: ReclaimCategoryStat
    build_cache: ReclaimCategoryStat
  }
  drift: {
    orphans: DriftItem[]
    missing: DriftItem[]
    untracked: DriftItem[]
  }
}

export interface HousekeepingSelection {
  reclaim: { dangling_images: boolean; build_cache: boolean }
  orphans: { kind: string; ref: string }[]
}

export interface HousekeepingPlan {
  reclaim: { dangling_images: boolean; build_cache: boolean }
  dangling_images: ReclaimCategoryStat
  build_cache: ReclaimCategoryStat
  orphans: DriftItem[]
  estimated_bytes: number
}

export interface HousekeepingResult {
  images_deleted: number
  images_reclaimed_bytes: number
  build_cache_reclaimed_bytes: number
  orphans_removed: DriftItem[]
  errors?: string[]
}

export interface JoinCommand {
  node: string
  image: string
  control_url: string
  command: string
  token_hint: string
}

export type ServerAccessMode = 'socket' | 'agent' | 'api'

export interface CreateNodePayload {
  name: string
  address?: string
  public_ip?: string
  public_hostname?: string
  connectivity: ServerConnectivity
  access_mode?: ServerAccessMode
  docker_endpoint?: string
  tls_ca_cert?: string
  tls_cert?: string
  tls_key?: string
  // acknowledge confirms the caller accepts a reachability change may briefly
  // interrupt the node's running workloads (required by the API on such a change).
  acknowledge?: boolean
}

// NodeWorkloads is the blast radius of a connectivity change on a node.
export interface NodeWorkloads {
  apps: number
  databases: number
}

export interface NodeCreated {
  node: Server
  token: string
}

// PlaceableNode is the minimal node info the create-form picker needs (no admin
// fields). Returned by GET /nodes, available to any authenticated user.
export interface PlaceableNode {
  id: number
  name: string
  connectivity: ServerConnectivity
  is_local: boolean
  online: boolean
  cordoned: boolean
  // The node's id within the swarm; empty when it is not a member. A service app
  // is placed by the Swarm scheduler (which ignores server_id), so pinning one to
  // a node means emitting a `node.id==<swarm_node_id>` placement constraint.
  swarm_node_id?: string
}

// A physical GPU discovered on a node. Admin policy (enabled, shared) is applied
// per device; UUID/model/memory are read-only hardware facts.
export interface GPUDevice {
  id: number
  server_id: number
  uuid: string
  index: number
  vendor: string
  model: string
  memory_mb: number
  enabled: boolean
  shared: boolean
  last_seen_at?: string | null
}

// GET /admin/nodes/{id}/gpus response: platform + node GPU capability plus the
// discovered devices.
export interface NodeGPUList {
  enabled: boolean          // platform GPU support on (MIABI_GPU_ENABLED)
  toolkit_present: boolean  // node has the NVIDIA Container Toolkit
  devices: GPUDevice[]
}

export const nodesApi = {
  list: () => api.get<ApiResponse<Server[]>>('/admin/nodes'),
  // Workspace-accessible placement list (any authenticated user).
  placeable: () => api.get<ApiResponse<PlaceableNode[]>>('/nodes'),
  create: (payload: CreateNodePayload) =>
    api.post<ApiResponse<NodeCreated>>('/admin/nodes', payload),
  update: (id: number, payload: CreateNodePayload) =>
    api.put<ApiResponse<Server>>(`/admin/nodes/${id}`, payload),
  workloads: (id: number) =>
    api.get<ApiResponse<NodeWorkloads>>(`/admin/nodes/${id}/workloads`),

  // GPUs: inventory + admin device policy.
  gpus: (id: number) => api.get<ApiResponse<NodeGPUList>>(`/admin/nodes/${id}/gpus`),
  updateGpu: (id: number, gpuID: number, payload: { enabled?: boolean; shared?: boolean }) =>
    api.patch<ApiResponse<GPUDevice>>(`/admin/nodes/${id}/gpus/${gpuID}`, payload),
  rescanGpus: (id: number) => api.post<ApiResponse<NodeGPUList>>(`/admin/nodes/${id}/gpus/rescan`),
  regenerateToken: (id: number) =>
    api.post<ApiResponse<{ token: string }>>(`/admin/nodes/${id}/token`),
  joinCommand: (id: number) =>
    api.get<ApiResponse<JoinCommand>>(`/admin/nodes/${id}/join-command`),
  cordon: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/cordon`),
  uncordon: (id: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/uncordon`),
  remove: (id: number) =>
    api.delete<ApiResponse<{ message: string }>>(`/admin/nodes/${id}`),
  // Single dashboard request: engine info + volume/network counts.
  stats: (id: number) => api.get<ApiResponse<NodeStats>>(`/admin/nodes/${id}/stats`),
  hostMetrics: (id: number) => api.get<ApiResponse<NodeHostMetrics>>(`/admin/nodes/${id}/host-metrics`),

  // Per-node Docker resources.
  containers: (id: number, all = true) => api.get<ApiResponse<Container[]>>(`/admin/nodes/${id}/containers?all=${all}`),
  containersStats: (id: number) => api.get<ApiResponse<ContainerStat[]>>(`/admin/nodes/${id}/container-stats`),
  ports: (id: number) => api.get<ApiResponse<NodePortUsage[]>>(`/admin/nodes/${id}/ports`),
  inspectContainer: (id: number, cid: string) => api.get<ApiResponse<Container>>(`/admin/nodes/${id}/containers/${cid}`),
  stopContainer: (id: number, cid: string) => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/containers/${cid}/stop`),
  restartContainer: (id: number, cid: string) => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/containers/${cid}/restart`),
  removeContainer: (id: number, cid: string) => api.delete<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/containers/${cid}?force=true`),
  pullImage: (id: number, ref: string) => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/images/pull`, { ref }),
  volumes: (id: number) => api.get<ApiResponse<DockerVolume[]>>(`/admin/nodes/${id}/volumes`),
  createVolume: (id: number, name: string) => api.post<ApiResponse<DockerVolume>>(`/admin/nodes/${id}/volumes`, { name }),
  removeVolume: (id: number, name: string) => api.delete<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/volumes/${encodeURIComponent(name)}?force=true`),
  networks: (id: number) => api.get<ApiResponse<DockerNetwork[]>>(`/admin/nodes/${id}/networks`),
  createNetwork: (id: number, name: string, driver = 'bridge') => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/networks`, { name, driver }),
  removeNetwork: (id: number, name: string) => api.delete<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/networks/${encodeURIComponent(name)}`),

  // Import existing (unmanaged) Docker resources.
  importable: (id: number) => api.get<ApiResponse<ImportableResources>>(`/admin/nodes/${id}/importable`),
  import: (id: number, payload: ImportPayload) => api.post<ApiResponse<ImportResult>>(`/admin/nodes/${id}/import`, payload),
  containerLogsUrl: (id: number, cid: string, tail = '200') => sseUrl(`/admin/nodes/${id}/containers/${cid}/logs?follow=true&tail=${tail}`),
  containerStatsUrl: (id: number, cid: string) => sseUrl(`/admin/nodes/${id}/containers/${cid}/stats`),

  // Housekeeping: reclaim disk + reconcile drift (dry-run first).
  housekeeping: (id: number) => api.get<ApiResponse<HousekeepingReport>>(`/admin/nodes/${id}/housekeeping`),
  housekeepingPlan: (id: number, sel: HousekeepingSelection) =>
    api.post<ApiResponse<HousekeepingPlan>>(`/admin/nodes/${id}/housekeeping/plan`, sel),
  housekeepingApply: (id: number, sel: HousekeepingSelection) =>
    api.post<ApiResponse<HousekeepingResult>>(`/admin/nodes/${id}/housekeeping/apply`, sel),

  // Edge gateway.
  gateway: (id: number) => api.get<ApiResponse<GatewayStatus>>(`/admin/nodes/${id}/gateway`),
  deployGateway: (id: number) => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/gateway/deploy`),
  updateGateway: (id: number) => api.post<ApiResponse<GatewayUpdateProgress>>(`/admin/nodes/${id}/gateway/update`),
  gatewayEventsUrl: (id: number) => sseUrl(`/admin/nodes/${id}/gateway/events`),
  gatewayCandidates: (id: number) => api.get<ApiResponse<GatewayCandidate[]>>(`/admin/nodes/${id}/gateway/candidates`),
  importGateway: (id: number, container?: string) => api.post<ApiResponse<GatewayStatus>>(`/admin/nodes/${id}/gateway/import`, { container: container ?? '' }),
  teardownGateway: (id: number) => api.post<ApiResponse<{ message: string }>>(`/admin/nodes/${id}/gateway/teardown`),
  gatewayLogsUrl: (id: number, tail = '200') => sseUrl(`/admin/nodes/${id}/gateway/logs?follow=true&tail=${tail}`),
  gatewayConfig: (id: number) => api.get<ApiResponse<NodeGatewayConfig>>(`/admin/nodes/${id}/gateway/config`),
  updateGatewayConfig: (id: number, config: string) =>
    api.put<ApiResponse<NodeGatewayConfig>>(`/admin/nodes/${id}/gateway/config`, { config }),
  updateGatewayImage: (id: number, image: string) =>
    api.put<ApiResponse<GatewayStatus>>(`/admin/nodes/${id}/gateway/image`, { image }),
}
