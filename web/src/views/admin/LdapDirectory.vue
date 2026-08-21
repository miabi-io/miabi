<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { adminApi } from '@/api/admin'
import type { LdapConfig, LdapConfigPayload, LdapGroupMapping, LdapMappingPayload } from '@/api/admin'
import { useNotificationStore } from '@/stores/notification'
import { useLicenseStore } from '@/stores/license'
import { useEntitlement } from '@/composables/useEntitlement'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const notify = useNotificationStore()
const licenseStore = useLicenseStore()

// LDAP / Active Directory authentication is an Enterprise feature.
const ldap = useEntitlement('sso_ldap')
const lockTitle = 'LDAP / Active Directory authentication requires an Enterprise license'

const configs = ref<LdapConfig[]>([])
const loading = ref(false)

async function load() {
  if (!ldap.has.value) return
  loading.value = true
  try {
    configs.value = (await adminApi.listLdap()).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  licenseStore.load() // ensure entitlements are available for the gate
  load()
})

// --- Create / edit ---
const showForm = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)

function emptyForm(): LdapConfigPayload {
  return {
    display_name: '', host: '', port: 389, tls_mode: 'starttls', ca_cert_pem: '',
    insecure_skip_tls: false, timeout_seconds: 10, bind_dn: '', bind_password: '',
    user_base_dn: '', user_filter: '', attr_email: '', attr_name: '', attr_username: '',
    group_base_dn: '', group_filter: '', member_attr: '', nested_groups: false, enabled: true,
  }
}
const form = ref<LdapConfigPayload>(emptyForm())
const editingHasPassword = ref(false)

function openCreate() {
  editingId.value = null
  editingHasPassword.value = false
  form.value = emptyForm()
  showForm.value = true
}
function openEdit(c: LdapConfig) {
  editingId.value = c.id
  editingHasPassword.value = c.bind_password_set
  form.value = {
    display_name: c.display_name, name: c.name, host: c.host, port: c.port,
    tls_mode: c.tls_mode, ca_cert_pem: c.ca_cert_pem ?? '', insecure_skip_tls: c.insecure_skip_tls,
    timeout_seconds: c.timeout_seconds, bind_dn: c.bind_dn, bind_password: '',
    user_base_dn: c.user_base_dn, user_filter: c.user_filter, attr_email: c.attr_email,
    attr_name: c.attr_name, attr_username: c.attr_username, group_base_dn: c.group_base_dn,
    group_filter: c.group_filter, member_attr: c.member_attr, nested_groups: c.nested_groups,
    enabled: c.enabled,
  }
  showForm.value = true
}

