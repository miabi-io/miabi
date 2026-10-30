<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import DedicatedBadge from '@/components/DedicatedBadge.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { clustersApi, type ClusterUpdate, type DedicationImpact } from '@/api/clusters'
import { clusterApi } from '@/api/cluster'
import { nodesApi } from '@/api/nodes'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'
import OrganizationPicker from '@/components/OrganizationPicker.vue'
import { copyText } from '@/utils/clipboard'
import { fmtSize } from '@/utils/format'
import { addressRecords, isDnsName, isIPv4, isPublicIPv6, normalizeHost, type PublicAddress } from '@/utils/publicAddress'
import type {
  AddressRecord, Cluster, Server, ClusterStatus, ClusterJoinInstructions,
  ClusterPreflight, NetCheck, NetCheckResult, ControlPlaneCert, ExternalCertificateOption,
} from '@/api/types'

const notify = useNotificationStore()
const { t } = useI18n()
const license = useLicenseStore()
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
  const ingressID = cluster.value?.ingress_server_id || (cluster.value?.mode === 'swarm' ? cluster.value?.manager_server_id : 0)
  return ingressID ? nodes.value.find((n) => n.id === ingressID) : undefined
})
const onlineMembers = computed(() => members.value.filter((n) => n.is_local || n.agent_connected).length)

type Tab = 'overview' | 'networking' | 'swarm' | 'nodes'
const tabs: Tab[] = ['overview', 'networking', 'swarm', 'nodes']
const tab = ref<Tab>(tabs.includes(route.query.tab as Tab) ? (route.query.tab as Tab) : 'overview')
watch(tab, (v) => { void router.replace({ query: { ...route.query, tab: v === 'overview' ? undefined : v } }) })

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

const saving = ref(false)
async function patch(body: ClusterUpdate, done: string): Promise<boolean> {
  if (!cluster.value) return false
  saving.value = true
  try {
    cluster.value = (await clustersApi.update(cluster.value.id, body)).data.data
    notify.success(done)
    return true
  } catch (e) {
    notify.apiError(e, 'Failed to update the cluster')
    return false
  } finally {
    saving.value = false
  }
}

const capacity = computed(() => cluster.value?.capacity)
const cpuUse = computed(() => (capacity.value?.nodes_with_usage ? Math.round(capacity.value.cpu_percent) : null))
const memUse = computed(() => {
  const c = capacity.value
  if (!c?.nodes_with_usage || !c.memory_bytes) return null
  return Math.round((c.mem_used_bytes / c.memory_bytes) * 100)
})
function meterClass(pct: number | null): string {
  if (pct === null) return ''
  return pct >= 90 ? 'meter-bad' : pct >= 75 ? 'meter-warn' : ''
}

// --- General settings ---
const showGeneral = ref(false)
const generalForm = ref({
  display_name: '', location_code: '', visibility: 'all' as Cluster['visibility'], cordoned: false,
  service_endpoint_mode: 'vip' as NonNullable<Cluster['service_endpoint_mode']>,
})
function openGeneral() {
  const c = cluster.value
  generalForm.value = {
    display_name: c?.display_name ?? '',
    location_code: c?.location_code ?? '',
    visibility: c?.visibility ?? 'all',
    cordoned: c?.cordoned ?? false,
    service_endpoint_mode: c?.service_endpoint_mode ?? 'vip',
  }
  showGeneral.value = true
}
async function saveGeneral() {
  const f = generalForm.value
  const body: ClusterUpdate = {
    display_name: f.display_name.trim(),
    location_code: f.location_code.trim(),
    cordoned: f.cordoned,
    service_endpoint_mode: f.service_endpoint_mode,
  }
  if (!cluster.value?.dedicated) body.visibility = f.visibility
  if (await patch(body, 'Cluster updated')) showGeneral.value = false
}

// --- Public address ---
const address = computed<PublicAddress>(() => ({
  ipv4: cluster.value?.ingress_ip || '',
  ipv6: cluster.value?.ingress_ipv6 || '',
  hostname: cluster.value?.ingress_hostname || '',
}))
const hasAddress = computed(() => !!(address.value.ipv4 || address.value.ipv6 || address.value.hostname))
const hasIP = computed(() => !!(address.value.ipv4 || address.value.ipv6))
const stack = computed(() => {
  const a = address.value
  if (a.ipv4 && a.ipv6) return { label: 'Dual stack', icon: 'mdi-check-decagram-outline', cls: 'chip-ok' }
  if (a.ipv4) return { label: 'IPv4 only', icon: 'mdi-numeric-4-circle-outline', cls: '' }
  if (a.ipv6) return { label: 'IPv6 only', icon: 'mdi-numeric-6-circle-outline', cls: '' }
  if (a.hostname) return { label: 'CNAME target', icon: 'mdi-link-variant', cls: '' }
  return { label: 'No public address', icon: 'mdi-alert-outline', cls: 'chip-warn' }
})

const showAddress = ref(false)
const addressForm = ref({ ipv4: '', ipv6: '', hostname: '' })
function openAddress() {
  addressForm.value = { ipv4: address.value.ipv4 ?? '', ipv6: address.value.ipv6 ?? '', hostname: address.value.hostname ?? '' }
  showAddress.value = true
}
const addressErrors = computed(() => {
  const f = addressForm.value
  return {
    ipv4: f.ipv4.trim() && !isIPv4(f.ipv4) ? 'Not an IPv4 address, e.g. 203.0.113.10.' : '',
    ipv6: f.ipv6.trim() && !isPublicIPv6(f.ipv6)
      ? 'Not a public IPv6 address. Link-local (fe80::) and unique-local (fc00::/7) addresses cannot be reached from the internet.'
      : '',
    hostname: f.hostname.trim() && !isDnsName(f.hostname) ? 'A DNS name such as lb.eu-central.example.com, without a scheme or path.' : '',
  }
})
const addressValid = computed(() => !Object.values(addressErrors.value).some(Boolean))
const draftAddress = computed<PublicAddress>(() => {
  const e = addressErrors.value
  const f = addressForm.value
  return {
    ipv4: e.ipv4 ? '' : f.ipv4.trim(),
    ipv6: e.ipv6 ? '' : f.ipv6.trim().replace(/^\[|\]$/g, '').toLowerCase(),
    hostname: e.hostname ? '' : normalizeHost(f.hostname),
  }
})
async function saveAddress() {
  if (!addressValid.value) return
  const f = addressForm.value
  const ok = await patch({
    ingress_ip: f.ipv4.trim(),
    ingress_ipv6: f.ipv6.trim(),
    ingress_hostname: f.hostname.trim(),
  }, 'Public address updated: DNS records follow in the background')
  if (ok) showAddress.value = false
}

// What Miabi writes for a typical custom domain, so the effect of an address is visible before saving it.
const sampleHosts = ['example.com', 'www.example.com', 'app.example.com']
function sampleRecords(a: PublicAddress): AddressRecord[] {
  return sampleHosts.flatMap((h) => addressRecords(h, sampleHosts, a, h === 'example.com'))
}
const records = computed(() => sampleRecords(address.value))
const draftRecords = computed(() => sampleRecords(draftAddress.value))
const wildcardRecords = computed(() => {
  const d = cluster.value?.external_base_domain
  return d ? addressRecords(`x.${d}`, [], address.value).map((r) => ({ ...r, name: `*.${d}` })) : []
})
const ipv6Check = computed(() => (address.value.ipv6 ? `curl -6 -sI http://[${address.value.ipv6}]/` : ''))

