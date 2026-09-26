<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { securityApi } from '@/api/resources'
import { useNotificationStore } from '@/stores/notification'
import { useElevationStore } from '@/stores/elevation'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import Ports from './Ports.vue'
import type {
  AdminAccessSpec, ApprovedBinding, PortRange, PortsExisting, PortsSpec, PortsSpecMode, SecurityDecision, SecurityEvent,
  SecurityOverview, SecurityPolicy, SecurityPolicyMode, SecurityScopeType, SecurityStatus,
} from '@/api/types'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const elevation = useElevationStore()

type TabKey = 'overview' | 'ports' | 'admin-access' | 'events'
const TABS: { key: TabKey; label: string; icon: string }[] = [
  { key: 'overview', label: 'security.center.tab.overview', icon: 'mdi-shield-check-outline' },
  { key: 'ports', label: 'security.center.tab.ports', icon: 'mdi-lan-connect' },
  { key: 'admin-access', label: 'security.center.tab.adminAccess', icon: 'mdi-shield-account-outline' },
  { key: 'events', label: 'security.center.tab.events', icon: 'mdi-format-list-bulleted' },
]
function tabFromQuery(): TabKey {
  const q = route.query.tab
  return typeof q === 'string' && TABS.some((x) => x.key === q) ? (q as TabKey) : 'overview'
}
const tab = ref<TabKey>(tabFromQuery())
watch(tab, (k) => router.replace({ query: { ...route.query, tab: k } }))

const MODES: SecurityPolicyMode[] = ['off', 'audit', 'enforce']
const SPEC_MODES: PortsSpecMode[] = ['approval', 'auto_approve_in_range', 'reject_all']
const EXISTING: PortsExisting[] = ['keep', 'report', 'revoke']
const OVERRIDE_SCOPES: SecurityScopeType[] = ['plan', 'organization', 'workspace']
const DECISIONS: SecurityDecision[] = ['deny', 'would_deny', 'change']

const status = ref<SecurityStatus | null>(null)
const overview = ref<SecurityOverview | null>(null)
const policies = ref<SecurityPolicy[]>([])
const loading = ref(false)
const queueKey = ref(0)

const entitled = computed(() => status.value?.entitled ?? false)
const canEdit = computed(() => entitled.value && (status.value?.mutable ?? false))
const lockReason = computed(() => {
  if (!status.value || canEdit.value) return ''
  return entitled.value ? t('security.center.lockedFrozen') : t('security.center.lockedEnterprise')
})

