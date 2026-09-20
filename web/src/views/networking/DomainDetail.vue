<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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
const { t } = useI18n()
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
    notify.success(t('notify.domainDetail.verified', { name: item.value.name }))
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
    notify.success(t(pid ? 'notify.domainDetail.providerLinked' : 'notify.domainDetail.providerCleared'))
    load()
  } catch (e) { notify.apiError(e) }
}

async function copy(v: string) {
  await copyText(v)
  notify.success(t('notify.common.copied'))
}

// --- Edit ---
const showEdit = ref(false)
const saving = ref(false)
const form = ref<DomainInput>({ name: '', tls_mode: 'acme', wildcard: false })
const tlsModes: { value: DomainTLSMode; label: string }[] = [
  { value: 'acme', label: t('domains.tls.acme') },
  { value: 'custom', label: t('domainDetail.tlsCustom') },
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
    notify.success(t('notify.domainDetail.updated'))
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
    notify.success(t('notify.domainDetail.deleted'))
    router.replace('/domains')
  } catch (e) { notify.apiError(e) }
  finally { deleting.value = false }
}
</script>

<template>
  <div v-if="item">
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" :title="$t('domainDetail.back')" :aria-label="$t('domainDetail.back')" @click="router.push('/domains')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ item.name }}</h1>
          <span class="cell-sub">{{ item.wildcard ? `also covers *.${item.name}` : 'apex / subdomain only' }}</span>
        </div>
        <span v-if="item.banned" class="badge badge-danger" :title="$t('domains.bannedHint')">
          <span class="mdi mdi-cancel"></span>{{ $t('domainDetail.banned') }}</span>
        <span v-else-if="item.verified" class="badge" :class="item.verified_via === 'admin' ? 'badge-info' : 'badge-success'">
          <span class="mdi" :class="item.verified_via === 'admin' ? 'mdi-shield-account-outline' : 'mdi-check-decagram'"></span>
          {{ verifiedLabel(item) }}
        </span>
        <span v-else-if="item.serving_unverified" class="badge badge-info">
          <span class="mdi mdi-shield-star-outline"></span>{{ $t('domains.servingUnverifiedBadge') }}</span>
        <span v-else class="badge badge-warning"><span class="mdi mdi-clock-alert-outline"></span>{{ $t('domainDetail.pending') }}</span>
      </div>
      <div v-if="ws.canEdit" class="flex items-center gap-2">
        <button v-if="!item.banned" class="btn btn-secondary" :disabled="verifying" @click="verify">
          <span class="mdi" :class="verifying ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span>
          {{ item.verified ? 'Re-check' : 'Verify' }}
        </button>
        <button class="btn btn-secondary" @click="openEdit"><span class="mdi mdi-pencil-outline"></span>{{ $t('action.edit') }}</button>
        <button class="btn btn-danger" @click="showDelete = true"><span class="mdi mdi-delete-outline"></span>{{ $t('action.delete') }}</button>
      </div>
    </div>

    <div v-if="item.banned" class="gate gate-bad mb-4">
      <span class="mdi mdi-cancel"></span>
      <span>Banned by a platform administrator{{ item.ban_reason ? `: ${item.ban_reason}` : '' }}. Its routes are forced offline and it cannot be verified.</span>
    </div>
    <div v-else-if="item.verified && item.verified_via === 'admin'" class="gate gate-warn mb-4">
      <span class="mdi mdi-shield-account-outline"></span>
      <span>{{ $t('domainDetail.adminVerifiedHint') }}</span>
    </div>
    <div v-else-if="item.serving_unverified" class="gate gate-warn mb-4">
      <span class="mdi mdi-shield-star-outline"></span>
      <span>{{ $t('domains.servingUnverified') }}</span>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>{{ $t('domainDetail.ownership') }}</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row">
          <span class="detail-key">{{ $t('gitops.lastCheck') }}</span>
          <span>
            <span class="mdi" :class="proofPresent(item) ? 'mdi-check-circle-outline check-ok' : 'mdi-alert-circle-outline check-bad'"></span>
            {{ proofPresent(item) ? 'TXT record found' : 'TXT record not found' }} · {{ lastChecked(item) }}
          </span>
        </div>
        <div class="detail-row"><span class="detail-key">{{ $t('domainDetail.verifiedAt') }}</span><span>{{ fmtDate(item.verified_at) }}</span></div>
        <div v-if="item.verification_error" class="detail-row">
          <span class="detail-key">{{ $t('domainDetail.lastError') }}</span><span class="check-bad">{{ item.verification_error }}</span>
        </div>
        <div class="detail-row">
          <span class="detail-key">{{ $t('domainDetail.dnsProvider') }}</span>
          <select
            v-if="ws.canEdit && dnsProviders.length"
            class="form-input form-input-sm"
            :value="item.dns_provider_id == null ? '' : String(item.dns_provider_id)"
            @change="setProvider(($event.target as HTMLSelectElement).value)"
          >
            <option value="">{{ $t('domainDetail.manualNoProvider') }}</option>
            <option v-for="p in dnsProviders" :key="p.id" :value="String(p.id)">{{ p.name }}</option>
          </select>
          <span v-else>{{ item.automated ? 'Connected — Miabi maintains the records' : 'Manual' }}</span>
        </div>
      </div>
    </div>

    <!-- The record stays visible after verification: this is the panel people read
         when a domain breaks, not a one-time setup wizard. -->
    <div class="card mb-4">
      <div class="card-header"><h2>{{ $t('domainDetail.dnsRecord') }}</h2></div>
      <div class="card-body">
        <p v-if="item.automated" class="note"><span class="mdi mdi-auto-fix"></span>{{ $t('domains.providerConnected') }}</p>
        <i18n-t v-else-if="!item.verified" keypath="domainDetail.txtHint" tag="p" class="note"><template #type><strong>TXT</strong></template></i18n-t>
        <p v-else class="note">{{ $t('domains.keepRecord') }}</p>
        <div class="dns-field">
          <span class="dns-label">{{ $t('domainDetail.type') }}</span>
          <code class="dns-value">TXT</code>
        </div>
        <div class="dns-field">
          <span class="dns-label">{{ $t('domains.dns.host') }}</span>
          <code class="dns-value">{{ item.challenge_host }}</code>
          <button class="btn-icon btn-icon-muted" :title="$t('domainDetail.copy')" :aria-label="$t('domainDetail.copy')" @click="copy(item.challenge_host)"><span class="mdi mdi-content-copy"></span></button>
        </div>
        <div class="dns-field">
          <span class="dns-label">{{ $t('domainDetail.value') }}</span>
          <code class="dns-value">{{ item.challenge_value }}</code>
          <button class="btn-icon btn-icon-muted" :title="$t('domainDetail.copy')" :aria-label="$t('domainDetail.copy')" @click="copy(item.challenge_value)"><span class="mdi mdi-content-copy"></span></button>
        </div>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>{{ $t('domainDetail.routes') }}</h2></div>
      <div class="card-body">
        <div v-if="item.routes.length" class="used-list">
          <router-link v-for="r in item.routes" :key="r.id" :to="`/routes/${r.id}`" class="used-row">
            <span class="mdi mdi-routes"></span>
            <span class="used-name">{{ r.name }}</span>
            <span class="cell-sub mono">{{ r.hosts.join(', ') }}</span>
            <span class="badge" :class="routeStatusClass[r.status] || 'badge-neutral'" :title="r.status_reason || ''">{{ r.status }}</span>
          </router-link>
        </div>
        <p v-else class="text-muted text-sm" style="margin: 0">{{ $t('domainDetail.noRoutesHint') }}</p>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>{{ $t('domainDetail.settings') }}</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row"><span class="detail-key">{{ $t('domains.form.defaultTls') }}</span><span>{{ item.tls_mode === 'acme' ? t('domains.tls.acme') : t('domainDetail.tlsCustom') }}</span></div>
        <div class="detail-row"><span class="detail-key">{{ $t('domainDetail.wildcard') }}</span><span>{{ item.wildcard ? 'Yes' : 'No' }}</span></div>
        <div class="detail-row"><span class="detail-key">{{ $t('domainDetail.added') }}</span><span>{{ fmtDate(item.created_at) }}</span></div>
        <div class="detail-row"><span class="detail-key">{{ $t('planDetail.updated') }}</span><span>{{ fmtDate(item.updated_at) }}</span></div>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showEdit" @close="showEdit = false">
        <div class="modal-header">
          <h3>{{ $t('domains.editTitle') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showEdit = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('domains.form.name') }}</label>
              <input v-model="form.name" class="form-input mono" disabled />
              <p class="form-hint">{{ $t('domains.form.nameFixed') }}</p>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('domains.form.defaultTls') }}</label>
              <div class="tabs" style="margin-bottom: 0">
                <button v-for="t in tlsModes" :key="t.value" type="button" class="tab" :class="{ active: form.tls_mode === t.value }" @click="form.tls_mode = t.value">{{ t.label }}</button>
              </div>
            </div>
            <label class="check"><input type="checkbox" v-model="form.wildcard" /> <span><i18n-t keypath="domains.form.wildcard" tag="span"><template #host><code>*.{{ item.name }}</code></template></i18n-t></span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showEdit = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="showDelete"
      :title="$t('confirm.title.deleteDomain')"
      :message="$t('confirm.message.domainDetail.deleteDomainNameRoutes', { name: item.name })"
      :confirm-label="$t('action.delete')"
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
