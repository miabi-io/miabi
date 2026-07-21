<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { AdminWorkspaceDetail, AdminEvent, AdminWorkspaceMember, Plan, WorkspaceQuotaOverride } from '@/api/types'

const route = useRoute()
const router = useRouter()
const licenseStore = useLicenseStore()
// Per-workspace quota overrides are an Enterprise capability.
const quotaOverride = useEntitlement('quota_override')
// The restricted security profile needs its own Enterprise flag on top of
// quota_override, mirroring the backend gate.
const securityProfile = useEntitlement('security_profile')
const notify = useNotificationStore()

const wsId = computed(() => Number(route.params.id))
const ws = ref<AdminWorkspaceDetail | null>(null)
const loading = ref(false)
const busy = ref(false)

const plans = ref<Plan[]>([])
const selectedPlanId = ref<number | null>(null)

async function load() {
  loading.value = true
  try {
    ws.value = (await adminApi.getWorkspace(wsId.value)).data.data
    selectedPlanId.value = ws.value.plan_id ?? null
    if (plans.value.length === 0) {
      plans.value = (await adminApi.listPlans('', 0, 100)).data.data
    }
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/workspaces')
  } finally {
    loading.value = false
  }
}

async function assignPlan() {
  if (!ws.value) return
  busy.value = true
  try {
    await adminApi.assignWorkspacePlan(ws.value.id, selectedPlanId.value)
    ws.value.plan_id = selectedPlanId.value
    notify.success('Plan updated')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

// --- Per-workspace overrides ---
type NumKey =
  | 'max_apps' | 'max_database_instances' | 'max_databases_per_instance' | 'max_cron_jobs'
  | 'max_volumes' | 'max_networks' | 'max_api_keys' | 'max_members' | 'max_runners' | 'max_cpu_cores' | 'max_memory_mb'
  | 'max_database_instance_size_mb' | 'max_storage_mb' | 'max_gpus'
const overrideFields: { key: NumKey; label: string }[] = [
  { key: 'max_apps', label: 'Apps' },
  { key: 'max_database_instances', label: 'DB instances' },
  { key: 'max_databases_per_instance', label: 'DBs / instance' },
  { key: 'max_cron_jobs', label: 'Cron jobs' },
  { key: 'max_volumes', label: 'Volumes' },
  { key: 'max_networks', label: 'Networks' },
  { key: 'max_api_keys', label: 'API keys' },
  { key: 'max_members', label: 'Members' },
  { key: 'max_runners', label: 'Runners' },
  { key: 'max_cpu_cores', label: 'CPU cores' },
  { key: 'max_memory_mb', label: 'Memory (MB)' },
  { key: 'max_database_instance_size_mb', label: 'DB size (MB)' },
  { key: 'max_storage_mb', label: 'Storage (MB)' },
  { key: 'max_gpus', label: 'GPUs' },
]
const override = ref<WorkspaceQuotaOverride | null>(null)
async function loadOverride() {
  if (!wsId.value) return
  try {
    override.value = (await adminApi.getWorkspaceQuota(wsId.value)).data.data
  } catch { /* none set */ }
}
function setNum(key: NumKey, raw: string) {
  if (!override.value) return
  override.value[key] = raw.trim() === '' ? null : Number(raw)
}
function setCap(key: 'allow_custom_tls' | 'allow_privileged_host_mounts' | 'allow_shell_exec' | 'allow_shared_storage' | 'allow_dns_providers' | 'allow_custom_labels' | 'allow_platform_runners' | 'allow_gpu', raw: string) {
  if (!override.value) return
  override.value[key] = raw === '' ? null : raw === 'true'
}
function setSecProfile(raw: string) {
  if (!override.value) return
  override.value.security_profile = raw === '' ? null : (raw as 'default' | 'restricted')
}
async function saveOverride() {
  if (!ws.value || !override.value) return
  busy.value = true
  try {
    await adminApi.setWorkspaceQuota(ws.value.id, override.value)
    notify.success('Overrides saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}
async function clearOverride() {
  if (!ws.value) return
  busy.value = true
  try {
    await adminApi.clearWorkspaceQuota(ws.value.id)
    await loadOverride()
    notify.success('Overrides cleared')
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

watch(wsId, () => { load(); loadOverride(); licenseStore.load() }, { immediate: true })

function back() {
  router.push('/admin/workspaces')
}

async function togglePrivileged() {
  if (!ws.value) return
  const next = !ws.value.privileged
  busy.value = true
  try {
    await adminApi.setWorkspacePrivileged(ws.value.id, next)
    ws.value.privileged = next
    notify.success(`Privileged ${next ? 'enabled' : 'disabled'}`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

const rotating = ref(false)
const showRotateConfirm = ref(false)
async function rotateKey() {
  if (!ws.value) return
  showRotateConfirm.value = false
  rotating.value = true
  try {
    const res = (await adminApi.rotateWorkspaceKey(ws.value.id)).data.data
    notify.success(`Key rotated (v${res.version}) — re-encrypted ${res.reencrypted} secret${res.reencrypted === 1 ? '' : 's'}`)
  } catch (e) {
    notify.apiError(e)
  } finally {
    rotating.value = false
  }
}

function fmtDate(s?: string): string {
  if (!s) return '—'
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

function relTime(ts: string): string {
  const then = new Date(ts).getTime()
  if (Number.isNaN(then)) return '—'
  const diff = Math.floor((Date.now() - then) / 1000)
  if (diff < 60) return `${Math.max(0, diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return new Date(ts).toLocaleDateString()
}

function roleBadge(role: string): string {
  if (role === 'owner') return 'badge-info'
  if (role === 'admin') return 'badge-success'
  return 'badge-neutral'
}

function memberName(m: AdminWorkspaceMember): string {
  return m.user?.name || m.user?.email || `User #${m.user_id}`
}

function prettyAction(action: string): string {
  const s = action.replace(/[._]/g, ' ').trim()
  return s.charAt(0).toUpperCase() + s.slice(1)
}

function eventIcon(action: string): string {
  if (action.includes('delete') || action.includes('remove')) return 'mdi-delete-outline'
  if (action.includes('create') || action.includes('invite')) return 'mdi-plus-circle-outline'
  if (action.includes('deploy')) return 'mdi-rocket-launch-outline'
  if (action.includes('login')) return 'mdi-login-variant'
  if (action.includes('update') || action.includes('privileged')) return 'mdi-pencil-outline'
  return 'mdi-circle-small'
}

function eventSeverity(e: AdminEvent): string {
  const a = e.action.toLowerCase()
  if (a.includes('delete')) return 'sev-error'
  if (a.includes('fail') || a.includes('remove') || a.includes('revoke')) return 'sev-warning'
  return ''
}
</script>

<template>
  <div>
    <div v-if="loading && !ws" class="card">
      <div class="card-body"><span class="spinner"></span></div>
    </div>

    <template v-else-if="ws">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" title="Back to workspaces" aria-label="Back to workspaces" @click="back">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <span class="avatar avatar-lg">{{ (ws.display_name || ws.name).charAt(0).toUpperCase() }}</span>
          <div class="header-title">
            <h1>
              {{ ws.display_name || ws.name }}
              <span v-if="ws.system" class="badge badge-info">system</span>
              <span v-if="ws.privileged" class="badge badge-success">privileged</span>
            </h1>
            <span class="subline">{{ ws.name }}</span>
          </div>
        </div>

        <div class="header-actions">
          <button
            v-if="!ws.system"
            class="btn btn-secondary btn-sm"
            :disabled="busy"
            @click="togglePrivileged"
          >
            <span class="mdi" :class="ws.privileged ? 'mdi-shield-off-outline' : 'mdi-shield-check-outline'"></span>
            {{ ws.privileged ? 'Revoke privileged' : 'Make privileged' }}
          </button>
          <button
            class="btn btn-secondary btn-sm"
            :disabled="rotating"
            title="Rotate the workspace's encryption key and re-encrypt its secrets"
            @click="showRotateConfirm = true"
          >
            <span class="mdi" :class="rotating ? 'mdi-loading mdi-spin' : 'mdi-key-change'"></span>
            {{ rotating ? 'Rotating…' : 'Rotate encryption key' }}
          </button>
        </div>
      </div>

      <!-- Stat cards -->
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Applications</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-cube-outline"></span></span>
          </div>
          <div class="stat-value">{{ ws.apps_count }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Databases</span>
            <span class="stat-icon stat-icon-info"><span class="mdi mdi-database"></span></span>
          </div>
          <div class="stat-value">{{ ws.databases_count }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Stacks</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-layers"></span></span>
          </div>
          <div class="stat-value">{{ ws.stacks_count }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Volumes</span>
            <span class="stat-icon stat-icon-secondary"><span class="mdi mdi-harddisk"></span></span>
          </div>
          <div class="stat-value">{{ ws.volumes_count }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Networks</span>
            <span class="stat-icon stat-icon-info"><span class="mdi mdi-lan"></span></span>
          </div>
          <div class="stat-value">{{ ws.networks_count }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Members</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-account-group"></span></span>
          </div>
          <div class="stat-value">{{ ws.members_count }}</div>
        </div>
      </div>

      <!-- Details -->
      <div class="card mt-4">
        <div class="card-header"><h2>Details</h2></div>
        <div class="card-body details">
          <div class="detail">
            <span class="text-muted">Owner</span>
            <span class="cell-text">
              <span class="cell-title">{{ ws.owner_name || '—' }}</span>
              <span class="cell-sub">{{ ws.owner_email }}</span>
            </span>
          </div>
          <div class="detail">
            <span class="text-muted">Name</span>
            <span><code>{{ ws.name }}</code></span>
          </div>
          <div class="detail">
            <span class="text-muted">Privileged</span>
            <span class="badge" :class="ws.privileged ? 'badge-success' : 'badge-neutral'">
              {{ ws.privileged ? 'Yes' : 'No' }}
            </span>
          </div>
          <div class="detail">
            <span class="text-muted">Created</span>
            <span>{{ fmtDate(ws.created_at) }}</span>
          </div>
          <div v-if="ws.description" class="detail detail-wide">
            <span class="text-muted">Description</span>
            <span>{{ ws.description }}</span>
          </div>
        </div>
      </div>

      <!-- Plan -->
      <div class="card mt-4">
        <div class="card-header"><h2>Plan</h2></div>
        <div class="card-body" style="display: flex; align-items: flex-end; gap: 12px; flex-wrap: wrap">
          <div class="form-group" style="margin-bottom: 0; flex: 1; min-width: 220px">
            <label class="form-label">Assigned plan</label>
            <select v-model="selectedPlanId" class="form-select">
              <option :value="null">Default plan</option>
              <option v-for="p in plans" :key="p.id" :value="p.id">{{ p.name }}{{ p.is_default ? ' (default)' : '' }}</option>
            </select>
            <p class="form-hint">Caps this workspace's resources. Enforced only when plan enforcement is enabled platform-wide.</p>
          </div>
          <button class="btn btn-primary" :disabled="busy || selectedPlanId === (ws.plan_id ?? null)" @click="assignPlan">Save</button>
        </div>
      </div>

      <!-- Quota overrides -->
      <div v-if="override" class="card mt-4">
        <div class="card-header">
          <h2>Quota overrides</h2>
          <span v-if="!quotaOverride.has.value" class="badge badge-neutral" title="Per-workspace overrides require an Enterprise license">
            <span class="mdi mdi-lock-outline"></span> Enterprise
          </span>
        </div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-bottom: 16px">
            Override individual limits for this workspace. Leave a field empty to inherit the plan; use
            <code>-1</code> for unlimited, <code>0</code> for none.
          </p>
          <p v-if="!quotaOverride.has.value" class="text-muted text-sm override-locked">
            <span class="mdi mdi-information-outline"></span>
            Per-workspace overrides are an Enterprise feature. Existing overrides stay enforced and can be cleared;
            saving new ones requires a license. <router-link to="/admin/license">Manage license</router-link>.
          </p>
          <div class="override-grid">
            <div v-for="f in overrideFields" :key="f.key" class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">{{ f.label }}</label>
              <input
                type="number"
                class="form-input"
                placeholder="inherit"
                :value="override[f.key] ?? ''"
                @input="setNum(f.key, ($event.target as HTMLInputElement).value)"
              />
            </div>
          </div>
          <div class="override-grid" style="margin-top: 14px">
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Custom TLS</label>
              <select class="form-select" :value="override.allow_custom_tls === null ? '' : String(override.allow_custom_tls)" @change="setCap('allow_custom_tls', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Privileged host mounts</label>
              <select class="form-select" :value="override.allow_privileged_host_mounts === null ? '' : String(override.allow_privileged_host_mounts)" @change="setCap('allow_privileged_host_mounts', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Shell access</label>
              <select class="form-select" :value="override.allow_shell_exec === null ? '' : String(override.allow_shell_exec)" @change="setCap('allow_shell_exec', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Shared storage (NFS / CIFS-SMB)</label>
              <select class="form-select" :value="override.allow_shared_storage === null ? '' : String(override.allow_shared_storage)" @change="setCap('allow_shared_storage', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">DNS providers</label>
              <select class="form-select" :value="override.allow_dns_providers === null ? '' : String(override.allow_dns_providers)" @change="setCap('allow_dns_providers', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Custom labels</label>
              <select class="form-select" :value="override.allow_custom_labels === null ? '' : String(override.allow_custom_labels)" @change="setCap('allow_custom_labels', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Platform runners</label>
              <select class="form-select" :value="override.allow_platform_runners === null ? '' : String(override.allow_platform_runners)" @change="setCap('allow_platform_runners', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">GPU access</label>
              <select class="form-select" :value="override.allow_gpu === null ? '' : String(override.allow_gpu)" @change="setCap('allow_gpu', ($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="true">Allow</option><option value="false">Deny</option>
              </select>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label form-label-sm">Security profile</label>
              <select class="form-select" :value="override.security_profile ?? ''" @change="setSecProfile(($event.target as HTMLSelectElement).value)">
                <option value="">Inherit</option><option value="default">Default</option><option value="restricted" :disabled="!securityProfile.mutable.value">Restricted (non-root){{ securityProfile.has.value ? '' : ' — Enterprise' }}</option>
              </select>
            </div>
          </div>
          <div style="display: flex; gap: 10px; margin-top: 20px">
            <button
              class="btn btn-primary"
              :disabled="busy || !quotaOverride.has.value"
              :title="quotaOverride.has.value ? '' : 'Per-workspace overrides require an Enterprise license'"
              @click="saveOverride"
            >
              Save overrides
            </button>
            <button class="btn btn-secondary" :disabled="busy" @click="clearOverride">Clear all</button>
          </div>
        </div>
      </div>

      <!-- Members -->
      <div class="card mt-4">
        <div class="card-header"><h2>Members</h2></div>
        <div v-if="!ws.members?.length" class="empty-state">
          <span class="mdi mdi-account-group-outline" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>No members</h3>
          <p>This workspace has no members.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr><th>Member</th><th>Role</th><th class="text-right">Joined</th></tr>
            </thead>
            <tbody>
              <tr v-for="m in ws.members" :key="m.id" class="row-clickable" @click="router.push(`/admin/users/${m.user_id}`)">
                <td>
                  <div class="cell-id">
                    <span class="avatar avatar-sm">{{ memberName(m).charAt(0).toUpperCase() }}</span>
                    <span class="cell-text">
                      <span class="cell-title">{{ memberName(m) }}</span>
                      <span class="cell-sub">{{ m.user?.email }}</span>
                    </span>
                  </div>
                </td>
                <td><span class="badge" :class="roleBadge(m.role)">{{ m.role }}</span></td>
                <td class="text-right cell-sub">{{ fmtDate(m.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Recent activity -->
      <div class="card mt-4">
        <div class="card-header"><h2>Recent activity</h2></div>
        <div v-if="!ws.recent_events?.length" class="empty-state">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>No activity</h3>
          <p>No recent activity for this workspace.</p>
        </div>
        <ul v-else class="timeline">
          <li v-for="e in ws.recent_events" :key="e.id" class="event">
            <span class="event-icon" :class="eventSeverity(e)">
              <span class="mdi" :class="eventIcon(e.action)"></span>
            </span>
            <div class="event-body">
              <div class="event-row">
                <span class="event-msg">{{ prettyAction(e.action) }}</span>
                <span class="event-time">{{ relTime(e.created_at) }}</span>
              </div>
              <span class="event-type">{{ e.target_type }}{{ e.target_id ? ' · ' + e.target_id : '' }}</span>
            </div>
          </li>
        </ul>
      </div>
    </template>

    <ConfirmDialog
      :open="showRotateConfirm"
      title="Rotate encryption key"
      :message="`Rotate the encryption key for &quot;${ws?.name}&quot;? Its secrets will be re-encrypted under a new key.`"
      confirm-label="Rotate key"
      variant="danger"
      :busy="rotating"
      @confirm="rotateKey"
      @cancel="showRotateConfirm = false"
    />
  </div>
</template>

<style scoped>
.card-header { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.override-locked { display: flex; align-items: center; gap: 6px; margin: -6px 0 16px; }
.override-locked .mdi { font-size: 15px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.avatar-lg { width: 44px; height: 44px; font-size: 18px; border-radius: var(--radius); }
.header-title h1 { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.subline { display: block; margin-top: 2px; font-size: 13px; color: var(--text-muted); }
.header-actions { display: flex; gap: 8px; }

.details {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 18px 24px;
}
.detail { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.detail-wide { grid-column: 1 / -1; }
.detail > .text-muted { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em; }
.detail code { font-family: 'JetBrains Mono', monospace; font-size: 12px; background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; }

.timeline { list-style: none; margin: 0; padding: 8px 0; }
.event { display: flex; gap: 12px; padding: 10px 20px; }
.event + .event { border-top: 1px solid var(--border-secondary); }
.event-icon {
  flex-shrink: 0; width: 30px; height: 30px; border-radius: 50%;
  display: inline-flex; align-items: center; justify-content: center; font-size: 16px;
  background: var(--bg-tertiary); color: var(--text-secondary);
}
.event-icon.sev-warning { background: var(--warning-50); color: var(--warning-600); }
.event-icon.sev-error { background: var(--danger-50); color: var(--danger-600); }
.event-body { flex: 1; min-width: 0; }
.event-row { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.event-msg { font-size: 14px; color: var(--text-primary); }
.event-time { flex-shrink: 0; font-size: 12px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.event-type { font-size: 11px; color: var(--text-muted); font-family: 'JetBrains Mono', monospace; }
</style>
