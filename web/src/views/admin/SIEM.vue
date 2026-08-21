<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { adminApi } from '@/api/admin'
import type { SIEMConfigPayload } from '@/api/admin'
import type { SIEMConfig } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const notify = useNotificationStore()
const licenseStore = useLicenseStore()
const siem = useEntitlement('siem_stream')

const targets = ref<SIEMConfig[]>([])
const loading = ref(false)
const testing = ref<number | null>(null)

async function load() {
  if (!siem.has.value) return
  loading.value = true
  try {
    targets.value = (await adminApi.listSIEM()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => {
  licenseStore.load()
  load()
})

function sinkIcon(s: string): string {
  return s === 'webhook' ? 'mdi-webhook' : 'mdi-console-network-outline'
}
function fmtDate(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

// --- Create / edit modal ---
interface Form {
  name: string
  sink: 'syslog' | 'webhook'
  endpoint: string
  format: 'json' | 'cef'
  auth_header: string
  enabled: boolean
}
function emptyForm(): Form {
  return { name: '', sink: 'webhook', endpoint: '', format: 'json', auth_header: '', enabled: true }
}
const showModal = ref(false)
const saving = ref(false)
const editing = ref<SIEMConfig | null>(null)
const form = ref<Form>(emptyForm())

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showModal.value = true
}
function openEdit(t: SIEMConfig) {
  editing.value = t
  form.value = { name: t.name, sink: t.sink, endpoint: t.endpoint, format: t.format, auth_header: '', enabled: t.enabled }
  showModal.value = true
}

async function save() {
  const f = form.value
  if (!f.name.trim() || !f.endpoint.trim()) return
  const payload: SIEMConfigPayload = {
    name: f.name.trim(),
    sink: f.sink,
    endpoint: f.endpoint.trim(),
    format: f.format,
    enabled: f.enabled,
  }
  if (f.auth_header.trim()) payload.auth_header = f.auth_header.trim()
  saving.value = true
  try {
    if (editing.value) await adminApi.updateSIEM(editing.value.id, payload)
    else await adminApi.createSIEM(payload)
    notify.success(editing.value ? 'Target updated' : 'Target created')
    showModal.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const pendingDelete = ref<SIEMConfig | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!pendingDelete.value) return
  deleting.value = true
  try {
    await adminApi.deleteSIEM(pendingDelete.value.id)
    notify.success('Target deleted')
    pendingDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function test(t: SIEMConfig) {
  testing.value = t.id
  try {
    await adminApi.testSIEM(t.id)
    notify.success('Test event delivered')
    await load()
  } catch (e) {
    notify.apiError(e)
    await load() // refresh last_error
  } finally {
    testing.value = null
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>SIEM Streaming</h1>
        <p class="text-muted">Ship the audit log to an external SIEM (syslog, webhook), at-least-once.</p>
      </div>
      <button v-if="siem.has.value" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> Add target
      </button>
    </div>

    <!-- Locked (Community / not entitled) -->
    <div v-if="!siem.has.value" class="card">
      <div class="card-body locked">
        <span class="mdi mdi-lock-outline"></span>
        <div>
          <p>Live audit streaming to a SIEM is an Enterprise feature. Stream every audit event to syslog or a webhook with at-least-once delivery.</p>
          <router-link to="/admin/license" class="btn btn-secondary btn-sm">Manage license</router-link>
        </div>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div v-if="loading && targets.length === 0" class="card-body"><span class="spinner"></span></div>

        <div v-else-if="targets.length === 0" class="empty-state">
          <span class="mdi mdi-export-variant" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>No streaming targets</h3>
          <p class="text-muted">Add a syslog or webhook target to start streaming the audit log.</p>
          <button class="btn btn-primary mt-4" @click="openCreate">Add target</button>
        </div>

        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Target</th><th>Endpoint</th><th>Status</th><th>Last shipped</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="t in targets" :key="t.id" class="row-clickable" @click="openEdit(t)">
                <td>
                  <div class="cell-id">
                    <span class="avatar avatar-sm"><span class="mdi" :class="sinkIcon(t.sink)"></span></span>
                    <span class="cell-text">
                      <span class="cell-title">{{ t.name }}</span>
                      <span class="cell-sub">{{ t.sink }} · {{ t.format }}</span>
                    </span>
                  </div>
                </td>
                <td class="text-muted endpoint">{{ t.endpoint }}</td>
                <td>
                  <span v-if="!t.enabled" class="badge badge-dot badge-warning">Disabled</span>
                  <span v-else-if="t.last_error" class="badge badge-dot badge-danger" :title="t.last_error">Error</span>
                  <span v-else class="badge badge-dot badge-success">OK</span>
                </td>
                <td class="text-muted">
                  {{ fmtDate(t.last_shipped_at) }}
                  <span v-if="t.last_shipped_id" class="cell-sub">#{{ t.last_shipped_id }}</span>
                </td>
                <td class="text-right actions" @click.stop>
                  <button class="btn-icon btn-icon-muted" title="Send test event" aria-label="Send test event" :disabled="testing === t.id" @click="test(t)">
                    <span class="mdi mdi-send-check-outline"></span>
                  </button>
                  <button class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(t)"><span class="mdi mdi-pencil"></span></button>
                  <button class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingDelete = t"><span class="mdi mdi-delete"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- Create / edit modal -->
    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit target' : 'Add target' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" placeholder="Splunk prod" required autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">Sink</label>
              <select v-model="form.sink" class="form-select">
                <option value="webhook">Webhook (HTTPS NDJSON)</option>
                <option value="syslog">Syslog (RFC 5424)</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label">Endpoint</label>
              <input
                v-model="form.endpoint"
                class="form-input"
                :placeholder="form.sink === 'webhook' ? 'https://siem.example.com/ingest' : 'tcp://siem.example.com:514'"
                required
              />
              <span class="form-hint">{{ form.sink === 'webhook' ? 'HTTPS URL to POST NDJSON batches to.' : 'tcp:// or udp:// host:port.' }}</span>
            </div>
            <div class="form-group">
              <label class="form-label">Format</label>
              <select v-model="form.format" class="form-select">
                <option value="json">JSON</option>
                <option value="cef">CEF (syslog)</option>
              </select>
            </div>
            <div v-if="form.sink === 'webhook'" class="form-group">
              <label class="form-label">Authorization header</label>
              <input
                v-model="form.auth_header"
                class="form-input"
                type="password"
                :placeholder="editing ? 'Leave blank to keep current' : 'Bearer …'"
              />
              <span class="form-hint">Sent as the Authorization header; encrypted at rest.</span>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="check-row"><input v-model="form.enabled" type="checkbox" /> Enabled</label>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name.trim() || !form.endpoint.trim()">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Add target' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete SIEM target"
      :message="`Delete SIEM target &quot;${pendingDelete?.name}&quot;?`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.locked {
  display: flex;
  align-items: center;
  gap: 14px;
}
.locked .mdi {
  font-size: 28px;
  color: var(--text-muted);
}
.locked p {
  margin: 0 0 8px;
  max-width: 64ch;
  color: var(--text-secondary, var(--text-muted));
}
.endpoint {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.cell-sub {
  margin-left: 6px;
  font-size: 11px;
  color: var(--text-muted);
}
</style>
