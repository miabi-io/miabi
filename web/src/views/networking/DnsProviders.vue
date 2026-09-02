<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { dnsProviderApi } from '@/api/dns'
import { usageApi } from '@/api/resources'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { useDnsProviderCatalog } from '@/composables/useDnsProviderCatalog'
import DnsProviderField from '@/components/DnsProviderField.vue'
import type { DNSProvider, DNSProviderType, WorkspaceUsage } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const providers = ref<DNSProvider[]>([])
const usage = ref<WorkspaceUsage | null>(null)
const loading = ref(false)
const showConnect = ref(false)
const saving = ref(false)

// Capability gate: only constrains the UI when enforcement is on.
const allowed = computed(() => !usage.value || !usage.value.enforced || usage.value.capabilities.dns_providers)

const { catalog, ensure: ensureCatalog, describe, label: typeLabel } = useDnsProviderCatalog()

const name = ref('')
const ptype = ref<DNSProviderType>('')
const creds = ref<Record<string, string>>({})
const testZone = ref('')

const descriptor = computed(() => describe(ptype.value))

function selectType(t: string) {
  ptype.value = t
  creds.value = {}
  for (const f of describe(t)?.fields ?? []) {
    if (f.default) creds.value[f.key] = f.default
  }
}

const credsComplete = computed(() =>
  (descriptor.value?.fields ?? []).every((f) => !f.required || (creds.value[f.key] ?? '').trim() !== ''),
)

function filled(source: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(source)) {
    if (v.trim() !== '') out[k] = v.trim()
  }
  return out
}

