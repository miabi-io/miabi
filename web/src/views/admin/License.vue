<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import type { LicenseView } from '@/api/types'
import { copyText } from '@/utils/clipboard'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const { t } = useI18n()
const licenseStore = useLicenseStore()

const loading = ref(false)
const installing = ref(false)
const removing = ref(false)
const token = ref('')
const view = ref<LicenseView | null>(null)
const showInstall = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

const DAY = 24 * 60 * 60 * 1000
const EXPIRY_WARNING_DAYS = 30

const isCommunity = computed(() => (view.value?.edition ?? 'community') === 'community')
const state = computed(() => view.value?.state ?? 'none')
const mismatch = computed(() => state.value === 'binding_mismatch')

const stateLabel: Record<string, string> = {
  valid: 'Active',
  grace: 'Grace period',
  degraded: 'Expired · read-only',
  none: 'No license',
  binding_mismatch: 'Wrong instance',
}

const stateClass = computed(() => {
  switch (state.value) {
    case 'valid':
      return 'badge-success'
    case 'grace':
      return 'badge-warning'
    case 'degraded':
    case 'binding_mismatch':
      return 'badge-danger'
    default:
      return 'badge-neutral'
  }
})

function daysUntil(s?: string | null): number | null {
  if (!s) return null
  return Math.ceil((new Date(s).getTime() - Date.now()) / DAY)
}

