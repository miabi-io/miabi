<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useNotificationStore } from '@/stores/notification'
import { useWorkspaceStore } from '@/stores/workspace'
import { apiKeyApi } from '@/api/resources'
import { infoApi } from '@/api/info'
import type { ApiKey, ApiKeyCreated, AppInfo } from '@/api/types'
import { copyText } from '@/utils/clipboard'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const notify = useNotificationStore()
const ws = useWorkspaceStore()
const { workspaces, currentWorkspaceId } = storeToRefs(ws)
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const appInfo = ref<AppInfo | null>(null)

// Workspace binding: a key scoped to one workspace (the recommended default) vs.
// 'account' (account-wide — reaches all the user's workspaces).
const ACCOUNT = 'account' as const
const workspaceScope = ref<number | typeof ACCOUNT>(ACCOUNT)
function workspaceName(id?: number | null): string {
  if (!id) return 'Account-wide'
  return workspaces.value.find((w) => w.id === id)?.name ?? `Workspace #${id}`
}
async function copyWorkspaceId(id: number) {
  if (await copyText(String(id))) notify.success('Workspace ID copied')
  else notify.error('Could not copy ID')
}

const scopeOptions = [
  { value: 'read', label: 'Read', hint: 'Read-only access to all resources' },
  { value: 'write', label: 'Write', hint: 'Create, update, and delete resources' },
  { value: 'deploy', label: 'Deploy', hint: 'Trigger deployments and lifecycle actions' },
  { value: 'admin', label: 'Admin', hint: 'Administrative operations' },
  { value: 'registry_read', label: 'Registry: Pull', hint: 'Pull images from the container registry' },
  { value: 'registry_write', label: 'Registry: Push', hint: 'Push images to the container registry' },
]

// A key carrying only registry scopes is limited to docker login/push/pull — it
// is refused by the rest of the API. Surface that so it isn't a surprise.
const registryOnly = computed(
  () => scopes.value.length > 0 && scopes.value.every((s) => s.startsWith('registry_')),
)
const expiryOptions = [
  { label: 'Never', value: 'never' },
  { label: '30 days', value: '30' },
  { label: '60 days', value: '60' },
  { label: '90 days', value: '90' },
  { label: '180 days', value: '180' },
  { label: '365 days', value: '365' },
]

const showCreate = ref(false)
const creating = ref(false)
const name = ref('')
const scopes = ref<string[]>(['read'])
const expiry = ref('never')
const allowedIPs = ref('')

const createdKey = ref<ApiKeyCreated | null>(null)
const copied = ref(false)