// --- External access ---
const showExternal = ref(false)
const externalForm = ref({ external_base_domain: '', external_cert_provider: '', external_certificate_id: 0 })
function openExternal() {
  const c = cluster.value
  externalForm.value = {
    external_base_domain: c?.external_base_domain ?? '',
    external_cert_provider: c?.external_cert_provider ?? '',
    external_certificate_id: c?.external_certificate_id ?? 0,
  }
  showExternal.value = true
  void loadCertOptions()
}

const certOptions = ref<ExternalCertificateOption[]>([])
async function loadCertOptions() {
  if (!cluster.value) return
  try {
    certOptions.value = (await clustersApi.externalCertificates(cluster.value.id)).data.data ?? []
  } catch {
    certOptions.value = []
  }
}
// Re-checked against the domain being typed, since the server's verdict is for the saved one.
function certProblem(o: ExternalCertificateOption): string {
  if (!editDomain.value) return 'set the external domain first'
  if (o.status !== 'active' || new Date(o.not_after) <= new Date()) return 'not active or expired'
  if (!o.dns_names.some((n) => n.toLowerCase() === `*.${editDomain.value}`)) return `does not cover *.${editDomain.value}`
  return ''
}

function normalizeDomain(domain: string) {
  return domain.trim().toLowerCase().replace(/^\*\./, '').replace(/^\.|\.$/g, '')
}
const editDomain = computed(() => normalizeDomain(externalForm.value.external_base_domain))
// Moving or clearing the domain changes URLs people already use, so it is confirmed with the number of apps affected.
const confirmRehost = ref(false)
const rehostMessage = computed(() => {
  const n = cluster.value?.external_apps ?? 0
  const apps = n === 1 ? '1 app has' : `${n} apps have`
  const from = `*.${cluster.value?.external_base_domain}`
  return editDomain.value
    ? `${apps} generated URLs under ${from}. They move to *.${editDomain.value}, and the old URLs stop working. Custom domains are not affected.`
    : `${apps} generated URLs under ${from}. They are removed, and those apps stop answering on them. Custom domains are not affected.`
})
function saveExternal() {
  const c = cluster.value
  if (!c) return
  if ((c.external_apps ?? 0) > 0 && editDomain.value !== (c.external_base_domain ?? '')) {
    confirmRehost.value = true
    return
  }
  void persistExternal()
}
async function persistExternal() {
  const c = cluster.value
  if (!c) return
  confirmRehost.value = false
  const f = externalForm.value
  const body: ClusterUpdate = {}
  if (!c.external_domain_pinned) body.external_base_domain = f.external_base_domain.trim()
  if (!c.external_provider_pinned) body.external_cert_provider = f.external_cert_provider.trim()
  if (f.external_certificate_id !== (c.external_certificate_id ?? 0)) body.external_certificate_id = f.external_certificate_id
  if (await patch(body, 'External access updated')) showExternal.value = false
}

// --- Dedication (impactful: it decides who may see and place here, and strands whatever the new
// owner does not own). Gated behind an explicit acknowledgement, the same way a node's connectivity is. ---
const eeOrgs = computed(() => license.has('organizations'))
const showDedication = ref(false)
const dedicationSaving = ref(false)
const dedicationAck = ref(false)
const dedicationImpact = ref<DedicationImpact | null>(null)
const dedicationTarget = ref(0)

const dedicatedTo = computed(() => cluster.value?.organization_name || '')
const strandedBy = computed(() => {
  const target = dedicationTarget.value
  return (dedicationImpact.value?.tenants ?? []).filter((t) => target === 0 || t.organization_id !== target)
})
const strandedCount = computed(() => strandedBy.value.reduce((n, t) => n + t.workloads, 0))
const dedicationChanged = computed(() => dedicationTarget.value !== (cluster.value?.organization_id ?? 0))

function openDedication() {
  if (!cluster.value) return
  dedicationAck.value = false
  dedicationTarget.value = cluster.value.organization_id ?? 0
  dedicationImpact.value = null
  clustersApi.dedication(cluster.value.id)
    .then((r) => { dedicationImpact.value = r.data.data })
    .catch(() => { dedicationImpact.value = null })
  showDedication.value = true
}

async function submitDedication() {
  if (!cluster.value || !dedicationAck.value || !dedicationChanged.value) return
  dedicationSaving.value = true
  try {
    cluster.value = (await clustersApi.update(cluster.value.id, {
      organization_id: dedicationTarget.value,
      acknowledge: true,
    })).data.data
    showDedication.value = false
    notify.success(dedicationTarget.value === 0 ? 'Location released to shared' : 'Location dedicated')
  } catch (e) {
    notify.apiError(e, 'Failed to change who this location belongs to')
  } finally {
    dedicationSaving.value = false
  }
}

// --- Gateway ---
const canSetGateway = computed(() => cluster.value?.mode === 'swarm' && !isDefault.value)
const gatewayCandidates = computed(() => members.value.filter((n) => n.connectivity === 'edge-gateway'))
const gatewayExplainer = computed(() => {
  if (isDefault.value) return "The control plane's gateway serves every app in this cluster, over the ingress overlay for swarm members."
  if (cluster.value?.mode === 'swarm') return "The gateway on this swarm's ingress node serves every app here, over the swarm's overlay."
  return "The node's own gateway serves its apps."
})
const showGateway = ref(false)
const gatewayForm = ref({ server_id: 0 })
function openGateway() {
  gatewayForm.value = { server_id: ingressNode.value?.id || gatewayCandidates.value[0]?.id || 0 }
  showGateway.value = true
}
async function saveGateway() {
  if (!cluster.value) return
  saving.value = true
  try {
    cluster.value = (await clustersApi.setGateway(cluster.value.id, { server_id: gatewayForm.value.server_id })).data.data
    showGateway.value = false
    notify.success('Gateway updated')
  } catch (e) {
    notify.apiError(e, 'Failed to update the gateway')
  } finally {
    saving.value = false
  }
}

