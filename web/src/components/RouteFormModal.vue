<script setup lang="ts">
// Create / edit one Goma route. Extracted from the routes page so an application
// can offer the same form in place: routing an app is something you decide while
// looking at the app, not a reason to leave the page and come back.
//
// It loads its own reference data (domains, middlewares, certificates, the
// selected app's ports), so a host only has to pass the workspace and the apps
// that may be chosen.
import { computed, nextTick, ref, watch } from 'vue'
import { useNotificationStore } from '@/stores/notification'
import { routeApi } from '@/api/routes'
import { middlewareApi } from '@/api/middlewares'
import { certificateApi } from '@/api/certificates'
import { appApi } from '@/api/apps'
import { domainApi } from '@/api/domains'
import { parseYaml, toYaml } from '@/utils/yaml'
import { useDirtyGuard } from '@/composables/useModal'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { Route, Middleware, Application, AppPort, RouteTLSMode, Certificate, Domain } from '@/api/types'

const props = defineProps<{
  open: boolean
  workspaceId: number | null
  // The route being edited; null creates a new one.
  editing: Route | null
  // Applications selectable in the form.
  apps: Application[]
  // Preselect an application when creating.
  presetAppId?: number | null
  // Fix the application (an app's own Routes tab) — it is shown, not chosen.
  lockApp?: boolean
}>()

const emit = defineEmits<{ close: []; saved: [] }>()

const confirmDiscard = ref(false)

const notify = useNotificationStore()

const middlewares = ref<Middleware[]>([])
const certificates = ref<Certificate[]>([])
const domains = ref<Domain[]>([])
const saving = ref(false)

interface RouteForm {
  name: string
  application_id: number | null
  path: string
  hosts: string
  methods: string[]
  middlewares: string[]
  rewrite: string
  target_port: number | undefined
  tls_mode: RouteTLSMode
  certificate_id: number | null
  enabled: boolean
}
function emptyForm(): RouteForm {
  return { name: '', application_id: null, path: '/', hosts: '', methods: [], middlewares: [], rewrite: '', target_port: undefined, tls_mode: 'acme', certificate_id: null, enabled: true }
}
const form = ref<RouteForm>(emptyForm())

const lockedAppName = computed(
  () => props.apps.find((a) => a.id === form.value.application_id)?.name ?? '',
)

// Host builder rows: each row is a registered domain + an optional subdomain.
// Empty subdomain serves the domain at its root (apex). extraHosts holds any
// host not under a registered domain (e.g. its domain was removed) so editing a
// route never silently drops it.
// Each row carries a stable id so v-for can key on identity, not array index —
// otherwise removing a middle row reuses the wrong input's DOM/state.
let hostRowSeq = 0
const hostRows = ref<{ id: number; sub: string; domain: string }[]>([])
const extraHosts = ref<string[]>([])

// Declared ports of the form's selected app, for the target-port dropdown. The
// apps list doesn't carry ports, so fetch the app detail on selection (cached).
const appPorts = ref<AppPort[]>([])
const portCache = new Map<number, AppPort[]>()
async function loadAppPorts(appId: number | null) {
  appPorts.value = []
  if (!appId || !props.workspaceId) return
  const cached = portCache.get(appId)
  if (cached) { appPorts.value = cached; return }
  try {
    const ports = (await appApi.get(props.workspaceId, appId)).data.data?.ports ?? []
    portCache.set(appId, ports)
    appPorts.value = ports
  } catch { /* leave empty → manual port input shown */ }
}
// User picked a different app: clear any chosen port and load the new app's ports.
function onAppChange() {
  form.value.target_port = undefined
  loadAppPorts(form.value.application_id)
}
// Whether the chosen target port speaks HTTPS — the backend is then reached over
// https and TLS verification is skipped for the internal address.
const selectedPortHttps = computed(() => {
  const tp = form.value.target_port
  if (!tp) return false
  return appPorts.value.some((p) => p.container_port === tp && (p.scheme || 'http') === 'https')
})

