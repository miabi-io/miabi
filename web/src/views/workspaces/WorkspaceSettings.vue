<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { workspaceApi, type DeletionJob } from '@/api/workspaces'
import { memberApi, usageApi } from '@/api/resources'
import { workspaceBackupApi, type UpdateBackupSettingsInput, type BackupTestResult } from '@/api/workspaceBackup'
import AppModal from '@/components/AppModal.vue'
import type { Member, Invitation, WorkspaceRole, WorkspaceUsage, WorkspaceLiveSample, CustomRole } from '@/api/types'
import { roleApi } from '@/api/rbac'
import NotificationChannels from '@/views/notifications/Notifications.vue'
import WorkspaceRolesPanel from '@/components/WorkspaceRolesPanel.vue'
import PortableBackupPanel from '@/components/PortableBackupPanel.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import Sparkline from '@/components/Sparkline.vue'
import { copyText } from '@/utils/clipboard'

const route = useRoute()
const router = useRouter()
const ws = useWorkspaceStore()
const notify = useNotificationStore()

const roles: WorkspaceRole[] = ['owner', 'admin', 'developer', 'viewer']
type Tab = 'settings' | 'members' | 'roles' | 'usage' | 'backup' | 'portability' | 'notifications'
const tabs: { id: Tab; label: string; icon: string }[] = [
  { id: 'settings', label: 'General', icon: 'mdi-cog-outline' },
  { id: 'members', label: 'Members', icon: 'mdi-account-group-outline' },
  { id: 'roles', label: 'Roles', icon: 'mdi-shield-account-outline' },
  { id: 'usage', label: 'Usage', icon: 'mdi-gauge' },
  { id: 'backup', label: 'Backup', icon: 'mdi-cloud-upload-outline' },
  { id: 'portability', label: 'Portability', icon: 'mdi-package-variant-closed' },
  { id: 'notifications', label: 'Notifications', icon: 'mdi-bell-outline' },
]

// Custom roles (for the member role dropdown); empty/ignored when not entitled.
const customRoleList = ref<CustomRole[]>([])
async function loadCustomRoles() {
  try {
    customRoleList.value = (await roleApi.list(wsId.value)).data.data ?? []
  } catch {
    customRoleList.value = []
  }
}

// Current user's built-in role here (for the role matrix's no-escalation hints).
const myRole = computed<WorkspaceRole>(() => {
  const w = ws.workspaces.find((x) => x.id === wsId.value)
  return (w?.role as WorkspaceRole) ?? 'viewer'
})

// Usage vs plan limits.
const usage = ref<WorkspaceUsage | null>(null)
const usageLoading = ref(false)
type UsageKey = keyof Pick<WorkspaceUsage, 'apps' | 'database_instances' | 'cron_jobs' | 'volumes' | 'networks' | 'api_keys' | 'members' | 'runners' | 'cpu_cores' | 'memory_mb' | 'storage_mb'>
const usageRows: { key: UsageKey; label: string; unit?: string }[] = [
  { key: 'apps', label: 'Applications' },
  { key: 'database_instances', label: 'Database instances' },
  { key: 'cron_jobs', label: 'Cron jobs' },
  { key: 'volumes', label: 'Volumes' },
  { key: 'networks', label: 'Networks' },
  { key: 'api_keys', label: 'API keys' },
  { key: 'members', label: 'Members' },
  { key: 'runners', label: 'Runners' },
  { key: 'cpu_cores', label: 'CPU', unit: 'cores' },
  { key: 'memory_mb', label: 'Memory', unit: 'MB' },
  { key: 'storage_mb', label: 'Storage', unit: 'MB' },
]
async function loadUsage() {
  if (!wsId.value) return
  usageLoading.value = true
  try {
    usage.value = (await usageApi.get(wsId.value)).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    usageLoading.value = false
  }
}
function meterPct(u: { used: number; limit: number }): number {
  if (u.limit < 0 || u.limit === 0) return u.limit === 0 ? 100 : 0
  return Math.min(100, Math.round((u.used / u.limit) * 100))
}
function meterClass(u: { used: number; limit: number }): string {
  if (u.limit < 0) return ''
  const pct = meterPct(u)
  if (u.used >= u.limit) return 'meter-full'
  if (pct >= 80) return 'meter-warn'
  return ''
}

// Live usage: actual CPU/memory/network consumed right now, aggregated across the
// workspace's running containers and streamed over SSE while the tab is open.
const live = ref<WorkspaceLiveSample | null>(null)
const liveConnected = ref(false)
let liveES: EventSource | null = null
// Rolling series (seeded from stored history, then appended live) for the tiles' sparklines.
const cpuSeries = ref<number[]>([])
const memSeries = ref<number[]>([])
const SERIES_CAP = 90

function pushSeries(arr: number[], v: number): number[] {
  const next = [...arr, v]
  return next.length > SERIES_CAP ? next.slice(next.length - SERIES_CAP) : next
}