async function load() {
  loading.value = true
  try {
    const [st, ov, pol] = await Promise.all([securityApi.status(), securityApi.overview(), securityApi.policies()])
    status.value = st.data.data
    overview.value = ov.data.data
    policies.value = pol.data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

function refresh() {
  queueKey.value++
  load()
  if (tab.value === 'events') loadEvents(true)
}

function parseSpec(raw?: string): PortsSpec {
  try {
    const s = JSON.parse(raw || '{}') as Partial<PortsSpec>
    return { ...s, v: 1, mode: s.mode ?? 'approval' }
  } catch {
    return { v: 1, mode: 'approval' }
  }
}

function rangeLabel(r: PortRange): string {
  const span = r.from === r.to ? `${r.from}` : `${r.from}–${r.to}`
  return r.protocols?.length ? `${span}/${r.protocols.join('+')}` : span
}

function specSummary(s: PortsSpec): string {
  const parts = [t(`security.center.specMode.${s.mode}`)]
  if (s.mode === 'auto_approve_in_range' && s.allowed_ranges?.length) parts.push(s.allowed_ranges.map(rangeLabel).join(', '))
  if (s.bind_address) parts.push(t('security.center.boundTo', { address: s.bind_address }))
  if (s.privileged_bypass === false) parts.push(t('security.center.noBypass'))
  return parts.join(' · ')
}

function scopeLabel(p: Pick<SecurityPolicy, 'scope_type' | 'scope_id'>): string {
  const name = t(`security.center.scope.${p.scope_type}`)
  return p.scope_type === 'platform' ? name : `${name} #${p.scope_id}`
}

const platformPolicy = computed(() =>
  policies.value.find((p) => p.kind === 'ports' && p.scope_type === 'platform') ?? null,
)
const overrides = computed(() => policies.value.filter((p) => p.kind === 'ports' && p.scope_type !== 'platform'))

interface RangeRow { from: number; to: number; tcp: boolean; udp: boolean }
interface PortsForm {
  mode: SecurityPolicyMode
  specMode: PortsSpecMode
  ranges: RangeRow[]
  bindAddress: string
  privilegedBypass: boolean
  existing: PortsExisting
  allowExceptions: boolean
}

function formFrom(p: SecurityPolicy | null): PortsForm {
  const s = parseSpec(p?.spec)
  return {
    mode: p?.mode ?? 'off',
    specMode: s.mode,
    ranges: (s.allowed_ranges ?? []).map((r) => ({
      from: r.from,
      to: r.to,
      tcp: !r.protocols?.length || r.protocols.includes('tcp'),
      udp: !r.protocols?.length || r.protocols.includes('udp'),
    })),
    bindAddress: s.bind_address ?? '',
    privilegedBypass: s.privileged_bypass !== false,
    existing: s.existing ?? 'keep',
    allowExceptions: p?.allow_exceptions ?? false,
  }
}

const form = ref<PortsForm>(formFrom(null))
watch(platformPolicy, (p) => (form.value = formFrom(p)))

function specFrom(f: PortsForm): PortsSpec {
  const spec: PortsSpec = { v: 1, mode: f.specMode, privileged_bypass: f.privilegedBypass, existing: f.existing }
  if (f.specMode === 'auto_approve_in_range') {
    spec.allowed_ranges = f.ranges.map((r) => {
      const range: PortRange = { from: r.from, to: r.to }
      // Both or neither checked means every protocol, which the backend spells as an empty list.
      if (r.tcp !== r.udp) range.protocols = [r.tcp ? 'tcp' : 'udp']
      return range
    })
  }
  if (f.bindAddress.trim()) spec.bind_address = f.bindAddress.trim()
  return spec
}

function addRange() {
  form.value.ranges.push({ from: 30000, to: 30099, tcp: true, udp: true })
}

const saving = ref(false)
async function savePlatform() {
  saving.value = true
  try {
    await securityApi.savePolicy({
      kind: 'ports', scope_type: 'platform', scope_id: 0, mode: form.value.mode,
      allow_exceptions: form.value.allowExceptions, spec: JSON.stringify(specFrom(form.value)),
    })
    notify.success(t('security.center.saved'))
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const override = ref<{ scopeType: SecurityScopeType; scopeId: number; mode: SecurityPolicyMode; specMode: PortsSpecMode }>(
  { scopeType: 'workspace', scopeId: 0, mode: 'enforce', specMode: 'reject_all' },
)
const addingOverride = ref(false)
async function addOverride() {
  if (override.value.scopeId <= 0) return
  // Starting from the platform spec keeps the bind address, bypass and ranges, so the
  // override tightens the request mode without tripping the "loosens" check on the rest.
  const base = parseSpec(platformPolicy.value?.spec)
  const spec: PortsSpec = { ...base, mode: override.value.specMode }
  if (spec.mode !== 'auto_approve_in_range') delete spec.allowed_ranges
  addingOverride.value = true
  try {
    await securityApi.savePolicy({
      kind: 'ports', scope_type: override.value.scopeType, scope_id: override.value.scopeId,
      mode: override.value.mode, allow_exceptions: false, spec: JSON.stringify(spec),
    })
    notify.success(t('security.center.overrideSaved'))
    override.value.scopeId = 0
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    addingOverride.value = false
  }
}

const pendingDelete = ref<SecurityPolicy | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!pendingDelete.value) return
  deleting.value = true
  try {
    await securityApi.deletePolicy(pendingDelete.value.id)
    notify.success(t('security.center.overrideDeleted'))
    pendingDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

const savedSpec = computed(() => parseSpec(platformPolicy.value?.spec))
const revokeOffered = computed(() =>
  platformPolicy.value?.mode === 'enforce' && savedSpec.value.mode === 'reject_all' && savedSpec.value.existing === 'revoke',
)
const revokeList = ref<ApprovedBinding[] | null>(null)
const revoking = ref(false)
async function previewRevoke() {
  revoking.value = true
  try {
    const list = (await securityApi.revokePorts(true)).data.data.bindings ?? []
    if (!list.length) notify.info(t('security.center.revokeNone'))
    else revokeList.value = list
  } catch (e) {
    notify.apiError(e)
  } finally {
    revoking.value = false
  }
}
async function confirmRevoke() {
  revoking.value = true
  try {
    const n = (await securityApi.revokePorts(false)).data.data.bindings?.length ?? 0
    notify.success(t('security.center.revoked', n))
    revokeList.value = null
    queueKey.value++
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    revoking.value = false
  }
}

const DEFAULT_ADMIN_SPEC: AdminAccessSpec = {
  v: 1, required: true, factors: ['totp'], ttl: '30m', idle_timeout: '10m', sensitive_window: '5m', max_attempts: 5, lockout: '15m',
}
const DURATIONS = ['1m', '5m', '10m', '15m', '30m', '1h', '2h', '4h', '8h', '12h', '24h']
const FUTURE_FACTORS = ['passkey', 'pin', 'sso']
const ADMIN_MODES: AdminForm['mode'][] = ['off', 'enforce']

function parseAdminSpec(raw?: string): AdminAccessSpec {
  try {
    return { ...DEFAULT_ADMIN_SPEC, ...(JSON.parse(raw || '{}') as Partial<AdminAccessSpec>), v: 1, factors: ['totp'] }
  } catch {
    return { ...DEFAULT_ADMIN_SPEC }
  }
}

function durationSeconds(d: string): number {
  const unit: Record<string, number> = { h: 3600, m: 60, s: 1 }
  let total = 0
  for (const m of d.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)) total += Number(m[1]) * unit[m[2]]
  return total
}

const adminPolicy = computed(() =>
  policies.value.find((p) => p.kind === 'admin_access' && p.scope_type === 'platform') ?? null,
)

interface AdminForm {
  mode: 'off' | 'enforce'
  required: boolean
  ttl: string
  idle: string
  sensitive: string
  attempts: number
  lockout: string
  ips: string
}
function adminFormFrom(p: SecurityPolicy | null): AdminForm {
  const s = parseAdminSpec(p?.spec)
  return {
    mode: p?.mode === 'enforce' ? 'enforce' : 'off',
    required: s.required,
    ttl: s.ttl,
    idle: s.idle_timeout,
    sensitive: s.sensitive_window,
    attempts: s.max_attempts,
    lockout: s.lockout,
    ips: (s.allowed_ips ?? []).join('\n'),
  }
}
const adminForm = ref<AdminForm>(adminFormFrom(null))
watch(adminPolicy, (p) => (adminForm.value = adminFormFrom(p)))

// A stored value outside the preset list (say "90m") still has to show as selected.
function durationOptions(current: string): string[] {
  return DURATIONS.includes(current) ? DURATIONS : [current, ...DURATIONS]
}

const adminError = computed(() => {
  const f = adminForm.value
  if (durationSeconds(f.idle) > durationSeconds(f.ttl)) return t('security.center.admin.idleExceedsTtl')
  if (!Number.isInteger(f.attempts) || f.attempts < 1 || f.attempts > 50) return t('security.center.admin.attemptsRange')
  return ''
})

const savingAdmin = ref(false)
async function saveAdmin() {
  const f = adminForm.value
  const spec: AdminAccessSpec = {
    v: 1, required: f.required, factors: ['totp'], ttl: f.ttl, idle_timeout: f.idle, sensitive_window: f.sensitive,
    max_attempts: f.attempts, lockout: f.lockout,
    allowed_ips: f.ips.split('\n').map((x) => x.trim()).filter(Boolean),
  }
  savingAdmin.value = true
  try {
    await securityApi.savePolicy({
      kind: 'admin_access', scope_type: 'platform', scope_id: 0, mode: f.mode, allow_exceptions: false, spec: JSON.stringify(spec),
    })
    notify.success(t('security.center.admin.saved'))
    await Promise.all([load(), elevation.refresh()])
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingAdmin.value = false
  }
}

const confirmAdminDelete = ref(false)
async function deleteAdmin() {
  if (!adminPolicy.value) return
  deleting.value = true
  try {
    await securityApi.deletePolicy(adminPolicy.value.id)
    notify.success(t('security.center.admin.deleted'))
    confirmAdminDelete.value = false
    await Promise.all([load(), elevation.refresh()])
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

const unlockMode = computed<'policy' | 'community' | 'off'>(() => {
  const p = adminPolicy.value
  if (p?.mode === 'enforce' && parseAdminSpec(p.spec).required) return 'policy'
  return elevation.state?.required ? 'community' : 'off'
})

interface Check { key: string; ok: boolean; text: string; to?: TabKey }
const checks = computed<Check[]>(() => {
  const o = overview.value
  if (!o) return []
  const p = o.ports
  const list: Check[] = []
  list.push(p.policy && p.spec
    ? { key: 'policy', ok: p.policy.mode === 'enforce', to: 'ports', text: t('security.center.check.policy', {
        mode: t(`security.center.mode.${p.policy.mode}`), spec: specSummary(p.spec) }) }
    : { key: 'policy', ok: false, to: 'ports', text: t('security.center.check.noPolicy') })
  if (p.all_interfaces > 0) list.push({ key: 'ifaces', ok: false, to: 'ports', text: t('security.center.check.allInterfaces', p.all_interfaces) })
  else if (p.approved > 0 && p.spec?.bind_address) list.push({ key: 'ifaces', ok: true, text: t('security.center.check.bound', { address: p.spec.bind_address }) })
  else list.push({ key: 'ifaces', ok: true, text: t('security.center.check.noApproved') })
  list.push(o.privileged_workspaces > 0
    ? { key: 'priv', ok: false, text: t('security.center.check.privileged', o.privileged_workspaces) }
    : { key: 'priv', ok: true, text: t('security.center.check.noPrivileged') })
  list.push(p.pending > 0
    ? { key: 'pending', ok: false, to: 'ports', text: t('security.center.check.pending', p.pending) }
    : { key: 'pending', ok: true, text: t('security.center.check.noPending') })
  list.push({
    key: 'unlock', ok: unlockMode.value !== 'off', to: 'admin-access',
    text: t('security.center.check.unlock', { state: t(`security.center.check.unlockState.${unlockMode.value}`) }),
  })
  if (p.spec?.mode === 'reject_all' && p.grandfathered > 0) {
    list.push({ key: 'grandfathered', ok: false, to: 'ports', text: t('security.center.check.grandfathered', p.grandfathered) })
  }
  return list
})

const DECISION_CLASS: Record<SecurityDecision, string> = {
  deny: 'badge-danger',
  would_deny: 'badge-warning',
  change: 'badge-neutral',
}

const events = ref<SecurityEvent[]>([])
const eventFilter = ref<{ kind: string; decision: string }>({ kind: '', decision: '' })
const eventsLoading = ref(false)
const eventsLoaded = ref(false)
const moreEvents = ref(false)
const PAGE = 100

async function loadEvents(reset: boolean) {
  eventsLoading.value = true
  try {
    const before = reset ? undefined : events.value[events.value.length - 1]?.id
    const page = (await securityApi.events({ ...eventFilter.value, before, limit: PAGE })).data.data ?? []
    events.value = reset ? page : [...events.value, ...page]
    moreEvents.value = page.length === PAGE
    eventsLoaded.value = true
  } catch (e) {
    notify.apiError(e)
  } finally {
    eventsLoading.value = false
  }
}
watch(eventFilter, () => loadEvents(true), { deep: true })
watch(tab, (k) => { if (k === 'events' && !eventsLoaded.value) loadEvents(true) }, { immediate: true })

const exporting = ref(false)
async function exportEvents() {
  exporting.value = true
  try {
    const res = await securityApi.exportEvents(eventFilter.value)
    const url = URL.createObjectURL(res.data)
    const a = document.createElement('a')
    a.href = url
    a.download = 'security-events.csv'
    a.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    notify.apiError(e)
  } finally {
    exporting.value = false
  }
}

function fmtDate(s?: string): string {
  return s ? new Date(s).toLocaleString() : '—'
}

onMounted(() => {
  load()
  if (!elevation.state) elevation.refresh()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>
          {{ $t('security.center.title') }}
          <span v-if="status && !entitled" class="badge badge-muted"><span class="mdi mdi-lock-outline"></span>{{ $t('adminNav.enterprise.title') }}</span>
        </h1>
        <p class="text-muted">{{ $t('security.center.subtitle') }}</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="refresh">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span> {{ $t('security.center.refresh') }}
      </button>
    </div>

    <div v-if="status && !status.enabled" class="app-banner app-banner--warning mb-4">
      <span class="mdi mdi-shield-off-outline app-banner-icon"></span>
      <div class="app-banner-content">
        <p class="app-banner-title">{{ $t('security.center.killSwitchTitle') }}</p>
        <p class="app-banner-text">{{ $t('security.center.killSwitchText') }}</p>
      </div>
    </div>

    <div v-if="status && !entitled" class="app-banner app-banner--info mb-4">
      <span class="mdi mdi-lock-outline app-banner-icon"></span>
      <div class="app-banner-content">
        <p class="app-banner-text">{{ $t('security.center.upgradeHint') }}</p>
        <p><router-link to="/admin/license" class="btn btn-secondary btn-sm">{{ $t('security.center.manageLicense') }}</router-link></p>
      </div>
    </div>
    <div v-else-if="status && !status.mutable" class="app-banner app-banner--warning mb-4">
      <span class="mdi mdi-lock-clock app-banner-icon"></span>
      <div class="app-banner-content">
        <p class="app-banner-text">{{ $t('security.center.lockedFrozen') }}</p>
      </div>
    </div>

    <div class="tabs">
      <button v-for="item in TABS" :key="item.key" class="tab" :class="{ active: tab === item.key }" @click="tab = item.key">
        <span class="mdi" :class="item.icon"></span> {{ $t(item.label) }}
      </button>
    </div>

    <template v-if="tab === 'overview'">
      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('security.center.posture') }}</h2></div>
        <div v-if="!overview" class="card-body"><span class="spinner"></span></div>
        <ul v-else class="checklist">
          <li v-for="c in checks" :key="c.key">
            <span class="mdi" :class="c.ok ? 'mdi-check-circle-outline text-success' : 'mdi-alert-circle-outline text-warning'"></span>
            <span class="check-text">{{ c.text }}</span>
            <button v-if="c.to && !c.ok" class="btn btn-ghost btn-sm" @click="tab = c.to">{{ $t('security.center.review') }}</button>
          </li>
        </ul>
      </div>

      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('security.center.last24h') }}</h2></div>
        <p v-if="!overview?.events_24h?.length" class="card-body text-muted">{{ $t('security.center.noEvents24h') }}</p>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('security.center.col.kind') }}</th><th>{{ $t('security.center.col.decision') }}</th><th class="text-right">{{ $t('security.center.col.count') }}</th></tr></thead>
            <tbody>
              <tr v-for="c in overview.events_24h" :key="`${c.kind}/${c.decision}`">
                <td>{{ $t(`security.center.kind.${c.kind}`) }}</td>
                <td><span class="badge" :class="DECISION_CLASS[c.decision]">{{ $t(`security.center.decision.${c.decision}`) }}</span></td>
                <td class="text-right">{{ c.count }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="overview?.ports.bindings" class="card">
        <div class="card-header">
          <div>
            <h2>{{ $t('security.center.bindingsTitle') }}</h2>
            <p class="form-hint">{{ $t('security.center.bindingsHint') }}</p>
          </div>
        </div>
        <p v-if="!overview.ports.bindings.length" class="card-body text-muted">{{ $t('security.center.check.noApproved') }}</p>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('security.center.col.app') }}</th><th>{{ $t('security.center.col.hostPort') }}</th><th>{{ $t('security.center.col.workspace') }}</th></tr></thead>
            <tbody>
              <tr v-for="b in overview.ports.bindings" :key="b.id">
                <td>
                  {{ b.app_name || `#${b.application_id}` }}
                  <span v-if="b.admin_adopted" class="badge badge-info" style="margin-left: 6px" :title="$t('security.center.adoptedHint')">{{ $t('security.center.adopted') }}</span>
                </td>
                <td class="mono">{{ b.host_port }}/{{ b.protocol }}</td>
                <td>#{{ b.workspace_id }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <template v-else-if="tab === 'ports'">
      <div class="card mb-4">
        <div class="card-header">
          <div>
            <h2>{{ $t('security.center.policyTitle') }}</h2>
            <p class="form-hint">
              <template v-if="platformPolicy">{{ $t('security.center.version', { v: platformPolicy.version, when: fmtDate(platformPolicy.updated_at) }) }}</template>
              <template v-else>{{ $t('security.center.notSet') }}</template>
            </p>
          </div>
        </div>
        <form class="card-body" @submit.prevent="savePlatform">
          <fieldset :disabled="!canEdit" :title="lockReason" class="plain">
            <div class="form-group">
              <label class="form-label">{{ $t('security.center.enforcement') }}</label>
              <div class="radio-row">
                <label v-for="m in MODES" :key="m" class="check-row"><input v-model="form.mode" type="radio" :value="m" /> {{ $t(`security.center.mode.${m}`) }}</label>
              </div>
              <span class="form-hint">{{ $t(`security.center.modeHint.${form.mode}`) }}</span>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('security.center.requests') }}</label>
              <div class="radio-row">
                <label v-for="m in SPEC_MODES" :key="m" class="check-row"><input v-model="form.specMode" type="radio" :value="m" /> {{ $t(`security.center.specMode.${m}`) }}</label>
              </div>
              <span class="form-hint">{{ $t(`security.center.specHint.${form.specMode}`) }}</span>
            </div>

            <div v-if="form.specMode === 'auto_approve_in_range'" class="form-group">
              <label class="form-label">{{ $t('security.center.ranges') }}</label>
              <div v-for="(r, i) in form.ranges" :key="i" class="range-row">
                <input v-model.number="r.from" type="number" min="1" max="65535" class="form-input" :aria-label="$t('security.center.from')" :placeholder="$t('security.center.from')" />
                <span class="text-muted">–</span>
                <input v-model.number="r.to" type="number" min="1" max="65535" class="form-input" :aria-label="$t('security.center.to')" :placeholder="$t('security.center.to')" />
                <label class="check-row"><input v-model="r.tcp" type="checkbox" /> tcp</label>
                <label class="check-row"><input v-model="r.udp" type="checkbox" /> udp</label>
                <button type="button" class="btn-icon btn-icon-danger" :title="$t('security.center.removeRange')" :aria-label="$t('security.center.removeRange')" @click="form.ranges.splice(i, 1)">
                  <span class="mdi mdi-close"></span>
                </button>
              </div>
              <p v-if="!form.ranges.length" class="form-hint text-warning">{{ $t('security.center.noRanges') }}</p>
              <button type="button" class="btn btn-ghost btn-sm" @click="addRange"><span class="mdi mdi-plus"></span>{{ $t('security.center.addRange') }}</button>
            </div>

            <div class="form-group">
              <label class="form-label" for="sec-bind">{{ $t('security.center.bindAddress') }}</label>
              <input id="sec-bind" v-model="form.bindAddress" class="form-input bind-input" placeholder="127.0.0.1" />
              <span class="form-hint">{{ $t('security.center.bindAddressHint') }}</span>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('security.center.existing') }}</label>
              <select v-model="form.existing" class="form-select bind-input">
                <option v-for="x in EXISTING" :key="x" :value="x">{{ $t(`security.center.existingMode.${x}`) }}</option>
              </select>
              <span class="form-hint">{{ $t('security.center.existingHint') }}</span>
            </div>

            <div class="form-group">
              <label class="check-row"><input v-model="form.privilegedBypass" type="checkbox" /> {{ $t('security.center.privilegedBypass') }}</label>
            </div>
            <div class="form-group">
              <label class="check-row"><input v-model="form.allowExceptions" type="checkbox" /> {{ $t('security.center.allowExceptions') }}</label>
              <span class="form-hint">{{ $t('security.center.allowExceptionsHint') }}</span>
            </div>
          </fieldset>

          <div class="flex items-center gap-2">
            <button type="submit" class="btn btn-primary" :disabled="!canEdit || saving" :title="lockReason">
              {{ saving ? $t('security.center.saving') : $t('security.center.save') }}
            </button>
            <button v-if="revokeOffered" type="button" class="btn btn-danger" :disabled="!canEdit || revoking" :title="lockReason || $t('security.center.revokeHint')" @click="previewRevoke">
              <span class="mdi mdi-link-off"></span> {{ $t('security.center.revokeTitle') }}
            </button>
          </div>
        </form>
      </div>

      <div class="card mb-4">
        <div class="card-header">
          <div>
            <h2>{{ $t('security.center.overridesTitle') }}</h2>
            <p class="form-hint">{{ $t('security.center.overridesHint') }}</p>
          </div>
        </div>
        <p v-if="!overrides.length" class="card-body text-muted">{{ $t('security.center.noOverrides') }}</p>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>{{ $t('security.center.col.scope') }}</th><th>{{ $t('security.center.col.mode') }}</th><th>{{ $t('security.center.col.rule') }}</th><th></th></tr></thead>
            <tbody>
              <tr v-for="p in overrides" :key="p.id">
                <td>{{ scopeLabel(p) }}</td>
                <td><span class="badge badge-neutral">{{ $t(`security.center.mode.${p.mode}`) }}</span></td>
                <td class="text-muted">{{ specSummary(parseSpec(p.spec)) }}</td>
                <td class="text-right">
                  <button class="btn-icon btn-icon-danger" :title="$t('security.center.deleteOverride')" :aria-label="$t('security.center.deleteOverride')" @click="pendingDelete = p">
                    <span class="mdi mdi-delete-outline"></span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <form class="card-body override-form" @submit.prevent="addOverride">
          <select v-model="override.scopeType" class="form-select" :aria-label="$t('security.center.col.scope')" :disabled="!canEdit" :title="lockReason">
            <option v-for="s in OVERRIDE_SCOPES" :key="s" :value="s">{{ $t(`security.center.scope.${s}`) }}</option>
          </select>
          <input v-model.number="override.scopeId" type="number" min="1" class="form-input id-input" :placeholder="$t('security.center.scopeId')" :aria-label="$t('security.center.scopeId')" :disabled="!canEdit" :title="lockReason" />
          <select v-model="override.mode" class="form-select" :aria-label="$t('security.center.enforcement')" :disabled="!canEdit" :title="lockReason">
            <option v-for="m in MODES" :key="m" :value="m">{{ $t(`security.center.mode.${m}`) }}</option>
          </select>
          <select v-model="override.specMode" class="form-select" :aria-label="$t('security.center.requests')" :disabled="!canEdit" :title="lockReason">
            <option v-for="m in SPEC_MODES" :key="m" :value="m">{{ $t(`security.center.specMode.${m}`) }}</option>
          </select>
          <button type="submit" class="btn btn-secondary" :disabled="!canEdit || addingOverride || override.scopeId <= 0" :title="lockReason">
            <span class="mdi mdi-plus"></span>{{ $t('security.center.addOverride') }}
          </button>
        </form>
      </div>

      <h2 class="queue-title">{{ $t('security.center.queueTitle') }}</h2>
      <Ports :key="queueKey" embedded />
    </template>

    <template v-else-if="tab === 'admin-access'">
      <div class="card mb-4">
        <div class="card-header"><h2>{{ $t('security.center.admin.howTitle') }}</h2></div>
        <div class="card-body explain">
          <p>{{ $t('security.center.admin.howSignIn') }}</p>
          <p>{{ $t('security.center.admin.howSensitive') }}</p>
          <p>{{ $t('security.center.admin.howApiKeys') }}</p>
          <p>{{ $t('security.center.admin.howThreats') }}</p>
          <p class="text-muted">{{ $t('security.center.admin.howCommunity') }}</p>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <div>
            <h2>{{ $t('security.center.admin.policyTitle') }}</h2>
            <p class="form-hint">
              <template v-if="adminPolicy">{{ $t('security.center.version', { v: adminPolicy.version, when: fmtDate(adminPolicy.updated_at) }) }}</template>
              <template v-else>{{ $t('security.center.admin.notSet') }}</template>
            </p>
          </div>
        </div>
        <form class="card-body" @submit.prevent="saveAdmin">
          <fieldset :disabled="!canEdit" :title="lockReason" class="plain">
            <div class="form-group">
              <label class="form-label">{{ $t('security.center.enforcement') }}</label>
              <div class="radio-row">
                <label v-for="m in ADMIN_MODES" :key="m" class="check-row"><input v-model="adminForm.mode" type="radio" :value="m" /> {{ $t(`security.center.mode.${m}`) }}</label>
              </div>
              <span class="form-hint">{{ $t('security.center.admin.noAudit') }}</span>
            </div>

            <div class="form-group">
              <label class="check-row"><input v-model="adminForm.required" type="checkbox" /> {{ $t('security.center.admin.required') }}</label>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('security.center.admin.factors') }}</label>
              <div class="radio-row">
                <label class="check-row"><input type="checkbox" checked disabled /> {{ $t('security.center.admin.factor.totp') }}</label>
                <label v-for="f in FUTURE_FACTORS" :key="f" class="check-row text-muted" :title="$t('security.center.admin.notAvailable')">
                  <input type="checkbox" disabled /> {{ $t(`security.center.admin.factor.${f}`) }} ({{ $t('security.center.admin.notAvailable') }})
                </label>
              </div>
            </div>

            <div class="duration-grid">
              <div class="form-group">
                <label class="form-label" for="aa-ttl">{{ $t('security.center.admin.ttl') }}</label>
                <select id="aa-ttl" v-model="adminForm.ttl" class="form-select">
                  <option v-for="d in durationOptions(adminForm.ttl)" :key="d" :value="d">{{ d }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label" for="aa-idle">{{ $t('security.center.admin.idle') }}</label>
                <select id="aa-idle" v-model="adminForm.idle" class="form-select">
                  <option v-for="d in durationOptions(adminForm.idle)" :key="d" :value="d">{{ d }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label" for="aa-sensitive">{{ $t('security.center.admin.sensitive') }}</label>
                <select id="aa-sensitive" v-model="adminForm.sensitive" class="form-select">
                  <option v-for="d in durationOptions(adminForm.sensitive)" :key="d" :value="d">{{ d }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label" for="aa-attempts">{{ $t('security.center.admin.attempts') }}</label>
                <input id="aa-attempts" v-model.number="adminForm.attempts" type="number" min="1" max="50" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label" for="aa-lockout">{{ $t('security.center.admin.lockout') }}</label>
                <select id="aa-lockout" v-model="adminForm.lockout" class="form-select">
                  <option v-for="d in durationOptions(adminForm.lockout)" :key="d" :value="d">{{ d }}</option>
                </select>
              </div>
            </div>

            <div class="form-group">
              <label class="form-label" for="aa-ips">{{ $t('security.center.admin.allowedIps') }}</label>
              <textarea id="aa-ips" v-model="adminForm.ips" class="form-input mono" rows="4" placeholder="203.0.113.7&#10;10.0.0.0/8"></textarea>
              <span class="form-hint">{{ $t('security.center.admin.allowedIpsHint') }}</span>
            </div>
          </fieldset>

          <p v-if="adminError" class="form-hint text-warning">{{ adminError }}</p>
          <div class="flex items-center gap-2">
            <button type="submit" class="btn btn-primary" :disabled="!canEdit || savingAdmin || !!adminError" :title="lockReason">
              {{ savingAdmin ? $t('security.center.saving') : $t('security.center.save') }}
            </button>
            <button v-if="adminPolicy" type="button" class="btn btn-secondary" @click="confirmAdminDelete = true">
              {{ $t('security.center.admin.delete') }}
            </button>
          </div>
        </form>
      </div>
    </template>

    <template v-else>
      <div class="card">
        <div class="card-header">
          <div class="flex items-center gap-2">
            <select v-model="eventFilter.kind" class="form-select" :aria-label="$t('security.center.col.kind')">
              <option value="">{{ $t('security.center.allKinds') }}</option>
              <option value="ports">{{ $t('security.center.kind.ports') }}</option>
              <option value="policy">{{ $t('security.center.kind.policy') }}</option>
            </select>
            <select v-model="eventFilter.decision" class="form-select" :aria-label="$t('security.center.col.decision')">
              <option value="">{{ $t('security.center.allDecisions') }}</option>
              <option v-for="d in DECISIONS" :key="d" :value="d">{{ $t(`security.center.decision.${d}`) }}</option>
            </select>
          </div>
          <button class="btn btn-secondary btn-sm" :disabled="exporting" @click="exportEvents">
            <span class="mdi mdi-download"></span> {{ $t('security.center.export') }}
          </button>
        </div>
        <div v-if="eventsLoading && !events.length" class="card-body"><span class="spinner"></span></div>
        <p v-else-if="!events.length" class="card-body text-muted">{{ $t('security.center.noEvents') }}</p>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>{{ $t('security.center.col.time') }}</th>
                <th>{{ $t('security.center.col.kind') }}</th>
                <th>{{ $t('security.center.col.action') }}</th>
                <th>{{ $t('security.center.col.decision') }}</th>
                <th>{{ $t('security.center.col.workspace') }}</th>
                <th>{{ $t('security.center.col.resource') }}</th>
                <th>{{ $t('security.center.col.scope') }}</th>
                <th>{{ $t('security.center.col.reason') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td class="text-muted nowrap">{{ fmtDate(e.created_at) }}</td>
                <td>{{ $t(`security.center.kind.${e.kind}`) }}</td>
                <td class="mono">{{ e.action }}</td>
                <td><span class="badge" :class="DECISION_CLASS[e.decision]">{{ $t(`security.center.decision.${e.decision}`) }}</span></td>
                <td>{{ e.workspace_id ? `#${e.workspace_id}` : '—' }}</td>
                <td class="mono">{{ e.resource || '—' }}</td>
                <td>{{ e.scope || '—' }}</td>
                <td class="text-muted">{{ e.reason || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="moreEvents" class="card-body load-more">
          <button class="btn btn-secondary btn-sm" :disabled="eventsLoading" @click="loadEvents(false)">{{ $t('security.center.loadMore') }}</button>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="!!pendingDelete"
      :title="$t('security.center.deleteOverrideTitle')"
      :message="$t('security.center.deleteOverrideMessage', { scope: pendingDelete ? scopeLabel(pendingDelete) : '' })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />

    <ConfirmDialog
      :open="confirmAdminDelete"
      :title="$t('security.center.admin.deleteTitle')"
      :message="$t('security.center.admin.deleteMessage')"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="deleteAdmin"
      @cancel="confirmAdminDelete = false"
    />

    <ConfirmDialog
      :open="!!revokeList"
      :title="$t('security.center.revokeConfirmTitle', revokeList?.length ?? 0)"
      :message="$t('security.center.revokeHint')"
      :confirm-label="$t('security.center.revoke')"
      variant="danger"
      :busy="revoking"
      @confirm="confirmRevoke"
      @cancel="revokeList = null"
    >
      <ul class="revoke-list">
        <li v-for="b in revokeList ?? []" :key="b.id">
          <span class="mono">{{ b.host_port }}/{{ b.protocol }}</span>
          <span>{{ b.app_name || `#${b.application_id}` }}</span>
          <span class="text-muted">{{ $t('security.center.scope.workspace') }} #{{ b.workspace_id }}</span>
        </li>
      </ul>
    </ConfirmDialog>
  </div>
</template>

<style scoped>
.page-header h1 {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tabs {
  margin-bottom: 16px;
}
.checklist {
  list-style: none;
  margin: 0;
  padding: 0;
}
.checklist li {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 20px;
  border-top: 1px solid var(--border-primary);
}
.checklist li:first-child {
  border-top: none;
}
.checklist .mdi {
  font-size: 18px;
}
.check-text {
  flex: 1;
}
.text-success {
  color: var(--success-600);
}
.plain {
  border: none;
  margin: 0;
  padding: 0;
  min-width: 0;
}
.radio-row {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.range-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.range-row .form-input {
  max-width: 110px;
}
.bind-input {
  max-width: 260px;
}
.override-form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  border-top: 1px solid var(--border-primary);
}
.override-form .form-select {
  width: auto;
}
.id-input {
  max-width: 110px;
}
.queue-title {
  font-size: 15px;
  font-weight: 600;
  margin: 8px 0 12px;
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
.nowrap {
  white-space: nowrap;
}
.explain p {
  margin: 0 0 8px;
  max-width: 80ch;
}
.explain p:last-child {
  margin-bottom: 0;
}
.duration-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 0 16px;
}
.load-more {
  text-align: center;
}
.revoke-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 260px;
  overflow-y: auto;
}
.revoke-list li {
  display: flex;
  gap: 10px;
  padding: 4px 0;
  font-size: 13px;
}
</style>
