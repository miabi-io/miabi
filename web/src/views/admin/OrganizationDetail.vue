<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { clustersApi } from '@/api/clusters'
import type { Cluster, OrganizationDetail, OrganizationUpdate } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const entitlement = useEntitlement('organizations')

const orgId = computed(() => Number(route.params.id))
const org = ref<OrganizationDetail | null>(null)
const clusters = ref<Cluster[]>([])
const loading = ref(false)
const saving = ref(false)
const confirmDelete = ref(false)
const confirmPromote = ref(false)

type LimitMode = 'inherit' | 'unlimited' | 'custom'

const form = ref({
  display_name: '',
  max_workspaces: 0,
  unlimited: true,
  default_cluster_id: 0,
  // Per-user caps are tri-state: inherit the platform default, unlimited, or a number. A plain
  // number field cannot express "inherit", and an empty one would read as 0 — which means none.
  per_user_mode: 'inherit' as LimitMode,
  per_user: 10,
  joined_mode: 'inherit' as LimitMode,
  joined: 10,
})

// The platform defaults, shown in the Inherit labels so an admin can see what they are inheriting.
const platformDefaults = ref({ owned: null as number | null, joined: null as number | null })
function defaultLabel(n: number | null): string {
  if (n === null) return 'the platform default'
  return n < 0 ? 'unlimited' : String(n)
}

function modeOf(v: number | null | undefined): LimitMode {
  if (v === null || v === undefined) return 'inherit'
  return v < 0 ? 'unlimited' : 'custom'
}
function limitValue(mode: LimitMode, n: number): number | undefined {
  if (mode === 'inherit') return undefined
  return mode === 'unlimited' ? -1 : Math.max(0, Number(n) || 0)
}

async function load() {
  loading.value = true
  try {
    const [detail, clusterList, settingList] = await Promise.all([
      adminApi.getOrganization(orgId.value),
      clustersApi.list().catch(() => ({ data: { data: [] as Cluster[] } })),
      adminApi.listSettings().catch(() => ({ data: { data: [] as { key: string; value: string }[] } })),
    ])
    org.value = detail.data.data
    clusters.value = clusterList.data.data ?? []
    const setting = (key: string): number | null => {
      const row = (settingList.data.data ?? []).find((r) => r.key === key)
      const n = row ? Number(row.value) : NaN
      return Number.isFinite(n) ? n : null
    }
    platformDefaults.value = {
      owned: setting('max_workspaces_per_user'),
      joined: setting('max_workspace_memberships_per_user'),
    }
    form.value = {
      display_name: org.value.display_name,
      max_workspaces: org.value.max_workspaces < 0 ? 0 : org.value.max_workspaces,
      unlimited: org.value.max_workspaces < 0,
      default_cluster_id: org.value.default_cluster_id ?? 0,
      per_user_mode: modeOf(org.value.max_workspaces_per_user),
      per_user: Math.max(0, org.value.max_workspaces_per_user ?? platformDefaults.value.owned ?? 10),
      joined_mode: modeOf(org.value.max_workspace_memberships_per_user),
      joined: Math.max(0, org.value.max_workspace_memberships_per_user ?? 10),
    }
  } catch (e) {
    notify.apiError(e)
    router.replace('/admin/organizations')
  } finally {
    loading.value = false
  }
}
watch(orgId, load, { immediate: true })

// Confined: the organization runs its own locations, so its workspaces deploy there and nowhere
// else, and a location outside the set cannot be its default.
const confined = computed(() => (org.value?.clusters.length ?? 0) > 0)
const selectableClusters = computed(() =>
  confined.value ? clusters.value.filter((c) => c.organization_id === orgId.value) : clusters.value.filter((c) => !c.organization_id),
)
const clusterLabel = (c: { name: string; display_name?: string }) => c.display_name || c.name

const usage = computed(() => {
  const o = org.value
  if (!o) return ''
  return o.max_workspaces < 0 ? `${o.workspaces.length} workspaces` : `${o.workspaces.length} of ${o.max_workspaces}`
})
const atCap = computed(() => !!org.value && org.value.max_workspaces >= 0 && org.value.workspaces.length >= org.value.max_workspaces)

