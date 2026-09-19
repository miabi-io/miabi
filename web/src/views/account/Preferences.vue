<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { storeToRefs } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { useWorkspaceStore } from '@/stores/workspace'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { ACCENTS as accents } from '@/theme/accents'
import { LANGUAGES as languages, resolveLanguage } from '@/i18n/languages'
import { useNotificationStore } from '@/stores/notification'
import { useLanguageStore } from '@/stores/language'
import { useI18n } from 'vue-i18n'
import { authApi } from '@/api/auth'

const auth = useAuthStore()
const language = useLanguageStore()
const { t } = useI18n()
const ws = useWorkspaceStore()
const theme = useThemeStore()
const notify = useNotificationStore()
const { workspaces } = storeToRefs(ws)

const saving = ref(false)
const defaultWorkspaceId = ref<number | null>(auth.user?.default_workspace_id ?? null)
const timezone = ref(auth.user?.preferences?.timezone || 'UTC')
const locale = ref(resolveLanguage(auth.user?.preferences?.locale))
const landingView = ref(auth.user?.preferences?.landing_view || 'dashboard')

const themeModes = computed<{ value: ThemeMode; label: string; icon: string }[]>(() => [
  { value: 'system', label: t('preferences.theme.system'), icon: 'mdi-monitor' },
  { value: 'light', label: t('preferences.theme.light'), icon: 'mdi-white-balance-sunny' },
  { value: 'dark', label: t('preferences.theme.dark'), icon: 'mdi-weather-night' },
])

// Each entry names a nav destination, so it reuses that label rather than keeping a
// second copy to translate.
const landingViews = computed(() => [
  { value: 'dashboard', label: t('nav.overview.dashboard') },
  { value: 'apps', label: t('nav.deploy.applications') },
  { value: 'databases', label: t('nav.data.databases') },
  { value: 'routes', label: t('nav.networking.routes') },
  { value: 'domains', label: t('nav.networking.domains') },
  { value: 'volumes', label: t('nav.data.volumes') },
  { value: 'jobs', label: t('nav.deploy.jobs') },
  { value: 'pipelines', label: t('nav.cicd.pipelines') },
  { value: 'monitoring', label: t('preferences.landing.monitoring') },
])

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
      locale: locale.value,
      landing_view: landingView.value,
    })).data.data
    if (auth.user) auth.setUser({ ...auth.user, preferences: prefs })
    // The save already persisted it, so this only switches the catalogue in place —
    // adopt() would push it back up a second time.
    await language.adopt(prefs.locale)
    notify.success(t('preferences.saved'))
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
        <h1>{{ t('preferences.title') }}</h1>
        <p class="page-sub">{{ t('preferences.subtitle') }}</p>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>{{ t('preferences.defaultWorkspace.title') }}</h2></div>
      <div class="card-body">
        <p class="note">
{{ t('preferences.defaultWorkspace.note') }}
        </p>
        <div class="form-group" style="margin-bottom: 0">
          <label class="form-label" for="default-ws">{{ t('preferences.defaultWorkspace.label') }}</label>
          <select
            id="default-ws"
            class="form-select"
            :value="defaultWorkspaceId == null ? '' : String(defaultWorkspaceId)"
            :disabled="saving"
            @change="saveDefaultWorkspace(($event.target as HTMLSelectElement).value)"
          >
            <option value="">{{ t('preferences.defaultWorkspace.none') }}</option>
            <option v-for="w in workspaces" :key="w.id" :value="String(w.id)">
              {{ w.display_name || w.name }}
            </option>
          </select>
        </div>
      </div>
    </div>

    <div class="card mb-4">
      <div class="card-header"><h2>{{ t('preferences.appearance.title') }}</h2></div>
      <div class="card-body">
        <p class="note">{{ t('preferences.appearance.note') }}</p>
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
      <div class="card-header"><h2>{{ t('preferences.display.title') }}</h2></div>
      <div class="card-body">
        <div class="form-group">
          <label class="form-label">{{ t('preferences.accent.label') }}</label>
          <div class="accent-grid">
            <button
              v-for="a in accents"
              :key="a.code"
              type="button"
              class="accent-option"
              :class="{ active: theme.accent === a.code }"
              :aria-pressed="theme.accent === a.code"
              :disabled="theme.accentLocked"
              @click="theme.setAccent(a.code)"
            >
              <span class="accent-swatch" :style="{ background: a.swatch }"></span>
              <span>{{ a.label }}</span>
            </button>
          </div>
          <p v-if="theme.accentLocked" class="form-hint">
            <span class="mdi mdi-lock-outline"></span> {{ t('preferences.accent.locked') }}
          </p>
          <p v-else class="form-hint">
{{ t('preferences.accent.hint') }}
          </p>
        </div>
        <div class="form-group">
          <label class="form-label" for="landing">{{ t('preferences.landing.label') }}</label>
          <select id="landing" v-model="landingView" class="form-select">
            <option v-for="v in landingViews" :key="v.value" :value="v.value">{{ v.label }}</option>
          </select>
          <p class="form-hint">{{ t('preferences.landing.hint') }}</p>
        </div>
        <div class="form-group">
          <label class="form-label" for="tz">{{ t('preferences.timezone.label') }}</label>
          <input id="tz" v-model="timezone" class="form-input mono" placeholder="UTC" />
          <p class="form-hint">
{{ t('preferences.timezone.hint') }}
            <template v-if="detectedTimezone && detectedTimezone !== timezone">
              This browser reports
              <a href="#" @click.prevent="timezone = detectedTimezone"><code>{{ detectedTimezone }}</code></a>.
            </template>
          </p>
        </div>
        <div class="form-group" style="margin-bottom: 0">
          <label class="form-label" for="locale">{{ t('preferences.language.label') }}</label>
          <select id="locale" v-model="locale" class="form-select">
            <option v-for="l in languages" :key="l.code" :value="l.code" :lang="l.code">{{ l.label }}</option>
          </select>
          <p class="form-hint">{{ t('preferences.language.hint') }}</p>
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
.accent-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 10px; }
.accent-option {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 12px; border: 1px solid var(--border-primary); border-radius: 8px;
  background: var(--bg-primary); color: var(--text-primary); cursor: pointer;
  font-size: 13px; text-align: left;
}
.accent-option:not(:disabled):hover { border-color: var(--border-input); }
.accent-option.active { border-color: var(--primary-500); box-shadow: var(--shadow-focus); }
.accent-option:disabled { cursor: default; }
.accent-option:disabled:not(.active) { opacity: 0.5; }
.accent-swatch { width: 18px; height: 18px; border-radius: 50%; flex: none; border: 1px solid rgba(0, 0, 0, 0.12); }
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