// HTTP methods offered as toggles; empty selection means "all methods".
const allMethods = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH', 'HEAD', 'OPTIONS']

// --- Simple / Advanced routing config ---
const formMode = ref<'simple' | 'advanced'>('simple')
const advancedConfig = ref('')
const yamlError = ref('')
// Fields the simple form can represent; anything else is "advanced".
const simpleKeys = new Set(['path', 'hosts', 'methods', 'middlewares', 'rewrite'])

function simpleToYaml(): string {
  const cfg: Record<string, unknown> = { path: form.value.path || '/' }
  const hosts = splitCsv(form.value.hosts)
  if (hosts.length) cfg.hosts = hosts
  if (form.value.methods.length) cfg.methods = [...form.value.methods]
  if (form.value.middlewares.length) cfg.middlewares = [...form.value.middlewares]
  if (form.value.rewrite) cfg.rewrite = form.value.rewrite
  return toYaml(cfg)
}
function yamlToSimple(text: string) {
  const cfg = parseYaml(text)
  form.value.path = (cfg.path as string) || '/'
  form.value.hosts = Array.isArray(cfg.hosts) ? (cfg.hosts as string[]).join(', ') : ''
  form.value.methods = Array.isArray(cfg.methods) ? (cfg.methods as string[]).map(String) : []
  form.value.middlewares = Array.isArray(cfg.middlewares) ? (cfg.middlewares as string[]).map(String) : []
  form.value.rewrite = (cfg.rewrite as string) || ''
}
function hasAdvancedKeys(cfg: Record<string, unknown>): boolean {
  return Object.keys(cfg).some((k) => !simpleKeys.has(k))
}
function switchMode(mode: 'simple' | 'advanced') {
  if (mode === formMode.value) return
  yamlError.value = ''
  if (mode === 'advanced') {
    advancedConfig.value = simpleToYaml()
  } else {
    try {
      const cfg = parseYaml(advancedConfig.value)
      if (hasAdvancedKeys(cfg)) yamlError.value = 'Advanced-only fields will be dropped in Simple mode.'
      yamlToSimple(advancedConfig.value)
      parseHostsToRows() // reflect YAML hosts into the builder
    } catch {
      yamlError.value = 'Could not parse the YAML into the simple form.'
      return
    }
  }
  formMode.value = mode
}

function splitCsv(s: string): string[] {
  return s.split(',').map((x) => x.trim()).filter(Boolean)
}

// --- Guided host builder (domain dropdown + subdomain) ---

// matchDomainName returns the most specific registered domain covering host.
function matchDomainName(host: string): string | null {
  const h = host.toLowerCase()
  let best: string | null = null
  for (const d of domains.value) {
    const name = d.name.toLowerCase()
    if (h === name || h.endsWith('.' + name)) {
      if (!best || name.length > best.length) best = name
    }
  }
  return best
}

// parseHostsToRows splits the canonical form.hosts string into builder rows; any
// host not under a registered domain is preserved verbatim in extraHosts.
function parseHostsToRows() {
  const rows: { id: number; sub: string; domain: string }[] = []
  const extra: string[] = []
  for (const host of splitCsv(form.value.hosts)) {
    const dom = matchDomainName(host)
    if (!dom) { extra.push(host); continue }
    const sub = host.toLowerCase() === dom ? '' : host.slice(0, host.length - dom.length - 1)
    rows.push({ id: hostRowSeq++, sub, domain: dom })
  }
  hostRows.value = rows
  extraHosts.value = extra
}

// syncHostsFromRows rebuilds form.hosts (the source of truth used by save + the
// YAML view) from the builder rows. Empty subdomain → the domain's root.
function syncHostsFromRows() {
  const hosts: string[] = []
  for (const r of hostRows.value) {
    if (!r.domain) continue
    const sub = r.sub.trim().replace(/^\.+|\.+$/g, '')
    hosts.push(sub ? `${sub}.${r.domain}` : r.domain)
  }
  hosts.push(...extraHosts.value)
  form.value.hosts = hosts.join(', ')
}

