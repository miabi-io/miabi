<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { webhookApi, type WebhookInput } from '@/api/webhooks'
import { NOTIFIABLE_EVENT_GROUPS, eventLabel } from '@/constants/notifiableEvents'
import { copyText } from '@/utils/clipboard'
import type { Webhook, WebhookDelivery } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<Webhook[]>([])
const loading = ref(false)
const testing = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<Webhook | null>(null)
const form = ref<WebhookInput>({ name: '', url: '', events: [], enabled: true })
// Custom headers edited as "Key: Value" lines; parsed into a map on save.
const headersText = ref('')
// Set after creation: the signing secret, shown exactly once.
const revealSecret = ref<string | null>(null)

function headersToText(h?: Record<string, string>): string {
  if (!h) return ''
  return Object.entries(h).map(([k, v]) => `${k}: ${v}`).join('\n')
}
function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf(':')
    if (i <= 0) continue
    const k = line.slice(0, i).trim()
    const v = line.slice(i + 1).trim()
    if (k) out[k] = v
  }
  return out
}

const showDeliveries = ref(false)
const deliveries = ref<WebhookDelivery[]>([])
const deliveriesLoading = ref(false)
const deliveriesFor = ref<Webhook | null>(null)

async function load(id: number | null) {
  if (!id) {
    items.value = []
    return
  }
  loading.value = true
  try {
    items.value = (await webhookApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  editing.value = null
  revealSecret.value = null
  form.value = { name: '', url: '', events: [], enabled: true }
  headersText.value = ''
  showModal.value = true
}
function openEdit(w: Webhook) {
  editing.value = w
  revealSecret.value = null
  form.value = { name: w.name, url: w.url, events: [...w.events], enabled: w.enabled }
  headersText.value = headersToText(w.headers)
  showModal.value = true
}

function toggleEvent(value: string) {
  const set = new Set(form.value.events)
  set.has(value) ? set.delete(value) : set.add(value)
  form.value.events = [...set]
}

async function save() {
  if (!currentWorkspaceId.value) return
  if (form.value.events.length === 0) {
    notify.error(t('notify.common.selectAtLeastOneEvent'))
    return
  }
  saving.value = true
  form.value.headers = parseHeaders(headersText.value)
  try {
    if (editing.value) {
      await webhookApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success(t('notify.webhooks.updated'))
      showModal.value = false
    } else {
      const created = (await webhookApi.create(currentWorkspaceId.value, form.value)).data.data
      notify.success(t('notify.webhooks.created'))
      revealSecret.value = created.secret ?? null
      if (!revealSecret.value) showModal.value = false
    }
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function copySecret() {
  if (!revealSecret.value) return
  if (await copyText(revealSecret.value)) notify.success(t('notify.common.copied'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

async function test(w: Webhook) {
  if (!currentWorkspaceId.value) return
  testing.value = w.id
  try {
    await webhookApi.test(currentWorkspaceId.value, w.id)
    notify.success(t('notify.webhooks.testOk', { name: w.name || w.url }))
  } catch (e) {
    notify.apiError(e, 'Test delivery failed')
  } finally {
    testing.value = null
  }
}

const pendingDelete = ref<Webhook | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !pendingDelete.value) return
  deleting.value = true
  try {
    await webhookApi.remove(currentWorkspaceId.value, pendingDelete.value.id)
    notify.success(t('notify.webhooks.deleted'))
    pendingDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function openDeliveries(w: Webhook) {
  if (!currentWorkspaceId.value) return
  deliveriesFor.value = w
  showDeliveries.value = true
  deliveriesLoading.value = true
  deliveries.value = []
  try {
    deliveries.value = (await webhookApi.deliveries(currentWorkspaceId.value, w.id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    deliveriesLoading.value = false
  }
}

async function redeliver(d: WebhookDelivery) {
  if (!currentWorkspaceId.value || !deliveriesFor.value) return
  try {
    await webhookApi.redeliver(currentWorkspaceId.value, deliveriesFor.value.id, d.id)
    notify.success(t('notify.webhooks.requeued'))
    openDeliveries(deliveriesFor.value)
  } catch (e) {
    notify.apiError(e, 'Redelivery failed')
  }
}

function fmtTime(s: string) {
  return new Date(s).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('webhooks.webhooks') }}</h1>
        <p class="subtitle">{{ $t('webhooks.subtitle') }}</p>
      </div>
      <button v-if="ws.isWorkspaceAdmin" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>{{ $t('webhooks.newWebhook') }}</button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-webhook" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('webhooks.noWebhooksYet') }}</h3>
        <p>{{ $t('webhooks.emptyHint') }}</p>
        <button v-if="ws.isWorkspaceAdmin" class="btn btn-primary mt-4" @click="openCreate">{{ $t('webhooks.addAWebhook') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>{{ $t('webhooks.webhook') }}</th><th>{{ $t('webhooks.events') }}</th><th>{{ $t('dashboard.col.status') }}</th><th></th></tr></thead>
          <tbody>
            <tr v-for="w in items" :key="w.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-webhook" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ w.name || 'Webhook' }}</span>
                    <span class="cell-sub">{{ w.url }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ w.events.length }} event{{ w.events.length === 1 ? '' : 's' }}</td>
              <td>
                <span class="badge" :class="w.enabled ? 'badge-success' : 'badge-neutral'">
                  {{ w.enabled ? 'Enabled' : 'Disabled' }}
                </span>
              </td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" :title="$t('webhooks.deliveries')" :aria-label="$t('webhooks.deliveries')" @click="openDeliveries(w)">
                  <span class="mdi mdi-history"></span>
                </button>
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="$t('webhooks.sendTest')" :aria-label="$t('webhooks.sendTest')" :disabled="testing === w.id" @click="test(w)">
                  <span class="mdi" :class="testing === w.id ? 'mdi-loading mdi-spin' : 'mdi-send-outline'"></span>
                </button>
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(w)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="pendingDelete = w"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create / edit / secret-reveal modal -->
    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ revealSecret ? 'Webhook created' : editing ? 'Edit webhook' : 'New webhook' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>

        <template v-if="revealSecret">
          <div class="modal-body">
            <div class="app-banner app-banner--warning">
              <span class="mdi mdi-alert-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-title">{{ $t('webhooks.copyYourSigningSecretNow') }}</p>
                <i18n-t keypath="webhooks.secretHint" tag="p" class="app-banner-text"><template #header><code>X-Miabi-Signature</code></template></i18n-t>
              </div>
            </div>
            <div class="code-block" style="margin-top: 14px">{{ revealSecret }}</div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copySecret">{{ $t('webhooks.copySecret') }}</button>
            <button type="button" class="btn btn-primary" @click="showModal = false">{{ $t('webhooks.done') }}</button>
          </div>
        </template>

        <form v-else @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }} <span class="text-muted">{{ $t('webhooks.optional') }}</span></label>
              <input v-model="form.name" class="form-input" :placeholder="$t('apiKeys.namePlaceholder')" :aria-label="$t('apps.form.name')" autofocus />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('webhooks.payloadUrl') }}</label>
              <input v-model="form.url" type="url" class="form-input" placeholder="https://example.com/hooks/miabi" :aria-label="$t('webhooks.payloadUrl')" required />
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('webhooks.events') }}</label>
              <div v-for="g in NOTIFIABLE_EVENT_GROUPS" :key="g.label" class="event-group">
                <div class="event-group-label">{{ g.label }}</div>
                <div class="event-grid">
                  <label v-for="e in g.events" :key="e.value" class="event-option">
                    <input type="checkbox" :checked="form.events.includes(e.value)" @change="toggleEvent(e.value)" />
                    <span>{{ e.label }}</span>
                  </label>
                </div>
              </div>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('webhooks.customHeaders') }}<span class="text-muted">{{ $t('webhooks.optional') }}</span></label>
              <textarea v-model="headersText" class="form-input" rows="3" placeholder="Authorization: Bearer xyz&#10;X-Custom: value" :aria-label="$t('webhooks.customHeaders')" style="font-family: monospace; resize: vertical"></textarea>
              <i18n-t keypath="webhooks.headersHint" tag="p" class="form-hint"><template #format><code>Key: Value</code></template></i18n-t>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="check-row">
                <input type="checkbox" v-model="form.enabled" />
                <span>{{ $t('jobs.enabled') }}</span>
              </label>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Create webhook' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <!-- Deliveries modal -->
    <Teleport to="body">
      <AppModal v-if="showDeliveries" @close="showDeliveries = false">
        <div class="modal-header">
          <h3>{{ $t('webhooks.recentDeliveries') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showDeliveries = false"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p class="text-muted text-sm" style="margin-top: 0">{{ deliveriesFor?.name || deliveriesFor?.url }}</p>
          <div v-if="deliveriesLoading"><span class="spinner"></span></div>
          <div v-else-if="deliveries.length === 0" class="text-muted text-sm">{{ $t('webhooks.noDeliveries') }}</div>
          <div v-else class="table-wrapper">
            <table>
              <thead><tr><th>{{ $t('webhooks.event') }}</th><th>{{ $t('webhooks.result') }}</th><th>{{ $t('webhooks.attempt') }}</th><th>{{ $t('webhooks.when') }}</th><th></th></tr></thead>
              <tbody>
                <tr v-for="d in deliveries" :key="d.id">
                  <td class="cell-sub">{{ eventLabel(d.event) }}</td>
                  <td>
                    <span class="badge" :class="d.status === 'success' ? 'badge-success' : 'badge-danger'">
                      {{ d.http_status_code || d.status }}
                    </span>
                    <span v-if="d.error_message" class="cell-sub" :title="d.error_message"> {{ d.error_message }}</span>
                  </td>
                  <td class="cell-sub">#{{ d.attempt }}</td>
                  <td class="cell-sub">{{ fmtTime(d.created_at) }}</td>
                  <td class="text-right">
                    <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="$t('webhooks.redeliver')" :aria-label="$t('webhooks.redeliver')" @click="redeliver(d)"><span class="mdi mdi-replay"></span></button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="modal-footer">
          <button type="button" class="btn btn-primary" @click="showDeliveries = false">{{ $t('shell.close') }}</button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      :title="$t('confirm.title.deleteWebhook')"
      :message="$t('confirm.message.webhooks.deleteWebhookUrlDelivery', { url: pendingDelete?.name || pendingDelete?.url })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.event-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 16px; }
.event-option, .check-row { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.event-option input, .check-row input { width: 15px; height: 15px; }

.event-group + .event-group {
  margin-top: 12px;
}
.event-group-label {
  margin-bottom: 6px;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
</style>