async function load(id: number | null) {
  if (!id) { providers.value = []; usage.value = null; return }
  loading.value = true
  try {
    providers.value = (await dnsProviderApi.list(id)).data.data ?? []
    usage.value = (await usageApi.get(id)).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, (id) => { load(id); ensureCatalog(id) }, { immediate: true })

function openConnect() {
  name.value = ''
  ptype.value = ''
  creds.value = {}
  testZone.value = ''
  ensureCatalog(currentWorkspaceId.value)
  showConnect.value = true
}

async function connect() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    await dnsProviderApi.connect(currentWorkspaceId.value, {
      name: name.value.trim(), type: ptype.value, credentials: filled(creds.value),
      test_zone: testZone.value.trim() || undefined,
    })
    notify.success('DNS provider connected')
    showConnect.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

// Rotate: replace a stored credential without disconnecting, which would unlink domains.
const rotating = ref<DNSProvider | null>(null)
const rotateCreds = ref<Record<string, string>>({})
const rotateZone = ref('')
const rotateSaving = ref(false)
const rotateDescriptor = computed(() => (rotating.value ? describe(rotating.value.type) : null))
const rotateComplete = computed(() =>
  (rotateDescriptor.value?.fields ?? []).every((f) => !f.required || (rotateCreds.value[f.key] ?? '').trim() !== ''),
)

function openRotate(p: DNSProvider) {
  rotateCreds.value = {}
  rotateZone.value = ''
  ensureCatalog(currentWorkspaceId.value)
  rotating.value = p
}

async function rotate() {
  const p = rotating.value
  if (!currentWorkspaceId.value || !p) return
  rotateSaving.value = true
  try {
    await dnsProviderApi.update(currentWorkspaceId.value, p.id, {
      credentials: filled(rotateCreds.value),
      test_zone: rotateZone.value.trim() || undefined,
    })
    notify.success('Credentials rotated')
    rotating.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    rotateSaving.value = false
  }
}

// Test dialog: pick a provider and a zone (one of the user's domains) to probe.
const testProvider = ref<DNSProvider | null>(null)
const probeZone = ref('')
const probing = ref(false)
function openTest(p: DNSProvider) {
  testProvider.value = p
  probeZone.value = ''
}
async function test() {
  const p = testProvider.value
  if (!currentWorkspaceId.value || !p) return
  const zone = probeZone.value.trim()
  if (!zone) return
  testProvider.value = null
  probing.value = true
  try {
    const updated = (await dnsProviderApi.test(currentWorkspaceId.value, p.id, zone)).data.data
    const i = providers.value.findIndex(x => x.id === p.id)
    if (i >= 0) providers.value[i] = updated
    notify.success('Connection OK')
  } catch (e) {
    notify.apiError(e)
    load(currentWorkspaceId.value)
  } finally {
    probing.value = false
  }
}

const pendingDisconnect = ref<DNSProvider | null>(null)
const disconnecting = ref(false)
async function disconnect() {
  const p = pendingDisconnect.value
  if (!currentWorkspaceId.value || !p) return
  pendingDisconnect.value = null
  disconnecting.value = true
  try {
    await dnsProviderApi.remove(currentWorkspaceId.value, p.id)
    notify.success('DNS provider disconnected')
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    disconnecting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>DNS Providers</h1>
        <p class="subtitle">Connect a DNS host so {{ ws.contextLabel }} can automate ownership verification and app records.</p>
      </div>
      <button v-if="ws.canEdit && allowed" class="btn btn-primary" @click="openConnect">
        <span class="mdi mdi-plus"></span> Connect provider
      </button>
    </div>

    <div v-if="!allowed" class="card">
      <div class="card-body" style="color: var(--warning, #d97706)">
        <span class="mdi mdi-lock-outline"></span>
        Connecting DNS providers isn't included in this workspace's plan.
      </div>
    </div>

    <div class="card">
      <div v-if="loading && providers.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="providers.length === 0" class="empty-state">
        <span class="mdi mdi-dns" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No DNS providers</h3>
        <p>Connect Cloudflare, Route 53, or DigitalOcean to automate DNS.</p>
        <button v-if="ws.canEdit && allowed" class="btn btn-primary mt-4" @click="openConnect">Connect a provider</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>Type</th><th>Status</th><th></th></tr></thead>
          <tbody>
            <tr v-for="p in providers" :key="p.id">
              <td><span class="cell-title">{{ p.display_name || p.name }}</span></td>
              <td class="cell-sub">{{ typeLabel(p.type) }}</td>
              <td>
                <span class="badge" :class="p.status === 'ok' ? 'badge-success' : 'badge-danger'">{{ p.status }}</span>
                <span v-if="p.last_error" class="cell-sub" :title="p.last_error" style="margin-left: 8px">⚠</span>
              </td>
              <td style="text-align: right">
                <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" @click="openTest(p)">Test</button>
                <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" style="margin-left: 8px" @click="openRotate(p)">Rotate</button>
                <button v-if="ws.canEdit" class="btn btn-danger btn-sm" style="margin-left: 8px" @click="pendingDisconnect = p">Disconnect</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showConnect" @close="showConnect = false">
        <div class="modal-header">
          <h3>Connect DNS provider</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showConnect = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="connect">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="name" class="form-input" placeholder="e.g. cloudflare-prod" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label" for="dns-type">Type</label>
              <select
                id="dns-type"
                class="form-select"
                :value="ptype"
                @change="selectType(($event.target as HTMLSelectElement).value)"
              >
                <option value="" disabled>Select a provider…</option>
                <option v-for="d in catalog ?? []" :key="d.type" :value="d.type">{{ d.label }}</option>
              </select>
            </div>

            <template v-if="descriptor">
              <DnsProviderField
                v-for="f in descriptor.fields"
                :key="f.key"
                :field="f"
                :model-value="creds[f.key] ?? ''"
                @update:model-value="creds[f.key] = $event"
              />
              <p v-if="descriptor.docs_url" class="form-hint">
                <a :href="descriptor.docs_url" target="_blank" rel="noopener">Where to create these credentials</a>
              </p>
            </template>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">Test zone <span class="text-muted">(optional)</span></label>
              <input v-model="testZone" class="form-input" placeholder="example.com" style="font-family: monospace" />
              <p class="form-hint">A domain on this provider; if set, the credential is verified before saving.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showConnect = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !ptype || !credsComplete">{{ saving ? 'Connecting…' : 'Connect' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <Teleport to="body">
      <AppModal v-if="rotating" @close="rotating = null">
        <div class="modal-header">
          <h3>Rotate credentials — {{ rotating.display_name || rotating.name }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="rotating = null"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="rotate">
          <div class="modal-body">
            <p class="form-hint" style="margin-top: 0">
              Replaces the stored credentials for {{ typeLabel(rotating.type) }}. Domains stay linked —
              disconnecting instead would unlink them.
            </p>
            <template v-if="rotateDescriptor">
              <DnsProviderField
                v-for="f in rotateDescriptor.fields"
                :key="f.key"
                :field="f"
                :model-value="rotateCreds[f.key] ?? ''"
                @update:model-value="rotateCreds[f.key] = $event"
              />
            </template>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label" for="rotate-zone">Test zone <span class="text-muted">(optional)</span></label>
              <input id="rotate-zone" v-model="rotateZone" class="form-input" placeholder="example.com" style="font-family: monospace" />
              <p class="form-hint">If set, the new credential is verified before it replaces the old one.</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="rotating = null">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="rotateSaving || !rotateComplete">
              {{ rotateSaving ? 'Rotating…' : 'Rotate' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!testProvider"
      title="Test DNS provider"
      :message="`Enter one of your domains on ${testProvider ? typeLabel(testProvider.type) : ''} to test ${testProvider?.name}.`"
      confirm-label="Test"
      variant="primary"
      :busy="probing"
      :confirm-disabled="!probeZone.trim()"
      @confirm="test"
      @cancel="testProvider = null"
    >
      <div class="form-group" style="margin-top: 12px; margin-bottom: 0">
        <label class="form-label" for="probe-zone">Domain</label>
        <input id="probe-zone" v-model="probeZone" class="form-input" placeholder="example.com" style="font-family: monospace" @keydown.enter.prevent="test" />
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :open="!!pendingDisconnect"
      title="Disconnect DNS provider?"
      :message="`Disconnect &quot;${pendingDisconnect?.name}&quot;? Existing DNS records are left untouched.`"
      confirm-label="Disconnect"
      variant="danger"
      :busy="disconnecting"
      @confirm="disconnect"
      @cancel="pendingDisconnect = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
</style>