function addHostRow() {
  hostRows.value.push({ id: hostRowSeq++, sub: '', domain: domains.value[0]?.name ?? '' })
}
function removeHostRow(i: number) { hostRows.value.splice(i, 1) }
function removeExtraHost(i: number) { extraHosts.value.splice(i, 1) }

// Keep form.hosts in lockstep with the builder. Rebuilding from rows is
// idempotent, so the parse → build round-trip during open/switch is safe.
watch([hostRows, extraHosts], syncHostsFromRows, { deep: true })

// Reference data the form offers. Fetched on first open and kept for the
// session — a host page shouldn't have to know the form needs them.
let loaded = false
async function ensureRefData() {
  if (loaded || !props.workspaceId) return
  try {
    const [mw, certs, doms] = await Promise.all([
      middlewareApi.list(props.workspaceId),
      certificateApi.list(props.workspaceId),
      domainApi.list(props.workspaceId),
    ])
    middlewares.value = mw.data.data ?? []
    certificates.value = certs.data.data ?? []
    domains.value = doms.data.data ?? []
    loaded = true
  } catch {
    // The form still works: hosts fall back to a free-text input, and the
    // middleware/certificate pickers just have nothing to offer.
  }
}
// A workspace switch invalidates every list above.
watch(() => props.workspaceId, () => { loaded = false; portCache.clear() })

// immediate, so a parent that mounts this already open still gets an
// initialised form rather than an empty one.
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    await ensureRefData()
    if (props.editing) resetForEdit(props.editing)
    else resetForCreate()
    await nextTick()
    resetDirty()
  },
  { immediate: true },
)

// A route is a long form — hosts, ports, TLS, middlewares — so closing it after
// an edit asks before throwing the work away. The backdrop no longer dismisses
// anything, so this only fires on a deliberate Escape, Cancel or close icon.
const { dirty, reset: resetDirty } = useDirtyGuard(() => ({
  form: form.value,
  hostRows: hostRows.value,
  advancedConfig: advancedConfig.value,
  formMode: formMode.value,
}))

function requestClose() {
  if (saving.value) return
  if (dirty.value) {
    confirmDiscard.value = true
    return
  }
  emit('close')
}

function discard() {
  confirmDiscard.value = false
  emit('close')
}


function resetForCreate() {
  form.value = emptyForm()
  form.value.application_id = props.presetAppId ?? props.apps[0]?.id ?? null
  formMode.value = 'simple'
  advancedConfig.value = ''
  yamlError.value = ''
  parseHostsToRows() // empty
  if (!hostRows.value.length && domains.value.length) addHostRow()
  loadAppPorts(form.value.application_id).then(() => {
    // One declared port is the only sensible target, so choosing it saves the
    // one field the caller can't already know.
    if (appPorts.value.length === 1) form.value.target_port = appPorts.value[0].container_port
  })
}

function resetForEdit(r: Route) {
  form.value = {
    name: r.name, application_id: r.application_id, path: r.path,
    hosts: (r.hosts || []).join(', '), methods: [...(r.methods || [])],
    middlewares: [...(r.middlewares || [])], rewrite: r.rewrite || '', target_port: r.target_port || undefined,
    tls_mode: r.tls_mode, certificate_id: r.certificate_id ?? null, enabled: r.enabled,
  }
  loadAppPorts(r.application_id)
  yamlError.value = ''
  parseHostsToRows()
  if (r.advanced_config && r.advanced_config.trim()) {
    formMode.value = 'advanced'
    advancedConfig.value = r.advanced_config
  } else {
    formMode.value = 'simple'
    advancedConfig.value = ''
  }
}

