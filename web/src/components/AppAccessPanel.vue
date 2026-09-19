<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { permissionApi, policyApi } from '@/api/rbac'
import { memberApi } from '@/api/resources'
import type { PermissionInfo, ResourcePolicy, Member } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const props = defineProps<{ wsId: number; appId: number }>()

const notify = useNotificationStore()
const policies = useEntitlement('resource_policies')

const loading = ref(false)
const granting = ref(false)
const appPerms = ref<PermissionInfo[]>([])
const grants = ref<ResourcePolicy[]>([])
const members = ref<Member[]>([])

const form = ref<{ userId: number | null; permissions: Set<string> }>({ userId: null, permissions: new Set() })

// Members not already granted, eligible for a new grant.
const grantableMembers = computed(() =>
  members.value.filter((m) => !grants.value.some((g) => g.user_id === m.user_id)),
)

function memberName(userId: number): string {
  const m = members.value.find((x) => x.user_id === userId)
  return m ? m.user.name : `user #${userId}`
}

async function load() {
  if (!policies.has.value || !props.wsId) return
  loading.value = true
  try {
    const cat = (await permissionApi.catalog()).data.data
    appPerms.value = cat.permissions.filter((p) => p.resource === 'app')
    grants.value = (await policyApi.listApp(props.wsId, props.appId)).data.data ?? []
    members.value = (await memberApi.list(props.wsId)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function toggle(id: string) {
  if (form.value.permissions.has(id)) form.value.permissions.delete(id)
  else form.value.permissions.add(id)
}

async function grant() {
  if (!form.value.userId || form.value.permissions.size === 0) return
  granting.value = true
  try {
    await policyApi.grantApp(props.wsId, props.appId, form.value.userId, [...form.value.permissions])
    notify.success('Access granted')
    form.value = { userId: null, permissions: new Set() }
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    granting.value = false
  }
}

const pendingRevoke = ref<ResourcePolicy | null>(null)
const revoking = ref(false)

async function confirmRevoke() {
  if (!pendingRevoke.value) return
  revoking.value = true
  try {
    await policyApi.revokeApp(props.wsId, props.appId, pendingRevoke.value.user_id)
    notify.success('Access revoked')
    pendingRevoke.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    revoking.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-header"><h2>App access</h2></div>

    <div v-if="!policies.has.value" class="card-body locked">
      <span class="mdi mdi-lock-outline"></span>
      <div>
        <p>Per-resource access policies let you grant a member rights on this app only — even if they're a viewer in the workspace.</p>
        <router-link to="/admin/license" class="btn btn-secondary btn-sm">Upgrade</router-link>
      </div>
    </div>

    <div v-else class="card-body">
      <div v-if="loading" class="spinner"></div>
      <template v-else>
        <p class="text-muted hint">Members below have extra permissions on this app, in addition to their workspace role.</p>
        <table v-if="grants.length" class="table">
          <thead><tr><th>Member</th><th>Permissions</th><th></th></tr></thead>
          <tbody>
            <tr v-for="g in grants" :key="g.id">
              <td class="cell-title">{{ memberName(g.user_id) }}</td>
              <td>
                <span v-for="p in g.permissions" :key="p" class="badge badge-neutral perm-badge">{{ p }}</span>
              </td>
              <td class="text-right">
                <button class="btn-icon btn-icon-danger" title="Revoke" aria-label="Revoke" @click="pendingRevoke = g"><span class="mdi mdi-delete"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-else class="text-muted">No per-app grants yet.</p>

        <div class="grant-form">
          <div class="form-label">Grant access</div>
          <div class="grant-row">
            <select v-model.number="form.userId" class="form-select" aria-label="Grant access">
              <option :value="null" disabled>Select member…</option>
              <option v-for="m in grantableMembers" :key="m.user_id" :value="m.user_id">{{ m.user.name }} ({{ m.user.email }})</option>
            </select>
            <div class="perm-checks">
              <label v-for="p in appPerms" :key="p.id" class="perm">
                <input type="checkbox" :checked="form.permissions.has(p.id)" @change="toggle(p.id)" />
                {{ p.action }}
              </label>
            </div>
            <button class="btn btn-primary btn-sm" :disabled="granting || !form.userId || form.permissions.size === 0" @click="grant">
              {{ granting ? 'Granting…' : 'Grant' }}
            </button>
          </div>
        </div>
      </template>
    </div>

    <ConfirmDialog
      :open="!!pendingRevoke"
      :title="$t('confirm.title.revokeAccess')"
      :message="pendingRevoke ? `Revoke ${memberName(pendingRevoke.user_id)}'s access to this app?` : ''"
      :confirm-label="$t('action.revoke')"
      variant="danger"
      :busy="revoking"
      @confirm="confirmRevoke"
      @cancel="pendingRevoke = null"
    />
  </div>
</template>

<style scoped>
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
  max-width: 60ch;
}
.hint {
  margin: 0 0 14px;
}
.perm-badge {
  margin-right: 4px;
  font-family: var(--font-mono, monospace);
  font-size: 11px;
}
.grant-form {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border-primary);
}
.grant-row {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
}
.perm-checks {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.perm {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
}
</style>
