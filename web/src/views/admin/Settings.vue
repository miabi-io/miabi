<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminApi } from '@/api/admin'
import type { SettingInput } from '@/api/admin'
import type { PlatformSetting } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'

const notify = useNotificationStore()

const loading = ref(false)
const saving = ref(false)

// Read-only encryption posture (per-workspace keys + auto-rotation + gateway config encryption).
const encryption = ref<{ encryption_enabled: boolean; per_workspace_keys: boolean; auto_rotate: boolean; rotate_months: number; gateway_config_encryption: boolean } | null>(null)

// Edited values, keyed by setting key.
const values = ref<Record<string, string>>({})
const types = ref<Record<string, PlatformSetting['type']>>({})
// Originally-loaded values, for dirty comparison.
const original = ref<Record<string, string>>({})
// API key order, preserved for the generic "Other" section.
const keys = ref<string[]>([])

interface SectionDef {
  id: string
  title: string
  keys: string[]
  labels: Record<string, string>
}

const SECTIONS: SectionDef[] = [
  {
    id: 'access',
    title: 'Access',
    keys: ['allowed_signup_domains'],
    labels: {
      allowed_signup_domains: 'Allowed signup domains (CSV)',
    },
  },
  {
    id: 'platform',
    title: 'Platform',
    keys: ['maintenance_mode', 'require_email_verification', 'default_workspace_role', 'custom_labels_enabled'],
    labels: {
      maintenance_mode: 'Maintenance mode',
      require_email_verification: 'Require email verification',
      default_workspace_role: 'Default workspace role',
      custom_labels_enabled: 'Allow custom container labels (Traefik &c.) — fleet-wide kill-switch',
    },
  },
  {
    id: 'limits',
    title: 'Limits & retention',
    keys: ['max_workspaces_per_user', 'max_workspace_memberships_per_user', 'audit_log_retention_days'],
    labels: {
      max_workspaces_per_user: 'Max workspaces per user — owned (0 = unlimited)',
      max_workspace_memberships_per_user: 'Max workspaces per user — joined as member (0 = unlimited)',
      audit_log_retention_days: 'Audit log retention (days)',
    },
  },
  {
    id: 'resources',
    title: 'Resource limits',
    keys: ['max_cpu_cores', 'max_memory_mb'],
    labels: {
      max_cpu_cores: 'Max CPU cores per app (0 = unlimited)',
      max_memory_mb: 'Max memory per app, MB (0 = unlimited)',
    },
  },
  {
    id: 'external-access',
    title: 'External access',
    keys: ['external_base_domain', 'external_base_provider'],
    labels: {
      external_base_domain: 'External base domain (wildcard, e.g. apps.example.com — point *.<domain> at the gateway)',
      external_base_provider: 'Cert provider for generated routes (blank = gateway default)',
    },
  },
]

// System-managed keys: shown read-only, never editable, and excluded from saves.
const READONLY_KEYS = new Set(['install_id'])
const READONLY_LABELS: Record<string, string> = { install_id: 'Install ID' }

// All keys explicitly placed in a known section.
const knownKeys = computed(() => new Set(SECTIONS.flatMap((s) => s.keys)))

// Keys present in the loaded data but not in any known section (excluding read-only).
const otherKeys = computed(() => keys.value.filter((k) => !knownKeys.value.has(k) && !READONLY_KEYS.has(k)))

// Read-only, system-managed settings surfaced for reference (e.g. install_id).
const readonlyItems = computed(() =>
  keys.value
    .filter((k) => READONLY_KEYS.has(k))
    .map((k) => ({ key: k, label: READONLY_LABELS[k] ?? k, value: values.value[k] ?? '' }))
)

// Sections that actually have at least one loaded setting.
const visibleSections = computed(() =>
  SECTIONS.map((s) => ({
    ...s,
    keys: s.keys.filter((k) => keys.value.includes(k)),
  })).filter((s) => s.keys.length > 0)
)

function friendlyLabel(section: SectionDef | null, key: string): string {
  return section?.labels[key] ?? key
}

const dirty = computed(() =>
  keys.value.some((k) => values.value[k] !== original.value[k])
)

function sectionDirty(sectionKeys: string[]): boolean {
  return sectionKeys.some((k) => values.value[k] !== original.value[k])
}

