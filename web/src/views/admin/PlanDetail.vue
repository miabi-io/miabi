<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import type { Plan, PlanInput } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

// The restricted security profile is an Enterprise-only policy; in Community the
// profile stays default.
const securityProfile = useEntitlement('security_profile')

const planId = computed(() => Number(route.params.id))
const plan = ref<Plan | null>(null)
const form = ref<PlanInput | null>(null)
const loading = ref(false)
const saving = ref(false)

type LimitKey = keyof Pick<PlanInput,
  | 'max_apps' | 'max_database_instances' | 'max_databases_per_instance' | 'max_cron_jobs'
  | 'max_volumes' | 'max_networks' | 'max_api_keys' | 'max_members' | 'max_runners' | 'max_cpu_cores' | 'max_memory_mb'
  | 'max_database_instance_size_mb' | 'max_storage_mb' | 'max_gpus'>

interface LimitField { key: LimitKey; label: string; desc: string; unit?: string }
const countFields: LimitField[] = [
  { key: 'max_apps', label: 'Applications', desc: 'Deployable apps in the workspace.' },
  { key: 'max_database_instances', label: 'Database instances', desc: 'Provisioned database servers.' },
  { key: 'max_databases_per_instance', label: 'Databases per instance', desc: 'Logical databases inside one instance.' },
  { key: 'max_cron_jobs', label: 'Cron jobs', desc: 'Scheduled jobs.' },
  { key: 'max_volumes', label: 'Volumes', desc: 'Managed persistent volumes.' },
  { key: 'max_networks', label: 'Networks', desc: 'Custom Docker networks.' },
  { key: 'max_api_keys', label: 'API keys', desc: 'Workspace-scoped API keys.' },
  { key: 'max_members', label: 'Members', desc: 'Workspace members (owner + invited).' },
  { key: 'max_runners', label: 'Runners', desc: 'Build/pipeline runners the workspace may register.' },
]
const computeFields: LimitField[] = [
  { key: 'max_cpu_cores', label: 'CPU', desc: 'Aggregate CPU across all apps.', unit: 'cores' },
  { key: 'max_memory_mb', label: 'Memory', desc: 'Aggregate memory across all apps.', unit: 'MB' },
  { key: 'max_database_instance_size_mb', label: 'DB instance size', desc: 'Declared data-volume size of one instance.', unit: 'MB' },
  { key: 'max_storage_mb', label: 'Total storage', desc: 'Aggregate volumes + DB instance data volumes.', unit: 'MB' },
  { key: 'max_gpus', label: 'GPUs', desc: 'Aggregate GPU units the workspace’s running apps may hold. Requires the GPU capability below.', unit: 'GPUs' },
]

function describe(v: number, unit?: string): string {
  if (v < 0) return 'Unlimited'
  if (v === 0) return 'None'
  return unit ? `${v} ${unit}` : String(v)
}

async function load() {
  loading.value = true
  try {
    plan.value = (await adminApi.getPlan(planId.value)).data.data
    const { id, created_at, updated_at, ...rest } = plan.value
    void id; void created_at; void updated_at
    form.value = { ...rest }
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/plans')
  } finally {
    loading.value = false
  }
}
watch(planId, load, { immediate: true })

