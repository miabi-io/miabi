<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { dnsProviderApi } from '@/api/dns'
import { usageApi } from '@/api/resources'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { DNSProvider, DNSProviderType, WorkspaceUsage } from '@/api/types'

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

// Connect form.
const name = ref('')
const ptype = ref<DNSProviderType>('cloudflare')
const apiToken = ref('')
const accessKeyId = ref('')
const secretAccessKey = ref('')
const region = ref('')
const testZone = ref('')

const TYPE_LABEL: Record<DNSProviderType, string> = {
  cloudflare: 'Cloudflare',
  route53: 'AWS Route 53',
  digitalocean: 'DigitalOcean',
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
watch(currentWorkspaceId, load, { immediate: true })

function openConnect() {
  name.value = ''
  ptype.value = 'cloudflare'
  apiToken.value = ''
  accessKeyId.value = ''; secretAccessKey.value = ''; region.value = ''
  testZone.value = ''
  showConnect.value = true
}

async function connect() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    const credentials = ptype.value === 'route53'
      ? { access_key_id: accessKeyId.value.trim(), secret_access_key: secretAccessKey.value.trim(), region: region.value.trim() }
      : { api_token: apiToken.value.trim() }
    await dnsProviderApi.connect(currentWorkspaceId.value, {
      name: name.value.trim(), type: ptype.value, credentials,
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
              <td class="cell-sub">{{ TYPE_LABEL[p.type] }}</td>
              <td>
                <span class="badge" :class="p.status === 'ok' ? 'badge-success' : 'badge-danger'">{{ p.status }}</span>
                <span v-if="p.last_error" class="cell-sub" :title="p.last_error" style="margin-left: 8px">⚠</span>
              </td>
              <td style="text-align: right">
                <button v-if="ws.canEdit" class="btn btn-secondary btn-sm" @click="openTest(p)">Test</button>
                <button v-if="ws.canEdit" class="btn btn-danger btn-sm" style="margin-left: 8px" @click="pendingDisconnect = p">Disconnect</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showConnect" class="modal-overlay">
        <div class="modal">
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
                <label class="form-label">Type</label>
                <select v-model="ptype" class="form-select">
                  <option value="cloudflare">Cloudflare</option>
                  <option value="route53">AWS Route 53</option>
                  <option value="digitalocean">DigitalOcean</option>
                </select>
              </div>
              <template v-if="ptype === 'route53'">
                <div class="form-group">
                  <label class="form-label">Access key ID</label>
                  <input v-model="accessKeyId" class="form-input" autocomplete="off" required style="font-family: monospace" />
                </div>
                <div class="form-group">
                  <label class="form-label">Secret access key</label>
                  <input v-model="secretAccessKey" type="password" class="form-input" autocomplete="new-password" required />
                </div>
                <div class="form-group">
                  <label class="form-label">Region <span class="text-muted">(optional)</span></label>
                  <input v-model="region" class="form-input" placeholder="us-east-1" style="font-family: monospace" />
                </div>
              </template>
              <template v-else>
                <div class="form-group">
                  <label class="form-label">API token</label>
                  <input v-model="apiToken" type="password" class="form-input" autocomplete="new-password" required />
                  <p class="form-hint">Use a scoped token (Cloudflare: Zone.DNS edit on your zone).</p>
                </div>
              </template>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Test zone <span class="text-muted">(optional)</span></label>
                <input v-model="testZone" class="form-input" placeholder="example.com" style="font-family: monospace" />
                <p class="form-hint">A domain on this provider; if set, the credential is verified before saving.</p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showConnect = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Connecting…' : 'Connect' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!testProvider"
      title="Test DNS provider"
      :message="`Enter one of your domains on ${testProvider ? TYPE_LABEL[testProvider.type] : ''} to test ${testProvider?.name}.`"
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
