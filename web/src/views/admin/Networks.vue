<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import type { NetworkAllocation, NetworkingInfo } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'

const notify = useNotificationStore()
const info = ref<NetworkingInfo | null>(null)
const allocations = ref<NetworkAllocation[]>([])
const loading = ref(false)
const search = ref('')
const kind = ref('')

async function load() {
  loading.value = true
  try {
    const [i, a] = await Promise.all([adminApi.getNetworkingInfo(), adminApi.getNetworkAllocations()])
    info.value = i.data.data
    allocations.value = a.data.data.allocations ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const KINDS: Record<string, { label: string; cls: string }> = {
  workspace: { label: 'Workspace network', cls: 'badge-info' },
  stack: { label: 'Stack network', cls: 'badge-info' },
  overlay: { label: 'Swarm overlay', cls: 'badge-info' },
  gateway: { label: 'Ingress overlay', cls: 'badge-success' },
  reserved: { label: 'Reserved', cls: 'badge-warning' },
}
const kindOf = (k: string) => KINDS[k] ?? { label: k, cls: 'badge-neutral' }
const kindCounts = computed(() => {
  const out: Record<string, number> = {}
  for (const a of allocations.value) out[a.kind] = (out[a.kind] ?? 0) + 1
  return out
})

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return allocations.value.filter((a) => {
    if (kind.value && a.kind !== kind.value) return false
    if (!q) return true
    return [a.subnet, a.docker_name, a.workspace_name, a.owner_name, a.node_name].some((v) => v?.toLowerCase().includes(q))
  })
})

const usagePercent = computed(() => {
  const i = info.value
  if (!i?.pool_total) return 0
  return Math.min(100, Math.round((i.pool_used / i.pool_total) * 100))
})

