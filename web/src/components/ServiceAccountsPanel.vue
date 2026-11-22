<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { serviceAccountApi } from '@/api/resources'
import type { ApiKey, ApiKeyCreated, User, WorkspaceRole } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { copyText } from '@/utils/clipboard'
import { fmtDate } from '@/utils/datetime'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const props = defineProps<{ wsId: number }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const notify = useNotificationStore()

const roles: Exclude<WorkspaceRole, 'owner'>[] = ['viewer', 'developer', 'admin']

const scopeOptions = [
  { value: 'read', label: 'apiKeys.scope.read', hint: 'apiKeys.scope.readHint' },
  { value: 'write', label: 'apiKeys.scope.write', hint: 'apiKeys.scope.writeHint' },
  { value: 'deploy', label: 'apiKeys.scope.deploy', hint: 'apiKeys.scope.deployHint' },
  { value: 'admin', label: 'apiKeys.scope.admin', hint: 'apiKeys.scope.adminHint' },
  { value: 'registry_read', label: 'apiKeys.scope.registry_read', hint: 'apiKeys.scope.registry_readHint' },
  { value: 'registry_write', label: 'apiKeys.scope.registry_write', hint: 'apiKeys.scope.registry_writeHint' },
]
const expiryOptions = [
  { label: 'apiKeys.expiry.never', value: 'never' },
  { label: 'apiKeys.expiry.d30', value: '30' },
  { label: 'apiKeys.expiry.d60', value: '60' },
  { label: 'apiKeys.expiry.d90', value: '90' },
  { label: 'apiKeys.expiry.d180', value: '180' },
  { label: 'apiKeys.expiry.d365', value: '365' },
]

const accounts = ref<User[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    accounts.value = (await serviceAccountApi.list(props.wsId)).data.data ?? []
    if (selected.value && !accounts.value.some((a) => a.id === selected.value?.id)) selected.value = null
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.wsId, () => {
  selected.value = null
  load()
})

