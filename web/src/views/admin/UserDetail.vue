<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useAuthStore } from '@/stores/auth'
import { useLicenseStore } from '@/stores/license'
import type { AdminUserDetail, AdminEvent } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { copyText } from '@/utils/clipboard'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const auth = useAuthStore()
const license = useLicenseStore()

const userId = computed(() => Number(route.params.id))

const user = ref<AdminUserDetail | null>(null)
const loading = ref(false)
const busy = ref(false)

// Generic confirmation: the account actions each open a styled ConfirmDialog
// instead of a native confirm(); the pending action's work runs on confirm.
interface ConfirmAction {
  title: string
  message: string
  confirmLabel: string
  variant: 'danger' | 'primary'
  run: () => Promise<void>
}
const confirmAction = ref<ConfirmAction | null>(null)
function runConfirmAction() {
  const a = confirmAction.value
  confirmAction.value = null
  void a?.run()
}

const isSelf = computed(() => !!user.value && auth.user?.id === user.value.id)
const canManage = computed(() => auth.isAdmin && !!user.value && !isSelf.value)

// --- Enterprise per-user workspace limits (owned + joined) ------------------
// Editable override strings: '' = inherit (null); a number overrides; -1 = unlimited.
const ownedLimit = ref('')
const memberLimit = ref('')
const eeOwned = computed(() => license.has('user_workspace_limit'))
const eeMember = computed(() => license.has('user_workspace_membership_limit'))

function parseLimit(s: string): number | null {
  const t = s.trim()
  if (t === '') return null
  const n = Number(t)
  return Number.isInteger(n) ? n : null
}
const limitsDirty = computed(
  () =>
    parseLimit(ownedLimit.value) !== (user.value?.workspace_limit ?? null) ||
    parseLimit(memberLimit.value) !== (user.value?.workspace_membership_limit ?? null)
)

// Sync the editable fields whenever the loaded user changes.
watch(user, (u) => {
  ownedLimit.value = u?.workspace_limit == null ? '' : String(u.workspace_limit)
  memberLimit.value = u?.workspace_membership_limit == null ? '' : String(u.workspace_membership_limit)
})

