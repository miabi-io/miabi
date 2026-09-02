<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { storeToRefs } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { useNotificationStore } from '@/stores/notification'
import { authApi } from '@/api/auth'

const auth = useAuthStore()
const ws = useWorkspaceStore()
const theme = useThemeStore()
const notify = useNotificationStore()
const { workspaces } = storeToRefs(ws)

const saving = ref(false)
const defaultWorkspaceId = ref<number | null>(auth.user?.default_workspace_id ?? null)
const timezone = ref(auth.user?.preferences?.timezone || 'UTC')
const locale = ref(auth.user?.preferences?.locale || 'en')
const landingView = ref(auth.user?.preferences?.landing_view || 'dashboard')

const themeModes: { value: ThemeMode; label: string; icon: string }[] = [
  { value: 'system', label: 'Match system', icon: 'mdi-monitor' },
  { value: 'light', label: 'Light', icon: 'mdi-white-balance-sunny' },
  { value: 'dark', label: 'Dark', icon: 'mdi-weather-night' },
]

const landingViews = [
  { value: 'dashboard', label: 'Dashboard' },
  { value: 'apps', label: 'Applications' },
  { value: 'databases', label: 'Databases' },
  { value: 'routes', label: 'Routes' },
  { value: 'domains', label: 'Domains' },
  { value: 'volumes', label: 'Volumes' },
  { value: 'jobs', label: 'Jobs' },
  { value: 'pipelines', label: 'Pipelines' },
  { value: 'monitoring', label: 'Monitoring' },
]

// The browser's own zone, offered as the obvious choice rather than making people
// recall an IANA name.
const detectedTimezone = computed(() => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || '' } catch { return '' }
})

onMounted(async () => {
  if (workspaces.value.length === 0) {
    try { await ws.fetchWorkspaces() } catch { /* the layout reports it */ }
  }
})

async function saveDefaultWorkspace(raw: string) {
  const id = raw === '' ? null : Number(raw)
  saving.value = true
  try {
    await authApi.setDefaultWorkspace(id)
    defaultWorkspaceId.value = id
    if (auth.user) auth.setUser({ ...auth.user, default_workspace_id: id ?? undefined })
    notify.success(id ? 'Default workspace saved' : 'Default workspace cleared')
  } catch (e) {
    notify.apiError(e)
    defaultWorkspaceId.value = auth.user?.default_workspace_id ?? null
  } finally {
    saving.value = false
  }
}

function setTheme(m: ThemeMode) {
  // The store applies it immediately and persists it against the account.
  theme.setMode(m)
}

async function saveDisplay() {
  saving.value = true
  try {
    const prefs = (await authApi.updatePreferences({
      timezone: timezone.value.trim() || 'UTC',
      locale: locale.value.trim() || 'en',
      landing_view: landingView.value,
    })).data.data
    if (auth.user) auth.setUser({ ...auth.user, preferences: prefs })
    notify.success('Preferences saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Preferences</h1>
        <p class="page-sub">Settings that follow your account, on every browser and machine.</p>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Default workspace</h2></div>
      <div class="card-body">
        <p class="note">
          Where a new sign-in lands when you have no workspace in mind — a fresh browser, or a new
          machine's CLI. Switching workspaces day to day does not change this.
        </p>
        <div class="form-group" style="margin-bottom: 0">
          <label class="form-label" for="default-ws">Workspace</label>
          <select
            id="default-ws"
            class="form-input"
            :value="defaultWorkspaceId == null ? '' : String(defaultWorkspaceId)"
            :disabled="saving"
            @change="saveDefaultWorkspace(($event.target as HTMLSelectElement).value)"
          >
            <option value="">No preference — use my oldest workspace</option>
            <option v-for="w in workspaces" :key="w.id" :value="String(w.id)">
              {{ w.display_name || w.name }}
            </option>
          </select>
        </div>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>Appearance</h2></div>
      <div class="card-body">
        <p class="note">Applied immediately and saved to your account.</p>
        <div class="theme-grid">
          <button
            v-for="m in themeModes"
            :key="m.value"
            type="button"
            class="theme-option"
            :class="{ active: theme.mode === m.value }"
            @click="setTheme(m.value)"
          >
            <span class="mdi" :class="m.icon"></span>
            <span>{{ m.label }}</span>
          </button>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>Display</h2></div>
      <div class="card-body">
        <div class="form-group">
          <label class="form-label" for="landing">Open on</label>
          <select id="landing" v-model="landingView" class="form-input">
            <option v-for="v in landingViews" :key="v.value" :value="v.value">{{ v.label }}</option>
          </select>
          <p class="form-hint">The section a new session opens on inside your default workspace.</p>
        </div>
        <div class="form-group">
          <label class="form-label" for="tz">Time zone</label>
          <input id="tz" v-model="timezone" class="form-input mono" placeholder="UTC" />
          <p class="form-hint">
            Affects how times are displayed; they are always stored in UTC.
            <template v-if="detectedTimezone && detectedTimezone !== timezone">
              This browser reports
              <a href="#" @click.prevent="timezone = detectedTimezone"><code>{{ detectedTimezone }}</code></a>.
            </template>
          </p>
        </div>
        <div class="form-group" style="margin-bottom: 0">
          <label class="form-label" for="locale">Language</label>
          <input id="locale" v-model="locale" class="form-input mono" placeholder="en" />
          <p class="form-hint">A BCP&nbsp;47 tag such as <code>en</code> or <code>fr-CA</code>, used for date and number formatting.</p>
        </div>
      </div>
      <div class="card-footer">
        <button class="btn btn-primary" :disabled="saving" @click="saveDisplay">
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-sub { color: var(--text-muted); font-size: 13px; margin: 4px 0 0; }
.note { font-size: 13px; color: var(--text-muted); margin-bottom: 12px; }
.form-hint { font-size: 12px; color: var(--text-muted); margin-top: 4px; }
.form-hint code { font-family: 'JetBrains Mono', monospace; background: var(--bg-tertiary); padding: 1px 5px; border-radius: 4px; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 13px; }
.theme-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; }
.theme-option {
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  padding: 16px 12px; border: 1px solid var(--border-primary); border-radius: 8px;
  background: var(--bg-secondary); color: var(--text-primary); cursor: pointer; font-size: 13px;
}
.theme-option .mdi { font-size: 22px; color: var(--text-muted); }
.theme-option:hover { border-color: var(--primary-500); }
.theme-option.active { border-color: var(--primary-500); background: var(--primary-50); }
.theme-option.active .mdi { color: var(--primary-500); }
.card-footer { padding: 12px 16px; border-top: 1px solid var(--border-primary); display: flex; justify-content: flex-end; }
</style>
