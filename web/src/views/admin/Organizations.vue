<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/admin'
import { clustersApi } from '@/api/clusters'
import type { Cluster, Organization, OrganizationInput, OrganizationUpdate } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const { t } = useI18n()
const router = useRouter()
// A second organization is Enterprise; the default one exists in every edition and stays editable.
const entitlement = useEntitlement('organizations')

const orgs = ref<Organization[]>([])
const clusters = ref<Cluster[]>([])
const loading = ref(false)
const saving = ref(false)
const showModal = ref(false)
const editing = ref<Organization | null>(null)
const confirmTarget = ref<Organization | null>(null)
const promoteTarget = ref<Organization | null>(null)

// unlimited is tracked apart from the number so clearing the field cannot be mistaken for 0, which
// genuinely means "no workspaces allowed".
const form = ref({ name: '', display_name: '', max_workspaces: 0, unlimited: true, default_cluster_id: 0 })

function blank() {
  return { name: '', display_name: '', max_workspaces: 0, unlimited: true, default_cluster_id: 0 }
}

async function load() {
  loading.value = true
  try {
    const [list, clusterList] = await Promise.all([
      adminApi.listOrganizations(),
      clustersApi.list().catch(() => ({ data: { data: [] as Cluster[] } })),
    ])
    orgs.value = list.data.data ?? []
    clusters.value = clusterList.data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// dedicatedTo maps each cluster to the organization it belongs to, so the table can show an org's
// locations without a request per row.
const dedicatedTo = computed(() => {
  const m = new Map<number, Cluster[]>()
  for (const c of clusters.value) {
    if (!c.organization_id) continue
    const list = m.get(c.organization_id) ?? []
    list.push(c)
    m.set(c.organization_id, list)
  }
  return m
})

const clusterLabel = (c: Cluster) => c.display_name || c.name

// An organization that owns clusters is confined to them, so offering a shared one here would only
// show a location the API refuses and placement skips. Mirrors the organization detail page.
const editingConfined = computed(() => (dedicatedTo.value.get(editing.value?.id ?? 0) ?? []).length > 0)
const selectableClusters = computed<Cluster[]>(() =>
  editingConfined.value
    ? clusters.value.filter((c) => c.organization_id === editing.value?.id)
    : clusters.value.filter((c) => !c.organization_id),
)

// Promoting an organization that runs its own clusters confines every future unassigned user and
// workspace to them. Existing rows keep the organization they already carry, so nothing moves — but
// the next workspace someone makes lands somewhere they did not choose.
const promoteWarning = computed(() => {
  const o = promoteTarget.value
  if (!o) return ''
  const owned = dedicatedTo.value.get(o.id) ?? []
  if (!owned.length) return ''
  const where = owned.map(clusterLabel).join(', ')
  return ' ' + t('confirm.message.organizations.promoteWarning', { name: o.display_name || o.name, where })
})

function capLabel(o: Organization): string {
  if (o.max_workspaces < 0) return `${o.workspace_count} workspaces`
  return `${o.workspace_count} of ${o.max_workspaces}`
}

function atCap(o: Organization): boolean {
  return o.max_workspaces >= 0 && o.workspace_count >= o.max_workspaces
}

function openCreate() {
  editing.value = null
  form.value = blank()
  showModal.value = true
}

function openEdit(o: Organization) {
  editing.value = o
  form.value = {
    name: o.name,
    display_name: o.display_name,
    max_workspaces: o.max_workspaces < 0 ? 0 : o.max_workspaces,
    unlimited: o.max_workspaces < 0,
    default_cluster_id: o.default_cluster_id ?? 0,
  }
  showModal.value = true
}

async function save() {
  saving.value = true
  try {
    const cap = form.value.unlimited ? -1 : Math.max(0, Number(form.value.max_workspaces) || 0)
    if (editing.value) {
      const payload: OrganizationUpdate = {
        display_name: form.value.display_name.trim(),
        max_workspaces: cap,
        default_cluster_id: form.value.default_cluster_id,
      }
      await adminApi.updateOrganization(editing.value.id, payload)
    } else {
      const payload: OrganizationInput = {
        name: form.value.name.trim() || undefined,
        display_name: form.value.display_name.trim(),
        max_workspaces: cap,
      }
      await adminApi.createOrganization(payload)
    }
    notify.success(editing.value ? 'Organization updated' : 'Organization created')
    showModal.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function promote() {
  const o = promoteTarget.value
  promoteTarget.value = null
  if (!o) return
  try {
    await adminApi.setDefaultOrganization(o.id)
    notify.success(`${o.display_name} is now the default organization`)
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}

async function remove() {
  const o = confirmTarget.value
  confirmTarget.value = null
  if (!o) return
  try {
    await adminApi.deleteOrganization(o.id)
    notify.success('Organization deleted')
    await load()
  } catch (e) {
    notify.apiError(e)
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Organizations</h1>
        <p class="cell-sub">
          The tenant a workspace belongs to. An organization caps how many workspaces it may hold and can be given
          clusters of its own, which its workloads then run on exclusively.
        </p>
      </div>
      <button v-if="entitlement.has.value" class="btn btn-primary" :disabled="!entitlement.mutable.value" @click="openCreate">
        <span class="mdi mdi-plus"></span> New organization
      </button>
      <span v-else class="badge badge-muted"><span class="mdi mdi-lock-outline"></span> Enterprise</span>
    </div>

    <div v-if="!entitlement.has.value" class="card" style="margin-bottom: 16px">
      <div class="card-body">
        <h3 style="margin: 0 0 4px">Separate your tenants with Enterprise</h3>
        <p class="text-muted" style="margin: 0">
          Every install has one organization and always has — this page shows it, and nothing changes without a license.
          A second organization, a per-tenant workspace cap and dedicating a cluster to one customer need Enterprise.
        </p>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && orgs.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr><th>Organization</th><th>Workspaces</th><th>Dedicated locations</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="o in orgs" :key="o.id">
              <td>
                <span class="cell-text">
                  <span class="cell-title">
                    <RouterLink :to="`/admin/organizations/${o.id}`">{{ o.display_name || o.name }}</RouterLink>
                    <span v-if="o.is_default" class="badge badge-info">default</span>
                    <span v-if="o.enforce_sso" class="badge">SSO enforced</span>
                  </span>
                  <span class="cell-sub mono">{{ o.name }}</span>
                </span>
              </td>
              <td>
                {{ capLabel(o) }}
                <span v-if="atCap(o)" class="badge badge-warning">at limit</span>
                <span v-else-if="o.max_workspaces === 0" class="cell-sub">no workspaces allowed</span>
              </td>
              <td>
                <template v-if="dedicatedTo.get(o.id)?.length">
                  <span v-for="c in dedicatedTo.get(o.id)" :key="c.id" class="badge">{{ clusterLabel(c) }}</span>
                </template>
                <span v-else class="text-muted">Shared locations</span>
              </td>
              <td style="text-align: right; white-space: nowrap">
                <button class="btn btn-ghost btn-sm" @click="router.push(`/admin/organizations/${o.id}`)">Open</button>
                <button class="btn btn-ghost btn-sm" :disabled="!entitlement.mutable.value" @click="openEdit(o)">Edit</button>
                <button
                  v-if="!o.is_default"
                  class="btn btn-ghost btn-sm"
                  :disabled="!entitlement.mutable.value"
                  @click="promoteTarget = o"
                >
                  Make default
                </button>
                <button
                  class="btn btn-ghost btn-sm"
                  :disabled="o.is_default || !entitlement.mutable.value"
                  :title="o.is_default ? 'The default organization cannot be deleted' : ''"
                  @click="confirmTarget = o"
                >
                  Delete
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <p class="text-muted" style="margin-top: 12px; font-size: 13px">
      A workspace is created in the organization its owner belongs to — set that on the user's page. Dedicate a location
      to an organization from the cluster's own page.
    </p>

    <Teleport to="body">
      <AppModal v-if="showModal" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit organization' : 'New organization' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Display name</label>
              <input v-model="form.display_name" class="form-input" placeholder="Acme Corp" required />
            </div>
            <div class="form-group">
              <label class="form-label">Handle <span class="text-muted">(optional)</span></label>
              <input v-model="form.name" class="form-input mono" placeholder="derived from the display name" :disabled="!!editing" />
              <p class="form-hint">
                A short lowercase handle the API and admins address the organization by. It cannot be changed afterwards.
              </p>
            </div>
            <div class="form-group">
              <label class="form-label">Workspace limit</label>
              <label class="checkbox-row">
                <input v-model="form.unlimited" type="checkbox" />
                <span>Unlimited</span>
              </label>
              <input
                v-if="!form.unlimited"
                v-model.number="form.max_workspaces"
                type="number"
                min="0"
                class="form-input"
                style="margin-top: 8px"
              />
              <p v-if="!form.unlimited" class="form-hint">
                How many workspaces this organization may hold in total. Workspaces already created stay; only the next
                one is refused. A limit of 0 allows none at all.
              </p>
            </div>
            <div v-if="editing" class="form-group">
              <label class="form-label">Default location <span class="text-muted">(optional)</span></label>
              <select v-model.number="form.default_cluster_id" class="form-input">
                <option :value="0">{{ editingConfined ? 'First dedicated location' : 'Platform default' }}</option>
                <option v-for="c in selectableClusters" :key="c.id" :value="c.id">{{ clusterLabel(c) }}</option>
              </select>
              <p class="form-hint">
                Where this organization's new workspaces put their resources when they name no location.
                <template v-if="editingConfined"> Only its own locations are offered — a shared one would be refused.</template>
              </p>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Create organization' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!promoteTarget"
      :title="$t('confirm.title.changeTheDefaultOrganization')"
      :message="$t('confirm.message.organizations.newUsersAndWorkspaces', { display_name: promoteTarget?.display_name ?? '', promoteWarning: promoteWarning })"
      :confirm-label="$t('action.makeDefault')"
      @confirm="promote"
      @cancel="promoteTarget = null"
    />

    <ConfirmDialog
      :open="!!confirmTarget"
      :title="$t('confirm.title.deleteOrganization')"
      :message="$t('confirm.message.organizations.displayNameWillBe', { display_name: confirmTarget?.display_name ?? '' })"
      :confirm-label="$t('action.delete')"
      variant="danger"
      @confirm="remove"
      @cancel="confirmTarget = null"
    />
  </div>
</template>
