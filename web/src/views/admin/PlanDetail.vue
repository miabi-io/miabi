<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { clustersApi } from '@/api/clusters'
import { adminRunnerApi, type Runner } from '@/api/runners'
import type { Cluster, DatabaseSize, Plan, PlanInput, StorageClass } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const notify = useNotificationStore()

// The restricted security profile is an Enterprise-only policy; in Community the
// profile stays default.
const securityProfile = useEntitlement('security_profile')
const placementPolicy = useEntitlement('placement_policy')
const clusters = ref<Cluster[]>([])
clustersApi.list().then((r) => { clusters.value = r.data.data ?? [] }).catch(() => { clusters.value = [] })
const databaseSizesPolicy = useEntitlement('database_sizes')
const databaseSizes = ref<DatabaseSize[]>([])
adminApi.listDatabaseSizes().then((r) => { databaseSizes.value = r.data.data ?? [] }).catch(() => { databaseSizes.value = [] })
const storageClasses = ref<StorageClass[]>([])
adminApi.listStorageClasses().then((r) => { storageClasses.value = r.data.data ?? [] }).catch(() => { storageClasses.value = [] })
// Which shared runners a plan offers. The pool is the platform's to lend; a workspace's own
// runners are never bound by its plan, so only shared runners are listed here.
const platformRunnersPolicy = useEntitlement('platform_runners')
const sharedRunners = ref<Runner[]>([])
adminRunnerApi.list().then((r) => { sharedRunners.value = r.data.data ?? [] }).catch(() => { sharedRunners.value = [] })

const planId = computed(() => Number(route.params.id))
const plan = ref<Plan | null>(null)
const form = ref<PlanInput | null>(null)
const loading = ref(false)
const saving = ref(false)

type LimitKey = keyof Pick<PlanInput,
  | 'max_apps' | 'max_database_instances' | 'max_databases_per_instance' | 'max_cron_jobs'
  | 'max_volumes' | 'max_networks' | 'max_api_keys' | 'max_members' | 'max_runners' | 'max_cpu_cores' | 'max_memory_mb'
  | 'max_database_instance_size_mb' | 'max_storage_mb' | 'max_gpus' | 'max_database_cpu_cores' | 'max_database_memory_mb'>

interface LimitField { key: LimitKey; label: string; desc: string; unit?: string }
const countFields: LimitField[] = [
  { key: 'max_apps', label: 'planDetail.field.apps', desc: 'planDetail.field.appsDesc' },
  { key: 'max_database_instances', label: 'planDetail.field.dbInstances', desc: 'planDetail.field.dbInstancesDesc' },
  { key: 'max_databases_per_instance', label: 'planDetail.field.dbsPerInstance', desc: 'planDetail.field.dbsPerInstanceDesc' },
  { key: 'max_cron_jobs', label: 'planDetail.field.cronJobs', desc: 'planDetail.field.cronJobsDesc' },
  { key: 'max_volumes', label: 'planDetail.field.volumes', desc: 'planDetail.field.volumesDesc' },
  { key: 'max_networks', label: 'planDetail.field.networks', desc: 'planDetail.field.networksDesc' },
  { key: 'max_api_keys', label: 'planDetail.field.apiKeys', desc: 'planDetail.field.apiKeysDesc' },
  { key: 'max_members', label: 'planDetail.field.members', desc: 'planDetail.field.membersDesc' },
  { key: 'max_runners', label: 'planDetail.field.runners', desc: 'planDetail.field.runnersDesc' },
]
const computeFields: LimitField[] = [
  { key: 'max_cpu_cores', label: 'planDetail.field.cpu', desc: 'planDetail.field.cpuDesc', unit: 'planDetail.unit.cores' },
  { key: 'max_memory_mb', label: 'planDetail.field.memory', desc: 'planDetail.field.memoryDesc', unit: 'planDetail.unit.mb' },
  { key: 'max_database_cpu_cores', label: 'planDetail.field.dbCpu', desc: 'planDetail.field.dbCpuDesc', unit: 'planDetail.unit.cores' },
  { key: 'max_database_memory_mb', label: 'planDetail.field.dbMemory', desc: 'planDetail.field.dbMemoryDesc', unit: 'planDetail.unit.mb' },
  { key: 'max_database_instance_size_mb', label: 'planDetail.field.dbInstanceSize', desc: 'planDetail.field.dbInstanceSizeDesc', unit: 'planDetail.unit.mb' },
  { key: 'max_storage_mb', label: 'planDetail.field.storage', desc: 'planDetail.field.storageDesc', unit: 'planDetail.unit.mb' },
  { key: 'max_gpus', label: 'planDetail.field.gpus', desc: 'planDetail.field.gpusDesc', unit: 'planDetail.unit.gpus' },
]

