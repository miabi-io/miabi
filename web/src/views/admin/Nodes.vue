<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { apiError as decodeApiError } from '@/api/client'
import { nodesApi, type CreateNodePayload } from '@/api/nodes'
import { clustersApi } from '@/api/clusters'
import { adminApi } from '@/api/admin'
import { ACCESS_MODES, CONNECTIVITY_TYPES, nodeOptionDescription } from '@/constants/node'
import FieldInfo from '@/components/FieldInfo.vue'
import { copyText } from '@/utils/clipboard'
import type { Cluster, Server, ServerConnectivity } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

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

const clusters = ref<Cluster[]>([])
const clusterFilter = ref<number | 'all'>('all')
const visibleNodes = computed(() =>
  clusterFilter.value === 'all' ? nodes.value : nodes.value.filter((n) => n.cluster_id === clusterFilter.value),
)
const anyInSwarm = computed(() => nodes.value.some((n) => n.in_swarm))

function clusterLabel(clusterID?: number): string {
  const c = clusters.value.find((x) => x.id === clusterID)
  return c ? c.display_name || c.name : '—'
}
function clusterNodeCount(clusterID: number): number {
  return nodes.value.filter((n) => n.cluster_id === clusterID).length
}

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

async function loadClusters() {
  try {
    clusters.value = (await clustersApi.list()).data.data ?? []
  } catch { /* the Cluster column falls back to a dash */ }
}

async function loadAgentImage() {
  try {
    const cfg = (await adminApi.getDeploymentConfig()).data.data
    const agent = cfg.images?.find((i) => i.key === 'agent')
    if (agent?.effective) agentImage.value = agent.effective
  } catch { /* fall back to the default ref */ }
}
onMounted(() => { load(); loadClusters(); loadAgentImage(); license.load() })

