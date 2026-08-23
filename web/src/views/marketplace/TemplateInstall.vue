<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { marketplaceApi } from '@/api/marketplace'
import type { CatalogEntry, TemplateManifest, ManifestConfig, ManifestDatabase, InstallJob, TemplateInstallView, UpgradePlan } from '@/api/marketplace'
import { databaseApi } from '@/api/resources'
import { apiErrorMessage } from '@/api/client'
import type { DatabaseInstance } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const slug = computed(() => String(route.params.slug))
const version = computed(() => (route.query.version ? String(route.query.version) : ''))

const loading = ref(false)
const installing = ref(false)
const entry = ref<CatalogEntry | null>(null)
const manifest = ref<TemplateManifest | null>(null)
const instances = ref<DatabaseInstance[]>([])

// Each placement value is a select token: 'auto' | 'dedicated' | '<instanceId>'.
const form = ref<{ name: string; inputs: Record<string, string>; placement: Record<string, string> }>({
  name: '',
  inputs: {},
  placement: {},
})

// defaultPlacement seeds the selector from the template's declared placement, so
// a template that asks for a dedicated instance defaults to "Dedicated" (the user
// can still switch to Automatic or an existing instance).
function defaultPlacement(db: ManifestDatabase): string {
  if (db.engine === 'redis' || db.placement === 'dedicated') return 'dedicated'
  if (db.placement === 'shared') {
    const existing = instancesFor(db.engine)
    return existing.length ? String(existing[0].id) : 'auto'
  }
  return 'auto'
}

function isMdi(icon?: string) {
  return !!icon && icon.startsWith('mdi-')
}
function fallbackIcon() {
  return entry.value?.db_only ? 'mdi-database-outline' : 'mdi-cube-outline'
}
function instancesFor(engine: string): DatabaseInstance[] {
  return instances.value.filter((i) => i.engine === engine && i.status === 'running')
}
function setBool(key: string, e: Event) {
  form.value.inputs[key] = (e.target as HTMLInputElement).checked ? 'true' : 'false'
}

