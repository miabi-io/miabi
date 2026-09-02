<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { domainApi, type DomainInput } from '@/api/domains'
import { dnsProviderApi } from '@/api/dns'
import { copyText } from '@/utils/clipboard'
import type { DomainDetail, DomainTLSMode, DNSProvider } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const domainId = computed(() => Number(route.params.id))
const item = ref<DomainDetail | null>(null)
const dnsProviders = ref<DNSProvider[]>([])
const loading = ref(false)
const verifying = ref(false)

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid || !domainId.value) return
  loading.value = true
  try {
    item.value = (await domainApi.get(wid, domainId.value)).data.data
    dnsProviders.value = (await dnsProviderApi.list(wid)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
    router.replace('/domains')
  } finally {
    loading.value = false
  }
}
watch([domainId, currentWorkspaceId], load, { immediate: true })

// --- Ownership presentation (mirrors the Domains list) ---

function verifiedLabel(d: DomainDetail): string {
  if (d.verified_via === 'admin') return 'verified · admin override'
  if (d.verified_via === 'dns_provider') return 'verified · DNS provider'
  return 'verified'
}

// proofPresent reports whether the last ownership check found the TXT record.
function proofPresent(d: DomainDetail): boolean {
  return !!d.verification_checked_at && !d.verification_error
}

function lastChecked(d: DomainDetail): string {
  if (!d.verification_checked_at) return 'never checked'
  const secs = Math.max(0, Math.round((Date.now() - new Date(d.verification_checked_at).getTime()) / 1000))
  if (secs < 60) return 'checked just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `checked ${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `checked ${hours}h ago`
  return `checked ${Math.round(hours / 24)}d ago`
}

function fmtDate(v?: string | null): string {
  return v ? new Date(v).toLocaleString() : '—'
}

const routeStatusClass: Record<string, string> = {
  live: 'badge-success', pending: 'badge-warning', offline: 'badge-neutral', error: 'badge-danger',
}

// --- Actions ---

async function verify() {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  verifying.value = true
  try {
    await domainApi.verify(wid, item.value.id)
    notify.success(`${item.value.name} verified`)
    load() // the routes this domain gates may have just gone live
  } catch (e) {
    notify.apiError(e, 'Verification failed — check the TXT record and try again')
  } finally {
    verifying.value = false
  }
}

async function setProvider(raw: string) {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  const pid = raw === '' ? null : Number(raw)
  try {
    await domainApi.setDnsProvider(wid, item.value.id, pid)
    notify.success(pid ? 'DNS provider linked — verification is now automatic' : 'Reverted to manual DNS')
    load()
  } catch (e) { notify.apiError(e) }
}

async function copy(v: string) {
  await copyText(v)
  notify.success('Copied')
}

// --- Edit ---
const showEdit = ref(false)
const saving = ref(false)
const form = ref<DomainInput>({ name: '', tls_mode: 'acme', wildcard: false })
const tlsModes: { value: DomainTLSMode; label: string }[] = [
  { value: 'acme', label: 'Automatic (Let’s Encrypt)' },
  { value: 'custom', label: 'Custom certificate' },
]
function openEdit() {
  if (!item.value) return
  form.value = { name: item.value.name, tls_mode: item.value.tls_mode, wildcard: item.value.wildcard }
  showEdit.value = true
}
async function save() {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  saving.value = true
  try {
    // The name round-trips unchanged: the API rejects a rename, and omitting it fails validation.
    await domainApi.update(wid, item.value.id, form.value)
    notify.success('Domain updated')
    showEdit.value = false
    load()
  } catch (e) { notify.apiError(e) }
  finally { saving.value = false }
}

// --- Delete ---
const showDelete = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  deleting.value = true
  try {
    await domainApi.remove(wid, item.value.id)
    notify.success('Domain deleted')
    router.replace('/domains')
  } catch (e) { notify.apiError(e) }
  finally { deleting.value = false }
}
</script>

