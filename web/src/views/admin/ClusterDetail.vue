<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { clustersApi } from '@/api/clusters'
import { clusterApi } from '@/api/cluster'
import { nodesApi } from '@/api/nodes'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'
import { copyText } from '@/utils/clipboard'
import type {
  Cluster, Server, ClusterStatus, ClusterJoinInstructions,
  ClusterPreflight, NetCheck, NetCheckResult, ControlPlaneCert,
} from '@/api/types'

const notify = useNotificationStore()
const route = useRoute()
const router = useRouter()
const id = String(route.params.id)

const cluster = ref<Cluster | null>(null)
const nodes = ref<Server[]>([])
const loading = ref(false)

const isDefault = computed(() => cluster.value?.is_default === true)
const label = computed(() => cluster.value?.display_name || cluster.value?.name || 'Cluster')
const members = computed(() => nodes.value.filter((n) => n.cluster_id === cluster.value?.id))
const managerNode = computed(() =>
  isDefault.value ? nodes.value.find((n) => n.is_local) : nodes.value.find((n) => n.id === cluster.value?.manager_server_id),
)
const ingressNode = computed(() => {
  if (isDefault.value) return nodes.value.find((n) => n.is_local)
  const ingressID = cluster.value?.ingress_server_id
  return ingressID ? nodes.value.find((n) => n.id === ingressID) : undefined
})

