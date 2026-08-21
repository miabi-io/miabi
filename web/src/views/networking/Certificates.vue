<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { certificateApi, type CertificateInput } from '@/api/certificates'
import { domainApi } from '@/api/domains'
import type { Certificate, Domain } from '@/api/types'
import { fmtDate, expiryBadge } from '@/utils/certificate'

const ws = useWorkspaceStore()
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
    notify.success('Issuance started — the certificate will appear once issued')
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
    notify.success('Certificate imported')
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
        <h1>Certificates</h1>
        <div class="text-muted text-sm">Import a TLS certificate once and select it on any route (TLS mode “custom”). ACME certificates are issued automatically by the gateway.</div>
      </div>
        <button v-if="ws.canEdit && issuableDomains.length" class="btn btn-secondary" @click="openIssue"><span class="mdi mdi-auto-fix"></span> Issue with DNS provider</button>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openImport"><span class="mdi mdi-plus"></span> Import certificate</button>
    </div>

    <div class="card">
      <div v-if="loading && certs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="certs.length === 0" class="empty-state">
        <span class="mdi mdi-certificate" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No certificates</h3>
        <p>Import a certificate + private key to terminate TLS with your own cert. One wildcard cert can back many routes.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openImport">Import certificate</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Name</th><th>Common name</th><th>Source</th><th>Expires</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="c in certs" :key="c.id" class="row-link" @click="open(c)">
              <td><span class="cell-title">{{ c.display_name || c.name }}</span></td>
              <td class="cell-sub" style="font-family: monospace">{{ c.common_name || '—' }}</td>
              <td>
                <span v-if="c.source === 'acme'" class="badge badge-info" title="Issued by Miabi via ACME DNS-01">ACME<span v-if="c.auto_renew"> · auto</span></span>
                <span v-else class="badge badge-neutral">Imported</span>
              </td>
              <td>
                <span v-if="c.status === 'issuing'" class="badge badge-warning"><span class="mdi mdi-loading mdi-spin"></span> issuing…</span>
                <span v-else-if="c.status === 'failed'" class="badge badge-danger" :title="c.last_error">failed</span>
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
      <div v-if="showForm" class="modal-overlay">
        <div class="modal" style="max-width: 640px; width: 100%">
          <div class="modal-header">
            <h3>Import certificate</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showForm = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="example.com wildcard" required autofocus />
              </div>
              <div class="form-group">
                <label class="form-label">Certificate (PEM) — leaf + intermediates</label>
                <textarea v-model="form.cert_pem" class="form-input" rows="5" required placeholder="-----BEGIN CERTIFICATE-----" style="font-family: monospace; font-size: 12px"></textarea>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Private key (PEM)</label>
                <textarea v-model="form.key_pem" class="form-input" rows="4" required placeholder="-----BEGIN PRIVATE KEY-----" style="font-family: monospace; font-size: 12px"></textarea>
                <p class="form-hint">The key is validated against the certificate, encrypted at rest, and never shown again.</p>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showForm = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Import' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Issue managed (ACME DNS-01) certificate -->
    <Teleport to="body">
      <div v-if="showIssue" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>Issue with DNS provider</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showIssue = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="issueCert">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Domain</label>
                <select v-model.number="issueForm.domain_id" class="form-select" required>
                  <option v-for="d in issuableDomains" :key="d.id" :value="d.id">{{ d.name }}</option>
                </select>
                <p class="form-hint">Only verified domains with a connected DNS provider can be issued.</p>
              </div>
              <label class="checkbox-label"><input v-model="issueForm.include_wildcard" type="checkbox" /> Include wildcard (<code>*.</code> + apex)</label>
              <label class="checkbox-label"><input v-model="issueForm.auto_renew" type="checkbox" /> Auto-renew before expiry</label>
              <p class="form-hint">Miabi solves an ACME DNS-01 challenge via your provider and stores the certificate here. This can take a minute.</p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showIssue = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="issuing || !issueForm.domain_id">{{ issuing ? 'Starting…' : 'Issue certificate' }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.row-link { cursor: pointer; }
.row-link:hover { background: var(--surface-2, rgba(127, 127, 127, 0.06)); }
</style>