// --- Add node ---
const showCreate = ref(false)
const creating = ref(false)
const blankForm = (): CreateNodePayload => ({
  display_name: '', address: '', connectivity: 'edge-gateway', access_mode: 'agent',
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
  if (!form.value.display_name.trim()) return
  const mode = form.value.access_mode || 'agent'
  if (mode === 'api' && !form.value.docker_endpoint?.trim()) {
    notify.error('A Docker endpoint is required for this access mode')
    return
  }
  creating.value = true
  const payload: CreateNodePayload = {
    display_name: form.value.display_name.trim(),
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
    loadClusters()
    license.load(true) // refresh node usage so the cap chip stays accurate
    if (mode === 'agent') {
      createdToken.value = res.data.data.token
      await loadAgentCommand(res.data.data.node.id, res.data.data.token)
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

// The join command is built by the server, so it carries the configured
// MIABI_CONTROL_URL — the address the node must dial back on, which is not
// necessarily the address this browser reached the panel at. Building it here
// from window.location.origin enrolled agents against whatever host the admin
// happened to open (an internal IP, a port-forward, a proxy that is not the
// control plane), and the node then silently failed to connect.
const agentCommand = ref('')

// localAgentCommand is the fallback used only when the server's command cannot
// be fetched. The token is shown exactly once, so an empty dialog would lose it
// — a command with a best-guess URL is recoverable, a missing one is not.
function localAgentCommand(token: string): string {
  return (
    `docker run -d --name miabi-agent --restart unless-stopped \\\n` +
    `  -e MIABI_CONTROL_URL=${window.location.origin} \\\n` +
    `  -e MIABI_NODE_TOKEN=${token} \\\n` +
    `  -v /var/run/docker.sock:/var/run/docker.sock \\\n` +
    `  ${agentImage.value}`
  )
}

// The server returns the command with a <JOIN_TOKEN> placeholder (the token is
// never in a GET response); the real one is spliced in here so it stays
// copy-paste ready.
async function loadAgentCommand(nodeId: number, token: string) {
  try {
    const jc = (await nodesApi.joinCommand(nodeId)).data.data
    agentCommand.value = jc.command.replace('<JOIN_TOKEN>', token)
  } catch {
    agentCommand.value = localAgentCommand(token)
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}

function connectivityLabel(c?: ServerConnectivity): string {
  return c === 'edge-gateway' ? 'Edge gateway' : 'Cluster gateway'
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
      <h1>Nodes</h1>
      <div class="header-actions">
        <span v-if="limited" class="node-usage" :class="{ 'node-usage--full': atNodeLimit }" :title="atNodeLimit ? 'Node limit reached — upgrade to add more' : 'Nodes used of your edition limit'">
          <span class="mdi mdi-server"></span> {{ nodeCount }} / {{ nodeLimit }} nodes
        </span>
        <button class="btn btn-primary" @click="openCreate"><span class="mdi mdi-plus"></span> Add node</button>
      </div>
    </div>

    <div class="card">
      <div v-if="clusters.length > 1" class="cluster-filter">
        <button type="button" :class="{ active: clusterFilter === 'all' }" @click="clusterFilter = 'all'">
          All <span class="count">{{ nodes.length }}</span>
        </button>
        <button
          v-for="c in clusters"
          :key="c.id"
          type="button"
          :class="{ active: clusterFilter === c.id }"
          @click="clusterFilter = c.id"
        >
          {{ c.display_name || c.name }} <span class="count">{{ clusterNodeCount(c.id) }}</span>
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
          <thead><tr><th>Name</th><th>Cluster</th><th>Role</th><th>Access</th><th>Connectivity</th><th>Status</th><th v-if="anyInSwarm">Swarm</th><th>Agent</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="n in visibleNodes" :key="n.id" class="row-clickable" @click="router.push(`/admin/nodes/${n.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-server" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">
                      {{ n.display_name || n.name }}
                      <span v-if="n.cordoned" class="badge badge-warning" style="margin-left: 8px">cordoned</span>
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
              <td>
                <router-link v-if="n.cluster_id" :to="`/admin/clusters/${n.cluster_id}`" @click.stop>{{ clusterLabel(n.cluster_id) }}</router-link>
                <span v-else class="cell-sub">—</span>
              </td>
              <td><span class="badge" :class="roleLabel(n) === 'manager' ? 'badge-info' : 'badge-muted'">{{ roleLabel(n) }}</span></td>
              <td>
                <span class="badge badge-muted">{{ accessLabel(n.access_mode) }}</span>
                <span v-if="n.access_mode === 'api' && n.tls_enabled" class="mdi mdi-lock-outline" title="TLS" style="margin-left: 4px"></span>
              </td>
              <td><span class="badge badge-muted">{{ connectivityLabel(n.connectivity) }}</span></td>
              <td><span class="badge" :class="statusClass(n)">{{ statusLabel(n) }}</span></td>
              <td v-if="anyInSwarm"><span class="badge" :class="swarmClass(n)">{{ swarmLabel(n) }}</span></td>
              <td class="cell-sub">{{ agentLabel(n) }}</td>
              <td class="cell-sub">{{ fmtDate(n.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Add node / token reveal -->
    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
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
                <label class="form-label">Display name</label>
                <input v-model="form.display_name" class="form-input" placeholder="e.g. Frankfurt Edge" required autofocus />
                <p class="form-hint">The node gets a standalone cluster of its own, which you can rename on the Clusters page.</p>
              </div>
              <div class="form-group">
                <span class="form-label label-row">
                  Access mode
                  <FieldInfo :items="ACCESS_MODES" title="Access modes explained" />
                </span>
                <select v-model="form.access_mode" class="form-select">
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
                <label class="form-label">Address <span class="cell-sub">(the node's private host or IP)</span></label>
                <input v-model="form.address" class="form-input" placeholder="e.g. 10.0.0.7" />
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <span class="form-label label-row">
                  Connectivity: Edge gateway
                  <FieldInfo :items="CONNECTIVITY_TYPES" title="Connectivity types explained" placement="top" />
                </span>
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
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.label-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.cluster-filter {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 10px 14px 0;
  font-size: 12px;
}
.cluster-filter button {
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
.cluster-filter button.active {
  border-color: var(--border);
  background: var(--bg-subtle, rgba(127, 127, 127, 0.1));
  color: var(--text);
}
.cluster-filter .count {
  font-variant-numeric: tabular-nums;
  opacity: 0.7;
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
</style>