async function loadDetail() {
  if (!ws.currentWorkspaceId) return
  loading.value = true
  try {
    const detail = (await marketplaceApi.template(ws.currentWorkspaceId, slug.value, version.value)).data.data
    entry.value = detail?.entry ?? null
    manifest.value = detail?.manifest ?? null
    // Seed the form, preserving any answers the user already typed.
    if (!form.value.name) form.value.name = entry.value?.display_name ?? ''
    for (const inp of manifest.value?.inputs ?? []) {
      if (form.value.inputs[inp.key] === undefined)
        form.value.inputs[inp.key] = inp.default ?? (inp.type === 'bool' ? 'false' : '')
    }
    for (const db of manifest.value?.databases ?? []) {
      if (form.value.placement[db.name] === undefined) form.value.placement[db.name] = defaultPlacement(db)
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

// ---- installs of this template (provenance + upgrade) ----
const installs = ref<TemplateInstallView[]>([])

async function loadInstalls() {
  if (!ws.currentWorkspaceId) return
  try {
    installs.value = (await marketplaceApi.installs(ws.currentWorkspaceId)).data.data ?? []
  } catch {
    installs.value = []
  }
}

// Installs of THIS template, matched by slug — the stable template identity (an
// install id is workspace-local and mutable). Drives the "In this workspace"
// card and the upgrade affordance.
const templateInstalls = computed(() => installs.value.filter((i) => i.template_name === slug.value))
// The applications those installs created, for the left-side card.
const appsUsing = computed(() => templateInstalls.value.flatMap((i) => i.apps ?? []))
// First install with a newer template version available — the upgrade target.
const upgradableInstall = computed(() => templateInstalls.value.find((i) => i.update_available) ?? null)

// ---- upgrade flow (preview the plan, then apply) ----
const upgradeTarget = ref<TemplateInstallView | null>(null)
const upgradePlan = ref<UpgradePlan | null>(null)
const upgradePlanErr = ref('')
const upgradeInputs = ref<Record<string, string>>({})
const upgrading = ref(false)

async function openUpgrade(i: TemplateInstallView) {
  if (!ws.currentWorkspaceId) return
  upgradeTarget.value = i
  upgradePlan.value = null
  upgradePlanErr.value = ''
  upgradeInputs.value = {}
  try {
    upgradePlan.value = (await marketplaceApi.upgradePlan(ws.currentWorkspaceId, i.id)).data.data
    for (const inp of upgradePlan.value.new_inputs ?? []) upgradeInputs.value[inp.key] = ''
  } catch (e) {
    upgradePlanErr.value = apiErrorMessage(e, 'Could not compute the upgrade plan')
  }
}

const upgradeMissingInput = computed(() =>
  (upgradePlan.value?.new_inputs ?? []).some((inp) => inp.required && !upgradeInputs.value[inp.key]?.trim()),
)

async function confirmUpgrade() {
  const i = upgradeTarget.value
  if (!i || !ws.currentWorkspaceId || !upgradePlan.value) return
  upgrading.value = true
  try {
    const res = (await marketplaceApi.upgrade(ws.currentWorkspaceId, i.id, upgradePlan.value.to_version, upgradeInputs.value)).data.data
    const bumped = res.apps_bumped?.length ?? 0
    notify.success(`Upgraded ${i.template_display_name} to ${res.to_version}${bumped ? ` · ${bumped} app(s) redeployed` : ''}${res.warnings?.length ? ` · ${res.warnings.length} item(s) need manual review` : ''}`)
    upgradeTarget.value = null
    await Promise.all([loadInstalls(), loadDetail()])
  } catch (e) {
    notify.apiError(e)
  } finally {
    upgrading.value = false
  }
}

function selectVersion(v: string) {
  router.replace({ query: { ...route.query, version: v } }) // triggers the watcher
}
function onVersionChange(e: Event) {
  selectVersion((e.target as HTMLSelectElement).value)
}

watch(version, loadDetail)

onMounted(async () => {
  // Load instances before the manifest so placement defaults (e.g. a "shared"
  // template) can pre-select an existing instance.
  if (ws.currentWorkspaceId) {
    try {
      instances.value = (await databaseApi.list(ws.currentWorkspaceId)).data.data ?? []
    } catch {
      instances.value = []
    }
  }
  await Promise.all([loadDetail(), loadInstalls()])
  // Deep link: marketplace/<slug>?upgrade opens the upgrade plan for this
  // template's install (used by the app detail "Upgrade via Marketplace" action
  // and the Marketplace Installed tab). No-op when nothing newer is available.
  if (route.query.upgrade !== undefined && upgradableInstall.value) {
    openUpgrade(upgradableInstall.value)
  }
})

const confirmOpen = ref(false)

// review validates required inputs client-side, then opens the confirmation
// modal (the server validates authoritatively on install).
function review() {
  if (!entry.value) return
  for (const inp of manifest.value?.inputs ?? []) {
    if (inp.required && !inp.generate && !form.value.inputs[inp.key]) {
      notify.error(`${inp.label || inp.key} is required`)
      return
    }
  }
  confirmOpen.value = true
}

// pickedInstance returns the instance a placement token points at, if any.
function pickedInstance(db: ManifestDatabase): DatabaseInstance | undefined {
  const v = form.value.placement[db.name]
  if (v === 'auto' || v === 'dedicated' || !v) return undefined
  return instances.value.find((i) => String(i.id) === v)
}

// placementLabel resolves, for the confirmation summary, what the chosen mode
// will actually do — matching the backend's reuse-or-create rule.
function placementLabel(db: ManifestDatabase): string {
  const v = form.value.placement[db.name]
  if (v === 'dedicated') return 'New dedicated instance'
  const inst = pickedInstance(db)
  if (inst) return `Use existing: ${inst.name}`
  const existing = instancesFor(db.engine)
  return existing.length ? `Automatic — reuse ${existing[0].name}` : 'Automatic — new instance'
}

// placementHint surfaces, inline under the selector, what the current choice will
// do — whether Automatic will reuse an existing instance or provision a new one.
function placementHint(db: ManifestDatabase): string {
  if (db.engine === 'redis') return 'Redis is always provisioned as a dedicated instance.'
  const v = form.value.placement[db.name]
  if (v === 'dedicated') return 'A new dedicated instance will be provisioned.'
  const inst = pickedInstance(db)
  if (inst) return `A new database will be created in “${inst.name}”.`
  const existing = instancesFor(db.engine)
  return existing.length
    ? `♻ Automatic will reuse “${existing[0].name}” — no new container.`
    : `Automatic will provision a new dedicated ${db.engine} instance.`
}

function volumeNames(): string {
  return (manifest.value?.volumes ?? []).map((v: { name: string }) => v.name).join(', ')
}
// A config is named for the install (<template>-<name>) and carries files; both
// matter before installing, so the label shows the name and how many files.
function configLabel(c: ManifestConfig): string {
  const n = Object.keys(c.files ?? {}).length
  return `${c.name} — ${n} file${n === 1 ? '' : 's'}`
}

// ---- custom-template edit / delete (custom source only) ----
const isCustom = computed(() => entry.value?.source === 'custom')

const editOpen = ref(false)
const editYaml = ref('')
const editLoading = ref(false)
const saving = ref(false)

async function openEdit() {
  if (!entry.value || !ws.currentWorkspaceId) return
  editOpen.value = true
  editLoading.value = true
  editYaml.value = ''
  try {
    const raw = (await marketplaceApi.templateRaw(ws.currentWorkspaceId, entry.value.name, version.value || entry.value.version)).data.data
    editYaml.value = raw?.yaml ?? ''
  } catch (e) {
    notify.apiError(e)
    editOpen.value = false
  } finally {
    editLoading.value = false
  }
}

async function saveEdit() {
  if (!entry.value || !ws.currentWorkspaceId || !editYaml.value.trim()) return
  saving.value = true
  try {
    await marketplaceApi.updateTemplate(ws.currentWorkspaceId, entry.value.name, editYaml.value)
    notify.success('Template updated')
    editOpen.value = false
    await loadDetail() // refresh metadata / provisions / inputs
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const deleteOpen = ref(false)
const deleting = ref(false)

async function confirmDelete() {
  if (!entry.value || !ws.currentWorkspaceId) return
  deleting.value = true
  try {
    await marketplaceApi.deleteTemplate(ws.currentWorkspaceId, entry.value.name)
    notify.success(`Deleted ${entry.value.display_name}`)
    router.push({ name: 'marketplace' })
  } catch (e) {
    notify.apiError(e)
    deleting.value = false
  }
}

// --- Async install with live progress (SSE) --------------------------------
// The install runs server-side as a job; we stream its phases and only navigate
// to the app/stack once it is live, rather than landing on an empty shell.
const installJob = ref<InstallJob | null>(null)
let jobES: EventSource | null = null

function stopJobStream() {
  jobES?.close()
  jobES = null
}

// Icon + state per stepper row.
function stepIcon(status: string): string {
  switch (status) {
    case 'done': return 'mdi-check-circle'
    case 'error': return 'mdi-alert-circle'
    case 'active': return 'mdi-loading mdi-spin'
    default: return 'mdi-circle-outline'
  }
}

async function install() {
  const e = entry.value
  if (!e || !ws.currentWorkspaceId) return
  installing.value = true
  try {
    // Decode each placement token into an instance pin or a mode override.
    const placements: Record<string, number> = {}
    const placement_modes: Record<string, string> = {}
    for (const [k, v] of Object.entries(form.value.placement)) {
      if (v === 'auto' || v === 'dedicated') placement_modes[k] = v
      else {
        const id = Number(v)
        if (id) placements[k] = id
      }
    }
    const job = (
      await marketplaceApi.startInstall(ws.currentWorkspaceId, {
        name: e.name,
        // Install the version the user selected (route query), not the entry's
        // latest. Empty falls back to latest, which the backend resolves.
        version: version.value || e.version,
        display_name: form.value.name || undefined,
        inputs: form.value.inputs,
        placements,
        placement_modes,
      })
    ).data.data
    installJob.value = job // switches the modal to the progress view
    if (job.status !== 'running') onJobDone(job)
    else startJobStream(job.id)
  } catch (err) {
    notify.apiError(err)
    installing.value = false
    confirmOpen.value = false // return to the form so the user can adjust
  }
}

function startJobStream(id: string) {
  if (!ws.currentWorkspaceId) return
  stopJobStream()
  jobES = new EventSource(marketplaceApi.installJobEventsUrl(ws.currentWorkspaceId, id))
  jobES.onmessage = (ev) => {
    let msg: { type?: string; data?: InstallJob }
    try { msg = JSON.parse(ev.data) } catch { return } // ignore keep-alives
    if (msg.type === 'job' && msg.data) {
      installJob.value = msg.data
      if (msg.data.status !== 'running') {
        stopJobStream()
        onJobDone(msg.data)
      }
    }
  }
  // EventSource auto-reconnects on transient errors; the server re-sends the
  // current snapshot on reconnect, so nothing to do here.
}

// onJobDone navigates on success or surfaces the failure (the modal stays open
// so the user can read the error and go back to adjust the form).
function onJobDone(job: InstallJob) {
  if (job.status === 'succeeded') {
    const r = job.result
    notify.success(`Installed ${form.value.name || entry.value?.display_name}`)
    if (r?.stack?.id) router.push({ name: 'stack-detail', params: { id: r.stack.id } })
    else if (r?.apps?.length === 1) router.push({ name: 'app-detail', params: { id: r.apps[0].id } })
    else router.push({ name: 'marketplace' })
  } else {
    installing.value = false
    notify.error(job.error || 'Install failed')
  }
}

// closeProgress dismisses the progress view after a failure, returning to the form.
function closeProgress() {
  stopJobStream()
  installJob.value = null
  installing.value = false
  confirmOpen.value = false
}

onUnmounted(stopJobStream)
</script>

<template>
  <div>
    <div class="page-header">
      <div class="header-left">
        <button class="btn-icon btn-icon-muted" title="Back to marketplace" aria-label="Back to marketplace" @click="router.push({ name: 'marketplace' })">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <h1>{{ entry ? entry.display_name : 'Install template' }}</h1>
      </div>
      <div v-if="entry && isCustom && ws.canEdit" class="header-actions">
        <button class="btn btn-secondary" @click="openEdit">
          <span class="mdi mdi-pencil-outline"></span> Edit
        </button>
        <button class="btn btn-danger" @click="deleteOpen = true">
          <span class="mdi mdi-delete-outline"></span> Delete
        </button>
      </div>
    </div>

    <div v-if="loading && !entry" class="loading-page"><span class="spinner"></span></div>

    <div v-else-if="!entry" class="card">
      <div class="empty-state">
        <span class="mdi mdi-help-circle-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>Template not found</h3>
        <p>It may have been removed. <a href="#" @click.prevent="router.push({ name: 'marketplace' })">Back to marketplace</a></p>
      </div>
    </div>

    <div v-else class="install-layout">
      <!-- Left column: what this installs + what's already installed -->
      <div class="install-side">
      <aside class="card info-card">
        <div class="card-body">
          <div class="info-head">
            <span class="template-icon">
              <img v-if="entry.icon && !isMdi(entry.icon)" :src="entry.icon" :alt="entry.display_name" />
              <span v-else class="mdi" :class="isMdi(entry.icon) ? entry.icon : fallbackIcon()"></span>
            </span>
            <div>
              <span class="badge" :class="entry.source === 'custom' ? 'badge-warning' : 'badge-success'">
                {{ entry.source === 'custom' ? 'Custom' : 'Official' }}
              </span>
              <span class="badge badge-neutral" style="margin-left: 6px">{{ entry.category }}</span>
            </div>
          </div>
          <p class="info-desc">{{ entry.description }}</p>

          <div class="info-section">
            <div class="info-label">Version</div>
            <select v-if="entry.versions.length > 1" class="form-select" :value="version || entry.version" @change="onVersionChange" aria-label="Version">
              <option v-for="v in entry.versions" :key="v" :value="v">v{{ v }}</option>
            </select>
            <div v-else class="info-value">v{{ version || entry.version }}</div>
          </div>

          <div class="info-section">
            <div class="info-label">Provisions</div>
            <ul class="provision-list">
              <li v-for="a in manifest?.applications ?? []" :key="'a' + a.name">
                <span class="mdi mdi-cube-outline"></span> {{ a.name }} <span class="cell-sub">({{ a.image }}{{ a.tag ? ':' + a.tag : '' }})</span>
              </li>
              <li v-for="d in manifest?.databases ?? []" :key="'d' + d.name">
                <span class="mdi mdi-database-outline"></span> {{ d.engine }} database <span class="cell-sub">({{ d.name }})</span>
              </li>
              <li v-for="vol in manifest?.volumes ?? []" :key="'v' + vol.name">
                <span class="mdi mdi-harddisk"></span> volume <span class="cell-sub">({{ vol.name }})</span>
              </li>
              <li v-for="cfg in manifest?.configs ?? []" :key="'c' + cfg.name">
                <span class="mdi mdi-file-document-outline"></span> config <span class="cell-sub">({{ configLabel(cfg) }})</span>
              </li>
            </ul>
          </div>

          <div v-if="entry.author" class="info-section">
            <div class="info-label">Author</div>
            <div class="info-value">{{ entry.author.name }}</div>
            <div class="author-links">
              <a v-if="entry.author.website" :href="entry.author.website" target="_blank" rel="noopener" class="info-link">
                <span class="mdi mdi-web"></span> Website
              </a>
              <a v-if="entry.author.email" :href="`mailto:${entry.author.email}`" class="info-link">
                <span class="mdi mdi-email-outline"></span> Email
              </a>
            </div>
          </div>

          <a v-if="entry.homepage" :href="entry.homepage" target="_blank" rel="noopener" class="info-link">
            <span class="mdi mdi-open-in-new"></span> Homepage
          </a>
        </div>
      </aside>

      <!-- Applications already using this template (matched by slug). -->
      <section v-if="appsUsing.length" class="card apps-card">
        <div class="card-body">
          <div class="apps-head">
            <h3 class="form-title" style="margin-bottom: 0">In this workspace</h3>
            <span class="badge badge-neutral">{{ appsUsing.length }}</span>
          </div>
          <p class="field-help" style="margin: 4px 0 12px">Applications using this template.</p>
          <ul class="apps-list">
            <li v-for="a in appsUsing" :key="a.id">
              <router-link :to="{ name: 'app-detail', params: { id: a.id } }" class="app-link">
                <span class="status-dot" :class="`status-${a.status}`"></span>{{ a.display_name || a.name }}
              </router-link>
            </li>
          </ul>
          <div v-if="upgradableInstall" class="apps-upgrade">
            <span class="badge badge-primary">v{{ upgradableInstall.latest_version }} available</span>
            <button v-if="ws.canEdit" class="btn btn-sm btn-primary" @click="openUpgrade(upgradableInstall)">
              <span class="mdi mdi-arrow-up-bold-circle-outline"></span> Upgrade
            </button>
          </div>
          <p v-else class="field-help" style="margin-top: 10px">
            <span class="mdi mdi-check-circle-outline" style="color: var(--success-500)"></span> Up to date
          </p>
        </div>
      </section>
      </div>

      <!-- Right: install form -->
      <section class="card form-card">
        <div class="card-body">
          <h3 class="form-title">Configure & install</h3>
          <form @submit.prevent="review">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" :placeholder="entry.display_name" aria-label="Name" />
              <p class="field-help">Display name for the installed {{ entry.applications > 1 ? 'apps' : 'app' }}.</p>
            </div>

            <div v-for="inp in manifest?.inputs ?? []" :key="inp.key" class="form-group">
              <label class="form-label">
                {{ inp.label || inp.key }}
                <span v-if="inp.required && !inp.generate" style="color: var(--danger-500)">*</span>
              </label>
              <select v-if="inp.type === 'select'" v-model="form.inputs[inp.key]" class="form-select" :aria-label="inp.label || inp.key">
                <option v-for="o in inp.options ?? []" :key="o" :value="o">{{ o }}</option>
              </select>
              <label v-else-if="inp.type === 'bool'" class="checkbox-row">
                <input type="checkbox" :checked="form.inputs[inp.key] === 'true'" @change="setBool(inp.key, $event)" />
                <span class="text-sm">Enabled</span>
              </label>
              <input
                v-else
                v-model="form.inputs[inp.key]"
                class="form-input"
                :type="inp.type === 'password' ? 'password' : inp.type === 'number' ? 'number' : 'text'"
                :placeholder="inp.placeholder || (inp.generate ? 'Auto-generated if left blank' : '')"
                :aria-label="inp.label || inp.key"
              />
              <p v-if="inp.help" class="field-help">{{ inp.help }}</p>
            </div>

            <div v-for="db in manifest?.databases ?? []" :key="db.name" class="form-group">
              <label class="form-label">{{ db.engine }} database ({{ db.name }})</label>
              <select v-model="form.placement[db.name]" class="form-select" :disabled="db.engine === 'redis'" :aria-label="`${db.engine} database (${db.name})`">
                <option v-if="db.engine !== 'redis'" value="auto">Automatic (reuse or create)</option>
                <option value="dedicated">New dedicated instance</option>
                <option v-for="inst in instancesFor(db.engine)" :key="inst.id" :value="String(inst.id)">
                  Use existing: {{ inst.name }}
                </option>
              </select>
              <p class="field-help">{{ placementHint(db) }}</p>
            </div>

            <div class="form-actions">
              <button type="button" class="btn btn-secondary" @click="router.push({ name: 'marketplace' })">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="installing || !ws.canEdit">Install…</button>
            </div>
          </form>
        </div>
      </section>
    </div>

    <!-- CONFIRMATION / LIVE PROGRESS -->
    <Teleport to="body">
      <!-- Review (before install) -->
      <AppModal v-if="(confirmOpen && entry) && (!installJob)" @close="confirmOpen = false">
        <div class="modal-header">
          <h3>Install {{ form.name || entry.display_name }}?</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="confirmOpen = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p class="text-muted text-sm" style="margin-bottom: 14px">
            This will create the following in <strong>{{ ws.contextLabel }}</strong>:
          </p>
          <dl class="review">
            <div class="review-row">
              <dt>Template</dt>
              <dd>{{ entry.display_name }} <span class="cell-sub">v{{ version || entry.version }}</span></dd>
            </div>
            <div v-if="(manifest?.applications ?? []).length" class="review-row">
              <dt>{{ (manifest?.applications ?? []).length > 1 ? 'Applications' : 'Application' }}</dt>
              <dd>
                <div v-for="a in manifest?.applications ?? []" :key="a.name">
                  {{ a.image }}{{ a.tag ? ':' + a.tag : '' }}
                </div>
              </dd>
            </div>
            <div v-if="(manifest?.databases ?? []).length" class="review-row">
              <dt>Databases</dt>
              <dd>
                <div v-for="d in manifest?.databases ?? []" :key="d.name">
                  {{ d.engine }} <span class="cell-sub">— {{ placementLabel(d) }}</span>
                </div>
              </dd>
            </div>
            <div v-if="(manifest?.volumes ?? []).length" class="review-row">
              <dt>Volumes</dt>
              <dd>{{ volumeNames() }}</dd>
            </div>
            <div v-if="(manifest?.configs ?? []).length" class="review-row">
              <dt>Configs</dt>
              <dd>
                <div v-for="c in manifest?.configs ?? []" :key="c.name">
                  {{ configLabel(c) }}
                </div>
              </dd>
            </div>
          </dl>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="confirmOpen = false" :disabled="installing">Back</button>
          <button type="button" class="btn btn-primary" :disabled="installing" @click="install">
            {{ installing ? 'Installing…' : 'Confirm & install' }}
          </button>
        </div>
      </AppModal>
      <!-- Live progress (during install) -->
      <AppModal v-else-if="(confirmOpen && entry) && installJob" @close="confirmOpen = false">
        <div class="modal-header">
          <h3>{{ installJob.status === 'failed' ? 'Install failed' : `Installing ${form.name || entry.display_name}` }}</h3>
          <button v-if="installJob.status !== 'running'" class="btn-icon btn-icon-muted" aria-label="Close" @click="closeProgress">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <div class="modal-body">
          <ul class="install-steps">
            <li v-for="p in installJob.phases" :key="p.key" class="install-step" :class="`is-${p.status}`">
              <span class="mdi step-icon" :class="stepIcon(p.status)"></span>
              <span class="step-label">{{ p.label }}</span>
            </li>
          </ul>
          <p v-if="installJob.status === 'running' && installJob.message" class="install-msg">
            {{ installJob.message }}
          </p>
          <div v-else-if="installJob.status === 'failed'" class="danger-note" style="margin-top: 14px">
            <span class="mdi mdi-alert-outline"></span>
            <div>{{ installJob.error || 'The install could not be completed.' }}</div>
          </div>
        </div>
        <div v-if="installJob.status === 'failed'" class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="closeProgress">Back to form</button>
        </div>
      </AppModal>
    </Teleport>

    <!-- EDIT (custom templates) -->
    <Teleport to="body">
      <AppModal v-if="editOpen" @close="editOpen = false">
        <div class="modal-header">
          <h3>Edit template</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="editOpen = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="saveEdit">
          <div class="modal-body">
            <p class="text-muted text-sm" style="margin-bottom: 12px">
              Edit the manifest for this custom template. It is re-validated on save. The
              <code>name</code> cannot be changed.
            </p>
            <div v-if="editLoading" class="loading-page"><span class="spinner"></span></div>
            <textarea v-else v-model="editYaml" class="form-input mono" rows="16" spellcheck="false"></textarea>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="editOpen = false" :disabled="saving">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || editLoading || !editYaml.trim()">
              {{ saving ? 'Saving…' : 'Save changes' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- DELETE CONFIRMATION (custom templates) -->
    <Teleport to="body">
      <AppModal v-if="deleteOpen && entry" dialog-class="modal-sm" @close="deleteOpen = false">
        <div class="modal-header">
          <h3>Delete {{ entry.display_name }}?</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="deleteOpen = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div class="danger-note">
            <span class="mdi mdi-alert-outline"></span>
            <div>
              This removes the custom template from this workspace. Existing installs are not affected. This cannot
              be undone.
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="deleteOpen = false" :disabled="deleting">Cancel</button>
          <button type="button" class="btn btn-danger" :disabled="deleting" @click="confirmDelete">
            {{ deleting ? 'Deleting…' : 'Delete template' }}
          </button>
        </div>
      </AppModal>
    </Teleport>

    <!-- UPGRADE PREVIEW -->
    <Teleport to="body">
      <AppModal v-if="upgradeTarget" @close="upgradeTarget = null">
        <div class="modal-header">
          <h3>Upgrade {{ upgradeTarget.template_display_name }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="upgradeTarget = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-if="upgradePlanErr" class="danger-note"><span class="mdi mdi-alert-outline"></span><div>{{ upgradePlanErr }}</div></div>
          <div v-else-if="!upgradePlan" class="text-muted text-sm">Computing changes…</div>
          <template v-else>
            <p class="text-sm" style="margin-bottom: 12px">
              <code>v{{ upgradePlan.from_version }}</code> → <code>v{{ upgradePlan.to_version }}</code>
            </p>

            <div v-for="a in upgradePlan.apps" :key="a.name" class="upg-app">
              <div class="upg-app-name"><span class="mdi mdi-cube-outline"></span> {{ a.name }}</div>
              <ul class="upg-list">
                <li v-if="a.image_changed">image <code>{{ a.old_image }}</code> → <code>{{ a.new_image }}</code></li>
                <li v-for="e in a.env" :key="e.key">
                  env <code>{{ e.key }}</code>
                  <span class="badge" :class="{ 'badge-success': e.kind === 'added', 'badge-warning': e.kind === 'changed', 'badge-neutral': e.kind === 'removed' }">{{ e.kind }}</span>
                  <span v-if="e.secret" class="text-muted">· secret</span>
                </li>
                <li v-for="m in a.new_mounts" :key="m">new mount <code>{{ m }}</code></li>
                <li v-if="!a.image_changed && !a.env?.length && !a.new_mounts?.length" class="text-muted">no changes</li>
              </ul>
            </div>

            <p v-if="upgradePlan.new_volumes?.length" class="text-sm">New volumes: <code v-for="v in upgradePlan.new_volumes" :key="v" style="margin-right: 4px">{{ v }}</code></p>
            <p v-if="upgradePlan.new_configs?.length" class="text-sm">New configs: <code v-for="c in upgradePlan.new_configs" :key="c" style="margin-right: 4px">{{ c }}</code></p>

            <div v-if="upgradePlan.new_inputs?.length" class="upg-inputs">
              <p class="text-sm" style="font-weight: 600">New settings to provide</p>
              <div v-for="inp in upgradePlan.new_inputs" :key="inp.key" class="form-group" style="margin-bottom: 8px">
                <label class="form-label">{{ inp.label || inp.key }}<span v-if="inp.required" style="color: var(--danger-600)"> *</span></label>
                <input v-model="upgradeInputs[inp.key]" class="form-input" :placeholder="inp.help || ''" :aria-label="inp.label || inp.key" />
              </div>
            </div>

            <div v-if="upgradePlan.warnings?.length" class="danger-note" style="margin-top: 12px">
              <span class="mdi mdi-alert-outline"></span>
              <div>
                <strong>Applied manually:</strong>
                <ul class="upg-list">
                  <li v-for="(wn, idx) in upgradePlan.warnings" :key="idx">{{ wn }}</li>
                </ul>
              </div>
            </div>
          </template>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" :disabled="upgrading" @click="upgradeTarget = null">Cancel</button>
          <button type="button" class="btn btn-primary" :disabled="!upgradePlan || upgrading || upgradeMissingInput" @click="confirmUpgrade">
            {{ upgrading ? 'Upgrading…' : `Upgrade to v${upgradePlan?.to_version ?? ''}` }}
          </button>
        </div>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.header-left {
  display: flex;
  align-items: center;
  gap: 10px;
}
.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.danger-note {
  display: flex;
  gap: 10px;
  font-size: 13px;
  color: var(--text-primary);
  background: var(--danger-50, rgba(220, 38, 38, 0.06));
  border: 1px solid var(--danger-200, rgba(220, 38, 38, 0.2));
  border-radius: var(--radius);
  padding: 12px;
}
.danger-note .mdi {
  color: var(--danger-500);
  font-size: 20px;
  flex-shrink: 0;
}
.install-layout {
  display: grid;
  grid-template-columns: 320px 1fr;
  gap: 16px;
  align-items: start;
}
.info-head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
.template-icon {
  width: 44px;
  height: 44px;
  border-radius: var(--radius);
  background: var(--primary-50);
  color: var(--primary-600);
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  flex-shrink: 0;
}
.template-icon img {
  width: 28px;
  height: 28px;
  object-fit: contain;
}
.template-icon .mdi {
  font-size: 24px;
}
.info-desc {
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 16px;
}
.info-section {
  margin-bottom: 16px;
}
.info-label {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
  margin-bottom: 6px;
}
.info-value {
  font-size: 14px;
  color: var(--text-primary);
}
.provision-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.provision-list li {
  font-size: 13px;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  gap: 6px;
}
.provision-list .mdi {
  color: var(--text-muted);
}
.info-link {
  font-size: 13px;
  color: var(--primary-600);
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.author-links {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 6px;
}
.form-title {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 16px;
}
.field-help {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 4px;
}
.checkbox-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 20px;
}
.review {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.review-row {
  display: grid;
  grid-template-columns: 110px 1fr;
  gap: 12px;
  align-items: start;
}
.review-row dt {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--text-muted);
}
.review-row dd {
  margin: 0;
  font-size: 13px;
  color: var(--text-primary);
}
@media (max-width: 720px) {
  .install-layout {
    grid-template-columns: 1fr;
  }
}

/* --- Live install progress stepper --- */
.install-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.install-step {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: var(--radius);
  font-size: 14px;
  color: var(--text-muted);
  transition: background 0.15s, color 0.15s;
}
.install-step .step-icon {
  font-size: 18px;
  flex-shrink: 0;
  color: var(--text-muted);
}
.install-step.is-active {
  background: var(--primary-50);
  color: var(--text-primary);
  font-weight: 600;
}
.install-step.is-active .step-icon {
  color: var(--primary-600);
}
.install-step.is-done {
  color: var(--text-primary);
}
.install-step.is-done .step-icon {
  color: var(--success-500, #16a34a);
}
.install-step.is-error .step-icon {
  color: var(--danger-500);
}
.install-msg {
  margin-top: 12px;
  font-size: 13px;
  color: var(--text-muted);
  text-align: center;
}
.mdi-spin {
  animation: mdi-spin 0.9s linear infinite;
}
@keyframes mdi-spin {
  to {
    transform: rotate(360deg);
  }
}

/* Left column: stacks the info card and the "in this workspace" apps card. */
.install-side {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.apps-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.apps-list {
  list-style: none;
  margin: 0 0 4px;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.app-link {
  font-size: 13px;
  color: var(--primary-600);
  display: inline-flex;
  align-items: center;
  gap: 6px;
  width: fit-content;
}
.app-link:hover {
  text-decoration: underline;
}
.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--text-muted);
  flex-shrink: 0;
}
.status-running {
  background: var(--success-500, #16a34a);
}
.status-failed {
  background: var(--danger-500, #dc2626);
}
.status-deploying {
  background: var(--warning-500, #d97706);
}
.apps-upgrade {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border-primary);
}
/* Upgrade preview modal. */
.upg-app {
  padding: 8px 0;
  border-top: 1px solid var(--border-primary);
}
.upg-app:first-of-type {
  border-top: none;
}
.upg-app-name {
  font-weight: 600;
  font-size: 13px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.upg-list {
  margin: 4px 0 0;
  padding-left: 20px;
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.upg-list .badge {
  margin-left: 4px;
}
.upg-inputs {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border-primary);
}
</style>
