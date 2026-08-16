<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { routeApi } from '@/api/routes'
import { appApi } from '@/api/apps'
import { middlewareApi } from '@/api/middlewares'
import type { Route, Application, Middleware } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const routeId = computed(() => Number(route.params.id))
const item = ref<Route | null>(null)
const app = ref<Application | null>(null)
const loading = ref(false)
const showDelete = ref(false)
const deleting = ref(false)
const tab = ref<'overview' | 'settings'>('overview')

const tabs = [
  { key: 'overview', label: 'Overview' },
  { key: 'settings', label: 'Settings' },
] as const

// Canary state (read-only; rollout is managed from the application's settings).
const canaryActive = computed(() => !!app.value?.canary_release_id)
const canaryWeight = computed(() => app.value?.canary_weight ?? 0)
const stableWeight = computed(() => (canaryActive.value ? 100 - canaryWeight.value : 100))
let poll: ReturnType<typeof setInterval> | null = null

// Middleware attach/detach (works without editing the route — including on
// generated external-access routes).
const allMiddlewares = ref<Middleware[]>([])
const selectedMw = ref('')
const mwBusy = ref(false)
const attachedMw = computed(() => item.value?.middlewares || [])
const availableMw = computed(() => allMiddlewares.value.filter((m) => !attachedMw.value.includes(m.name)))

const port = computed(() => item.value?.target_port || app.value?.port || 80)
// Prefer the real backend resolved server-side (e.g. a port-forward node's
// address:hostPort); fall back to the in-network alias for display.
const stableEndpoint = computed(() => item.value?.backends?.[0] || (app.value ? `http://mb-app-${app.value.id}:${port.value}` : ''))
const canaryEndpoint = computed(() => item.value?.backends?.[1] || (app.value ? `http://mb-app-${app.value.id}-canary:${port.value}` : ''))

// DNS guidance: where the route's hosts should point. dns_target is the public
// IP of the gateway that terminates this route (resolved server-side).
const hosts = computed(() => (item.value?.hosts || []).filter((h: string) => !!h.trim()))
// Browser URL for a host (https when the route has TLS), so users can open the
// app directly from here.
function hostUrl(host: string): string {
  const scheme = item.value && item.value.tls_mode !== 'none' ? 'https' : 'http'
  const path = item.value?.path && item.value.path !== '/' ? item.value.path : ''
  return `${scheme}://${host}${path}`
}
const dnsTarget = computed(() => item.value?.dns_target || '')
const dnsHostname = computed(() => item.value?.dns_hostname || '')

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    notify.success('Copied to clipboard')
  } catch {
    notify.error('Could not copy')
  }
}

const maintenanceOn = computed(() => item.value?.maintenance?.enabled === true)
const mtStatus = ref<number | undefined>(undefined)
const mtMessage = ref('')
const mtBusy = ref(false)
const secBusy = ref(false)
const previewAs = ref<'text' | 'json' | 'xml'>('text')

const GATEWAY_DEFAULT_STATUS = 503
const GATEWAY_DEFAULT_MESSAGE = '503 Service temporarily unavailable'
const STATUS_TEXT: Record<number, string> = {
  400: 'Bad Request', 401: 'Unauthorized', 403: 'Forbidden', 404: 'Not Found',
  418: "I'm a teapot", 429: 'Too Many Requests', 500: 'Internal Server Error',
  502: 'Bad Gateway', 503: 'Service Unavailable', 504: 'Gateway Timeout',
}

function syncMaintenanceForm(r: Route | null) {
  mtStatus.value = r?.maintenance?.status_code || undefined
  mtMessage.value = r?.maintenance?.message || ''
}

