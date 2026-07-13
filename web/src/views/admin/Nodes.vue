<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { apiError as decodeApiError } from '@/api/client'
import { nodesApi, type CreateNodePayload } from '@/api/nodes'
import { clusterApi } from '@/api/cluster'
import { adminApi } from '@/api/admin'
import { ACCESS_MODES, CONNECTIVITY_TYPES, nodeOptionDescription } from '@/constants/node'
import FieldInfo from '@/components/FieldInfo.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'
import type {
  Server, ServerConnectivity, ClusterStatus, ClusterJoinInstructions,
  ClusterPreflight, NetCheck, NetCheckResult, ControlPlaneCert,
} from '@/api/types'

const notify = useNotificationStore()
const router = useRouter()
const license = useLicenseStore()

const nodes = ref<Server[]>([])
const loading = ref(false)
const agentImage = ref('ghcr.io/miabi-io/agent:latest')

// Edition node cap (-1 = unlimited). Count comes from the live list so it stays
// accurate after add/remove. Enforced server-side; this just surfaces it.
const nodeLimit = computed(() => license.view?.node_usage?.limit ?? -1)
const nodeCount = computed(() => nodes.value.length)
const limited = computed(() => nodeLimit.value >= 0)
const atNodeLimit = computed(() => limited.value && nodeCount.value >= nodeLimit.value)

// "I added this machine" vs "the swarm brought this machine" are different things: one
// is a placement target an operator chose, the other exists because it is in the swarm.
// A badge says which; this lets you look at one kind at a time.
const originFilter = ref<'all' | 'added' | 'cluster'>('all')
const addedCount = computed(() => nodes.value.filter((n) => !n.auto_joined).length)
const clusterCount = computed(() => nodes.value.filter((n) => n.auto_joined).length)
const visibleNodes = computed(() => {
  const list = nodes.value
  if (originFilter.value === 'added') return list.filter((n) => !n.auto_joined)
  if (originFilter.value === 'cluster') return list.filter((n) => n.auto_joined)
  return list
})

