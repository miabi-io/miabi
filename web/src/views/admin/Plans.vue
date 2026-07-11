<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import type { Plan, PlanInput } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { usePagination } from '@/composables/usePagination'
import { useEntitlement } from '@/composables/useEntitlement'
import Pagination from '@/components/Pagination.vue'

const notify = useNotificationStore()
const router = useRouter()
const licenseStore = useLicenseStore()

// The restricted security profile is Enterprise-only.
const securityProfile = useEntitlement('security_profile')

// Plan-catalog cap: Community keeps the seeded plans (editable/deletable) but
// can't add more; an Enterprise license raises or lifts the limit. -1 = unlimited.
const planUsage = computed(() => licenseStore.view?.plan_usage)
const atPlanCap = computed(() => {
  const u = planUsage.value
  return !!u && u.limit >= 0 && u.used >= u.limit
})
const capTitle = computed(() =>
  atPlanCap.value
    ? `Your edition allows ${planUsage.value?.limit} plans — upgrade your license to add more`
    : '',
)

onMounted(() => { licenseStore.load() }) // idempotent; drives the plan-cap gate

const plans = ref<Plan[]>([])
const loading = ref(false)
const search = ref('')
let searchTimer: ReturnType<typeof setTimeout> | undefined

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listPlans(search.value.trim(), page, pageable.value.size)
    plans.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
})
function reload() { goToPage(pageable.value.current_page) }
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}
onBeforeUnmount(() => { if (searchTimer) clearTimeout(searchTimer) })

// The numeric limit dimensions, rendered as a grid in the form and a summary
// in the table. -1 = unlimited, 0 = none.
type LimitKey =
  | 'max_apps' | 'max_database_instances' | 'max_cron_jobs'
  | 'max_volumes' | 'max_networks' | 'max_api_keys' | 'max_members'
  | 'max_databases_per_instance' | 'max_cpu_cores' | 'max_memory_mb'
  | 'max_database_instance_size_mb' | 'max_storage_mb' | 'max_runners'
const limitFields: { key: LimitKey; label: string }[] = [
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
]
function fmtLimit(v: number): string {
  return v < 0 ? '∞' : String(v)
}

// --- Create / edit ---
const showForm = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const form = ref<PlanInput>(blank())

