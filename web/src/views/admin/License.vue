<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import type { LicenseView } from '@/api/types'
import { copyText } from '@/utils/clipboard'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const licenseStore = useLicenseStore()

async function copyInstallID() {
  const id = view.value?.instance_install_id
  if (!id) return
  if (await copyText(id)) notify.success('Install ID copied')
  else notify.error('Copy failed — select and copy it manually')
}

const loading = ref(false)
const installing = ref(false)
const removing = ref(false)
const token = ref('')
const view = ref<LicenseView | null>(null)

const isCommunity = computed(() => (view.value?.edition ?? 'community') === 'community')
const flagList = computed(() =>
  Object.entries(view.value?.flags ?? {})
    .filter(([, on]) => on)
    .map(([f]) => f),
)

const stateLabel: Record<string, string> = {
  valid: 'Active',
  grace: 'In grace period',
  degraded: 'Expired (read-only)',
  none: 'No license',
  binding_mismatch: 'Wrong deployment',
}

const stateClass = computed(() => {
  switch (view.value?.state) {
    case 'valid':
      return 'badge-success'
    case 'grace':
      return 'badge-warning'
    case 'degraded':
    case 'binding_mismatch':
      return 'badge-danger'
    default:
      return 'badge-muted'
  }
})

function fmtDate(s?: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function fmtLimit(n: number): string {
  return n < 0 ? 'Unlimited' : String(n)
}

function apply(v: LicenseView | null) {
  view.value = v
  licenseStore.set(v) // keep the global banner in sync
}

async function load() {
  loading.value = true
  try {
    const res = await adminApi.getLicense()
    apply(res.data.data)
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function install() {
  if (!token.value.trim() || installing.value) return
  installing.value = true
  try {
    const res = await adminApi.installLicense(token.value.trim())
    apply(res.data.data)
    token.value = ''
    notify.success('License installed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    installing.value = false
  }
}

const showRemoveConfirm = ref(false)
async function remove() {
  showRemoveConfirm.value = false
  if (removing.value) return
  removing.value = true
  try {
    await adminApi.removeLicense()
    await load()
    notify.success('License removed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    removing.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>License</h1>
      <button v-if="!isCommunity" class="btn btn-danger" :disabled="removing" @click="showRemoveConfirm = true">
        <span class="mdi mdi-delete-outline"></span>
        {{ removing ? 'Removing…' : 'Remove license' }}
      </button>
    </div>

    <div v-if="loading" class="spinner"></div>

    <template v-else>
      <!-- Current edition / status -->
      <div class="card">
        <div class="card-body">
          <div class="section-title">Edition</div>
          <div class="edition-row">
            <div class="edition-name">
              {{ view?.edition ?? 'community' }}
              <span v-if="view?.tier" class="badge badge-info" style="text-transform: capitalize">{{ view.tier }}</span>
              <span class="badge" :class="stateClass">{{ stateLabel[view?.state ?? 'none'] }}</span>
            </div>
          </div>

          <div v-if="isCommunity" class="text-muted community-note">
            This is the open-source Community Edition. Install an Enterprise license to
            unlock SSO, custom roles, audit export, white-label, and more.
          </div>

          <!-- Your Install ID: copied by the customer when purchasing a license. -->
          <div class="install-id">
            <div class="install-id-head">
              <span class="detail-k">Your Install ID</span>
              <button class="btn-icon btn-icon-muted" title="Copy Install ID" aria-label="Copy Install ID" @click="copyInstallID">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
            <code class="install-id-value">{{ view?.instance_install_id || '—' }}</code>
            <p class="text-muted text-sm install-id-help">
              A unique ID for this Miabi instance. To unlock enterprise features, contact
              <a href="mailto:sales@miabi.io">sales@miabi.io</a>
              and provide this ID to bind the license to this deployment.
            </p>
          </div>

          <div v-if="view?.state === 'binding_mismatch'" class="url-mismatch">
            <span class="mdi mdi-alert-circle-outline"></span>
            <span v-if="view?.binding_error === 'install_id'">
              This license is bound to Install ID <strong>{{ view?.install_id }}</strong>, which is not this instance,
              so Enterprise features are disabled. Install a license issued for this instance's Install ID (above).
            </span>
            <span v-else>
              This license is bound to <strong>{{ view?.url }}</strong>, but this instance runs on a different URL,
              so Enterprise features are disabled. Install a license issued for this deployment, or an unlimited one.
            </span>
          </div>

          <div v-else class="detail-grid">
            <div class="detail"><span class="detail-k">Customer</span><span>{{ view?.customer || '—' }}</span></div>
            <div v-if="view?.tier" class="detail"><span class="detail-k">Plan</span><span style="text-transform: capitalize">{{ view.tier }}</span></div>
            <div class="detail"><span class="detail-k">Bound to</span><span>{{ view?.install_id || view?.url || 'Unlimited (any instance)' }}</span></div>
            <div class="detail"><span class="detail-k">License ID</span><span>{{ view?.license_id || '—' }}</span></div>
            <div class="detail"><span class="detail-k">Expires</span><span>{{ fmtDate(view?.not_after) }}</span></div>
            <div class="detail">
              <span class="detail-k">Nodes</span>
              <span>{{ view?.node_usage.used ?? 0 }} / {{ fmtLimit(view?.node_usage.limit ?? -1) }}</span>
            </div>
            <div class="detail">
              <span class="detail-k">Plans</span>
              <span>{{ view?.plan_usage.used ?? 0 }} / {{ fmtLimit(view?.plan_usage.limit ?? -1) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Entitlements -->
      <div v-if="!isCommunity" class="card">
        <div class="card-body">
          <div class="section-title">Entitlements</div>
          <div v-if="flagList.length" class="flag-list">
            <span v-for="f in flagList" :key="f" class="badge badge-feature">
              <span class="mdi mdi-check"></span> {{ f }}
            </span>
          </div>
          <div v-else class="text-muted">No feature entitlements on this license.</div>
        </div>
      </div>

      <!-- Install -->
      <div class="card">
        <div class="card-body">
          <div class="section-title">{{ isCommunity ? 'Install a license' : 'Replace license' }}</div>
          <p class="text-muted form-hint">Paste a signed license token (the contents of your <code>.license</code> file).</p>
          <textarea
            v-model="token"
            class="form-input token-input"
            rows="4"
            spellcheck="false"
            placeholder="miabi-v1.…"
            aria-label="License token"
          ></textarea>
          <div class="install-actions">
            <button class="btn btn-primary" :disabled="!token.trim() || installing" @click="install">
              <span class="mdi mdi-key-outline"></span>
              {{ installing ? 'Installing…' : 'Install license' }}
            </button>
          </div>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="showRemoveConfirm"
      title="Remove license"
      :message="`Remove the license and revert to Community Edition?`"
      confirm-label="Remove license"
      variant="danger"
      :busy="removing"
      @confirm="remove"
      @cancel="showRemoveConfirm = false"
    />
  </div>
</template>

<style scoped>
.section-title {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-secondary, var(--text-muted));
  margin-bottom: 12px;
}

.edition-row {
  display: flex;
  align-items: center;
}

.edition-name {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 18px;
  font-weight: 600;
  text-transform: capitalize;
}

.community-note {
  margin-top: 12px;
  max-width: 60ch;
  line-height: 1.5;
}
.url-mismatch {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 12px;
  padding: 12px 14px;
  border-radius: 8px;
  line-height: 1.5;
  background: var(--danger-bg, rgba(220, 38, 38, 0.1));
  color: var(--danger, #b91c1c);
}
.url-mismatch .mdi { font-size: 18px; }
.install-id {
  margin-top: 16px;
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  background: var(--bg-secondary);
}
.install-id-head { display: flex; align-items: center; justify-content: space-between; }
.install-id-value {
  display: block;
  margin-top: 6px;
  font-family: monospace;
  font-size: 14px;
  word-break: break-all;
  color: var(--text-primary);
}
.install-id-help { margin: 8px 0 0; max-width: 60ch; }

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px;
  margin-top: 12px;
}

.detail {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.detail-k {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.flag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}

.badge-success {
  background: var(--success-bg, rgba(34, 197, 94, 0.15));
  color: var(--success, #16a34a);
}
.badge-warning {
  background: var(--warning-bg, rgba(245, 158, 11, 0.15));
  color: var(--warning, #d97706);
}
.badge-danger {
  background: var(--danger-bg, rgba(239, 68, 68, 0.15));
  color: var(--danger, #dc2626);
}
.badge-muted {
  background: var(--bg-tertiary, rgba(120, 120, 120, 0.15));
  color: var(--text-muted);
}
.badge-feature {
  background: var(--bg-tertiary, rgba(120, 120, 120, 0.12));
  color: var(--text-secondary, var(--text-muted));
  font-weight: 500;
  font-family: var(--font-mono, monospace);
}

.token-input {
  width: 100%;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  resize: vertical;
}

.install-actions {
  margin-top: 12px;
}

.form-hint {
  margin-bottom: 10px;
}
</style>
