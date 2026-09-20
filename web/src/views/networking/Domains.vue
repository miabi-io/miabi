<script setup lang="ts">
import { useI18n } from 'vue-i18n'
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
const { t } = useI18n()
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

// verifiedLabel qualifies the verified badge with how ownership was established, so an
// admin override never looks like a DNS proof.
function verifiedLabel(d: Domain): string {
  if (d.verified_via === 'admin') return 'verified · admin override'
  if (d.verified_via === 'dns_provider') return 'verified · DNS provider'
  return 'verified'
}

// proofPresent reports whether the last ownership check found the TXT record. Meaningful
// for any domain that has ever been checked, verified or not.
function proofPresent(d: Domain): boolean {
  return !!d.verification_checked_at && !d.verification_error
}

function lastChecked(d: Domain): string {
  if (!d.verification_checked_at) return 'never checked'
  const secs = Math.max(0, Math.round((Date.now() - new Date(d.verification_checked_at).getTime()) / 1000))
  if (secs < 60) return 'checked just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `checked ${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `checked ${hours}h ago`
  return `checked ${Math.round(hours / 24)}d ago`
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
      notify.success(t('domains.updated'))
    } else {
      const d = (await domainApi.create(currentWorkspaceId.value, form.value)).data.data
      notify.success(t('domains.registered'))
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
    notify.success(t('domains.verified', { name: d.name }))
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
    notify.success(t('domains.deleted'))
    toDelete.value = null
    load(id)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success(t('notify.common.copied'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

const tlsModes: { value: DomainTLSMode; label: string }[] = [
  { value: 'acme', label: t('domains.tls.acme') },
  { value: 'custom', label: t('domains.tls.custom') },
]
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('nav.networking.domains') }}</h1>
        <p class="subtitle">{{ $t('domains.subtitle') }}</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> {{ $t('dashboard.quick.addDomain.label') }}
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-web" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('domains.empty') }}</h3>
        <p>{{ $t('domains.emptyHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('domains.addOne') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('domains.col.domain') }}</th><th>TLS</th><th>{{ $t('domains.col.dns') }}</th><th>{{ $t('dashboard.col.status') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="d in items" :key="d.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-web" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <router-link :to="`/domains/${d.id}`" class="cell-title cell-link">{{ d.name }}</router-link>
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
                <span v-if="d.banned" class="badge badge-danger" :title="$t('domains.bannedHint')"><span class="mdi mdi-cancel"></span> banned</span>
                <span v-else-if="d.verified" class="badge" :class="d.verified_via === 'admin' ? 'badge-info' : 'badge-success'" :title="d.verified_via === 'admin' ? $t('domains.grantedByAdmin') : ''">
                  <span class="mdi" :class="d.verified_via === 'admin' ? 'mdi-shield-account-outline' : 'mdi-check-decagram'"></span> {{ verifiedLabel(d) }}
                </span>
                <span v-else-if="d.serving_unverified" class="badge badge-info" :title="$t('domains.servingHint')">
                  <span class="mdi mdi-shield-star-outline"></span> {{ $t('domains.servingUnverifiedBadge') }}
                </span>
                <span v-else class="badge badge-warning"><span class="mdi mdi-clock-alert-outline"></span> pending</span>
              </td>
              <td class="text-right table-actions">
                <button v-if="!d.banned" class="btn-icon btn-icon-muted" :title="$t('domains.dnsRecords')" :aria-label="$t('domains.dnsRecords')" @click="openDns(d)"><span class="mdi mdi-dns-outline"></span></button>
                <button v-if="ws.canEdit && !d.verified && !d.banned" class="btn-icon btn-icon-muted" :title="$t('domains.verifyNow')" :aria-label="$t('domains.verifyNow')" :disabled="verifying === d.id" @click="verify(d)">
                  <span class="mdi" :class="verifying === d.id ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(d)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = d"><span class="mdi mdi-delete-outline"></span></button>
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
          <h3>{{ editing ? $t('domains.editTitle') : $t('dashboard.quick.addDomain.label') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('domains.form.name') }}</label>
              <input v-model="form.name" class="form-input mono" placeholder="example.com" required :autofocus="!editing" :disabled="!!editing" />
              <p v-if="editing" class="hint">{{ $t('domains.form.nameFixed') }}</p>
              <p v-else class="hint"><i18n-t keypath="domains.form.nameHint" tag="span"><template #star><code>*.</code></template></i18n-t></p>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('domains.form.defaultTls') }}</label>
              <div class="tabs" style="margin-bottom: 0">
                <button v-for="t in tlsModes" :key="t.value" type="button" class="tab" :class="{ active: form.tls_mode === t.value }" @click="form.tls_mode = t.value">{{ t.label }}</button>
              </div>
            </div>
            <label class="check"><input type="checkbox" v-model="form.wildcard" /> <span><i18n-t keypath="domains.form.wildcard" tag="span"><template #host><code>*.{{ form.name || 'example.com' }}</code></template></i18n-t></span></label>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Add domain') }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- DNS verification -->
    <Teleport to="body">
      <AppModal v-if="showDns && dnsDomain" @close="showDns = false">
        <div class="modal-header">
          <h3>DNS for {{ dnsDomain.name }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showDns = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <!-- Ownership state first, then the record — the panel stays a diagnostic
               after verification, which is when people actually need to read it. -->
          <div v-if="dnsDomain.verified && dnsDomain.verified_via === 'admin'" class="gate gate-warn">
            <span class="mdi mdi-shield-account-outline"></span>
            {{ $t('domains.verifiedByAdmin') }}
          </div>
          <div v-else-if="dnsDomain.verified" class="gate gate-ok">
            <span class="mdi mdi-check-decagram"></span> Ownership verified{{ dnsDomain.verified_via === 'dns_provider' ? ' through the connected DNS provider' : ' by DNS' }}.
          </div>
          <div v-else-if="dnsDomain.serving_unverified" class="gate gate-warn">
            <span class="mdi mdi-shield-star-outline"></span>
            {{ $t('domains.servingUnverified') }}
          </div>

          <p class="check-line">
            <span class="mdi" :class="proofPresent(dnsDomain) ? 'mdi-check-circle-outline check-ok' : 'mdi-alert-circle-outline check-bad'"></span>
            {{ proofPresent(dnsDomain) ? $t('domains.txtFound') : $t('domains.txtMissing') }} · {{ lastChecked(dnsDomain) }}
          </p>

          <p v-if="dnsDomain.automated" class="note"><span class="mdi mdi-auto-fix"></span> {{ $t('domains.providerConnected') }}</p>
          <p v-else-if="!dnsDomain.verified" class="note"><i18n-t keypath="domains.addTxt" tag="span"><template #txt><strong>TXT</strong></template></i18n-t> <em>{{ $t('domains.addTxtTip') }}</em></p>
          <p v-else class="note">{{ $t('domains.keepRecord') }}</p>

          <div class="dns-field">
            <span class="dns-label">{{ $t('domains.dns.type') }}</span>
            <code class="dns-value">TXT</code>
          </div>
          <div class="dns-field">
            <span class="dns-label">{{ $t('domains.dns.host') }}</span>
            <code class="dns-value">{{ dnsDomain.challenge_host }}</code>
            <button class="btn-icon btn-icon-muted" :title="$t('action.copy')" :aria-label="$t('action.copy')" @click="copy(dnsDomain.challenge_host)"><span class="mdi mdi-content-copy"></span></button>
          </div>
          <div class="dns-field">
            <span class="dns-label">{{ $t('domains.dns.value') }}</span>
            <code class="dns-value">{{ dnsDomain.challenge_value }}</code>
            <button class="btn-icon btn-icon-muted" :title="$t('action.copy')" :aria-label="$t('action.copy')" @click="copy(dnsDomain.challenge_value)"><span class="mdi mdi-content-copy"></span></button>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="showDns = false">{{ $t('shell.close') }}</button>
          <button v-if="ws.canEdit" type="button" class="btn btn-primary" :disabled="verifying === dnsDomain.id" @click="verify(dnsDomain)">
            <span class="mdi" :class="verifying === dnsDomain.id ? 'mdi-loading mdi-spin' : 'mdi-shield-check-outline'"></span>
            {{ dnsDomain.verified ? 'Re-check' : 'Verify' }}
          </button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      :title="$t('confirm.title.deleteDomain')"
      :message="$t('confirm.message.domains.deleteDomainNameRoutes', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')"
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
.cell-link { color: inherit; text-decoration: none; }
.cell-link:hover { text-decoration: underline; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.gate { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-radius: 8px; font-size: 13px; }
.gate .mdi { font-size: 18px; }
.gate-ok { background: var(--success-50); color: var(--success-600); }
.gate-warn { background: var(--warning-50); color: var(--warning-600); align-items: flex-start; line-height: 1.5; }
.check-line { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); margin: 12px 0; }
.check-ok { color: var(--success-500); }
.check-bad { color: var(--warning-500); }
.dns-field { display: flex; align-items: center; gap: 10px; padding: 6px 0; }
.dns-label { width: 92px; font-size: 12px; color: var(--text-muted); flex-shrink: 0; }
.dns-value { flex: 1; font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 6px 10px; border-radius: 6px; overflow-x: auto; white-space: nowrap; }
</style>