const needsIngress = computed(() =>
  !!cluster.value && !isDefault.value && cluster.value.mode === 'standalone' &&
  (cluster.value.legacy_ingress || (!!managerNode.value && managerNode.value.connectivity !== 'edge-gateway')),
)
const swarmTargets = ref<Cluster[]>([])
const convertTarget = ref(0)
const converting = ref(false)
watch(needsIngress, async (needed) => {
  if (!needed) return
  try {
    swarmTargets.value = ((await clustersApi.list()).data.data ?? []).filter((c) => c.mode === 'swarm' && c.id !== cluster.value?.id)
    convertTarget.value = swarmTargets.value[0]?.id ?? 0
  } catch {
    swarmTargets.value = []
  }
})
async function convertIngress(action: 'gateway' | 'join') {
  if (!cluster.value) return
  converting.value = true
  try {
    await clustersApi.convertIngress(cluster.value.id, { action, target_cluster_id: action === 'join' ? convertTarget.value : undefined })
    if (action === 'join') {
      notify.success('Node joined the swarm cluster')
      router.push(`/admin/clusters/${convertTarget.value}`)
    } else {
      notify.success('Gateway confirmed')
      await load()
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    converting.value = false
  }
}

// --- Docker Swarm ---
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
  advertiseAddr.value = managerNode.value?.address || ''
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
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

function nodeName(n?: Server): string {
  return n ? n.display_name || n.name : ''
}
function statusClass(n: Server): string {
  return n.is_local || n.agent_connected ? 'badge-success badge-dot' : 'badge-danger'
}
function statusLabel(n: Server): string {
  if (n.is_local) return 'control plane'
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
        </h1>
      </div>
      <div v-if="cluster" class="header-actions">
        <button v-if="swarmEnabled" class="btn btn-secondary" @click="openJoin"><span class="mdi mdi-lan-connect"></span> Join nodes</button>
        <button class="btn btn-secondary" @click="openGeneral"><span class="mdi mdi-pencil-outline"></span> Edit</button>
      </div>
    </div>

    <div v-if="loading && !cluster" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="cluster">
      <div class="card hero">
        <div class="hero-main">
          <span class="hero-icon" :class="cluster.cordoned ? 'is-warn' : 'is-ok'">
            <span class="mdi" :class="cluster.mode === 'swarm' ? 'mdi-hexagon-multiple-outline' : 'mdi-server-outline'"></span>
          </span>
          <div class="hero-text">
            <div class="hero-title">
              {{ cluster.mode === 'swarm' ? 'Docker Swarm cluster' : 'Standalone node' }}
              <span v-if="cluster.location_code" class="badge badge-muted mono">{{ cluster.location_code }}</span>
            </div>
            <div class="hero-chips">
              <span v-if="isDefault" class="chip chip-info"><span class="mdi mdi-star-outline"></span> default · control plane</span>
              <DedicatedBadge v-if="cluster.dedicated" :dedicated="true" :organization="cluster.organization_name" />
              <span v-else class="chip"><span class="mdi mdi-account-group-outline"></span> shared</span>
              <span v-if="cluster.visibility === 'restricted'" class="chip"><span class="mdi mdi-shield-lock-outline"></span> admins only</span>
              <span v-if="cluster.cordoned" class="chip chip-warn"><span class="mdi mdi-cancel"></span> cordoned</span>
              <span v-else class="chip chip-ok"><span class="mdi mdi-check"></span> accepting placements</span>
              <button type="button" class="chip chip-btn" :class="stack.cls" title="Public address" @click="tab = 'networking'">
                <span class="mdi" :class="stack.icon"></span> {{ stack.label }}
              </button>
            </div>
          </div>
        </div>
        <div class="hero-stats">
          <div class="hero-stat">
            <span class="hero-stat-label">Nodes</span>
            <span class="hero-stat-value">{{ onlineMembers }}<small>/{{ members.length }} online</small></span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">CPU</span>
            <span class="hero-stat-value">{{ capacity?.nodes_measured ? capacity.cpu_cores : '—' }}<small v-if="capacity?.nodes_measured"> cores</small></span>
            <span v-if="cpuUse !== null" class="meter" :class="meterClass(cpuUse)"><span :style="{ width: `${Math.min(cpuUse, 100)}%` }"></span></span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">Memory</span>
            <span class="hero-stat-value">{{ capacity?.nodes_measured ? fmtSize(capacity.memory_bytes) : '—' }}</span>
            <span v-if="memUse !== null" class="meter" :class="meterClass(memUse)" :title="`${memUse}% used`"><span :style="{ width: `${Math.min(memUse, 100)}%` }"></span></span>
          </div>
          <div class="hero-stat">
            <span class="hero-stat-label">Storage</span>
            <span class="hero-stat-value">{{ capacity?.nodes_measured ? fmtSize(capacity.storage_bytes) : '—' }}</span>
            <span v-if="capacity?.nodes_measured" class="hero-stat-sub">{{ fmtSize(capacity.storage_free_bytes) }} free</span>
          </div>
        </div>
        <p v-if="capacity && capacity.nodes_measured < capacity.nodes" class="hero-foot cell-sub">
          Capacity covers {{ capacity.nodes_measured }} of {{ capacity.nodes }} nodes; the rest are not measured yet.
        </p>
      </div>

      <div v-if="needsIngress" class="app-banner app-banner--warning section-gap">
        <span class="mdi mdi-alert-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-title">{{ cluster.legacy_ingress ? 'This node now runs its own gateway' : 'Apps on this node are not publicly routed' }}</p>
          <p class="app-banner-text">
            <template v-if="cluster.legacy_ingress">
              Port forwarding is retired, so this node now runs its own gateway, which needs public ports 80 and 443.
              If it has none, join it to a swarm cluster, whose gateway then serves its apps.
            </template>
            <template v-else>This node runs no gateway of its own. Give it one, or join it to a swarm cluster.</template>
          </p>
          <div class="banner-actions">
            <button class="btn btn-secondary btn-sm" :disabled="converting" @click="convertIngress('gateway')">
              {{ cluster.legacy_ingress ? 'Keep its own gateway' : 'Give it a gateway' }}
            </button>
            <template v-if="swarmTargets.length">
              <select v-model.number="convertTarget" class="form-select form-select-sm" aria-label="Swarm cluster">
                <option v-for="t in swarmTargets" :key="t.id" :value="t.id">{{ t.display_name || t.name }}</option>
              </select>
              <button class="btn btn-secondary btn-sm" :disabled="converting || !convertTarget" @click="convertIngress('join')">Join as a worker</button>
            </template>
          </div>
        </div>
      </div>

      <div v-if="!hasAddress" class="app-banner app-banner--info section-gap">
        <span class="mdi mdi-information-outline app-banner-icon"></span>
        <div class="app-banner-content">
          <p class="app-banner-title">No public address</p>
          <p class="app-banner-text">
            Miabi writes no DNS records for apps in this location, and route pages cannot show the record to create.
            Set the IPv4 and IPv6 addresses of its gateway, or of a load balancer in front of it.
          </p>
        </div>
        <div class="app-banner-actions">
          <button class="btn btn-primary btn-sm" @click="openAddress">Set public address</button>
        </div>
      </div>

      <div class="tabs" role="tablist">
        <button class="tab" :class="{ active: tab === 'overview' }" role="tab" @click="tab = 'overview'">Overview</button>
        <button class="tab" :class="{ active: tab === 'networking' }" role="tab" @click="tab = 'networking'">
          Networking & DNS <span v-if="!hasAddress" class="tab-dot" title="No public address"></span>
        </button>
        <button class="tab" :class="{ active: tab === 'swarm' }" role="tab" @click="tab = 'swarm'">
          Docker Swarm <span v-if="swarmEnabled && (networksPending > 0 || !agentsDeployed)" class="tab-dot" title="Needs attention"></span>
        </button>
        <button class="tab" :class="{ active: tab === 'nodes' }" role="tab" @click="tab = 'nodes'">
          Nodes <span class="tab-count">{{ members.length }}</span>
        </button>
      </div>

      <!-- Overview -->
      <div v-if="tab === 'overview'" class="grid-2">
        <div class="card">
          <div class="card-header">
            <h3>General</h3>
            <button class="btn btn-ghost btn-sm" @click="openGeneral"><span class="mdi mdi-pencil-outline"></span> Edit</button>
          </div>
          <div class="card-body">
            <dl class="kv">
              <dt>Location</dt>
              <dd>
                {{ cluster.display_name || '—' }}
                <span v-if="cluster.location_code" class="badge badge-muted mono">{{ cluster.location_code }}</span>
              </dd>
              <dt>Mode</dt>
              <dd>{{ cluster.mode === 'swarm' ? 'Docker Swarm' : 'Standalone (one plain-Docker node)' }}</dd>
              <dt>Belongs to</dt>
              <dd>
                <DedicatedBadge v-if="cluster.dedicated" :dedicated="true" :organization="dedicatedTo" />
                <span v-else>Shared: any workspace may place here</span>
                <button v-if="eeOrgs" class="btn btn-ghost btn-xs" @click="openDedication">Change</button>
              </dd>
              <dt>Placement</dt>
              <dd>
                {{ cluster.dedicated ? `${dedicatedTo || 'Its organization'} only` : cluster.visibility === 'restricted' ? 'Platform admins only' : 'All workspaces' }}
                <span v-if="cluster.cordoned" class="badge badge-warning">cordoned</span>
              </dd>
              <dt>Manager node</dt>
              <dd>
                <router-link v-if="managerNode" :to="`/admin/nodes/${managerNode.id}`">{{ nodeName(managerNode) }}</router-link>
                <span v-else class="text-muted">—</span>
              </dd>
              <template v-if="cluster.mode === 'swarm'">
                <dt>Service load balancing</dt>
                <dd>{{ cluster.service_endpoint_mode === 'dnsrr' ? 'DNS round-robin' : 'Virtual IP' }}</dd>
              </template>
            </dl>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h3>External access</h3>
            <button class="btn btn-ghost btn-sm" @click="openExternal"><span class="mdi mdi-pencil-outline"></span> Edit</button>
          </div>
          <div class="card-body">
            <dl class="kv">
              <dt>Generated URLs</dt>
              <dd>
                <span v-if="cluster.external_base_domain" class="mono">*.{{ cluster.external_base_domain }}</span>
                <span v-else class="text-muted">Off: apps here get no one-click URL</span>
                <span v-if="cluster.external_domain_pinned" class="badge badge-muted">set by environment</span>
              </dd>
              <template v-if="cluster.external_base_domain">
                <dt>Apps using them</dt>
                <dd>{{ cluster.external_apps ?? 0 }}</dd>
                <dt>Certificate</dt>
                <dd>
                  <template v-if="cluster.external_certificate">
                    <span class="mono">{{ cluster.external_certificate.name || 'missing' }}</span>
                    <span v-if="cluster.external_certificate.usable" class="cell-sub">
                      until {{ new Date(cluster.external_certificate.not_after).toLocaleDateString() }}
                    </span>
                    <span v-else class="badge badge-warning" :title="cluster.external_certificate.problem">
                      falling back to one per app: {{ cluster.external_certificate.problem }}
                    </span>
                  </template>
                  <span v-else>One per app{{ cluster.external_cert_provider ? ` (${cluster.external_cert_provider})` : '' }}</span>
                </dd>
              </template>
            </dl>
            <p v-if="cluster.external_base_domain" class="form-hint">
              The wildcard records for <code>*.{{ cluster.external_base_domain }}</code> are listed under
              <a href="#" @click.prevent="tab = 'networking'">Networking & DNS</a>.
            </p>
          </div>
        </div>
      </div>

      <!-- Networking -->
      <template v-else-if="tab === 'networking'">
        <div class="card section-gap">
          <div class="card-header">
            <div>
              <h3>Public address</h3>
              <p class="card-sub">Where DNS records for this location's routes point: its gateway, or a load balancer in front of it.</p>
            </div>
            <button class="btn btn-secondary btn-sm" @click="openAddress"><span class="mdi mdi-pencil-outline"></span> Edit address</button>
          </div>
          <div class="addr-list">
            <div class="addr-row">
              <span class="addr-family">IPv4</span>
              <span v-if="address.ipv4" class="mono addr-value">{{ address.ipv4 }}</span>
              <span v-else class="text-muted addr-value">Not set</span>
              <span class="badge badge-neutral">A</span>
              <button v-if="address.ipv4" class="btn-icon btn-icon-muted" aria-label="Copy IPv4 address" title="Copy" @click="copy(address.ipv4)"><span class="mdi mdi-content-copy"></span></button>
              <span v-else class="addr-gap"></span>
            </div>
            <div class="addr-row">
              <span class="addr-family">IPv6</span>
              <span v-if="address.ipv6" class="mono addr-value">{{ address.ipv6 }}</span>
              <span v-else class="text-muted addr-value">Not set: hosts get no AAAA record, so IPv6-only visitors cannot connect</span>
              <span class="badge badge-neutral">AAAA</span>
              <button v-if="address.ipv6" class="btn-icon btn-icon-muted" aria-label="Copy IPv6 address" title="Copy" @click="copy(address.ipv6)"><span class="mdi mdi-content-copy"></span></button>
              <span v-else class="addr-gap"></span>
            </div>
            <div class="addr-row" :class="{ 'addr-row--unused': hasIP && address.hostname }">
              <span class="addr-family">Hostname</span>
              <span v-if="address.hostname" class="mono addr-value">
                {{ address.hostname }}
                <span v-if="hasIP" class="cell-sub">unused while an IP is set</span>
              </span>
              <span v-else class="text-muted addr-value">Not set</span>
              <span class="badge badge-neutral">CNAME</span>
              <button v-if="address.hostname" class="btn-icon btn-icon-muted" aria-label="Copy hostname" title="Copy" @click="copy(address.hostname)"><span class="mdi mdi-content-copy"></span></button>
              <span v-else class="addr-gap"></span>
            </div>
          </div>
          <div v-if="address.ipv6" class="card-body card-body--foot">
            <p class="form-hint" style="margin: 0">
              <span class="mdi mdi-information-outline"></span>
              The gateway must accept IPv6 connections on ports 80 and 443. Check from a host with IPv6:
              <code class="select-all">{{ ipv6Check }}</code>
              <button class="btn-icon btn-icon-muted" aria-label="Copy command" title="Copy" @click="copy(ipv6Check)"><span class="mdi mdi-content-copy"></span></button>
            </p>
          </div>
        </div>

        <div class="card section-gap">
          <div class="card-header">
            <div>
              <h3>DNS records</h3>
              <p class="card-sub">
                What a host routed here resolves through. Miabi writes these for domains connected to a DNS provider;
                create them by hand for any other.
              </p>
            </div>
          </div>
          <div class="card-body">
            <template v-if="wildcardRecords.length">
              <div class="rec-heading">Generated URLs <span class="cell-sub">create these once</span></div>
              <div class="rec-table">
                <div v-for="r in wildcardRecords" :key="r.type" class="rec-row">
                  <span class="badge badge-neutral rec-type">{{ r.type }}</span>
                  <span class="mono">{{ r.name }}</span>
                  <span class="mono rec-value">{{ r.value }}</span>
                  <button class="btn-icon btn-icon-muted" aria-label="Copy value" title="Copy value" @click="copy(r.value)"><span class="mdi mdi-content-copy"></span></button>
                </div>
              </div>
            </template>
            <div class="rec-heading">Custom domains <span class="cell-sub">e.g. an app on example.com and www.example.com</span></div>
            <div v-if="records.length" class="rec-table">
              <div v-for="r in records" :key="`${r.name}-${r.type}`" class="rec-row">
                <span class="badge badge-neutral rec-type">{{ r.type }}</span>
                <span class="mono">{{ r.name }}</span>
                <span class="mono rec-value">{{ r.value }}</span>
                <span v-if="r.type === 'CNAME' && r.name.startsWith('www.') && hasIP" class="rec-why" title="www follows its bare name, so the pair cannot drift apart">alias of @</span>
                <span v-else></span>
              </div>
            </div>
            <p v-else class="cell-sub">No records: set a public address first.</p>
            <ul class="rec-rules">
              <li>Each IP family gets its own record: <strong>A</strong> for IPv4, <strong>AAAA</strong> for IPv6.</li>
              <li><code>www</code> is a <strong>CNAME</strong> to its bare name when both are routed, instead of a second set of address records.</li>
              <li>Without an IP, hosts get a CNAME to the hostname; the zone apex cannot hold a CNAME, so it gets nothing.</li>
            </ul>
          </div>
        </div>

        <div class="card section-gap">
          <div class="card-header">
            <div>
              <h3>Gateway</h3>
              <p class="card-sub">{{ gatewayExplainer }}</p>
            </div>
            <button v-if="canSetGateway" class="btn btn-secondary btn-sm" @click="openGateway"><span class="mdi mdi-swap-horizontal"></span> Change</button>
          </div>
          <div class="card-body">
            <dl class="kv">
              <dt>Gateway node</dt>
              <dd>
                <router-link v-if="ingressNode" :to="`/admin/nodes/${ingressNode.id}`">{{ nodeName(ingressNode) }}</router-link>
                <span v-else class="text-muted">—</span>
                <span v-if="ingressNode" class="badge" :class="statusClass(ingressNode)">{{ statusLabel(ingressNode) }}</span>
              </dd>
              <template v-if="swarmEnabled && status?.ingress_network">
                <dt>Ingress overlay</dt>
                <dd><code>{{ status.ingress_network }}</code></dd>
              </template>
            </dl>
            <div v-if="swarmEnabled && status?.ingress_network" class="ingress-hint">
              <span class="mdi mdi-information-outline"></span>
              Running your own reverse proxy? Attach it to the ingress overlay:
              <code class="select-all">{{ ingressAttachCmd }}</code>
              <button type="button" class="btn-icon btn-icon-muted" aria-label="Copy command" title="Copy command" @click="copy(ingressAttachCmd)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
          </div>
        </div>
      </template>

      <!-- Docker Swarm -->
      <template v-else-if="tab === 'swarm'">
        <div v-if="!status" class="card"><div class="card-body cell-sub">Swarm status is unavailable: the manager node could not be reached.</div></div>
        <template v-else>
          <div class="card section-gap">
            <div class="card-header">
              <div class="swarm-head">
                <span class="mdi" :class="swarmEnabled ? 'mdi-lan-connect' : 'mdi-lan-disconnect'"></span>
                <div>
                  <h3>
                    Docker Swarm
                    <span class="badge" :class="swarmEnabled ? 'badge-success' : 'badge-muted'">{{ swarmEnabled ? 'enabled' : 'disabled' }}</span>
                  </h3>
                  <p class="card-sub">
                    <template v-if="swarmEnabled">{{ status.managers }} manager(s), {{ status.nodes }} node(s)<span v-if="status.manager_addr"> · advertises <code>{{ status.manager_addr }}</code></span></template>
                    <template v-else>Run apps across nodes on a private overlay network. Single-node on plain Docker is unaffected.</template>
                  </p>
                </div>
              </div>
              <button v-if="!swarmEnabled" class="btn btn-primary btn-sm" :disabled="busy" @click="openEnable">Enable Swarm</button>
              <button v-else class="btn btn-secondary btn-sm" :disabled="busy" @click="showDisable = true">Disable Swarm</button>
            </div>
            <div v-if="status.error" class="card-body"><span class="badge badge-danger">{{ status.error }}</span></div>
          </div>

          <div v-if="swarmEnabled && networksPending > 0" class="app-banner app-banner--warning section-gap">
            <span class="mdi mdi-alert-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">{{ networksPending }} workspace network(s) are still node-local bridges</p>
              <p class="app-banner-text">
                Apps and databases in them can't reach each other across nodes: an app on one node won't resolve a
                database on another. Convert them to cluster overlays to fix it.
              </p>
            </div>
            <div class="app-banner-actions">
              <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="showApplyNetworking = true">Apply cluster networking</button>
            </div>
          </div>

          <div v-if="swarmEnabled" class="grid-2">
            <div class="card">
              <div class="card-header">
                <h3>Node agents</h3>
                <span class="badge" :class="agentsDeployed ? 'badge-success badge-dot' : 'badge-warning'">{{ agentsDeployed ? 'nodes managed' : 'nodes unmanaged' }}</span>
              </div>
              <div class="card-body">
                <template v-if="agentsDeployed">
                  <p class="cell-sub" style="margin-top: 0">
                    The agent runs on every cluster node ({{ agentTasks }} task(s)), including nodes that join later, so
                    metrics, stats, shell and housekeeping work everywhere.
                  </p>
                  <div class="agent-flags">
                    <span v-if="agentInsecureTLS" class="badge badge-warning" title="The agents accept ANY certificate for the control plane. Switch to 'Trust a certificate authority' — it still verifies — and redeploy them.">TLS verification off</span>
                    <span v-else-if="agentCustomCA" class="badge badge-info" title="The agents verify the control plane against your own certificate authority.">custom CA</span>
                    <span v-else class="badge badge-success">TLS verified</span>
                  </div>
                  <div class="card-actions">
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="openDeployAgents">Redeploy</button>
                    <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="showRemoveAgents = true">Remove agents</button>
                  </div>
                </template>
                <template v-else>
                  <p class="cell-sub" style="margin-top: 0">
                    Cluster nodes run tasks, but Miabi has no Docker connection to them: apps scheduled there show
                    <strong>no metrics, stats or shell</strong>, and their disks are unwatched.
                  </p>
                  <div class="card-actions">
                    <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="openDeployAgents">
                      <span class="mdi mdi-download-network-outline"></span> Manage cluster nodes
                    </button>
                  </div>
                </template>
              </div>
            </div>

            <div class="card">
              <div class="card-header">
                <h3>Network check</h3>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="netChecking" @click="runNetCheck">
                  <span class="mdi mdi-lan-pending"></span> {{ netChecking ? 'Probing every path…' : 'Run check' }}
                </button>
              </div>
              <div class="card-body">
                <p class="cell-sub" style="margin-top: 0">Probes DNS, a TCP connection and a 1400-byte payload between every pair of nodes, and a service virtual IP.</p>
                <template v-if="netCheck">
                  <div class="netcheck-summary" :class="netCheck.ok ? 'ok' : 'bad'">
                    <span class="mdi" :class="netCheck.ok ? 'mdi-check-circle-outline' : 'mdi-alert-circle-outline'"></span>
                    {{ netCheck.summary }}
                  </div>
                  <div v-if="netCheck.vip" class="cell-sub" style="margin-top: 6px">
                    <span class="mdi" :class="netCheck.vip.payload ? 'mdi-check text-ok' : 'mdi-close text-bad'"></span>
                    Service virtual IP on <strong>{{ netCheck.vip.from }}</strong>:
                    {{ netCheck.vip.payload ? 'works' : netCheck.vip.verdict }}
                  </div>
                  <div v-for="p in netCheck.probes.filter((x) => !x.reachable)" :key="p.server_id" class="cell-sub" style="margin-top: 6px">
                    <span class="mdi mdi-alert-outline"></span>
                    <strong>{{ p.node_name }}</strong> could not be probed: {{ p.error }}
                  </div>
                </template>
              </div>
              <div v-if="netCheck?.results.length" class="table-wrapper">
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
            </div>
          </div>
        </template>
      </template>

      <!-- Nodes -->
      <div v-else class="card">
        <div class="card-header">
          <h3>Nodes</h3>
          <button v-if="swarmEnabled" class="btn btn-secondary btn-sm" @click="openJoin"><span class="mdi mdi-lan-connect"></span> Join nodes</button>
        </div>
        <div v-if="members.length === 0" class="card-body cell-sub">No nodes in this cluster.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Name</th><th>Role</th><th>Status</th><th v-if="swarmEnabled">Swarm</th><th v-if="swarmEnabled"></th></tr></thead>
            <tbody>
              <tr v-for="n in members" :key="n.id" class="row-clickable" @click="router.push(`/admin/nodes/${n.id}`)">
                <td>
                  <span class="cell-title">{{ nodeName(n) }}</span>
                  <div class="cell-sub mono">{{ n.address || (n.is_local ? 'local socket' : '—') }}</div>
                </td>
                <td>
                  <span class="role-tags">
                    <span v-if="n.id === managerNode?.id" class="badge badge-info">manager</span>
                    <span v-if="n.id === ingressNode?.id" class="badge badge-neutral"><span class="mdi mdi-web"></span> gateway</span>
                    <span v-if="n.id !== managerNode?.id && n.id !== ingressNode?.id" class="cell-sub">worker</span>
                  </span>
                </td>
                <td><span class="badge" :class="statusClass(n)">{{ statusLabel(n) }}</span></td>
                <td v-if="swarmEnabled"><span class="badge" :class="swarmClass(n)">{{ swarmLabel(n) }}</span></td>
                <td v-if="swarmEnabled" class="text-right">
                  <button v-if="n.in_swarm && !n.is_local" class="btn btn-xs btn-secondary" :disabled="busy" @click.stop="pendingLeave = n">Leave</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <AppModal v-if="showGeneral" @close="showGeneral = false">
        <div class="modal-header">
          <h3>Edit cluster</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showGeneral = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveGeneral">
          <div class="modal-body">
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Location name</label>
                <input v-model="generalForm.display_name" class="form-input" maxlength="40" placeholder="e.g. Frankfurt" autofocus />
              </div>
              <div class="form-group">
                <label class="form-label">Location code</label>
                <input v-model="generalForm.location_code" class="form-input mono" maxlength="32" placeholder="e.g. eu-central" />
              </div>
            </div>
            <p class="form-hint" style="margin-top: -8px">The code takes lowercase letters, digits and hyphens. Two clusters may share one.</p>
            <div class="form-group">
              <label class="form-label">Who can place here</label>
              <template v-if="cluster?.dedicated">
                <p class="form-static"><DedicatedBadge :dedicated="true" :organization="dedicatedTo" /></p>
                <p class="form-hint">
                  Only {{ dedicatedTo || 'its organization' }} sees this location and may place in it. Changing that is
                  its own action: use Change beside “Belongs to”.
                </p>
              </template>
              <template v-else>
                <select v-model="generalForm.visibility" class="form-select">
                  <option value="all">All workspaces</option>
                  <option value="restricted">Platform admins only</option>
                </select>
                <p class="form-hint">
                  To give the location to one organization, use Change beside “Belongs to”: it decides who may see and
                  place here, so it is confirmed on its own.
                </p>
              </template>
            </div>
            <div class="form-group">
              <label class="form-label"><input v-model="generalForm.cordoned" type="checkbox" /> Cordoned</label>
              <p class="form-hint">No new apps, databases or volumes land here; running ones stay.</p>
            </div>
            <div v-if="cluster?.mode === 'swarm'" class="form-group" style="margin-bottom: 0">
              <label class="form-label">Service load balancing</label>
              <select v-model="generalForm.service_endpoint_mode" class="form-select">
                <option value="vip">Virtual IP</option>
                <option value="dnsrr">DNS round-robin</option>
              </select>
              <p class="form-hint">
                How service apps are reached by name. Choose DNS round-robin when the network check reports that
                virtual IPs do not work, as on nodes that are LXC containers. Running services switch in place.
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showGeneral = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="showAddress" @close="showAddress = false">
        <div class="modal-header">
          <h3>Public address</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showAddress = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveAddress">
          <div class="modal-body">
            <p class="cell-sub" style="margin-top: 0">
              Where DNS records for this location's routes point: its gateway, or a load balancer in front of it. Set
              both IP families to serve visitors on either.
            </p>
            <div class="form-group">
              <label class="form-label" for="addr-v4">IPv4 address</label>
              <input id="addr-v4" v-model="addressForm.ipv4" class="form-input mono" :class="{ 'is-invalid': addressErrors.ipv4 }" placeholder="e.g. 203.0.113.10" autofocus />
              <p v-if="addressErrors.ipv4" class="form-error">{{ addressErrors.ipv4 }}</p>
            </div>
            <div class="form-group">
              <label class="form-label" for="addr-v6">IPv6 address</label>
              <input id="addr-v6" v-model="addressForm.ipv6" class="form-input mono" :class="{ 'is-invalid': addressErrors.ipv6 }" placeholder="e.g. 2001:db8:4f8::10" />
              <p v-if="addressErrors.ipv6" class="form-error">{{ addressErrors.ipv6 }}</p>
              <p v-else class="form-hint">The gateway must accept IPv6 connections on ports 80 and 443, or IPv6 visitors fail to connect.</p>
            </div>
            <div class="form-group">
              <label class="form-label" for="addr-host">Hostname</label>
              <input id="addr-host" v-model="addressForm.hostname" class="form-input mono" :class="{ 'is-invalid': addressErrors.hostname }" placeholder="e.g. lb.eu-central.example.com" />
              <p v-if="addressErrors.hostname" class="form-error">{{ addressErrors.hostname }}</p>
              <p v-else class="form-hint">A CNAME target, used only when no IP is set, as for a load balancer whose addresses change.</p>
            </div>
            <div class="preview">
              <div class="preview-title">Records for example.com and www.example.com</div>
              <div v-if="draftRecords.length" class="rec-table">
                <div v-for="r in draftRecords" :key="`${r.name}-${r.type}`" class="rec-row rec-row--compact">
                  <span class="badge badge-neutral rec-type">{{ r.type }}</span>
                  <span class="mono">{{ r.name }}</span>
                  <span class="mono rec-value">{{ r.value }}</span>
                </div>
              </div>
              <p v-else class="cell-sub" style="margin: 0">None: Miabi will manage no DNS records for this location.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showAddress = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !addressValid">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="showExternal" @close="showExternal = false">
        <div class="modal-header">
          <h3>External access</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showExternal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveExternal">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">External domain</label>
              <input
                v-model="externalForm.external_base_domain"
                class="form-input mono"
                placeholder="e.g. apps.eu-central.example.com"
                :disabled="cluster?.external_domain_pinned"
                autofocus
              />
              <p v-if="cluster?.external_domain_pinned" class="form-hint">Set by environment (MIABI_EXTERNAL_BASE_DOMAIN).</p>
              <p v-else-if="editDomain" class="form-hint">
                Point <code>*.{{ editDomain }}</code> at
                <template v-if="hasAddress"><code>{{ [address.ipv4, address.ipv6].filter(Boolean).join(' and ') || address.hostname }}</code></template>
                <template v-else>this cluster's gateway</template>.
                Apps here get one-click URLs under it.
              </p>
              <p v-else class="form-hint">Empty turns one-click external access off in this location. Custom domains still work.</p>
            </div>
            <div class="form-group">
              <label class="form-label">Certificate for generated URLs</label>
              <select v-model.number="externalForm.external_certificate_id" class="form-select" :disabled="!editDomain">
                <option :value="0">One certificate per app, from the certificate provider</option>
                <option v-for="o in certOptions" :key="o.id" :value="o.id" :disabled="!!certProblem(o) && o.id !== externalForm.external_certificate_id">
                  {{ o.display_name || o.name }}{{ certProblem(o) ? ` (${certProblem(o)})` : '' }}
                </option>
              </select>
              <p class="form-hint">
                A wildcard for <code>*.{{ editDomain || 'external-domain' }}</code> serves every app here at once, with no
                certificate to request per app. Import one, or issue it through a DNS provider, under Certificates in the
                Miabi System workspace. If it expires or stops matching, apps fall back to a certificate per app.
              </p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Certificate provider</label>
              <input
                v-model="externalForm.external_cert_provider"
                class="form-input mono"
                placeholder="Gateway default"
                :disabled="cluster?.external_provider_pinned"
              />
              <p class="form-hint">
                {{ cluster?.external_provider_pinned
                  ? 'Set by environment (MIABI_EXTERNAL_BASE_PROVIDER).'
                  : 'The gateway certManager provider for per-app certificates, and the fallback for a wildcard. Empty uses its default.' }}
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showExternal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="showDedication" @close="showDedication = false">
        <div class="modal-header">
          <h3>Who this location belongs to</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDedication = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <OrganizationPicker
            v-model="dedicationTarget"
            label="Organization"
            default-label="Shared — any workspace may place here"
          />
          <p class="form-hint">
            A dedicated location is seen only by its organization, and that organization is then confined to the
            locations it owns, so its workloads never land on shared hardware. Resources already here do not move.
          </p>

          <div class="ded-impact">
            <p v-if="!dedicationImpact" class="cell-sub"><span class="spinner spinner-sm"></span> Reading what runs here…</p>
            <template v-else-if="dedicationImpact.workloads === 0">
              <p class="cell-sub">Nothing runs here yet, so nothing is affected.</p>
            </template>
            <template v-else>
              <p class="ded-impact-title">{{ dedicationImpact.workloads }} workload(s) run here</p>
              <ul class="ded-tenants">
                <li v-for="tnt in dedicationImpact.tenants" :key="tnt.organization_id">
                  <span>{{ tnt.name || 'Workspaces in no organization' }}</span>
                  <span class="cell-sub">{{ tnt.workloads }}</span>
                </li>
              </ul>
            </template>
          </div>

          <div v-if="dedicationChanged && strandedCount > 0" class="app-banner app-banner--warning" style="margin-top: 12px">
            <span class="mdi mdi-alert-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">{{ strandedCount }} workload(s) would be left behind</p>
              <p class="app-banner-text">
                They keep running, but the workspaces that own them can no longer see, scale or place beside them here.
                Move or delete them first.
              </p>
            </div>
          </div>

          <label v-if="dedicationChanged" class="ded-ack">
            <input v-model="dedicationAck" type="checkbox" />
            <span>
              I understand this changes who may see and place in this location
              <template v-if="dedicationTarget === 0">, and that its organization loses the guarantee that its
              workloads run on hardware nobody else shares</template>.
            </span>
          </label>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="showDedication = false">Cancel</button>
          <button class="btn btn-danger" :disabled="dedicationSaving || !dedicationChanged || !dedicationAck" @click="submitDedication">
            {{ dedicationSaving ? 'Saving…' : dedicationTarget === 0 ? 'Release to shared' : 'Dedicate location' }}
          </button>
        </div>
      </AppModal>

      <AppModal v-if="showGateway" @close="showGateway = false">
        <div class="modal-header">
          <h3>Cluster gateway</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showGateway = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveGateway">
          <div class="modal-body">
            <p class="cell-sub" style="margin-top: 0">
              The node whose gateway serves every route in this cluster: the control plane's gateway cannot reach into its overlay.
            </p>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Gateway node</label>
              <select v-model.number="gatewayForm.server_id" class="form-select" required>
                <option v-for="n in gatewayCandidates" :key="n.id" :value="n.id">{{ nodeName(n) }}</option>
              </select>
              <p v-if="gatewayCandidates.length === 0" class="form-hint">No node of this cluster runs its own gateway yet: set one to edge-gateway connectivity.</p>
              <p class="form-hint">
                The public address stays as it is. If the new node has other public IPs, update it under Public address.
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showGateway = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !gatewayForm.server_id">{{ saving ? 'Saving…' : 'Save' }}</button>
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
              Select the nodes to join the swarm. Offline nodes can't be joined from here until their agent reconnects: use the manual command below.
            </p>
            <label
              v-for="n in joinCandidates"
              :key="n.id"
              class="join-row"
              :class="{ 'join-row--disabled': !n.agent_connected }"
            >
              <input type="checkbox" :disabled="!n.agent_connected" v-model="joinSelected[n.id]" />
              <span class="join-row-name">{{ nodeName(n) }}</span>
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
              <p class="form-hint">The address swarm peers reach this manager on: use a private/WG address reachable from your nodes.</p>
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
      :open="confirmRehost"
      :title="editDomain ? 'Move generated URLs?' : 'Remove generated URLs?'"
      :message="rehostMessage"
      :confirm-label="editDomain ? 'Move URLs' : 'Remove URLs'"
      variant="danger"
      :busy="saving"
      @confirm="persistExternal"
      @cancel="confirmRehost = false"
    />

    <ConfirmDialog
      :open="showRemoveAgents"
      :title="$t('confirm.title.removeClusterAgents')"
      :message="$t('confirm.message.clusterDetail.theAgentIsRemoved')"
      :confirm-label="$t('action.removeAgents')"
      variant="danger"
      :busy="busy"
      @confirm="removeAgents"
      @cancel="showRemoveAgents = false"
    />

    <ConfirmDialog
      :open="showApplyNetworking"
      :title="$t('confirm.title.applyClusterNetworking')"
      :message="$t('confirm.message.clusterDetail.eachWorkspaceNetworkIs')"
      :confirm-label="$t('action.apply')"
      :busy="busy"
      @confirm="applyNetworking"
      @cancel="showApplyNetworking = false"
    />

    <ConfirmDialog
      :open="showDisable"
      :title="$t('confirm.title.disableDockerSwarm')"
      :message="isDefault
        ? 'The manager and all member nodes will leave the swarm. Workspace networks are moved back to node-local bridges first, so apps and databases stop being reachable across nodes — anything relying on that will break. Containers are not restarted.'
        : 'Every node leaves the swarm and becomes a standalone cluster of its own. The cluster must have no apps, databases or volumes left.'"
      :confirm-label="$t('action.disableSwarm')"
      variant="danger"
      :busy="busy"
      @confirm="disableSwarm"
      @cancel="showDisable = false"
    />

    <ConfirmDialog
      :open="!!pendingLeave"
      :title="$t('confirm.title.removeNodeFromTheSwarm')"
      :message="$t('confirm.message.clusterDetail.removeNameFromThe', { name: pendingLeave?.display_name || pendingLeave?.name })"
      :confirm-label="$t('action.remove')"
      variant="danger"
      :busy="busy"
      @confirm="leaveNode"
      @cancel="pendingLeave = null"
    />
  </div>
</template>

<style scoped>
.back-link { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); font-size: 13px; text-decoration: none; margin-bottom: 4px; }
.back-link:hover { color: var(--text-primary); }
.page-header h1 { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.handle { font-size: 12px; font-weight: 400; color: var(--text-muted); }
.header-actions { display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.mono { font-family: var(--font-mono, monospace); }
.section-gap { margin-bottom: 16px; }
.select-all { user-select: all; overflow-wrap: anywhere; }

.hero { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 18px 28px; padding: 18px 22px; margin-bottom: 16px; }
.hero-main { display: flex; align-items: center; gap: 14px; min-width: 0; }
.hero-icon { display: inline-flex; align-items: center; justify-content: center; width: 44px; height: 44px; border-radius: 12px; font-size: 24px; flex-shrink: 0; }
.hero-icon.is-ok { background: var(--primary-50); color: var(--primary-600); }
.hero-icon.is-warn { background: var(--warning-50); color: var(--warning-600); }
.hero-title { display: flex; align-items: center; gap: 8px; font-size: 16px; font-weight: 700; color: var(--text-primary); }
.hero-chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; }
.chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 9px; border-radius: 999px; font-size: 12px; background: var(--bg-tertiary); color: var(--text-secondary); border: none; font-family: inherit; }
.chip .mdi { font-size: 13px; }
.chip-btn { cursor: pointer; }
.chip-btn:hover { filter: brightness(0.96); }
.chip-ok { background: var(--success-50); color: var(--success-600); }
.chip-warn { background: var(--warning-50); color: var(--warning-600); }
.chip-info { background: var(--primary-50); color: var(--primary-600); }
.hero-stats { display: flex; flex-wrap: wrap; gap: 8px 28px; }
.hero-stat { display: flex; flex-direction: column; gap: 2px; min-width: 84px; }
.hero-stat-label { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.hero-stat-value { font-size: 18px; font-weight: 700; color: var(--text-primary); }
.hero-stat-value small { font-size: 12px; font-weight: 500; color: var(--text-muted); }
.hero-stat-sub { font-size: 11px; color: var(--text-muted); }
.hero-foot { flex-basis: 100%; margin: -6px 0 0; }
.meter { display: block; height: 4px; width: 84px; border-radius: 2px; background: var(--bg-tertiary); overflow: hidden; }
.meter > span { display: block; height: 100%; background: var(--primary-500); }
.meter-warn > span { background: var(--warning-500, #f59e0b); }
.meter-bad > span { background: var(--danger-500, #ef4444); }

.banner-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-top: 10px; }
.form-select-sm { width: auto; padding-top: 4px; padding-bottom: 4px; font-size: 13px; }

.tab-dot { display: inline-block; width: 7px; height: 7px; margin-left: 4px; border-radius: 50%; background: var(--warning-500, #f59e0b); vertical-align: middle; }
.tab-count { margin-left: 4px; padding: 0 6px; border-radius: 999px; font-size: 11px; background: var(--bg-secondary, rgba(0, 0, 0, 0.06)); }

.grid-2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; align-items: start; }
.card-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.card-sub { margin: 4px 0 0; font-size: 12px; font-weight: 400; color: var(--text-muted); }
.card-body--foot { border-top: 1px solid var(--border-primary); }
.card-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.kv { display: grid; grid-template-columns: max-content 1fr; gap: 8px 18px; margin: 0; font-size: 13px; }
.kv dt { color: var(--text-muted); }
.kv dd { margin: 0; display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; }

.addr-list { padding: 4px 0; }
.addr-row { display: grid; grid-template-columns: 90px 1fr auto 28px; align-items: center; gap: 12px; padding: 10px 20px; font-size: 13px; }
.addr-row + .addr-row { border-top: 1px solid var(--border-primary); }
.addr-row--unused .addr-value { opacity: 0.7; }
.addr-family { font-weight: 600; color: var(--text-secondary); }
.addr-value { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; overflow-wrap: anywhere; }
.addr-gap { width: 28px; }

.rec-heading { display: flex; align-items: baseline; gap: 8px; margin: 0 0 8px; font-size: 13px; font-weight: 600; }
.rec-table + .rec-heading { margin-top: 18px; }
.rec-table { border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; }
.rec-row { display: grid; grid-template-columns: 64px minmax(0, 1fr) minmax(0, 1fr) auto; align-items: center; gap: 12px; padding: 8px 12px; font-size: 13px; }
.rec-row--compact { grid-template-columns: 64px minmax(0, 1fr) minmax(0, 1fr); padding: 6px 10px; font-size: 12px; }
.rec-row + .rec-row { border-top: 1px solid var(--border-primary); }
.rec-type { justify-self: start; }
.rec-value { overflow-wrap: anywhere; }
.rec-why { font-size: 11px; color: var(--text-muted); white-space: nowrap; }
.rec-rules { margin: 14px 0 0; padding-left: 18px; font-size: 12px; color: var(--text-muted); line-height: 1.7; }

.preview { margin-top: 4px; padding: 12px; border-radius: 8px; background: var(--bg-tertiary); }
.preview-title { margin-bottom: 8px; font-size: 12px; font-weight: 600; color: var(--text-secondary); }
.preview .rec-table { background: var(--bg-primary); }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.is-invalid { border-color: var(--danger-500, #ef4444); }

.swarm-head { display: flex; align-items: flex-start; gap: 12px; }
.swarm-head > .mdi { font-size: 22px; color: var(--text-secondary); }
.swarm-head h3 { display: flex; align-items: center; gap: 8px; }
.agent-flags { display: flex; gap: 6px; }
.ingress-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 12px; font-size: 12px; color: var(--text-muted); }
.netcheck-summary { display: flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 600; }
.netcheck-summary.ok { color: var(--success-600, #15803d); }
.netcheck-summary.bad { color: var(--danger-600, #b91c1c); }
.text-ok { color: var(--success-600, #15803d); }
.text-bad { color: var(--danger-600, #b91c1c); }
.role-tags { display: inline-flex; flex-wrap: wrap; gap: 4px; }

.ded-impact { margin-top: 14px; padding: 10px 14px; border: 1px solid var(--border-primary); border-radius: 8px; }
.ded-impact-title { margin: 0 0 6px; font-size: 13px; font-weight: 600; }
.ded-tenants { margin: 0; padding: 0; list-style: none; font-size: 13px; }
.ded-tenants li { display: flex; justify-content: space-between; gap: 12px; padding: 2px 0; }
.ded-ack { display: flex; align-items: flex-start; gap: 8px; margin-top: 14px; font-size: 13px; line-height: 1.5; }
.form-static { margin: 0; }
.spinner-sm { width: 13px; height: 13px; }

.pf-finding { border: 1px solid; border-radius: 6px; padding: 8px 10px; margin-bottom: 10px; font-size: 12px; }
.pf-blocker { border-color: var(--danger-200, #f0a6a6); background: var(--danger-50, rgba(220, 90, 90, 0.1)); }
.pf-warning { border-color: var(--warning-200, #f5c26b); background: var(--warning-50, rgba(245, 194, 107, 0.12)); }
.pf-title { display: flex; align-items: center; gap: 6px; font-weight: 600; }
.pf-detail { margin: 4px 0 0; color: var(--text-muted); }
.pf-ports { margin-bottom: 12px; font-size: 12px; }
.pf-ports summary { cursor: pointer; color: var(--text-muted); }
.pf-table td { padding: 4px 8px 4px 0; vertical-align: top; }
.ca-mode { display: flex; gap: 14px; margin: 8px 0; font-size: 12px; }
.ca-mode label { display: inline-flex; align-items: center; gap: 4px; cursor: pointer; }
.ca-actions { margin: 8px 0; }
.ca-cert { margin: 8px 0; padding: 8px 10px; border: 1px solid var(--border-primary); border-radius: 6px; font-size: 12px; }
.ca-cert dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 10px; margin: 6px 0; }
.ca-cert dt { color: var(--text-muted); }
.ca-cert dd { margin: 0; overflow-wrap: anywhere; }
.ca-pem { font-family: var(--font-mono, monospace); font-size: 11px; margin-top: 8px; }
.form-hint-warn { color: var(--warning-600, #b45309); }
.join-row { display: flex; align-items: center; gap: 10px; padding: 8px 0; cursor: pointer; }
.join-row--disabled { opacity: 0.55; cursor: not-allowed; }
.join-row-name { flex: 1; font-weight: 500; }
.manual-join { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--border-primary); }
.manual-join-title { font-weight: 600; margin-bottom: 6px; }

@media (max-width: 640px) {
  .form-row { grid-template-columns: 1fr; }
  .addr-row { grid-template-columns: 70px 1fr 28px; padding: 10px 14px; }
  .addr-row > .badge { display: none; }
  .rec-row { grid-template-columns: 56px 1fr; }
  .rec-row .rec-value { grid-column: 2; }
  .grid-2 { grid-template-columns: 1fr; }
}
</style>
