<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { certificateApi, type CertificateInput } from '@/api/certificates'
import type { Certificate } from '@/api/types'
import { fmtDate, expiryBadge } from '@/utils/certificate'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const certId = computed(() => Number(route.params.id))
const item = ref<Certificate | null>(null)
const usedBy = ref<{ id: number; name: string }[]>([])
const loading = ref(false)

async function load() {
  const wid = currentWorkspaceId.value
  if (!wid || !certId.value) return
  loading.value = true
  try {
    item.value = (await certificateApi.get(wid, certId.value)).data.data
    usedBy.value = (await certificateApi.usage(wid, certId.value)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
    router.replace('/certificates')
  } finally {
    loading.value = false
  }
}
watch([certId, currentWorkspaceId], load, { immediate: true })

// --- Replace (renew) ---
const showReplace = ref(false)
const saving = ref(false)
const form = ref<{ name: string; cert_pem: string; key_pem: string }>({ name: '', cert_pem: '', key_pem: '' })
function openReplace() {
  if (!item.value) return
  form.value = { name: item.value.name, cert_pem: '', key_pem: '' }
  showReplace.value = true
}
async function save() {
  const wid = currentWorkspaceId.value
  if (!wid || !item.value) return
  saving.value = true
  const input: CertificateInput = { name: form.value.name.trim(), cert_pem: form.value.cert_pem, key_pem: form.value.key_pem }
  try {
    await certificateApi.replace(wid, item.value.id, input)
    notify.success('Certificate replaced')
    showReplace.value = false
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
    await certificateApi.remove(wid, item.value.id)
    notify.success('Certificate deleted')
    router.replace('/certificates')
  } catch (e) { notify.apiError(e, 'Delete failed (certificate may be in use)') }
  finally { deleting.value = false }
}
</script>

<template>
  <div v-if="item">
    <div class="page-header">
      <div class="title-group">
        <button class="btn-icon btn-icon-muted" title="Back" aria-label="Back" @click="router.push('/certificates')">
          <span class="mdi mdi-arrow-left"></span>
        </button>
        <div>
          <h1>{{ item.display_name || item.name }}</h1>
          <span class="cell-sub" style="font-family: monospace">{{ item.common_name }}</span>
        </div>
        <span class="badge badge-dot" :class="expiryBadge(item).cls">{{ expiryBadge(item).text }}</span>
      </div>
      <div v-if="ws.canEdit" class="flex items-center gap-2">
        <button class="btn btn-secondary" @click="openReplace"><span class="mdi mdi-autorenew"></span> Replace (renew)</button>
        <button class="btn btn-danger" @click="showDelete = true"><span class="mdi mdi-delete-outline"></span> Delete</button>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Certificate</h2></div>
      <div class="card-body detail-list">
        <div class="detail-row"><span class="detail-key">Common name</span><span class="mono">{{ item.common_name || '—' }}</span></div>
        <div class="detail-row"><span class="detail-key">Domains (SAN)</span><span class="mono">{{ (item.dns_names || []).join(', ') || '—' }}</span></div>
        <div class="detail-row"><span class="detail-key">Issuer</span><span>{{ item.issuer || '—' }}</span></div>
        <div class="detail-row"><span class="detail-key">Serial</span><span class="mono">{{ item.serial_hex || '—' }}</span></div>
        <div class="detail-row"><span class="detail-key">Valid from</span><span>{{ fmtDate(item.not_before) }}</span></div>
        <div class="detail-row">
          <span class="detail-key">Expires</span>
          <span><span class="badge badge-dot" :class="expiryBadge(item).cls">{{ expiryBadge(item).text }}</span> <span class="text-muted">{{ fmtDate(item.not_after) }}</span></span>
        </div>
        <div class="detail-row"><span class="detail-key">Imported</span><span>{{ fmtDate(item.created_at) }}</span></div>
        <div class="detail-row"><span class="detail-key">Updated</span><span>{{ fmtDate(item.updated_at) }}</span></div>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>Used by</h2></div>
      <div class="card-body">
        <div v-if="usedBy.length" class="used-list">
          <router-link v-for="r in usedBy" :key="r.id" :to="`/routes/${r.id}`" class="used-row">
            <span class="mdi mdi-routes"></span> {{ r.name }}
          </router-link>
        </div>
        <p v-else class="text-muted text-sm" style="margin: 0">Not used by any route. The certificate can be safely deleted.</p>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showReplace" max-width="640px" @close="showReplace = false">
        <div class="modal-header">
          <h3>Replace certificate</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showReplace = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" disabled />
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
            <button type="button" class="btn btn-secondary" @click="showReplace = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : 'Replace' }}</button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="showDelete"
      title="Delete certificate"
      :message="`Delete certificate &quot;${item.name}&quot;? This is blocked while a route still references it.`"
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
.mono { font-family: monospace; font-size: 13px; }
.text-muted { color: var(--text-muted); }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.detail-list { display: flex; flex-direction: column; }
.detail-row { display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 12px 0; border-bottom: 1px solid var(--border-primary); font-size: 13px; }
.detail-row:last-child { border-bottom: none; }
.detail-key { color: var(--text-muted); }
.used-list { display: flex; flex-direction: column; gap: 6px; }
.used-row { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-primary); text-decoration: none; }
.used-row:hover { text-decoration: underline; }
</style>