async function save() {
  if (!props.workspaceId || !form.value.application_id) return
  let advanced = ''
  if (formMode.value === 'advanced') {
    try {
      yamlToSimple(advancedConfig.value) // keep structured columns in sync
      advanced = advancedConfig.value
    } catch {
      yamlError.value = 'Invalid YAML — fix it before saving.'
      return
    }
  }
  saving.value = true
  try {
    const input = {
      name: form.value.name,
      application_id: form.value.application_id,
      path: form.value.path,
      hosts: splitCsv(form.value.hosts),
      methods: form.value.methods,
      middlewares: form.value.middlewares,
      rewrite: form.value.rewrite || '',
      target_port: form.value.target_port || 0,
      tls_mode: form.value.tls_mode,
      advanced_config: advanced,
      // Custom TLS uses a stored certificate.
      certificate_id: form.value.tls_mode === 'custom' ? form.value.certificate_id : null,
      enabled: form.value.enabled,
    }
    if (props.editing) {
      await routeApi.update(props.workspaceId, props.editing.id, input)
      notify.success('Route updated')
    } else {
      await routeApi.create(props.workspaceId, input)
      notify.success('Route created')
    }
    emit('saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <AppModal v-if="open" auto-focus :escapable="!confirmDiscard" @close="requestClose">
      <div class="modal-header">
        <h3 id="route-form-title">{{ editing ? 'Edit route' : 'New route' }}</h3>
        <button class="btn-icon btn-icon-muted" aria-label="Close" data-modal-skip-focus @click="requestClose"><span class="mdi mdi-close"></span></button>
      </div>
    <form @submit.prevent="save">
      <div class="modal-body">
        <div class="form-row">
          <div class="form-group" style="flex: 1">
            <label class="form-label">Name</label>
            <input v-model="form.name" class="form-input" placeholder="e.g. web" pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" title="Lowercase letters, digits and hyphens" required autofocus />
            <p class="form-hint">Lowercase letters, digits and hyphens (e.g. my-api).</p>
          </div>
          <div class="form-group" style="flex: 1">
            <label class="form-label">Application</label>
            <!-- Opened from an application's own page the app is a given, not
                 a choice — a picker there invites routing the wrong app. -->
            <input v-if="lockApp" class="form-input" :value="lockedAppName" disabled aria-label="Application" />
            <select v-else v-model="form.application_id" class="form-select" required @change="onAppChange">
              <option v-for="a in apps" :key="a.id" :value="a.id">{{ a.name }}</option>
            </select>
          </div>
        </div>
        <div class="form-group">
          <label class="form-label">Target port <span class="text-muted">(optional — defaults to the app's port)</span></label>
          <select v-if="appPorts.length" v-model="form.target_port" class="form-select">
            <option :value="undefined">App default port</option>
            <option v-for="p in appPorts" :key="p.id" :value="p.container_port">{{ p.container_port }}/{{ p.protocol }} · {{ p.scheme || 'http' }}{{ p.name ? ` — ${p.name}` : '' }}</option>
          </select>
          <input v-else v-model.number="form.target_port" type="number" class="form-input" placeholder="app port" />
          <p v-if="selectedPortHttps" class="form-hint"><span class="mdi mdi-lock-outline"></span> Backend served over HTTPS; TLS verification is skipped for the internal address.</p>
        </div>

        <!-- Routing config: Simple / Advanced -->
        <div class="mode-tabs">
          <button type="button" :class="['mode-tab', { active: formMode === 'simple' }]" @click="switchMode('simple')">Simple</button>
          <button type="button" :class="['mode-tab', { active: formMode === 'advanced' }]" @click="switchMode('advanced')">Advanced</button>
        </div>

        <template v-if="formMode === 'simple'">
          <div class="form-group">
            <label class="form-label">Hosts</label>
            <template v-if="domains.length">
              <div v-for="(row, i) in hostRows" :key="row.id" class="host-row">
                <input v-model="row.sub" class="form-input host-sub" placeholder="subdomain (blank = root)" aria-label="Subdomain" />
                <span class="host-dot">.</span>
                <select v-model="row.domain" class="form-select host-domain" aria-label="Domain">
                  <option v-for="d in domains" :key="d.id" :value="d.name">{{ d.name }}{{ d.verified ? '' : ' — unverified' }}</option>
                </select>
                <button type="button" class="btn-icon btn-icon-danger" title="Remove host" aria-label="Remove host" @click="removeHostRow(i)"><span class="mdi mdi-close"></span></button>
              </div>
              <div v-for="(h, i) in extraHosts" :key="'x' + i" class="host-row">
                <input :value="h" class="form-input mono" disabled title="Its domain is no longer registered" aria-label="Host" />
                <button type="button" class="btn-icon btn-icon-danger" title="Remove host" aria-label="Remove host" @click="removeExtraHost(i)"><span class="mdi mdi-close"></span></button>
              </div>
              <button type="button" class="btn btn-secondary btn-sm" @click="addHostRow"><span class="mdi mdi-plus"></span> Add host</button>
              <p v-if="!hostRows.length && !extraHosts.length" class="hint">No hosts — this route will match all hosts (catch-all).</p>
              <p v-else class="hint">Leave a subdomain blank to serve the domain at its root. Routes: <code>{{ form.hosts || '(catch-all)' }}</code></p>
            </template>
            <template v-else>
              <input v-model="form.hosts" class="form-input" placeholder="app.example.com" />
              <p class="hint">No domains registered. Enter a host manually, <router-link to="/domains">add a domain</router-link> to pick from a list, or leave blank to match all hosts.</p>
            </template>
          </div>
          <div class="form-group">
            <label class="form-label">Path</label>
            <input v-model="form.path" class="form-input" placeholder="/" />
          </div>
          <div class="form-group">
            <label class="form-label">Methods <span class="text-muted">(blank = all)</span></label>
            <div class="methods-grid">
              <label v-for="m in allMethods" :key="m" class="method-chip" :class="{ active: form.methods.includes(m) }">
                <input type="checkbox" :value="m" v-model="form.methods" /> {{ m }}
              </label>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">
              Rewrite <span class="text-muted">(optional)</span>
              <span
                class="mdi mdi-information-outline info-icon"
                :title="'Rewrites the request path before forwarding it to the app.\n\nExample:\n  path:    /api/v1\n  rewrite: /\n\nA request to /api/v1/users is forwarded as /users.'"
              ></span>
            </label>
            <input v-model="form.rewrite" class="form-input" placeholder="/new-prefix/" />
          </div>
          <div class="form-group">
            <label class="form-label">Middlewares <span class="text-muted">({{ form.middlewares.length }} selected)</span></label>
            <div v-if="middlewares.length === 0" class="text-muted text-sm">No middlewares defined yet.</div>
            <div v-else class="middleware-select">
              <label v-for="m in middlewares" :key="m.id" class="middleware-option" :class="{ active: form.middlewares.includes(m.name) }">
                <input type="checkbox" :value="m.name" v-model="form.middlewares" />
                <span class="middleware-option-name">{{ m.name }}</span>
                <span class="badge badge-neutral middleware-option-type">{{ m.type }}</span>
              </label>
            </div>
          </div>
        </template>
        <template v-else>
          <div class="form-warning">
            <span class="mdi mdi-alert-outline"></span>
            <span>Enter advanced configuration at your own risk!</span>
          </div>
          <div class="form-group">
            <label class="form-label">Configuration (YAML)</label>
            <textarea v-model="advancedConfig" class="form-input yaml-editor" rows="14" spellcheck="false" placeholder="path: /&#10;hosts: []&#10;methods: []"></textarea>
            <p class="text-muted text-sm"><code>name</code> and <code>backends</code> are managed by Miabi; everything else (path, hosts, methods, middlewares, rewrite, cors, rateLimit, …) comes from here.</p>
          </div>
        </template>
        <p v-if="yamlError" class="form-error">{{ yamlError }}</p>
        <div class="form-group">
          <label class="form-label">TLS</label>
          <select v-model="form.tls_mode" class="form-select">
            <option value="acme">ACME (automatic)</option>
            <option value="custom">Custom certificate</option>
            <option value="none">None</option>
          </select>
        </div>
        <div v-if="form.tls_mode === 'custom'" class="form-group">
          <label class="form-label">Certificate</label>
          <select v-model="form.certificate_id" class="form-select" required>
            <option :value="null" disabled>Select a stored certificate…</option>
            <option v-for="c in certificates" :key="c.id" :value="c.id">
              {{ c.name }} — {{ (c.dns_names || [c.common_name]).join(', ') }}
            </option>
          </select>
          <p class="form-hint">Pick a stored certificate, or <router-link to="/certificates">import one</router-link>.</p>
        </div>
        <label class="checkbox-label" style="margin-bottom: 0">
          <input type="checkbox" v-model="form.enabled" /> Enabled
        </label>
      </div>
      <div class="modal-footer">
        <button type="button" class="btn btn-secondary" @click="requestClose">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Create') }}</button>
      </div>
    </form>
    </AppModal>

    <ConfirmDialog
      :open="confirmDiscard"
      title="Discard changes?"
      message="This route has unsaved changes. Closing now loses them."
      confirm-label="Discard"
      cancel-label="Keep editing"
      variant="danger"
      @confirm="discard"
      @cancel="confirmDiscard = false"
    />
  </Teleport>
</template>

<style scoped>
.text-muted { color: var(--text-muted); font-weight: 400; }
.form-row { display: flex; gap: 12px; }
.mode-tabs { display: inline-flex; border: 1px solid var(--border-primary); border-radius: 8px; overflow: hidden; margin-bottom: 12px; }
.mode-tab { padding: 6px 18px; background: var(--bg-secondary); border: none; cursor: pointer; font-size: 13px; color: var(--text-muted); }
.mode-tab.active { background: var(--primary-600); color: #fff; }
.form-warning { display: flex; align-items: center; gap: 8px; padding: 8px 12px; margin-bottom: 12px; border-radius: 8px; background: color-mix(in srgb, var(--warning, #d97706) 14%, transparent); color: var(--warning, #d97706); font-size: 13px; }
.form-error { color: var(--danger, #dc2626); font-size: 13px; margin-top: 6px; }
.yaml-editor { width: 100%; font-family: 'JetBrains Mono', monospace; font-size: 13px; line-height: 1.5; white-space: pre; overflow-wrap: normal; overflow-x: auto; tab-size: 2; }
code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 5px; border-radius: 4px; }
.hint { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.host-row { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
.host-sub { flex: 1; }
.host-dot { color: var(--text-muted); font-weight: 600; }
.host-domain { flex: 0 0 auto; max-width: 55%; font-family: 'JetBrains Mono', monospace; }
.info-icon { font-size: 14px; color: var(--text-muted); cursor: help; margin-left: 2px; vertical-align: middle; }
.info-icon:hover { color: var(--primary-500); }
.methods-grid { display: flex; flex-wrap: wrap; gap: 8px; }
.method-chip {
  display: inline-flex; align-items: center; gap: 6px; cursor: pointer;
  padding: 5px 12px; border: 1px solid var(--border-primary); border-radius: 999px;
  font-size: 12px; font-weight: 600; color: var(--text-secondary); user-select: none;
  transition: background 0.12s, border-color 0.12s, color 0.12s;
}
.method-chip:hover { border-color: var(--primary-500); }
.method-chip.active { background: var(--primary-600); border-color: var(--primary-600); color: #fff; }
.method-chip input { display: none; }
.middleware-select {
  display: flex; flex-direction: column; gap: 4px;
  max-height: 180px; overflow-y: auto;
  padding: 6px; border: 1px solid var(--border-primary);
  border-radius: var(--radius, 8px); background: var(--bg-secondary);
}
.middleware-option {
  display: flex; align-items: center; gap: 8px; cursor: pointer;
  padding: 6px 8px; border-radius: 6px; font-size: 13px;
}
.middleware-option:hover { background: var(--bg-tertiary); }
.middleware-option.active { background: color-mix(in srgb, var(--primary-500) 12%, transparent); }
.middleware-option-name { font-weight: 500; }
.middleware-option-type { font-size: 10px; margin-left: auto; }
</style>