<template>
  <div v-if="item">
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" title="Back" aria-label="Back" @click="router.push('/domains')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ item.name }}</h1>
          <span class="cell-sub">{{ item.wildcard ? `also covers *.${item.name}` : 'apex / subdomain only' }}</span>
        </div>
        <span v-if="item.banned" class="badge badge-danger" title="Banned by a platform administrator">
          <span class="mdi mdi-cancel"></span> banned
        </span>
        <span v-else-if="item.verified" class="badge" :class="item.verified_via === 'admin' ? 'badge-info' : 'badge-success'">
          <span class="mdi" :class="item.verified_via === 'admin' ? 'mdi-shield-account-outline' : 'mdi-check-decagram'"></span>
          {{ verifiedLabel(item) }}
        </span>
        <span v-else-if="item.serving_unverified" class="badge badge-info">
          <span class="mdi mdi-shield-star-outline"></span> serving · unverified
        </span>
        <span v-else class="badge badge-warning"><span class="mdi mdi-clock-alert-outline"></span> pending</span>
      </div>
      <div v-if="ws.canEdit" class="flex items-center gap-2">
        <button v-if="!item.banned" class="btn btn-secondary" :disabled="verifying" @click="verify">
          <span class="mdi" :class="verifying ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span>
          {{ item.verified ? 'Re-check' : 'Verify' }}
        </button>
        <button class="btn btn-secondary" @click="openEdit"><span class="mdi mdi-pencil-outline"></span> Edit</button>
        <button class="btn btn-danger" @click="showDelete = true"><span class="mdi mdi-delete-outline"></span> Delete</button>
      </div>
    </div>

    <div v-if="item.banned" class="gate gate-bad mb-4">
      <span class="mdi mdi-cancel"></span>
      <span>Banned by a platform administrator{{ item.ban_reason ? `: ${item.ban_reason}` : '' }}. Its routes are forced offline and it cannot be verified.</span>
    </div>
    <div v-else-if="item.verified && item.verified_via === 'admin'" class="gate gate-warn mb-4">
      <span class="mdi mdi-shield-account-outline"></span>
      <span>Verified by a platform administrator, not by DNS. This is never revoked automatically — publish the record below and it converts to a DNS proof on its own.</span>
    </div>
    <div v-else-if="item.serving_unverified" class="gate gate-warn mb-4">
      <span class="mdi mdi-shield-star-outline"></span>
      <span>Serving because this workspace is privileged — ownership is still unproven. Add the record below to verify it properly.</span>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Ownership</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row">
          <span class="detail-key">Last check</span>
          <span>
            <span class="mdi" :class="proofPresent(item) ? 'mdi-check-circle-outline check-ok' : 'mdi-alert-circle-outline check-bad'"></span>
            {{ proofPresent(item) ? 'TXT record found' : 'TXT record not found' }} · {{ lastChecked(item) }}
          </span>
        </div>
        <div class="detail-row"><span class="detail-key">Verified at</span><span>{{ fmtDate(item.verified_at) }}</span></div>
        <div v-if="item.verification_error" class="detail-row">
          <span class="detail-key">Last error</span><span class="check-bad">{{ item.verification_error }}</span>
        </div>
        <div class="detail-row">
          <span class="detail-key">DNS provider</span>
          <select
            v-if="ws.canEdit && dnsProviders.length"
            class="form-input form-input-sm"
            :value="item.dns_provider_id == null ? '' : String(item.dns_provider_id)"
            @change="setProvider(($event.target as HTMLSelectElement).value)"
          >
            <option value="">Manual (no provider)</option>
            <option v-for="p in dnsProviders" :key="p.id" :value="String(p.id)">{{ p.name }}</option>
          </select>
          <span v-else>{{ item.automated ? 'Connected — Miabi maintains the records' : 'Manual' }}</span>
        </div>
      </div>
    </div>

    <!-- The record stays visible after verification: this is the panel people read
         when a domain breaks, not a one-time setup wizard. -->
    <div class="card mb-4">
      <div class="card-header"><h2>DNS record</h2></div>
      <div class="card-body">
        <p v-if="item.automated" class="note"><span class="mdi mdi-auto-fix"></span> A DNS provider is connected — Miabi creates and maintains this record for you.</p>
        <p v-else-if="!item.verified" class="note">Add this <strong>TXT</strong> record at your DNS host, then click Verify. Propagation can take a few minutes.</p>
        <p v-else class="note">This is the record that proves ownership. Keep it in place — removing it un-verifies the domain and takes its routes offline.</p>
        <div class="dns-field">
          <span class="dns-label">Type</span>
          <code class="dns-value">TXT</code>
        </div>
        <div class="dns-field">
          <span class="dns-label">Name / Host</span>
          <code class="dns-value">{{ item.challenge_host }}</code>
          <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(item.challenge_host)"><span class="mdi mdi-content-copy"></span></button>
        </div>
        <div class="dns-field">
          <span class="dns-label">Value</span>
          <code class="dns-value">{{ item.challenge_value }}</code>
          <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(item.challenge_value)"><span class="mdi mdi-content-copy"></span></button>
        </div>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Routes</h2></div>
      <div class="card-body">
        <div v-if="item.routes.length" class="used-list">
          <router-link v-for="r in item.routes" :key="r.id" :to="`/routes/${r.id}`" class="used-row">
            <span class="mdi mdi-routes"></span>
            <span class="used-name">{{ r.name }}</span>
            <span class="cell-sub mono">{{ r.hosts.join(', ') }}</span>
            <span class="badge" :class="routeStatusClass[r.status] || 'badge-neutral'" :title="r.status_reason || ''">{{ r.status }}</span>
          </router-link>
        </div>
        <p v-else class="text-muted text-sm" style="margin: 0">
          No route uses a host under this domain yet. The domain can be safely deleted.
        </p>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>Settings</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row"><span class="detail-key">Default TLS</span><span>{{ item.tls_mode === 'acme' ? 'Automatic (Let’s Encrypt)' : 'Custom certificate' }}</span></div>
        <div class="detail-row"><span class="detail-key">Wildcard</span><span>{{ item.wildcard ? 'Yes' : 'No' }}</span></div>
        <div class="detail-row"><span class="detail-key">Added</span><span>{{ fmtDate(item.created_at) }}</span></div>
        <div class="detail-row"><span class="detail-key">Updated</span><span>{{ fmtDate(item.updated_at) }}</span></div>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showEdit" @close="showEdit = false">
        <div class="modal-header">
          <h3>Edit domain</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showEdit = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Domain name</label>
              <input v-model="form.name" class="form-input mono" disabled />
              <p class="form-hint">The name is fixed once a domain is added. To change it, delete this domain and add the correct one.</p>
            </div>
            <div class="form-group">
              <label class="form-label">Default TLS</label>
              <div class="tabs" style="margin-bottom: 0">
                <button v-for="t in tlsModes" :key="t.value" type="button" class="tab" :class="{ active: form.tls_mode === t.value }" @click="form.tls_mode = t.value">{{ t.label }}</button>
              </div>
            </div>
            <label class="check"><input type="checkbox" v-model="form.wildcard" /> <span>Wildcard — also cover <code>*.{{ item.name }}</code></span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEdit = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="showDelete"
      title="Delete domain"
      :message="`Delete domain &quot;${item.name}&quot;? Routes using a host under it can no longer be created until it is re-added.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="showDelete = false"
    />
  </div>
  <div v-else-if="loading" class="card"><div class="card-body"><span class="spinner"></span></div></div>