async function save() {
  if (!form.value || !plan.value || !form.value.name.trim()) return
  saving.value = true
  try {
    plan.value = (await adminApi.updatePlan(plan.value.id, { ...form.value, name: form.value.name.trim() })).data.data
    notify.success('Plan saved')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function makeDefault() {
  if (!plan.value) return
  try {
    plan.value = (await adminApi.setDefaultPlan(plan.value.id)).data.data
    if (form.value) form.value.is_default = true
    notify.success(`"${plan.value.name}" is now the default plan`)
  } catch (e) {
    notify.apiError(e)
  }
}

const showDelete = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  if (!plan.value) return
  deleting.value = true
  try {
    await adminApi.deletePlan(plan.value.id, true)
    notify.success('Plan deleted')
    router.replace('/admin/plans')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

function fmtDate(s?: string): string {
  return s ? new Date(s).toLocaleString() : '—'
}
</script>

<template>
  <div>
    <div v-if="loading && !plan" class="loading-page"><span class="spinner"></span></div>

    <template v-else-if="plan && form">
      <div class="page-header">
        <div class="header-left">
          <button class="btn-icon btn-icon-muted" title="Back to plans" aria-label="Back to plans" @click="router.push('/admin/plans')">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ plan.name }}
              <span v-if="plan.is_default" class="badge badge-success">default</span>
              <span v-if="plan.is_active" class="badge badge-dot badge-success">active</span>
              <span v-else class="badge badge-dot badge-danger">inactive</span>
            </h1>
            <span class="subline">{{ plan.description || 'No description' }}</span>
          </div>
        </div>
        <div class="header-actions">
          <button v-if="!plan.is_default" class="btn btn-secondary" @click="makeDefault">Set as default</button>
          <button class="btn btn-danger" @click="showDelete = true">Delete</button>
        </div>
      </div>

      <!-- General -->
      <div class="card">
        <div class="card-header"><h2>General</h2></div>
        <div class="card-body">
          <div class="form-row">
            <div class="form-group">
              <label class="form-label">Name</label>
              <input v-model="form.name" class="form-input" required />
            </div>
            <div class="form-group">
              <label class="form-label">Description</label>
              <input v-model="form.description" class="form-input" placeholder="optional" />
            </div>
          </div>
          <div class="toggles">
            <label class="checkbox-label"><input v-model="form.is_active" type="checkbox" /> Active <span class="text-muted">(assignable to workspaces)</span></label>
            <label class="checkbox-label"><input v-model="form.is_default" type="checkbox" /> Default plan <span class="text-muted">(applied to unassigned workspaces)</span></label>
          </div>
        </div>
      </div>

      <!-- Resource limits -->
      <div class="card mt-4">
        <div class="card-header">
          <h2>Resource limits</h2>
          <span class="text-muted text-sm">−1 = unlimited · 0 = none</span>
        </div>
        <div class="card-body">
          <h3 class="section-label">Counts</h3>
          <div class="limit-grid">
            <div v-for="f in countFields" :key="f.key" class="limit-cell">
              <label class="form-label">{{ f.label }}</label>
              <input v-model.number="form[f.key]" type="number" min="-1" class="form-input" />
              <p class="form-hint">{{ f.desc }} <strong>{{ describe(form[f.key], f.unit) }}</strong></p>
            </div>
          </div>

          <h3 class="section-label" style="margin-top: 24px">Compute &amp; storage</h3>
          <div class="limit-grid">
            <div v-for="f in computeFields" :key="f.key" class="limit-cell">
              <label class="form-label">{{ f.label }} <span v-if="f.unit" class="text-muted">({{ f.unit }})</span></label>
              <input v-model.number="form[f.key]" type="number" min="-1" class="form-input" />
              <p class="form-hint">{{ f.desc }} <strong>{{ describe(form[f.key], f.unit) }}</strong></p>
            </div>
          </div>
        </div>
      </div>

      <!-- Capabilities -->
      <div class="card mt-4">
        <div class="card-header"><h2>Capabilities</h2></div>
        <div class="card-body">
          <label class="checkbox-label"><input v-model="form.allow_custom_tls" type="checkbox" /> Allow custom TLS certificates</label>
          <label class="checkbox-label"><input v-model="form.allow_privileged_host_mounts" type="checkbox" /> Allow privileged host mounts</label>
          <label class="checkbox-label"><input v-model="form.allow_shell_exec" type="checkbox" /> Allow shell access into containers</label>
          <label class="checkbox-label"><input v-model="form.allow_shared_storage" type="checkbox" /> Allow shared storage (NFS / CIFS-SMB)</label>
          <label class="checkbox-label"><input v-model="form.allow_dns_providers" type="checkbox" /> Allow connecting DNS providers</label>
          <label class="checkbox-label"><input v-model="form.allow_custom_labels" type="checkbox" /> Allow custom container labels (Traefik &c.)</label>
          <label class="checkbox-label"><input v-model="form.allow_platform_runners" type="checkbox" /> Allow using the platform-shared runner pool</label>
          <label class="checkbox-label"><input v-model="form.allow_gpu" type="checkbox" /> Allow GPU access (device passthrough) — set the GPUs limit above too</label>
          <div class="form-group" style="margin-top: 12px; max-width: 360px">
            <label class="form-label">
              Container security profile
              <span v-if="!securityProfile.has.value" class="badge badge-neutral" style="margin-left: 6px" title="The restricted profile requires an Enterprise license">
                <span class="mdi mdi-lock-outline"></span> Enterprise
              </span>
            </label>
            <select v-model="form.security_profile" class="form-select" :disabled="!securityProfile.mutable.value">
              <option value="default">Default — image's user (may be root)</option>
              <option value="restricted">Restricted — force non-root UID</option>
            </select>
            <p class="form-hint">Restricted runs application &amp; job containers as a non-root platform UID (like OpenShift's restricted SCC). May break images that require root.<template v-if="!securityProfile.has.value"> Requires an Enterprise license; Community always uses Default.</template></p>
            <label class="checkbox-label" style="margin-top: 10px" :class="{ 'is-disabled': form.security_profile !== 'restricted' }">
              <input v-model="form.allow_official_image_user" type="checkbox" :disabled="form.security_profile !== 'restricted'" />
              Exempt official marketplace apps (keep the image's default user)
            </label>
            <p class="form-hint">When Restricted, apps installed from an <strong>official</strong> marketplace template still run as the image's own user, so curated images that need it aren't broken. Only official installs qualify; the user's own apps stay non-root.</p>
          </div>
        </div>
      </div>

      <!-- Metadata -->
      <div class="card mt-4">
        <div class="card-header"><h2>Details</h2></div>
        <div class="card-body details">
          <div class="detail"><span class="text-muted">Plan ID</span><span><code>{{ plan.id }}</code></span></div>
          <div class="detail"><span class="text-muted">Created</span><span>{{ fmtDate(plan.created_at) }}</span></div>
          <div class="detail"><span class="text-muted">Updated</span><span>{{ fmtDate(plan.updated_at) }}</span></div>
        </div>
      </div>

      <div class="save-bar">
        <button class="btn btn-secondary" @click="router.push('/admin/plans')">Cancel</button>
        <button class="btn btn-primary" :disabled="saving || !form.name.trim()" @click="save">{{ saving ? 'Saving…' : 'Save changes' }}</button>
      </div>
    </template>

    <ConfirmDialog
      :open="showDelete"
      title="Delete plan"
      :message="`Delete plan &quot;${plan?.name}&quot;? Any workspaces using it will fall back to the default plan.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="showDelete = false"
    />
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
.text-sm { font-size: 13px; }
code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; font-family: monospace; }
.page-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-title h1 { display: flex; align-items: center; gap: 8px; }
.subline { font-size: 13px; color: var(--text-muted); }
.header-actions { display: flex; gap: 8px; }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 18px; }
.toggles { display: flex; flex-direction: column; gap: 10px; }
.section-label { font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); margin-bottom: 12px; }
.limit-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 18px; }
.limit-cell .form-hint { margin-top: 4px; font-size: 12px; color: var(--text-muted); line-height: 1.4; }
.checkbox-label { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--text-secondary); cursor: pointer; }
.details { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.detail { display: flex; flex-direction: column; gap: 2px; font-size: 14px; }
.save-bar { display: flex; justify-content: flex-end; gap: 10px; margin-top: 24px; position: sticky; bottom: 0; padding: 16px 0; background: var(--bg-secondary); }
</style>
