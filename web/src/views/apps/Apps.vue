<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { appApi } from '@/api/apps'
import { usageApi } from '@/api/resources'
import { registryApi } from '@/api/registries'
import { gitRepositoryApi } from '@/api/gitRepositories'
import { networkApi } from '@/api/networks'
import { stackApi } from '@/api/stacks'
import type { Application, Registry, GitRepository, Network, Stack, AppPort, BuildMethod, RuntimeKind } from '@/api/types'
import NodePicker from '@/components/NodePicker.vue'
import PlacementPicker from '@/components/PlacementPicker.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const apps = ref<Application[]>([])
const search = ref('')
// Client-side filter (the list isn't paginated) over name, slug, image/repo, node.
const filteredApps = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return apps.value
  return apps.value.filter((a) =>
    a.name.toLowerCase().includes(q) ||
    (a.display_name || '').toLowerCase().includes(q) ||
    (a.image || '').toLowerCase().includes(q) ||
    (a.git_repo || '').toLowerCase().includes(q) ||
    (a.server_name || '').toLowerCase().includes(q),
  )
})
const registries = ref<Registry[]>([])
const gitRepos = ref<GitRepository[]>([])
const networks = ref<Network[]>([])
const stacks = ref<Stack[]>([])
const loading = ref(false)
const showCreate = ref(false)
const creating = ref(false)
// Cluster mode availability — when on, apps may run as replicated Swarm services.
const clusterEnabled = ref(false)

interface AppForm {
  name: string
  server_id: number
  source_type: 'image' | 'git'
  image: string
  tag: string
  git_repo: string
  git_ref: string
  build_method: BuildMethod
  builder: string
  registry_id: number | null
  git_repository_id: number | null
  stack_id: number | null
  network_ids: number[]
  ports: AppPort[]
  runtime_kind: RuntimeKind
  replicas: number
  // Swarm placement constraints, e.g. ["node.id==abc"]. Only meaningful for the
  // service runtime, where the scheduler — not server_id — decides placement.
  placement_constraints: string[]
}
function emptyForm(): AppForm {
  return { name: '', server_id: 0, source_type: 'image', image: '', tag: '', git_repo: '', git_ref: '', build_method: 'auto', builder: '', registry_id: null, git_repository_id: null, stack_id: null, network_ids: [], ports: [{ container_port: 8080, protocol: 'tcp', scheme: 'http', name: '' }], runtime_kind: 'container', replicas: 1, placement_constraints: [] }
}
// A service is placed by the Swarm scheduler, which ignores server_id; a container
// is placed by server_id. The two are never both meaningful, so the form shows one
// control or the other.
const isService = computed(() => clusterEnabled.value && form.value.runtime_kind === 'service')
function addPort() {
  form.value.ports.push({ container_port: 0, protocol: 'tcp', scheme: 'http', name: '' })
}
function removePort(i: number) {
  form.value.ports.splice(i, 1)
}
const form = ref<AppForm>(emptyForm())