const dirty = computed(() => {
  const o = org.value
  if (!o) return false
  const cap = form.value.unlimited ? -1 : Math.max(0, Number(form.value.max_workspaces) || 0)
  const perUser = limitValue(form.value.per_user_mode, form.value.per_user)
  const joined = limitValue(form.value.joined_mode, form.value.joined)
  return (
    form.value.display_name.trim() !== o.display_name ||
    cap !== o.max_workspaces ||
    form.value.default_cluster_id !== (o.default_cluster_id ?? 0) ||
    perUser !== (o.max_workspaces_per_user ?? undefined) ||
    joined !== (o.max_workspace_memberships_per_user ?? undefined)
  )
})

// The default org's label stays editable in Community; its limits do not, so a label-only change
// must still be saveable without sending a single gated field.
const labelOnlyChange = computed(() => {
  const o = org.value
  if (!o) return false
  const cap = form.value.unlimited ? -1 : Math.max(0, Number(form.value.max_workspaces) || 0)
  return (
    form.value.display_name.trim() !== o.display_name &&
    cap === o.max_workspaces &&
    limitValue(form.value.per_user_mode, form.value.per_user) === (o.max_workspaces_per_user ?? undefined) &&
    limitValue(form.value.joined_mode, form.value.joined) === (o.max_workspace_memberships_per_user ?? undefined) &&
    form.value.default_cluster_id === (o.default_cluster_id ?? 0)
  )
})