const SUBNET_SOURCES: Record<string, { label: string; title: string }> = {
  configured: { label: 'configured', title: 'Set by MIABI_PROXY_SUBNET, which the installer fills from networking.proxy.subnet in miabi.yaml.' },
  'control-plane': { label: "copied from the control plane", title: "MIABI_PROXY_SUBNET is unset, so nodes copy the control plane's own proxy network." },
  default: { label: 'default', title: "Neither MIABI_PROXY_SUBNET nor a local proxy network exists, so nodes use the installer's default. A node where it overlaps another network falls back to Docker's pool." },
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Networks</h1>
        <p class="text-muted">Address ranges for the networks Miabi creates, and every subnet handed out from its pool.</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="load">
        <span class="mdi mdi-refresh"></span> Refresh
      </button>
    </div>

    <div v-if="info" class="card section-gap">
      <div class="card-header">
        <div>
          <h2 class="card-title">Configuration</h2>
          <p class="card-sub">Set through the environment or the install manifest and applied at boot.</p>
        </div>
      </div>
      <div class="card-body">
        <div class="kv-grid">
          <span class="text-muted" title="MIABI_NETWORK_POOL_CIDR / MIABI_NETWORK_SUBNET_PREFIX">Subnet pool</span>
          <span>
            <span class="mono text-sm">{{ info.pool_cidr || '—' }}</span>
            <span v-if="info.subnet_prefix" class="text-muted text-sm"> · carved into /{{ info.subnet_prefix }}s</span>
            <span v-if="!info.pool_enabled" class="badge badge-warning" style="margin-left: 6px" title="The allocator could not start, so networks use Docker's own address pool.">not in use</span>
          </span>

          <template v-if="info.pool_enabled">
            <span class="text-muted">Pool usage</span>
            <span class="usage">
              <span class="usage-bar"><span class="usage-fill" :class="{ warn: usagePercent >= 85 }" :style="{ width: usagePercent + '%' }"></span></span>
              <span class="text-sm">{{ info.pool_used.toLocaleString() }} of {{ info.pool_total.toLocaleString() }} subnets ({{ usagePercent }}%)</span>
            </span>
          </template>

          <span class="text-muted" title="MIABI_PROXY_NETWORK">Shared proxy network</span>
          <span class="mono text-sm">{{ info.proxy_network || '—' }}</span>

          <span class="text-muted" title="MIABI_PROXY_SUBNET">Proxy network subnet</span>
          <span>
            <span class="mono text-sm">{{ info.proxy_subnet || "Docker's pool" }}</span>
            <span
              v-if="info.proxy_subnet_source && SUBNET_SOURCES[info.proxy_subnet_source]"
              class="badge badge-neutral"
              style="margin-left: 6px"
              :title="SUBNET_SOURCES[info.proxy_subnet_source].title"
            >{{ SUBNET_SOURCES[info.proxy_subnet_source].label }}</span>
          </span>

          <span class="text-muted" title="MIABI_NETWORK_IPV6">IPv6</span>
          <span>
            <span class="badge" :class="info.ipv6_active ? 'badge-success' : 'badge-neutral'">{{ info.ipv6_active ? 'enabled' : 'disabled' }}</span>
            <span v-if="info.ipv6_enabled && !info.ipv6_active" class="badge badge-warning" style="margin-left: 6px">requested</span>
          </span>

          <template v-if="info.ipv6_active || info.ipv6_ula_prefix">
            <span class="text-muted" title="MIABI_NETWORK_IPV6_ULA_PREFIX">IPv6 addressing</span>
            <span class="mono text-sm">{{ info.ipv6_ula_prefix ? `${info.ipv6_ula_prefix} · derived from each IPv4 subnet` : 'assigned by Docker' }}</span>
          </template>
        </div>
        <p v-if="info.ipv6_enabled && !info.ipv6_active" class="form-hint text-warning" style="margin-top: 10px">
          IPv6 is requested but not in force: {{ info.ipv6_reason }}
        </p>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <div>
          <h2 class="card-title">Subnet allocations</h2>
          <p class="card-sub">Each subnet is pinned to one network, so a network gets the same range on every node.</p>
        </div>
      </div>
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input v-model="search" class="form-input" type="search" placeholder="Search by subnet, network, workspace or node…" aria-label="Search allocations" />
        </div>
        <div class="filters">
          <button class="chip" :class="{ active: kind === '' }" @click="kind = ''">All</button>
          <button v-for="(n, k) in kindCounts" :key="k" class="chip" :class="{ active: kind === k }" @click="kind = String(k)">
            {{ kindOf(String(k)).label }} <span class="chip-count">{{ n }}</span>
          </button>
        </div>
        <span class="text-muted">{{ filtered.length }} subnet{{ filtered.length === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && allocations.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="filtered.length === 0" class="empty-state">
        <span class="mdi mdi-lan" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ allocations.length ? 'No subnets match your filters.' : 'No subnet has been allocated yet.' }}</h3>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Subnet</th><th>Network</th><th>Kind</th><th>Workspace</th><th>Node</th><th>Allocated</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in filtered" :key="a.id">
              <td>
                <span class="mono">{{ a.subnet }}</span>
                <div v-if="a.gateway" class="cell-sub mono">gateway {{ a.gateway }}</div>
              </td>
              <td>
                <template v-if="a.kind === 'reserved'">
                  <span class="cell-sub" title="Found on a host without an allocation, for instance a network created before the pool existed, so the allocator never hands it out.">Already in use on a host</span>
                </template>
                <template v-else>
                  <span class="mono text-sm">{{ a.docker_name }}</span>
                  <div v-if="a.owner_name" class="cell-sub">{{ a.owner_name }}</div>
                </template>
              </td>
              <td><span class="badge" :class="kindOf(a.kind).cls">{{ kindOf(a.kind).label }}</span></td>
              <td>
                <router-link v-if="a.workspace_id" :to="`/admin/workspaces/${a.workspace_id}`">{{ a.workspace_name || `#${a.workspace_id}` }}</router-link>
                <span v-else class="cell-sub">—</span>
              </td>
              <td>
                <router-link v-if="a.node_id && a.node_name" :to="`/admin/nodes/${a.node_id}`">{{ a.node_name }}</router-link>
                <span v-else class="cell-sub" :title="a.kind === 'reserved' ? '' : 'Pinned to the network, not to one node'">{{ a.kind === 'reserved' ? '—' : 'every node' }}</span>
              </td>
              <td class="cell-sub" :title="new Date(a.created_at).toLocaleString()">{{ new Date(a.created_at).toLocaleDateString() }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.section-gap { margin-bottom: 16px; }
.kv-grid { display: grid; grid-template-columns: max-content 1fr; gap: 10px 24px; align-items: center; }
.usage { display: inline-flex; align-items: center; gap: 10px; }
.usage-bar { display: inline-block; width: 180px; height: 6px; border-radius: 999px; background: var(--bg-tertiary); overflow: hidden; }
.usage-fill { display: block; height: 100%; background: var(--primary-500); }
.usage-fill.warn { background: var(--warning); }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
.search { position: relative; flex: 1; max-width: 360px; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; }
.filters { display: flex; gap: 6px; flex-wrap: wrap; }
.chip { padding: 4px 12px; border-radius: 999px; border: 1px solid var(--border); background: transparent; color: var(--text-muted); font-size: 13px; cursor: pointer; }
.chip.active { background: var(--primary-500); border-color: var(--primary-500); color: #fff; }
.chip-count { opacity: 0.75; margin-left: 2px; }
</style>
