<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { authApi } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { useNotificationStore } from '@/stores/notification'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import type { Session } from '@/api/types'

const auth = useAuthStore()
const notify = useNotificationStore()

// --- Profile (display name + username handle) ---
const name = ref(auth.user?.name ?? '')
const username = ref(auth.user?.username ?? '')
const nameBusy = ref(false)
const nameValid = computed(() => name.value.trim().length > 0)
// A blank username means "leave unchanged"; the API derives/keeps the handle.
const usernameChanged = computed(
  () => username.value.trim() !== '' && username.value.trim() !== (auth.user?.username ?? ''),
)
const profileChanged = computed(
  () => name.value.trim() !== (auth.user?.name ?? '').trim() || usernameChanged.value,
)

async function saveProfile() {
  if (!nameValid.value || !profileChanged.value || nameBusy.value) return
  nameBusy.value = true
  try {
    await auth.updateProfile(name.value.trim(), usernameChanged.value ? username.value.trim() : undefined)
    name.value = auth.user?.name ?? name.value
    username.value = auth.user?.username ?? username.value
    notify.success('Profile updated')
  } catch (e) {
    notify.apiError(e, 'Could not update profile')
  } finally {
    nameBusy.value = false
  }
}

// --- Active sessions ---
const sessions = ref<Session[]>([])
const sessionsLoading = ref(false)
const revokingSession = ref<number | null>(null)
const revokingOthers = ref(false)
// Confirmation target: a single session to revoke, or 'others' for a bulk sign-out.
const confirmTarget = ref<Session | 'others' | null>(null)

async function loadSessions() {
  sessionsLoading.value = true
  try {
    sessions.value = (await authApi.listSessions()).data.data ?? []
  } catch (e) {
    notify.apiError(e, 'Could not load sessions')
  } finally {
    sessionsLoading.value = false
  }
}

async function confirmAction() {
  if (!confirmTarget.value) return
  if (confirmTarget.value === 'others') {
    revokingOthers.value = true
    try {
      const res = await authApi.revokeOtherSessions()
      notify.success(res.data.data.message)
      confirmTarget.value = null
      await loadSessions()
    } catch (e) {
      notify.apiError(e, 'Could not revoke sessions')
    } finally {
      revokingOthers.value = false
    }
    return
  }
  const s = confirmTarget.value
  revokingSession.value = s.id
  try {
    await authApi.revokeSession(s.id)
    notify.success('Session revoked')
    confirmTarget.value = null
    sessions.value = sessions.value.filter((x) => x.id !== s.id)
  } catch (e) {
    notify.apiError(e, 'Could not revoke session')
  } finally {
    revokingSession.value = null
  }
}

const confirmTitle = computed(() => (confirmTarget.value === 'others' ? 'Revoke all other sessions' : 'Revoke session'))
const confirmMessage = computed(() =>
  confirmTarget.value === 'others'
    ? 'This will sign out all other devices and browsers. Continue?'
    : 'Sign this device out of your account? It will need to sign in again.',
)
const confirmBusy = computed(() => revokingOthers.value || revokingSession.value !== null)

// Render a friendly browser/OS label from a raw User-Agent string.
function sessionLabel(ua: string): string {
  if (!ua) return 'Unknown device'
  const browser =
    /Edg\//.test(ua) ? 'Edge'
    : /OPR\/|Opera/.test(ua) ? 'Opera'
    : /Chrome\//.test(ua) ? 'Chrome'
    : /Firefox\//.test(ua) ? 'Firefox'
    : /Safari\//.test(ua) ? 'Safari'
    : /curl/.test(ua) ? 'curl'
    : 'Browser'
  const os =
    /Windows/.test(ua) ? 'Windows'
    : /Mac OS X|Macintosh/.test(ua) ? 'macOS'
    : /Android/.test(ua) ? 'Android'
    : /iPhone|iPad|iOS/.test(ua) ? 'iOS'
    : /Linux/.test(ua) ? 'Linux'
    : ''
  return os ? `${browser} on ${os}` : browser
}

// Short form-factor hint shown as a badge, when the User-Agent reveals one.
function sessionDevice(ua: string): string {
  if (/Mobile|iPhone|Android.*Mobile/.test(ua)) return 'Mobile'
  if (/iPad|Tablet/.test(ua)) return 'Tablet'
  return ''
}

