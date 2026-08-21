<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { permissionApi, roleApi } from '@/api/rbac'
import type { CustomRoleInput } from '@/api/rbac'
import type { PermissionInfo, CustomRole, WorkspaceRole } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const props = defineProps<{ wsId: number; myRole: WorkspaceRole | string }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const notify = useNotificationStore()
const customRoles = useEntitlement('custom_roles')

const loading = ref(false)
const saving = ref(false)
const perms = ref<PermissionInfo[]>([])
const presets = ref<Record<string, string[]>>({})
const roles = ref<CustomRole[]>([])

const baseRoles: WorkspaceRole[] = ['admin', 'developer', 'viewer']

// The current admin's own permissions (from their built-in role preset). Used to
// disable permissions they cannot grant — mirroring the server's no-escalation.
const myPermissions = computed<Set<string>>(() => new Set(presets.value[props.myRole] ?? []))

// Permissions grouped by resource for the matrix.
const grouped = computed<Record<string, PermissionInfo[]>>(() => {
  const g: Record<string, PermissionInfo[]> = {}
  for (const p of perms.value) (g[p.resource] ??= []).push(p)
  return g
})

async function load() {
  loading.value = true
  try {
    const cat = (await permissionApi.catalog()).data.data
    perms.value = cat.permissions
    presets.value = Object.fromEntries(cat.roles.map((r) => [r.role, r.permissions]))
    if (customRoles.has.value) {
      roles.value = (await roleApi.list(props.wsId)).data.data ?? []
    }
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// --- Editor modal ---
const showModal = ref(false)
const editing = ref<CustomRole | null>(null)
const form = ref<{ name: string; base_role: string; permissions: Set<string> }>({
  name: '',
  base_role: 'viewer',
  permissions: new Set(),
})

function openCreate() {
  editing.value = null
  form.value = { name: '', base_role: 'viewer', permissions: new Set() }
  showModal.value = true
}
function openEdit(r: CustomRole) {
  editing.value = r
  form.value = { name: r.name, base_role: r.base_role, permissions: new Set(r.permissions) }
  showModal.value = true
}
function toggle(id: string) {
  if (form.value.permissions.has(id)) form.value.permissions.delete(id)
  else form.value.permissions.add(id)
}

async function save() {
  const body: CustomRoleInput = {
    name: form.value.name.trim(),
    base_role: form.value.base_role,
    permissions: [...form.value.permissions],
  }
  if (!body.name || body.permissions.length === 0) return
  saving.value = true
  try {
    if (editing.value) await roleApi.update(props.wsId, editing.value.id, body)
    else await roleApi.create(props.wsId, body)
    notify.success(editing.value ? 'Role updated' : 'Role created')
    showModal.value = false
    await load()
    emit('changed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const pendingDelete = ref<CustomRole | null>(null)
const deleting = ref(false)

async function confirmDelete() {
  if (!pendingDelete.value) return
  deleting.value = true
  try {
    await roleApi.remove(props.wsId, pendingDelete.value.id)
    notify.success('Role deleted')
    pendingDelete.value = null
    await load()
    emit('changed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-header">
      <h2>Custom roles</h2>
      <button
        v-if="customRoles.has.value"
        class="btn btn-primary btn-sm"
        @click="openCreate"
      >
        <span class="mdi mdi-plus"></span> New role
      </button>
    </div>

    <!-- Locked (Community / not entitled) -->
    <div v-if="!customRoles.has.value" class="card-body locked">
      <span class="mdi mdi-lock-outline"></span>
      <div>
        <p>Custom roles let you define fine-grained permission sets beyond the built-in roles.</p>
        <router-link to="/admin/license" class="btn btn-secondary btn-sm">Upgrade</router-link>
      </div>
    </div>

    <div v-else class="card-body">
      <div v-if="loading" class="spinner"></div>
      <table v-else-if="roles.length" class="table">
        <thead>
          <tr><th>Name</th><th>Base role</th><th>Permissions</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="r in roles" :key="r.id">
            <td class="cell-title">{{ r.name }}</td>
            <td><span class="badge badge-neutral">{{ r.base_role }}</span></td>
            <td class="text-muted">{{ r.permissions.length }} permission{{ r.permissions.length === 1 ? '' : 's' }}</td>
            <td class="text-right actions">
              <button class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(r)"><span class="mdi mdi-pencil"></span></button>
              <button class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingDelete = r"><span class="mdi mdi-delete"></span></button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="text-muted">No custom roles yet. Create one to grant a tailored permission set.</p>
    </div>

    <!-- Editor -->
    <Teleport to="body">
      <AppModal v-if="showModal" dialog-class="modal-lg" @close="showModal = false">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit role' : 'New role' }}</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-row">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="Deployer" aria-label="Name" required autofocus />
              </div>
              <div class="form-group">
                <label class="form-label">Base role (rank fallback)</label>
                <select v-model="form.base_role" class="form-select" aria-label="Base role (rank fallback)">
                  <option v-for="r in baseRoles" :key="r" :value="r">{{ r }}</option>
                </select>
              </div>
            </div>

            <div class="form-label">Permissions</div>
            <div class="matrix">
              <div v-for="(list, resource) in grouped" :key="resource" class="matrix-group">
                <div class="matrix-resource">{{ resource }}</div>
                <label
                  v-for="p in list"
                  :key="p.id"
                  class="perm"
                  :class="{ disabled: !myPermissions.has(p.id) }"
                  :title="!myPermissions.has(p.id) ? 'You cannot grant a permission you do not hold' : ''"
                >
                  <input
                    type="checkbox"
                    :checked="form.permissions.has(p.id)"
                    :disabled="!myPermissions.has(p.id)"
                    @change="toggle(p.id)"
                  />
                  {{ p.action }}
                </label>
              </div>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving || !form.name.trim() || form.permissions.size === 0">
              {{ saving ? 'Saving…' : editing ? 'Save' : 'Create role' }}
            </button>
          </div>
        </form>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete role"
      :message="pendingDelete ? `Delete role &quot;${pendingDelete.name}&quot;?` : ''"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.locked {
  display: flex;
  align-items: center;
  gap: 14px;
}
.locked .mdi {
  font-size: 28px;
  color: var(--text-muted);
}
.locked p {
  margin: 0 0 8px;
  color: var(--text-secondary, var(--text-muted));
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
.form-row {
  display: flex;
  gap: 16px;
  margin-bottom: 16px;
}
.form-row .form-group {
  flex: 1;
}
.matrix {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 14px;
  margin-top: 8px;
}
.matrix-resource {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-muted);
  margin-bottom: 6px;
}
.perm {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 0;
  font-size: 13px;
  cursor: pointer;
}
.perm.disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.modal-lg {
  max-width: 640px;
  width: 92vw;
}
</style>