async function load(id: number | null) {
  apps.value = []
  if (!id) return
  loading.value = true
  try {
    apps.value = (await appApi.list(id)).data.data ?? []
    registries.value = (await registryApi.list(id)).data.data ?? []
    gitRepos.value = (await gitRepositoryApi.list(id)).data.data ?? []
    networks.value = (await networkApi.list(id)).data.data ?? []
    stacks.value = (await stackApi.list(id)).data.data ?? []
    // Whether the "service" runtime is offerable comes from the workspace usage
    // capabilities (readable by any member), not the platform-admin cluster status.
    try { clusterEnabled.value = (await usageApi.get(id)).data.data?.capabilities?.cluster_enabled === true } catch { clusterEnabled.value = false }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  form.value = emptyForm()
  // In cluster mode, default new apps to the replicated service runtime (mirrors
  // the backend default); the user can still switch back to a single container.
  if (clusterEnabled.value) form.value.runtime_kind = 'service'
  showCreate.value = true
}

async function create() {
  if (!currentWorkspaceId.value) return
  creating.value = true
  try {
    const isImage = form.value.source_type === 'image'
    await appApi.create(currentWorkspaceId.value, {
      display_name: form.value.name.trim(),
      server_id: form.value.server_id,
      source_type: form.value.source_type,
      image: isImage ? form.value.image.trim() : undefined,
      tag: isImage ? form.value.tag.trim() || undefined : undefined,
      git_repo: !isImage ? form.value.git_repo.trim() || undefined : undefined,
      git_ref: !isImage ? form.value.git_ref.trim() || undefined : undefined,
      build_method: !isImage ? form.value.build_method : undefined,
      builder: !isImage && form.value.build_method !== 'dockerfile' ? form.value.builder.trim() || undefined : undefined,
      registry_id: isImage ? form.value.registry_id : null,
      git_repository_id: !isImage ? form.value.git_repository_id : null,
      stack_id: form.value.stack_id,
      network_ids: form.value.network_ids,
      ports: form.value.ports.filter((p) => p.container_port > 0),
      // Cluster runtime. Send the explicit choice when the selector is shown;
      // otherwise omit it so the backend applies its cluster-mode default (which
      // is "service" when cluster mode is on) — this also self-heals if the UI
      // couldn't read cluster state but the server is in fact a swarm manager.
      runtime_kind: clusterEnabled.value ? form.value.runtime_kind : undefined,
      replicas: isService.value ? Math.max(1, form.value.replicas) : undefined,
      // Only a service has placement constraints — a container is placed by
      // server_id above, and the Swarm scheduler is not involved.
      placement_constraints: isService.value && form.value.placement_constraints.length ? form.value.placement_constraints : undefined,
    })
    notify.success('Application created')
    showCreate.value = false
    await load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e, 'Failed to create application')
  } finally {
    creating.value = false
  }
}

// When a saved repository is selected, reflect its clone URL in the URL field
// so it's visible (and still editable as a per-app override).
function onGitRepoSelect() {
  const repo = gitRepos.value.find((r) => r.id === form.value.git_repository_id)
  if (repo) form.value.git_repo = repo.url
}

function badge(status: string) {
  return status === 'running' ? 'badge-success' : status === 'failed' ? 'badge-danger' : status === 'deploying' ? 'badge-warning' : 'badge-neutral'
}