function describe(v: number, unit?: string): string {
  if (v < 0) return t('planDetail.unlimited')
  if (v === 0) return t('planDetail.none')
  return unit ? `${v} ${t(unit)}` : String(v)
}

async function load() {
  loading.value = true
  try {
    plan.value = (await adminApi.getPlan(planId.value)).data.data
    const { id, created_at, updated_at, ...rest } = plan.value
    void id; void created_at; void updated_at
    // The API reads a null default as "keep what is stored", so the select must always hand back a
    // concrete string — otherwise clearing it back to the node's own default would be impossible.
    form.value = { ...rest, default_storage_class: rest.default_storage_class ?? '' }
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/plans')
  } finally {
    loading.value = false
  }
}
watch(planId, load, { immediate: true })

const placementLocations = computed(() => form.value?.placement?.locations ?? [])
const placementPool = computed({
  get: () => form.value?.placement?.pool ?? '',
  set: (pool: string) => {
    if (form.value) form.value.placement = { ...form.value.placement, pool }
  },
})
function setLocations(locations: number[]) {
  if (form.value) form.value.placement = { ...form.value.placement, locations }
}
function toggleLocation(clusterId: number) {
  const ids = placementLocations.value
  setLocations(ids.includes(clusterId) ? ids.filter((x) => x !== clusterId) : [...ids, clusterId])
}
function makeDefaultLocation(clusterId: number) {
  setLocations([clusterId, ...placementLocations.value.filter((x) => x !== clusterId)])
}

const planSizes = computed(() => form.value?.database_sizes ?? [])
function setPlanSizes(ids: number[]) {
  if (form.value) form.value.database_sizes = ids
}
function toggleSize(id: number) {
  const ids = planSizes.value
  setPlanSizes(ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id])
}

const planClasses = computed(() => form.value?.storage_classes ?? [])
function toggleClass(name: string) {
  if (!form.value) return
  const names = planClasses.value
  form.value.storage_classes = names.includes(name) ? names.filter((n) => n !== name) : [...names, name]
  // A default outside the offered list would resolve to a class the plan refuses.
  if (form.value.default_storage_class && !form.value.storage_classes.includes(form.value.default_storage_class)) {
    form.value.default_storage_class = ''
  }
}
const planRunners = computed(() => form.value?.platform_runners ?? [])
function toggleRunner(name: string) {
  if (!form.value) return
  const names = planRunners.value
  form.value.platform_runners = names.includes(name) ? names.filter((n) => n !== name) : [...names, name]
}

function makeDefaultSize(id: number) {
  setPlanSizes([id, ...planSizes.value.filter((x) => x !== id)])
}
function sizeSummary(s: DatabaseSize): string {
  return `${+(s.nano_cpus / 1e9).toFixed(2)} CPU · ${Math.round(s.memory_bytes / 1048576)} MB`
}