async function save() {
  saving.value = true
  try {
    const o = org.value
    const payload: OrganizationUpdate = { display_name: form.value.display_name.trim() }
    const cap = form.value.unlimited ? -1 : Math.max(0, Number(form.value.max_workspaces) || 0)
    // Every limit is sent only when it changed: they are gated on the organizations entitlement, and
    // resending an unchanged one would refuse an edit Community is allowed to make.
    if (o && cap !== o.max_workspaces) payload.max_workspaces = cap
    const perUser = limitValue(form.value.per_user_mode, form.value.per_user)
    if (o && perUser !== (o.max_workspaces_per_user ?? undefined)) {
      if (form.value.per_user_mode === 'inherit') payload.inherit_workspaces_per_user = true
      else payload.max_workspaces_per_user = perUser
    }
    const joined = limitValue(form.value.joined_mode, form.value.joined)
    if (o && joined !== (o.max_workspace_memberships_per_user ?? undefined)) {
      if (form.value.joined_mode === 'inherit') payload.inherit_workspace_memberships_per_user = true
      else payload.max_workspace_memberships_per_user = joined
    }
    // Only send the location when it actually changed: it is the one field still gated on the
    // organizations entitlement, and sending it unchanged would refuse an otherwise allowed edit.
    if (o && form.value.default_cluster_id !== (o.default_cluster_id ?? 0)) {
      payload.default_cluster_id = form.value.default_cluster_id
    }
    await adminApi.updateOrganization(orgId.value, payload)
    notify.success('Organization updated')
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function promote() {
  confirmPromote.value = false
  try {
    await adminApi.setDefaultOrganization(orgId.value)
    notify.success('Default organization updated')
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}

async function remove() {
  confirmDelete.value = false
  try {
    await adminApi.deleteOrganization(orgId.value)
    notify.success('Organization deleted')
    router.replace('/admin/organizations')
  } catch (e) {
    notify.apiError(e)
  }
}

const promoteMessage = computed(() => {
  const base = `New users and workspaces that name no organization will belong to "${org.value?.display_name ?? ''}". Existing ones do not move.`
  if (!confined.value) return base
  const where = (org.value?.clusters ?? []).map(clusterLabel).join(', ')
  return `${base} It runs its own locations (${where}), so those new workspaces will deploy there and nowhere else.`
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <button class="btn btn-ghost btn-sm" @click="router.push('/admin/organizations')">
          <span class="mdi mdi-arrow-left"></span> Organizations
        </button>
        <h1 style="margin-top: 6px">
          {{ org?.display_name || org?.name || '…' }}
          <span v-if="org?.is_default" class="badge badge-info">default</span>
          <span v-if="confined" class="badge badge-muted"><span class="mdi mdi-server-security"></span> dedicated</span>
        </h1>
        <p class="cell-sub mono">{{ org?.name }}</p>
      </div>
      <div v-if="org" style="display: flex; gap: 8px">
        <button v-if="!org.is_default" class="btn btn-secondary" :disabled="!entitlement.mutable.value" @click="confirmPromote = true">
          Make default
        </button>
        <button
          class="btn btn-danger"
          :disabled="org.is_default || !entitlement.mutable.value"
          :title="org.is_default ? 'The default organization cannot be deleted' : ''"
          @click="confirmDelete = true"
        >
          Delete
        </button>
      </div>
    </div>

    <div v-if="loading && !org" class="card"><div class="card-body"><span class="spinner"></span></div></div>

    <template v-else-if="org">
      <div class="card">
        <div class="card-header"><h2>Overview</h2></div>
        <div class="card-body">
          <div class="form-group">
            <label class="form-label">Workspaces</label>
            <div>
              {{ usage }}
              <span v-if="atCap" class="badge badge-warning">at limit</span>
              <span v-else-if="org.max_workspaces === 0" class="cell-sub">no workspaces allowed</span>
            </div>
          </div>
          <div class="form-group">
            <label class="form-label">Users</label>
            <div>{{ org.user_count }}</div>
          </div>
          <div class="form-group">
            <label class="form-label">Owner</label>
            <div v-if="org.owner_name">{{ org.owner_name }} <span class="cell-sub">{{ org.owner_email }}</span></div>
            <div v-else class="text-muted">Not assigned</div>
          </div>
          <div class="form-group" style="margin-bottom: 0">
            <label class="form-label">Identifier</label>
            <code class="mono">{{ org.uid }}</code>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header"><h2>Settings</h2></div>
        <div class="card-body">
          <div class="form-group">
            <label class="form-label">Display name</label>
            <input v-model="form.display_name" class="form-input" style="max-width: 420px" />
          </div>
          <div v-if="!entitlement.has.value" class="form-hint" style="margin-bottom: 16px">
            <span class="mdi mdi-lock-outline"></span>
            Workspace limits come from
            <router-link to="/admin/settings">Platform Settings</router-link> in this edition. With an
            Enterprise licence this organization's own limits take over.
          </div>
          <div class="form-group">
            <label class="form-label">
              Workspace limit
              <span v-if="!entitlement.has.value" class="badge badge-neutral" style="margin-left: 6px" title="Organization limits require an Enterprise license">
                <span class="mdi mdi-lock-outline"></span> Enterprise
              </span>
            </label>
            <label class="checkbox-row">
              <input v-model="form.unlimited" type="checkbox" :disabled="!entitlement.mutable.value" />
              <span>Unlimited</span>
            </label>
            <input
              v-if="!form.unlimited"
              v-model.number="form.max_workspaces"
              type="number"
              min="0"
              class="form-input"
              :disabled="!entitlement.mutable.value"
              style="max-width: 200px; margin-top: 8px"
            />
            <p class="form-hint">
              How many workspaces this organization may hold in total. Workspaces already created stay;
              only the next one is refused. A limit of 0 allows none at all.
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">Workspaces per user — owned</label>
            <select v-model="form.per_user_mode" class="form-select" :disabled="!entitlement.mutable.value" style="max-width: 420px">
              <option value="inherit">Inherit the platform default ({{ defaultLabel(platformDefaults.owned) }})</option>
              <option value="unlimited">Unlimited</option>
              <option value="custom">Limit to…</option>
            </select>
            <input
              v-if="form.per_user_mode === 'custom'"
              v-model.number="form.per_user"
              type="number"
              min="0"
              class="form-input"
              :disabled="!entitlement.mutable.value"
              style="max-width: 200px; margin-top: 8px"
            />
            <p class="form-hint">
              How many workspaces one of this organization's users may own. With a licence this is where
              it is set; <router-link to="/admin/settings">Platform Settings</router-link> supplies the
              default for organizations that inherit, and governs outright without one.
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">Workspaces per user — joined as member</label>
            <select v-model="form.joined_mode" class="form-select" :disabled="!entitlement.mutable.value" style="max-width: 420px">
              <option value="inherit">Inherit the platform default ({{ defaultLabel(platformDefaults.joined) }})</option>
              <option value="unlimited">Unlimited</option>
              <option value="custom">Limit to…</option>
            </select>
            <input
              v-if="form.joined_mode === 'custom'"
              v-model.number="form.joined"
              type="number"
              min="0"
              class="form-input"
              :disabled="!entitlement.mutable.value"
              style="max-width: 200px; margin-top: 8px"
            />
            <p class="form-hint">
              How many other workspaces one of its users may be a member of. Workspaces they own are
              counted by the limit above, not this one.
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">
              Default location
              <span v-if="!entitlement.has.value" class="badge badge-neutral" style="margin-left: 6px" title="Choosing where an organization's workspaces land requires an Enterprise license">
                <span class="mdi mdi-lock-outline"></span> Enterprise
              </span>
            </label>
            <select v-model.number="form.default_cluster_id" class="form-select" :disabled="!entitlement.mutable.value" style="max-width: 420px">
              <option :value="0">{{ confined ? 'First dedicated location' : 'Platform default' }}</option>
              <option v-for="c in selectableClusters" :key="c.id" :value="c.id">{{ clusterLabel(c) }}</option>
            </select>
            <p class="form-hint">
              Where this organization's new workspaces put their resources when they name no location.
              <template v-if="confined">
                Only its own locations are offered — a shared one would be refused.
              </template>
            </p>
          </div>
          <div style="display: flex; justify-content: flex-end">
            <button class="btn btn-primary" :disabled="saving || !dirty || (!entitlement.mutable.value && !labelOnlyChange)" @click="save">
              {{ saving ? 'Saving…' : 'Save changes' }}
            </button>
          </div>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header">
          <h2>Dedicated locations</h2>
        </div>
        <div v-if="!org.clusters.length" class="card-body">
          <p class="text-muted" style="margin: 0">
            None. This organization's workspaces deploy to the shared locations, alongside every other tenant.
            Dedicate one from the location's own page.
          </p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Location</th><th>Handle</th><th></th></tr></thead>
            <tbody>
              <tr v-for="c in org.clusters" :key="c.id">
                <td>
                  {{ clusterLabel(c) }}
                  <span v-if="c.cordoned" class="badge badge-warning">cordoned</span>
                </td>
                <td class="mono">{{ c.name }}</td>
                <td style="text-align: right">
                  <button class="btn btn-ghost btn-sm" @click="router.push(`/admin/clusters/${c.id}`)">Open</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="org.clusters.length" class="card-body" style="padding-top: 0">
          <p class="text-muted text-sm" style="margin: 0">
            This organization deploys only here — its workspaces cannot use a shared location, and no other tenant
            can use these.
          </p>
        </div>
      </div>

      <div class="card mt-4">
        <div class="card-header"><h2>Workspaces</h2></div>
        <div v-if="!org.workspaces.length" class="card-body">
          <p class="text-muted" style="margin: 0">None yet. A workspace joins this organization when its creator belongs to it.</p>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Workspace</th><th>Handle</th><th></th></tr></thead>
            <tbody>
              <tr v-for="w in org.workspaces" :key="w.id">
                <td>{{ w.display_name || w.name }}</td>
                <td class="mono">{{ w.name }}</td>
                <td style="text-align: right">
                  <button class="btn btn-ghost btn-sm" @click="router.push(`/admin/workspaces/${w.id}`)">Open</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <ConfirmDialog
      :open="confirmPromote"
      :title="$t('confirm.title.changeTheDefaultOrganization')"
      :message="promoteMessage"
      :confirm-label="$t('action.makeDefault')"
      @confirm="promote"
      @cancel="confirmPromote = false"
    />

    <ConfirmDialog
      :open="confirmDelete"
      :title="$t('confirm.title.deleteOrganization')"
      :message="$t('confirm.message.organizationDetail.displayNameWillBe', { display_name: org?.display_name ?? '' })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      @confirm="remove"
      @cancel="confirmDelete = false"
    />
  </div>
</template>