async function startLiveStream() {
  if (!wsId.value || liveES) return
  // Seed the sparklines with stored history so the trend is visible immediately.
  try {
    const pts = (await usageApi.history(wsId.value, '1h')).data.data ?? []
    cpuSeries.value = pts.map((p) => p.cpu_cores)
    memSeries.value = pts.map((p) => p.memory_bytes)
  } catch { /* the stream will fill the series */ }
  liveES = new EventSource(usageApi.liveStreamUrl(wsId.value))
  liveES.onmessage = (ev) => {
    try {
      const s = JSON.parse(ev.data) as WorkspaceLiveSample
      live.value = s
      liveConnected.value = true
      cpuSeries.value = pushSeries(cpuSeries.value, s.cpu_cores)
      memSeries.value = pushSeries(memSeries.value, s.memory_bytes)
    } catch { /* ignore malformed frames */ }
  }
  liveES.onerror = () => { liveConnected.value = false } // EventSource auto-reconnects
}
function stopLiveStream() {
  liveES?.close()
  liveES = null
  liveConnected.value = false
  live.value = null
  cpuSeries.value = []
  memSeries.value = []
}
function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

const wsId = computed(() => Number(route.params.id))
const activeTab = computed<Tab>(() => (route.query.tab as Tab) || 'settings')
function setTab(t: Tab) {
  router.replace({ query: { ...route.query, tab: t } })
}

const isAdmin = computed(() => ws.isWorkspaceAdmin)
const isOwner = computed(() => ws.isWorkspaceOwner)

// General. `name` is the unique handle (URL/CLI/docker); `displayName` is the
// free-text label.
const form = ref({ displayName: '', name: '', description: '' })
const savingMeta = ref(false)
// The built-in platform workspace cannot be renamed or deleted.
const isSystemWs = ref(false)

// Members + invitations
const members = ref<Member[]>([])
const invitations = ref<Invitation[]>([])
const invite = ref<{ email: string; role: WorkspaceRole }>({ email: '', role: 'developer' })
const inviteToken = ref<string | null>(null)

// Backup settings (shared S3 target)
const backup = ref<UpdateBackupSettingsInput>({
  s3_enabled: false,
  s3_endpoint: '',
  s3_bucket: '',
  s3_region: '',
  s3_access_key: '',
  s3_secret_key: '',
  s3_use_ssl: true,
  s3_force_path_style: false,
  database_backup_path: '',
  volume_backup_path: '',
  bundle_path: '',
  bundle_passphrase: '',
})
const backupSecretSet = ref(false)
const bundlePassphraseSet = ref(false)
// The last connection test's result, kept on screen: it names each prefix it
// wrote to, which is the detail that makes a failure actionable.
const backupTest = ref<BackupTestResult | null>(null)
const savingBackup = ref(false)
const testingBackup = ref(false)

async function loadBackup() {
  if (!isAdmin.value) return
  try {
    const s = (await workspaceBackupApi.get(wsId.value)).data.data
    backup.value = {
      s3_enabled: s.s3_enabled,
      s3_endpoint: s.s3_endpoint ?? '',
      s3_bucket: s.s3_bucket ?? '',
      s3_region: s.s3_region ?? '',
      s3_access_key: s.s3_access_key ?? '',
      s3_secret_key: '', // never returned; left blank keeps the stored secret
      s3_use_ssl: s.s3_use_ssl,
      s3_force_path_style: s.s3_force_path_style,
      database_backup_path: s.database_backup_path ?? '',
      volume_backup_path: s.volume_backup_path ?? '',
      bundle_path: s.bundle_path ?? '',
      bundle_passphrase: '', // never returned; left blank keeps the stored one
    }
    backupSecretSet.value = s.s3_secret_set
    bundlePassphraseSet.value = s.bundle_passphrase_set
  } catch (e) {
    notify.apiError(e)
  }
}