async function load() {
  loading.value = true
  try {
    keys.value = (await apiKeyApi.list()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => {
  load()
  infoApi.get().then((res) => { appInfo.value = res.data.data }).catch(() => {})
})

function openCreate() {
  name.value = ''
  scopes.value = ['read']
  expiry.value = 'never'
  allowedIPs.value = ''
  // Default to a workspace-scoped key (least privilege) bound to the active
  // workspace; fall back to account-wide when there is no current workspace.
  workspaceScope.value = currentWorkspaceId.value ?? ACCOUNT
  createdKey.value = null
  copied.value = false
  showCreate.value = true
}

function toggleScope(value: string) {
  const i = scopes.value.indexOf(value)
  if (i === -1) scopes.value.push(value)
  else scopes.value.splice(i, 1)
}

function resolveExpiryDays(): number | undefined {
  if (expiry.value === 'never') return undefined
  return parseInt(expiry.value, 10)
}

async function create() {
  if (!name.value.trim() || scopes.value.length === 0) return
  creating.value = true
  try {
    const ips = allowedIPs.value
      .split(/[\n,]/)
      .map((ip) => ip.trim())
      .filter((ip) => ip.length > 0)
    createdKey.value = (
      await apiKeyApi.create({
        name: name.value.trim(),
        scopes: [...scopes.value],
        allowed_ips: ips.length > 0 ? ips : undefined,
        expires_in_days: resolveExpiryDays(),
        workspace_id: workspaceScope.value === ACCOUNT ? undefined : workspaceScope.value,
      })
    ).data.data
    notify.success('API key created — copy it now')
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    creating.value = false
  }
}

const toRevoke = ref<ApiKey | null>(null)
const revoking = ref(false)
async function confirmRevoke() {
  if (!toRevoke.value) return
  revoking.value = true
  try {
    await apiKeyApi.revoke(toRevoke.value.id)
    notify.success('API key revoked')
    toRevoke.value = null
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    revoking.value = false
  }
}

const toDelete = ref<ApiKey | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!toDelete.value) return
  deleting.value = true
  try {
    await apiKeyApi.remove(toDelete.value.id)
    notify.success('API key deleted')
    toDelete.value = null
    load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

async function copyKey() {
  if (!createdKey.value) return
  if (await copyText(createdKey.value.key)) {
    copied.value = true
    notify.success('Copied')
    setTimeout(() => (copied.value = false), 2000)
  } else {
    notify.error('Copy failed — select and copy the key manually')
  }
}

function isExpired(k: ApiKey): boolean {
  return !!k.expires_at && new Date(k.expires_at) < new Date()
}
function isActive(k: ApiKey): boolean {
  return !k.revoked && !isExpired(k)
}
function canDelete(k: ApiKey): boolean {
  return k.revoked || isExpired(k)
}
function status(k: ApiKey): { label: string; class: string } {
  if (k.revoked) return { label: 'revoked', class: 'badge-danger' }
  if (isExpired(k)) return { label: 'expired', class: 'badge-warning' }
  return { label: 'active', class: 'badge-success badge-dot' }
}
function formatDate(s: string | null): string {
  return s ? new Date(s).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : 'Never'
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>API Keys</h1>
      <button class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New API key
      </button>
    </div>

    <div v-if="appInfo?.openapi_docs" class="card api-docs-callout">
      <div class="api-docs-text">
        <strong>Developer resources</strong>
        <span>Authenticate with your API key and explore the full HTTP API.</span>
      </div>
      <div class="api-docs-links">
        <a class="btn btn-secondary btn-sm" href="/docs" target="_blank" rel="noopener noreferrer">
          <span class="mdi mdi-book-open-variant"></span> API Reference
        </a>
      </div>
    </div>

    <div class="card">
      <div v-if="loading && keys.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="keys.length === 0" class="empty-state">
        <span class="mdi mdi-key-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No API keys</h3>
        <p>Create a key to access the Miabi API programmatically.</p>
        <button class="btn btn-primary mt-4" @click="openCreate">Create an API key</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Scopes</th>
              <th>Allowed IPs</th>
              <th>Last used</th>
              <th>Expires</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="k.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-key-variant" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">
                      {{ k.name }}
                      <span class="ws-pill" :class="{ 'ws-pill-account': !k.workspace_id }">
                        <span class="mdi" :class="k.workspace_id ? 'mdi-folder-outline' : 'mdi-earth'"></span>
                        {{ workspaceName(k.workspace_id) }}
                      </span>
                    </span>
                    <span class="cell-sub"><code>{{ k.key_prefix }}…</code></span>
                  </span>
                </div>
              </td>
              <td>
                <span v-for="s in (k.scopes && k.scopes.length ? k.scopes : ['read'])" :key="s" class="scope-pill">{{ s }}</span>
              </td>
              <td class="cell-sub">
                <template v-if="k.allowed_ips && k.allowed_ips.length">
                  <code v-for="(ip, i) in k.allowed_ips.slice(0, 2)" :key="i" style="margin-right: 4px; font-size: 12px">{{ ip }}</code>
                  <span v-if="k.allowed_ips.length > 2" style="font-size: 12px; color: var(--text-muted)">+{{ k.allowed_ips.length - 2 }}</span>
                </template>
                <span v-else style="color: var(--text-muted)">Any</span>
              </td>
              <td class="cell-sub">{{ k.last_used_at ? formatDate(k.last_used_at) : 'Never' }}</td>
              <td class="cell-sub">{{ formatDate(k.expires_at) }}</td>
              <td><span class="badge" :class="status(k).class">{{ status(k).label }}</span></td>
              <td class="text-right">
                <div style="display: inline-flex; gap: 6px">
                  <button v-if="isActive(k)" class="btn btn-sm btn-warning" @click="toRevoke = k">Revoke</button>
                  <button v-if="canDelete(k)" class="btn btn-sm btn-danger" @click="toDelete = k">Delete</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <AppModal v-if="showCreate" @close="showCreate = false">
        <div class="modal-header">
          <h3>New API key</h3>
          <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showCreate = false"><span class="mdi mdi-close"></span></button>
        </div>

        <template v-if="!createdKey">
          <form @submit.prevent="create">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="name" class="form-input" placeholder="e.g. CI pipeline" aria-label="Name" required autofocus />
              </div>

              <div class="form-group">
                <label class="form-label">Workspace access</label>
                <select v-model="workspaceScope" class="form-select" aria-label="Workspace access">
                  <option v-for="w in workspaces" :key="w.id" :value="w.id">{{ w.name }}</option>
                  <option :value="ACCOUNT">Account-wide (all my workspaces)</option>
                </select>
                <small class="form-hint">
                  A workspace key only touches that workspace and needs no workspace id when used.
                  Account-wide keys reach all your workspaces — use only for cross-workspace automation.
                </small>
              </div>

              <div class="form-group">
                <label class="form-label">Expiration</label>
                <select v-model="expiry" class="form-select" aria-label="Expiration">
                  <option v-for="opt in expiryOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                </select>
                <small class="form-hint">"Never" means the key will not expire.</small>
              </div>

              <div class="form-group">
                <label class="form-label">Scopes</label>
                <div class="scope-list">
                  <label v-for="opt in scopeOptions" :key="opt.value" class="scope-option">
                    <input type="checkbox" :checked="scopes.includes(opt.value)" @change="toggleScope(opt.value)" />
                    <span class="scope-text">
                      <strong>{{ opt.label }}</strong>
                      <small>{{ opt.hint }}</small>
                    </span>
                  </label>
                </div>
                <small class="form-hint">Scopes are fixed at creation. To change them, create a new key.</small>
                <small v-if="registryOnly" class="form-hint registry-note">
                  This key carries only registry scopes — use it for <code>docker login</code> /
                  push / pull. It is rejected by the rest of the API.
                </small>
              </div>

              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Allowed IPs <span style="font-weight: 400; color: var(--text-muted)">(optional)</span></label>
                <textarea
                  v-model="allowedIPs"
                  class="form-input"
                  rows="3"
                  placeholder="Comma or newline separated, e.g.&#10;192.168.1.1&#10;10.0.0.0/24"
                  aria-label="Allowed IPs"
                ></textarea>
                <small class="form-hint">Restrict this key to specific IPs or CIDR ranges. Leave empty to allow all.</small>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showCreate = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="creating || !name.trim() || scopes.length === 0">
                {{ creating ? 'Creating…' : 'Create key' }}
              </button>
            </div>
          </form>
        </template>

        <template v-else>
          <div class="modal-body">
            <div class="app-banner app-banner--warning">
              <span class="mdi mdi-alert-outline app-banner-icon"></span>
              <div class="app-banner-content">
                <p class="app-banner-title">Copy your key now</p>
                <p class="app-banner-text">This is the only time the full key is shown.</p>
              </div>
            </div>
            <div class="code-block" style="margin-top: 14px">{{ createdKey.key }}</div>
            <p class="form-hint" style="margin-top: 8px">
              {{ createdKey.expires_at ? `Expires ${formatDate(createdKey.expires_at)}` : 'This key never expires.' }}
            </p>

            <div v-if="createdKey.workspace_id" class="created-ws">
              <div class="created-ws-info">
                <span class="created-ws-label">Workspace</span>
                <span class="created-ws-name">{{ workspaceName(createdKey.workspace_id) }}</span>
                <code>ID {{ createdKey.workspace_id }}</code>
              </div>
              <button type="button" class="btn btn-secondary btn-sm" @click="copyWorkspaceId(createdKey.workspace_id)">
                <span class="mdi mdi-content-copy"></span> Copy ID
              </button>
            </div>
            <p v-if="createdKey.workspace_id" class="form-hint" style="margin-top: 6px">
              This key is scoped to the workspace above — supply this workspace ID when targeting
              resources via the API, CLI, or Terraform.
            </p>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="copyKey">{{ copied ? 'Copied!' : 'Copy key' }}</button>
            <button type="button" class="btn btn-primary" @click="showCreate = false">Done</button>
          </div>
        </template>
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!toRevoke"
      title="Revoke API key"
      :message="`Revoke API key &quot;${toRevoke?.name}&quot;? Applications using it will immediately lose access. This cannot be undone.`"
      confirm-label="Revoke"
      variant="danger"
      :busy="revoking"
      @confirm="confirmRevoke"
      @cancel="toRevoke = null"
    />

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete API key"
      :message="`Permanently delete API key &quot;${toDelete?.name}&quot;? This removes it entirely and cannot be undone.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.api-docs-callout {
  display: flex; align-items: center; justify-content: space-between;
  gap: 16px; flex-wrap: wrap; padding: 16px 20px; margin-bottom: 20px;
}
.api-docs-text { display: flex; flex-direction: column; gap: 2px; }
.api-docs-text strong { font-size: 14px; color: var(--text-primary); }
.api-docs-text span { font-size: 13px; color: var(--text-muted); }
.api-docs-links { display: flex; gap: 8px; flex-shrink: 0; }
.api-docs-links .btn { text-decoration: none; }
.created-ws {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  margin-top: 14px; padding: 10px 14px;
  background: var(--bg-secondary, var(--primary-50)); border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}
.created-ws-info { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.created-ws-label { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; color: var(--text-muted); }
.created-ws-name { font-size: 13px; font-weight: 600; color: var(--text-primary); }
.created-ws-info code { font-family: 'JetBrains Mono', monospace; font-size: 12px; color: var(--text-primary); }
.ws-pill {
  display: inline-flex; align-items: center; gap: 3px;
  margin-left: 8px; padding: 1px 7px; font-size: 11px; font-weight: 500;
  border-radius: 10px; vertical-align: middle;
  background: var(--primary-50); color: var(--primary-600);
}
.ws-pill .mdi { font-size: 12px; }
.ws-pill-account { background: var(--warning-50, #fff7ed); color: var(--warning-600, #c2410c); }
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