async function load() {
  loading.value = true
  try {
    const [c, n] = await Promise.all([clustersApi.get(id), nodesApi.list()])
    cluster.value = c.data.data
    nodes.value = n.data.data ?? []
    await loadStatus()
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const showEdit = ref(false)
const saving = ref(false)
const editForm = ref({ display_name: '', location_code: '' })
function openEdit() {
  editForm.value = {
    display_name: cluster.value?.display_name ?? '',
    location_code: cluster.value?.location_code ?? '',
  }
  showEdit.value = true
}
async function saveEdit() {
  if (!cluster.value) return
  saving.value = true
  try {
    cluster.value = (await clustersApi.update(cluster.value.id, {
      display_name: editForm.value.display_name.trim(),
      location_code: editForm.value.location_code.trim(),
    })).data.data
    showEdit.value = false
    notify.success('Cluster updated')
  } catch (e) {
    notify.apiError(e, 'Failed to update the cluster')
  } finally {
    saving.value = false
  }
}

// --- Docker Swarm (default cluster) ---
const status = ref<ClusterStatus | null>(null)
const swarmEnabled = computed(() => status.value?.enabled === true)
const busy = ref(false)
const ingressAttachCmd = computed(() =>
  status.value?.ingress_network
    ? `docker network connect ${status.value.ingress_network} <your-proxy-container>`
    : '',
)

async function loadStatus() {
  try {
    status.value = (await clusterApi.status(id)).data.data
  } catch { /* best-effort; the page works without it */ }
}

// Normal for an install that was already clustered before it upgraded: the conversion only
// runs on the enable transition, so it has to be applied explicitly.
const networksPending = computed(() => status.value?.networks_pending ?? 0)
const showApplyNetworking = ref(false)
async function applyNetworking() {
  showApplyNetworking.value = false
  busy.value = true
  try {
    status.value = (await clusterApi.applyNetworking(id)).data.data
    notify.success('Workspace networks converted to cluster overlays')
  } catch (e) {
    notify.apiError(e, 'Failed to apply cluster networking')
  } finally {
    busy.value = false
  }
}

const preflight = ref<ClusterPreflight | null>(null)
const preflightLoading = ref(false)
async function loadPreflight() {
  preflightLoading.value = true
  try {
    preflight.value = (await clusterApi.preflight(id)).data.data
  } catch {
    preflight.value = null
  } finally {
    preflightLoading.value = false
  }
}

const netCheck = ref<NetCheck | null>(null)
const netChecking = ref(false)
async function runNetCheck() {
  netChecking.value = true
  netCheck.value = null
  try {
    netCheck.value = (await clusterApi.netCheck(id)).data.data
    if (netCheck.value?.ok) notify.success('All cross-node paths are healthy')
  } catch (e) {
    notify.apiError(e, 'Network check failed')
  } finally {
    netChecking.value = false
  }
}
function verdictClass(r: NetCheckResult): string {
  if (r.payload) return 'badge-success badge-dot'
  if (r.tcp) return 'badge-warning'
  return 'badge-danger'
}

const agentsDeployed = computed(() => status.value?.agents_deployed === true)
const agentTasks = computed(() => status.value?.agent_tasks ?? 0)
const agentInsecureTLS = computed(() => status.value?.agent_insecure_tls === true)
const agentCustomCA = computed(() => status.value?.agent_custom_ca === true)
const showDeployAgents = ref(false)
const showRemoveAgents = ref(false)
const agentTls = ref<'verify' | 'ca' | 'skip'>('verify')
const caMode = ref<'file' | 'paste'>('file')
const caCertPath = ref('/etc/pki/ca-trust/source/anchors/ca.crt')
const caCert = ref('')
const cpCert = ref<ControlPlaneCert | null>(null)
const cpCertLoading = ref(false)

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

function openDeployAgents() {
  // Keep what is in force, so a redeploy cannot silently re-enable verification that still fails.
  agentTls.value = agentInsecureTLS.value ? 'skip' : agentCustomCA.value ? 'ca' : 'verify'
  if (status.value?.agent_ca_cert_path) {
    caMode.value = 'file'
    caCertPath.value = status.value.agent_ca_cert_path
  }
  caCert.value = ''
  cpCert.value = null
  showDeployAgents.value = true
}
async function deployAgents() {
  showDeployAgents.value = false
  busy.value = true
  try {
    await clusterApi.deployAgents(id, {
      insecureSkipVerify: agentTls.value === 'skip',
      caCertPath: agentTls.value === 'ca' && caMode.value === 'file' ? caCertPath.value.trim() : '',
      caCert: agentTls.value === 'ca' && caMode.value === 'paste' ? caCert.value.trim() : '',
    })
    notify.success('Agent deployed to every cluster node')
    await load()
  } catch (e) {
    notify.apiError(e, 'Failed to deploy the cluster agents')
  } finally {
    busy.value = false
  }
}
async function removeAgents() {
  showRemoveAgents.value = false
  busy.value = true
  try {
    await clusterApi.removeAgents(id)
    notify.success('Cluster agents removed')
    await load()
  } catch (e) {
    notify.apiError(e, 'Failed to remove the cluster agents')
  } finally {
    busy.value = false
  }
}

const showEnable = ref(false)
const advertiseAddr = ref('')
function openEnable() {
  void loadPreflight()
  const mgr = managerNode.value
  advertiseAddr.value = mgr?.address || mgr?.public_ip || ''
  showEnable.value = true
}
async function enableSwarm() {
  busy.value = true
  try {
    status.value = (await clusterApi.enable(id, advertiseAddr.value.trim())).data.data
    showEnable.value = false
    notify.success('Cluster mode enabled')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

const showDisable = ref(false)
async function disableSwarm() {
  showDisable.value = false
  busy.value = true
  try {
    await clusterApi.disable(id)
    notify.success('Cluster mode disabled')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

const pendingLeave = ref<Server | null>(null)
async function leaveNode() {
  const n = pendingLeave.value
  if (!n) return
  pendingLeave.value = null
  busy.value = true
  try {
    await clusterApi.leaveNode(id, n.id)
    notify.success(`${n.display_name || n.name} removed from the cluster`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

const showJoin = ref(false)
const joinBusy = ref(false)
const joinSelected = ref<Record<number, boolean>>({})
const manualJoin = ref<ClusterJoinInstructions | null>(null)
const joinCandidates = computed(() =>
  nodes.value.filter((n) => !n.is_local && !n.in_swarm && n.id !== cluster.value?.manager_server_id),
)
const selectedJoinIds = computed(() =>
  joinCandidates.value.filter((n) => joinSelected.value[n.id] && n.agent_connected).map((n) => n.id),
)
async function openJoin() {
  const sel: Record<number, boolean> = {}
  for (const n of joinCandidates.value) if (n.agent_connected) sel[n.id] = true
  joinSelected.value = sel
  manualJoin.value = null
  showJoin.value = true
  try {
    manualJoin.value = (await clusterApi.joinToken(id)).data.data
  } catch { /* best-effort; the host-side command just won't show */ }
}
async function joinSelectedNodes() {
  const ids = selectedJoinIds.value
  if (ids.length === 0) return
  joinBusy.value = true
  let joined = 0
  for (const nodeID of ids) {
    try {
      await clusterApi.joinNode(id, nodeID)
      joined++
    } catch (e) {
      notify.apiError(e)
    }
  }
  joinBusy.value = false
  showJoin.value = false
  if (joined > 0) notify.success(`${joined} node${joined > 1 ? 's' : ''} joined the cluster`)
  await load()
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}

function statusClass(n: Server): string {
  return n.is_local || n.agent_connected ? 'badge-success badge-dot' : 'badge-danger'
}
function statusLabel(n: Server): string {
  if (n.is_local) return 'manager'
  return n.agent_connected ? 'online' : 'offline'
}
function swarmLabel(n: Server): string {
  const role = n.swarm_role || 'standalone'
  if (n.in_swarm && n.swarm_availability && n.swarm_availability !== 'active') {
    return `${role} · ${n.swarm_availability}`
  }
  return role
}
function swarmClass(n: Server): string {
  if (!n.in_swarm) return 'badge-muted'
  if (n.swarm_role === 'leader') return 'badge-info'
  if (n.swarm_state && n.swarm_state !== 'ready') return 'badge-warning'
  return 'badge-success'
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <router-link to="/admin/clusters" class="back-link"><span class="mdi mdi-arrow-left"></span> Clusters</router-link>
        <h1>
          {{ label }}
          <code v-if="cluster?.name" class="handle" title="Name (fixed at creation)">{{ cluster.name }}</code>
          <span v-if="isDefault" class="badge badge-info">default</span>
        </h1>
      </div>
      <div v-if="cluster" class="header-actions">
        <button v-if="swarmEnabled" class="btn btn-secondary" @click="openJoin"><span class="mdi mdi-lan-connect"></span> Join nodes</button>
        <button class="btn btn-secondary" @click="openEdit"><span class="mdi mdi-pencil-outline"></span> Edit</button>
      </div>
    </div>

    <div v-if="loading && !cluster" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="cluster">
      <div class="card overview">
        <dl>
          <dt>Location</dt>
          <dd>
            {{ cluster.display_name || '—' }}
            <span v-if="cluster.location_code" class="badge badge-muted mono">{{ cluster.location_code }}</span>
          </dd>
          <dt>Mode</dt>
          <dd>{{ cluster.mode === 'swarm' ? 'Docker Swarm' : 'Standalone (one plain-Docker node)' }}</dd>
          <dt>Manager node</dt>
          <dd>
            <router-link v-if="managerNode" :to="`/admin/nodes/${managerNode.id}`">{{ managerNode.display_name || managerNode.name }}</router-link>
            <span v-else>—</span>
          </dd>
          <dt>Gateway</dt>
          <dd>
            <router-link v-if="ingressNode" :to="`/admin/nodes/${ingressNode.id}`">{{ ingressNode.display_name || ingressNode.name }}</router-link>
            <span v-else-if="cluster.legacy_ingress">Central gateway, by host port</span>
            <span v-else>—</span>
          </dd>
        </dl>
        <div v-if="cluster.legacy_ingress" class="pending-hint">
          <span class="mdi mdi-alert-outline"></span>
          <span>
            This cluster was converted from a port-forward node. Give the node its own gateway, or join it to the
            default cluster as a worker: port-forward connectivity is being retired.
          </span>
        </div>
      </div>

      <div v-if="status" class="card cluster-bar">
        <div class="cluster-bar-main">
          <span class="mdi" :class="swarmEnabled ? 'mdi-lan-connect' : 'mdi-lan-disconnect'" style="font-size: 22px"></span>
          <div>
            <div class="cluster-bar-title">
              Docker Swarm
              <span class="badge" :class="swarmEnabled ? 'badge-success' : 'badge-muted'">{{ swarmEnabled ? 'enabled' : 'disabled' }}</span>
            </div>
            <div class="cell-sub">
              <template v-if="swarmEnabled">{{ status.managers }} manager(s), {{ status.nodes }} node(s)<span v-if="status.manager_addr"> · advertises {{ status.manager_addr }}</span></template>
              <template v-else>Run apps across nodes on a private overlay network. Single-node on plain Docker is unaffected.</template>
              <span v-if="status.error" class="badge badge-danger" style="margin-left: 8px">{{ status.error }}</span>
            </div>

            <div v-if="swarmEnabled && status.ingress_network" class="ingress-hint">
              <span class="mdi mdi-information-outline"></span>
              Running your own reverse proxy? Attach it to the ingress overlay
              <code>{{ status.ingress_network }}</code>:
              <code class="ingress-cmd">{{ ingressAttachCmd }}</code>
              <button type="button" class="btn btn-ghost btn-sm" title="Copy command" @click="copy(ingressAttachCmd)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>

            <div v-if="swarmEnabled" class="agents">
              <template v-if="agentsDeployed">
                <span class="badge badge-success badge-dot">nodes managed</span>
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
                <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="openDeployAgents">Redeploy</button>
                <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="showRemoveAgents = true">Remove agents</button>
              </template>
              <template v-else>
                <span class="badge badge-warning">nodes unmanaged</span>
                <span class="cell-sub">
                  Cluster nodes run tasks, but Miabi has no Docker connection to them: apps scheduled
                  there show <strong>no metrics, stats or shell</strong>, and their disks are unwatched.
                </span>
                <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="openDeployAgents">
                  <span class="mdi mdi-download-network-outline"></span> Manage cluster nodes
                </button>
              </template>
            </div>

            <div v-if="swarmEnabled" class="netcheck">
              <div class="netcheck-head">
                <button type="button" class="btn btn-sm btn-secondary" :disabled="netChecking" @click="runNetCheck">
                  <span class="mdi mdi-lan-pending"></span>
                  {{ netChecking ? 'Probing every path…' : 'Run network check' }}
                </button>
                <span class="cell-sub">Probes DNS, a TCP connection and a 1400-byte payload between every pair of nodes.</span>
              </div>
              <template v-if="netCheck">
                <div class="netcheck-summary" :class="netCheck.ok ? 'ok' : 'bad'">
                  <span class="mdi" :class="netCheck.ok ? 'mdi-check-circle-outline' : 'mdi-alert-circle-outline'"></span>
                  {{ netCheck.summary }}
                </div>
                <div v-if="netCheck.results.length" class="table-wrapper" style="margin-top: 8px">
                  <table>
                    <thead><tr><th>From → To</th><th>DNS</th><th>TCP</th><th>1400 B</th><th>Result</th></tr></thead>
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
                <div v-for="p in netCheck.probes.filter((x) => !x.reachable)" :key="p.server_id" class="cell-sub" style="margin-top: 6px">
                  <span class="mdi mdi-alert-outline"></span>
                  <strong>{{ p.node_name }}</strong> could not be probed — {{ p.error }}
                </div>
              </template>
            </div>

            <div v-if="swarmEnabled && networksPending > 0" class="pending-hint">
              <span class="mdi mdi-alert-outline"></span>
              <span>
                <strong>{{ networksPending }} workspace network(s) are still node-local bridges.</strong>
                Apps and databases in them can't reach each other across nodes — an app on one node
                won't resolve a database on another. Convert them to cluster overlays to fix it.
              </span>
              <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="showApplyNetworking = true">
                Apply cluster networking
              </button>
            </div>
          </div>
        </div>
        <div>
          <button v-if="!swarmEnabled" class="btn btn-secondary" :disabled="busy" @click="openEnable">Enable Swarm</button>
          <button v-else class="btn btn-secondary" :disabled="busy" @click="showDisable = true">Disable Swarm</button>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h3>Nodes</h3></div>
        <div v-if="members.length === 0" class="card-body cell-sub">No nodes in this cluster.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Name</th><th>Status</th><th v-if="swarmEnabled">Swarm</th></tr></thead>
            <tbody>
              <tr v-for="n in members" :key="n.id" class="row-clickable" @click="router.push(`/admin/nodes/${n.id}`)">
                <td>
                  <span class="cell-title">{{ n.display_name || n.name }}</span>
                  <div class="cell-sub">{{ n.address || (n.is_local ? 'local socket' : '—') }}</div>
                </td>
                <td><span class="badge" :class="statusClass(n)">{{ statusLabel(n) }}</span></td>
                <td v-if="swarmEnabled">
                  <span class="badge" :class="swarmClass(n)">{{ swarmLabel(n) }}</span>
                  <button v-if="n.in_swarm && !n.is_local" class="btn btn-xs btn-secondary" style="margin-left: 6px" :disabled="busy" @click.stop="pendingLeave = n">Leave</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <AppModal v-if="showEdit" @close="showEdit = false">
        <div class="modal-header">
          <h3>Edit cluster</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEdit = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveEdit">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Location name</label>
              <input v-model="editForm.display_name" class="form-input" maxlength="40" placeholder="e.g. Frankfurt" autofocus />
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Location code</label>
              <input v-model="editForm.location_code" class="form-input mono" maxlength="32" placeholder="e.g. eu-central" />
              <p class="form-hint">Lowercase letters, digits and hyphens. Two clusters may share a code.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEdit = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="showJoin" @close="showJoin = false">
        <div class="modal-header">
          <h3>Join nodes to the cluster</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showJoin = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p v-if="joinCandidates.length === 0" class="cell-sub">All managed nodes are already in the cluster.</p>
          <template v-else>
            <p class="cell-sub" style="margin-bottom: 12px">
              Select the nodes to join the swarm. Offline nodes can't be joined from here until their agent reconnects — use the manual command below.
            </p>
            <label
              v-for="n in joinCandidates"
              :key="n.id"
              class="join-row"
              :class="{ 'join-row--disabled': !n.agent_connected }"
            >
              <input type="checkbox" :disabled="!n.agent_connected" v-model="joinSelected[n.id]" />
              <span class="join-row-name">{{ n.display_name || n.name }}</span>
              <span class="badge" :class="n.agent_connected ? 'badge-success badge-dot' : 'badge-danger'">{{ n.agent_connected ? 'online · standalone' : 'offline' }}</span>
            </label>
          </template>
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
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="showEnable" @close="showEnable = false">
        <div class="modal-header">
          <h3>Enable Docker Swarm</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEnable = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="enableSwarm">
          <div class="modal-body">
            <p class="cell-sub" style="margin-bottom: 12px">
              The manager initializes a Docker Swarm. Member nodes can then be joined to a private overlay network.
              If Docker is already in swarm mode, Miabi adopts it instead.
            </p>
            <p v-if="!isDefault" class="form-hint" style="margin-bottom: 12px">
              The node must have no apps, databases or volumes yet: this cluster's workspace networks are
              created as overlays, and nodes join or leave it only when empty.
            </p>
            <!-- A VM-backed engine (Docker Desktop, OrbStack) forms a swarm, then drops every cross-node packet. -->
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
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Advertise address</label>
              <input v-model="advertiseAddr" class="form-input mono" placeholder="e.g. 10.0.0.1" autofocus />
              <p class="form-hint">The address swarm peers reach this manager on — use a private/WG address reachable from your nodes.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEnable = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="busy || !advertiseAddr.trim()">{{ busy ? 'Enabling…' : 'Enable Swarm' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Deploying the agents grants Miabi the Docker socket — root-equivalent — on every machine in the swarm. -->
    <Teleport to="body">
      <AppModal v-if="showDeployAgents" @close="showDeployAgents = false">
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

              <p v-if="agentTls === 'verify'" class="form-hint">
                If your control plane uses a self-signed or private-CA certificate, the agents will
                fail with <code>certificate signed by unknown authority</code> and never connect —
                choose “Trust a certificate authority” instead.
              </p>

              <template v-else-if="agentTls === 'ca'">
                <p class="form-hint">
                  The agents still <strong>verify</strong> — just against this authority instead of
                  the public ones. A forged certificate is still rejected.
                </p>
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

                  <div v-if="cpCert" class="ca-cert">
                    <div v-if="cpCert.publicly_trusted" class="form-hint">
                      <span class="mdi mdi-check-circle-outline"></span>
                      This certificate is <strong>already publicly trusted</strong> — you can simply
                      choose “Verify the certificate”; no CA needs distributing.
                    </div>
                    <!-- Trusting a CA does not skip the hostname check, so a cert without the dial host still fails. -->
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

              <p v-else class="form-hint form-hint-warn">
                <span class="mdi mdi-alert-outline"></span>
                <strong>Not recommended in production.</strong>
                The agents will accept <strong>any</strong> certificate for
                <code>{{ status?.manager_addr || 'the control plane' }}</code>, so anyone able to
                intercept their connections could impersonate it — and the control plane drives
                Docker on every node. Prefer “Trust a certificate authority”, which still verifies.
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showDeployAgents = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="busy">{{ busy ? 'Deploying…' : 'Deploy agents' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="showRemoveAgents"
      title="Remove cluster agents?"
      message="The agent is removed from every cluster node. They keep running their tasks — Swarm schedules those itself — but Miabi loses its Docker connection to them.

Apps scheduled on those nodes will stop showing metrics, stats and a shell. The node records themselves are kept."
      confirm-label="Remove agents"
      variant="danger"
      :busy="busy"
      @confirm="removeAgents"
      @cancel="showRemoveAgents = false"
    />

    <ConfirmDialog
      :open="showApplyNetworking"
      title="Apply cluster networking?"
      message="Each workspace network is converted from a node-local bridge to a cluster overlay, so apps and databases reach each other across nodes. Containers are NOT restarted, but connections open inside a workspace drop briefly while it switches over."
      confirm-label="Apply"
      :busy="busy"
      @confirm="applyNetworking"
      @cancel="showApplyNetworking = false"
    />

    <ConfirmDialog
      :open="showDisable"
      title="Disable Docker Swarm?"
      :message="isDefault
        ? 'The manager and all member nodes will leave the swarm. Workspace networks are moved back to node-local bridges first, so apps and databases stop being reachable across nodes — anything relying on that will break. Containers are not restarted.'
        : 'Every node leaves the swarm and becomes a standalone cluster of its own. The cluster must have no apps, databases or volumes left.'"
      confirm-label="Disable Swarm"
      variant="danger"
      :busy="busy"
      @confirm="disableSwarm"
      @cancel="showDisable = false"
    />

    <ConfirmDialog
      :open="!!pendingLeave"
      title="Remove node from the swarm?"
      :message="`Remove ${pendingLeave?.display_name || pendingLeave?.name} from the swarm? It becomes a standalone cluster of its own.`"
      confirm-label="Remove"
      variant="danger"
      :busy="busy"
      @confirm="leaveNode"
      @cancel="pendingLeave = null"
    />
  </div>
</template>

<style scoped>
.back-link { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); font-size: 13px; text-decoration: none; margin-bottom: 4px; }
.back-link:hover { color: var(--text); }
.page-header h1 { display: flex; align-items: center; gap: 8px; }
.handle { font-size: 12px; font-weight: 400; color: var(--text-muted); }
.header-actions { display: inline-flex; align-items: center; gap: 8px; }
.mono { font-family: var(--font-mono, monospace); }
.overview { padding: 14px 18px; margin-bottom: 16px; }
.overview dl { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; font-size: 13px; }
.overview dt { color: var(--text-muted); }
.overview dd { margin: 0; display: flex; align-items: center; gap: 8px; }
.cluster-bar { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 18px; margin-bottom: 16px; }
.cluster-bar-main { display: flex; align-items: flex-start; gap: 14px; }
.cluster-bar-title { display: inline-flex; align-items: center; gap: 8px; font-weight: 600; }
.ingress-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 8px; font-size: 12px; color: var(--text-muted); }
.ingress-cmd { font-family: var(--font-mono, monospace); user-select: all; }
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
.pending-hint > span:first-child { color: var(--warning, #b45309); font-size: 16px; }
.pf-finding { border: 1px solid; border-radius: 6px; padding: 8px 10px; margin-bottom: 10px; font-size: 12px; }
.pf-blocker { border-color: var(--danger-border, #f0a6a6); background: var(--danger-bg, rgba(220, 90, 90, 0.1)); }
.pf-warning { border-color: var(--warning-border, #f5c26b); background: var(--warning-bg, rgba(245, 194, 107, 0.12)); }
.pf-title { display: flex; align-items: center; gap: 6px; font-weight: 600; }
.pf-detail { margin: 4px 0 0; color: var(--text-muted); }
.pf-ports { margin-bottom: 12px; font-size: 12px; }
.pf-ports summary { cursor: pointer; color: var(--text-muted); }
.pf-table td { padding: 4px 8px 4px 0; vertical-align: top; }
.ca-mode { display: flex; gap: 14px; margin: 8px 0; font-size: 12px; }
.ca-mode label { display: inline-flex; align-items: center; gap: 4px; cursor: pointer; }
.ca-actions { margin: 8px 0; }
.ca-cert { margin: 8px 0; padding: 8px 10px; border: 1px solid var(--border); border-radius: 6px; font-size: 12px; }
.ca-cert dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 10px; margin: 6px 0; }
.ca-cert dt { color: var(--text-muted); }
.ca-cert dd { margin: 0; overflow-wrap: anywhere; }
.ca-pem { font-family: var(--font-mono, monospace); font-size: 11px; margin-top: 8px; }
.form-hint-warn { color: var(--warning, #b45309); }
.agents { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 10px; font-size: 12px; }
.netcheck { margin-top: 10px; }
.netcheck-head { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; font-size: 12px; }
.netcheck-summary { display: flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 12px; font-weight: 600; }
.netcheck-summary.ok { color: var(--success, #15803d); }
.netcheck-summary.bad { color: var(--danger, #b91c1c); }
.text-ok { color: var(--success, #15803d); }
.text-bad { color: var(--danger, #b91c1c); }
.join-row { display: flex; align-items: center; gap: 10px; padding: 8px 0; cursor: pointer; }
.join-row--disabled { opacity: 0.55; cursor: not-allowed; }
.join-row-name { flex: 1; font-weight: 500; }
.manual-join { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--border); }
.manual-join-title { font-weight: 600; margin-bottom: 6px; }
</style>
