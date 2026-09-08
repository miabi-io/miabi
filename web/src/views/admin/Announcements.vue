<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { announcementApi, type Announcement, type AnnouncementAudience, type AnnouncementPayload } from '@/api/announcements'
import { adminApi } from '@/api/admin'
import type { AdminWorkspace } from '@/api/types'
import type { AlertSeverity } from '@/api/inbox'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const announcements = useEntitlement('announcements')

const items = ref<Announcement[]>([])
const workspaces = ref<AdminWorkspace[]>([])
const loading = ref(false)
const saving = ref(false)
const showModal = ref(false)
const editing = ref<Announcement | null>(null)

const confirmTarget = ref<Announcement | null>(null)

const audiences: { value: AnnouncementAudience; label: string; hint: string }[] = [
  { value: 'all', label: 'Everyone', hint: 'Every active user on this platform.' },
  { value: 'admins', label: 'Platform admins', hint: 'Super-admins only.' },
  { value: 'owners', label: 'Workspace owners & admins', hint: 'The people who can act on a platform change.' },
  { value: 'workspaces', label: 'Specific workspaces', hint: 'Members of the workspaces you pick.' },
]

const blank = (): AnnouncementPayload => ({
  title: '',
  message: '',
  link: '',
  action_text: '',
  severity: 'info',
  audience: 'all',
  workspace_ids: [],
  pinned: false,
  dismissal: 'once',
  publish_at: '',
  expires_at: '',
})
const form = ref<AnnouncementPayload>(blank())

// reach is the resolved audience size, refreshed as the operator changes the
// targeting — the blast radius shown before the send, not after.
const reach = ref<number | null>(null)
const audienceHint = computed(() => audiences.find((a) => a.value === form.value.audience)?.hint ?? '')

const dismissalHint = computed(() =>
  form.value.dismissal === 'never'
    ? 'The banner has no close button; it goes away when it expires or you retract it.'
    : 'The banner disappears for each reader once they close it.',
)

// A banner nobody can close and nothing retires stays on every recipient's screen
// indefinitely, which is worth saying before the send rather than after.
const lockedForever = computed(() => form.value.pinned && form.value.dismissal === 'never' && !form.value.expires_at)

async function loadReach() {
  if (form.value.audience === 'workspaces' && !form.value.workspace_ids?.length) {
    reach.value = 0
    return
  }
  try {
    reach.value = (await announcementApi.audience(form.value.audience, form.value.workspace_ids ?? [])).data.data.recipients
  } catch {
    reach.value = null
  }
}
watch(() => [form.value.audience, form.value.workspace_ids], loadReach, { deep: true })