</template>

<style scoped>
.title-group { display: flex; align-items: center; gap: 12px; }
.title-group h1 { margin: 0; line-height: 1.2; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.text-muted { color: var(--text-muted); }
.note { font-size: 13px; color: var(--text-muted); margin-bottom: 12px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.detail-list { display: flex; flex-direction: column; }
.detail-row { display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 12px 0; border-bottom: 1px solid var(--border-primary); font-size: 13px; }
.detail-row:last-child { border-bottom: none; }
.detail-key { color: var(--text-muted); }
.form-input-sm { width: auto; min-width: 220px; padding: 4px 8px; font-size: 12px; }
.gate { display: flex; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: 8px; font-size: 13px; line-height: 1.5; }
.gate .mdi { font-size: 18px; flex-shrink: 0; }
.gate-warn { background: var(--warning-50); color: var(--warning-600); }
.gate-bad { background: var(--danger-50); color: var(--danger-600); }
.check-ok { color: var(--success-500); }
.check-bad { color: var(--warning-500); }
.used-list { display: flex; flex-direction: column; gap: 6px; }
.used-row { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-primary); text-decoration: none; }
.used-row:hover .used-name { text-decoration: underline; }
.used-row .cell-sub { flex: 1; }
.dns-field { display: flex; align-items: center; gap: 10px; padding: 6px 0; }
.dns-label { width: 92px; font-size: 12px; color: var(--text-muted); flex-shrink: 0; }
.dns-value { flex: 1; font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 6px 10px; border-radius: 6px; overflow-x: auto; white-space: nowrap; }
</style>
