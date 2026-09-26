<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { appApi } from '@/api/apps'
import { usageApi } from '@/api/resources'
import { registryApi } from '@/api/registries'
import { gitRepositoryApi, type GitInspectResult } from '@/api/gitRepositories'
import { networkApi } from '@/api/networks'
import { stackApi } from '@/api/stacks'
import { locationApi, type Location } from '@/api/locations'
import type { Application, Registry, GitRepository, Network, Stack, AppPort, BuildMethod, RuntimeKind } from '@/api/types'
import PlacementPicker from '@/components/PlacementPicker.vue'
import LocationPicker from '@/components/LocationPicker.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
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
// Whether any location the workspace may use runs a swarm; the fallback when locations cannot be read.
const anySwarm = ref(false)
const locations = ref<Location[]>([])

interface AppForm {
  name: string
  server_id: number
  location: string
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
  // Adopt the pipeline the repository carries at .miabi/pipeline.yaml. Only
  // offered once a probe has actually found one.
  use_pipeline: boolean
}
function emptyForm(): AppForm {
  return { name: '', server_id: 0, location: '', source_type: 'image', image: '', tag: '', git_repo: '', git_ref: '', build_method: 'auto', builder: '', registry_id: null, git_repository_id: null, stack_id: null, network_ids: [], ports: [{ container_port: 8080, protocol: 'tcp', scheme: 'http', name: '' }], runtime_kind: 'container', replicas: 1, placement_constraints: [], use_pipeline: true }
}

// --- Repository inspection ---
//
// Probing clones the repo server-side, so it is deliberately manual: the user
// asks for it once the URL and branch are filled in. The result decides whether
// the pipeline toggle is offered at all.
const inspecting = ref(false)
const inspected = ref<GitInspectResult | null>(null)
const inspectError = ref('')

/** Reset the probe whenever the thing being probed changes. */
function resetInspection() {
  inspected.value = null
  inspectError.value = ''
}

/** A short human summary of when the discovered pipeline runs. */
const pipelineTriggerLabel = computed(() => {
  const r = inspected.value
  if (!r?.has_pipeline) return ''
  const parts: string[] = []
  if (r.triggers_push) {
    parts.push(r.push_branches?.length ? `on push to ${r.push_branches.join(', ')}` : 'on any push')
  }
  if (r.schedule) parts.push(`on schedule (${r.schedule})`)
  if (!parts.length) parts.push('when you deploy')
  return parts.join(' · ')
})
// A service is placed by the Swarm scheduler, which ignores server_id; a container
// is placed by server_id. The two are never both meaningful, so the form shows one
// control or the other.
// The service runtime needs a swarm in the location the app will land in, not merely somewhere.
const clusterEnabled = computed(() => {
  const selected = locations.value.find((l) => (form.value.location ? l.name === form.value.location : l.default))
  return selected ? !!selected.swarm : anySwarm.value
})
watch(clusterEnabled, (on) => {
  if (!on && form.value.runtime_kind === 'service') form.value.runtime_kind = 'container'
})
const isService = computed(() => clusterEnabled.value && form.value.runtime_kind === 'service')
function addPort() {
  form.value.ports.push({ container_port: 0, protocol: 'tcp', scheme: 'http', name: '' })
}
function removePort(i: number) {
  form.value.ports.splice(i, 1)
}
const form = ref<AppForm>(emptyForm())

  watch(() => [form.value.git_repo, form.value.git_ref, form.value.git_repository_id], resetInspection)

const canInspect = computed(
  () => form.value.source_type === 'git' && (!!form.value.git_repo.trim() || !!form.value.git_repository_id),
)

