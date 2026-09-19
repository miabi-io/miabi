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

const form = ref({ display_name: '', max_workspaces: 0, unlimited: true, default_cluster_id: 0 })

async function load() {
  loading.value = true
  try {
    const [detail, clusterList] = await Promise.all([
      adminApi.getOrganization(orgId.value),
      clustersApi.list().catch(() => ({ data: { data: [] as Cluster[] } })),
    ])
    org.value = detail.data.data
    clusters.value = clusterList.data.data ?? []
    form.value = {
      display_name: org.value.display_name,
      max_workspaces: org.value.max_workspaces < 0 ? 0 : org.value.max_workspaces,
      unlimited: org.value.max_workspaces < 0,
      default_cluster_id: org.value.default_cluster_id ?? 0,
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
  return (
    form.value.display_name.trim() !== o.display_name ||
    cap !== o.max_workspaces ||
    form.value.default_cluster_id !== (o.default_cluster_id ?? 0)
  )
})

async function save() {
  saving.value = true
  try {
    const payload: OrganizationUpdate = {
      display_name: form.value.display_name.trim(),
      max_workspaces: form.value.unlimited ? -1 : Math.max(0, Number(form.value.max_workspaces) || 0),
      default_cluster_id: form.value.default_cluster_id,
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
            <input v-model="form.display_name" class="form-input" :disabled="!entitlement.mutable.value" style="max-width: 420px" />
          </div>
          <div class="form-group">
            <label class="form-label">Workspace limit</label>
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
              Workspaces already created stay; only the next one is refused. A limit of 0 allows none at all.
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">Default location</label>
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
            <button class="btn btn-primary" :disabled="saving || !dirty || !entitlement.mutable.value" @click="save">
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
      title="Change the default organization"
      :message="promoteMessage"
      confirm-label="Make default"
      @confirm="promote"
      @cancel="confirmPromote = false"
    />

    <ConfirmDialog
      :open="confirmDelete"
      title="Delete organization"
      :message="`&quot;${org?.display_name ?? ''}&quot; will be removed, and any location dedicated to it becomes administrator-only until you reassign it. Deleting is refused while it still holds workspaces or users.`"
      confirm-label="Delete"
      variant="danger"
      @confirm="remove"
      @cancel="confirmDelete = false"
    />
  </div>
</template>