async function saveLimits() {
  if (!user.value) return
  const id = user.value.id
  const o = parseLimit(ownedLimit.value)
  const m = parseLimit(memberLimit.value)
  busy.value = true
  try {
    if (eeOwned.value && o !== (user.value.workspace_limit ?? null)) {
      await adminApi.setWorkspaceLimit(id, o)
    }
    if (eeMember.value && m !== (user.value.workspace_membership_limit ?? null)) {
      await adminApi.setWorkspaceMembershipLimit(id, m)
    }
    notify.success('Workspace limits updated')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function load() {
  loading.value = true
  try {
    const res = await adminApi.getUser(userId.value)
    user.value = res.data.data
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/users')
  } finally {
    loading.value = false
  }
}

watch(userId, load, { immediate: true })
// Entitlement flags gate the limit inputs; load them once (no-op in CE).
license.load().catch(() => {})

function back() {
  router.push('/admin/users')
}

function toggleRole() {
  if (!user.value) return
  const id = user.value.id
  const next = user.value.role === 'admin' ? 'user' : 'admin'
  const verb = next === 'admin' ? 'Promote' : 'Demote'
  confirmAction.value = {
    title: `${verb} user?`,
    message: `${verb} ${user.value.name} to ${next}?`,
    confirmLabel: verb,
    variant: 'primary',
    run: async () => {
      busy.value = true
      try {
        await adminApi.updateUser(id, { role: next })
        notify.success(`Role updated to ${next}`)
        await load()
      } catch (e) {
        notify.apiError(e)
      } finally {
        busy.value = false
      }
    },
  }
}

function revoke() {
  if (!user.value) return
  const id = user.value.id
  confirmAction.value = {
    title: 'Revoke sessions?',
    message: `Revoke all active sessions for ${user.value.name}?`,
    confirmLabel: 'Revoke',
    variant: 'danger',
    run: async () => {
      busy.value = true
      try {
        await adminApi.revokeSessions(id)
        notify.success('Sessions revoked')
      } catch (e) {
        notify.apiError(e)
      } finally {
        busy.value = false
      }
    },
  }
}

function disableTwoFactor() {
  if (!user.value) return
  const id = user.value.id
  confirmAction.value = {
    title: 'Disable two-factor?',
    message: `Disable two-factor authentication for ${user.value.name}? Use this only for account recovery.`,
    confirmLabel: 'Disable',
    variant: 'danger',
    run: async () => {
      busy.value = true
      try {
        await adminApi.disableTwoFactor(id)
        notify.success('Two-factor authentication disabled')
        load()
      } catch (e) {
        notify.apiError(e)
      } finally {
        busy.value = false
      }
    },
  }
}

// Admin password reset: the platform generates a new password, shown once here so
// the admin can hand it to the user. Irreversible — the old password is discarded
// and every session is signed out.
const generatedPassword = ref<string | null>(null)
const pwCopied = ref(false)
function resetPassword() {
  if (!user.value) return
  const id = user.value.id
  const name = user.value.name
  confirmAction.value = {
    title: 'Reset password?',
    message: `Generate a new password for ${name}? This is irreversible: their current password stops working immediately, every active session is signed out, and the new password is shown only once.`,
    confirmLabel: 'Reset password',
    variant: 'danger',
    run: async () => {
      busy.value = true
      try {
        const res = await adminApi.resetUserPassword(id)
        pwCopied.value = false
        generatedPassword.value = res.data.data?.password ?? null
        notify.success('Password reset')
      } catch (e) {
        notify.apiError(e)
      } finally {
        busy.value = false
      }
    },
  }
}
async function copyGeneratedPassword() {
  if (generatedPassword.value && (await copyText(generatedPassword.value))) {
    pwCopied.value = true
    notify.success('Password copied')
  }
}

async function verifyEmail() {
  if (!user.value) return
  busy.value = true
  try {
    await adminApi.verifyEmail(user.value.id)
    notify.success('Email marked as verified')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

// Disable/enable the account. Disabling signs the user out, blocks login, and
// stops all of their applications and databases (handled by the backend).
async function setActive(active: boolean) {
  if (!user.value) return
  const u = user.value
  if (!active) {
    confirmAction.value = {
      title: 'Disable account?',
      message: `Disable ${u.name}'s account? They will be signed out and unable to log in, and all their applications and databases will be stopped.`,
      confirmLabel: 'Disable',
      variant: 'danger',
      run: () => applyActive(u.id, false),
    }
    return
  }
  await applyActive(u.id, true)
}

async function applyActive(id: number, active: boolean) {
  busy.value = true
  try {
    await adminApi.updateUser(id, { active })
    notify.success(active ? 'Account enabled' : 'Account disabled — workloads stopped')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

// --- Scheduled deletion (grace period) + ownership transfer ----------------

const pendingDeletion = computed(() => !!user.value?.scheduled_deletion_at)
const deletionCountdown = computed(() => {
  const at = user.value?.scheduled_deletion_at
  if (!at) return ''
  const days = Math.ceil((new Date(at).getTime() - Date.now()) / 86_400_000)
  return days <= 0 ? 'imminently' : `in ${days} day${days === 1 ? '' : 's'}`
})

const showDeleteDialog = ref(false)
const confirmText = ref('')
// workspaceId -> chosen new owner user id (0 = delete this workspace's data).
const transferChoice = ref<Record<number, number>>({})

function openDeleteDialog() {
  if (!user.value || user.value.active) {
    notify.error('Disable the account before deleting it')
    return
  }
  confirmText.value = ''
  // Default every workspace to deletion; the admin opts into transfers explicitly.
  transferChoice.value = Object.fromEntries((user.value.owned_workspaces ?? []).map((w) => [w.id, 0]))
  showDeleteDialog.value = true
}

async function scheduleDeletion() {
  if (!user.value) return
  if (confirmText.value !== 'DELETE') {
    notify.error('Type DELETE to confirm')
    return
  }
  const transfers = Object.entries(transferChoice.value)
    .filter(([, owner]) => Number(owner) > 0)
    .map(([ws, owner]) => ({ workspace_id: Number(ws), new_owner_id: Number(owner) }))
  busy.value = true
  try {
    await adminApi.scheduleDeletion(user.value.id, transfers)
    notify.success('Account scheduled for deletion')
    showDeleteDialog.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

async function cancelDeletion() {
  if (!user.value) return
  busy.value = true
  try {
    await adminApi.cancelDeletion(user.value.id)
    notify.success('Deletion cancelled — account remains disabled')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    busy.value = false
  }
}

// Force deletion: permanently delete a pending-deletion account now, skipping the
// remaining grace period. Irreversible, so it is double-confirmed.
function forceDeletion() {
  if (!user.value) return
  const id = user.value.id
  confirmAction.value = {
    title: 'Permanently delete account?',
    message:
      `Permanently delete ${user.value.name} and all of their remaining data right now? ` +
      'This skips the grace period and cannot be undone.',
    confirmLabel: 'Delete now',
    variant: 'danger',
    run: async () => {
      busy.value = true
      try {
        await adminApi.forceDeletion(id)
        notify.success('Account permanently deleted')
        router.push('/admin/users')
      } catch (e) {
        notify.apiError(e)
      } finally {
        busy.value = false
      }
    },
  }
}

function fmtDate(s?: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleString()
}

function relTime(ts: string): string {
  const then = new Date(ts).getTime()
  if (Number.isNaN(then)) return '—'
  const diff = Math.floor((Date.now() - then) / 1000)
  if (diff < 60) return `${Math.max(0, diff)}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return new Date(ts).toLocaleString()
}

function eventIcon(action: string): string {
  if (action.includes('delete')) return 'mdi-delete'
  if (action.includes('create')) return 'mdi-plus'
  if (action.includes('login')) return 'mdi-login'
  return 'mdi-circle-small'
}

function eventSeverity(e: AdminEvent): string {
  const a = e.action.toLowerCase()
  if (a.includes('delete')) return 'sev-error'
  if (a.includes('fail') || a.includes('revoke')) return 'sev-warning'
  return ''
}
</script>

<template>
  <div>
    <div v-if="loading && !user" class="card">
      <div class="card-body"><span class="spinner"></span></div>
    </div>

    <template v-else-if="user">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" title="Back to users" aria-label="Back to users" @click="back">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ user.name }}
              <span class="badge" :class="user.role === 'admin' ? 'badge-info' : 'badge-neutral'">
                {{ user.role }}
              </span>
              <span v-if="pendingDeletion" class="badge badge-danger">Pending deletion</span>
              <span v-else-if="user.active" class="badge badge-dot badge-success">Active</span>
              <span v-else class="badge badge-neutral">Inactive</span>
            </h1>
            <span class="subline">{{ user.email }}</span>
          </div>
        </div>

        <div v-if="canManage" class="header-actions">
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="toggleRole">
            <span class="mdi" :class="user.role === 'admin' ? 'mdi-arrow-down-bold' : 'mdi-shield-account'"></span>
            {{ user.role === 'admin' ? 'Demote to user' : 'Promote to admin' }}
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="revoke">
            <span class="mdi mdi-logout-variant"></span> Revoke sessions
          </button>
          <button v-if="user.two_factor_enabled" class="btn btn-secondary btn-sm" :disabled="busy" @click="disableTwoFactor">
            <span class="mdi mdi-shield-off-outline"></span> Disable 2FA
          </button>
          <button class="btn btn-secondary btn-sm" :disabled="busy" @click="resetPassword" title="Generate a new password for this user (irreversible)">
            <span class="mdi mdi-lock-reset"></span> Reset password
          </button>
          <!-- Pending deletion: cancel it, or force it through now (skip the grace period). -->
          <button v-if="pendingDeletion" class="btn btn-primary btn-sm" :disabled="busy" @click="cancelDeletion">
            <span class="mdi mdi-undo"></span> Cancel deletion
          </button>
          <button v-if="pendingDeletion" class="btn btn-danger btn-sm" :disabled="busy" @click="forceDeletion" title="Delete the account and all its data now, skipping the remaining grace period">
            <span class="mdi mdi-delete-alert"></span> Delete now
          </button>
          <template v-else>
            <button v-if="!user.email_verified_at" class="btn btn-secondary btn-sm" :disabled="busy" @click="verifyEmail">
              <span class="mdi mdi-email-check-outline"></span> Verify email
            </button>
            <!-- Disable stops the user's workloads; only a disabled account can be deleted. -->
            <button v-if="user.active" class="btn btn-warning btn-sm" :disabled="busy" @click="setActive(false)" title="Sign the user out and stop all their apps & databases">
              <span class="mdi mdi-account-cancel-outline"></span> Disable account
            </button>
            <button v-else class="btn btn-secondary btn-sm" :disabled="busy" @click="setActive(true)">
              <span class="mdi mdi-account-check-outline"></span> Enable account
            </button>
            <button v-if="!user.active" class="btn btn-danger btn-sm" :disabled="busy" @click="openDeleteDialog" title="Schedule permanent deletion of the user and all their data">
              <span class="mdi mdi-delete-clock"></span> Delete account
            </button>
          </template>
        </div>
      </div>

      <!-- Pending-deletion banner -->
      <div v-if="pendingDeletion" class="del-banner">
        <span class="mdi mdi-clock-alert-outline"></span>
        <div class="del-banner-text">
          This account is scheduled for permanent deletion <strong>{{ deletionCountdown }}</strong>
          (on {{ fmtDate(user.scheduled_deletion_at) }}). All remaining data it owns will be erased.
        </div>
        <div class="del-banner-actions">
          <button class="btn btn-sm btn-secondary" :disabled="busy" @click="cancelDeletion">Cancel</button>
          <button class="btn btn-sm btn-danger" :disabled="busy" @click="forceDeletion" title="Delete now, skipping the remaining grace period">Delete now</button>
        </div>
      </div>

      <!-- Stat cards -->
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Apps</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-cube-outline"></span></span>
          </div>
          <div class="stat-value">{{ user.apps_total }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Running</span>
            <span class="stat-icon stat-icon-success"><span class="mdi mdi-play"></span></span>
          </div>
          <div class="stat-value">{{ user.apps_running }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Failed</span>
            <span class="stat-icon" :class="user.apps_failed > 0 ? 'stat-icon-danger' : 'stat-icon-warning'">
              <span class="mdi mdi-alert-circle-outline"></span>
            </span>
          </div>
          <div class="stat-value">{{ user.apps_failed }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Databases</span>
            <span class="stat-icon stat-icon-info"><span class="mdi mdi-database"></span></span>
          </div>
          <div class="stat-value">{{ user.databases }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Stacks</span>
            <span class="stat-icon stat-icon-secondary"><span class="mdi mdi-layers"></span></span>
          </div>
          <div class="stat-value">{{ user.stacks }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <span class="stat-label">Workspaces</span>
            <span class="stat-icon stat-icon-primary"><span class="mdi mdi-briefcase"></span></span>
          </div>
          <div class="stat-value">{{ user.workspaces_owned }}</div>
          <div class="stat-sub">{{ user.workspaces_member }} member</div>
        </div>
      </div>

      <!-- Account details -->
      <div class="card mt-4">
        <div class="card-header"><h2>Account</h2></div>
        <div class="card-body details">
          <div class="detail">
            <span class="text-muted">Role</span>
            <span class="badge" :class="user.role === 'admin' ? 'badge-info' : 'badge-neutral'">{{ user.role }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">Status</span>
            <span v-if="user.active" class="badge badge-dot badge-success">Active</span>
            <span v-else class="badge badge-neutral">Inactive</span>
          </div>
          <div class="detail">
            <span class="text-muted">Email verified</span>
            <span class="badge" :class="user.email_verified_at ? 'badge-success' : 'badge-warning'">
              {{ user.email_verified_at ? fmtDate(user.email_verified_at) : 'Not verified' }}
            </span>
          </div>
          <div class="detail">
            <span class="text-muted">Last login</span>
            <span>{{ fmtDate(user.last_login_at) }}</span>
          </div>
          <div class="detail">
            <span class="text-muted">Created</span>
            <span>{{ fmtDate(user.created_at) }}</span>
          </div>
        </div>
      </div>

      <!-- Workspace limits (Enterprise per-user overrides) -->
      <div v-if="canManage" class="card mt-4">
        <div class="card-header">
          <h2>Workspace limits <span class="badge badge-info">Enterprise</span></h2>
        </div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin: 0 0 14px">
            Per-user overrides of the platform defaults. Leave blank to inherit the platform
            setting; <code>-1</code> = unlimited, <code>0</code> = none.
          </p>
          <div class="limit-row">
            <div class="limit-label">
              <label class="form-label">Owned workspaces</label>
              <div class="form-hint text-muted">Owns {{ user.workspaces_owned }} today</div>
            </div>
            <input
              v-model="ownedLimit"
              type="number"
              class="form-input limit-input"
              :disabled="!eeOwned || busy"
              :placeholder="eeOwned ? 'Inherit' : 'Enterprise only'"
            />
          </div>
          <div class="limit-row">
            <div class="limit-label">
              <label class="form-label">Joined as member</label>
              <div class="form-hint text-muted">Member of {{ user.workspaces_member }} today</div>
            </div>
            <input
              v-model="memberLimit"
              type="number"
              class="form-input limit-input"
              :disabled="!eeMember || busy"
              :placeholder="eeMember ? 'Inherit' : 'Enterprise only'"
            />
          </div>
          <div class="limit-actions">
            <span v-if="!eeOwned && !eeMember" class="text-muted text-sm">
              Requires an Enterprise license.
            </span>
            <button class="btn btn-primary btn-sm" :disabled="busy || !limitsDirty || (!eeOwned && !eeMember)" @click="saveLimits">
              <span class="mdi mdi-content-save"></span> Save limits
            </button>
          </div>
        </div>
      </div>

      <!-- Owned workspaces -->
      <div class="card mt-4">
        <div class="card-header"><h2>Owned workspaces</h2></div>
        <div v-if="!user.owned_workspaces?.length" class="empty-state">
          <span class="mdi mdi-briefcase-outline" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>No workspaces</h3>
          <p>This user owns no workspaces.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Apps</th>
                <th>Databases</th>
                <th>Stacks</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="w in user.owned_workspaces" :key="w.id">
                <td>
                  {{ w.name }}
                  <span v-if="w.privileged" class="badge badge-info">privileged</span>
                </td>
                <td>{{ w.apps }}</td>
                <td>{{ w.databases }}</td>
                <td>{{ w.stacks }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Recent activity -->
      <div class="card mt-4">
        <div class="card-header"><h2>Recent activity</h2></div>
        <div v-if="!user.recent_events?.length" class="empty-state">
          <span class="mdi mdi-timeline-text-outline" style="font-size: 40px; color: var(--text-muted)"></span>
          <h3>No activity</h3>
          <p>No recent activity.</p>
        </div>
        <ul v-else class="timeline">
          <li v-for="e in user.recent_events" :key="e.id" class="event">
            <span class="event-icon" :class="eventSeverity(e)">
              <span class="mdi" :class="eventIcon(e.action)"></span>
            </span>
            <div class="event-body">
              <div class="event-row">
                <span class="event-msg">{{ e.action }}</span>
                <span class="event-time">{{ relTime(e.created_at) }}</span>
              </div>
              <span class="event-type">{{ e.target_type }}{{ e.target_id ? ' · ' + e.target_id : '' }}</span>
            </div>
          </li>
        </ul>
      </div>

      <!-- Schedule-deletion dialog: per-workspace ownership transfer or delete -->
      <div v-if="showDeleteDialog" class="modal-overlay" @click.self="showDeleteDialog = false">
        <div class="modal">
          <div class="modal-header">
            <h2>Delete {{ user.name }}</h2>
            <button class="btn-icon" aria-label="Close" @click="showDeleteDialog = false"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p class="text-muted" style="margin-top: 0">
              The account stays disabled and is permanently deleted after a grace period, along with all
              data it still owns. For each workspace that has other members you can transfer ownership
              instead of deleting it.
            </p>
            <div v-if="user.owned_workspaces?.length" class="transfer-list">
              <div v-for="w in user.owned_workspaces" :key="w.id" class="transfer-row">
                <div class="transfer-ws">
                  <span class="transfer-ws-name">{{ w.name }}</span>
                  <span class="transfer-ws-meta">{{ w.apps }} apps · {{ w.databases }} dbs · {{ w.stacks }} stacks</span>
                </div>
                <select v-if="w.members.length" v-model.number="transferChoice[w.id]" class="form-input transfer-select">
                  <option :value="0">Delete this workspace's data</option>
                  <option v-for="m in w.members" :key="m.user_id" :value="m.user_id">Transfer to {{ m.name }} ({{ m.email }})</option>
                </select>
                <span v-else class="text-muted text-sm">No other members — will be deleted</span>
              </div>
            </div>
            <p v-else class="text-muted">This user owns no workspaces.</p>
            <div class="form-group" style="margin: 14px 0 0">
              <label>Type <code>DELETE</code> to confirm</label>
              <input v-model="confirmText" class="form-input" placeholder="DELETE" autocomplete="off" />
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-secondary" :disabled="busy" @click="showDeleteDialog = false">Cancel</button>
            <button class="btn btn-danger" :disabled="busy || confirmText !== 'DELETE'" @click="scheduleDeletion">
              <span class="mdi mdi-delete-clock"></span> Schedule deletion
            </button>
          </div>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="!!confirmAction"
      :title="confirmAction?.title ?? ''"
      :message="confirmAction?.message ?? ''"
      :confirm-label="confirmAction?.confirmLabel ?? 'Confirm'"
      :variant="confirmAction?.variant ?? 'primary'"
      :busy="busy"
      @confirm="runConfirmAction"
      @cancel="confirmAction = null"
    />

    <!-- New password: shown exactly once. The admin must copy it now and hand it
         over out-of-band — it is not stored in clear and can't be shown again. -->
    <div v-if="generatedPassword" class="modal-overlay" @click.self="generatedPassword = null">
      <div class="modal">
        <div class="modal-header">
          <h2>New password</h2>
          <button class="btn-icon" aria-label="Close" @click="generatedPassword = null"><span class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <p class="text-muted" style="margin-top: 0">
            A new password has been generated and every session was signed out. Copy it now and share
            it with the user through a secure channel — <strong>it won't be shown again</strong>.
          </p>
          <div class="pw-reveal">
            <code class="pw-value">{{ generatedPassword }}</code>
            <button class="btn btn-secondary btn-sm" @click="copyGeneratedPassword">
              <span class="mdi" :class="pwCopied ? 'mdi-check' : 'mdi-content-copy'"></span>
              {{ pwCopied ? 'Copied' : 'Copy' }}
            </button>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-primary btn-sm" @click="generatedPassword = null">Done</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pw-reveal {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
  background: var(--surface-2, rgba(127, 127, 127, 0.06));
}
.pw-value {
  flex: 1;
  font-family: var(--font-mono, monospace);
  font-size: 15px;
  letter-spacing: 0.5px;
  user-select: all;
  word-break: break-all;
}
.del-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 12px 0 4px;
  padding: 12px 16px;
  border-radius: 8px;
  background: var(--danger-bg, rgba(239, 68, 68, 0.1));
  border: 1px solid var(--danger-border, rgba(239, 68, 68, 0.3));
}
.del-banner > .mdi { font-size: 22px; color: var(--danger, #ef4444); }
.del-banner-text { flex: 1; font-size: 14px; }
.del-banner-actions { display: flex; gap: 8px; flex-shrink: 0; }
.transfer-list { display: flex; flex-direction: column; gap: 8px; }
.transfer-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border-primary);
  border-radius: 8px;
}
.transfer-ws { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.transfer-ws-name { font-weight: 600; }
.transfer-ws-meta { font-size: 12px; color: var(--text-muted); }
.transfer-select { width: auto; min-width: 240px; max-width: 320px; }
.limit-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-primary);
}
.limit-label { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.limit-label .form-label { margin-bottom: 0; }
.limit-input { width: 160px; flex-shrink: 0; }
.limit-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  padding-top: 14px;
}
.header-left {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}
.header-title h1 {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.subline {
  display: block;
  margin-top: 2px;
  font-size: 13px;
  color: var(--text-muted);
}
.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.details {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 16px;
}
.detail {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 14px;
}
.detail .text-muted {
  font-size: 12px;
}

/* Events timeline */
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