async function inspectRepo() {
  if (!currentWorkspaceId.value || !canInspect.value) return
  inspecting.value = true
  inspectError.value = ''
  inspected.value = null
  try {
    inspected.value = (
      await gitRepositoryApi.inspect(currentWorkspaceId.value, {
        git_repo: form.value.git_repo.trim() || undefined,
        git_ref: form.value.git_ref.trim() || undefined,
        git_repository_id: form.value.git_repository_id,
      })
    ).data.data ?? null
    // Default the toggle on when a pipeline is found, off otherwise, so the
    // create call never claims a pipeline the probe didn't see.
    form.value.use_pipeline = inspected.value?.has_pipeline === true
  } catch (e: any) {
    inspectError.value = e?.response?.data?.message || e?.message || 'Could not read the repository'
  } finally {
    inspecting.value = false
  }
}
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
    try { anySwarm.value = (await usageApi.get(id)).data.data?.capabilities?.cluster_enabled === true } catch { anySwarm.value = false }
    try { locations.value = (await locationApi.list(id)).data.data?.locations ?? [] } catch { locations.value = [] }
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
      server_id: isService.value ? undefined : form.value.server_id || undefined,
      location: form.value.location || undefined,
      source_type: form.value.source_type,
      image: isImage ? form.value.image.trim() : undefined,
      tag: isImage ? form.value.tag.trim() || undefined : undefined,
      git_repo: !isImage ? form.value.git_repo.trim() || undefined : undefined,
      git_ref: !isImage ? form.value.git_ref.trim() || undefined : undefined,
      build_method: !isImage ? form.value.build_method : undefined,
      builder: !isImage && form.value.build_method !== 'dockerfile' ? form.value.builder.trim() || undefined : undefined,
      registry_id: isImage ? form.value.registry_id : null,
      git_repository_id: !isImage ? form.value.git_repository_id : null,
      // Only claim a pipeline the probe actually found — the backend re-reads the
      // repository regardless, but asking for one that isn't there just produces a
      // warning on the app's timeline.
      use_pipeline: !isImage && form.value.use_pipeline && inspected.value?.has_pipeline === true,
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
    notify.success(t('apps.created'))
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
        <h1>{{ $t('nav.deploy.applications') }}</h1>
        <p class="subtitle">Containerized apps Miabi builds, deploys, and runs for {{ ws.contextLabel }}.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> {{ $t('dashboard.newApplication') }}
      </button>
    </div>

    <div class="card">
      <div v-if="loading && apps.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="apps.length === 0" class="empty-state">
        <span class="mdi mdi-cube-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('dashboard.apps.empty') }}</h3>
        <p>{{ $t('apps.emptyHint') }}</p>
        <div class="empty-actions mt-4">
          <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">{{ $t('apps.deployFirst') }}</button>
          <button class="btn btn-secondary" @click="router.push({ name: 'marketplace' })">
            <span class="mdi mdi-storefront-outline"></span> {{ $t('apps.browseMarketplace') }}
          </button>
        </div>
      </div>
      <template v-else>
        <div class="card-body toolbar">
          <div class="search">
            <span class="mdi mdi-magnify"></span>
            <input v-model="search" class="form-input" type="search" :aria-label="$t('apps.searchLabel')" :placeholder="$t('apps.searchPlaceholder')" />
          </div>
          <span class="text-muted text-sm">{{ filteredApps.length }} of {{ apps.length }}</span>
        </div>
        <div v-if="filteredApps.length === 0" class="empty-state" style="padding: 40px">
          <span class="mdi mdi-magnify" style="font-size: 36px; color: var(--text-muted)"></span>
          <p>No applications match “{{ search }}”.</p>
        </div>
        <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('dashboard.col.application') }}</th><th>{{ $t('apps.col.source') }}</th><th>{{ $t('dashboard.col.node') }}</th><th>{{ $t('dashboard.col.status') }}</th><th class="text-right">{{ $t('dashboard.col.created') }}</th></tr></thead>
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
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('dashboard.newApplication') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" :placeholder="$t('apps.form.namePlaceholder')" required autofocus />
            </div>
            <!-- Cluster runtime: only offered when cluster mode is enabled. -->
            <div v-if="clusterEnabled" class="form-row">
              <div class="form-group" style="flex: 2; margin-bottom: 0">
                <label class="form-label">{{ $t('apps.form.runtime') }}</label>
                <select v-model="form.runtime_kind" class="form-select">
                  <option value="container">{{ $t('apps.form.runtimeContainer') }}</option>
                  <option value="service">{{ $t('apps.form.runtimeService') }}</option>
                </select>
              </div>
              <div v-if="isService" class="form-group" style="flex: 1; margin-bottom: 0">
                <label class="form-label">{{ $t('apps.form.replicas') }}</label>
                <input v-model.number="form.replicas" type="number" min="1" class="form-input" placeholder="1" />
              </div>
            </div>
            <!-- The Swarm scheduler ignores server_id, so only a container can pin a node. -->
            <LocationPicker v-model="form.location" v-model:server-id="form.server_id" :allow-pin="!isService" />
            <PlacementPicker v-if="isService" v-model="form.placement_constraints" :replicas="form.replicas" :location="form.location" />
            <p v-if="isService" class="form-hint">
              Runs as a Swarm service on the workspace overlay network with {{ Math.max(1, form.replicas) }} replica(s).
            </p>
            <div class="form-group">
              <label class="form-label">{{ $t('apps.col.source') }}</label>
              <div class="tabs" style="margin-bottom: 0">
                <button type="button" class="tab" :class="{ active: form.source_type === 'image' }" @click="form.source_type = 'image'">{{ $t('apps.form.sourceImage') }}</button>
                <button type="button" class="tab" :class="{ active: form.source_type === 'git' }" @click="form.source_type = 'git'">{{ $t('apps.form.sourceGit') }}</button>
              </div>
            </div>
            <template v-if="form.source_type === 'image'">
              <div class="form-row">
                <div class="form-group" style="flex: 2; margin-bottom: 0">
                  <label class="form-label">{{ $t('apps.form.image') }}</label>
                  <input v-model="form.image" class="form-input" placeholder="nginx" required />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">{{ $t('apps.form.tag') }} <span class="text-muted">{{ $t('apps.form.optional') }}</span></label>
                  <input v-model="form.tag" class="form-input" placeholder="latest" />
                </div>
              </div>
              <p class="form-hint">{{ $t('apps.form.deploys') }} <code>{{ form.image || 'image' }}:{{ form.tag || 'latest' }}</code></p>
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.registryCredential') }} <span class="text-muted">{{ $t('apps.form.forPrivate') }}</span></label>
                <select v-model="form.registry_id" class="form-select">
                  <option :value="null">{{ $t('apps.form.registryNone') }}</option>
                  <option v-for="r in registries" :key="r.id" :value="r.id">{{ r.name }} ({{ r.server }})</option>
                </select>
                <p v-if="registries.length === 0" class="form-hint">
                  <i18n-t keypath="apps.form.noRegistries" tag="span"><template #link><RouterLink to="/registries">{{ $t('nav.sources.registries') }}</RouterLink></template></i18n-t>
                </p>
              </div>
            </template>
            <template v-else>
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.repository') }}</label>
                <select v-model="form.git_repository_id" class="form-select" @change="onGitRepoSelect">
                  <option :value="null">{{ $t('apps.form.publicUrl') }}</option>
                  <option v-for="r in gitRepos" :key="r.id" :value="r.id">{{ r.name }} — {{ r.url }}</option>
                </select>
                <p class="form-hint">
                  <template v-if="form.git_repository_id">{{ $t('apps.form.usesSaved') }}</template>
                  <template v-else>{{ $t('apps.form.selectSaved') }}</template>
                  <RouterLink to="/git-repositories">{{ $t('apps.form.manageRepos') }}</RouterLink>
                </p>
              </div>
              <div class="form-group">
                <label class="form-label">
                  {{ $t('apps.form.repositoryUrl') }}
                  <span v-if="form.git_repository_id" class="text-muted">{{ $t('apps.form.optionalOverrides') }}</span>
                </label>
                <input v-model="form.git_repo" class="form-input" placeholder="https://github.com/user/repo" :required="!form.git_repository_id" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.branch') }} <span class="text-muted">{{ $t('apps.form.optional') }}</span></label>
                <input v-model="form.git_ref" class="form-input" placeholder="main" />
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.buildMethod') }}</label>
                <select v-model="form.build_method" class="form-select">
                  <option value="auto">{{ $t('apps.form.buildAuto') }}</option>
                  <option value="buildpack">{{ $t('apps.form.buildBuildpacks') }}</option>
                  <option value="dockerfile">{{ $t('apps.form.buildDockerfile') }}</option>
                </select>
                <p class="form-hint">
                  {{ $t('apps.form.buildAutoHint') }}
                </p>
              </div>
              <div v-if="form.build_method !== 'dockerfile'" class="form-group">
                <label class="form-label">{{ $t('apps.form.builderImage') }} <span class="text-muted">{{ $t('apps.form.optionalAdvanced') }}</span></label>
                <input v-model="form.builder" class="form-input" placeholder="paketobuildpacks/builder-jammy-base" />
                <p class="form-hint">{{ $t('apps.form.builderHint') }}</p>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.repoContents') }}</label>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="!canInspect || inspecting" @click="inspectRepo">
                  <span class="mdi" :class="inspecting ? 'mdi-loading mdi-spin' : 'mdi-magnify'"></span>
                  {{ inspecting ? 'Reading repository…' : 'Check repository' }}
                </button>
                <p class="form-hint">
                  <i18n-t keypath="apps.form.inspectHint" tag="span"><template #file><code>.miabi/pipeline.yaml</code></template></i18n-t>
                </p>

                <p v-if="inspectError" class="form-hint text-danger">
                  <span class="mdi mdi-alert-circle-outline"></span> {{ inspectError }}
                </p>

                <div v-else-if="inspected" class="inspect-result">
                  <!-- Found a usable pipeline: offer to adopt it. -->
                  <template v-if="inspected.has_pipeline">
                    <label class="inspect-toggle">
                      <input v-model="form.use_pipeline" type="checkbox" />
                      <span>
                        <strong>Use the pipeline from {{ inspected.pipeline_path }}</strong>
                        <span class="text-muted"> — runs {{ pipelineTriggerLabel }}</span>
                      </span>
                    </label>
                    <ol class="inspect-steps">
                      <li v-for="s in inspected.steps" :key="s.name">
                        <span class="step-name">{{ s.name }}</span>
                        <span class="text-muted">{{ s.uses ? `built-in: ${s.uses}` : s.image }}</span>
                        <span v-if="s.continue_on_error" class="badge badge-neutral">{{ $t('apps.form.continueOnError') }}</span>
                      </li>
                    </ol>
                    <p v-if="form.use_pipeline" class="form-hint">
                      {{ $t('apps.form.pipelineAdopted') }}
                    </p>
                    <p v-else class="form-hint">
                      {{ $t('apps.form.pipelineSkipped') }}
                    </p>
                  </template>

                  <!-- A pipeline file exists but is broken: say so rather than silently building. -->
                  <p v-else-if="inspected.pipeline_error" class="form-hint text-danger">
                    <span class="mdi mdi-alert-circle-outline"></span>
                    <i18n-t keypath="apps.form.pipelineInvalid" tag="span">
                      <template #file><code>{{ inspected.pipeline_path }}</code></template>
                      <template #error>{{ inspected.pipeline_error }}</template>
                    </i18n-t>
                  </p>

                  <p v-else class="form-hint">
                    <span class="mdi mdi-check"></span>
                    No pipeline file — this app will build
                    {{ inspected.has_dockerfile ? 'from its Dockerfile' : 'with Cloud Native Buildpacks' }}
                    and deploy.
                  </p>
                </div>
              </div>
            </template>
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.containerPorts') }}</label>
              <div v-for="(p, i) in form.ports" :key="i" class="port-row">
                <input v-model.number="p.container_port" type="number" class="form-input" :aria-label="$t('apps.form.containerPort')" placeholder="8080" style="flex: 1" />
                <select v-model="p.protocol" class="form-select" :aria-label="$t('apps.form.portProtocol')" style="width: 84px">
                  <option value="tcp">TCP</option>
                  <option value="udp">UDP</option>
                </select>
                <select v-model="p.scheme" class="form-select" :title="$t('apps.form.appProtocol')" :aria-label="$t('apps.form.appProtocol')" style="width: 96px">
                  <option value="http">http</option>
                  <option value="https">https</option>
                </select>
                <input v-model="p.name" class="form-input" :aria-label="$t('apps.form.portName')" :placeholder="$t('apps.form.portNamePlaceholder')" style="flex: 1" />
                <button type="button" class="btn-icon btn-icon-danger" :aria-label="$t('apps.form.removePort')" @click="removePort(i)"><span class="mdi mdi-close"></span></button>
              </div>
              <button type="button" class="btn btn-ghost btn-sm" @click="addPort"><span class="mdi mdi-plus"></span> {{ $t('apps.form.addPort') }}</button>
            </div>
            <div v-if="networks.length" class="form-group">
              <label class="form-label">{{ $t('nav.networking.networks') }} <span class="text-muted">{{ $t('apps.form.defaultAttached') }}</span></label>
              <label v-for="n in networks" :key="n.id" class="checkbox-label">
                <input type="checkbox" :value="n.id" v-model="form.network_ids" :disabled="n.is_default" />
                {{ n.name }} <span v-if="n.is_default" class="text-muted">{{ $t('apps.form.isDefault') }}</span>
              </label>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('dashboard.col.stack') }} <span class="text-muted">{{ $t('apps.form.optional') }}</span></label>
              <select v-model="form.stack_id" class="form-select">
                <option :value="null">{{ $t('apps.form.none') }}</option>
                <option v-for="s in stacks" :key="s.id" :value="s.id">{{ s.name }}</option>
              </select>
              <p v-if="stacks.length === 0" class="form-hint">
                <i18n-t keypath="apps.form.noStacks" tag="span"><template #link><RouterLink to="/stacks">{{ $t('nav.deploy.stacks') }}</RouterLink></template></i18n-t>
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creating">{{ creating ? 'Creating…' : 'Create application' }}</button>
          </div>
        </form>
      </AppModal>
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
.text-danger { color: var(--danger, #dc2626); }
.inspect-result { margin-top: 10px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-secondary); }
.inspect-toggle { display: flex; gap: 8px; align-items: flex-start; font-size: 13px; cursor: pointer; }
.inspect-toggle input { margin-top: 3px; flex-shrink: 0; }
.inspect-steps { margin: 10px 0 0; padding-left: 20px; font-size: 12px; display: flex; flex-direction: column; gap: 4px; }
.inspect-steps li { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.inspect-steps .step-name { font-weight: 600; }
</style>