function blank(): PlanInput {
  return {
    name: '', description: '', is_default: false, is_active: true,
    max_apps: -1, max_database_instances: -1, max_cron_jobs: -1,
    max_volumes: -1, max_networks: -1, max_api_keys: -1, max_members: -1,
    max_databases_per_instance: -1, max_cpu_cores: -1, max_memory_mb: -1,
    max_database_instance_size_mb: -1, max_storage_mb: -1, max_runners: -1, max_gpus: 0,
    allow_custom_tls: true, allow_privileged_host_mounts: true, allow_shell_exec: true,
    allow_shared_storage: true, allow_dns_providers: true, allow_custom_labels: true,
    allow_platform_runners: false, allow_gpu: false,
    security_profile: 'default',
    allow_official_image_user: false,
  }
}
function openCreate() {
  editingId.value = null
  form.value = blank()
  showForm.value = true
}
function openDetail(p: Plan) {
  router.push(`/admin/plans/${p.id}`)
}
async function save() {
  if (!form.value.name.trim()) return
  saving.value = true
  try {
    const payload: PlanInput = { ...form.value, name: form.value.name.trim() }
    if (editingId.value) await adminApi.updatePlan(editingId.value, payload)
    else await adminApi.createPlan(payload)
    notify.success(editingId.value ? 'Plan updated' : 'Plan created')
    showForm.value = false
    reload()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function setDefault(p: Plan) {
  try {
    await adminApi.setDefaultPlan(p.id)
    notify.success(`"${p.name}" is now the default plan`)
    reload()
  } catch (e) {
    notify.apiError(e)
  }
}

// Editing and deleting a plan happen on the detail page (/admin/plans/:id).
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Plans</h1>
        <div class="text-muted text-sm">Per-workspace resource limits and capabilities. Enforced when <code>MIABI_PLAN_ENFORCEMENT</code> is enabled.</div>
      </div>
      <button class="btn btn-primary" :disabled="atPlanCap" :title="capTitle" @click="openCreate"><span class="mdi mdi-plus"></span> New plan</button>
    </div>

    <div v-if="atPlanCap" class="cap-note">
      <span class="mdi mdi-lock-outline"></span>
      <span>Your edition includes <strong>{{ planUsage?.limit }}</strong> platform plans. You can edit or delete them, but adding more requires an Enterprise license.</span>
      <router-link to="/admin/license" class="cap-link">Manage license →</router-link>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input v-model="search" class="form-input" type="search" placeholder="Search plans…" aria-label="Search plans" @input="onSearchInput" />
        </div>
        <span class="text-muted">{{ pageable.total_elements }} plan{{ pageable.total_elements === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && plans.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="plans.length === 0" class="empty-state">
        <span class="mdi mdi-tune-variant" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No plans {{ search.trim() ? 'found' : '' }}</h3>
        <p v-if="search.trim()">No plans match your search.</p>
        <p v-else>Create a plan to cap what a workspace can provision.</p>
        <button v-if="!search.trim()" class="btn btn-primary mt-4" :disabled="atPlanCap" :title="capTitle" @click="openCreate">New plan</button>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Name</th><th>Limits</th><th>Capabilities</th><th>Status</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="p in plans" :key="p.id" class="row-clickable" @click="openDetail(p)">
              <td>
                <div class="cell-text">
                  <span class="cell-title"><RouterLink :to="`/admin/plans/${p.id}`" @click.stop>{{ p.name }}</RouterLink><span v-if="p.is_default" class="badge badge-success" style="margin-left: 6px">default</span></span>
                  <span class="cell-sub">{{ p.description || '—' }}</span>
                </div>
              </td>
              <td class="cell-sub">
                <span v-for="f in limitFields.slice(0, 2)" :key="f.key" class="limit-pill" :title="f.label">{{ f.label }}: {{ fmtLimit(p[f.key]) }}</span>
                <span class="text-muted" style="font-size: 12px">+{{ limitFields.length - 2 }} more</span>
              </td>
              <td class="cell-sub">
                <span class="badge" :class="p.allow_custom_tls ? 'badge-success' : 'badge-neutral'">TLS</span>
                <span class="badge" :class="p.allow_privileged_host_mounts ? 'badge-success' : 'badge-neutral'" style="margin-left: 4px">Host mounts</span>
                <span class="badge" :class="p.allow_shell_exec ? 'badge-success' : 'badge-neutral'" style="margin-left: 4px">Shell</span>
                <span class="badge" :class="p.allow_shared_storage ? 'badge-success' : 'badge-neutral'" style="margin-left: 4px">Shared storage</span>
                <span class="badge" :class="p.allow_dns_providers ? 'badge-success' : 'badge-neutral'" style="margin-left: 4px">DNS</span>
                <span class="badge" :class="p.allow_custom_labels ? 'badge-success' : 'badge-neutral'" style="margin-left: 4px">Labels</span>
                <span v-if="p.security_profile === 'restricted'" class="badge badge-info" style="margin-left: 4px" title="Containers run as a non-root UID">Non-root</span>
              </td>
              <td>
                <span v-if="p.is_active" class="badge badge-dot badge-success">active</span>
                <span v-else class="badge badge-dot badge-danger">inactive</span>
              </td>
              <td class="text-right" @click.stop>
                <button v-if="!p.is_default" class="btn btn-sm btn-secondary" @click="setDefault(p)">Set as default</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <Teleport to="body">
      <div v-if="showForm" class="modal-overlay" @click.self="showForm = false">
        <div class="modal" style="max-width: 600px; width: 100%">
          <div class="modal-header">
            <h3>{{ editingId ? 'Edit plan' : 'New plan' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showForm = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-row">
                <div class="form-group">
                  <label class="form-label">Name</label>
                  <input v-model="form.name" class="form-input" placeholder="e.g. Pro" required autofocus />
                </div>
                <div class="form-group">
                  <label class="form-label">Description</label>
                  <input v-model="form.description" class="form-input" placeholder="optional" />
                </div>
              </div>

              <label class="form-label">Limits <span class="text-muted" style="font-weight: 400">(−1 = unlimited, 0 = none)</span></label>
              <div class="limit-grid">
                <div v-for="f in limitFields" :key="f.key" class="form-group" style="margin-bottom: 0">
                  <label class="form-label form-label-sm">{{ f.label }}</label>
                  <input v-model.number="form[f.key]" type="number" min="-1" class="form-input" />
                </div>
              </div>

              <label class="form-label" style="margin-top: 16px">Capabilities</label>
              <label class="checkbox-label"><input v-model="form.allow_custom_tls" type="checkbox" /> Allow custom TLS certificates</label>
              <label class="checkbox-label"><input v-model="form.allow_privileged_host_mounts" type="checkbox" /> Allow privileged host mounts</label>
              <label class="checkbox-label"><input v-model="form.allow_shell_exec" type="checkbox" /> Allow shell access into containers</label>
              <label class="checkbox-label"><input v-model="form.allow_shared_storage" type="checkbox" /> Allow shared storage (NFS / CIFS-SMB)</label>
              <label class="checkbox-label"><input v-model="form.allow_dns_providers" type="checkbox" /> Allow connecting DNS providers</label>
              <label class="checkbox-label"><input v-model="form.allow_custom_labels" type="checkbox" /> Allow custom container labels (Traefik &c.)</label>
              <label class="checkbox-label"><input v-model="form.allow_platform_runners" type="checkbox" /> Allow using the platform-shared runner pool</label>

              <label class="form-label" style="margin-top: 16px">
                Container security profile
                <span v-if="!securityProfile.has.value" class="badge badge-neutral" style="margin-left: 6px" title="The restricted profile requires an Enterprise license">
                  <span class="mdi mdi-lock-outline"></span> Enterprise
                </span>
              </label>
              <select v-model="form.security_profile" class="form-select" :disabled="!securityProfile.mutable.value">
                <option value="default">Default — image's user (may be root)</option>
                <option value="restricted">Restricted — force non-root UID</option>
              </select>
              <label class="checkbox-label" style="margin-top: 10px" :class="{ 'is-disabled': form.security_profile !== 'restricted' }">
                <input v-model="form.allow_official_image_user" type="checkbox" :disabled="form.security_profile !== 'restricted'" />
                Exempt official marketplace apps (keep the image's default user)
              </label>

              <div style="margin-top: 16px; display: flex; gap: 24px">
                <label class="checkbox-label"><input v-model="form.is_active" type="checkbox" /> Active</label>
                <label class="checkbox-label"><input v-model="form.is_default" type="checkbox" /> Default plan</label>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showForm = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editingId ? 'Save' : 'Create') }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }
code { background: var(--bg-tertiary); padding: 1px 6px; border-radius: 4px; font-size: 12px; font-family: monospace; }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.search { position: relative; flex: 1; max-width: 360px; }
.search .mdi { position: absolute; left: 10px; top: 50%; transform: translateY(-50%); color: var(--text-muted); pointer-events: none; }
.search .form-input { padding-left: 32px; }
.table-actions { display: flex; justify-content: flex-end; gap: 4px; }
.limit-pill { display: inline-block; padding: 2px 8px; margin: 2px 4px 2px 0; font-size: 12px; border-radius: 4px; background: var(--bg-tertiary); color: var(--text-secondary); }
.form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 16px; }
.limit-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }
.form-label-sm { font-size: 12px; }
.cap-note { display: flex; align-items: center; gap: 8px; margin-bottom: 16px; padding: 10px 14px; border-radius: 8px; font-size: 13px; background: var(--warning-bg, rgba(245, 158, 11, 0.1)); color: var(--warning, #b45309); }
.cap-note .mdi { font-size: 16px; }
.cap-link { margin-left: auto; font-weight: 600; white-space: nowrap; color: inherit; text-decoration: none; }
.checkbox-label { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--text-secondary); cursor: pointer; margin-top: 8px; }
</style>
