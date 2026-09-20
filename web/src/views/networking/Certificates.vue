<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { certificateApi, type CertificateInput } from '@/api/certificates'
import { domainApi } from '@/api/domains'
import type { Certificate, Domain } from '@/api/types'
import { fmtDate, expiryBadge } from '@/utils/certificate'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const router = useRouter()
const { currentWorkspaceId } = storeToRefs(ws)

const certs = ref<Certificate[]>([])
const domains = ref<Domain[]>([])
const loading = ref(false)

// Domains eligible for managed issuance: verified + a connected DNS provider.
const issuableDomains = ref<Domain[]>([])

async function load() {
  const id = currentWorkspaceId.value
  if (!id) { certs.value = []; domains.value = []; return }
  loading.value = true
  try {
    certs.value = (await certificateApi.list(id)).data.data ?? []
    domains.value = (await domainApi.list(id)).data.data ?? []
    issuableDomains.value = domains.value.filter(d => d.verified && d.automated)
  } catch (e) { notify.apiError(e) }
  finally { loading.value = false }
}
watch(currentWorkspaceId, load, { immediate: true })

// --- Issue managed (ACME) certificate ---
const showIssue = ref(false)
const issuing = ref(false)
const issueForm = ref<{ domain_id: number | null; include_wildcard: boolean; auto_renew: boolean }>(
  { domain_id: null, include_wildcard: true, auto_renew: true },
)
function openIssue() {
  issueForm.value = { domain_id: issuableDomains.value[0]?.id ?? null, include_wildcard: true, auto_renew: true }
  showIssue.value = true
}
async function issueCert() {
  const id = currentWorkspaceId.value
  if (!id || !issueForm.value.domain_id) return
  issuing.value = true
  try {
    await certificateApi.issue(id, {
      domain_id: issueForm.value.domain_id,
      include_wildcard: issueForm.value.include_wildcard,
      auto_renew: issueForm.value.auto_renew,
    })
    notify.success(t('notify.certificates.issuanceStarted'))
    showIssue.value = false
    load()
  } catch (e) { notify.apiError(e) }
  finally { issuing.value = false }
}

// --- Import ---
const showForm = ref(false)
const saving = ref(false)
const form = ref<{ name: string; cert_pem: string; key_pem: string }>({ name: '', cert_pem: '', key_pem: '' })

function openImport() {
  form.value = { name: '', cert_pem: '', key_pem: '' }
  showForm.value = true
}
async function save() {
  const id = currentWorkspaceId.value
  if (!id) return
  saving.value = true
  const input: CertificateInput = { name: form.value.name.trim(), cert_pem: form.value.cert_pem, key_pem: form.value.key_pem }
  try {
    await certificateApi.import(id, input)
    notify.success(t('notify.certificates.imported'))
    showForm.value = false
    load()
  } catch (e) { notify.apiError(e) }
  finally { saving.value = false }
}

function open(c: Certificate) { router.push(`/certificates/${c.id}`) }
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('certificates.certificates') }}</h1>
        <div class="text-muted text-sm">{{ $t('certificates.subtitle') }}</div>
      </div>
        <button v-if="ws.canEdit && issuableDomains.length" class="btn btn-secondary" @click="openIssue"><span class="mdi mdi-auto-fix"></span>{{ $t('certificates.issueWithDnsProvider') }}</button>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openImport"><span class="mdi mdi-plus"></span>{{ $t('certificates.importCertificate') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && certs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="certs.length === 0" class="empty-state">
        <span class="mdi mdi-certificate" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('certificates.noCertificates') }}</h3>
        <p>{{ $t('certificates.importHint') }}</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openImport">{{ $t('certificates.importCertificate') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('apps.form.name') }}</th><th>{{ $t('certificates.commonName') }}</th><th>{{ $t('certificates.source') }}</th><th>{{ $t('certificates.expires') }}</th><th>{{ $t('dashboard.col.created') }}</th></tr></thead>
          <tbody>
            <tr v-for="c in certs" :key="c.id" class="row-link" @click="open(c)">
              <td><span class="cell-title">{{ c.display_name || c.name }}</span></td>
              <td class="cell-sub" style="font-family: monospace">{{ c.common_name || '—' }}</td>
              <td>
                <span v-if="c.source === 'acme'" class="badge badge-info" :title="$t('certificates.acmeHint')">{{ $t('certificates.acme') }}<span v-if="c.auto_renew"> · {{ $t('certificates.auto') }}</span></span>
                <span v-else class="badge badge-neutral">{{ $t('certificates.imported') }}</span>
              </td>
              <td>
                <span v-if="c.status === 'issuing'" class="badge badge-warning"><span class="mdi mdi-loading mdi-spin"></span>{{ $t('certificates.issuing') }}</span>
                <span v-else-if="c.status === 'failed'" class="badge badge-danger" :title="c.last_error">{{ $t('certificates.failed') }}</span>
                <template v-else>
                  <span class="badge badge-dot" :class="expiryBadge(c).cls">{{ expiryBadge(c).text }}</span>
                  <span class="cell-sub" style="margin-left: 8px">{{ fmtDate(c.not_after) }}</span>
                </template>
              </td>
              <td class="cell-sub">{{ fmtDate(c.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showForm" max-width="640px" @close="showForm = false">
        <div class="modal-header">
          <h3>{{ $t('certificates.importCertificate') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showForm = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" :placeholder="$t('certificates.namePlaceholder')" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('certificates.certPem') }}</label>
              <textarea v-model="form.cert_pem" class="form-input" rows="5" required placeholder="-----BEGIN CERTIFICATE-----" style="font-family: monospace; font-size: 12px"></textarea>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('certificates.privateKeyPem') }}</label>
              <textarea v-model="form.key_pem" class="form-input" rows="4" required placeholder="-----BEGIN PRIVATE KEY-----" style="font-family: monospace; font-size: 12px"></textarea>
              <p class="form-hint">{{ $t('certificates.keyHint') }}</p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showForm = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Import' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Issue managed (ACME DNS-01) certificate -->
    <Teleport to="body">
      <AppModal v-if="showIssue" @close="showIssue = false">
        <div class="modal-header">
          <h3>{{ $t('certificates.issueWithDnsProvider') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showIssue = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="issueCert">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('certificates.domain') }}</label>
              <select v-model.number="issueForm.domain_id" class="form-select" required>
                <option v-for="d in issuableDomains" :key="d.id" :value="d.id">{{ d.name }}</option>
              </select>
              <p class="form-hint">{{ $t('certificates.verifiedOnlyHint') }}</p>
            </div>
            <label class="checkbox-label"><input v-model="issueForm.include_wildcard" type="checkbox" /> <i18n-t keypath="certificates.includeWildcard" tag="span"><template #star><code>*.</code></template></i18n-t></label>
            <label class="checkbox-label"><input v-model="issueForm.auto_renew" type="checkbox" />{{ $t('certificates.autoRenewBeforeExpiry') }}</label>
            <p class="form-hint">{{ $t('certificates.acmeIssueHint') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showIssue = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="issuing || !issueForm.domain_id">{{ issuing ? 'Starting…' : 'Issue certificate' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.row-link { cursor: pointer; }
.row-link:hover { background: var(--surface-2, rgba(127, 127, 127, 0.06)); }
</style>
