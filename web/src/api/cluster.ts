import api from './client'
import type {
  ApiResponse, ClusterStatus, ClusterJoinInstructions, ClusterMember,
  ClusterPreflight, NetCheck, SwarmTask, ControlPlaneCert,
} from './types'

type ClusterRef = number | string

const base = (id: ClusterRef) => `/admin/clusters/${id}`

// clusterApi drives a cluster's Docker Swarm. Status is always available; the mutations require
// platform-admin rights.
export const clusterApi = {
  status: (id: ClusterRef) => api.get<ApiResponse<ClusterStatus>>(`${base(id)}/swarm`),
  // Swarm membership (docker node ls), including unmanaged members.
  members: (id: ClusterRef) => api.get<ApiResponse<ClusterMember[]>>(`${base(id)}/nodes`),
  joinToken: (id: ClusterRef) => api.get<ApiResponse<ClusterJoinInstructions>>(`${base(id)}/join-token`),
  // advertiseAddr is required when initializing a new swarm, ignored when adopting one.
  enable: (id: ClusterRef, advertiseAddr: string) =>
    api.post<ApiResponse<ClusterStatus>>(`${base(id)}/swarm/enable`, { advertise_addr: advertiseAddr }),
  disable: (id: ClusterRef) => api.post<ApiResponse<{ message: string }>>(`${base(id)}/swarm/disable`),
  // Converts the default cluster's workspace networks still on node-local bridges into overlays.
  applyNetworking: (id: ClusterRef) => api.post<ApiResponse<ClusterStatus>>(`${base(id)}/network/apply`),
  preflight: (id: ClusterRef) => api.get<ApiResponse<ClusterPreflight>>(`${base(id)}/preflight`),
  // Starts and removes probe containers, so it is a POST.
  netCheck: (id: ClusterRef) => api.post<ApiResponse<NetCheck>>(`${base(id)}/net-check`),
  // Keyed by swarm node id, so an unmanaged member can be drained too.
  setAvailability: (id: ClusterRef, swarmNodeId: string, availability: 'active' | 'pause' | 'drain') =>
    api.post<ApiResponse<{ message: string }>>(`${base(id)}/members/${swarmNodeId}/availability`, { availability }),
  // Grants Miabi the Docker socket on every node of the swarm, which is why it is an explicit action.
  deployAgents: (id: ClusterRef, opts: { insecureSkipVerify?: boolean; caCert?: string; caCertPath?: string }) =>
    api.post<ApiResponse<{ deployed: boolean; running_tasks: number; insecure_tls: boolean }>>(
      `${base(id)}/agents`,
      {
        insecure_skip_verify: !!opts.insecureSkipVerify,
        ca_cert: opts.caCert ?? '',
        ca_cert_path: opts.caCertPath ?? '',
      },
    ),
  controlPlaneCert: () => api.get<ApiResponse<ControlPlaneCert>>('/admin/cluster/control-plane-cert'),
  removeAgents: (id: ClusterRef) => api.delete<ApiResponse<{ message: string }>>(`${base(id)}/agents`),
  nodeTasks: (id: ClusterRef, swarmNodeId: string) =>
    api.get<ApiResponse<SwarmTask[]>>(`${base(id)}/members/${swarmNodeId}/tasks`),
  joinNode: (id: ClusterRef, nodeId: number) =>
    api.post<ApiResponse<{ message: string }>>(`${base(id)}/nodes/${nodeId}/join`),
  leaveNode: (id: ClusterRef, nodeId: number) =>
    api.post<ApiResponse<{ message: string }>>(`${base(id)}/nodes/${nodeId}/leave`),
}