async function load() {
  loading.value = true
  try {
    nodes.value = (await nodesApi.list()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// --- Cluster (Docker Swarm) ---
const cluster = ref<ClusterStatus | null>(null)
const clusterEnabled = computed(() => cluster.value?.enabled === true)
// Command to attach a self-managed reverse proxy to the shared ingress overlay,
// so clustered apps are publicly reachable through it. Miabi attaches its own
// managed gateway automatically; this is only needed for a user-run proxy.
const ingressAttachCmd = computed(() =>
  cluster.value?.ingress_network
    ? `docker network connect ${cluster.value.ingress_network} <your-proxy-container>`
    : '',
)
const clusterBusy = ref(false)
const showEnable = ref(false)
const advertiseAddr = ref('')
// A cluster has no name of its own — Swarm gives it an unreadable id and a manager
// address that moves. Without a label the UI can only say "the cluster", which is fine
// with one and useless once someone runs prod-eu-west-1 and prod-us-east-1.
const clusterName = ref('')
const renaming = ref(false)
const renameDraft = ref('')

async function saveClusterName() {
  const name = renameDraft.value.trim()
  renaming.value = false
  clusterBusy.value = true
  try {
    cluster.value = (await clusterApi.rename(name)).data.data
    notify.success(name ? `Cluster renamed to “${name}”` : 'Cluster name cleared')
  } catch (e) {
    notify.apiError(e, 'Failed to rename the cluster')
  } finally {
    clusterBusy.value = false
  }
}
function startRename() {
  renameDraft.value = cluster.value?.name ?? ''
  renaming.value = true
}

// Workspace networks still on node-local bridges. While cluster mode is on, these
// workspaces have NO cross-node connectivity: their apps and databases sit on
// per-node islands, and an app on one node cannot resolve a database on another.
// Normal for an install that was already clustered before this version — the
// conversion only runs on the enable transition — so it must be applied explicitly.
const networksPending = computed(() => cluster.value?.networks_pending ?? 0)
const showApplyNetworking = ref(false)

async function applyNetworking() {
  showApplyNetworking.value = false
  clusterBusy.value = true
  try {
    cluster.value = (await clusterApi.applyNetworking()).data.data
    notify.success('Workspace networks converted to cluster overlays')
    load()
  } catch (e) {
    notify.apiError(e, 'Failed to apply cluster networking')
  } finally {
    clusterBusy.value = false
  }
}

async function loadCluster() {
  try {
    cluster.value = (await clusterApi.status()).data.data
  } catch { /* status is best-effort; the page still works without it */ }
}

// Preflight, loaded when the enable dialog opens. Its whole purpose is to say —
// BEFORE the swarm is formed — that this engine cannot carry the overlay data plane
// to other hosts (Docker Desktop / OrbStack run the daemon in a VM), and to list the
// ports that must be open. Both failures otherwise surface much later, as an app
// that resolves its database and then times out.
const preflight = ref<ClusterPreflight | null>(null)
const preflightLoading = ref(false)
async function loadPreflight() {
  preflightLoading.value = true
  try {
    preflight.value = (await clusterApi.preflight()).data.data
  } catch {
    preflight.value = null // best-effort: never block enabling on the advisory check
  } finally {
    preflightLoading.value = false
  }
}

// Network check: probes the real overlay between every pair of nodes and separates
// the three failures an app cannot tell apart (DNS / TCP / MTU).
const netCheck = ref<NetCheck | null>(null)
const netChecking = ref(false)
async function runNetCheck() {
  netChecking.value = true
  netCheck.value = null
  try {
    netCheck.value = (await clusterApi.netCheck()).data.data
    if (netCheck.value?.ok) notify.success('All cross-node paths are healthy')
  } catch (e) {
    notify.apiError(e, 'Network check failed')
  } finally {
    netChecking.value = false
  }
}
// The global agent service. A swarm worker with no agent runs tasks perfectly well —
// Swarm ships them to it and never involves Miabi — but Miabi holds no Docker client
// for it, so an app scheduled there has no metrics, no stats and no shell, and the
// node's disk can fill with nobody watching. Deploying the agent as a GLOBAL service
// lets Swarm carry it to every worker, including ones that join later.
const agentsDeployed = computed(() => cluster.value?.agents_deployed === true)
const agentTasks = computed(() => cluster.value?.agent_tasks ?? 0)
const agentInsecureTLS = computed(() => cluster.value?.agent_insecure_tls === true)
const agentCustomCA = computed(() => cluster.value?.agent_custom_ca === true)
const showDeployAgents = ref(false)
const showRemoveAgents = ref(false)
// How the agents treat the control plane's certificate, in descending order of safety:
//   verify — it is publicly trusted; nothing to do.
//   ca     — trust THIS authority. Verification still happens, anchored on it, so a
//            forged certificate is still rejected. Right for a private control plane.
//   skip   — trust ANY certificate. No verification. Last resort.
const agentTls = ref<'verify' | 'ca' | 'skip'>('verify')
// Two ways to supply the CA, and the FILE is usually the right one. A host that trusts
// a private CA already has it in its system store — which is exactly why the nodes work
// and the agent containers do not: the container has its own bundle and has never heard
// of it. Mounting the file the host already has beats copying its contents around, and
// it stays correct when the CA is rotated on the hosts.
const caMode = ref<'file' | 'paste'>('file')
const caCertPath = ref('/etc/pki/ca-trust/source/anchors/ca.crt')
const caCert = ref('')
const cpCert = ref<ControlPlaneCert | null>(null)
const cpCertLoading = ref(false)

// Fetch what the control plane actually serves, so nobody has to go and find a PEM —
// which is how you end up with everyone choosing "skip" instead.
async function fetchControlPlaneCert() {
  cpCertLoading.value = true
  try {
    const cert = (await clusterApi.controlPlaneCert()).data.data
    cpCert.value = cert
    caCert.value = cert.pem
    if (cert.publicly_trusted) {
      notify.success('This certificate is already publicly trusted — you can just verify')
    }
  } catch (e) {
    notify.apiError(e, 'Could not read the control plane certificate')
  } finally {
    cpCertLoading.value = false
  }
}

async function deployAgents() {
  showDeployAgents.value = false
  clusterBusy.value = true
  try {
    await clusterApi.deployAgents({
      insecureSkipVerify: agentTls.value === 'skip',
      caCertPath: agentTls.value === 'ca' && caMode.value === 'file' ? caCertPath.value.trim() : '',
      caCert: agentTls.value === 'ca' && caMode.value === 'paste' ? caCert.value.trim() : '',
    })
    notify.success('Agent deployed to every cluster node')
    await loadCluster()
    load()
  } catch (e) {
    notify.apiError(e, 'Failed to deploy the cluster agents')
  } finally {
    clusterBusy.value = false
  }
}
function openDeployAgents() {
  // Default to whatever is already in force, so a redeploy does not silently re-enable
  // verification against a certificate that still cannot be verified — which would kill
  // every agent at once.
  agentTls.value = agentInsecureTLS.value ? 'skip' : agentCustomCA.value ? 'ca' : 'verify'
  // Keep the path already in force, so a redeploy does not silently drop it.
  if (cluster.value?.agent_ca_cert_path) {
    caMode.value = 'file'
    caCertPath.value = cluster.value.agent_ca_cert_path
  }
  caCert.value = ''
  cpCert.value = null
  showDeployAgents.value = true
}
async function removeAgents() {
  showRemoveAgents.value = false
  clusterBusy.value = true
  try {
    await clusterApi.removeAgents()
    notify.success('Cluster agents removed')
    await loadCluster()
    load()
  } catch (e) {
    notify.apiError(e, 'Failed to remove the cluster agents')
  } finally {
    clusterBusy.value = false
  }
}

function verdictClass(r: NetCheckResult): string {
  if (r.payload) return 'badge-success badge-dot'
  if (r.tcp) return 'badge-warning' // MTU black hole — the sneakiest failure
  return 'badge-danger'
}

function openEnable() {
  void loadPreflight()
  clusterName.value = ''
  // Prefill with the manager's known address as a sensible default; the admin
  // should use the private/WG address peers can reach.
  const mgr = nodes.value.find((n) => n.is_local)
  advertiseAddr.value = mgr?.address || mgr?.public_ip || ''
  showEnable.value = true
}

async function enableCluster() {
  clusterBusy.value = true
  try {
    cluster.value = (await clusterApi.enable(advertiseAddr.value.trim(), clusterName.value.trim())).data.data
    showEnable.value = false
    notify.success('Cluster mode enabled')
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    clusterBusy.value = false
  }
}

const showDisableCluster = ref(false)
async function disableCluster() {
  showDisableCluster.value = false
  clusterBusy.value = true
  try {
    await clusterApi.disable()
    notify.success('Cluster mode disabled')
    await loadCluster()
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    clusterBusy.value = false
  }
}

async function joinNode(n: Server) {
  clusterBusy.value = true
  try {
    await clusterApi.joinNode(n.id)
    notify.success(`${n.name} joined the cluster`)
    await loadCluster()
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    clusterBusy.value = false
  }
}

const pendingLeave = ref<Server | null>(null)
async function leaveNode() {
  const n = pendingLeave.value
  if (!n) return
  pendingLeave.value = null
  clusterBusy.value = true
  try {
    await clusterApi.leaveNode(n.id)
    notify.success(`${n.name} removed from the cluster`)
    await loadCluster()
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    clusterBusy.value = false
  }
}

// A node can be joined when cluster mode is on, the node is a remote, online,
// non-member node.
function canJoin(n: Server): boolean {
  return clusterEnabled.value && !n.is_local && !!n.agent_connected && !n.in_swarm
}

// --- Join nodes to the cluster (header action) ---
const showJoin = ref(false)
const joinBusy = ref(false)
const joinSelected = ref<Record<number, boolean>>({})
// Manual `docker swarm join` command for hosts not connected to the manager.
const manualJoin = ref<ClusterJoinInstructions | null>(null)

// Candidate nodes for the join dialog: remote nodes not yet in the swarm
// (offline ones are listed but cannot be joined until their agent reconnects).
const joinCandidates = computed(() => nodes.value.filter((n) => !n.is_local && !n.in_swarm))
const selectedJoinIds = computed(() =>
  joinCandidates.value.filter((n) => joinSelected.value[n.id] && n.agent_connected).map((n) => n.id),
)

async function openJoin() {
  // Preselect every eligible (online) candidate.
  const sel: Record<number, boolean> = {}
  for (const n of joinCandidates.value) if (n.agent_connected) sel[n.id] = true
  joinSelected.value = sel
  manualJoin.value = null
  showJoin.value = true
  // Fetch the manual join command for hosts not connected to the manager.
  try {
    manualJoin.value = (await clusterApi.joinToken()).data.data
  } catch { /* best-effort; the host-side command just won't show */ }
}

async function joinSelectedNodes() {
  const ids = selectedJoinIds.value
  if (ids.length === 0) return
  joinBusy.value = true
  let joined = 0
  for (const id of ids) {
    try {
      await clusterApi.joinNode(id)
      joined++
    } catch (e) {
      notify.apiError(e)
    }
  }
  joinBusy.value = false
  showJoin.value = false
  if (joined > 0) notify.success(`${joined} node${joined > 1 ? 's' : ''} joined the cluster`)
  await loadCluster()
  load()
}
async function loadAgentImage() {
  try {
    const cfg = (await adminApi.getDeploymentConfig()).data.data
    const agent = cfg.images?.find((i) => i.key === 'agent')
    if (agent?.effective) agentImage.value = agent.effective
  } catch { /* fall back to the default ref */ }
}
onMounted(() => { load(); loadAgentImage(); loadCluster(); license.load() })

// --- Add node ---
const showCreate = ref(false)
const creating = ref(false)
const blankForm = (): CreateNodePayload => ({
  name: '', address: '', connectivity: 'port-forward', access_mode: 'agent',
  docker_endpoint: '', tls_ca_cert: '', tls_cert: '', tls_key: '',
})
const form = ref<CreateNodePayload>(blankForm())
const createdToken = ref<string | null>(null)

function openCreate() {
  form.value = blankForm()
  createdToken.value = null
  showCreate.value = true
}

// Endpoint placeholder per access mode.
const endpointPlaceholder = computed(() => form.value.access_mode === 'api' ? 'tcp://10.0.0.10:2376' : '')

// Hint lines describing the currently selected option.
const accessModeDesc = computed(() => nodeOptionDescription(ACCESS_MODES, form.value.access_mode))
const connectivityDesc = computed(() => nodeOptionDescription(CONNECTIVITY_TYPES, form.value.connectivity))

async function submit() {
  if (!form.value.name.trim()) return
  const mode = form.value.access_mode || 'agent'
  if (mode === 'api' && !form.value.docker_endpoint?.trim()) {
    notify.error('A Docker endpoint is required for this access mode')
    return
  }
  creating.value = true
  const payload: CreateNodePayload = {
    name: form.value.name.trim(),
    address: form.value.address?.trim() || undefined,
    connectivity: form.value.connectivity,
    access_mode: mode,
    docker_endpoint: form.value.docker_endpoint?.trim() || undefined,
    tls_ca_cert: form.value.tls_ca_cert || undefined,
    tls_cert: form.value.tls_cert || undefined,
    tls_key: form.value.tls_key || undefined,
  }
  try {
    const res = await nodesApi.create(payload)
    load()
    license.load(true) // refresh node usage so the cap chip stays accurate
    if (mode === 'agent') {
      createdToken.value = res.data.data.token
      notify.success('Node added — copy the join token now')
    } else {
      showCreate.value = false
      notify.success('Node added — connecting…')
    }
  } catch (e) {
    // The node cap is an edition limit: surface an upgrade-oriented message and
    // reveal the in-modal upgrade banner rather than a bare error toast.
    if (decodeApiError(e).code === 'NODE_LIMIT_REACHED') {
      license.load(true)
      notify.error(
        `Community edition is limited to ${nodeLimit.value} nodes. Upgrade to Enterprise to add more.`,
        { title: 'Node limit reached' },
      )
    } else {
      notify.apiError(e)
    }
  } finally {
    creating.value = false
  }
}

const ACCESS_LABELS: Record<string, string> = { socket: 'Local socket', agent: 'Agent', api: 'Docker API' }
function accessLabel(m?: string): string { return ACCESS_LABELS[m || 'agent'] || m || '—' }

const controlUrl = computed(() => window.location.origin)
const agentCommand = computed(
  () =>
    `docker run -d --name miabi-agent --restart unless-stopped \\\n` +
    `  -e MIABI_CONTROL_URL=${controlUrl.value} \\\n` +
    `  -e MIABI_NODE_TOKEN=${createdToken.value ?? '<token>'} \\\n` +
    `  -v /var/run/docker.sock:/var/run/docker.sock \\\n` +
    `  ${agentImage.value}`,
)

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}

function connectivityLabel(c?: ServerConnectivity): string {
  return c === 'edge-gateway' ? 'Edge gateway' : 'Port forwarding'
}
function statusClass(n: Server): string {
  if (n.is_local || n.agent_connected) return 'badge-success badge-dot'
  return 'badge-danger'
}
function statusLabel(n: Server): string {
  if (n.is_local) return 'manager'
  return n.agent_connected ? 'online' : 'offline'
}
function roleLabel(n: Server): string {
  return n.role || (n.is_local ? 'manager' : 'node')
}
function fmtDate(s?: string): string { return s ? new Date(s).toLocaleDateString() : '—' }
// The Agent column only applies to agent-mode nodes; socket/Docker-API have none.
function agentLabel(n: Server): string {
  if (n.access_mode !== 'agent') return 'N/A'
  return n.agent_version || (n.agent_connected ? 'connected' : '—')
}

// Swarm column: the node's role in the cluster (or "standalone"), with a hint of
// its availability when it differs from the normal "active".
function swarmLabel(n: Server): string {
  if (!clusterEnabled.value) return '—'
  const role = n.swarm_role || 'standalone'
  if (n.in_swarm && n.swarm_availability && n.swarm_availability !== 'active') {
    return `${role} · ${n.swarm_availability}`
  }
  return role
}
function swarmClass(n: Server): string {
  if (!clusterEnabled.value || !n.in_swarm) return 'badge-muted'
  if (n.swarm_role === 'leader') return 'badge-info'
  if (n.swarm_state && n.swarm_state !== 'ready') return 'badge-warning'
  return 'badge-success'
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Nodes</h1>
      <div class="header-actions">
        <span v-if="limited" class="node-usage" :class="{ 'node-usage--full': atNodeLimit }" :title="atNodeLimit ? 'Node limit reached — upgrade to add more' : 'Nodes used of your edition limit'">
          <span class="mdi mdi-server"></span> {{ nodeCount }} / {{ nodeLimit }} nodes
        </span>
        <button v-if="clusterEnabled" class="btn btn-secondary" @click="openJoin"><span class="mdi mdi-lan-connect"></span> Join the cluster</button>
        <button class="btn btn-primary" @click="openCreate"><span class="mdi mdi-plus"></span> Add node</button>
      </div>
    </div>

    <!-- Cluster (Docker Swarm) status. Cluster mode is opt-in and auto-detected;
         single-node on plain Docker stays first-class. -->
    <div v-if="cluster" class="card cluster-bar">
      <div class="cluster-bar-main">
        <span class="mdi" :class="clusterEnabled ? 'mdi-lan-connect' : 'mdi-lan-disconnect'" style="font-size: 22px"></span>
        <div>
          <div class="cluster-bar-title">
            <!-- Name it, or the UI can only say "the cluster" — fine with one, useless
                 once someone runs prod-eu-west-1 and prod-us-east-1. -->
            <template v-if="clusterEnabled && cluster.name && !renaming">
              {{ cluster.name }}
              <button type="button" class="btn-icon btn-icon-muted" title="Rename cluster" @click="startRename">
                <span class="mdi mdi-pencil-outline" style="font-size: 13px"></span>
              </button>
            </template>
            <template v-else-if="clusterEnabled && !renaming">
              Cluster networking
              <button type="button" class="btn-icon btn-icon-muted" title="Name this cluster" @click="startRename">
                <span class="mdi mdi-pencil-outline" style="font-size: 13px"></span>
              </button>
            </template>
            <form v-else-if="renaming" class="rename-form" @submit.prevent="saveClusterName">
              <input
                v-model="renameDraft"
                class="form-input"
                maxlength="40"
                placeholder="e.g. prod-eu-west-1"
                autofocus
                @keyup.esc="renaming = false"
              />
              <button type="submit" class="btn btn-primary btn-sm" :disabled="clusterBusy">Save</button>
              <button type="button" class="btn btn-secondary btn-sm" @click="renaming = false">Cancel</button>
            </form>
            <template v-else>Cluster networking</template>
            <span class="badge" :class="clusterEnabled ? 'badge-success' : 'badge-muted'">{{ clusterEnabled ? 'enabled' : 'disabled' }}</span>
          </div>
          <div class="cell-sub">
            <template v-if="clusterEnabled">Docker Swarm · {{ cluster.managers }} manager(s), {{ cluster.nodes }} node(s)<span v-if="cluster.manager_addr"> · advertises {{ cluster.manager_addr }}</span></template>
            <template v-else>Run apps across nodes on a private overlay network. Single-node on plain Docker is unaffected.</template>
            <span v-if="cluster.error" class="badge badge-danger" style="margin-left: 8px">{{ cluster.error }}</span>
          </div>
          <!-- Self-managed reverse proxy: Miabi attaches its own gateway to the
               ingress overlay automatically. If you run your own proxy, connect it
               to that overlay so clustered apps are reachable through it. -->
          <div v-if="clusterEnabled && cluster.ingress_network" class="ingress-hint">
            <span class="mdi mdi-information-outline"></span>
            Running your own reverse proxy? Attach it to the ingress overlay
            <code>{{ cluster.ingress_network }}</code>:
            <code class="ingress-cmd">{{ ingressAttachCmd }}</code>
            <button type="button" class="btn btn-ghost btn-sm" title="Copy command" @click="copy(ingressAttachCmd)">
              <span class="mdi mdi-content-copy"></span>
            </button>
          </div>
          <!-- Cluster mode is on, but some workspaces are still on node-local
               bridges, so they have no cross-node connectivity at all. Say exactly
               that, rather than leaving the admin to discover it as an app that
               can't resolve its database. -->
          <!-- Managed nodes. A swarm worker with no agent runs tasks fine — Swarm ships
               them to it and never involves Miabi — but Miabi holds no Docker client for
               it, so an app scheduled there has no metrics, no stats and no shell, and
               its disk can fill with nobody watching. Swarm can carry the agent to every
               worker itself (a global service), including ones that join later. -->
          <div v-if="clusterEnabled" class="agents">
            <template v-if="agentsDeployed">
              <span class="badge badge-success badge-dot">nodes managed</span>
              <!-- A workaround taken once to get a self-signed cert working must not
                   quietly become the permanent posture. Say it, every time. -->
              <span v-if="agentInsecureTLS" class="badge badge-warning" title="The agents accept ANY certificate for the control plane. Switch to 'Trust a certificate authority' — it still verifies — and redeploy them.">
                TLS verification off
              </span>
              <span v-else-if="agentCustomCA" class="badge badge-info" title="The agents verify the control plane against your own certificate authority.">
                custom CA
              </span>
              <span class="cell-sub">
                The agent runs on every cluster node ({{ agentTasks }} task(s)) — including nodes that
                join later — so metrics, stats, shell and housekeeping work everywhere.
              </span>
              <button type="button" class="btn btn-ghost btn-sm" :disabled="clusterBusy" @click="openDeployAgents">
                Redeploy
              </button>
              <button type="button" class="btn btn-ghost btn-sm" :disabled="clusterBusy" @click="showRemoveAgents = true">
                Remove agents
              </button>
            </template>
            <template v-else>
              <span class="badge badge-warning">nodes unmanaged</span>
              <span class="cell-sub">
                Cluster nodes run tasks, but Miabi has no Docker connection to them: apps scheduled
                there show <strong>no metrics, stats or shell</strong>, and their disks are unwatched.
              </span>
              <button type="button" class="btn btn-sm btn-primary" :disabled="clusterBusy" @click="openDeployAgents">
                <span class="mdi mdi-download-network-outline"></span> Manage cluster nodes
              </button>
            </template>
          </div>

          <!-- Network check. Cluster networking fails silently: the swarm forms, DNS
               resolves, and packets vanish — so an app looks broken when the network
               is. This probes the real overlay between every pair of nodes and names
               which of the three failures it is. -->
          <div v-if="clusterEnabled" class="netcheck">
            <div class="netcheck-head">
              <button type="button" class="btn btn-sm btn-secondary" :disabled="netChecking" @click="runNetCheck">
                <span class="mdi mdi-lan-pending"></span>
                {{ netChecking ? 'Probing every path…' : 'Run network check' }}
              </button>
              <span class="cell-sub">
                Probes DNS, a TCP connection and a 1400-byte payload between every pair of nodes.
              </span>
            </div>

            <template v-if="netCheck">
              <div class="netcheck-summary" :class="netCheck.ok ? 'ok' : 'bad'">
                <span class="mdi" :class="netCheck.ok ? 'mdi-check-circle-outline' : 'mdi-alert-circle-outline'"></span>
                {{ netCheck.summary }}
              </div>

              <div v-if="netCheck.results.length" class="table-wrapper" style="margin-top: 8px">
                <table>
                  <thead>
                    <tr><th>From → To</th><th>DNS</th><th>TCP</th><th>1400 B</th><th>Result</th></tr>
                  </thead>
                  <tbody>
                    <tr v-for="r in netCheck.results" :key="`${r.from}->${r.to}`">
                      <td class="cell-title">{{ r.from }} → {{ r.to }}<div v-if="r.ip" class="cell-sub mono">{{ r.ip }}</div></td>
                      <td><span class="mdi" :class="r.dns ? 'mdi-check text-ok' : 'mdi-close text-bad'"></span></td>
                      <td><span class="mdi" :class="r.tcp ? 'mdi-check text-ok' : 'mdi-close text-bad'"></span></td>
                      <td><span class="mdi" :class="r.payload ? 'mdi-check text-ok' : 'mdi-close text-bad'"></span></td>
                      <td><span class="badge" :class="verdictClass(r)">{{ r.verdict }}</span></td>
                    </tr>
                  </tbody>
                </table>
              </div>

              <!-- A node with no Miabi agent cannot host a probe, so it is absent from the
                   matrix entirely. Say so, or its absence reads as "healthy". -->
              <div v-for="p in netCheck.probes.filter((x) => !x.reachable)" :key="p.server_id" class="cell-sub" style="margin-top: 6px">
                <span class="mdi mdi-alert-outline"></span>
                <strong>{{ p.node_name }}</strong> could not be probed — {{ p.error }}
              </div>
            </template>
          </div>

          <div v-if="clusterEnabled && networksPending > 0" class="pending-hint">
            <span class="mdi mdi-alert-outline"></span>
            <span>
              <strong>{{ networksPending }} workspace network(s) are still node-local bridges.</strong>
              Apps and databases in them can't reach each other across nodes — an app on one node
              won't resolve a database on another. Convert them to cluster overlays to fix it.
            </span>
            <button type="button" class="btn btn-sm btn-primary" :disabled="clusterBusy" @click="showApplyNetworking = true">
              Apply cluster networking
            </button>
          </div>
        </div>
      </div>
      <div>
        <button v-if="!clusterEnabled" class="btn btn-secondary" :disabled="clusterBusy" @click="openEnable">Enable cluster</button>
        <button v-else class="btn btn-secondary" :disabled="clusterBusy" @click="showDisableCluster = true">Disable cluster</button>
      </div>
    </div>

    <div class="card">
      <!-- Only worth showing once the swarm has actually brought a node in: with none,
           every node is one an admin added and the filter is noise. -->
      <div v-if="clusterCount > 0" class="origin-filter">
        <button type="button" :class="{ active: originFilter === 'all' }" @click="originFilter = 'all'">
          All <span class="count">{{ nodes.length }}</span>
        </button>
        <button type="button" :class="{ active: originFilter === 'added' }" @click="originFilter = 'added'">
          Added <span class="count">{{ addedCount }}</span>
        </button>
        <button
          type="button"
          :class="{ active: originFilter === 'cluster' }"
          title="Nodes the swarm brought in: the agent registered itself, rather than an admin adding the machine"
          @click="originFilter = 'cluster'"
        >
          From the cluster <span class="count">{{ clusterCount }}</span>
        </button>
      </div>

      <div v-if="loading && nodes.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="nodes.length === 0" class="empty-state">
        <span class="mdi mdi-server-network" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No nodes</h3>
        <p>Add a node to run apps on additional Docker hosts.</p>
        <button class="btn btn-primary mt-4" @click="openCreate">Add a node</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>Role</th><th>Access</th><th>Connectivity</th><th>Status</th><th v-if="clusterEnabled">Swarm</th><th>Agent</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="n in visibleNodes" :key="n.id" class="row-clickable" @click="router.push(`/admin/nodes/${n.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-server" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">
                      {{ n.name }}
                      <span v-if="n.cordoned" class="badge badge-warning" style="margin-left: 8px">cordoned</span>
                      <!-- The cluster brought this node in, an admin did not: the global
                           agent service landed on a swarm member and it registered itself. -->
                      <span
                        v-if="n.auto_joined"
                        class="badge badge-info"
                        style="margin-left: 8px"
                        title="Joined by the cluster — the agent registered itself from the swarm, rather than an admin adding it"
                      >cluster</span>
                    </span>
                    <span class="cell-sub">{{ n.address || (n.is_local ? 'local socket' : '—') }}</span>
                  </span>
                </div>
              </td>
              <td><span class="badge" :class="roleLabel(n) === 'manager' ? 'badge-info' : 'badge-muted'">{{ roleLabel(n) }}</span></td>
              <td>
                <span class="badge badge-muted">{{ accessLabel(n.access_mode) }}</span>
                <span v-if="n.access_mode === 'api' && n.tls_enabled" class="mdi mdi-lock-outline" title="TLS" style="margin-left: 4px"></span>
              </td>
              <td><span class="badge badge-muted">{{ connectivityLabel(n.connectivity) }}</span></td>
              <td><span class="badge" :class="statusClass(n)">{{ statusLabel(n) }}</span></td>
              <td v-if="clusterEnabled">
                <span class="badge" :class="swarmClass(n)">{{ swarmLabel(n) }}</span>
                <button v-if="canJoin(n)" class="btn btn-xs btn-secondary" style="margin-left: 6px" :disabled="clusterBusy" @click.stop="joinNode(n)">Join</button>
                <button v-else-if="n.in_swarm && !n.is_local" class="btn btn-xs btn-secondary" style="margin-left: 6px" :disabled="clusterBusy" @click.stop="pendingLeave = n">Leave</button>
              </td>
              <td class="cell-sub">{{ agentLabel(n) }}</td>
              <td class="cell-sub">{{ fmtDate(n.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Add node / token reveal -->
    <Teleport to="body">
      <div v-if="showCreate" class="modal-overlay" @click.self="showCreate = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Add node</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
          </div>

          <template v-if="!createdToken">
            <form @submit.prevent="submit">
              <div class="modal-body">
                <!-- Edition node cap reached: nodes are a paid scale dimension.
                     Block the add and point to the upgrade page. -->
                <div v-if="atNodeLimit" class="app-banner app-banner--warning">
                  <span class="mdi mdi-lock-outline app-banner-icon"></span>
                  <div class="app-banner-content">
                    <p class="app-banner-title">Node limit reached</p>
                    <p class="app-banner-text">
                      Community edition is limited to {{ nodeLimit }} nodes (standalone or Swarm).
                      Upgrade to Enterprise to add more.
                    </p>
                    <router-link to="/admin/license" class="btn btn-secondary btn-sm" style="margin-top: 10px">Upgrade</router-link>
                  </div>
                </div>
                <div class="form-group">
                  <label class="form-label">Name</label>
                  <input v-model="form.name" class="form-input" placeholder="e.g. edge-eu-1" required autofocus />
                </div>
                <div class="form-group">
                  <span class="form-label label-row">
                    Access mode
                    <FieldInfo :items="ACCESS_MODES" title="Access modes explained" />
                  </span>
                  <select v-model="form.access_mode" class="form-input">
                    <option v-for="o in ACCESS_MODES" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                  <p class="form-hint">{{ accessModeDesc }}</p>
                </div>

                <!-- api: endpoint + TLS -->
                <template v-if="form.access_mode === 'api'">
                  <div class="form-group">
                    <label class="form-label">Docker endpoint</label>
                    <input v-model="form.docker_endpoint" class="form-input" :placeholder="endpointPlaceholder" required style="font-family: monospace" />
                    <p class="cell-sub" style="margin-top: 4px">The node must be reachable from the manager (inbound).</p>
                  </div>
                  <div class="form-group">
                    <label class="form-label">TLS <span class="cell-sub">(optional — leave blank for plaintext on a trusted network)</span></label>
                    <textarea v-model="form.tls_ca_cert" class="form-input" rows="2" placeholder="CA certificate (PEM)" style="font-family: monospace; font-size: 12px"></textarea>
                    <textarea v-model="form.tls_cert" class="form-input" rows="2" placeholder="Client certificate (PEM) — for mTLS" style="font-family: monospace; font-size: 12px; margin-top: 6px"></textarea>
                    <textarea v-model="form.tls_key" class="form-input" rows="2" placeholder="Client key (PEM) — stored encrypted" style="font-family: monospace; font-size: 12px; margin-top: 6px"></textarea>
                  </div>
                </template>

                <div v-if="form.access_mode !== 'api'" class="form-group">
                  <label class="form-label">Address <span class="cell-sub">(host/IP the proxy reaches published ports at)</span></label>
                  <input v-model="form.address" class="form-input" placeholder="e.g. 10.0.0.7" />
                </div>
                <div class="form-group" style="margin-bottom: 0">
                  <span class="form-label label-row">
                    Connectivity
                    <FieldInfo :items="CONNECTIVITY_TYPES" title="Connectivity types explained" placement="top" />
                  </span>
                  <select v-model="form.connectivity" class="form-input">
                    <option v-for="o in CONNECTIVITY_TYPES" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                  <p class="form-hint">{{ connectivityDesc }}</p>
                </div>
              </div>
              <div class="modal-footer">
                <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
                <button type="submit" class="btn btn-primary" :disabled="creating || atNodeLimit">{{ creating ? 'Saving…' : 'Add node' }}</button>
              </div>
            </form>
          </template>

          <template v-else>
            <div class="modal-body">
              <div class="app-banner app-banner--warning">
                <span class="mdi mdi-alert-outline app-banner-icon"></span>
                <div class="app-banner-content">
                  <p class="app-banner-title">Copy the join token now</p>
                  <p class="app-banner-text">This is the only time it is shown. Run the agent on the node:</p>
                </div>
              </div>
              <div class="code-block" style="margin-top: 14px; white-space: pre">{{ agentCommand }}</div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="copy(createdToken!)">Copy token</button>
              <button type="button" class="btn btn-secondary" @click="copy(agentCommand)">Copy command</button>
              <button type="button" class="btn btn-primary" @click="showCreate = false">Done</button>
            </div>
          </template>
        </div>
      </div>
    </Teleport>

    <!-- Join nodes to the cluster -->
    <Teleport to="body">
      <div v-if="showJoin" class="modal-overlay" @click.self="showJoin = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Join nodes to the cluster</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showJoin = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <!-- Managed nodes (connected to the manager): one-click join. -->
            <p v-if="joinCandidates.length === 0" class="cell-sub">All managed nodes are already in the cluster.</p>
            <template v-else>
              <p class="cell-sub" style="margin-bottom: 12px">
                Select the nodes to join the swarm overlay network. Offline nodes can't be joined from here until their agent reconnects — use the manual command below.
              </p>
              <label
                v-for="n in joinCandidates"
                :key="n.id"
                class="join-row"
                :class="{ 'join-row--disabled': !n.agent_connected }"
              >
                <input type="checkbox" :disabled="!n.agent_connected" v-model="joinSelected[n.id]" />
                <span class="join-row-name">{{ n.name }}</span>
                <span class="badge" :class="n.agent_connected ? 'badge-success badge-dot' : 'badge-danger'">{{ n.agent_connected ? 'online · standalone' : 'offline' }}</span>
              </label>
            </template>

            <!-- Manual join: for a host not connected to the manager (offline or
                 unmanaged). The operator runs the command on the host itself. -->
            <div class="manual-join">
              <div class="manual-join-title">Join a node manually</div>
              <p class="cell-sub" style="margin-bottom: 8px">
                For a host that isn't connected to Miabi, run this on the host. It must reach the manager on ports 2377/tcp, 7946/tcp+udp and 4789/udp.
              </p>
              <div v-if="manualJoin" class="code-block" style="white-space: pre-wrap; word-break: break-all">{{ manualJoin.command }}</div>
              <p v-else class="cell-sub">Join command unavailable.</p>
              <button v-if="manualJoin" type="button" class="btn btn-secondary btn-sm" style="margin-top: 10px" @click="copy(manualJoin.command)">Copy command</button>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showJoin = false">Cancel</button>
            <button type="button" class="btn btn-primary" :disabled="joinBusy || selectedJoinIds.length === 0" @click="joinSelectedNodes">
              {{ joinBusy ? 'Joining…' : selectedJoinIds.length ? `Join ${selectedJoinIds.length}` : 'Join' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Enable cluster mode -->
    <Teleport to="body">
      <div v-if="showEnable" class="modal-overlay" @click.self="showEnable = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Enable cluster mode</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEnable = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="enableCluster">
            <div class="modal-body">
              <p class="cell-sub" style="margin-bottom: 12px">
                The manager initializes a Docker Swarm. Member nodes can then be joined to a private overlay network.
                If Docker is already in swarm mode, Miabi adopts it instead.
              </p>

              <!-- Preflight. A VM-backed Docker engine (Docker Desktop, OrbStack) forms
                   a swarm and resolves DNS perfectly, then drops every cross-node packet,
                   because VXLAN and IPSec cannot be forwarded into the VM. Saying that here
                   costs one paragraph; discovering it later costs an afternoon. -->
              <div v-if="preflightLoading" class="cell-sub" style="margin-bottom: 12px">Checking this host…</div>
              <template v-else-if="preflight">
                <div
                  v-for="f in preflight.findings"
                  :key="f.title"
                  class="pf-finding"
                  :class="f.severity === 'blocker' ? 'pf-blocker' : 'pf-warning'"
                >
                  <div class="pf-title">
                    <span class="mdi" :class="f.severity === 'blocker' ? 'mdi-alert-octagon' : 'mdi-alert-outline'"></span>
                    {{ f.title }}
                  </div>
                  <p class="pf-detail">{{ f.detail }}</p>
                </div>

                <details class="pf-ports">
                  <summary>Ports that must be open between every pair of nodes</summary>
                  <table class="pf-table">
                    <tbody>
                      <tr v-for="r in preflight.firewall" :key="r.port">
                        <td><code>{{ r.port }}</code></td>
                        <td class="cell-sub">{{ r.purpose }}</td>
                      </tr>
                    </tbody>
                  </table>
                </details>
              </template>

              <div class="form-group">
                <label class="form-label">Cluster name <span class="text-muted">(optional)</span></label>
                <input v-model="clusterName" class="form-input" maxlength="40" placeholder="e.g. prod-eu-west-1" />
                <p class="form-hint">
                  A label for this cluster. Swarm identifies it by an unreadable id and a manager
                  address that moves, so without a name the panel can only say “the cluster”. You can
                  set it later.
                </p>
              </div>

              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Advertise address</label>
                <input v-model="advertiseAddr" class="form-input" placeholder="e.g. 10.0.0.1" style="font-family: monospace" autofocus />
                <p class="form-hint">The address swarm peers reach this manager on — use a private/WG address reachable from your nodes.</p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showEnable = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="clusterBusy || !advertiseAddr.trim()">{{ clusterBusy ? 'Enabling…' : 'Enable cluster' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Deploying the agents grants Miabi the Docker socket — root-equivalent — on every
         machine in the swarm, now and in future. It also has to actually connect: a
         control plane behind a self-signed certificate rejects every agent with
         "certificate signed by unknown authority", which is a dead end unless the choice
         is offered here. -->
    <Teleport to="body">
      <div v-if="showDeployAgents" class="modal-overlay" @click.self="showDeployAgents = false">
        <div class="modal">
          <div class="modal-header">
            <h3>Manage cluster nodes</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDeployAgents = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="deployAgents">
            <div class="modal-body">
              <p class="cell-sub" style="margin-top: 0">
                Swarm installs the Miabi agent on every node in this cluster, and on any node that
                joins later. Metrics, stats, shell and housekeeping then work on all of them.
              </p>
              <p class="cell-sub">
                The agent mounts each node's Docker socket, which is <strong>root-equivalent</strong>
                on that host. Only do this for machines you would trust Miabi to administer.
              </p>

              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Control-plane TLS</label>
                <select v-model="agentTls" class="form-select">
                  <option value="verify">Verify the certificate (publicly trusted)</option>
                  <option value="ca">Trust a certificate authority — self-signed or private CA</option>
                  <option value="skip">Skip verification — last resort</option>
                </select>

                <!-- Verify: the goal state, and the failure it produces if the cert is
                     private, so the operator knows which option they actually need. -->
                <p v-if="agentTls === 'verify'" class="form-hint">
                  If your control plane uses a self-signed or private-CA certificate, the agents will
                  fail with <code>certificate signed by unknown authority</code> and never connect —
                  choose “Trust a certificate authority” instead.
                </p>

                <!-- Trust a CA: verification still happens, anchored on their own authority.
                     Fetching the certificate is what keeps this from being a research task. -->
                <template v-else-if="agentTls === 'ca'">
                  <p class="form-hint">
                    The agents still <strong>verify</strong> — just against this authority instead of
                    the public ones. A forged certificate is still rejected.
                  </p>

                  <!-- The file is the right default, and the reason is the whole bug: a host
                       that trusts a private CA has it in its system store, but the agent
                       container has its own bundle and has never heard of it. Mount what the
                       host already has, rather than copying its contents through an env var. -->
                  <div class="ca-mode">
                    <label><input v-model="caMode" type="radio" value="file" /> A CA file on the nodes</label>
                    <label><input v-model="caMode" type="radio" value="paste" /> Paste the certificate</label>
                  </div>

                  <template v-if="caMode === 'file'">
                    <input v-model="caCertPath" class="form-input mono" placeholder="/etc/pki/ca-trust/source/anchors/my-ca.crt" />
                    <p class="form-hint">
                      Bind-mounted read-only into each agent. Your nodes already trust this CA — that
                      is why <code>curl</code> works on the host and fails inside a container, which
                      has its own certificate bundle. The file must exist at this path on
                      <strong>every</strong> node, including ones that join later.
                    </p>
                  </template>

                  <template v-else>
                  <div class="ca-actions">
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="cpCertLoading" @click="fetchControlPlaneCert">
                      <span class="mdi mdi-certificate-outline"></span>
                      {{ cpCertLoading ? 'Reading…' : "Use the control plane's certificate" }}
                    </button>
                  </div>

                  <!-- Fetched over an unverified connection, so the operator confirms it by
                       fingerprint. Showing it is what makes the confirmation meaningful. -->
                  <div v-if="cpCert" class="ca-cert">
                    <div v-if="cpCert.publicly_trusted" class="form-hint">
                      <span class="mdi mdi-check-circle-outline"></span>
                      This certificate is <strong>already publicly trusted</strong> — you can simply
                      choose “Verify the certificate”; no CA needs distributing.
                    </div>
                    <!-- The trap: trusting a CA does NOT skip the hostname check. A cert
                         that names nothing (Goma's default has no SANs at all) fails
                         however well it is trusted. Say it here, or the operator picks
                         this option and hits a second, different, confusing error. -->
                    <div v-else-if="!cpCert.matches_host" class="form-hint form-hint-warn">
                      <span class="mdi mdi-alert-octagon-outline"></span>
                      <strong>This certificate does not name <code>{{ cpCert.dial_host }}</code>.</strong>
                      <template v-if="!cpCert.hosts?.length"> It names no hosts at all.</template>
                      <template v-else> It names only {{ cpCert.hosts.join(', ') }}.</template>
                      Trusting it will <strong>not</strong> work: the agents will still fail with
                      <code>cannot validate certificate for {{ cpCert.dial_host }}</code>, because
                      trusting a CA does not skip the hostname check.
                      Issue a certificate that includes <code>{{ cpCert.dial_host }}</code> — or use
                      “Skip verification” until you have one.
                    </div>
                    <dl>
                      <dt>Subject</dt><dd>{{ cpCert.subject }}</dd>
                      <dt>Issuer</dt><dd>{{ cpCert.issuer }}<span v-if="cpCert.self_signed"> (self-signed)</span></dd>
                      <dt>Expires</dt><dd>{{ new Date(cpCert.not_after).toLocaleString() }}</dd>
                      <dt>SHA-256</dt><dd class="mono">{{ cpCert.fingerprint }}</dd>
                    </dl>
                    <!-- A server usually sends its leaf, not its CA. Pinning the leaf
                         works until the cert is renewed — then every agent drops off at
                         once, and nothing says why. Better to say so now. -->
                    <p v-if="cpCert.matches_host && !cpCert.anchor_is_ca" class="form-hint form-hint-warn">
                      <span class="mdi mdi-alert-outline"></span>
                      Your control plane sent its own certificate, not the CA that signed it — so this
                      pins <strong>that certificate</strong>. It will work until the certificate is
                      <strong>renewed</strong>, and then every agent will stop connecting at once.
                      For a durable anchor, paste your <strong>CA certificate</strong> below instead.
                    </p>
                    <p class="form-hint">
                      Check the fingerprint against the host before trusting it —
                      <code>openssl x509 -noout -fingerprint -sha256</code>.
                    </p>
                  </div>

                  <textarea
                    v-model="caCert"
                    class="form-input ca-pem"
                    rows="5"
                    placeholder="-----BEGIN CERTIFICATE-----&#10;…&#10;-----END CERTIFICATE-----"
                  ></textarea>
                  </template>
                </template>

                <!-- Skip: name the actual risk, not a generic scold. -->
                <p v-else class="form-hint form-hint-warn">
                  <span class="mdi mdi-alert-outline"></span>
                  <strong>Not recommended in production.</strong>
                  The agents will accept <strong>any</strong> certificate for
                  <code>{{ cluster?.manager_addr || 'the control plane' }}</code>, so anyone able to
                  intercept their connections could impersonate it — and the control plane drives
                  Docker on every node. Prefer “Trust a certificate authority”, which still verifies.
                </p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showDeployAgents = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="clusterBusy">
                {{ clusterBusy ? 'Deploying…' : 'Deploy agents' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="showRemoveAgents"
      title="Remove cluster agents?"
      message="The agent is removed from every cluster node. They keep running their tasks — Swarm schedules those itself — but Miabi loses its Docker connection to them.

Apps scheduled on those nodes will stop showing metrics, stats and a shell. The node records themselves are kept."
      confirm-label="Remove agents"
      variant="danger"
      :busy="clusterBusy"
      @confirm="removeAgents"
      @cancel="showRemoveAgents = false"
    />

    <ConfirmDialog
      :open="showApplyNetworking"
      title="Apply cluster networking?"
      message="Each workspace network is converted from a node-local bridge to a cluster overlay, so apps and databases reach each other across nodes. Containers are NOT restarted, but connections open inside a workspace drop briefly while it switches over."
      confirm-label="Apply"
      :busy="clusterBusy"
      @confirm="applyNetworking"
      @cancel="showApplyNetworking = false"
    />

    <ConfirmDialog
      :open="showDisableCluster"
      title="Disable cluster mode?"
      message="The manager and all member nodes will leave the swarm. Workspace networks are moved back to node-local bridges first, so apps and databases stop being reachable across nodes — anything relying on that will break. Containers are not restarted."
      confirm-label="Disable cluster"
      variant="danger"
      :busy="clusterBusy"
      @confirm="disableCluster"
      @cancel="showDisableCluster = false"
    />

    <ConfirmDialog
      :open="!!pendingLeave"
      title="Remove node from cluster?"
      :message="`Remove ${pendingLeave?.name} from the cluster?`"
      confirm-label="Remove"
      variant="danger"
      :busy="clusterBusy"
      @confirm="leaveNode"
      @cancel="pendingLeave = null"
    />
  </div>
</template>

<style scoped>
.label-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.cluster-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 18px;
  margin-bottom: 16px;
}
.cluster-bar-main {
  display: flex;
  align-items: flex-start;
  gap: 14px;
}
.cluster-bar-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}
.ingress-hint {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-muted);
}
.ingress-cmd {
  font-family: var(--font-mono, monospace);
  user-select: all;
}
.pending-hint {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 10px;
  padding: 8px 10px;
  border: 1px solid var(--warning-border, #f5c26b);
  border-radius: 6px;
  background: var(--warning-bg, rgba(245, 194, 107, 0.12));
  font-size: 12px;
  color: var(--text);
}
.pending-hint > span:first-child {
  color: var(--warning, #b45309);
  font-size: 16px;
}
/* Preflight (enable dialog) */
.pf-finding {
  border: 1px solid;
  border-radius: 6px;
  padding: 8px 10px;
  margin-bottom: 10px;
  font-size: 12px;
}
.pf-blocker {
  border-color: var(--danger-border, #f0a6a6);
  background: var(--danger-bg, rgba(220, 90, 90, 0.1));
}
.pf-warning {
  border-color: var(--warning-border, #f5c26b);
  background: var(--warning-bg, rgba(245, 194, 107, 0.12));
}
.pf-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 600;
}
.pf-detail {
  margin: 4px 0 0;
  color: var(--text-muted);
}
.pf-ports {
  margin-bottom: 12px;
  font-size: 12px;
}
.pf-ports summary {
  cursor: pointer;
  color: var(--text-muted);
}
.pf-table td {
  padding: 4px 8px 4px 0;
  vertical-align: top;
}
.rename-form {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.rename-form .form-input {
  width: 180px;
  padding: 3px 8px;
  font-size: 13px;
}
.origin-filter {
  display: flex;
  gap: 4px;
  padding: 10px 14px 0;
  font-size: 12px;
}
.origin-filter button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border: 1px solid transparent;
  border-radius: 999px;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
}
.origin-filter button.active {
  border-color: var(--border);
  background: var(--bg-subtle, rgba(127, 127, 127, 0.1));
  color: var(--text);
}
.origin-filter .count {
  font-variant-numeric: tabular-nums;
  opacity: 0.7;
}
.ca-mode {
  display: flex;
  gap: 14px;
  margin: 8px 0;
  font-size: 12px;
}
.ca-mode label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
}
.ca-actions {
  margin: 8px 0;
}
.ca-cert {
  margin: 8px 0;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 12px;
}
.ca-cert dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 2px 10px;
  margin: 6px 0;
}
.ca-cert dt {
  color: var(--text-muted);
}
.ca-cert dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.ca-pem {
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  margin-top: 8px;
}
.form-hint-warn {
  color: var(--warning, #b45309);
}
.agents {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
  font-size: 12px;
}
/* Network check */
.netcheck {
  margin-top: 10px;
}
.netcheck-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 12px;
}
.netcheck-summary {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  font-weight: 600;
}
.netcheck-summary.ok {
  color: var(--success, #15803d);
}
.netcheck-summary.bad {
  color: var(--danger, #b91c1c);
}
.text-ok {
  color: var(--success, #15803d);
}
.text-bad {
  color: var(--danger, #b91c1c);
}
.mono {
  font-family: var(--font-mono, monospace);
}
.header-actions {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.node-usage {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-muted);
  background: var(--surface-2, rgba(127, 127, 127, 0.1));
}
.node-usage--full {
  color: var(--warning, #b7791f);
  background: var(--warning-bg, rgba(183, 121, 31, 0.12));
}
.join-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 0;
  cursor: pointer;
}
.join-row--disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.join-row-name {
  flex: 1;
  font-weight: 500;
}
.manual-join {
  margin-top: 18px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.manual-join-title {
  font-weight: 600;
  margin-bottom: 6px;
}
</style>
