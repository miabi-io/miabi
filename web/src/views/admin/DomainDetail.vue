<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { AdminDomainDetail, DomainStatus, RouteStatus } from '@/api/types'

const route = useRoute()
const { t } = useI18n()
const router = useRouter()
const notify = useNotificationStore()

const id = computed(() => Number(route.params.id))
const domain = ref<AdminDomainDetail | null>(null)
const loading = ref(false)
const verifying = ref(false)
const forcing = ref(false)
const banning = ref(false)

const status = computed<DomainStatus>(() => {
  const d = domain.value
  if (!d) return 'pending'
  if (d.banned) return 'banned'
  if (d.verified) return 'verified'
  if (d.verification_error) return 'failed'
  return 'pending'
})

async function load() {
  loading.value = true
  try {
    domain.value = (await adminApi.getDomain(id.value)).data.data
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/domains')
  } finally {
    loading.value = false
  }
}

async function verify() {
  if (!domain.value) return
  verifying.value = true
  try {
    await adminApi.verifyDomain(domain.value.id)
    notify.success(`${domain.value.name} verified`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    verifying.value = false
  }
}

const showForceVerify = ref(false)
async function forceVerify() {
  if (!domain.value) return
  showForceVerify.value = false
  forcing.value = true
  try {
    await adminApi.forceVerifyDomain(domain.value.id)
    notify.success(`${domain.value.name} force-verified`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    forcing.value = false
  }
}

const showBan = ref(false)
const banReason = ref('')
function openBan() {
  banReason.value = ''
  showBan.value = true
}
async function ban() {
  if (!domain.value) return
  showBan.value = false
  banning.value = true
  try {
    await adminApi.banDomain(domain.value.id, banReason.value.trim())
    notify.success(`${domain.value.name} banned`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    banning.value = false
  }
}

async function unban() {
  if (!domain.value) return
  banning.value = true
  try {
    await adminApi.unbanDomain(domain.value.id)
    notify.success(`${domain.value.name} unbanned`)
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    banning.value = false
  }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}

function statusBadge(s: DomainStatus): string {
  if (s === 'verified') return 'badge-success'
  if (s === 'failed' || s === 'banned') return 'badge-danger'
  return 'badge-warning'
}

function routeBadge(s?: RouteStatus): string {
  if (s === 'live') return 'badge-success'
  if (s === 'error') return 'badge-danger'
  if (s === 'offline') return 'badge-warning'
  return 'badge-neutral'
}

function fmtDate(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

load()
</script>

<template>
  <div>
    <div v-if="loading && !domain" class="card">
      <div class="card-body"><span class="spinner"></span></div>
    </div>

    <template v-else-if="domain">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" title="Back to domains" aria-label="Back to domains" @click="router.push('/admin/domains')">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ domain.name }}
              <span class="badge" :class="statusBadge(status)">{{ status }}</span>
              <span v-if="domain.wildcard" class="badge badge-info">wildcard</span>
            </h1>
            <span class="subline">{{ domain.workspace_name || ('workspace #' + domain.workspace_id) }}</span>
          </div>
        </div>

        <div class="header-actions">
          <button class="btn btn-primary btn-sm" :disabled="verifying || domain.banned" @click="verify">
            <span class="mdi mdi-check-decagram"></span> {{ verifying ? 'Validating…' : 'Validate domain' }}
          </button>
          <button v-if="!domain.verified" class="btn btn-secondary btn-sm" :disabled="forcing || domain.banned" @click="showForceVerify = true">
            <span class="mdi mdi-shield-alert-outline"></span> Force verify
          </button>
          <button v-if="!domain.banned" class="btn btn-danger btn-sm" :disabled="banning" @click="openBan">
            <span class="mdi mdi-cancel"></span> Ban
          </button>
          <button v-else class="btn btn-secondary btn-sm" :disabled="banning" @click="unban">
            <span class="mdi mdi-check-circle-outline"></span> Unban
          </button>
        </div>
      </div>

      <!-- Banned banner -->
      <div v-if="domain.banned" class="banner banner-danger">
        <span class="mdi mdi-cancel"></span>
        <div>
          <strong>This domain is banned.</strong>
          Its routes are forced offline and it cannot be verified.
          <span v-if="domain.ban_reason"> Reason: {{ domain.ban_reason }}</span>
        </div>
      </div>

      <!-- Privileged-workspace note -->
      <div v-else-if="domain.workspace_privileged && !domain.verified" class="banner banner-info">
        <span class="mdi mdi-shield-star-outline"></span>
        <div>The owning workspace is <strong>privileged</strong> — its routes under this domain can serve even before it is verified.</div>
      </div>

      <div class="grid">
        <!-- Details -->
        <div class="card">
          <div class="card-header"><h2>Details</h2></div>
          <div class="card-body detail-list">
            <div class="detail-row"><span class="detail-label">Workspace</span><span>{{ domain.workspace_name || ('#' + domain.workspace_id) }}</span></div>
            <div class="detail-row"><span class="detail-label">Status</span><span class="badge" :class="statusBadge(status)">{{ status }}</span></div>
            <div class="detail-row"><span class="detail-label">TLS mode</span><span>{{ domain.tls_mode === 'acme' ? 'automatic (ACME)' : 'custom' }}</span></div>
            <div class="detail-row"><span class="detail-label">DNS</span><span class="badge" :class="domain.automated ? 'badge-success' : 'badge-neutral'">{{ domain.automated ? 'automated' : 'manual' }}</span></div>
            <div class="detail-row"><span class="detail-label">Verified at</span><span>{{ fmtDate(domain.verified_at) }}</span></div>
            <div class="detail-row"><span class="detail-label">Last checked</span><span>{{ fmtDate(domain.verification_checked_at) }}</span></div>
            <div v-if="domain.verification_error" class="detail-row"><span class="detail-label">Last error</span><span class="text-danger">{{ domain.verification_error }}</span></div>
            <div class="detail-row"><span class="detail-label">Created</span><span>{{ fmtDate(domain.created_at) }}</span></div>
          </div>
        </div>

        <!-- Ownership challenge -->
        <div class="card">
          <div class="card-header"><h2>Ownership challenge</h2></div>
          <div class="card-body">
            <div v-if="domain.verified" class="gate gate-ok">
              <span class="mdi mdi-check-decagram"></span> Ownership verified — routes under this domain can go live.
            </div>
            <p v-else class="form-hint">Add this TXT record at your DNS provider, then validate.</p>
            <div class="dns-field">
              <label>Host</label>
              <div class="dns-value">
                <code>{{ domain.challenge_host }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(domain.challenge_host)"><span class="mdi mdi-content-copy"></span></button>
              </div>
            </div>
            <div class="dns-field">
              <label>Value</label>
              <div class="dns-value">
                <code>{{ domain.challenge_value }}</code>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(domain.challenge_value)"><span class="mdi mdi-content-copy"></span></button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Dependent routes -->
      <div class="card">
        <div class="card-header"><h2>Routes ({{ domain.routes.length }})</h2></div>
        <div v-if="domain.routes.length === 0" class="card-body text-muted">No routes use this domain yet.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Route</th>
                <th>Hosts</th>
                <th>Status</th>
                <th>Reason</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in domain.routes" :key="r.id">
                <td class="cell-title">{{ r.name }}</td>
                <td class="cell-sub">{{ (r.hosts || []).join(', ') || '—' }}</td>
                <td><span class="badge" :class="routeBadge(r.status)">{{ r.status || 'pending' }}</span></td>
                <td class="cell-sub">{{ r.status_reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Recent activity -->
      <div class="card">
        <div class="card-header"><h2>Recent activity</h2></div>
        <div v-if="domain.recent_events.length === 0" class="card-body text-muted">No recent activity.</div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Action</th><th>When</th></tr>
            </thead>
            <tbody>
              <tr v-for="e in domain.recent_events" :key="e.id">
                <td class="cell-title">{{ e.action }}</td>
                <td class="cell-sub">{{ fmtDate(e.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="showForceVerify"
      :title="$t('confirm.title.forceVerifyDomain')"
      :message="$t('confirm.message.domainDetail.forceMarkNameAs', { name: domain?.name })"
      :confirm-label="$t('action.forceVerify')"
      variant="danger"
      :busy="forcing"
      @confirm="forceVerify"
      @cancel="showForceVerify = false"
    />

    <ConfirmDialog
      :open="showBan"
      :title="$t('confirm.title.banDomain')"
      :message="$t('confirm.message.domainDetail.banNameItsRoutes', { name: domain?.name })"
      :confirm-label="$t('action.ban')"
      variant="danger"
      :busy="banning"
      @confirm="ban"
      @cancel="showBan = false"
    >
      <div class="form-group" style="margin-top: 12px; margin-bottom: 0">
        <label class="form-label" for="ban-reason">Reason <span class="text-muted">(optional)</span></label>
        <input id="ban-reason" v-model="banReason" class="form-input" placeholder="e.g. abuse report" />
      </div>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
}
@media (max-width: 880px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}
.header-actions {
  display: flex;
  gap: 8px;
}
.subline {
  color: var(--text-muted);
  font-size: 13px;
}
.detail-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.detail-row {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: center;
}
.detail-label {
  color: var(--text-muted);
}
.dns-field {
  margin-top: 12px;
}
.dns-field label {
  display: block;
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 4px;
}
.dns-value {
  display: flex;
  align-items: center;
  gap: 8px;
}
.dns-value code {
  flex: 1;
  overflow-x: auto;
  padding: 6px 10px;
  background: var(--bg-tertiary);
  border-radius: 6px;
  font-size: 13px;
}
.gate {
  padding: 10px 12px;
  border-radius: 8px;
  margin-bottom: 8px;
  font-size: 14px;
}
.gate-ok {
  background: color-mix(in srgb, var(--success-500, #22c55e) 12%, transparent);
  color: var(--success-600, #16a34a);
}
.badge .mdi {
  font-size: 13px;
}
.text-danger {
  color: var(--danger-500, #ef4444);
}
.banner {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px 14px;
  border-radius: 8px;
  margin-bottom: 16px;
  font-size: 14px;
}
.banner .mdi {
  font-size: 18px;
  line-height: 1.3;
}
.banner-danger {
  background: color-mix(in srgb, var(--danger-500, #ef4444) 12%, transparent);
  color: var(--danger-600, #dc2626);
}
.banner-info {
  background: color-mix(in srgb, var(--primary-500, #6366f1) 12%, transparent);
  color: var(--primary-600, #4f46e5);
}
</style>