async function save() {
  if (!form.value || !plan.value || !form.value.name.trim()) return
  saving.value = true
  try {
    plan.value = (await adminApi.updatePlan(plan.value.id, { ...form.value, name: form.value.name.trim() })).data.data
    notify.success(t('notify.planDetail.saved'))
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
    notify.success(t('notify.plans.defaultSet', { name: plan.value.name }))
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
    notify.success(t('notify.planDetail.deleted'))
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
          <button class="btn-icon btn-icon-muted" :title="$t('planDetail.backToPlans')" :aria-label="$t('planDetail.backToPlans')" @click="router.push('/admin/plans')">
            <span class="mdi mdi-arrow-left"></span>
          </button>
          <div class="header-title">
            <h1>
              {{ plan.name }}
              <span v-if="plan.is_default" class="badge badge-success">{{ $t('planDetail.default') }}</span>
              <span v-if="plan.system" class="badge badge-info"
                :title="$t('plans.systemPlanHint')">{{ $t('planDetail.system') }}</span>
              <span v-if="plan.is_active" class="badge badge-dot badge-success">{{ $t('planDetail.active') }}</span>
              <span v-else class="badge badge-dot badge-danger">{{ $t('planDetail.inactive') }}</span>
            </h1>
            <span class="subline">{{ plan.description || 'No description' }}</span>
          </div>
        </div>
        <div class="header-actions">
          <button v-if="!plan.is_default && !plan.system" class="btn btn-secondary" @click="makeDefault">{{ $t('plans.setAsDefault') }}</button>
          <!-- The system plan is the platform's own: deleting it would drop the
               system workspace onto the default plan's limits, silently. The API
               refuses it too — this only keeps the console from offering it. -->
          <button v-if="!plan.system" class="btn btn-danger" @click="showDelete = true">{{ $t('action.delete') }}</button>
        </div>
      </div>

      <!-- General -->
      <div class="card">
        <div class="card-header"><h2>{{ $t('planDetail.general') }}</h2></div>
        <div class="card-body">
          <div class="form-row">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <!-- The system plan is resolved by name when it is pinned to the
                   system workspace, so renaming it would quietly unpin it. Its
                   limits stay editable. -->
              <input v-model="form.name" class="form-input" required :disabled="plan.system" />
              <p v-if="plan.system" class="form-hint">{{ $t('planDetail.slugFixed') }}</p>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('plans.description') }}</label>
              <input v-model="form.description" class="form-input" :placeholder="$t('planDetail.optional')" />
            </div>
          </div>
          <div class="toggles">
            <label class="checkbox-label"><input v-model="form.is_active" type="checkbox" />{{ $t('plans.activeToggle') }}<span class="text-muted">{{ $t('planDetail.activeNote') }}</span></label>
            <label class="checkbox-label"><input v-model="form.is_default" type="checkbox" :disabled="plan.system" />{{ $t('plans.defaultPlan') }}<span class="text-muted">{{ $t('planDetail.defaultNote') }}</span></label>
            <p v-if="plan.system" class="form-hint">{{ $t('planDetail.systemNotDefault') }}</p>
          </div>
        </div>
      </div>

      <!-- Resource limits -->
      <div class="card mt-4">
        <div class="card-header">
          <h2>{{ $t('planDetail.resourceLimits') }}</h2>
          <span class="text-muted text-sm">{{ $t('planDetail.limitsHint') }}</span>
        </div>
        <div class="card-body">
          <h3 class="section-label">{{ $t('planDetail.counts') }}</h3>
          <div class="limit-grid">
            <div v-for="f in countFields" :key="f.key" class="limit-cell">
              <label class="form-label">{{ $t(f.label) }}</label>
              <input v-model.number="form[f.key]" type="number" min="-1" class="form-input" />
              <p class="form-hint">{{ $t(f.desc) }} <strong>{{ describe(form[f.key], f.unit) }}</strong></p>
            </div>
          </div>

          <h3 class="section-label" style="margin-top: 24px">{{ $t('planDetail.computeStorageTitle') }}</h3>
          <div class="limit-grid">
            <div v-for="f in computeFields" :key="f.key" class="limit-cell">
              <label class="form-label">{{ $t(f.label) }} <span v-if="f.unit" class="text-muted">({{ $t(f.unit) }})</span></label>
              <input v-model.number="form[f.key]" type="number" min="-1" class="form-input" />
              <p class="form-hint">{{ $t(f.desc) }} <strong>{{ describe(form[f.key], f.unit) }}</strong></p>
            </div>
          </div>
        </div>
      </div>

      <!-- Capabilities -->
      <div class="card mt-4">
        <div class="card-header"><h2>{{ $t('planDetail.capabilities') }}</h2></div>
        <div class="card-body">
          <label class="checkbox-label"><input v-model="form.allow_custom_tls" type="checkbox" />{{ $t('plans.capTls') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_privileged_host_mounts" type="checkbox" />{{ $t('plans.capHostMounts') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_shell_exec" type="checkbox" />{{ $t('plans.capShell') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_shared_storage" type="checkbox" />{{ $t('plans.capSharedStorage') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_dns_providers" type="checkbox" />{{ $t('plans.capDns') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_custom_labels" type="checkbox" />{{ $t('plans.capLabels') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_platform_runners" type="checkbox" />{{ $t('plans.capSharedRunners') }}</label>
          <label class="checkbox-label"><input v-model="form.allow_gpu" type="checkbox" />{{ $t('planDetail.capGpu') }}</label>
          <div class="form-group" style="margin-top: 12px; max-width: 360px">
            <label class="form-label">{{ $t('plans.containerSecurityProfile') }}<span v-if="!securityProfile.has.value" class="badge badge-neutral" style="margin-left: 6px" :title="$t('plans.restrictedRequiresEE')">
                <span class="mdi mdi-lock-outline"></span>{{ $t('planDetail.enterprise') }}</span>
            </label>
            <select v-model="form.security_profile" class="form-select" :disabled="!securityProfile.mutable.value">
              <option value="default">{{ $t('plans.profileDefault') }}</option>
              <option value="restricted">{{ $t('plans.profileRestricted') }}</option>
            </select>
            <p class="form-hint">{{ $t('planDetail.restrictedHint') }}<template v-if="!securityProfile.has.value">{{ $t('planDetail.storageClassRequiresEE') }}</template></p>
            <label class="checkbox-label" style="margin-top: 10px" :class="{ 'is-disabled': form.security_profile !== 'restricted' }">
              <input v-model="form.allow_official_image_user" type="checkbox" :disabled="form.security_profile !== 'restricted'" />{{ $t('plans.exemptMarketplace') }}</label>
            <i18n-t keypath="planDetail.officialExemptHint" tag="p" class="form-hint"><template #official><strong>{{ $t('planDetail.official') }}</strong></template></i18n-t>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header">
          <h2>{{ $t('planDetail.placement') }}</h2>
          <span v-if="!placementPolicy.has.value" class="badge badge-neutral" :title="$t('planDetail.placementRequiresEE')">
            <span class="mdi mdi-lock-outline"></span>{{ $t('planDetail.enterprise') }}</span>
        </div>
        <div class="card-body">
          <p class="form-hint" style="margin-top: 0">{{ $t('planDetail.placementHint') }}<template v-if="!placementPolicy.has.value">{{ $t('planDetail.placementRequiresEEHint') }}</template>
          </p>
          <label class="form-label" style="margin-top: 12px">{{ $t('planDetail.locations') }}</label>
          <label v-for="c in clusters" :key="c.id" class="checkbox-label">
            <input type="checkbox" :checked="placementLocations.includes(c.id)" :disabled="!placementPolicy.mutable.value" @change="toggleLocation(c.id)" />
            {{ c.display_name || c.name }}<span v-if="c.location_code" class="text-muted"> ({{ c.location_code }})</span>
            <span v-if="placementLocations[0] === c.id" class="badge badge-info" style="margin-left: 6px">{{ $t('planDetail.default') }}</span>
            <button
              v-else-if="placementLocations.includes(c.id) && placementPolicy.mutable.value"
              type="button"
              class="btn btn-ghost btn-sm"
              @click.prevent="makeDefaultLocation(c.id)"
            >{{ $t('action.makeDefault') }}</button>
          </label>
          <p class="form-hint">{{ $t('planDetail.locationsHint') }}</p>
          <div class="form-group" style="margin-top: 12px; max-width: 360px">
            <label class="form-label">{{ $t('planDetail.nodePool') }}</label>
            <input v-model.trim="placementPool" class="form-input mono" :placeholder="$t('planDetail.namePlaceholder')" :disabled="!placementPolicy.mutable.value" />
            <p class="form-hint">{{ $t('planDetail.nodePoolHint') }}</p>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header">
          <h2>{{ $t('adminNav.tenants.databaseSizes') }}</h2>
          <span v-if="!databaseSizesPolicy.has.value" class="badge badge-neutral" :title="$t('planDetail.databaseSizesRequireEE')">
            <span class="mdi mdi-lock-outline"></span>{{ $t('planDetail.enterprise') }}</span>
        </div>
        <div class="card-body">
          <p class="form-hint" style="margin-top: 0">
            <i18n-t keypath="planDetail.databaseSizesHint" tag="span"><template #link><router-link to="/admin/database-sizes">{{ $t('planDetail.databaseSizes2') }}</router-link></template></i18n-t>
            <template v-if="!databaseSizesPolicy.has.value"> {{ $t('planDetail.requiresAnEnterpriseLicense') }}</template>
          </p>
          <p v-if="databaseSizesPolicy.has.value && !databaseSizes.length" class="text-muted text-sm">{{ $t('planDetail.noDatabaseSizes') }}</p>
          <label v-for="s in databaseSizes" :key="s.id" class="checkbox-label">
            <input type="checkbox" :checked="planSizes.includes(s.id)" :disabled="!databaseSizesPolicy.mutable.value" @change="toggleSize(s.id)" />
            {{ s.display_name || s.name }} <span class="text-muted">({{ sizeSummary(s) }})</span>
            <span v-if="planSizes[0] === s.id" class="badge badge-info" style="margin-left: 6px">{{ $t('planDetail.default') }}</span>
            <button
              v-else-if="planSizes.includes(s.id) && databaseSizesPolicy.mutable.value"
              type="button"
              class="btn btn-ghost btn-sm"
              @click.prevent="makeDefaultSize(s.id)"
            >{{ $t('action.makeDefault') }}</button>
          </label>
          <p class="form-hint">{{ $t('planDetail.databaseSizesOptional') }}</p>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header"><h2>{{ $t('adminNav.infrastructure.storageClasses') }}</h2></div>
        <div class="card-body">
          <i18n-t keypath="planDetail.storageClassesHint" tag="p" class="form-hint" style="margin-top: 0"><template #link><router-link to="/admin/storage-classes">{{ $t('planDetail.storageClasses2') }}</router-link></template></i18n-t>
          <p v-if="!storageClasses.length" class="text-muted text-sm">{{ $t('planDetail.noStorageClasses') }}</p>
          <label v-for="c in storageClasses" :key="c.id" class="checkbox-label">
            <input type="checkbox" :checked="planClasses.includes(c.name)" @change="toggleClass(c.name)" />
            {{ c.display_name || c.name }} <span class="text-muted mono">{{ c.name }}</span>
            <span v-if="!c.enabled" class="badge" style="margin-left: 6px">{{ $t('planDetail.disabled') }}</span>
          </label>
          <div v-if="form" class="form-group" style="margin-top: 12px; max-width: 360px">
            <label class="form-label">{{ $t('planDetail.defaultStorageClass') }}</label>
            <select v-model="form.default_storage_class" class="form-select">
              <option value="">{{ $t('planDetail.nodeDefault') }}</option>
              <option
                v-for="c in storageClasses.filter((c) => !planClasses.length || planClasses.includes(c.name))"
                :key="c.id"
                :value="c.name"
              >{{ c.display_name || c.name }}</option>
            </select>
            <p class="form-hint">{{ $t('planDetail.defaultStorageClassHint') }}</p>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header">
          <h2>{{ $t('adminNav.infrastructure.sharedRunners') }}</h2>
          <span v-if="!platformRunnersPolicy.has.value" class="badge badge-neutral" :title="$t('planDetail.platformRunnersRequireEE')">
            <span class="mdi mdi-lock-outline"></span>{{ $t('planDetail.enterprise') }}</span>
        </div>
        <div class="card-body">
          <p class="form-hint" style="margin-top: 0">
            <i18n-t keypath="planDetail.platformRunnersHint" tag="span"><template #link><router-link to="/admin/runners">{{ $t('planDetail.platformRunners2') }}</router-link></template></i18n-t>
            <template v-if="!platformRunnersPolicy.has.value"> {{ $t('planDetail.requiresAnEnterpriseLicense') }}</template>
          </p>
          <p v-if="!sharedRunners.length" class="text-muted text-sm">{{ $t('planDetail.noPlatformRunners') }}</p>
          <label v-for="r in sharedRunners" :key="r.id" class="checkbox-label">
            <input type="checkbox" :checked="planRunners.includes(r.name)" :disabled="!platformRunnersPolicy.mutable.value" @change="toggleRunner(r.name)" />
            {{ r.display_name || r.name }} <span class="text-muted mono">{{ r.name }}</span>
            <span v-if="r.cordoned" class="badge" style="margin-left: 6px">{{ $t('planDetail.disabled') }}</span>
          </label>
          <p class="form-hint">{{ $t('planDetail.platformRunnersOptional') }}</p>
        </div>
      </div>

      <!-- Metadata -->
      <div class="card mt-4">
        <div class="card-header"><h2>{{ $t('planDetail.details') }}</h2></div>
        <div class="card-body details">
          <div class="detail"><span class="text-muted">{{ $t('planDetail.planId') }}</span><span><code>{{ plan.id }}</code></span></div>
          <div class="detail"><span class="text-muted">{{ $t('dashboard.col.created') }}</span><span>{{ fmtDate(plan.created_at) }}</span></div>
          <div class="detail"><span class="text-muted">{{ $t('planDetail.updated') }}</span><span>{{ fmtDate(plan.updated_at) }}</span></div>
        </div>
      </div>

      <div class="save-bar">
        <button class="btn btn-secondary" @click="router.push('/admin/plans')">{{ $t('action.cancel') }}</button>
        <button class="btn btn-primary" :disabled="saving || !form.name.trim()" @click="save">{{ saving ? 'Saving…' : 'Save changes' }}</button>
      </div>
    </template>

    <ConfirmDialog
      :open="showDelete"
      :title="$t('confirm.title.deletePlan')"
      :message="$t('confirm.message.planDetail.deletePlanNameAny', { name: plan?.name })"
      :confirm-label="$t('action.delete')"
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
