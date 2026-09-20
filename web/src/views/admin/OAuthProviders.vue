<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { adminApi } from '@/api/admin'
import type { OAuthProviderPayload } from '@/api/admin'
import type { OAuthProvider } from '@/api/types'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

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
      <h1>{{ $t('adminNav.identity.oauthProviders') }}</h1>
      <button
        class="btn btn-primary"
        :disabled="atProviderCap"
        :title="atProviderCap ? capTitle : ''"
        @click="openCreate"
      >
        <span class="mdi mdi-plus"></span>{{ $t('oauth.addProvider') }}</button>
    </div>

    <div v-if="atProviderCap" class="cap-note">
      <span class="mdi mdi-lock-outline"></span>
      <span>{{ capTitle }}.</span>
      <router-link to="/admin/license" class="cap-link">{{ $t('plans.manageLicense') }}</router-link>
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
        <h3>{{ $t('oauth.noSsoProviders') }}</h3>
        <p class="text-muted">{{ $t('oauth.emptyHint') }}</p>
        <button class="btn btn-primary mt-4" @click="openCreate">{{ $t('oauth.addProvider') }}</button>
      </div>

      <div v-else class="table-wrapper">
        <table>
          <thead>
            <tr>
              <th>{{ $t('oauth.provider') }}</th>
              <th>{{ $t('oauth.type') }}</th>
              <th>{{ $t('dashboard.col.status') }}</th>
              <th>{{ $t('oauth.visibility') }}</th>
              <th>{{ $t('oauth.autoRegister') }}</th>
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
                <span v-if="p.enabled" class="badge badge-dot badge-success">{{ $t('jobs.enabled') }}</span>
                <span v-else class="badge badge-dot badge-warning">{{ $t('oauth.disabled') }}</span>
              </td>
              <td>
                <span v-if="p.hidden" class="text-muted">{{ $t('oauth.hidden') }}</span>
                <span v-else>{{ $t('oauth.visible') }}</span>
              </td>
              <td>{{ p.auto_register ? 'Yes' : 'No' }}</td>
              <td class="text-right actions" @click.stop>
                <button class="btn-icon btn-icon-muted" :title="$t('action.edit')" :aria-label="$t('action.edit')" @click="openEdit(p)">
                  <span class="mdi mdi-pencil"></span>
                </button>
                <button class="btn-icon btn-icon-muted" :title="$t('action.delete')" :aria-label="$t('action.delete')" @click="pendingDelete = p">
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
      <AppModal v-if="showModal" @close="closeModal">
        <div class="modal-header">
          <h3>{{ editing ? 'Edit provider' : 'Add provider' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="closeModal">
            <span class="mdi mdi-close"></span>
          </button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('oauth.displayName') }}</label>
              <input
                v-model="form.name"
                class="form-input"
                :placeholder="$t('oauth.googleWorkspace')"
                required
                autofocus
              />
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.type') }}</label>
              <select v-model="form.type" class="form-select">
                <option value="google">{{ $t('oauth.google') }}</option>
                <option value="oidc">{{ $t('oauth.genericOidc') }}</option>
              </select>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.slug" class="form-input" :placeholder="$t('oauth.googleSlug')" />
              <span class="form-hint">{{ $t('oauth.slugHint') }}</span>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.clientId') }}</label>
              <input v-model="form.client_id" class="form-input" required />
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.clientSecret') }}</label>
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
                <label class="form-label">{{ $t('oauth.issuer') }}</label>
                <input
                  v-model="form.issuer"
                  class="form-input"
                  :placeholder="$t('oauth.issuerPlaceholder')"
                />
                <span class="form-hint">{{ $t('oauth.issuerHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.authUrl') }}</label>
                <input v-model="form.auth_url" class="form-input" />
                <span class="form-hint">{{ $t('oauth.discoveryHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.tokenUrl') }}</label>
                <input v-model="form.token_url" class="form-input" />
                <span class="form-hint">{{ $t('oauth.discoveryHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.userinfoUrl') }}</label>
                <input v-model="form.userinfo_url" class="form-input" />
                <span class="form-hint">{{ $t('oauth.discoveryHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.emailClaim') }}</label>
                <input v-model="form.email_claim" class="form-input" placeholder="email" />
                <span class="form-hint">{{ $t('oauth.emailClaimHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.nameClaim') }}</label>
                <input v-model="form.name_claim" class="form-input" :placeholder="$t('oauth.nameClaimDefault')" />
                <span class="form-hint">{{ $t('oauth.nameClaimHint') }}</span>
              </div>
              <div class="form-group">
                <label class="form-label">{{ $t('oauth.usernameClaim') }}</label>
                <input v-model="form.username_claim" class="form-input" placeholder="preferred_username" />
                <span class="form-hint">{{ $t('oauth.usernameClaimHint') }}</span>
              </div>
            </template>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.scopes') }}</label>
              <input v-model="form.scopes" class="form-input" />
              <span class="form-hint">{{ $t('oauth.scopesHint') }}</span>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.allowedDomains') }}</label>
              <input v-model="form.allowed_domains" class="form-input" />
              <span class="form-hint">{{ $t('oauth.allowedDomainsHint') }}</span>
            </div>

            <div class="form-group">
              <label class="form-label">{{ $t('oauth.autoJoinWorkspace') }}</label>
              <div class="autojoin-row">
                <input
                  v-model="form.default_workspace_id"
                  class="form-input"
                  type="number"
                  min="0"
                  :placeholder="$t('oauth.workspaceId')"
                  :aria-label="$t('oauth.autoJoinWorkspaceId')"
                />
                <select v-model="form.default_role" class="form-select" :aria-label="$t('oauth.autoJoinRole')">
                  <option value="">{{ $t('oauth.noAutoJoin') }}</option>
                  <option value="viewer">{{ $t('oauth.viewer') }}</option>
                  <option value="developer">{{ $t('oauth.developer') }}</option>
                  <option value="admin">{{ $t('oauth.admin') }}</option>
                </select>
              </div>
              <span class="form-hint">{{ $t('oauth.autoJoinHint') }}</span>
            </div>

            <div class="form-group toggles" style="margin-bottom: 0">
              <label class="check-row">
                <input v-model="form.enabled" type="checkbox" />{{ $t('jobs.enabled') }}</label>
              <label class="check-row" :class="{ 'check-disabled': !hiddenCap.has.value && !form.hidden }">
                <input
                  v-model="form.hidden"
                  type="checkbox"
                  :disabled="!hiddenCap.has.value && !form.hidden"
                />{{ $t('oauth.hidden') }}<span v-if="!hiddenCap.has.value && !form.hidden" class="mdi mdi-lock-outline cap-lock" :title="hiddenTitle"></span>
              </label>
              <label class="check-row">
                <input v-model="form.auto_register" type="checkbox" />{{ $t('oauth.autoRegisterUsers') }}</label>
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="closeModal">{{ $t('action.cancel') }}</button>
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
      </AppModal>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      :title="$t('confirm.title.deleteProvider')"
      :message="$t('confirm.message.oAuthProviders.deleteProviderNameThis', { name: pendingDelete?.name })"
      :confirm-label="$t('action.delete')"
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
