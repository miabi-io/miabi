<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { channelApi, type ChannelInput } from '@/api/notifications'
import { NOTIFIABLE_EVENTS } from '@/constants/notifiableEvents'
import type { NotificationChannel } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<NotificationChannel[]>([])
const loading = ref(false)
const testing = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<NotificationChannel | null>(null)
const form = ref<ChannelInput>({ type: 'telegram', name: '', bot_token: '', chat_id: '', webhook_url: '', events: [], enabled: true })

const channelTypes: { value: 'telegram' | 'slack' | 'discord'; label: string }[] = [
  { value: 'telegram', label: 'Telegram' },
  { value: 'slack', label: 'Slack' },
  { value: 'discord', label: 'Discord' },
]
function typeLabel(t: string) {
  return channelTypes.find((c) => c.value === t)?.label ?? t
}

async function load(id: number | null) {
  if (!id) {
    items.value = []
    return
  }
  loading.value = true
  try {
    items.value = (await channelApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  editing.value = null
  form.value = { type: 'telegram', name: '', bot_token: '', chat_id: '', webhook_url: '', events: [], enabled: true }
  showModal.value = true
}
function openEdit(c: NotificationChannel) {
  editing.value = c
  form.value = {
    type: c.type,
    name: c.name,
    bot_token: '',
    chat_id: c.config?.chat_id ?? '',
    webhook_url: '',
    events: [...c.events],
    enabled: c.enabled,
  }
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
    notify.error('Select at least one event')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await channelApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success('Channel updated')
    } else {
      await channelApi.create(currentWorkspaceId.value, form.value)
      notify.success('Channel added')
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function test(c: NotificationChannel) {
  if (!currentWorkspaceId.value) return
  testing.value = c.id
  try {
    await channelApi.test(currentWorkspaceId.value, c.id)
    notify.success(`${c.name}: test message sent`)
  } catch (e) {
    notify.apiError(e, 'Test message failed')
  } finally {
    testing.value = null
  }
}

const pendingDelete = ref<NotificationChannel | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !pendingDelete.value) return
  deleting.value = true
  try {
    await channelApi.remove(currentWorkspaceId.value, pendingDelete.value.id)
    notify.success('Channel deleted')
    pendingDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <p class="subtitle">Send Telegram, Slack, or Discord messages when app events fire — deploys, container crashes, and more.</p>
      <button v-if="ws.isWorkspaceAdmin" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New channel
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-send-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No notification channels yet</h3>
        <p>Connect a Telegram bot to get deploy and container alerts in a chat.</p>
        <button v-if="ws.isWorkspaceAdmin" class="btn btn-primary mt-4" @click="openCreate">Add a channel</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Channel</th><th>Chat</th><th>Events</th><th>Status</th><th></th></tr></thead>
          <tbody>
            <tr v-for="c in items" :key="c.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-send" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ c.name }}</span>
                    <span class="cell-sub">{{ typeLabel(c.type) }}</span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ c.config?.chat_id || '—' }}</td>
              <td class="cell-sub">{{ c.events.length }} event{{ c.events.length === 1 ? '' : 's' }}</td>
              <td>
                <span class="badge" :class="c.enabled ? 'badge-success' : 'badge-neutral'">
                  {{ c.enabled ? 'Enabled' : 'Disabled' }}
                </span>
              </td>
              <td class="text-right table-actions">
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" title="Send test" aria-label="Send test" :disabled="testing === c.id" @click="test(c)">
                  <span class="mdi" :class="testing === c.id ? 'mdi-loading mdi-spin' : 'mdi-send-outline'"></span>
                </button>
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(c)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingDelete = c"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showModal" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ editing ? 'Edit channel' : 'New notification channel' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Type</label>
                <select v-model="form.type" class="form-select" :disabled="!!editing" aria-label="Type">
                  <option v-for="t in channelTypes" :key="t.value" :value="t.value">{{ t.label }}</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="e.g. Ops alerts" aria-label="Name" required autofocus />
              </div>

              <!-- Telegram -->
              <template v-if="form.type === 'telegram'">
                <div class="form-group">
                  <label class="form-label">Bot token <span v-if="editing" class="text-muted">(leave blank to keep current)</span></label>
                  <input v-model="form.bot_token" type="password" class="form-input" placeholder="123456:ABC-DEF…" autocomplete="new-password" aria-label="Bot token" :required="!editing" />
                  <p class="form-hint">Create a bot with <strong>@BotFather</strong> and paste its token.</p>
                </div>
                <div class="form-group">
                  <label class="form-label">Chat ID</label>
                  <input v-model="form.chat_id" class="form-input" placeholder="e.g. -1001234567890" aria-label="Chat ID" required />
                  <p class="form-hint">A user, group, or channel id. Add the bot to the chat first.</p>
                </div>
              </template>

              <!-- Slack / Discord -->
              <template v-else>
                <div class="form-group">
                  <label class="form-label">Webhook URL <span v-if="editing" class="text-muted">(leave blank to keep current)</span></label>
                  <input v-model="form.webhook_url" type="password" class="form-input" :placeholder="form.type === 'slack' ? 'https://hooks.slack.com/services/…' : 'https://discord.com/api/webhooks/…'" autocomplete="new-password" aria-label="Webhook URL" :required="!editing" />
                  <p class="form-hint">Create an incoming webhook in your {{ typeLabel(form.type || '') }} workspace and paste its URL.</p>
                </div>
              </template>
              <div class="form-group">
                <label class="form-label">Events</label>
                <div class="event-grid">
                  <label v-for="e in NOTIFIABLE_EVENTS" :key="e.value" class="event-option">
                    <input type="checkbox" :checked="form.events.includes(e.value)" @change="toggleEvent(e.value)" />
                    <span>{{ e.label }}</span>
                  </label>
                </div>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="check-row">
                  <input type="checkbox" v-model="form.enabled" />
                  <span>Enabled</span>
                </label>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">
                {{ saving ? 'Saving…' : editing ? 'Save' : 'Add channel' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete channel"
      :message="`Delete notification channel &quot;${pendingDelete?.name}&quot;?`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.section-head { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 16px; }
.subtitle { font-size: 13px; color: var(--text-muted); margin: 0; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.event-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 16px; }
.event-option, .check-row { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
.event-option input, .check-row input { width: 15px; height: 15px; }
</style>
