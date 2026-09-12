// Option metadata for the node Add/Edit forms. The select fields render only the
// `label`; the `description` is surfaced via an info popover and a hint line.

export interface NodeOption {
  value: string
  label: string
  description: string
}

export const ACCESS_MODES: NodeOption[] = [
  {
    value: 'agent',
    label: 'Agent',
    description: 'The node dials in to the manager over a tunnel — works behind NAT with no inbound ports to open.',
  },
  {
    value: 'api',
    label: 'Docker API',
    description: "The manager connects directly to the node's Docker TCP endpoint. The node must be reachable inbound (TLS recommended).",
  },
]

export const CONNECTIVITY_TYPES: NodeOption[] = [
  {
    value: 'edge-gateway',
    label: 'Edge gateway',
    description: 'The node runs its own gateway for public ingress and TLS termination at the edge.',
  },
  {
    value: 'port-forward',
    label: 'Port forwarding (legacy)',
    description: 'A central proxy forwards traffic to node:port. No longer offered for new nodes: a node without public ports should join the cluster instead.',
  },
]

export function nodeOptionDescription(options: NodeOption[], value?: string): string {
  return options.find((o) => o.value === value)?.description ?? ''
}