const mtDirty = computed(() => {
  const saved = item.value?.maintenance
  return (mtStatus.value || 0) !== (saved?.status_code || 0) || mtMessage.value !== (saved?.message || '')
})
const statusInvalid = computed(() => {
  const v = mtStatus.value
  return v !== undefined && v !== null && String(v) !== '' && (v < 400 || v > 599)
})
const effectiveStatus = computed(() => mtStatus.value || GATEWAY_DEFAULT_STATUS)
const effectiveMessage = computed(() => mtMessage.value.trim() || GATEWAY_DEFAULT_MESSAGE)
const messageIsJson = computed(() => {
  try {
    JSON.parse(effectiveMessage.value)
    return true
  } catch {
    return false
  }
})
const previewContentType = computed(() =>
  previewAs.value === 'json' ? 'application/json'
    : previewAs.value === 'xml' ? 'application/xhtml+xml'
      : 'text/plain; charset=utf-8',
)
function escapeXml(v: string): string {
  return v.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}
const previewBody = computed(() => {
  const code = effectiveStatus.value
  const msg = effectiveMessage.value
  if (previewAs.value === 'json') {
    return messageIsJson.value ? msg : JSON.stringify({ success: false, statusCode: code, error: msg }, null, 2)
  }
  if (previewAs.value === 'xml') {
    return `<?xml version="1.0" encoding="UTF-8"?>\n<error>\n  <success>false</success>\n  <statusCode>${code}</statusCode>\n  <error>${escapeXml(msg)}</error>\n</error>`
  }
  return msg
})
const previewStatusLine = computed(
  () => `HTTP/1.1 ${effectiveStatus.value} ${STATUS_TEXT[effectiveStatus.value] || ''}`.trimEnd(),
)

async function load(force = false) {
  const wid = currentWorkspaceId.value
  if (!wid || !routeId.value) return
  loading.value = true
  const keepEdits = !force && mtDirty.value
  try {
    item.value = (await routeApi.get(wid, routeId.value)).data.data
    if (!keepEdits) syncMaintenanceForm(item.value)
    app.value = (await appApi.get(wid, item.value.application_id)).data.data
  } catch (e) {
    notify.apiError(e)
    router.replace('/routes')
  } finally {
    loading.value = false
  }
}

// Middleware catalog for the attach picker. Loaded once per workspace, kept
// out of the 5s route poll.
async function loadMiddlewares() {
  const wid = currentWorkspaceId.value
  if (!wid) return
  try {
    allMiddlewares.value = (await middlewareApi.list(wid)).data.data || []
  } catch {
    /* non-fatal: the picker just shows empty */
  }
}

