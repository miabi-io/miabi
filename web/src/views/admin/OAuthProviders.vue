<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { adminApi } from '@/api/admin'
import type { OAuthProviderPayload } from '@/api/admin'
import type { OAuthProvider } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const licenseStore = useLicenseStore()

// Entitlement gates: Community allows one provider and never hides providers;
// multi_sso lifts the count cap, sso_hidden_provider unlocks the Hidden toggle.
const multiSso = useEntitlement('multi_sso')
const hiddenCap = useEntitlement('sso_hidden_provider')

const providers = ref<OAuthProvider[]>([])
const loading = ref(false)

// atProviderCap: a new provider would exceed the Community single-provider limit.
const atProviderCap = computed(() => !multiSso.has.value && providers.value.length >= 1)
const capTitle = 'Community Edition allows one SSO provider — upgrade to an Enterprise license to add more'
const hiddenTitle = 'Hiding a provider from the login page requires an Enterprise license'

async function load() {
  loading.value = true
  try {
    const res = await adminApi.listProviders()
    providers.value = res.data.data
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

function providerIcon(p: OAuthProvider): string {
  return p.type === 'google' ? 'mdi-google' : 'mdi-shield-key-outline'
}

// --- Create / Edit modal ---
interface ProviderForm {
  name: string
  type: 'google' | 'oidc'
  slug: string
  client_id: string
  client_secret: string
  issuer: string
  auth_url: string
  token_url: string
  userinfo_url: string
  scopes: string
  allowed_domains: string
  enabled: boolean
  hidden: boolean
  auto_register: boolean
  email_claim: string
  name_claim: string
  username_claim: string
  // v-model on <input type="number"> replaces the string with a number as soon
  // as the field is non-empty — Vue casts by the element's type, not by the
  // declared one — so this is a string only while it is blank.
  default_workspace_id: number | string
  default_role: string
}

function emptyForm(): ProviderForm {
  return {
    name: '',
    type: 'google',
    slug: '',
    client_id: '',
    client_secret: '',
    issuer: '',
    auth_url: '',
    token_url: '',
    userinfo_url: '',
    scopes: '',
    allowed_domains: '',
    enabled: true,
    hidden: false,
    auto_register: true,
    email_claim: '',
    name_claim: '',
    username_claim: '',
    default_workspace_id: '',
    default_role: '',
  }
}

const showModal = ref(false)
const saving = ref(false)
const editing = ref<OAuthProvider | null>(null)
const form = ref<ProviderForm>(emptyForm())

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showModal.value = true
}

function openEdit(p: OAuthProvider) {
  editing.value = p
  form.value = {
    name: p.display_name ?? '',
    type: p.type,
    slug: p.name ?? '',
    client_id: '',
    client_secret: '',
    issuer: p.issuer ?? '',
    auth_url: p.auth_url ?? '',
    token_url: p.token_url ?? '',
    userinfo_url: p.userinfo_url ?? '',
    scopes: p.scopes ?? '',
    allowed_domains: p.allowed_domains ?? '',
    enabled: p.enabled,
    hidden: p.hidden,
    auto_register: p.auto_register,
    email_claim: p.email_claim ?? '',
    name_claim: p.name_claim ?? '',
    username_claim: p.username_claim ?? '',
    default_workspace_id: p.default_workspace_id ? String(p.default_workspace_id) : '',
    default_role: p.default_role ?? '',
  }
  showModal.value = true
}

function closeModal() {
  showModal.value = false
}

async function save() {
  const f = form.value
  if (!f.name.trim() || !f.client_id.trim()) return
  if (!editing.value && !f.client_secret.trim()) return

  const payload: OAuthProviderPayload = {
    display_name: f.name.trim(),
    type: f.type,
    client_id: f.client_id.trim(),
    enabled: f.enabled,
    hidden: f.hidden,
    auto_register: f.auto_register,
  }

  const handle = f.slug.trim()
  if (handle) payload.name = handle

  const scopes = f.scopes.trim()
  if (scopes) payload.scopes = scopes

  const domains = f.allowed_domains.trim()
  payload.allowed_domains = domains

  // Auto-join (both provider types). Send 0 to clear on edit, the id to set.
  // String() first: the field is a number once typed into (see AdminForm).
  const rawWs = String(f.default_workspace_id ?? '').trim()
  const wsId = rawWs ? parseInt(rawWs, 10) : NaN
  payload.default_workspace_id = Number.isFinite(wsId) && wsId > 0 ? wsId : 0
  payload.default_role = f.default_role.trim()

  if (f.type === 'oidc') {
    payload.issuer = f.issuer.trim()
    const authUrl = f.auth_url.trim()
    const tokenUrl = f.token_url.trim()
    const userinfoUrl = f.userinfo_url.trim()
    if (authUrl) payload.auth_url = authUrl
    if (tokenUrl) payload.token_url = tokenUrl
    if (userinfoUrl) payload.userinfo_url = userinfoUrl
    payload.email_claim = f.email_claim.trim()
    payload.name_claim = f.name_claim.trim()
    payload.username_claim = f.username_claim.trim()
  }

  const secret = f.client_secret.trim()
  if (secret) payload.client_secret = secret

  saving.value = true
  try {
    if (editing.value) {
      await adminApi.updateProvider(editing.value.id, payload)
      notify.success('Provider updated')
    } else {
      await adminApi.createProvider(payload)
      notify.success('Provider created')
    }
    showModal.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

const pendingDelete = ref<OAuthProvider | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!pendingDelete.value) return
  deleting.value = true
  try {
    await adminApi.deleteProvider(pendingDelete.value.id)
    notify.success('Provider deleted')
    pendingDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  load()
  licenseStore.load() // idempotent; ensures entitlements are available for the gates
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>OAuth Providers</h1>
      <button
        class="btn btn-primary"
        :disabled="atProviderCap"
        :title="atProviderCap ? capTitle : ''"
        @click="openCreate"
      >
        <span class="mdi mdi-plus"></span> Add provider
      </button>
    </div>

    <div v-if="atProviderCap" class="cap-note">
      <span class="mdi mdi-lock-outline"></span>
      <span>{{ capTitle }}.</span>
      <router-link to="/admin/license" class="cap-link">Manage license →</router-link>
    </div>

    <div class="card">
      <div v-if="loading && providers.length === 0" class="card-body">
        <span class="spinner"></span>
      </div>

      <div v-else-if="providers.length === 0" class="empty-state">
        <span
          class="mdi mdi-shield-key-outline"
          style="font-size: 44px; color: var(--text-muted)"
        ></span>
        <h3>No SSO providers</h3>
        <p class="text-muted">
          Add Google or a generic OIDC provider to enable single sign-on.
        </p>
        <button class="btn btn-primary mt-4" @click="openCreate">Add provider</button>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>Provider</th>
              <th>Type</th>
              <th>Status</th>
              <th>Visibility</th>
              <th>Auto-register</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="p in providers"
              :key="p.id"
              class="row-clickable"
              @click="openEdit(p)"
            >
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm">
                    <span class="mdi" :class="providerIcon(p)"></span>
                  </span>
                  <span class="cell-text">
                    <span class="cell-title">{{ p.display_name || p.name }}</span>
                    <span class="cell-sub">{{ p.name }}</span>
                  </span>
                </div>
              </td>
              <td>
                <span class="badge">{{ p.type === 'google' ? 'Google' : 'OIDC' }}</span>
              </td>
              <td>
                <span v-if="p.enabled" class="badge badge-dot badge-success">Enabled</span>
                <span v-else class="badge badge-dot badge-warning">Disabled</span>
              </td>
              <td>
                <span v-if="p.hidden" class="text-muted">Hidden</span>
                <span v-else>Visible</span>
              </td>
              <td>{{ p.auto_register ? 'Yes' : 'No' }}</td>
              <td class="text-right actions" @click.stop>
                <button class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(p)">
                  <span class="mdi mdi-pencil"></span>
                </button>
                <button class="btn-icon btn-icon-muted" title="Delete" aria-label="Delete" @click="pendingDelete = p">
                  <span class="mdi mdi-delete"></span>
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Create / Edit modal -->
    <Teleport to="body">
      <div v-if="showModal" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ editing ? 'Edit provider' : 'Add provider' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="closeModal">
              <span class="mdi mdi-close"></span>
            </button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Display name</label>
                <input
                  v-model="form.name"
                  class="form-input"
                  placeholder="Google Workspace"
                  required
                  autofocus
                />
              </div>

              <div class="form-group">
                <label class="form-label">Type</label>
                <select v-model="form.type" class="form-select">
                  <option value="google">Google</option>
                  <option value="oidc">Generic OIDC</option>
                </select>
              </div>

              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.slug" class="form-input" placeholder="google" />
                <span class="form-hint">Auto-generated from name if blank.</span>
              </div>

              <div class="form-group">
                <label class="form-label">Client ID</label>
                <input v-model="form.client_id" class="form-input" required />
              </div>

              <div class="form-group">
                <label class="form-label">Client Secret</label>
                <input
                  v-model="form.client_secret"
                  class="form-input"
                  type="password"
                  :placeholder="editing ? 'Leave blank to keep current' : ''"
                  :required="!editing"
                />
              </div>

              <template v-if="form.type === 'oidc'">
                <div class="form-group">
                  <label class="form-label">Issuer</label>
                  <input
                    v-model="form.issuer"
                    class="form-input"
                    placeholder="https://id.example.com"
                  />
                  <span class="form-hint">OIDC discovery base URL, e.g. https://id.example.com</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Auth URL</label>
                  <input v-model="form.auth_url" class="form-input" />
                  <span class="form-hint">Leave blank to use discovery.</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Token URL</label>
                  <input v-model="form.token_url" class="form-input" />
                  <span class="form-hint">Leave blank to use discovery.</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Userinfo URL</label>
                  <input v-model="form.userinfo_url" class="form-input" />
                  <span class="form-hint">Leave blank to use discovery.</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Email claim</label>
                  <input v-model="form.email_claim" class="form-input" placeholder="email" />
                  <span class="form-hint">Userinfo claim mapped to the user's email. Blank = standard "email".</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Name claim</label>
                  <input v-model="form.name_claim" class="form-input" placeholder="name" />
                  <span class="form-hint">Userinfo claim mapped to the display name. Blank = standard "name".</span>
                </div>
                <div class="form-group">
                  <label class="form-label">Username claim</label>
                  <input v-model="form.username_claim" class="form-input" placeholder="preferred_username" />
                  <span class="form-hint">
                    Userinfo claim mapped to the handle. Blank = standard "preferred_username"; if the provider
                    sends neither, the handle is derived from the email address.
                  </span>
                </div>
              </template>

              <div class="form-group">
                <label class="form-label">Scopes</label>
                <input v-model="form.scopes" class="form-input" />
                <span class="form-hint">Default: openid email profile</span>
              </div>

              <div class="form-group">
                <label class="form-label">Allowed domains</label>
                <input v-model="form.allowed_domains" class="form-input" />
                <span class="form-hint">CSV of allowed email domains; blank = any.</span>
              </div>

              <div class="form-group">
                <label class="form-label">Auto-join workspace</label>
                <div class="autojoin-row">
                  <input
                    v-model="form.default_workspace_id"
                    class="form-input"
                    type="number"
                    min="0"
                    placeholder="Workspace ID"
                    aria-label="Auto-join workspace ID"
                  />
                  <select v-model="form.default_role" class="form-select" aria-label="Auto-join role">
                    <option value="">No auto-join</option>
                    <option value="viewer">Viewer</option>
                    <option value="developer">Developer</option>
                    <option value="admin">Admin</option>
                  </select>
                </div>
                <span class="form-hint">New SSO users join this workspace with the chosen role. Blank ID = none.</span>
              </div>

              <div class="form-group toggles" style="margin-bottom: 0">
                <label class="check-row">
                  <input v-model="form.enabled" type="checkbox" />
                  Enabled
                </label>
                <label class="check-row" :class="{ 'check-disabled': !hiddenCap.has.value && !form.hidden }">
                  <input
                    v-model="form.hidden"
                    type="checkbox"
                    :disabled="!hiddenCap.has.value && !form.hidden"
                  />
                  Hidden
                  <span v-if="!hiddenCap.has.value && !form.hidden" class="mdi mdi-lock-outline cap-lock" :title="hiddenTitle"></span>
                </label>
                <label class="check-row">
                  <input v-model="form.auto_register" type="checkbox" />
                  Auto-register users
                </label>
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="closeModal">Cancel</button>
              <button
                type="submit"
                class="btn btn-primary"
                :disabled="
                  saving ||
                  !form.name.trim() ||
                  !form.client_id.trim() ||
                  (!editing && !form.client_secret.trim())
                "
              >
                {{ saving ? 'Saving…' : editing ? 'Save' : 'Add provider' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete provider"
      :message="`Delete provider &quot;${pendingDelete?.name}&quot;? This cannot be undone.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.toggles {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.check-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
.text-right {
  text-align: right;
}
.cap-note {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
  padding: 10px 14px;
  border-radius: 8px;
  font-size: 13px;
  background: var(--warning-bg, rgba(245, 158, 11, 0.1));
  color: var(--warning, #b45309);
}
.cap-note .mdi {
  font-size: 16px;
}
.cap-link {
  margin-left: auto;
  font-weight: 600;
  white-space: nowrap;
  color: inherit;
  text-decoration: none;
}
.check-disabled {
  opacity: 0.6;
}
.cap-lock {
  color: var(--text-muted);
  font-size: 14px;
}
.autojoin-row {
  display: flex;
  gap: 8px;
}
.autojoin-row .form-input {
  flex: 1;
}
</style>