async function load() {
  if (!announcements.has.value) return
  loading.value = true
  try {
    items.value = (await announcementApi.list()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

async function loadWorkspaces() {
  if (!announcements.has.value) return
  try {
    workspaces.value = (await adminApi.listWorkspaces(0, 200)).data.data ?? []
  } catch {
    /* targeting by workspace degrades to the other audiences */
  }
}

function openCreate() {
  editing.value = null
  form.value = blank()
  reach.value = null
  showModal.value = true
  void loadReach()
}

function openEdit(a: Announcement) {
  editing.value = a
  form.value = {
    title: a.title,
    message: a.message,
    link: a.link ?? '',
    action_text: a.action_text ?? '',
    severity: a.severity,
    audience: a.audience,
    workspace_ids: a.workspace_ids ?? [],
    pinned: a.pinned,
    dismissal: a.dismissal ?? 'once',
    publish_at: toLocalInput(a.publish_at),
    expires_at: toLocalInput(a.expires_at),
  }
  showModal.value = true
  void loadReach()
}

async function save() {
  if (!form.value.title.trim()) return
  saving.value = true
  try {
    const payload: AnnouncementPayload = {
      ...form.value,
      title: form.value.title.trim(),
      publish_at: toISO(form.value.publish_at),
      expires_at: toISO(form.value.expires_at),
    }
    if (editing.value) {
      await announcementApi.update(editing.value.id, payload)
      notify.success('Announcement updated in every inbox it reached')
    } else {
      const res = await announcementApi.create(payload)
      const a = res.data.data
      notify.success(
        a.status === 'scheduled'
          ? `Scheduled for ${fmt(a.publish_at)}`
          : `Delivered to ${a.recipients} ${a.recipients === 1 ? 'user' : 'users'}`,
      )
    }
    showModal.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function publishNow(a: Announcement) {
  try {
    await announcementApi.publish(a.id)
    notify.success('Announcement broadcast')
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}

function askRetract(a: Announcement) {
  confirmTarget.value = a
}

async function retract() {
  const a = confirmTarget.value
  if (!a) return
  try {
    await announcementApi.retract(a.id)
    notify.success('Announcement retracted')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    confirmTarget.value = null
  }
}

// The datetime-local input speaks local wall-clock time; the API speaks RFC3339.
function toISO(v?: string) {
  return v ? new Date(v).toISOString() : ''
}
function toLocalInput(iso?: string | null) {
  if (!iso) return ''
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function fmt(iso?: string | null) {
  return iso ? new Date(iso).toLocaleString() : '—'
}
function sevClass(s: AlertSeverity) {
  return s === 'critical' ? 'badge-danger' : s === 'warning' ? 'badge-warning' : 'badge-neutral'
}
function statusClass(s: string) {
  return s === 'published' ? 'badge-success' : s === 'scheduled' ? 'badge-info' : 'badge-neutral'
}
function audienceLabel(a: Announcement) {
  const base = audiences.find((x) => x.value === a.audience)?.label ?? a.audience
  if (a.audience !== 'workspaces') return base
  return `${a.workspace_ids?.length ?? 0} workspace${a.workspace_ids?.length === 1 ? '' : 's'}`
}

onMounted(async () => {
  await load()
  await loadWorkspaces()
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Announcements</h1>
        <p class="text-muted">
          Put a notice in the inbox of the users it concerns — planned maintenance, a breaking
          upgrade, a policy change.
        </p>
      </div>
      <button v-if="announcements.has.value" class="btn btn-primary" :disabled="!announcements.mutable.value" @click="openCreate">
        <span class="mdi mdi-bullhorn-outline"></span> New announcement
      </button>
    </div>

    <!-- Locked (Community / not entitled) -->
    <div v-if="!announcements.has.value" class="card">
      <div class="card-body locked">
        <span class="mdi mdi-lock-outline"></span>
        <div>
          <p>
            Platform announcements are an Enterprise feature. Broadcast a notice to every user, to
            platform admins, to workspace owners, or to selected workspaces — scheduled, pinned as a
            banner, retractable, and delivered to their in-app inbox.
          </p>
          <router-link to="/admin/license" class="btn btn-secondary btn-sm">Manage license</router-link>
        </div>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div v-if="loading && !items.length" class="card-body"><span class="spinner"></span></div>

        <div v-else-if="!items.length" class="empty-state">
          <span class="mdi mdi-bullhorn-outline" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>Nothing broadcast yet</h3>
          <p class="text-muted">Announcements you send appear here, with what they reached.</p>
          <button class="btn btn-primary mt-4" :disabled="!announcements.mutable.value" @click="openCreate">
            New announcement
          </button>
        </div>

        <div v-else class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Notice</th>
                <th>Audience</th>
                <th>Status</th>
                <th>Delivered</th>
                <th>Sent</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in items" :key="a.id">
                <td>
                  <div class="cell-title">
                    {{ a.title }}
                    <span v-if="a.pinned" class="mdi mdi-pin" title="Pinned as a banner"></span>
                    <span
                      v-if="a.pinned && a.dismissal === 'never'"
                      class="mdi mdi-lock-outline"
                      title="Readers cannot dismiss this banner"
                    ></span>
                  </div>
                  <div class="cell-sub">{{ a.message }}</div>
                </td>
                <td>
                  <span class="badge badge-neutral">{{ audienceLabel(a) }}</span>
                </td>
                <td>
                  <span class="badge" :class="statusClass(a.status)">{{ a.status }}</span>
                  <span class="badge" :class="sevClass(a.severity)">{{ a.severity }}</span>
                </td>
                <td>{{ a.recipients }}</td>
                <td>
                  <div>{{ a.status === 'scheduled' ? fmt(a.publish_at) : fmt(a.published_at) }}</div>
                  <div class="cell-sub">{{ a.author_name || `#${a.created_by}` }}</div>
                </td>
                <td class="right">
                  <button
                    v-if="a.status === 'scheduled'"
                    class="btn btn-secondary btn-sm"
                    :disabled="!announcements.mutable.value"
                    @click="publishNow(a)"
                  >
                    Send now
                  </button>
                  <button class="btn btn-secondary btn-sm" :disabled="!announcements.mutable.value" @click="openEdit(a)">
                    Edit
                  </button>
                  <button class="btn btn-danger btn-sm" @click="askRetract(a)">Retract</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <Teleport to="body">
      <AppModal v-if="showModal" max-width="720px" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit announcement' : 'New announcement' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Title</label>
              <input v-model="form.title" class="form-input" maxlength="200" placeholder="Scheduled maintenance on Sunday" required autofocus />
            </div>

            <div class="form-group">
              <label class="form-label">Message</label>
              <textarea
                v-model="form.message"
                class="form-input"
                rows="3"
                maxlength="4000"
                placeholder="Miabi will be unavailable between 02:00 and 03:00 UTC while the control plane is upgraded. Running workloads are unaffected."
              ></textarea>
            </div>

            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Audience</label>
                <select v-model="form.audience" class="form-select">
                  <option v-for="a in audiences" :key="a.value" :value="a.value">{{ a.label }}</option>
                </select>
                <span class="form-hint">
                  {{ audienceHint }}
                  <template v-if="reach !== null"> Reaches {{ reach }} {{ reach === 1 ? 'user' : 'users' }}.</template>
                </span>
              </div>
              <div class="form-group">
                <label class="form-label">Severity</label>
                <select v-model="form.severity" class="form-select">
                  <option value="info">Info</option>
                  <option value="warning">Warning</option>
                  <option value="critical">Critical</option>
                </select>
              </div>
            </div>

            <div v-if="form.audience === 'workspaces'" class="form-group">
              <label class="form-label">Workspaces</label>
              <select v-model="form.workspace_ids" class="form-select" multiple size="6">
                <option v-for="w in workspaces" :key="w.id" :value="w.id">
                  {{ w.display_name || w.name }}
                </option>
              </select>
              <span class="form-hint">Every member of the selected workspaces receives the notice.</span>
            </div>

            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Link</label>
                <input v-model="form.link" class="form-input" placeholder="/admin/nodes (optional)" />
                <span class="form-hint">Where the notice takes the reader.</span>
              </div>
              <div class="form-group">
                <label class="form-label">Action text</label>
                <input v-model="form.action_text" class="form-input" maxlength="60" placeholder="View status" />
              </div>
            </div>

            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Publish at</label>
                <input v-model="form.publish_at" class="form-input" type="datetime-local" :disabled="!!editing?.published_at" />
                <span class="form-hint">
                  {{ editing?.published_at ? 'Already broadcast; the send time is fixed.' : 'Leave empty to broadcast immediately.' }}
                </span>
              </div>
              <div class="form-group">
                <label class="form-label">Expires at</label>
                <input v-model="form.expires_at" class="form-input" type="datetime-local" />
                <span class="form-hint">The notice retires itself; leave empty to keep it until retracted.</span>
              </div>
            </div>

            <div class="form-group" style="margin-bottom: 0">
              <label class="check-row">
                <input v-model="form.pinned" type="checkbox" />
                Pin as a banner across the app
              </label>
              <span class="form-hint">Reserve this for notices that change what someone should do right now.</span>

              <template v-if="form.pinned">
                <label class="form-label pin-label">Dismissal</label>
                <select v-model="form.dismissal" class="form-select">
                  <option value="once">Readers can close it</option>
                  <option value="never">Readers cannot close it</option>
                </select>
                <span class="form-hint">{{ dismissalHint }}</span>
                <div v-if="lockedForever" class="warn-row">
                  <span class="mdi mdi-alert-outline"></span>
                  Nobody can close this banner and nothing retires it. Set an expiry, or plan to
                  retract it yourself.
                </div>
              </template>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.title.trim()">
              {{ saving ? 'Saving…' : editing ? 'Save' : form.publish_at ? 'Schedule' : 'Broadcast' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!confirmTarget"
      title="Retract announcement"
      :message="`&quot;${confirmTarget?.title ?? ''}&quot; will be removed from every inbox it was delivered to.`"
      confirm-label="Retract"
      variant="danger"
      @confirm="retract"
      @cancel="confirmTarget = null"
    />

  </div>
</template>

<style scoped>
.locked {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}

.locked .mdi {
  font-size: 28px;
  color: var(--text-muted);
}

.form-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 16px;
}

.cell-title {
  font-weight: 600;
  color: var(--text-primary);
}

.cell-sub {
  font-size: 12px;
  color: var(--text-tertiary);
  margin-top: 2px;
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.right {
  text-align: right;
  white-space: nowrap;
}

.right .btn + .btn {
  margin-left: 6px;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-primary);
}

.check-row input {
  width: auto;
  margin: 0;
}

.pin-label {
  display: block;
  margin-top: 12px;
}

.warn-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 8px;
  padding: 8px 10px;
  border: 1px solid var(--warning-600);
  border-radius: 6px;
  font-size: 12px;
  color: var(--text-secondary);
}

.warn-row .mdi {
  color: var(--warning-600);
  font-size: 16px;
  flex-shrink: 0;
}

</style>