function applySettings(settings: PlatformSetting[]) {
  const nextValues: Record<string, string> = {}
  const nextTypes: Record<string, PlatformSetting['type']> = {}
  const nextKeys: string[] = []
  for (const s of settings) {
    nextValues[s.key] = s.value ?? ''
    nextTypes[s.key] = s.type
    nextKeys.push(s.key)
  }
  values.value = nextValues
  types.value = nextTypes
  original.value = { ...nextValues }
  keys.value = nextKeys
}

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listSettings()
    applySettings(res.data.data ?? [])
    encryption.value = (await adminApi.getEncryptionInfo()).data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function save() {
  if (!dirty.value || saving.value) return
  saving.value = true
  try {
    const payload: SettingInput[] = keys.value
      .filter((key) => !READONLY_KEYS.has(key))
      .map((key) => ({
        key,
        value: values.value[key] ?? '',
        type: types.value[key] ?? 'string',
      }))
    const res = await adminApi.updateSettings(payload)
    applySettings(res.data.data ?? [])
    notify.success('Settings saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

function boolValue(key: string): boolean {
  return values.value[key] === 'true'
}

function setBool(key: string, checked: boolean) {
  values.value[key] = checked ? 'true' : 'false'
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Platform Settings</h1>
      <button class="btn btn-primary" :disabled="!dirty || saving" @click="save">
        <span class="mdi mdi-content-save"></span>
        {{ saving ? 'Saving…' : 'Save changes' }}
      </button>
    </div>

    <div v-if="loading" class="spinner"></div>

    <template v-else>
      <!-- Encryption posture (read-only; operator-configured via env) -->
      <div v-if="encryption" class="card">
        <div class="card-body">
          <h2 class="card-title">Encryption</h2>
          <p class="text-muted text-sm" style="margin-bottom: 12px">
            Secrets are encrypted at rest. Per-workspace keys, auto-rotation, and gateway config
            encryption are configured via environment variables; rotate a workspace's key on demand
            from its admin page.
          </p>
          <div class="enc-grid">
            <span class="text-muted">Encryption</span>
            <span class="badge" :class="encryption.encryption_enabled ? 'badge-success' : 'badge-danger'">{{ encryption.encryption_enabled ? 'enabled' : 'disabled (no key)' }}</span>
            <span class="text-muted">Per-workspace keys</span>
            <span class="badge" :class="encryption.per_workspace_keys ? 'badge-success' : 'badge-neutral'">{{ encryption.per_workspace_keys ? 'on' : 'off' }}</span>
            <span class="text-muted">Auto-rotation</span>
            <span class="badge" :class="encryption.auto_rotate ? 'badge-success' : 'badge-neutral'">{{ encryption.auto_rotate ? `every ${encryption.rotate_months} months` : 'off' }}</span>
            <span class="text-muted" title="Encryption of the config Miabi sends to Goma Gateway (middleware rules &amp; TLS), via GOMA_CONFIG_ENCRYPTION_KEY">Gateway config encryption</span>
            <span class="badge" :class="encryption.gateway_config_encryption ? 'badge-success' : 'badge-neutral'">{{ encryption.gateway_config_encryption ? 'enabled' : 'disabled (no key)' }}</span>
          </div>
        </div>
      </div>

      <!-- System-managed, read-only (e.g. install_id) -->
      <div v-if="readonlyItems.length" class="card">
        <div class="card-body">
          <div class="section-title">Deployment</div>
          <div v-for="item in readonlyItems" :key="item.key" class="setting-row">
            <div class="setting-label">
              <label class="form-label">{{ item.label }}</label>
              <div class="form-hint text-muted">{{ item.key }} · read-only</div>
            </div>
            <div class="setting-control">
              <input class="form-input mono" :value="item.value" readonly disabled />
            </div>
          </div>
        </div>
      </div>

      <!-- Known sections -->
      <div v-for="section in visibleSections" :key="section.id" class="card">
        <div class="card-body">
          <div class="section-title">
            {{ section.title }}
            <span v-if="sectionDirty(section.keys)" class="text-muted unsaved">unsaved</span>
          </div>

          <div v-for="key in section.keys" :key="key" class="setting-row">
            <div class="setting-label">
              <label class="form-label" :for="`set-${key}`">{{ friendlyLabel(section, key) }}</label>
              <div class="form-hint text-muted">{{ key }}</div>
            </div>
            <div class="setting-control">
              <label v-if="types[key] === 'bool'" class="switch">
                <input
                  :id="`set-${key}`"
                  type="checkbox"
                  :checked="boolValue(key)"
                  @change="setBool(key, ($event.target as HTMLInputElement).checked)"
                />
              </label>
              <input
                v-else-if="types[key] === 'int'"
                :id="`set-${key}`"
                v-model="values[key]"
                type="number"
                class="form-input"
              />
              <input
                v-else
                :id="`set-${key}`"
                v-model="values[key]"
                type="text"
                class="form-input"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- Other / unknown settings -->
      <div v-if="otherKeys.length" class="card">
        <div class="card-body">
          <div class="section-title">
            Other
            <span v-if="sectionDirty(otherKeys)" class="text-muted unsaved">unsaved</span>
          </div>

          <div v-for="key in otherKeys" :key="key" class="setting-row">
            <div class="setting-label">
              <label class="form-label" :for="`set-${key}`">{{ key }}</label>
              <div class="form-hint text-muted">{{ key }}</div>
            </div>
            <div class="setting-control">
              <label v-if="types[key] === 'bool'" class="switch">
                <input
                  :id="`set-${key}`"
                  type="checkbox"
                  :checked="boolValue(key)"
                  @change="setBool(key, ($event.target as HTMLInputElement).checked)"
                />
              </label>
              <input
                v-else-if="types[key] === 'int'"
                :id="`set-${key}`"
                v-model="values[key]"
                type="number"
                class="form-input"
              />
              <input
                v-else
                :id="`set-${key}`"
                v-model="values[key]"
                type="text"
                class="form-input"
              />
            </div>
          </div>
        </div>
      </div>

      <div v-if="!keys.length" class="card">
        <div class="card-body text-muted">No platform settings available.</div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.card-title { font-size: 15px; font-weight: 600; margin: 0 0 4px; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
.enc-grid { display: grid; grid-template-columns: max-content max-content; gap: 8px 16px; align-items: center; }
.section-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-secondary, var(--text-muted));
  margin-bottom: 12px;
}

.unsaved {
  font-size: 10px;
  font-weight: 500;
  text-transform: none;
  letter-spacing: 0;
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-primary);
}

.setting-row:last-child {
  border-bottom: none;
  padding-bottom: 0;
}

.setting-label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.setting-label .form-label {
  margin-bottom: 0;
}

.setting-control {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  min-width: 200px;
  max-width: 280px;
}

.setting-control .form-input {
  width: 100%;
}

.switch {
  display: inline-flex;
  align-items: center;
}

.switch input[type='checkbox'] {
  width: 18px;
  height: 18px;
  cursor: pointer;
}
</style>