function formatCreated(ts?: string) {
  if (!ts) return '—'
  return new Date(ts).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Applications</h1>
        <p class="subtitle">Containerized apps Miabi builds, deploys, and runs for {{ ws.contextLabel }}.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New application
      </button>
    </div>

    <div class="card">
      <div v-if="loading && apps.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="apps.length === 0" class="empty-state">
        <span class="mdi mdi-cube-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No applications yet</h3>
        <p>Deploy from a Docker image or a Git repository — or install a ready-made app from the Marketplace.</p>
        <div class="empty-actions mt-4">
          <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">Deploy your first application</button>
          <button class="btn btn-secondary" @click="router.push({ name: 'marketplace' })">
            <span class="mdi mdi-storefront-outline"></span> Browse the Marketplace
          </button>
        </div>
      </div>
      <template v-else>
        <div class="card-body toolbar">
          <div class="search">
            <span class="mdi mdi-magnify"></span>
            <input v-model="search" class="form-input" type="search" aria-label="Search applications" placeholder="Search applications by name, image, repo, or node…" />
          </div>
          <span class="text-muted text-sm">{{ filteredApps.length }} of {{ apps.length }}</span>
        </div>
        <div v-if="filteredApps.length === 0" class="empty-state" style="padding: 40px">
          <span class="mdi mdi-magnify" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No applications match “{{ search }}”.</p>
        </div>
        <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Application</th><th>Source</th><th>Node</th><th>Status</th><th class="text-right">Created</th></tr></thead>
          <tbody>
            <tr v-for="a in filteredApps" :key="a.id" class="row-clickable" @click="router.push(`/apps/${a.id}`)">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm">{{ (a.display_name || a.name).charAt(0).toUpperCase() }}</span>
                  <span class="cell-text">
                    <span class="cell-title">{{ a.display_name || a.name }}</span>
                    <span class="cell-sub">{{ a.name }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ a.source_type === 'git' ? a.git_repo : `${a.image}:${a.tag || 'latest'}` }}</td>
              <td class="cell-sub">
                <span v-if="a.server_name"><span class="mdi mdi-server-network"></span> {{ a.server_name }}</span>
                <span v-else>—</span>
              </td>
              <td><span class="badge badge-dot" :class="badge(a.status)">{{ a.status }}</span></td>
              <td class="cell-sub text-right">{{ formatCreated(a.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        </div>
      </template>
    </div>

    <Teleport to="body">
      <div v-if="showCreate" class="modal-overlay" @click.self="showCreate = false">
        <div class="modal">
          <div class="modal-header">
            <h3>New application</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="create">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="e.g. web-api" required autofocus />
              </div>
              <!-- Cluster runtime: only offered when cluster mode is enabled. -->
              <div v-if="clusterEnabled" class="form-row">
                <div class="form-group" style="flex: 2; margin-bottom: 0">
                  <label class="form-label">Runtime</label>
                  <select v-model="form.runtime_kind" class="form-select">
                    <option value="container">Container (single node)</option>
                    <option value="service">Service (replicated, cluster)</option>
                  </select>
                </div>
                <div v-if="isService" class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Replicas</label>
                  <input v-model.number="form.replicas" type="number" min="1" class="form-input" placeholder="1" />
                </div>
              </div>
              <!--
                A container is placed by server_id; a service is placed by the Swarm
                scheduler, which ignores server_id. Showing the node picker for a
                service would silently discard the choice, so each runtime gets the
                control that actually decides where it runs.
              -->
              <NodePicker v-if="!isService" v-model="form.server_id" />
              <PlacementPicker v-else v-model="form.placement_constraints" :replicas="form.replicas" />
              <p v-if="isService" class="form-hint">
                Runs as a Swarm service on the workspace overlay network with {{ Math.max(1, form.replicas) }} replica(s).
              </p>
              <div class="form-group">
                <label class="form-label">Source</label>
                <div class="tabs" style="margin-bottom: 0">
                  <button type="button" class="tab" :class="{ active: form.source_type === 'image' }" @click="form.source_type = 'image'">Docker image</button>
                  <button type="button" class="tab" :class="{ active: form.source_type === 'git' }" @click="form.source_type = 'git'">Git repository</button>
                </div>
              </div>
              <template v-if="form.source_type === 'image'">
                <div class="form-row">
                  <div class="form-group" style="flex: 2; margin-bottom: 0">
                    <label class="form-label">Image</label>
                    <input v-model="form.image" class="form-input" placeholder="nginx" required />
                  </div>
                  <div class="form-group" style="flex: 1; margin-bottom: 0">
                    <label class="form-label">Tag <span class="text-muted">(optional)</span></label>
                    <input v-model="form.tag" class="form-input" placeholder="latest" />
                  </div>
                </div>
                <p class="form-hint">Deploys <code>{{ form.image || 'image' }}:{{ form.tag || 'latest' }}</code></p>
                <div class="form-group">
                  <label class="form-label">Registry credential <span class="text-muted">(for private images)</span></label>
                  <select v-model="form.registry_id" class="form-select">
                    <option :value="null">Public / none</option>
                    <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }} ({{ r.server }})</option>
                  </select>
                  <p v-if="registries.length === 0" class="form-hint">
                    No registries yet — add one under <RouterLink to="/registries">Registries</RouterLink>.
                  </p>
                </div>
              </template>
              <template v-else>
                <div class="form-group">
                  <label class="form-label">Repository</label>
                  <select v-model="form.git_repository_id" class="form-select" @change="onGitRepoSelect">
                    <option :value="null">Public URL (no saved repository)</option>
                    <option v-for="r in gitRepos" :key="r.id" :value="r.id">{{ r.name }} — {{ r.url }}</option>
                  </select>
                  <p class="form-hint">
                    <template v-if="form.git_repository_id">Uses the saved repository's URL and credentials.</template>
                    <template v-else>Select a saved repository to reuse its URL and credentials.</template>
                    <RouterLink to="/git-repositories">Manage repositories →</RouterLink>
                  </p>
                </div>
                <div class="form-group">
                  <label class="form-label">
                    Repository URL
                    <span v-if="form.git_repository_id" class="text-muted">(optional — overrides the saved repository)</span>
                  </label>
                  <input v-model="form.git_repo" class="form-input" placeholder="https://github.com/user/repo" :required="!form.git_repository_id" />
                </div>
                <div class="form-group">
                  <label class="form-label">Branch / ref <span class="text-muted">(optional)</span></label>
                  <input v-model="form.git_ref" class="form-input" placeholder="main" />
                </div>
                <div class="form-group">
                  <label class="form-label">Build method</label>
                  <select v-model="form.build_method" class="form-select">
                    <option value="auto">Auto (recommended)</option>
                    <option value="buildpack">Buildpacks (no Dockerfile)</option>
                    <option value="dockerfile">Dockerfile</option>
                  </select>
                  <p class="form-hint">
                    Auto builds the repo's Dockerfile when present, otherwise uses Cloud Native Buildpacks.
                  </p>
                </div>
                <div v-if="form.build_method !== 'dockerfile'" class="form-group">
                  <label class="form-label">Builder image <span class="text-muted">(optional, advanced)</span></label>
                  <input v-model="form.builder" class="form-input" placeholder="paketobuildpacks/builder-jammy-base" />
                  <p class="form-hint">Override the Cloud Native Buildpacks builder. Leave empty to use the platform default.</p>
                </div>
              </template>
              <div class="form-group">
                <label class="form-label">Container ports</label>
                <div v-for="(p, i) in form.ports" :key="i" class="port-row">
                  <input v-model.number="p.container_port" type="number" class="form-input" aria-label="Container port" placeholder="8080" style="flex: 1" />
                  <select v-model="p.protocol" class="form-select" aria-label="Port protocol" style="width: 84px">
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                  </select>
                  <select v-model="p.scheme" class="form-select" title="Application protocol (Gateway backend URL)" aria-label="Application protocol (Gateway backend URL)" style="width: 96px">
                    <option value="http">http</option>
                    <option value="https">https</option>
                  </select>
                  <input v-model="p.name" class="form-input" aria-label="Port name" placeholder="name (opt)" style="flex: 1" />
                  <button type="button" class="btn-icon btn-icon-danger" aria-label="Remove port" @click="removePort(i)"><span class="mdi mdi-close"></span></button>
                </div>
                <button type="button" class="btn btn-ghost btn-sm" @click="addPort"><span class="mdi mdi-plus"></span> Add port</button>
              </div>
              <div v-if="networks.length" class="form-group">
                <label class="form-label">Networks <span class="text-muted">(default is always attached)</span></label>
                <label v-for="n in networks" :key="n.id" class="checkbox-label">
                  <input type="checkbox" :value="n.id" v-model="form.network_ids" :disabled="n.is_default" />
                  {{ n.name }} <span v-if="n.is_default" class="text-muted">(default)</span>
                </label>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Stack <span class="text-muted">(optional)</span></label>
                <select v-model="form.stack_id" class="form-select">
                  <option :value="null">None</option>
                  <option v-for="s in stacks" :key="s.id" :value="s.id">{{ s.name }}</option>
                </select>
                <p v-if="stacks.length === 0" class="form-hint">
                  No stacks yet — create one under <RouterLink to="/stacks">Stacks</RouterLink>.
                </p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="creating">{{ creating ? 'Creating…' : 'Create application' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.empty-actions { display: flex; gap: 10px; justify-content: center; flex-wrap: wrap; }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.search { position: relative; flex: 1; max-width: 360px; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; }
.text-muted { color: var(--text-muted); }
.form-row { display: flex; gap: 12px; margin-bottom: 20px; }
.port-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.form-hint code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; color: var(--text-secondary); }
</style>