async function attachMiddleware() {
  if (!currentWorkspaceId.value || !item.value || !selectedMw.value) return
  const name = selectedMw.value
  mwBusy.value = true
  try {
    item.value = (await routeApi.attachMiddleware(currentWorkspaceId.value, item.value.id, name)).data.data
    selectedMw.value = ''
    notify.success(`Attached "${name}"`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    mwBusy.value = false
  }
}

async function detachMiddleware(name: string) {
  if (!currentWorkspaceId.value || !item.value) return
  mwBusy.value = true
  try {
    item.value = (await routeApi.detachMiddleware(currentWorkspaceId.value, item.value.id, name)).data.data
    notify.success(`Detached "${name}"`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    mwBusy.value = false
  }
}

watch([routeId, currentWorkspaceId], () => {
  load(true)
  loadMiddlewares()
  if (poll) clearInterval(poll)
  poll = setInterval(() => load(), 5000) // reflect async canary deploy / weight changes
}, { immediate: true })
onBeforeUnmount(() => { if (poll) clearInterval(poll) })

async function toggleEnabled() {
  if (!currentWorkspaceId.value || !item.value) return
  const r = item.value
  try {
    await routeApi.setEnabled(currentWorkspaceId.value, r.id, !r.enabled)
    notify.success(r.enabled ? 'Route disabled' : 'Route enabled')
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}


async function setSecurity(body: { exploit_protection?: boolean }) {
  if (!currentWorkspaceId.value || !item.value) return
  secBusy.value = true
  try {
    await routeApi.setSecurity(currentWorkspaceId.value, item.value.id, body)
    notify.success('Route security updated')
    await load(true)
  } catch (e) {
    notify.apiError(e)
  } finally {
    secBusy.value = false
  }
}

async function saveMaintenance(enabled: boolean) {
  if (!currentWorkspaceId.value || !item.value) return
  mtBusy.value = true
  try {
    await routeApi.setMaintenance(currentWorkspaceId.value, item.value.id, {
      enabled,
      status_code: mtStatus.value || 0,
      message: mtMessage.value || '',
    })
    notify.success(enabled ? 'Route is in maintenance mode' : 'Maintenance mode turned off')
    await load(true)
  } catch (e) {
    notify.apiError(e)
  } finally {
    mtBusy.value = false
  }
}

async function removeRoute() {
  if (!currentWorkspaceId.value || !item.value) return
  deleting.value = true
  try {
    await routeApi.remove(currentWorkspaceId.value, item.value.id)
    notify.success('Route deleted')
    router.replace('/routes')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div v-if="item">
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" title="Back" aria-label="Back" @click="router.push('/routes')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ item.display_name || item.name }}</h1>
          <span class="cell-sub">{{ item.path }} · {{ app?.name || `app #${item.application_id}` }}</span>
        </div>
        <span class="badge badge-dot" :class="item.enabled ? 'badge-success' : 'badge-neutral'">
          {{ item.enabled ? 'enabled' : 'disabled' }}
        </span>
        <span v-if="item.generated" class="badge badge-info badge-dot" title="Auto-generated for external access; managed from the app's External Access">auto-generated</span>
        <span v-if="maintenanceOn" class="badge badge-warning badge-dot" title="The gateway answers this route itself; the backend is never reached">maintenance</span>
        <span v-if="canaryActive" class="badge badge-warning badge-dot">canary {{ canaryWeight }}%</span>
      </div>
    </div>

    <div class="tabs">
      <button v-for="t in tabs" :key="t.key" class="tab" :class="{ active: tab === t.key }" @click="tab = t.key">
        {{ t.label }}
      </button>
    </div>

    <!-- Overview -->
    <div v-if="tab === 'overview'">
      <div class="card mb-4">
        <div class="card-header"><h2>Configuration</h2></div>
        <div class="card-body detail-list">
          <div class="detail-row"><span class="detail-key">Application</span><span>{{ app?.name || `#${item.application_id}` }}</span></div>
          <div class="detail-row"><span class="detail-key">Path</span><span class="mono">{{ item.path }}</span></div>
          <div class="detail-row">
            <span class="detail-key">Hosts</span>
            <span v-if="hosts.length" class="host-links">
              <a v-for="h in hosts" :key="h" class="host-link" :href="hostUrl(h)" target="_blank" rel="noopener">
                {{ h }}<span class="mdi mdi-open-in-new"></span>
              </a>
            </span>
            <span v-else>—</span>
          </div>
          <div class="detail-row"><span class="detail-key">Methods</span><span>{{ (item.methods || []).join(', ') || 'all' }}</span></div>
          <div class="detail-row"><span class="detail-key">Target port</span><span class="mono">{{ port }}</span></div>
          <div class="detail-row">
            <span class="detail-key">TLS</span>
            <span><span class="badge badge-neutral">{{ item.tls_mode }}</span><span v-if="item.has_custom_cert" class="text-muted text-sm" style="margin-left: 8px">custom cert</span></span>
          </div>
        </div>
      </div>

      <!-- Middlewares: attach/detach without editing the route (generated routes too). -->
      <div class="card mb-4">
        <div class="card-header"><h2>Middlewares</h2></div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-top: 0">
            Attach Goma middlewares (auth, rate-limit, headers…) to this route without editing it.<template v-if="item.generated"> Allowed on auto-generated routes, so you can secure external access in place.</template>
          </p>
          <div class="mw-chips">
            <span v-for="m in attachedMw" :key="m" class="mw-chip">
              {{ m }}
              <button
                v-if="ws.canEdit"
                class="mw-chip-x"
                :disabled="mwBusy"
                title="Detach middleware"
                aria-label="Detach middleware"
                @click="detachMiddleware(m)"
              ><span class="mdi mdi-close"></span></button>
            </span>
            <span v-if="!attachedMw.length" class="text-muted text-sm">No middlewares attached.</span>
          </div>

          <div v-if="ws.canEdit" class="mw-attach">
            <select v-model="selectedMw" class="input mw-select form-select" :disabled="mwBusy || !availableMw.length">
              <option value="">{{ availableMw.length ? 'Select a middleware…' : 'No more middlewares to attach' }}</option>
              <option v-for="m in availableMw" :key="m.id" :value="m.name">{{ m.name }} · {{ m.type }}</option>
            </select>
            <button class="btn btn-secondary btn-sm" :disabled="!selectedMw || mwBusy" @click="attachMiddleware">
              <span v-if="mwBusy" class="spinner spinner-sm"></span>
              <span v-else>Attach</span>
            </button>
          </div>
          <p v-if="ws.canEdit && !allMiddlewares.length" class="text-muted text-sm mw-empty-hint">
            No middlewares defined in this workspace yet.
            <a href="#" @click.prevent="router.push('/middlewares')">Create one</a> to attach it here.
          </p>
        </div>
      </div>

      <div v-if="hosts.length" class="card mb-4">
        <div class="card-header"><h2>DNS</h2></div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-top: 0">
            Point each host's DNS at the gateway serving this route, then Miabi issues TLS automatically.
          </p>
          <div v-if="dnsTarget || dnsHostname" class="dns-table">
            <div class="dns-head">
              <span>Type</span><span>Name</span><span>Value</span><span></span>
            </div>
            <div v-for="h in hosts" :key="h" class="dns-row">
              <span class="badge badge-neutral">{{ dnsTarget ? 'A' : 'CNAME' }}</span>
              <span class="mono">{{ h }}</span>
              <span class="mono">{{ dnsTarget || dnsHostname }}</span>
              <button class="btn-icon btn-icon-muted" title="Copy value" aria-label="Copy value" @click="copy(dnsTarget || dnsHostname)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </div>
          <div v-else class="dns-empty">
            <span class="mdi mdi-alert-outline"></span>
            No public address is set for the node serving this route. Set its
            <strong>Public IP</strong> on the node's page so Miabi can show the record to create.
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Backends</h2></div>
        <div class="card-body">
          <div class="backend-row">
            <span class="badge badge-success badge-dot">stable</span>
            <span class="mono backend-ep">{{ stableEndpoint }}</span>
            <span class="weight-pill">{{ stableWeight }}%</span>
          </div>
          <div v-if="canaryActive" class="backend-row">
            <span class="badge badge-warning badge-dot">canary</span>
            <span class="mono backend-ep">{{ canaryEndpoint }}</span>
            <span class="weight-pill">{{ canaryWeight }}%</span>
          </div>
          <div class="split-bar">
            <div class="split-stable" :style="{ width: stableWeight + '%' }"></div>
            <div v-if="canaryActive" class="split-canary" :style="{ width: canaryWeight + '%' }"></div>
          </div>
          <p v-if="canaryActive" class="text-muted text-sm mt-4">
            A canary rollout is in progress — the platform is shifting traffic automatically.
            Manage it from the application's <strong>Deployments</strong> tab.
          </p>
          <p v-else class="text-muted text-sm mt-4">Goma Gateway splits traffic across these weighted backends.</p>
        </div>
      </div>
    </div>

    <!-- Settings -->
    <div v-else>
      <div class="card mb-4">
        <div class="card-header"><h2>Maintenance</h2></div>
        <div class="card-body detail-list">
          <div class="mt-state" :class="maintenanceOn ? 'is-parked' : 'is-live'">
            <span class="mdi" :class="maintenanceOn ? 'mdi-pause-octagon-outline' : 'mdi-check-circle-outline'"></span>
            <div class="mt-state-copy">
              <div class="cell-title">{{ maintenanceOn ? 'In maintenance' : 'Serving normally' }}</div>
              <div class="text-muted text-sm">
                <template v-if="maintenanceOn">
                  Every request gets <strong>{{ effectiveStatus }}</strong> from the gateway. The app is
                  still running and never sees the traffic.
                </template>
                <template v-else>
                  Requests reach the app as usual. Parking answers at the gateway instead, so a deploy or
                  a migration stays invisible to visitors.
                </template>
              </div>
            </div>
            <button
              class="btn btn-sm"
              :class="maintenanceOn ? 'btn-secondary' : 'btn-warning'"
              :disabled="!ws.canEdit || mtBusy"
              @click="saveMaintenance(!maintenanceOn)"
            >
              {{ mtBusy ? 'Saving…' : maintenanceOn ? 'Resume traffic' : 'Start maintenance' }}
            </button>
          </div>

          <div class="mt-response">
            <div class="mt-response-head">
              <div class="cell-title">Response while parked</div>
              <div class="text-muted text-sm">
                Leave both blank to use the gateway's default — {{ GATEWAY_DEFAULT_STATUS }} with a short
                message.
              </div>
            </div>

            <div class="mt-fields">
              <label class="mt-field mt-field-status">
                <span class="form-label">Status code</span>
                <input
                  v-model.number="mtStatus"
                  type="number"
                  class="form-input"
                  :class="{ 'is-invalid': statusInvalid }"
                  min="400"
                  max="599"
                  :placeholder="String(GATEWAY_DEFAULT_STATUS)"
                  :disabled="!ws.canEdit"
                />
                <span v-if="statusInvalid" class="form-error">Use 400–599 — a 2xx reads as healthy.</span>
                <span v-else class="form-hint">4xx or 5xx</span>
              </label>

              <label class="mt-field mt-field-message">
                <span class="form-label">
                  Message
                  <span v-if="messageIsJson && mtMessage.trim()" class="badge badge-info mt-json-badge">valid JSON</span>
                </span>
                <textarea
                  v-model="mtMessage"
                  class="form-input mt-message"
                  rows="4"
                  maxlength="1024"
                  spellcheck="false"
                  placeholder="Back at 14:00 UTC"
                  :disabled="!ws.canEdit"
                ></textarea>
                <span class="form-hint">{{ mtMessage.length }}/1024 · plain text, or JSON to answer API clients in your own shape</span>
              </label>
            </div>

            <div class="mt-preview">
              <div class="mt-preview-head">
                <span class="form-label" style="margin: 0">What the caller gets</span>
                <div class="mt-preview-tabs">
                  <button
                    v-for="f in [
                      { key: 'text', label: 'Browser' },
                      { key: 'json', label: 'JSON' },
                      { key: 'xml', label: 'XML' },
                    ]"
                    :key="f.key"
                    type="button"
                    class="mt-preview-tab"
                    :class="{ active: previewAs === f.key }"
                    @click="previewAs = f.key as 'text' | 'json' | 'xml'"
                  >
                    {{ f.label }}
                  </button>
                </div>
              </div>
              <pre class="mt-preview-body"><code>{{ previewStatusLine }}
Content-Type: {{ previewContentType }}

{{ previewBody }}</code></pre>
              <p class="text-muted text-sm mt-preview-note">
                <template v-if="previewAs === 'text'">
                  Browsers land here: the <code>Accept</code> header they send matches none of the
                  structured formats.
                </template>
                <template v-else-if="previewAs === 'json'">
                  <template v-if="messageIsJson">Your message parses as JSON, so it is returned as written.</template>
                  <template v-else>Not valid JSON, so the gateway wraps it in its own envelope.</template>
                </template>
                <template v-else>
                  XML is always wrapped and escaped — a hand-written XML message is not passed through.
                </template>
              </p>
            </div>

            <div class="mt-actions">
              <button
                v-if="mtDirty"
                class="btn btn-secondary btn-sm"
                :disabled="mtBusy"
                @click="syncMaintenanceForm(item)"
              >
                Discard
              </button>
              <button
                class="btn btn-primary btn-sm"
                :disabled="!ws.canEdit || mtBusy || !mtDirty || statusInvalid"
                @click="saveMaintenance(maintenanceOn)"
              >
                {{ mtBusy ? 'Saving…' : mtDirty ? 'Save response' : 'Saved' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>Gateway security</h2></div>
        <div class="card-body detail-list">
          <div class="detail-row">
            <span>
              <div class="cell-title">Exploit protection</div>
              <div class="text-muted text-sm">
                Rejects requests carrying common injection and traversal signatures at the gateway, before
                the backend sees them. Off by default — a strict backend may already reject them, and a
                noisy filter can block legitimate traffic.
              </div>
            </span>
            <button
              class="btn btn-secondary btn-sm"
              :disabled="!ws.canEdit || secBusy"
              @click="setSecurity({ exploit_protection: item.exploit_protection !== true })"
            >
              {{ item.exploit_protection === true ? 'Disable' : 'Enable' }}
            </button>
          </div>
        </div>
      </div>

      <!-- Generated external-access routes are managed by the platform; read-only here. -->
      <div v-if="item.generated" class="card">
        <div class="card-header"><h2>Settings</h2></div>
        <div class="card-body">
          <div class="managed-note">
            <span class="mdi mdi-lock-outline"></span>
            <div>
              <div class="cell-title">Auto-generated &amp; managed</div>
              <div class="text-muted text-sm">
                This route is created and reconciled automatically for the application's external access.
                Enable, disable, or remove it from the app's <strong>External Access</strong> card — it can't be
                edited or deleted here.
              </div>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="card">
        <div class="card-header"><h2>Settings</h2></div>
        <div class="card-body detail-list">
          <div class="detail-row">
            <span>
              <div class="cell-title">{{ item.enabled ? 'Route enabled' : 'Route disabled' }}</div>
              <div class="text-muted text-sm">Disabling removes it from the gateway without deleting it.</div>
            </span>
            <button class="btn btn-secondary btn-sm" :disabled="!ws.canEdit" @click="toggleEnabled">
              {{ item.enabled ? 'Disable' : 'Enable' }}
            </button>
          </div>
          <div class="detail-row">
            <span>
              <div class="cell-title">Edit route</div>
              <div class="text-muted text-sm">Change hosts, path, methods, middlewares, and TLS.</div>
            </span>
            <button class="btn btn-secondary btn-sm" @click="router.push('/routes')">Open editor</button>
          </div>
          <div class="detail-row">
            <span>
              <div class="cell-title" style="color: var(--danger-600)">Delete route</div>
              <div class="text-muted text-sm">Permanently removes this route from the gateway.</div>
            </span>
            <button class="btn btn-danger btn-sm" :disabled="!ws.canEdit" @click="showDelete = true">Delete</button>
          </div>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :open="showDelete"
      title="Delete route"
      :message="`Delete route &quot;${item?.name}&quot;? Its hosts will stop routing to the app.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="removeRoute"
      @cancel="showDelete = false"
    />
  </div>

  <div v-else class="card"><div class="card-body"><span class="spinner"></span></div></div>
</template>

<style scoped>
.managed-note { display: flex; align-items: flex-start; gap: 10px; font-size: 13px; }
.managed-note .mdi { font-size: 20px; color: var(--text-muted); flex-shrink: 0; }
.title-group { display: flex; align-items: center; gap: 12px; }
.title-group h1 { margin: 0; line-height: 1.2; }
.mono { font-family: monospace; font-size: 13px; }
.text-muted { color: var(--text-muted); }
.detail-list { display: flex; flex-direction: column; }
.detail-row { display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 12px 0; border-bottom: 1px solid var(--border-primary); font-size: 13px; }
.detail-row:last-child { border-bottom: none; }
.host-links { display: flex; flex-wrap: wrap; gap: 4px 12px; justify-content: flex-end; }
.host-link { color: var(--primary-600); text-decoration: none; display: inline-flex; align-items: center; gap: 3px; }
.host-link:hover { text-decoration: underline; }
.host-link .mdi { font-size: 13px; opacity: .7; }
.detail-key { color: var(--text-muted); }

.mw-chips { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 14px; }
.mw-chip { display: inline-flex; align-items: center; gap: 4px; padding: 3px 4px 3px 10px; font-size: 13px; border-radius: 14px; background: var(--bg-tertiary); border: 1px solid var(--border-primary); }
.mw-chip-x { display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; border: none; border-radius: 50%; background: transparent; color: var(--text-muted); cursor: pointer; padding: 0; }
.mw-chip-x:hover:not(:disabled) { background: var(--danger-100, rgba(239,68,68,.12)); color: var(--danger-600); }
.mw-chip-x:disabled { opacity: .5; cursor: default; }
.mw-chip-x .mdi { font-size: 14px; }
.mw-attach { display: flex; gap: 8px; align-items: center; }
.mw-select { flex: 1; max-width: 360px; }
.mw-empty-hint { margin-bottom: 0; }
.spinner-sm { width: 14px; height: 14px; }

.backend-row { display: flex; align-items: center; gap: 12px; padding: 8px 0; }
.backend-ep { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.weight-pill { font-weight: 600; font-size: 13px; min-width: 44px; text-align: right; }

.split-bar { display: flex; height: 14px; border-radius: 7px; overflow: hidden; background: var(--bg-tertiary); margin-top: 12px; }
.split-bar.lg { height: 30px; border-radius: 8px; }
.split-stable { background: var(--success-500, #22c55e); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }
.split-canary { background: var(--warning-500, #f59e0b); display: flex; align-items: center; justify-content: center; color: #fff; font-size: 11px; font-weight: 600; transition: width 250ms ease; }

.dns-table { display: flex; flex-direction: column; }
.dns-head, .dns-row { display: grid; grid-template-columns: 80px 1fr 1fr 36px; align-items: center; gap: 12px; padding: 8px 0; }
.dns-head { color: var(--text-muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; border-bottom: 1px solid var(--border-primary); }
.dns-row { border-bottom: 1px solid var(--border-primary); }
.dns-row:last-child { border-bottom: none; }
.dns-row .mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mt-state { display: flex; align-items: flex-start; gap: 12px; padding: 14px 16px; border-radius: 10px; border: 1px solid var(--border-primary); }
.mt-state .mdi { font-size: 22px; line-height: 1; margin-top: 2px; }
.mt-state-copy { flex: 1; min-width: 0; }
.mt-state.is-live { background: var(--bg-secondary); }
.mt-state.is-live .mdi { color: var(--success-500, #22c55e); }
.mt-state.is-parked { background: color-mix(in srgb, var(--warning-500, #f59e0b) 10%, transparent); border-color: color-mix(in srgb, var(--warning-500, #f59e0b) 35%, transparent); }
.mt-state.is-parked .mdi { color: var(--warning-500, #f59e0b); }

.mt-response { margin-top: 16px; }
.mt-response-head { margin-bottom: 12px; }
.mt-fields { display: flex; gap: 16px; flex-wrap: wrap; }
.mt-field { display: flex; flex-direction: column; gap: 6px; }
.mt-field-status { width: 120px; }
.mt-field-message { flex: 1; min-width: 260px; }
.mt-field .form-label { display: flex; align-items: center; gap: 8px; margin: 0; }
.mt-json-badge { font-size: 10px; }
.mt-message { font-family: var(--font-mono, monospace); font-size: 12px; resize: vertical; }
.form-input.is-invalid { border-color: var(--danger-500, #ef4444); }

.mt-preview { margin-top: 16px; }
.mt-preview-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
.mt-preview-tabs { display: flex; gap: 2px; padding: 2px; background: var(--bg-tertiary); border-radius: 8px; }
.mt-preview-tab { border: none; background: transparent; color: var(--text-muted); font-size: 12px; padding: 4px 10px; border-radius: 6px; cursor: pointer; }
.mt-preview-tab.active { background: var(--bg-primary); color: var(--text-primary); font-weight: 600; }
.mt-preview-body { margin: 0; padding: 12px 14px; background: var(--bg-tertiary); border: 1px solid var(--border-primary); border-radius: 8px; font-size: 12px; line-height: 1.55; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }
.mt-preview-note { margin: 6px 0 0; }

.mt-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }

.dns-empty { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-muted); padding: 10px 12px; border: 1px dashed var(--border-primary); border-radius: 8px; }
</style>