async function save() {
  if (!form.value.display_name.trim() || !form.value.host.trim()) {
    notify.error('Display name and host are required')
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      await adminApi.updateLdap(editingId.value, form.value)
      notify.success('LDAP connection updated')
    } else {
      await adminApi.createLdap(form.value)
      notify.success('LDAP connection created')
    }
    showForm.value = false
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

// --- Test ---
const testingId = ref<number | null>(null)
async function test(c: LdapConfig) {
  testingId.value = c.id
  try {
    const res = (await adminApi.testLdap(c.id)).data.data
    if (res.ok) notify.success(res.message || 'Connection OK')
    else notify.error(res.error || 'Connection failed', { title: 'LDAP test failed' })
  } catch (e) {
    notify.apiError(e)
  } finally {
    testingId.value = null
  }
}

// --- Delete ---
const toDelete = ref<LdapConfig | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!toDelete.value) return
  deleting.value = true
  try {
    await adminApi.deleteLdap(toDelete.value.id)
    notify.success('LDAP connection deleted')
    toDelete.value = null
    await load()
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

// --- Group mappings ---
const mappingConfig = ref<LdapConfig | null>(null)
const mapForm = ref<LdapMappingPayload>({ group_dn: '', system_admin: false, workspace_id: null, workspace_role: 'viewer' })
const savingMapping = ref(false)
function openMappings(c: LdapConfig) {
  mappingConfig.value = c
  mapForm.value = { group_dn: '', system_admin: false, workspace_id: null, workspace_role: 'viewer' }
}
async function addMapping() {
  if (!mappingConfig.value || !mapForm.value.group_dn.trim()) {
    notify.error('A group DN is required')
    return
  }
  savingMapping.value = true
  try {
    const payload: LdapMappingPayload = {
      group_dn: mapForm.value.group_dn.trim(),
      system_admin: mapForm.value.system_admin,
      workspace_id: mapForm.value.workspace_id || null,
      workspace_role: mapForm.value.workspace_id ? mapForm.value.workspace_role : undefined,
    }
    await adminApi.createLdapMapping(mappingConfig.value.id, payload)
    notify.success('Mapping added')
    await refreshMappingConfig()
  } catch (e) {
    notify.apiError(e)
  } finally {
    savingMapping.value = false
  }
}
async function removeMapping(m: LdapGroupMapping) {
  if (!mappingConfig.value) return
  try {
    await adminApi.deleteLdapMapping(mappingConfig.value.id, m.id)
    notify.success('Mapping removed')
    await refreshMappingConfig()
  } catch (e) {
    notify.apiError(e)
  }
}
async function refreshMappingConfig() {
  await load()
  if (mappingConfig.value) {
    mappingConfig.value = configs.value.find((c) => c.id === mappingConfig.value?.id) ?? null
  }
}

const canWrite = computed(() => ldap.mutable.value)
function tlsLabel(m: string) {
  return m === 'ldaps' ? 'LDAPS' : m === 'starttls' ? 'StartTLS' : 'plain'
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>LDAP / Active Directory</h1>
        <p class="subtitle">Let users sign in with their corporate directory credentials. Groups drive platform-admin and workspace access.</p>
      </div>
      <button v-if="ldap.has.value" class="btn btn-primary" :disabled="!canWrite" :title="canWrite ? '' : 'License is read-only'" @click="openCreate">
        <span class="mdi mdi-plus"></span> Add connection
      </button>
    </div>

    <!-- Locked (Community / unlicensed) -->
    <div v-if="!ldap.has.value" class="card">
      <div class="empty-state">
        <span class="mdi mdi-shield-lock-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>Enterprise feature</h3>
        <p :title="lockTitle">Connect an LDAP directory or Active Directory to authenticate users against it, with groups mapped onto roles and workspace membership.</p>
        <router-link to="/admin/license" class="btn btn-secondary mt-4">Manage license →</router-link>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div v-if="loading && configs.length === 0" class="card-body"><span class="spinner"></span></div>
        <div v-else-if="configs.length === 0" class="empty-state">
          <span class="mdi mdi-account-key-outline" style="font-size: 44px; color: var(--text-muted)"></span>
          <h3>No directories connected</h3>
          <p>Add an LDAP or Active Directory connection so users can sign in with their directory username and password.</p>
          <button class="btn btn-primary mt-4" :disabled="!canWrite" @click="openCreate">Add a connection</button>
        </div>
        <div v-else class="table-wrapper">
          <table>
            <thead><tr><th>Name</th><th>Server</th><th>TLS</th><th>Groups</th><th>Status</th><th></th></tr></thead>
            <tbody>
              <tr v-for="c in configs" :key="c.id">
                <td>
                  <span class="cell-title">{{ c.display_name || c.name }}</span>
                  <span class="cell-sub mono">{{ c.name }}</span>
                </td>
                <td class="cell-sub mono">{{ c.host }}:{{ c.port }}</td>
                <td><span class="badge" :class="c.tls_mode === 'none' ? 'badge-warning' : 'badge-neutral'">{{ tlsLabel(c.tls_mode) }}</span></td>
                <td class="cell-sub">{{ (c.mappings || []).length }} mapping{{ (c.mappings || []).length === 1 ? '' : 's' }}</td>
                <td><span class="badge" :class="c.enabled ? 'badge-success' : 'badge-neutral'">{{ c.enabled ? 'enabled' : 'disabled' }}</span></td>
                <td class="text-right table-actions">
                  <button class="btn-icon btn-icon-muted" title="Test connection" aria-label="Test connection" :disabled="testingId === c.id" @click="test(c)">
                    <span class="mdi" :class="testingId === c.id ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>
                  </button>
                  <button class="btn-icon btn-icon-muted" title="Group mappings" aria-label="Group mappings" @click="openMappings(c)"><span class="mdi mdi-account-multiple-outline"></span></button>
                  <button class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" :disabled="!canWrite" @click="openEdit(c)"><span class="mdi mdi-pencil-outline"></span></button>
                  <button class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" :disabled="!canWrite" @click="toDelete = c"><span class="mdi mdi-delete-outline"></span></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- Create / edit modal -->
    <Teleport to="body">
      <div v-if="showForm" class="modal-overlay">
        <div class="modal" style="max-width: 640px; width: 100%">
          <div class="modal-header">
            <h3>{{ editingId ? 'Edit LDAP connection' : 'Add LDAP connection' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showForm = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Display name</label>
                <input v-model="form.display_name" class="form-input" placeholder="e.g. Corp Active Directory" required autofocus />
              </div>

              <h4 class="form-section">Connection</h4>
              <div class="form-row">
                <div class="form-group" style="flex: 2; margin-bottom: 0">
                  <label class="form-label">Host</label>
                  <input v-model="form.host" class="form-input mono" placeholder="ldap.corp.example.com" required />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Port</label>
                  <input v-model.number="form.port" type="number" class="form-input" placeholder="389" />
                </div>
              </div>
              <div class="form-row" style="margin-top: 12px">
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">TLS</label>
                  <select v-model="form.tls_mode" class="form-select" aria-label="TLS mode">
                    <option value="starttls">StartTLS (upgrade on 389)</option>
                    <option value="ldaps">LDAPS (implicit TLS, 636)</option>
                    <option value="none">None (plaintext — not recommended)</option>
                  </select>
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Timeout (s)</label>
                  <input v-model.number="form.timeout_seconds" type="number" class="form-input" placeholder="10" />
                </div>
              </div>
              <div class="form-group" style="margin-top: 12px">
                <label class="form-label">CA certificate (PEM, optional)</label>
                <textarea v-model="form.ca_cert_pem" class="form-input mono" rows="2" placeholder="-----BEGIN CERTIFICATE----- …"></textarea>
              </div>
              <label class="check-row">
                <input v-model="form.insecure_skip_tls" type="checkbox" />
                <span>Skip TLS certificate verification <span class="text-muted">(insecure — dev only)</span></span>
              </label>

              <h4 class="form-section">Service account (bind)</h4>
              <div class="form-group">
                <label class="form-label">Bind DN</label>
                <input v-model="form.bind_dn" class="form-input mono" placeholder="cn=miabi,ou=svc,dc=corp,dc=example,dc=com" />
              </div>
              <div class="form-group">
                <label class="form-label">Bind password</label>
                <input v-model="form.bind_password" type="password" class="form-input" autocomplete="new-password"
                  :placeholder="editingHasPassword ? '•••••••• (leave blank to keep)' : 'Service-account password'" />
              </div>

              <h4 class="form-section">User search</h4>
              <div class="form-group">
                <label class="form-label">User base DN</label>
                <input v-model="form.user_base_dn" class="form-input mono" placeholder="ou=Users,dc=corp,dc=example,dc=com" />
              </div>
              <div class="form-group">
                <label class="form-label">User filter <span class="text-muted">(%s = login)</span></label>
                <input v-model="form.user_filter" class="form-input mono" placeholder="(sAMAccountName=%s) or (uid=%s)" />
              </div>
              <div class="form-row">
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Email attr</label>
                  <input v-model="form.attr_email" class="form-input mono" placeholder="mail" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Name attr</label>
                  <input v-model="form.attr_name" class="form-input mono" placeholder="displayName" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Username attr</label>
                  <input v-model="form.attr_username" class="form-input mono" placeholder="sAMAccountName" />
                </div>
              </div>

              <h4 class="form-section">Groups <span class="text-muted">(optional — drives access)</span></h4>
              <div class="form-row">
                <div class="form-group" style="flex: 2; margin-bottom: 0">
                  <label class="form-label">Group base DN</label>
                  <input v-model="form.group_base_dn" class="form-input mono" placeholder="ou=Groups,dc=corp,dc=example,dc=com" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Member attr</label>
                  <input v-model="form.member_attr" class="form-input mono" placeholder="memberOf" />
                </div>
              </div>
              <div class="form-group" style="margin-top: 12px">
                <label class="form-label">Group filter <span class="text-muted">(%s = user DN; blank uses member attr)</span></label>
                <input v-model="form.group_filter" class="form-input mono" placeholder="(member=%s)" />
              </div>
              <label class="check-row">
                <input v-model="form.nested_groups" type="checkbox" />
                <span>Expand nested groups <span class="text-muted">(Active Directory)</span></span>
              </label>

              <label class="check-row">
                <input v-model="form.enabled" type="checkbox" />
                <span>Enabled</span>
              </label>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showForm = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editingId ? 'Save changes' : 'Create') }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <!-- Group mappings modal -->
    <Teleport to="body">
      <div v-if="mappingConfig" class="modal-overlay">
        <div class="modal" style="max-width: 620px; width: 100%">
          <div class="modal-header">
            <h3>Group mappings — {{ mappingConfig.display_name || mappingConfig.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="mappingConfig = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <p class="text-muted text-sm">Map a directory group (full DN or bare CN) onto platform-admin and/or a workspace role. Reconciled on each login.</p>
            <div v-if="(mappingConfig.mappings || []).length" class="table-wrapper" style="margin: 12px 0">
              <table>
                <thead><tr><th>Group</th><th>Grants</th><th></th></tr></thead>
                <tbody>
                  <tr v-for="m in mappingConfig.mappings" :key="m.id">
                    <td class="cell-sub mono">{{ m.group_dn }}</td>
                    <td class="cell-sub">
                      <span v-if="m.system_admin" class="badge badge-warning">platform admin</span>
                      <span v-if="m.workspace_id" class="badge badge-neutral">ws #{{ m.workspace_id }} · {{ m.workspace_role }}</span>
                    </td>
                    <td class="text-right">
                      <button class="btn-icon btn-icon-danger" title="Remove mapping" aria-label="Remove mapping" :disabled="!canWrite" @click="removeMapping(m)"><span class="mdi mdi-delete-outline"></span></button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else class="text-muted text-sm" style="margin: 12px 0">No mappings yet — without any, directory users are provisioned but get no admin or workspace access automatically.</p>

            <form v-if="canWrite" class="map-form" @submit.prevent="addMapping">
              <div class="form-group">
                <label class="form-label">Group DN or CN</label>
                <input v-model="mapForm.group_dn" class="form-input mono" placeholder="cn=platform-admins,ou=Groups,… (or just platform-admins)" required />
              </div>
              <div class="form-row" style="align-items: flex-end">
                <label class="check-row" style="flex: 1; margin: 0">
                  <input v-model="mapForm.system_admin" type="checkbox" />
                  <span>Platform admin</span>
                </label>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Workspace ID</label>
                  <input v-model.number="mapForm.workspace_id" type="number" class="form-input" placeholder="(optional)" />
                </div>
                <div class="form-group" style="flex: 1; margin-bottom: 0">
                  <label class="form-label">Workspace role</label>
                  <select v-model="mapForm.workspace_role" class="form-select" aria-label="Workspace role" :disabled="!mapForm.workspace_id">
                    <option value="owner">Owner</option>
                    <option value="admin">Admin</option>
                    <option value="developer">Developer</option>
                    <option value="viewer">Viewer</option>
                  </select>
                </div>
                <button type="submit" class="btn btn-primary" :disabled="savingMapping">Add</button>
              </div>
            </form>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete LDAP connection?"
      :message="`Delete &quot;${toDelete?.display_name || toDelete?.name}&quot;? Users provisioned from it keep their accounts but can no longer sign in via this directory.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.form-section { margin: 20px 0 10px; font-size: 13px; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.04em; }
.form-section:first-of-type { margin-top: 8px; }
.check-row { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-size: 14px; cursor: pointer; }
.mono { font-family: monospace; }
.map-form { border-top: 1px solid var(--border-primary); padding-top: 14px; margin-top: 8px; }
</style>
