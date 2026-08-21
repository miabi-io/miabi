<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { domainApi, type DomainInput } from '@/api/domains'
import { dnsProviderApi } from '@/api/dns'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'
import type { Domain, DomainTLSMode, DNSProvider } from '@/api/types'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Domain[]>([])
const dnsProviders = ref<DNSProvider[]>([])
const loading = ref(false)
const verifying = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<Domain | null>(null)
const form = ref<DomainInput>(emptyForm())

// DNS setup dialog
const showDns = ref(false)
const dnsDomain = ref<Domain | null>(null)

function emptyForm(): DomainInput {
  return { name: '', tls_mode: 'acme', wildcard: false }
}

async function load(id: number | null) {
  if (!id) { items.value = []; dnsProviders.value = []; return }
  loading.value = true
  try {
    items.value = (await domainApi.list(id)).data.data ?? []
    dnsProviders.value = (await dnsProviderApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

// setProvider links/unlinks a DNS provider so Miabi automates the records.
async function setProvider(d: Domain, raw: string) {
  if (!currentWorkspaceId.value) return
  const pid = raw === '' ? null : Number(raw)
  try {
    const updated = (await domainApi.setDnsProvider(currentWorkspaceId.value, d.id, pid)).data.data
    const i = items.value.findIndex(x => x.id === d.id)
    if (i >= 0) items.value[i] = updated
    notify.success(pid ? 'DNS provider linked — verification is now automatic' : 'Reverted to manual DNS')
  } catch (e) {
    notify.apiError(e)
  }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showModal.value = true
}
function openEdit(d: Domain) {
  editing.value = d
  form.value = { name: d.name, tls_mode: d.tls_mode, wildcard: d.wildcard }
  showModal.value = true
}

async function save() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    if (editing.value) {
      await domainApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success('Domain updated')
    } else {
      const d = (await domainApi.create(currentWorkspaceId.value, form.value)).data.data
      notify.success('Domain registered')
      dnsDomain.value = d
      showDns.value = true // surface the TXT record to add next
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

function openDns(d: Domain) {
  dnsDomain.value = d
  showDns.value = true
}

async function verify(d: Domain) {
  if (!currentWorkspaceId.value) return
  verifying.value = d.id
  try {
    const updated = (await domainApi.verify(currentWorkspaceId.value, d.id)).data.data
    const idx = items.value.findIndex((i) => i.id === d.id)
    if (idx >= 0) items.value[idx] = updated
    if (dnsDomain.value?.id === d.id) dnsDomain.value = updated
    notify.success(`${d.name} verified`)
    showDns.value = false
  } catch (e) {
    notify.apiError(e, 'DNS record not found yet — it can take a few minutes to propagate.')
  } finally {
    verifying.value = null
  }
}

const toDelete = ref<Domain | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  const id = currentWorkspaceId.value
  if (!id || !toDelete.value) return
  deleting.value = true
  try {
    await domainApi.remove(id, toDelete.value.id)
    notify.success('Domain deleted')
    toDelete.value = null
    load(id)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}

const tlsModes: { value: DomainTLSMode; label: string }[] = [
  { value: 'acme', label: 'Automatic (Let’s Encrypt)' },
  { value: 'custom', label: 'Custom certificate' },
]
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Domains</h1>
        <p class="subtitle">Owned hostnames — verify ownership over DNS and set a default TLS policy.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> Add domain
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-web" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No domains yet</h3>
        <p>Register a domain you own, add a TXT record, and routes can serve it with automatic TLS.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Add a domain</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Domain</th><th>TLS</th><th>DNS</th><th>Status</th><th></th></tr></thead>
          <tbody>
            <tr v-for="d in items" :key="d.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-web" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ d.name }}</span>
                    <span v-if="d.wildcard" class="cell-sub">wildcard · *.{{ d.name }}</span>
                  </span>
                </div>
              </td>
              <td><span class="badge badge-neutral">{{ d.tls_mode === 'acme' ? 'automatic' : 'custom' }}</span></td>
              <td>
                <select
                  v-if="ws.canEdit && dnsProviders.length"
                  class="form-select form-select-sm"
                  :value="d.dns_provider_id == null ? '' : String(d.dns_provider_id)"
                  @change="setProvider(d, ($event.target as HTMLSelectElement).value)"
                >
                  <option value="">Manual</option>
                  <option v-for="p in dnsProviders" :key="p.id" :value="String(p.id)">{{ p.name }}</option>
                </select>
                <span v-else class="badge" :class="d.automated ? 'badge-success' : 'badge-neutral'">{{ d.automated ? 'automated' : 'manual' }}</span>
              </td>
              <td>
                <span v-if="d.banned" class="badge badge-danger" title="Banned by a platform administrator"><span class="mdi mdi-cancel"></span> banned</span>
                <span v-else-if="d.verified" class="badge badge-success"><span class="mdi mdi-check-decagram"></span> verified</span>
                <span v-else class="badge badge-warning"><span class="mdi mdi-clock-alert-outline"></span> pending</span>
              </td>
              <td class="text-right table-actions">
                <button v-if="!d.verified && !d.banned" class="btn-icon btn-icon-muted" title="DNS setup" aria-label="DNS setup" @click="openDns(d)"><span class="mdi mdi-dns-outline"></span></button>
                <button v-if="ws.canEdit && !d.verified && !d.banned" class="btn-icon btn-icon-muted" title="Verify now" aria-label="Verify now" :disabled="verifying === d.id" @click="verify(d)">
                  <span class="mdi" :class="verifying === d.id ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(d)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="toDelete = d"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create / edit -->
    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit domain' : 'Add domain' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Domain name</label>
              <input v-model="form.name" class="form-input mono" placeholder="example.com" required :autofocus="!editing" :disabled="!!editing" />
              <p v-if="editing" class="hint">The name is fixed once a domain is added. To change it, delete this domain and add the correct one.</p>
              <p v-else class="hint">The apex or a subdomain you control. A leading <code>*.</code> is treated as wildcard.</p>
            </div>
            <div class="form-group">
              <label class="form-label">Default TLS</label>
              <div class="tabs" style="margin-bottom: 0">
                <button v-for="t in tlsModes" :key="t.value" type="button" class="tab" :class="{ active: form.tls_mode === t.value }" @click="form.tls_mode = t.value">{{ t.label }}</button>
              </div>
            </div>
            <label class="check"><input type="checkbox" v-model="form.wildcard" /> <span>Wildcard — also cover <code>*.{{ form.name || 'example.com' }}</code></span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Add domain') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- DNS verification -->
    <Teleport to="body">
      <AppModal v-if="showDns && dnsDomain" @close="showDns = false">
        <div class="modal-header">
          <h3>Verify {{ dnsDomain.name }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showDns = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <div v-if="dnsDomain.verified" class="gate gate-ok">
            <span class="mdi mdi-check-decagram"></span> Ownership verified.
          </div>
          <template v-else-if="dnsDomain.automated">
            <p class="note"><span class="mdi mdi-auto-fix"></span> A DNS provider is connected — Miabi creates the verification record for you. Just click <strong>Verify</strong>.</p>
          </template>
          <template v-else>
            <p class="note">Add this <strong>TXT</strong> record at your DNS provider, then click Verify. Propagation can take a few minutes. <em>Tip: connect a DNS provider (the DNS column) to skip this step.</em></p>
            <div class="dns-field">
              <span class="dns-label">Type</span>
              <code class="dns-value">TXT</code>
            </div>
            <div class="dns-field">
              <span class="dns-label">Name / Host</span>
              <code class="dns-value">{{ dnsDomain.challenge_host }}</code>
              <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(dnsDomain.challenge_host)"><span class="mdi mdi-content-copy"></span></button>
            </div>
            <div class="dns-field">
              <span class="dns-label">Value</span>
              <code class="dns-value">{{ dnsDomain.challenge_value }}</code>
              <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(dnsDomain.challenge_value)"><span class="mdi mdi-content-copy"></span></button>
            </div>
          </template>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="showDns = false">Close</button>
          <button v-if="ws.canEdit && !dnsDomain.verified" type="button" class="btn btn-primary" :disabled="verifying === dnsDomain.id" @click="verify(dnsDomain)">
            <span class="mdi" :class="verifying === dnsDomain.id ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span> Verify
          </button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete domain"
      :message="`Delete domain &quot;${toDelete?.name}&quot;? Routes using a host under it can no longer be created until it is re-added.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.mono { font-family: 'JetBrains Mono', monospace; }
.badge .mdi { font-size: 13px; }
.hint { font-size: 12px; color: var(--text-muted); margin-top: 6px; }
.hint code, .check code { font-family: 'JetBrains Mono', monospace; background: var(--bg-tertiary); padding: 1px 5px; border-radius: 4px; }
.check { display: flex; align-items: center; gap: 8px; font-size: 13px; margin-top: 10px; cursor: pointer; }
.note { font-size: 13px; color: var(--text-muted); margin-bottom: 12px; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.gate { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-radius: 8px; font-size: 13px; }
.gate .mdi { font-size: 18px; }
.gate-ok { background: var(--success-50); color: var(--success-600); }
.dns-field { display: flex; align-items: center; gap: 10px; padding: 6px 0; }
.dns-label { width: 92px; font-size: 12px; color: var(--text-muted); flex-shrink: 0; }
.dns-value { flex: 1; font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 6px 10px; border-radius: 6px; overflow-x: auto; white-space: nowrap; }
</style>