async function saveBackup() {
  if (!isAdmin.value) return
  if (backup.value.s3_enabled && !backup.value.s3_bucket.trim()) {
    notify.error('An S3 bucket is required when S3 is enabled')
    return
  }
  savingBackup.value = true
  try {
    const s = (await workspaceBackupApi.update(wsId.value, backup.value)).data.data
    backupSecretSet.value = s.s3_secret_set
    bundlePassphraseSet.value = s.bundle_passphrase_set
    backup.value.s3_secret_key = ''
    backup.value.bundle_passphrase = ''
    notify.success('Backup settings saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingBackup.value = false
  }
}

async function testBackup() {
  if (!isAdmin.value) return
  testingBackup.value = true
  backupTest.value = null
  try {
    const res = (await workspaceBackupApi.test(wsId.value, backup.value)).data.data
    backupTest.value = res
    if (res.ok) notify.success(res.message)
    else notify.error(res.message)
  } catch (e) {
    notify.apiError(e)
  } finally {
    testingBackup.value = false
  }
}

async function loadWorkspace() {
  const w = ws.workspaces.find((x) => x.id === wsId.value)
  if (w) {
    form.value = { displayName: w.display_name || w.name, name: w.name, description: w.description || '' }
    isSystemWs.value = !!w.system
  } else {
    try {
      const res = (await workspaceApi.get(wsId.value)).data.data
      form.value = { displayName: res.display_name || res.name, name: res.name, description: res.description || '' }
      isSystemWs.value = !!res.system
    } catch (e) {
      notify.apiError(e)
    }
  }
}

async function loadMembers() {
  try {
    members.value = (await memberApi.list(wsId.value)).data.data ?? []
    invitations.value = (await memberApi.invitations(wsId.value)).data.data ?? []
    if (isAdmin.value) loadCustomRoles()
  } catch (e) {
    notify.apiError(e)
  }
}

async function saveMeta() {
  if (!isAdmin.value) return
  savingMeta.value = true
  try {
    await workspaceApi.update(wsId.value, { display_name: form.value.displayName.trim(), description: form.value.description.trim() })
    await ws.fetchWorkspaces()
    notify.success('Workspace updated')
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingMeta.value = false
  }
}

// The handle (name) has its own endpoint, separate from display name/description
// — renaming changes the workspace's URLs and its `docker login` handle.
const savingName = ref(false)
const currentName = computed(() => ws.workspaces.find((x) => x.id === wsId.value)?.name ?? form.value.name)
const nameDirty = computed(() => form.value.name.trim() !== '' && form.value.name.trim() !== currentName.value)

async function saveName() {
  if (!isAdmin.value || isSystemWs.value || !nameDirty.value) return
  savingName.value = true
  try {
    const updated = (await workspaceApi.updateName(wsId.value, form.value.name.trim())).data.data
    form.value.name = updated.name
    await ws.fetchWorkspaces()
    notify.success('Workspace name updated')
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingName.value = false
  }
}

// --- Workspace deletion (type-to-confirm + live teardown progress) ---
const deleteModalOpen = ref(false)
const confirmName = ref('')
const deletionJob = ref<DeletionJob | null>(null)
let deletionES: EventSource | null = null

// The name must be typed exactly to arm the delete button (GitHub-style guard).
const deleteArmed = computed(() => confirmName.value.trim() === form.value.name.trim())

function openDeleteModal() {
  if (!isOwner.value) return
  confirmName.value = ''
  deletionJob.value = null
  deleteModalOpen.value = true
}

function closeDeleteModal() {
  // Never abandon an in-flight teardown by closing the dialog.
  if (deletionJob.value?.status === 'running') return
  deleteModalOpen.value = false
  stopDeletionStream()
}

function stopDeletionStream() {
  deletionES?.close()
  deletionES = null
}

// Icon + state per stepper row.
function stepIcon(status: string): string {
  switch (status) {
    case 'done': return 'mdi-check-circle'
    case 'error': return 'mdi-alert-circle'
    case 'active': return 'mdi-loading mdi-spin'
    default: return 'mdi-circle-outline'
  }
}

async function confirmDelete() {
  if (!isOwner.value || !deleteArmed.value || deletionJob.value) return
  try {
    const job = (await workspaceApi.startDeletion(wsId.value)).data.data
    deletionJob.value = job // switches the modal to the progress view
    if (job.status !== 'running') onDeletionDone(job)
    else startDeletionStream(job.id)
  } catch (e) {
    notify.apiError(e, 'Could not start workspace deletion')
  }
}

function startDeletionStream(jobId: string) {
  stopDeletionStream()
  deletionES = new EventSource(workspaceApi.deletionJobEventsUrl(wsId.value, jobId))
  deletionES.onmessage = (ev) => {
    let msg: { type?: string; data?: DeletionJob }
    try { msg = JSON.parse(ev.data) } catch { return } // ignore keep-alives
    if (msg.type === 'job' && msg.data) {
      deletionJob.value = msg.data
      if (msg.data.status !== 'running') {
        stopDeletionStream()
        onDeletionDone(msg.data)
      }
    }
  }
  // EventSource auto-reconnects on transient errors; the server re-sends the
  // current snapshot on reconnect, so nothing to do here.
}

async function onDeletionDone(job: DeletionJob) {
  if (job.status === 'succeeded') {
    notify.success('Workspace deleted')
    deleteModalOpen.value = false
    await ws.fetchWorkspaces()
    router.push('/workspaces')
  } else {
    notify.error(job.error || 'Workspace deletion failed')
  }
}

onUnmounted(stopDeletionStream)
onUnmounted(stopLiveStream)

async function sendInvite() {
  if (!invite.value.email.trim()) return
  try {
    const res = (await memberApi.invite(wsId.value, invite.value.email.trim(), invite.value.role)).data.data
    inviteToken.value = res.token
    invite.value.email = ''
    notify.success('Invitation created — share the token below')
    loadMembers()
  } catch (e) {
    notify.apiError(e)
  }
}
async function changeRole(userId: number, value: string) {
  try {
    // A "custom:<id>" value assigns a custom role; anything else is a built-in role.
    if (value.startsWith('custom:')) {
      await roleApi.assignMember(wsId.value, userId, Number(value.slice(7)))
    } else {
      await memberApi.updateRole(wsId.value, userId, value)
    }
    notify.success('Role updated')
    loadMembers()
  } catch (e) {
    notify.apiError(e)
    loadMembers()
  }
}
const pendingRemoveMember = ref<{ id: number; name: string } | null>(null)
const removingMember = ref(false)
function removeMember(userId: number, name: string) {
  pendingRemoveMember.value = { id: userId, name }
}
async function confirmRemoveMember() {
  if (!pendingRemoveMember.value) return
  removingMember.value = true
  try {
    await memberApi.remove(wsId.value, pendingRemoveMember.value.id)
    notify.success('Member removed')
    pendingRemoveMember.value = null
    loadMembers()
  } catch (e) {
    notify.apiError(e)
  } finally {
    removingMember.value = false
  }
}

async function copyToken() {
  if (!inviteToken.value) return
  if (await copyText(inviteToken.value)) notify.success('Token copied')
  else notify.error('Copy failed — select and copy the token manually')
}

function loadTab(tab: Tab) {
  // Only the usage tab needs the live stream; tear it down when leaving.
  if (tab !== 'usage') stopLiveStream()
  if (tab === 'settings') loadWorkspace()
  else if (tab === 'members') loadMembers()
  else if (tab === 'usage') { loadUsage(); startLiveStream() }
  else if (tab === 'backup') loadBackup()
}

onMounted(async () => {
  if (!ws.loaded) await ws.fetchWorkspaces().catch(() => {})
  // Align the active workspace context with the page being viewed.
  if (wsId.value && ws.currentWorkspaceId !== wsId.value) ws.setWorkspace(wsId.value)
  loadTab(activeTab.value)
})
watch(activeTab, (t) => loadTab(t))
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/workspaces')">
          <span class="mdi mdi-arrow-left"></span> Workspaces
        </button>
        <h1 style="margin-top: 8px">{{ form.displayName || form.name || 'Workspace' }}</h1>
      </div>
    </div>

    <div class="tabs">
      <button v-for="t in tabs" :key="t.id" class="tab" :class="{ active: activeTab === t.id }" @click="setTab(t.id)">
        <span class="mdi" :class="t.icon"></span> {{ t.label }}
      </button>
    </div>

    <!-- General -->
    <div v-if="activeTab === 'settings'" class="card">
      <div class="card-header"><h2>General</h2></div>
      <div class="card-body">
        <div class="form-group">
          <label class="form-label">Workspace ID</label>
          <code class="ws-id mono">{{ wsId }}</code>
        </div>
        <div class="form-group">
          <label class="form-label">Display name</label>
          <input v-model="form.displayName" class="form-input" :disabled="!isAdmin || isSystemWs" aria-label="Display name" style="max-width: 420px" />
          <p class="text-muted text-sm" style="margin-top: 4px">
            The free-text label shown across the UI.
            <template v-if="isSystemWs"> The built-in platform workspace cannot be renamed.</template>
          </p>
        </div>
        <div class="form-group">
          <label class="form-label">Name</label>
          <div class="slug-row">
            <input
              v-model="form.name"
              class="form-input mono"
              :disabled="!isAdmin || isSystemWs"
              placeholder="my-workspace"
              autocomplete="off"
              spellcheck="false"
              aria-label="Name"
              style="max-width: 320px"
            />
            <button
              v-if="isAdmin && !isSystemWs"
              class="btn btn-secondary"
              :disabled="savingName || !nameDirty"
              @click="saveName"
            >
              {{ savingName ? 'Saving…' : 'Change name' }}
            </button>
          </div>
          <p class="text-muted text-sm" style="margin-top: 4px">
            The unique handle in the workspace's URLs and its <code>docker login</code> namespace.
            Lowercase letters, digits, and hyphens. <strong>Changing it updates every URL and the
            docker handle</strong>, and reserved names are not allowed.
            <template v-if="isSystemWs"> The system workspace name is reserved and cannot be changed.</template>
          </p>
        </div>
        <div class="form-group">
          <label class="form-label">Description</label>
          <input v-model="form.description" class="form-input" :disabled="!isAdmin" placeholder="What is this workspace for?" aria-label="Description" style="max-width: 420px" />
        </div>
        <button v-if="isAdmin" class="btn btn-primary" :disabled="savingMeta" @click="saveMeta">
          {{ savingMeta ? 'Saving…' : 'Save changes' }}
        </button>
        <p v-else class="text-muted text-sm">You need admin access to edit workspace settings.</p>
      </div>
      <template v-if="isOwner && !isSystemWs">
        <div class="card-header" style="border-top: 1px solid var(--border-primary)"><h2 style="color: var(--danger-600)">Danger zone</h2></div>
        <div class="card-body flex items-center justify-between">
          <div>
            <div style="font-weight: 600; color: var(--text-primary)">Delete this workspace</div>
            <div class="text-muted text-sm">Permanently removes the workspace and all its resources.</div>
          </div>
          <button class="btn btn-danger" @click="openDeleteModal">Delete workspace</button>
        </div>
      </template>
    </div>

    <!-- Members -->
    <template v-else-if="activeTab === 'members'">
      <div class="card mb-4">
        <div class="card-header"><h2>Members</h2></div>
        <div class="table-wrapper">
          <table>
            <thead><tr><th>Member</th><th>Role</th><th></th></tr></thead>
            <tbody>
              <tr v-for="m in members" :key="m.id">
                <td>
                  <div class="cell-id">
                    <span class="avatar avatar-sm">{{ m.user.name.charAt(0).toUpperCase() }}</span>
                    <span class="cell-text">
                      <span class="cell-title">{{ m.user.name }}</span>
                      <span class="cell-sub">{{ m.user.email }}</span>
                    </span>
                  </div>
                </td>
                <td>
                  <select v-if="isAdmin" class="form-select" style="max-width: 170px" aria-label="Member role" :value="m.custom_role_id ? 'custom:' + m.custom_role_id : m.role" @change="changeRole(m.user_id, ($event.target as HTMLSelectElement).value)">
                    <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
                    <optgroup v-if="customRoleList.length" label="Custom roles">
                      <option v-for="cr in customRoleList" :key="cr.id" :value="'custom:' + cr.id">{{ cr.name }}</option>
                    </optgroup>
                  </select>
                  <span v-else class="badge badge-neutral">{{ m.custom_role_id ? (customRoleList.find((c) => c.id === m.custom_role_id)?.name ?? m.role) : m.role }}</span>
                </td>
                <td class="text-right">
                  <button v-if="isAdmin && m.role !== 'owner'" class="btn-icon btn-icon-danger" title="Remove" aria-label="Remove" @click="removeMember(m.user_id, m.user.name)">
                    <span class="mdi mdi-account-remove-outline"></span>
                  </button>
                </td>
              </tr>
              <tr v-if="members.length === 0"><td colspan="3" class="text-center text-muted">No members.</td></tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="isAdmin" class="card">
        <div class="card-header"><h2>Invite a member</h2></div>
        <div class="card-body">
          <form class="invite-form" @submit.prevent="sendInvite">
            <input v-model="invite.email" type="email" class="form-input" placeholder="email@example.com" aria-label="Invitee email" required />
            <select v-model="invite.role" class="form-select" aria-label="Invitee role">
              <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
            </select>
            <button class="btn btn-primary">Send invite</button>
          </form>

          <div v-if="inviteToken" class="app-banner app-banner--info mb-4" style="margin-top: 16px">
            <span class="mdi mdi-ticket-confirmation-outline app-banner-icon"></span>
            <div class="app-banner-content">
              <p class="app-banner-title">Invitation token</p>
              <p class="app-banner-text">Share this token with the invitee — it is shown only once.</p>
              <div class="code-block" style="margin-top: 8px">{{ inviteToken }}</div>
            </div>
            <div class="app-banner-actions">
              <button class="app-banner-btn" @click="copyToken">Copy</button>
            </div>
          </div>

          <div v-if="invitations.length" style="margin-top: 16px">
            <div class="form-label">Pending invitations</div>
            <div class="table-wrapper">
              <table>
                <tbody>
                  <tr v-for="i in invitations" :key="i.id">
                    <td>{{ i.email }}</td>
                    <td><span class="badge badge-neutral">{{ i.role }}</span></td>
                    <td class="text-right cell-sub">{{ i.status }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- Custom roles -->
    <WorkspaceRolesPanel v-else-if="activeTab === 'roles'" :ws-id="wsId" :my-role="myRole" @changed="loadCustomRoles" />

    <!-- Usage vs plan limits -->
    <template v-else-if="activeTab === 'usage'">
      <!-- Live usage: actual consumption across running containers (SSE) -->
      <div class="card" style="margin-bottom: 16px">
        <div class="card-header">
          <h2>Live usage</h2>
          <span class="badge" :class="liveConnected ? 'badge-success' : 'badge-neutral'">
            <span class="mdi" :class="liveConnected ? 'mdi-access-point' : 'mdi-access-point-off'"></span>
            {{ liveConnected ? 'Live' : 'Connecting…' }}
          </span>
        </div>
        <div class="card-body">
          <p class="text-muted text-sm" style="margin-bottom: 16px">
            Actual consumption right now across the workspace's running app &amp; database containers.
          </p>
          <div class="live-grid">
            <div class="live-stat">
              <span class="live-value">{{ (live?.cpu_cores ?? 0).toFixed(2) }}</span>
              <span class="live-label">CPU cores</span>
              <span class="live-sub" v-if="usage && usage.cpu_cores.limit >= 0">limit {{ usage.cpu_cores.limit }}</span>
              <Sparkline :values="cpuSeries" :width="180" :height="34" stroke="var(--primary-500)" class="live-spark" />
            </div>
            <div class="live-stat">
              <span class="live-value">{{ fmtBytes(live?.memory_bytes ?? 0) }}</span>
              <span class="live-label">Memory</span>
              <span class="live-sub" v-if="usage && usage.memory_mb.limit >= 0">limit {{ usage.memory_mb.limit }} MB</span>
              <Sparkline :values="memSeries" :width="180" :height="34" stroke="var(--info-500, #0ea5e9)" class="live-spark" />
            </div>
            <div class="live-stat">
              <span class="live-value">{{ live?.containers ?? 0 }}</span>
              <span class="live-label">Containers</span>
            </div>
            <div class="live-stat">
              <span class="live-value">{{ fmtBytes(live?.net_rx_bytes ?? 0) }} / {{ fmtBytes(live?.net_tx_bytes ?? 0) }}</span>
              <span class="live-label">Net RX / TX</span>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <h2>Usage</h2>
          <span v-if="usage" class="badge" :class="usage.enforced ? 'badge-success' : 'badge-neutral'">
            {{ usage.enforced ? 'Enforced' : 'Not enforced' }}
          </span>
        </div>
        <div v-if="usageLoading && !usage" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="usage" class="card-body">
          <p class="text-muted text-sm" style="margin-bottom: 16px">
            Plan: <strong>{{ usage.plan_name || 'None (unlimited)' }}</strong>.
            <span v-if="!usage.enforced">Limits are shown for reference; enforcement is disabled platform-wide.</span>
          </p>
          <div v-for="row in usageRows" :key="row.key" class="meter">
            <div class="meter-head">
              <span>{{ row.label }}</span>
              <span class="meter-count">{{ usage[row.key].used }} / {{ usage[row.key].limit < 0 ? '∞' : usage[row.key].limit }}{{ row.unit ? ' ' + row.unit : '' }}</span>
            </div>
            <div class="meter-track">
              <div class="meter-fill" :class="meterClass(usage[row.key])" :style="{ width: meterPct(usage[row.key]) + '%' }"></div>
            </div>
          </div>

          <div class="meter meter-flat">
            <div class="meter-head">
              <span>Databases per instance</span>
              <span class="meter-count">{{ usage.limits.max_databases_per_instance < 0 ? '∞' : usage.limits.max_databases_per_instance }}</span>
            </div>
          </div>

          <div class="caps">
            <span class="text-muted text-sm">Capabilities</span>
            <span class="badge" :class="usage.capabilities.custom_tls ? 'badge-success' : 'badge-neutral'">Custom TLS</span>
            <span class="badge" :class="usage.capabilities.privileged_host_mounts ? 'badge-success' : 'badge-neutral'">Privileged host mounts</span>
            <span class="badge" :class="usage.capabilities.shell_exec ? 'badge-success' : 'badge-neutral'">Shell access</span>
            <span class="badge" :class="usage.capabilities.shared_storage ? 'badge-success' : 'badge-neutral'">Shared storage</span>
            <span class="badge" :class="usage.capabilities.dns_providers ? 'badge-success' : 'badge-neutral'">DNS providers</span>
            <span
              class="badge"
              :class="usage.limits.security_profile === 'restricted' ? 'badge-info' : 'badge-neutral'"
              :title="usage.limits.security_profile === 'restricted'
                ? 'App and job containers run as a non-root platform UID'
                : 'Containers run as the image default user'"
            >
              Security: {{ usage.limits.security_profile === 'restricted' ? 'Restricted' : 'Default' }}
            </span>
          </div>
        </div>
      </div>
    </template>

    <!-- Backup (shared S3 target) -->
    <template v-else-if="activeTab === 'backup'">
      <div v-if="!isAdmin" class="card">
        <div class="card-body">
          <p class="text-muted text-sm">You need admin access to manage backup settings.</p>
        </div>
      </div>
      <div v-else class="card">
        <div class="card-header">
          <div>
            <h2>S3 backup target</h2>
            <p class="text-muted text-sm" style="margin: 4px 0 0">
              One bucket and credentials shared by database and volume backups. The secret is stored
              encrypted and never shown again.
            </p>
          </div>
        </div>
        <div class="card-body">
          <label class="toggle-row">
            <input v-model="backup.s3_enabled" type="checkbox" />
            <span>Enable S3 backups for this workspace</span>
          </label>

          <fieldset class="backup-fields" :disabled="!backup.s3_enabled">
            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Bucket</label>
                <input v-model="backup.s3_bucket" class="form-input" placeholder="my-backups" aria-label="Bucket" />
              </div>
              <div class="form-group">
                <label class="form-label">Region</label>
                <input v-model="backup.s3_region" class="form-input" placeholder="us-east-1" aria-label="Region" />
              </div>
            </div>

            <div class="form-group">
              <label class="form-label">Endpoint <span class="text-muted">(optional, for S3-compatible)</span></label>
              <input v-model="backup.s3_endpoint" class="form-input" placeholder="https://s3.amazonaws.com" aria-label="Endpoint" />
            </div>

            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Access key</label>
                <input v-model="backup.s3_access_key" class="form-input" autocomplete="off" aria-label="Access key" />
              </div>
              <div class="form-group">
                <label class="form-label">Secret key</label>
                <input
                  v-model="backup.s3_secret_key"
                  class="form-input"
                  type="password"
                  autocomplete="new-password"
                  :placeholder="backupSecretSet ? '••••• (set — leave blank to keep)' : ''"
                  aria-label="Secret key"
                />
              </div>
            </div>

            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Database backup path</label>
                <input v-model="backup.database_backup_path" class="form-input" placeholder="backups/databases" aria-label="Database backup path" />
              </div>
              <div class="form-group">
                <label class="form-label">Volume backup path</label>
                <input v-model="backup.volume_backup_path" class="form-input" placeholder="backups/volumes" aria-label="Volume backup path" />
              </div>
            </div>

            <div class="form-grid">
              <div class="form-group">
                <label class="form-label">Bundle path <span class="text-muted">(portable backups)</span></label>
                <input v-model="backup.bundle_path" class="form-input" placeholder="bundles" aria-label="Bundle path" />
              </div>
              <div class="form-group">
                <label class="form-label">Bundle passphrase</label>
                <input
                  v-model="backup.bundle_passphrase"
                  class="form-input"
                  type="password"
                  autocomplete="new-password"
                  :placeholder="bundlePassphraseSet ? '\u2022\u2022\u2022\u2022\u2022 (set \u2014 leave blank to keep)' : 'at least 12 characters'"
                  aria-label="Bundle passphrase"
                />
                <p class="text-muted text-sm" style="margin: 6px 0 0">
                  Seals a portable bundle's secrets and encrypts its dumps. Record it outside Miabi:
                  it is the only thing that opens a bundle on another install \u2014 including one
                  rebuilt after losing this one.
                </p>
              </div>
            </div>

            <div class="toggle-list">
              <label class="toggle-row">
                <input v-model="backup.s3_use_ssl" type="checkbox" />
                <span>Use SSL (HTTPS)</span>
              </label>
              <label class="toggle-row">
                <input v-model="backup.s3_force_path_style" type="checkbox" />
                <span>Force path-style URLs (required by MinIO and some S3-compatible stores)</span>
              </label>
            </div>
          </fieldset>

          <div class="backup-actions">
            <button class="btn btn-primary" :disabled="savingBackup" @click="saveBackup">
              {{ savingBackup ? 'Saving…' : 'Save settings' }}
            </button>
            <button class="btn btn-secondary" :disabled="testingBackup || !backup.s3_enabled" @click="testBackup">
              {{ testingBackup ? 'Testing…' : 'Test connection' }}
            </button>
          </div>

          <!-- What the test actually did, per prefix. A connection test that only
               says "looks valid" is the one nobody can act on. -->
          <div v-if="backupTest" class="test-result" :class="backupTest.ok ? 'test-ok' : 'test-failed'">
            <p class="test-headline">
              <span class="mdi" :class="backupTest.ok ? 'mdi-check-circle-outline' : 'mdi-alert-circle-outline'"></span>
              {{ backupTest.message }}
            </p>
            <ul class="test-checks">
              <li v-for="c in backupTest.checks" :key="c.prefix">
                <span class="mdi" :class="c.error ? 'mdi-close' : 'mdi-check'"></span>
                <code>{{ c.prefix || '(bucket root)' }}</code>
                <span v-if="c.error" class="text-muted"> — {{ c.error }}</span>
                <span v-else-if="!c.removed" class="text-muted"> — written and read back, but not deletable</span>
                <span v-else class="text-muted"> — written, read back and removed</span>
              </li>
            </ul>
          </div>
        </div>
      </div>
    </template>

    <!-- Portability (export/restore the whole workspace as a bundle) -->
    <template v-else-if="activeTab === 'portability'">
      <div v-if="!isAdmin" class="card">
        <div class="card-body">
          <p class="text-muted text-sm">You need admin access to export or restore this workspace.</p>
        </div>
      </div>
      <PortableBackupPanel v-else :ws-id="wsId" :can-restore="myRole === 'owner'" />
    </template>

    <!-- Notifications (Telegram channels) -->
    <NotificationChannels v-else-if="activeTab === 'notifications'" />

  <!-- Delete workspace: type-to-confirm, then live teardown progress -->
  <Teleport to="body">
    <!-- Confirm step -->
    <AppModal v-if="deleteModalOpen && (!deletionJob)" @close="closeDeleteModal">
      <div class="modal-header">
        <h3>Delete workspace</h3>
        <button class="btn-icon btn-icon-muted" aria-label="Close" @click="closeDeleteModal"><span class="mdi mdi-close"></span></button>
      </div>
      <div class="modal-body">
        <div class="danger-note">
          <span class="mdi mdi-alert-outline"></span>
          <div>
            This permanently removes <strong>{{ form.name }}</strong> and <strong>all its resources</strong> —
            applications, databases, volumes and stacks. This cannot be undone.
          </div>
        </div>
        <div class="form-group" style="margin-top: 16px">
          <label class="form-label">Type <strong>{{ form.name }}</strong> to confirm</label>
          <input
            v-model="confirmName"
            class="form-input"
            :placeholder="form.name"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            aria-label="Type workspace name to confirm"
            @keyup.enter="confirmDelete"
          />
        </div>
      </div>
      <div class="modal-footer">
        <button type="button" class="btn btn-secondary" @click="closeDeleteModal">Cancel</button>
        <button type="button" class="btn btn-danger" :disabled="!deleteArmed" @click="confirmDelete">
          Delete workspace
        </button>
      </div>
    </AppModal>
    <!-- Progress step -->
    <AppModal v-else-if="deleteModalOpen && deletionJob" @close="closeDeleteModal">
      <div class="modal-header">
        <h3>{{ deletionJob.status === 'failed' ? 'Deletion failed' : `Deleting ${form.name}` }}</h3>
        <button v-if="deletionJob.status !== 'running'" class="btn-icon btn-icon-muted" aria-label="Close" @click="closeDeleteModal">
          <span class="mdi mdi-close"></span>
        </button>
      </div>
      <div class="modal-body">
        <ul class="delete-steps">
          <li v-for="p in deletionJob.phases" :key="p.key" class="delete-step" :class="`is-${p.status}`">
            <span class="mdi step-icon" :class="stepIcon(p.status)"></span>
            <span class="step-label">{{ p.label }}</span>
          </li>
        </ul>
        <p v-if="deletionJob.status === 'running' && deletionJob.message" class="delete-msg">
          {{ deletionJob.message }}
        </p>
        <div v-else-if="deletionJob.status === 'failed'" class="danger-note" style="margin-top: 14px">
          <span class="mdi mdi-alert-outline"></span>
          <div>{{ deletionJob.error || 'The workspace could not be fully deleted.' }}</div>
        </div>
      </div>
      <div v-if="deletionJob.status === 'failed'" class="modal-footer">
        <button type="button" class="btn btn-secondary" @click="closeDeleteModal">Close</button>
      </div>
    </AppModal>
  </Teleport>

  <ConfirmDialog
    :open="!!pendingRemoveMember"
    title="Remove member"
    :message="`Remove ${pendingRemoveMember?.name} from this workspace?`"
    confirm-label="Remove"
    variant="danger"
    :busy="removingMember"
    @confirm="confirmRemoveMember"
    @cancel="pendingRemoveMember = null"
  />
  </div>
</template>

<style scoped>
.invite-form {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.invite-form .form-input {
  max-width: 280px;
}
.invite-form .form-select {
  max-width: 150px;
}
.tab .mdi {
  font-size: 15px;
  margin-right: 4px;
}
.backup-fields {
  border: 0;
  padding: 0;
  margin: 16px 0 0;
  min-width: 0;
}
.backup-fields:disabled {
  opacity: 0.55;
}
.form-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 12px 16px;
  margin-bottom: 8px;
}
.toggle-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 8px;
}
.toggle-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  color: var(--text-primary);
}
.toggle-row input {
  width: auto;
  margin: 0;
}
.backup-actions {
  display: flex;
  gap: 10px;
  margin-top: 20px;
}
.test-result {
  margin-top: 16px;
  padding: 12px 14px;
  border-radius: 8px;
  border: 1px solid var(--border-primary);
  background: var(--bg-tertiary);
  font-size: 13px;
}
.test-result.test-failed {
  border-color: var(--danger-500, var(--danger-600));
}
.test-headline {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0;
}
.test-ok .test-headline .mdi {
  color: var(--success-500, var(--primary-500));
}
.test-failed .test-headline .mdi {
  color: var(--danger-500, var(--danger-600));
}
.test-checks {
  margin: 10px 0 0;
  padding-left: 4px;
  list-style: none;
}
.test-checks li {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin-bottom: 4px;
}
.test-checks code {
  font-size: 12px;
}
.meter { margin-bottom: 16px; }
.meter-head { display: flex; justify-content: space-between; font-size: 13px; color: var(--text-secondary); margin-bottom: 6px; }
.meter-count { font-variant-numeric: tabular-nums; color: var(--text-muted); }
.meter-track { height: 8px; border-radius: 4px; background: var(--bg-tertiary); overflow: hidden; }
.meter-fill { height: 100%; border-radius: 4px; background: var(--primary-500); transition: width 200ms ease; }
.meter-fill.meter-warn { background: var(--warning-500); }
.meter-fill.meter-full { background: var(--danger-500, var(--danger-600)); }
.meter-flat .meter-head { margin-bottom: 0; }
.caps { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--border-primary); }
.live-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; }
.live-stat { display: flex; flex-direction: column; gap: 2px; padding: 14px 16px; border-radius: var(--radius); background: var(--bg-tertiary); }
.live-value { font-size: 22px; font-weight: 600; font-variant-numeric: tabular-nums; line-height: 1.2; }
.live-label { font-size: 12px; color: var(--text-muted); }
.live-sub { font-size: 11px; color: var(--text-muted); opacity: 0.8; }
.live-spark { margin-top: 8px; width: 100%; }
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
.mono { font-family: monospace; }
.ws-id { display: inline-block; padding: 4px 10px; border-radius: var(--radius); background: var(--bg-tertiary); color: var(--text-secondary); font-size: 13px; }
.slug-row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }

