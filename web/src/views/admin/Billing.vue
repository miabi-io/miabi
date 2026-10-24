<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useAdminInfoStore } from '@/stores/adminInfo'
import type { AdminBillingStatus } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const adminInfo = useAdminInfoStore()

const loading = ref(false)
const connecting = ref(false)
const disconnecting = ref(false)
const showDisconnect = ref(false)
const code = ref('')
const status = ref<AdminBillingStatus | null>(null)
const connectedBy = ref('')

const state = computed(() => status.value?.billing_api ?? 'off')
const conn = computed(() => status.value?.connection)
const connected = computed(() => !!conn.value?.connected)
const codeValid = computed(() => code.value.trim().startsWith('mce1.'))

function fmtDate(s?: string): string {
  if (!s) return '—'
  return new Date(s).toLocaleString(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

async function resolveConnectedBy() {
  connectedBy.value = ''
  const id = conn.value?.connected_by
  if (!id) return
  connectedBy.value = `#${id}`
  try {
    const res = await adminApi.getUser(id)
    connectedBy.value = res.data.data.email || connectedBy.value
  } catch {
    // The user may have been deleted since; the id is still useful.
  }
}

async function load() {
  loading.value = true
  try {
    const res = await adminApi.billingStatus()
    status.value = res.data.data
    adminInfo.billingApi = res.data.data.billing_api
    void resolveConnectedBy()
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function connect() {
  if (!codeValid.value || connecting.value) return
  connecting.value = true
  try {
    await adminApi.connectBilling(code.value.trim())
    code.value = ''
    notify.success('Connected to the billing console')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    connecting.value = false
  }
}

async function disconnect() {
  showDisconnect.value = false
  if (disconnecting.value) return
  disconnecting.value = true
  try {
    await adminApi.disconnectBilling()
    notify.success('Billing console disconnected')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    disconnecting.value = false
  }
}
</script>

<template>
  <div class="billing-page">
    <div class="page-header">
      <div>
        <h1>Billing</h1>
        <p class="text-muted text-sm subtitle">Connect this instance to a Miabi Console that sells and manages its workspaces.</p>
      </div>
      <div v-if="!loading && state === 'on' && connected" class="header-actions">
        <button class="btn btn-danger-outline" :disabled="disconnecting" @click="showDisconnect = true">
          <span class="mdi mdi-link-variant-off"></span> {{ disconnecting ? 'Disconnecting…' : 'Disconnect' }}
        </button>
      </div>
    </div>

    <div v-if="loading && !status" class="spinner"></div>

    <template v-else-if="status">
      <div v-if="state === 'off'" class="notice notice-neutral" role="status">
        <span class="mdi mdi-power-plug-off-outline"></span>
        <div>
          <div class="notice-title">The Billing API is turned off</div>
          <div class="notice-body">Set <code>MIABI_BILLING_API_ENABLED=true</code> and restart Miabi to connect a console.</div>
        </div>
      </div>

      <div v-else-if="state === 'unlicensed'" class="notice notice-warning" role="alert">
        <span class="mdi mdi-license"></span>
        <div>
          <div class="notice-title">Your license does not include the Billing add-on</div>
          <div class="notice-body">
            The Billing API is enabled, but it needs an Enterprise license with the Billing add-on, which is sold separately.
            <router-link to="/admin/license">Manage license</router-link>
          </div>
        </div>
      </div>

      <div v-else-if="connected && conn" class="card">
        <div class="card-header">
          <h2>Console connection</h2>
          <span class="badge badge-success"><span class="mdi mdi-check"></span> Connected</span>
        </div>
        <div class="card-body">
          <dl class="details">
            <div class="wide">
              <dt>Console</dt>
              <dd><a :href="conn.console_url" target="_blank" rel="noopener">{{ conn.console_url }}</a></dd>
            </div>
            <div><dt>Instance ID</dt><dd class="mono">{{ conn.console_instance_id || '—' }}</dd></div>
            <div><dt>Connected</dt><dd>{{ fmtDate(conn.connected_at) }}</dd></div>
            <div><dt>Connected by</dt><dd>{{ connectedBy || '—' }}</dd></div>
            <div><dt>Billing key</dt><dd class="mono">{{ conn.key_prefix ? `${conn.key_prefix}…` : '—' }}</dd></div>
            <div><dt>Key last used</dt><dd>{{ conn.key_last_used_at ? fmtDate(conn.key_last_used_at) : 'Never' }}</dd></div>
            <div v-if="conn.portal_allowed_hosts?.length" class="wide">
              <dt>Portal hosts</dt>
              <dd class="mono">{{ conn.portal_allowed_hosts.join(', ') }}</dd>
            </div>
          </dl>
        </div>
      </div>

      <div v-else class="card">
        <div class="card-header"><h2>Connect a console</h2></div>
        <div class="card-body">
          <p class="text-muted form-hint">
            In the console, open the instance and copy its enrolment code, then paste it here.
            Miabi creates a billing-only API key and hands it to that console. Codes expire after a short time.
          </p>
          <textarea v-model="code" class="form-input code-input" rows="3" spellcheck="false"
            placeholder="mce1.…" aria-label="Enrolment code"></textarea>
          <p v-if="code.trim() && !codeValid" class="field-error">An enrolment code starts with <code>mce1.</code></p>
          <div class="connect-actions">
            <button class="btn btn-primary" :disabled="!codeValid || connecting" @click="connect">
              <span class="mdi mdi-link-variant"></span>
              {{ connecting ? 'Connecting…' : 'Connect' }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="showDisconnect"
      title="Disconnect the billing console?"
      message="The billing key is revoked at once, so the console can no longer manage workspaces on this instance. Existing workspaces keep running. You can reconnect later with a new enrolment code."
      confirm-label="Disconnect"
      variant="danger"
      :busy="disconnecting"
      @confirm="disconnect"
      @cancel="showDisconnect = false"
    />
  </div>
</template>

<style scoped>
.billing-page { display: flex; flex-direction: column; gap: 16px; color: var(--text-primary); }
.billing-page .page-header { margin-bottom: 0; }
.subtitle { margin-top: 4px; }
.header-actions { display: flex; gap: 8px; }
.btn-danger-outline {
  background: transparent;
  border: 1px solid var(--danger-600);
  color: var(--danger-600);
}
.btn-danger-outline:hover:not(:disabled) { background: var(--danger-50); }

.notice {
  display: flex;
  gap: 12px;
  padding: 12px 16px;
  border-radius: var(--radius);
  border: 1px solid;
  line-height: 1.5;
}
.notice > .mdi { font-size: 20px; line-height: 1.2; }
.notice-title { font-weight: 600; }
.notice-body { font-size: 13px; }
.notice-body a { color: inherit; text-decoration: underline; margin-left: 4px; }
.notice-warning { background: var(--warning-50); border-color: var(--warning-600); color: var(--warning-600); }
.notice-neutral { background: var(--bg-secondary); border-color: var(--border-primary); color: var(--text-secondary); }

.card-header { display: flex; align-items: center; justify-content: space-between; }
.details {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 18px 24px;
  margin: 0;
}
.details > div { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.details .wide { grid-column: 1 / -1; }
.details dt {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}
.details dd { margin: 0; color: var(--text-primary); word-break: break-word; }
.mono { font-family: var(--font-mono, monospace); font-size: 13px; }

.form-hint { margin-bottom: 10px; max-width: 70ch; line-height: 1.5; }
.code-input { width: 100%; font-family: var(--font-mono, monospace); font-size: 12px; resize: vertical; }
.field-error { margin: 6px 0 0; font-size: 12px; color: var(--danger-600); }
.connect-actions { display: flex; justify-content: flex-end; margin-top: 12px; }
</style>