function fmtDate(s?: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

const daysLeft = computed(() => daysUntil(view.value?.not_after))
const graceDaysLeft = computed(() => daysUntil(view.value?.grace_ends))

// One line that answers "how long is this license good for?".
const expirySummary = computed(() => {
  const v = view.value
  if (!v?.not_after) return ''
  if (mismatch.value) return `Not active on this instance · issued until ${fmtDate(v.not_after)}`
  if (state.value === 'grace' && graceDaysLeft.value !== null) {
    return `Expired ${fmtDate(v.not_after)} · full access for ${plural(Math.max(graceDaysLeft.value, 0), 'more day')}`
  }
  if (state.value === 'degraded') return `Expired ${fmtDate(v.not_after)} · grace ended ${fmtDate(v.grace_ends)}`
  if (daysLeft.value !== null && daysLeft.value >= 0) {
    return `Valid until ${fmtDate(v.not_after)} · ${plural(daysLeft.value, 'day')} left`
  }
  return `Valid until ${fmtDate(v.not_after)}`
})

interface Alert {
  kind: 'danger' | 'warning'
  icon: string
  title: string
  body: string
}

const alerts = computed<Alert[]>(() => {
  const v = view.value
  if (!v) return []
  const out: Alert[] = []
  if (mismatch.value) {
    out.push(
      v.binding_error === 'install_id'
        ? {
            kind: 'danger',
            icon: 'mdi-link-variant-off',
            title: 'This license belongs to another instance',
            body: `It is bound to Install ID ${v.install_id}, not this instance's. Enterprise features are off until you install a license issued for this Install ID.`,
          }
        : {
            kind: 'danger',
            icon: 'mdi-link-variant-off',
            title: 'This license belongs to another deployment',
            body: `It is bound to ${v.url}, but this instance runs on a different URL. Enterprise features are off until you install a license for this deployment.`,
          },
    )
  } else if (state.value === 'degraded') {
    out.push({
      kind: 'danger',
      icon: 'mdi-lock-clock',
      title: 'License expired',
      body: 'Enterprise features are read-only: existing configuration keeps working but cannot be changed. Install a renewed license to restore full access.',
    })
  } else if (state.value === 'grace') {
    out.push({
      kind: 'warning',
      icon: 'mdi-timer-sand',
      title: 'License expired — grace period',
      body: `Everything still works until ${fmtDate(v.grace_ends)}. After that, Enterprise features become read-only.`,
    })
  } else if (state.value === 'valid' && daysLeft.value !== null && daysLeft.value <= EXPIRY_WARNING_DAYS) {
    out.push({
      kind: 'warning',
      icon: 'mdi-calendar-alert',
      title: `License expires in ${plural(Math.max(daysLeft.value, 0), 'day')}`,
      body: `Renew before ${fmtDate(v.not_after)} to avoid the grace period.`,
    })
  }
  if (v.warnings?.includes('over_node_limit')) {
    out.push({
      kind: 'warning',
      icon: 'mdi-server-network',
      title: 'Over the node limit',
      body: `${v.node_usage.used} nodes are registered besides the control plane, but the license covers ${v.node_usage.limit}. Remove nodes or upgrade the license.`,
    })
  }
  return out
})

const boundTo = computed(() => {
  const v = view.value
  if (!v) return { label: '—', matches: false }
  if (v.install_id) return { label: v.install_id, matches: v.install_id === v.instance_install_id }
  if (v.url) return { label: v.url, matches: !mismatch.value }
  return { label: 'Any instance', matches: true }
})

interface Meter {
  label: string
  used: number
  limit: number
}

const meters = computed<Meter[]>(() => {
  const v = view.value
  if (!v) return []
  return [
    { label: 'Nodes (besides control plane)', used: v.node_usage.used, limit: v.node_usage.limit },
    { label: 'Plans', used: v.plan_usage.used, limit: v.plan_usage.limit },
    { label: 'Storage classes', used: v.storage_class_usage.used, limit: v.storage_class_usage.limit },
    { label: 'Shared runners', used: v.shared_runner_usage.used, limit: v.shared_runner_usage.limit },
  ]
})

function meterPct(m: Meter): number {
  if (m.limit < 0) return 0
  if (m.limit === 0) return m.used > 0 ? 100 : 0
  return Math.min(100, Math.round((m.used / m.limit) * 100))
}

function meterClass(m: Meter): string {
  if (m.limit < 0) return ''
  if (m.used > m.limit) return 'over'
  return meterPct(m) >= 80 ? 'near' : ''
}

const FEATURE_LABELS: Record<string, string> = {
  multi_sso: 'Multiple SSO providers',
  sso_hidden_provider: 'Hidden SSO providers',
  sso_saml: 'SAML single sign-on',
  sso_ldap: 'LDAP / Active Directory',
  scim: 'SCIM provisioning',
  custom_roles: 'Custom roles',
  resource_policies: 'Per-resource access',
  quota_override: 'Workspace quota overrides',
  placement_policy: 'Placement policies',
  audit_log: 'Audit log',
  audit_export: 'Audit export & retention',
  siem_stream: 'SIEM streaming',
  ha: 'High availability',
  dr: 'Disaster recovery',
  platform_backup: 'Platform backup & restore',
  white_label: 'White-label branding',
  private_registry: 'Private registry',
  security_profile: 'Restricted security profile',
  platform_runners: 'Shared runners',
  user_workspace_limit: 'Per-user workspace limits',
  user_workspace_membership_limit: 'Per-user membership limits',
  analytics_export: 'Analytics export',
  advanced_canary: 'Advanced canary releases',
  announcements: 'Announcements',
  database_sizes: 'Database sizes',
  storage_classes: 'Storage classes',
  recovery_points: 'Recovery points',
  organizations: 'Multiple organizations',
  security_policies: 'Security Center policies',
  live_migration: 'Live migration',
  billing: 'Billing API (add-on)',
  ee_features: 'Enterprise features',
}

function featureLabel(flag: string): string {
  if (FEATURE_LABELS[flag]) return FEATURE_LABELS[flag]
  const s = flag.replace(/_/g, ' ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

const features = computed(() =>
  Object.entries(view.value?.flags ?? {})
    .filter(([, on]) => on)
    .map(([flag]) => ({ flag, label: featureLabel(flag) }))
    .sort((a, b) => a.label.localeCompare(b.label)),
)

async function copy(value: string | undefined, what: string) {
  if (!value) return
  if (await copyText(value)) notify.success(`${what} copied`)
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
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

async function onFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  token.value = (await file.text()).trim()
  if (fileInput.value) fileInput.value.value = ''
}

async function install() {
  if (!token.value.trim() || installing.value) return
  installing.value = true
  try {
    const res = await adminApi.installLicense(token.value.trim())
    apply(res.data.data)
    token.value = ''
    showInstall.value = false
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
  <div class="license-page">
    <div class="page-header">
      <div>
        <h1>License</h1>
        <p class="text-muted text-sm subtitle">Edition, entitlements and usage for this Miabi instance.</p>
      </div>
      <div v-if="!loading && !isCommunity" class="header-actions">
        <button class="btn btn-secondary" @click="showInstall = !showInstall">
          <span class="mdi mdi-swap-horizontal"></span> Replace license
        </button>
        <button class="btn btn-danger-outline" :disabled="removing" @click="showRemoveConfirm = true">
          <span class="mdi mdi-delete-outline"></span> {{ removing ? 'Removing…' : 'Remove' }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="spinner"></div>

    <template v-else>
      <div v-for="a in alerts" :key="a.title" class="notice" :class="`notice-${a.kind}`" role="alert">
        <span class="mdi" :class="a.icon"></span>
        <div>
          <div class="notice-title">{{ a.title }}</div>
          <div class="notice-body">{{ a.body }}</div>
        </div>
      </div>

      <!-- Status -->
      <div class="card">
        <div class="card-body status">
          <div class="status-main">
            <div class="status-icon" :class="isCommunity ? 'is-community' : `is-${state}`">
              <span class="mdi" :class="isCommunity ? 'mdi-open-source-initiative' : 'mdi-shield-check-outline'"></span>
            </div>
            <div class="status-text">
              <div class="edition">
                <span class="edition-name">{{ isCommunity ? 'Community Edition' : `${view?.edition} Edition` }}</span>
                <span v-if="view?.tier" class="badge badge-info tier">{{ view.tier }}</span>
                <span v-if="!isCommunity" class="badge" :class="stateClass">{{ stateLabel[state] }}</span>
              </div>
              <p v-if="isCommunity" class="text-muted status-line">
                The open-source edition. An Enterprise license unlocks SSO, custom roles, audit export,
                white-label branding and more.
              </p>
              <p v-else-if="expirySummary" class="status-line">{{ expirySummary }}</p>
            </div>
          </div>

          <div class="install-id">
            <span class="field-label">Your Install ID</span>
            <div class="copy-field">
              <code>{{ view?.instance_install_id || '—' }}</code>
              <button class="btn-icon btn-icon-muted" title="Copy Install ID" aria-label="Copy Install ID"
                @click="copy(view?.instance_install_id, 'Install ID')">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
            <p class="text-muted text-sm">
              Give this ID to <a href="mailto:sales@miabi.io">sales@miabi.io</a> when buying a license, so it is issued for this instance.
            </p>
          </div>
        </div>
      </div>

      <!-- Details: shown in every licensed state, including a mismatch, so the dates are never hidden. -->
      <div v-if="!isCommunity" class="card">
        <div class="card-header"><h2>License details</h2></div>
        <div class="card-body">
          <dl class="details">
            <div><dt>Customer</dt><dd>{{ view?.customer || '—' }}</dd></div>
            <div>
              <dt>License ID</dt>
              <dd class="mono with-copy">
                {{ view?.license_id || '—' }}
                <button v-if="view?.license_id" class="btn-icon btn-icon-muted btn-icon-sm" title="Copy License ID" aria-label="Copy License ID"
                  @click="copy(view?.license_id, 'License ID')">
                  <span class="mdi mdi-content-copy"></span>
                </button>
              </dd>
            </div>
            <div>
              <dt>Valid until</dt>
              <dd>
                {{ fmtDate(view?.not_after) }}
                <span v-if="daysLeft !== null && daysLeft >= 0" class="text-muted">({{ plural(daysLeft, 'day') }})</span>
              </dd>
            </div>
            <div><dt>Grace period ends</dt><dd>{{ fmtDate(view?.grace_ends) }}</dd></div>
            <div class="wide">
              <dt>Bound to</dt>
              <dd class="bound">
                <span :class="{ mono: view?.install_id }">{{ boundTo.label }}</span>
                <span v-if="boundTo.matches" class="badge badge-success"><span class="mdi mdi-check"></span> This instance</span>
                <span v-else class="badge badge-danger"><span class="mdi mdi-close"></span> Not this instance</span>
              </dd>
            </div>
          </dl>
        </div>
      </div>

      <!-- Usage -->
      <div v-if="!isCommunity && !mismatch" class="card">
        <div class="card-header"><h2>Usage</h2></div>
        <div class="card-body">
          <div class="meters">
            <div v-for="m in meters" :key="m.label" class="meter" :class="meterClass(m)">
              <div class="meter-head">
                <span>{{ m.label }}</span>
                <span class="meter-value">{{ m.used }} <span class="text-muted">/ {{ m.limit < 0 ? '∞' : m.limit }}</span></span>
              </div>
              <div class="meter-track">
                <div class="meter-fill" :style="{ width: m.limit < 0 ? '100%' : `${meterPct(m)}%` }" :class="{ unlimited: m.limit < 0 }"></div>
              </div>
              <div v-if="m.limit < 0" class="text-muted text-xs">Unlimited</div>
            </div>
          </div>
        </div>
      </div>

      <!-- Features -->
      <div v-if="!isCommunity" class="card">
        <div class="card-header">
          <h2>Included features</h2>
          <span class="text-muted text-sm">{{ features.length }}</span>
        </div>
        <div class="card-body">
          <p v-if="mismatch" class="text-muted">No features are active while the license is bound to another instance.</p>
          <ul v-else-if="features.length" class="features">
            <li v-for="f in features" :key="f.flag" :title="f.flag">
              <span class="mdi mdi-check-circle"></span>{{ f.label }}
            </li>
          </ul>
          <p v-else class="text-muted">This license includes no feature entitlements.</p>
        </div>
      </div>

      <!-- Install / replace -->
      <div v-if="isCommunity || showInstall" class="card">
        <div class="card-header"><h2>{{ isCommunity ? 'Install a license' : 'Replace license' }}</h2></div>
        <div class="card-body">
          <p class="text-muted form-hint">
            Upload your <code>.license</code> file or paste its contents.
            <template v-if="!isCommunity">The current license is replaced once the new one validates.</template>
          </p>
          <textarea
            v-model="token"
            class="form-input token-input"
            rows="4"
            spellcheck="false"
            placeholder="miabi-v1.…"
            aria-label="License token"
          ></textarea>
          <div class="install-actions">
            <input ref="fileInput" type="file" accept=".license,.txt,text/plain" class="sr-only" @change="onFile" />
            <button class="btn btn-secondary" type="button" @click="fileInput?.click()">
              <span class="mdi mdi-file-upload-outline"></span> Upload file
            </button>
            <span class="spacer"></span>
            <button v-if="!isCommunity" class="btn btn-ghost" type="button" @click="showInstall = false; token = ''">Cancel</button>
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
      :title="$t('confirm.title.removeLicense')"
      :message="$t('confirm.message.license.removeTheLicenseAnd')"
      :confirm-label="$t('action.removeLicense')"
      variant="danger"
      :busy="removing"
      @confirm="remove"
      @cancel="showRemoveConfirm = false"
    />
  </div>
</template>

<style scoped>
.license-page { display: flex; flex-direction: column; gap: 16px; color: var(--text-primary); }
.license-page .page-header { margin-bottom: 0; }
.subtitle { margin-top: 4px; }
.header-actions { display: flex; gap: 8px; flex-wrap: wrap; }
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
.notice-danger { background: var(--danger-50); border-color: var(--danger-600); color: var(--danger-600); }
.notice-warning { background: var(--warning-50); border-color: var(--warning-600); color: var(--warning-600); }

.status {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 360px);
  gap: 24px;
  align-items: start;
}
.status-main { display: flex; gap: 16px; align-items: flex-start; min-width: 0; }
.status-icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border-radius: 12px;
  font-size: 26px;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}
.status-icon.is-valid { background: var(--success-50); color: var(--success-600); }
.status-icon.is-grace { background: var(--warning-50); color: var(--warning-600); }
.status-icon.is-degraded,
.status-icon.is-binding_mismatch { background: var(--danger-50); color: var(--danger-600); }
.status-text { min-width: 0; }
.edition { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.edition-name { font-size: 20px; font-weight: 700; letter-spacing: -0.01em; text-transform: capitalize; }
.tier { text-transform: capitalize; }
.status-line { margin: 6px 0 0; max-width: 60ch; line-height: 1.5; }

.install-id {
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-secondary);
}
.install-id p { margin: 8px 0 0; }
.field-label, .details dt {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}
.copy-field { display: flex; align-items: center; gap: 8px; margin-top: 6px; }
.copy-field code {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono, monospace);
  font-size: 13px;
  word-break: break-all;
  color: var(--text-primary);
}

.details {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 18px 24px;
  margin: 0;
}
.details > div { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.details .wide { grid-column: 1 / -1; }
.details dd { margin: 0; color: var(--text-primary); word-break: break-word; }
.mono { font-family: var(--font-mono, monospace); font-size: 13px; }
.with-copy, .bound { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.btn-icon-sm { width: 26px; height: 26px; font-size: 15px; }

.meters { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 20px; }
.meter-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px; font-size: 13px; }
.meter-value { font-weight: 600; font-variant-numeric: tabular-nums; }
.meter-track { height: 6px; border-radius: 999px; background: var(--bg-tertiary); overflow: hidden; }
.meter-fill { height: 100%; border-radius: inherit; background: var(--primary-500); transition: width 0.3s; }
.meter-fill.unlimited { opacity: 0.25; }
.meter.near .meter-fill { background: var(--warning-600); }
.meter.over .meter-fill { background: var(--danger-600); }
.meter.over .meter-value { color: var(--danger-600); }
.text-xs { font-size: 11px; margin-top: 6px; }

.card-header { display: flex; align-items: center; justify-content: space-between; }
.features {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px 16px;
}
.features li { display: flex; align-items: center; gap: 8px; font-size: 14px; }
.features .mdi { color: var(--success-600); }

.form-hint { margin-bottom: 10px; }
.token-input { width: 100%; font-family: var(--font-mono, monospace); font-size: 12px; resize: vertical; }
.install-actions { display: flex; align-items: center; gap: 8px; margin-top: 12px; flex-wrap: wrap; }
.spacer { flex: 1; }
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

@media (max-width: 800px) {
  .status { grid-template-columns: 1fr; }
}
</style>