/* --- Delete workspace: warning note + live teardown stepper --- */
.danger-note {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px 14px;
  border-radius: var(--radius);
  background: var(--danger-50, rgba(220, 38, 38, 0.06));
  border: 1px solid var(--danger-200, rgba(220, 38, 38, 0.25));
  color: var(--text-primary);
  font-size: 13px;
  line-height: 1.45;
}
.danger-note .mdi {
  font-size: 18px;
  flex-shrink: 0;
  color: var(--danger-600);
}
.delete-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.delete-step {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: var(--radius);
  font-size: 14px;
  color: var(--text-muted);
  transition: background 0.15s, color 0.15s;
}
.delete-step .step-icon {
  font-size: 18px;
  flex-shrink: 0;
  color: var(--text-muted);
}
.delete-step.is-active {
  background: var(--primary-50);
  color: var(--text-primary);
  font-weight: 600;
}
.delete-step.is-active .step-icon {
  color: var(--primary-600);
}
.delete-step.is-done {
  color: var(--text-primary);
}
.delete-step.is-done .step-icon {
  color: var(--success-500, #16a34a);
}
.delete-step.is-error .step-icon {
  color: var(--danger-500, var(--danger-600));
}
.delete-msg {
  margin-top: 12px;
  font-size: 13px;
  color: var(--text-muted);
  text-align: center;
}
.mdi-spin {
  animation: mdi-spin 0.9s linear infinite;
}
@keyframes mdi-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