const showCreate = ref(false)
const creating = ref(false)
const createForm = ref<{ name: string; role: string }>({ name: '', role: 'developer' })
function openCreate() {
  createForm.value = { name: '', role: 'developer' }
  showCreate.value = true
}
async function create() {
  if (!createForm.value.name.trim()) return
  creating.value = true
  try {
    const sa = (await serviceAccountApi.create(props.wsId, createForm.value.name.trim(), createForm.value.role)).data.data
    notify.success(t('notify.serviceAccounts.created'))
    showCreate.value = false
    await load()
    emit('changed')
    if (sa) selectAccount(sa)
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

const toDelete = ref<User | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!toDelete.value) return
  deleting.value = true
  try {
    await serviceAccountApi.remove(props.wsId, toDelete.value.id)
    notify.success(t('notify.serviceAccounts.deleted'))
    if (selected.value?.id === toDelete.value.id) selected.value = null
    toDelete.value = null
    await load()
    emit('changed')
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copyAccountId(id: number) {
  if (await copyText(String(id))) notify.success(t('notify.serviceAccounts.idCopied'))
  else notify.error(t('notify.common.couldNotCopyId'))
}

const selected = ref<User | null>(null)
const keys = ref<ApiKey[]>([])
const keysLoading = ref(false)

function selectAccount(sa: User) {
  if (selected.value?.id === sa.id) {
    selected.value = null
    return
  }
  selected.value = sa
  loadKeys()
}
async function loadKeys() {
  if (!selected.value) return
  keysLoading.value = true
  try {
    keys.value = (await serviceAccountApi.keys(props.wsId, selected.value.id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    keysLoading.value = false
  }
}

const showKey = ref(false)
const creatingKey = ref(false)
const keyName = ref('')
const keyScopes = ref<string[]>(['read'])
const keyExpiry = ref('never')
const keyAllowedIPs = ref('')
const createdKey = ref<ApiKeyCreated | null>(null)
const copied = ref(false)

const registryOnly = computed(
  () => keyScopes.value.length > 0 && keyScopes.value.every((s) => s.startsWith('registry_')),
)

function openCreateKey() {
  keyName.value = ''
  keyScopes.value = ['read']
  keyExpiry.value = 'never'
  keyAllowedIPs.value = ''
  createdKey.value = null
  copied.value = false
  showKey.value = true
}
function toggleScope(value: string) {
  const i = keyScopes.value.indexOf(value)
  if (i === -1) keyScopes.value.push(value)
  else keyScopes.value.splice(i, 1)
}
async function createKey() {
  if (!selected.value || !keyName.value.trim() || keyScopes.value.length === 0) return
  creatingKey.value = true
  try {
    const ips = keyAllowedIPs.value
      .split(/[\n,]/)
      .map((ip) => ip.trim())
      .filter((ip) => ip.length > 0)
    createdKey.value = (
      await serviceAccountApi.createKey(props.wsId, selected.value.id, {
        name: keyName.value.trim(),
        scopes: [...keyScopes.value],
        allowed_ips: ips.length > 0 ? ips : undefined,
        expires_in_days: keyExpiry.value === 'never' ? undefined : parseInt(keyExpiry.value, 10),
      })
    ).data.data
    notify.success(t('notify.apiKeys.created'))
    loadKeys()
  } catch (e) {
    notify.apiError(e)
  } finally {
    creatingKey.value = false
  }
}
async function copyKey() {
  if (!createdKey.value) return
  if (await copyText(createdKey.value.key)) {
    copied.value = true
    notify.success(t('notify.common.copied'))
    setTimeout(() => (copied.value = false), 2000)
  } else {
    notify.error(t('notify.apiKeys.copyFailedSelectAndCopy'))
  }
}

const toRevoke = ref<ApiKey | null>(null)
const revoking = ref(false)
async function confirmRevoke() {
  if (!toRevoke.value || !selected.value) return
  revoking.value = true
  try {
    await serviceAccountApi.revokeKey(props.wsId, selected.value.id, toRevoke.value.id)
    notify.success(t('notify.apiKeys.revoked'))
    toRevoke.value = null
    loadKeys()
  } catch (e) {
    notify.apiError(e)
  } finally {
    revoking.value = false
  }
}

function isExpired(k: ApiKey): boolean {
  return !!k.expires_at && new Date(k.expires_at) < new Date()
}
function status(k: ApiKey): { label: string; class: string } {
  if (k.revoked) return { label: 'apiKeys.status.revoked', class: 'badge-danger' }
  if (isExpired(k)) return { label: 'apiKeys.status.expired', class: 'badge-warning' }
  return { label: 'apiKeys.status.active', class: 'badge-success badge-dot' }
}
</script>

<template>
  <div>
    <div class="card mb-4">
      <div class="card-header">
        <h2>{{ $t('serviceAccounts.title') }}</h2>
        <div class="header-actions">
          <button class="btn btn-primary btn-sm" @click="openCreate">
            <span class="mdi mdi-plus"></span>{{ $t('serviceAccounts.new') }}</button>
        </div>
      </div>
      <div class="card-body intro">
        <p class="text-muted text-sm">{{ $t('serviceAccounts.intro') }}</p>
      </div>
      <div v-if="loading && accounts.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="accounts.length === 0" class="empty-state">
        <span class="mdi mdi-robot-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>{{ $t('serviceAccounts.empty') }}</h3>
        <p>{{ $t('serviceAccounts.emptyHint') }}</p>
        <button class="btn btn-primary mt-4" @click="openCreate">{{ $t('serviceAccounts.new') }}</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>{{ $t('apps.form.name') }}</th>
              <th>{{ $t('serviceAccounts.id') }}</th>
              <th>{{ $t('serviceAccounts.created') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="sa in accounts" :key="sa.id" :class="{ 'row-selected': selected?.id === sa.id }">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-robot-outline" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ sa.name }}</span>
                    <span v-if="sa.username" class="cell-sub mono">@{{ sa.username }}</span>
                  </span>
                </div>
              </td>
              <td>
                <button class="id-chip mono" :title="$t('apiKeys.copyId')" @click="copyAccountId(sa.id)">
                  {{ sa.id }}<span class="mdi mdi-content-copy"></span>
                </button>
              </td>
              <td class="cell-sub">{{ fmtDate(sa.created_at) }}</td>
              <td class="text-right">
                <div class="actions">
                  <button class="btn btn-sm btn-secondary" @click="selectAccount(sa)">
                    <span class="mdi mdi-key-variant"></span>{{ selected?.id === sa.id ? $t('serviceAccounts.hideKeys') : $t('serviceAccounts.keys') }}</button>
                  <button class="btn-icon btn-icon-danger" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="toDelete = sa">
                    <span class="mdi mdi-delete-outline"></span>
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="selected" class="card">
      <div class="card-header">
        <h2>{{ $t('serviceAccounts.keysFor', { name: selected.name }) }}</h2>
        <button class="btn btn-primary btn-sm" @click="openCreateKey">
          <span class="mdi mdi-plus"></span>{{ $t('apiKeys.newApiKey') }}</button>
      </div>
      <div v-if="keysLoading && keys.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="keys.length === 0" class="card-body">
        <p class="text-muted text-sm">{{ $t('serviceAccounts.noKeys') }}</p>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>{{ $t('apps.form.name') }}</th>
              <th>{{ $t('apiKeys.scopes') }}</th>
              <th>{{ $t('apiKeys.allowedIps') }}</th>
              <th>{{ $t('apiKeys.lastUsed') }}</th>
              <th>{{ $t('apiKeys.expires') }}</th>
              <th>{{ $t('dashboard.col.status') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id">
              <td>
                <span class="cell-text">
                  <span class="cell-title">
                    {{ k.name }}
                  </span>
                  <span class="cell-sub"><code>{{ k.key_prefix }}…</code></span>
                </span>
              </td>
              <td>
                <span v-for="s in (k.scopes && k.scopes.length ? k.scopes : ['read'])" :key="s" class="scope-pill">{{ s }}</span>
              </td>
              <td class="cell-sub">
                <template v-if="k.allowed_ips && k.allowed_ips.length">
                  <code v-for="(ip, i) in k.allowed_ips.slice(0, 2)" :key="i" style="margin-right: 4px; font-size: 12px">{{ ip }}</code>
                  <span v-if="k.allowed_ips.length > 2" style="font-size: 12px; color: var(--text-muted)">+{{ k.allowed_ips.length - 2 }}</span>
                </template>
                <span v-else style="color: var(--text-muted)">{{ $t('apiKeys.any') }}</span>
              </td>
              <td class="cell-sub">{{ k.last_used_at ? fmtDate(k.last_used_at) : $t('serviceAccounts.never') }}</td>
              <td class="cell-sub">{{ k.expires_at ? fmtDate(k.expires_at) : $t('serviceAccounts.never') }}</td>
              <td><span class="badge" :class="status(k).class">{{ $t(status(k).label) }}</span></td>
              <td class="text-right">
                <button v-if="!k.revoked && !isExpired(k)" class="btn btn-sm btn-warning" @click="toRevoke = k">{{ $t('apiKeys.revoke') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>{{ $t('serviceAccounts.new') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="create">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="createForm.name" class="form-input" :placeholder="$t('serviceAccounts.namePlaceholder')" :aria-label="$t('apps.form.name')" required autofocus />
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('wsSettings.role') }}</label>
              <select v-model="createForm.role" class="form-select" :aria-label="$t('wsSettings.role')">
                <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
              </select>
              <small class="form-hint">{{ $t('serviceAccounts.roleHint') }}</small>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showCreate = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="creating || !createForm.name.trim()">
              {{ creating ? $t('serviceAccounts.creating') : $t('serviceAccounts.create') }}
            </button>
          </div>
        </form>
      </AppModal>

      <AppModal v-if="showKey && selected" @close="showKey = false">
        <div class="modal-header">
          <h3>{{ $t('serviceAccounts.newKeyFor', { name: selected.name }) }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showKey = false"><span class="mdi mdi-close"></span></button>
        </div>

        <template v-if="!createdKey">
          <form @submit.prevent="createKey">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">{{ $t('apps.form.name') }}</label>
                <input v-model="keyName" class="form-input" :placeholder="$t('apiKeys.namePlaceholder')" :aria-label="$t('apps.form.name')" required autofocus />
              </div>

              <div class="form-group">
                <label class="form-label">{{ $t('apiKeys.expiration') }}</label>
                <select v-model="keyExpiry" class="form-select" :aria-label="$t('apiKeys.expiration')">
                  <option v-for="opt in expiryOptions" :key="opt.value" :value="opt.value">{{ $t(opt.label) }}</option>
                </select>
              </div>

              <div class="form-group">
                <label class="form-label">{{ $t('apiKeys.scopes') }}</label>
                <div class="scope-list">
                  <label v-for="opt in scopeOptions" :key="opt.value" class="scope-option">
                    <input type="checkbox" :checked="keyScopes.includes(opt.value)" @change="toggleScope(opt.value)" />
                    <span class="scope-text">
                      <strong>{{ $t(opt.label) }}</strong>
                      <small>{{ $t(opt.hint) }}</small>
                    </span>
                  </label>
                </div>
                <small class="form-hint">{{ $t('apiKeys.scopesFixed') }}</small>
                <small v-if="registryOnly" class="form-hint registry-note">
                  <i18n-t keypath="apiKeys.registryOnlyNote" tag="span"><template #cmd><code>docker login</code></template></i18n-t>
                </small>
              </div>

              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">{{ $t('apiKeys.allowedIps') }}<span style="font-weight: 400; color: var(--text-muted)">{{ $t('apiKeys.optional') }}</span></label>
                <textarea
                  v-model="keyAllowedIPs"
                  class="form-input"
                  rows="3"
                  :placeholder="$t('apiKeys.allowedIpsPlaceholder')"
                  :aria-label="$t('apiKeys.allowedIps')"
                ></textarea>
                <small class="form-hint">{{ $t('apiKeys.allowedIpsHint') }}</small>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showKey = false">{{ $t('action.cancel') }}</button>
              <button type="submit" class="btn btn-primary" :disabled="creatingKey || !keyName.trim() || keyScopes.length === 0">
                {{ creatingKey ? $t('serviceAccounts.creating') : $t('serviceAccounts.createKey') }}
              </button>
            </div>
          </form>
        </template>

        <template v-else>
          <div class="modal-body">
            <div class="app-banner app-banner--warning">
              <span class="mdi mdi-alert-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-title">{{ $t('apiKeys.copyYourKeyNow') }}</p>
                <p class="app-banner-text">{{ $t('apiKeys.onlyTimeShown') }}</p>
              </div>
            </div>
            <div class="code-block" style="margin-top: 14px">{{ createdKey.key }}</div>
            <p class="form-hint" style="margin-top: 8px">
              {{ createdKey.expires_at ? $t('serviceAccounts.expiresOn', { date: fmtDate(createdKey.expires_at) }) : $t('serviceAccounts.neverExpires') }}
            </p>
            <p v-if="createdKey.workspace_id" class="form-hint" style="margin-top: 6px">{{ $t('apiKeys.workspaceScopedHint') }}</p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copyKey">{{ copied ? $t('serviceAccounts.copied') : $t('serviceAccounts.copyKey') }}</button>
            <button type="button" class="btn btn-primary" @click="showKey = false">{{ $t('apiKeys.done') }}</button>
          </div>
        </template>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      :title="$t('confirm.title.deleteServiceAccount')"
      :message="toDelete ? $t('confirm.message.serviceAccounts.delete', { name: toDelete.name }) : ''"
      :confirm-label="$t('action.delete')"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />

    <ConfirmDialog
      :open="!!toRevoke"
      :title="$t('confirm.title.revokeApiKey')"
      :message="$t('confirm.message.apiKeys.revokeApiKeyName', { name: toRevoke?.name })"
      :confirm-label="$t('action.revoke')"
      variant="danger"
      :busy="revoking"
      @confirm="confirmRevoke"
      @cancel="toRevoke = null"
    />
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.header-actions,
.actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.intro { padding-top: 0; padding-bottom: 8px; }
.intro p { margin: 0; }
.row-selected td { background: var(--bg-secondary, var(--primary-50)); }
.id-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 1px 7px; font-size: 12px; border-radius: 4px; cursor: pointer;
  border: 1px solid var(--border-primary); background: var(--bg-tertiary); color: var(--text-secondary);
}
.id-chip .mdi { font-size: 12px; color: var(--text-muted); }
.scope-list { display: flex; flex-direction: column; gap: 10px; }
.scope-option { display: flex; align-items: flex-start; gap: 8px; cursor: pointer; }
.scope-option input { margin-top: 3px; width: 16px; height: 16px; accent-color: var(--primary-600); }
.scope-text { display: flex; flex-direction: column; gap: 1px; }
.scope-text strong { font-size: 13px; color: var(--text-primary); }
.scope-text small { font-size: 12px; color: var(--text-muted); }
.registry-note { margin-top: 6px; color: var(--primary-600); }
.registry-note code { font-family: var(--font-mono, monospace); font-size: 11px; }
.scope-pill {
  display: inline-block;
  padding: 2px 8px;
  margin-right: 4px;
  font-size: 12px;
  border-radius: 4px;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}
.form-hint { display: block; margin-top: 4px; font-size: 12px; color: var(--text-muted); }
</style>
