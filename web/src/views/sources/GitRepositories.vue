<script setup lang="ts">
import { ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { gitRepositoryApi, type GitRepositoryInput } from '@/api/gitRepositories'
import type { GitRepository, GitAuthType } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CredentialSecretField from '@/components/CredentialSecretField.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const items = ref<GitRepository[]>([])
const loading = ref(false)
const testing = ref<number | null>(null)

const showModal = ref(false)
const saving = ref(false)
const editing = ref<GitRepository | null>(null)
const form = ref<GitRepositoryInput>({ name: '', url: '', auth_type: 'public', username: '', secret: '' })

async function load(id: number | null) {
  if (!id) { items.value = []; return }
  loading.value = true
  try {
    items.value = (await gitRepositoryApi.list(id)).data.data ?? []
  } catch (e) {
    notify.apiError(e)
  } finally {
    loading.value = false
  }
}
watch(currentWorkspaceId, load, { immediate: true })

function openCreate() {
  editing.value = null
  form.value = { name: '', url: '', auth_type: 'public', username: '', secret: '' }
  showModal.value = true
}
function openEdit(r: GitRepository) {
  editing.value = r
  form.value = { name: r.name, url: r.url, auth_type: r.auth_type, username: r.username, secret: '' }
  showModal.value = true
}

async function save() {
  if (!currentWorkspaceId.value) return
  saving.value = true
  try {
    if (editing.value) {
      await gitRepositoryApi.update(currentWorkspaceId.value, editing.value.id, form.value)
      notify.success('Git repository updated')
    } else {
      await gitRepositoryApi.create(currentWorkspaceId.value, form.value)
      notify.success('Git repository added')
    }
    showModal.value = false
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    saving.value = false
  }
}

async function test(r: GitRepository) {
  if (!currentWorkspaceId.value) return
  testing.value = r.id
  try {
    await gitRepositoryApi.test(currentWorkspaceId.value, r.id)
    notify.success(`${r.name}: connection succeeded`)
  } catch (e) {
    notify.apiError(e, 'Connection failed')
  } finally {
    testing.value = null
  }
}

const pendingDelete = ref<GitRepository | null>(null)
const deleting = ref(false)
async function confirmDelete() {
  if (!currentWorkspaceId.value || !pendingDelete.value) return
  deleting.value = true
  try {
    await gitRepositoryApi.remove(currentWorkspaceId.value, pendingDelete.value.id)
    notify.success('Git repository deleted')
    pendingDelete.value = null
    load(currentWorkspaceId.value)
  } catch (e) {
    notify.apiError(e)
  } finally {
    deleting.value = false
  }
}

const authTypes: { value: GitAuthType; label: string }[] = [
  { value: 'public', label: 'Public' },
  { value: 'token', label: 'HTTPS Token' },
  { value: 'ssh', label: 'SSH Key' },
]
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Git Repositories</h1>
        <p class="subtitle">Public or private repositories used at build time and by GitOps.</p>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New repository
      </button>
    </div>

    <div class="card">
      <div v-if="loading && items.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="items.length === 0" class="empty-state">
        <span class="mdi mdi-git" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No git repositories yet</h3>
        <p>Add a GitHub, GitLab, or Bitbucket repository — public, or private with a token or SSH key.</p>
        <button v-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">Add a repository</button>
      </div>
      <div v-else class="table-wrapper">
        <table>
          <thead><tr><th>Repository</th><th>URL</th><th>Auth</th><th></th></tr></thead>
          <tbody>
            <tr v-for="r in items" :key="r.id">
              <td>
                <div class="cell-id">
                  <span class="avatar avatar-sm"><span class="mdi mdi-git" style="font-size: 14px"></span></span>
                  <span class="cell-text">
                    <span class="cell-title">{{ r.display_name || r.name }}</span>
                    <span class="cell-sub">
                      <template v-if="r.secret_ref">
                        <span class="mdi mdi-key-variant"></span> {{ r.secret_ref }}
                      </template>
                      <template v-else>{{ r.has_secret ? 'secret set' : 'no secret' }}</template>
                    </span>
                  </span>
                </div>
              </td>
              <td class="cell-sub">{{ r.url }}</td>
              <td><span class="badge badge-neutral">{{ r.auth_type }}</span></td>
              <td class="text-right table-actions">
                <button class="btn-icon btn-icon-muted" title="Test connection" aria-label="Test connection" :disabled="testing === r.id" @click="test(r)">
                  <span class="mdi" :class="testing === r.id ? 'mdi-loading mdi-spin' : 'mdi-connection'"></span>
                </button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-muted" title="Edit" aria-label="Edit" @click="openEdit(r)"><span class="mdi mdi-pencil-outline"></span></button>
                <button v-if="ws.canEdit" class="btn-icon btn-icon-danger" title="Delete" aria-label="Delete" @click="pendingDelete = r"><span class="mdi mdi-delete-outline"></span></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="showModal" class="modal-overlay">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ editing ? 'Edit repository' : 'New repository' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showModal = false"><span class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="e.g. acme/api" required autofocus aria-label="Name" />
              </div>
              <div class="form-group">
                <label class="form-label">Repository URL</label>
                <input v-model="form.url" class="form-input" :placeholder="form.auth_type === 'ssh' ? 'git@github.com:acme/api.git' : 'https://github.com/acme/api.git'" required aria-label="Repository URL" />
              </div>
              <div class="form-group">
                <label class="form-label">Auth type</label>
                <div class="tabs" style="margin-bottom: 0">
                  <button v-for="t in authTypes" :key="t.value" type="button" class="tab" :class="{ active: form.auth_type === t.value }" @click="form.auth_type = t.value">{{ t.label }}</button>
                </div>
              </div>
              <template v-if="form.auth_type !== 'public'">
                <div class="form-group">
                  <label class="form-label">Username <span class="text-muted">(optional)</span></label>
                  <input v-model="form.username" class="form-input" :placeholder="form.auth_type === 'ssh' ? 'git' : 'x-access-token'" autocomplete="off" aria-label="Username" />
                </div>
                <CredentialSecretField
                  v-model="form.secret"
                  :label="form.auth_type === 'ssh' ? 'SSH private key' : 'Access token'"
                  :editing="!!editing"
                  :current-ref="editing?.secret_ref || ''"
                  :multiline="form.auth_type === 'ssh'"
                  :placeholder="form.auth_type === 'ssh' ? '-----BEGIN OPENSSH PRIVATE KEY-----' : 'ghp_…'"
                />
              </template>
              <p v-else class="text-muted" style="font-size: 12px; margin: 0">No credentials needed — the repository is cloned anonymously.</p>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showModal = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editing ? 'Save' : 'Add repository') }}</button>
            </div>
          </form>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog
      :open="!!pendingDelete"
      title="Delete git repository"
      :message="`Delete git repository &quot;${pendingDelete?.name}&quot;? Apps building from it will fail to clone.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
  </div>
</template>

<style scoped>
.subtitle { font-size: 13px; color: var(--text-muted); margin-top: 2px; }
.text-muted { color: var(--text-muted); font-weight: 400; }
.mdi-spin { animation: spin 0.8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
