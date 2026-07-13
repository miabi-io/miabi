import api from './client'
import type {
  ApiResponse, ClusterStatus, ClusterJoinInstructions, ClusterMember,
  ClusterPreflight, NetCheck, SwarmTask, ControlPlaneCert,
} from './types'

// clusterApi drives Miabi's optional cluster mode (Docker Swarm under the hood).
// Status is always available; the mutations require platform-admin rights and
// only take effect on a swarm-capable manager.
export const clusterApi = {
  status: () => api.get<ApiResponse<ClusterStatus>>('/admin/cluster'),
  // Swarm membership (docker node ls), including unmanaged members.
  members: () => api.get<ApiResponse<ClusterMember[]>>('/admin/cluster/nodes'),
  // Manual join command/token for hosts not connected to the manager.
  joinToken: () => api.get<ApiResponse<ClusterJoinInstructions>>('/admin/cluster/join-token'),
  // advertiseAddr is the address swarm peers reach the manager on; required when
  // initializing a new swarm, ignored when adopting an existing one.
  enable: (advertiseAddr: string, name: string) =>
    api.post<ApiResponse<ClusterStatus>>('/admin/cluster/enable', { advertise_addr: advertiseAddr, name }),
  // A label, nothing more — one control plane drives one swarm.
  rename: (name: string) => api.patch<ApiResponse<ClusterStatus>>('/admin/cluster', { name }),
  disable: () => api.post<ApiResponse<{ message: string }>>('/admin/cluster/disable'),
  // Convert workspace networks still on node-local bridges into cluster overlays,
  // so apps and databases reach each other across nodes. Enable already does this
  // on the transition into cluster mode; this is the explicit action for an install
  // that was already clustered when it upgraded, or to re-run it after a node that
  // was offline comes back. Containers are not restarted, but in-flight connections
  // inside each workspace drop briefly.
  applyNetworking: () => api.post<ApiResponse<ClusterStatus>>('/admin/cluster/network/apply'),
  // What this host can/can't do before enabling cluster mode, plus the ports that
  // must be open between nodes (including ESP, which almost nobody opens).
  preflight: () => api.get<ApiResponse<ClusterPreflight>>('/admin/cluster/preflight'),
  // Probe the overlay data plane between every pair of nodes. Starts and removes
  // probe containers, so it is a POST.
  netCheck: () => api.post<ApiResponse<NetCheck>>('/admin/cluster/net-check'),
  // active | pause | drain. Keyed by SWARM node id so an unmanaged member can be
  // drained too — which is exactly when you most need to.
  setAvailability: (swarmNodeId: string, availability: 'active' | 'pause' | 'drain') =>
    api.post<ApiResponse<{ message: string }>>(`/admin/cluster/members/${swarmNodeId}/availability`, { availability }),
  // Install the agent on every swarm worker as a global service (and on workers that
  // join later). This grants Miabi the Docker socket — root-equivalent — on each of
  // them, which is why it is an explicit action and not part of enabling cluster mode.
  // insecureSkipVerify is needed when the control plane is behind a self-signed or
  // private-CA certificate: without it the agent dies with "certificate signed by
  // unknown authority" and never connects. It is a real downgrade — see the dialog.
  deployAgents: (opts: { insecureSkipVerify?: boolean; caCert?: string; caCertPath?: string }) =>
    api.post<ApiResponse<{ deployed: boolean; running_tasks: number; insecure_tls: boolean }>>(
      '/admin/cluster/agents',
      {
        insecure_skip_verify: !!opts.insecureSkipVerify,
        ca_cert: opts.caCert ?? '',
        // A CA file already on the nodes is bind-mounted into each agent. It is why the
        // hosts worked while the agents did not: the host trusts the CA, the container
        // has its own bundle and has never heard of it.
        ca_cert_path: opts.caCertPath ?? '',
      },
    ),
  // The certificate the control plane serves. Fetching it turns "trust a custom CA"
  // from a research task into one click — which decides whether it is the common path
  // or whether everyone picks "skip verification" instead.
  controlPlaneCert: () => api.get<ApiResponse<ControlPlaneCert>>('/admin/cluster/control-plane-cert'),
  removeAgents: () => api.delete<ApiResponse<{ message: string }>>('/admin/cluster/agents'),
  nodeTasks: (swarmNodeId: string) =>
    api.get<ApiResponse<SwarmTask[]>>(`/admin/cluster/members/${swarmNodeId}/tasks`),
  joinNode: (nodeId: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/cluster/nodes/${nodeId}/join`),
  leaveNode: (nodeId: number) =>
    api.post<ApiResponse<{ message: string }>>(`/admin/cluster/nodes/${nodeId}/leave`),
}