function formatSessionDate(s: string): string {
  return s
    ? new Date(s).toLocaleDateString(undefined, {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      })
    : '—'
}

onMounted(loadSessions)
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Profile</h1>
    </div>

    <div class="profile-grid">
      <!-- My Profile -->
      <div class="card">
        <div class="card-header"><h2>My Profile</h2></div>
        <div class="card-body">
          <p class="sec-desc">Update the name shown across Miabi. Your email is managed by an administrator.</p>
          <form class="profile-form" @submit.prevent="saveProfile">
            <div class="form-group">
              <label class="form-label" for="profile-name">Display name</label>
              <input id="profile-name" v-model="name" type="text" class="form-input" maxlength="100" autocomplete="name" required />
            </div>
            <div class="form-group">
              <label class="form-label" for="profile-username">Username</label>
              <input id="profile-username" v-model="username" type="text" class="form-input mono" autocomplete="username" spellcheck="false" placeholder="your-handle" />
              <small class="form-hint">Your unique handle — lowercase letters, digits, and hyphens. Used as a directory identifier.</small>
            </div>
            <div class="form-group">
              <label class="form-label" for="profile-email">Email</label>
              <input id="profile-email" :value="auth.user?.email" type="email" class="form-input" disabled />
              <small class="form-hint">Contact an administrator to change your email address.</small>
            </div>
            <button type="submit" class="btn btn-primary" :disabled="!nameValid || !profileChanged || nameBusy">
              {{ nameBusy ? 'Saving…' : 'Save changes' }}
            </button>
          </form>
        </div>
      </div>

      <!-- Active Sessions -->
      <div class="card">
        <div class="card-header">
          <h2>Active Sessions</h2>
          <button
            v-if="sessions.length > 1"
            class="btn btn-danger btn-sm"
            :disabled="revokingOthers"
            @click="confirmTarget = 'others'"
          >
            {{ revokingOthers ? 'Revoking…' : 'Revoke All Others' }}
          </button>
        </div>
        <div class="card-body">
          <p class="sec-desc">These are the devices and browsers currently logged in to your account.</p>

          <div v-if="sessionsLoading" style="text-align: center; padding: 20px 0">
            <div class="spinner"></div>
          </div>

          <div v-else-if="sessions.length === 0" class="text-muted" style="text-align: center; padding: 16px 0">
            No active sessions found.
          </div>

          <div v-else class="session-list">
            <div v-for="s in sessions" :key="s.id" class="session-item" :class="{ 'session-current': s.current }">
              <div class="session-info">
                <div class="session-browser">
                  {{ sessionLabel(s.user_agent) }}
                  <span v-if="sessionDevice(s.user_agent)" class="badge badge-neutral" style="margin-left: 6px">{{ sessionDevice(s.user_agent) }}</span>
                  <span v-if="s.current" class="badge badge-success" style="margin-left: 6px">Current</span>
                </div>
                <div class="session-meta">
                  {{ s.ip_address || 'unknown IP' }} &middot; Created {{ formatSessionDate(s.created_at) }} &middot; Expires {{ formatSessionDate(s.expires_at) }}
                </div>
              </div>
              <button
                v-if="!s.current"
                class="btn btn-danger btn-sm"
                :disabled="revokingSession === s.id"
                @click="confirmTarget = s"
              >
                {{ revokingSession === s.id ? 'Revoking…' : 'Revoke' }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :open="!!confirmTarget"
      :title="confirmTitle"
      :message="confirmMessage"
      confirm-label="Revoke"
      variant="danger"
      :busy="confirmBusy"
      @confirm="confirmAction"
      @cancel="confirmTarget = null"
    />
  </div>
</template>

<style scoped>
.profile-grid {
  display: grid;
  gap: 24px;
  max-width: 640px;
}

.profile-form {
  display: grid;
  gap: 1rem;
}

.form-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 4px;
}

.mono {
  font-family: monospace;
}

.session-list {
  display: grid;
  gap: 8px;
}

.session-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-secondary);
}

.session-current {
  border-color: var(--primary-300);
  background: var(--primary-50);
}

[data-theme="dark"] .session-current {
  border-color: var(--primary-900);
} 
.session-info {
  min-width: 0;
}

.session-browser {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  display: flex;
  align-items: center;
}

.session-meta {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
}
</style>
