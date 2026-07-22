<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { secretApi, type SecretInput } from '@/api/secrets'
import type { Secret } from '@/api/types'
import { usePagination } from '@/composables/usePagination'
import { copyText } from '@/utils/clipboard'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const secrets = ref<Secret[]>([])
const loading = ref(false)
const search = ref('')

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) { secrets.value = []; return }
  loading.value = true
  try {
    const res = await secretApi.list(id, search.value.trim(), page, pageable.value.size)
    secrets.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) { notify.apiError(e) }
  finally { loading.value = false }
})

// Reload the current page (e.g. after a create/edit/delete).
function reload() { goToPage(pageable.value.current_page) }
// Switching workspaces resets to the first page.
watch(currentWorkspaceId, () => goToPage(0))

let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}
onBeforeUnmount(() => { if (searchTimer) clearTimeout(searchTimer) })

// --- Create / edit ---
const showForm = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const form = ref<{ name: string; value: string; description: string }>({ name: '', value: '', description: '' })

function openCreate() {
  editingId.value = null
  form.value = { name: '', value: '', description: '' }
  showForm.value = true
}
function openEdit(s: Secret) {
  editingId.value = s.id
  form.value = { name: s.name, value: '', description: s.description || '' }
  showForm.value = true
}
async function save() {
  const id = currentWorkspaceId.value
  if (!id) return
  saving.value = true
  const input: SecretInput = { name: form.value.name.trim(), value: form.value.value, description: form.value.description }
  try {
    if (editingId.value) await secretApi.update(id, editingId.value, { value: form.value.value, description: form.value.description })
    else await secretApi.create(id, input)
    notify.success(editingId.value ? 'Secret updated' : 'Secret created')
    showForm.value = false
    if (editingId.value) reload()
    else goToPage(0)
  } catch (e) { notify.apiError(e) }
  finally { saving.value = false }
}

// --- Reveal ---
const revealed = ref<{ name: string; value: string } | null>(null)
const revealingId = ref<number | null>(null)
async function reveal(s: Secret) {
  const id = currentWorkspaceId.value
  if (!id) return

  if (revealed.value?.name === s.name) {
    revealed.value = null
    return
  }

  revealingId.value = s.id
  try {
    const v = (await secretApi.reveal(id, s.id)).data.data
    revealed.value = { name: s.name, value: v?.value ?? '' }
  } catch (e) {
    notify.apiError(e, 'Only admins can reveal a secret')
  } finally {
    revealingId.value = null
  }
}
// --- Delete ---
const toDelete = ref<Secret | null>(null)
const deleting = ref(false)
const showDetails = ref(false)
const selectedSecret = ref<Secret | null>(null)

function openDetails(s: Secret) {
  selectedSecret.value = s
  if (revealed.value?.name !== s.name) {
    revealed.value = null
  }
  showDetails.value = true
}
function closeDetails() {
  showDetails.value = false
  revealed.value = null
}
async function confirmDelete() {
  const id = currentWorkspaceId.value
  if (!id || !toDelete.value) return
  deleting.value = true
  try {
    await secretApi.remove(id, toDelete.value.id)
    notify.success('Secret deleted')
    toDelete.value = null
    // Step back a page if we just removed the last row on a non-first page.
    const page = secrets.value.length === 1 && pageable.value.current_page > 0
      ? pageable.value.current_page - 1
      : pageable.value.current_page
    goToPage(page)
  } catch (e) { notify.apiError(e) }
  finally { deleting.value = false }
}

async function copy(text: string) {
  if (await copyText(text)) notify.success('Copied')
  else notify.error('Copy failed — select and copy it manually')
}
function reference(s: Secret) {
  return `\${{ secrets.${s.name} }}`
}
// Literal reference examples (built in script so the `}}` doesn't confuse the
// Vue template's mustache parser).
const refExample = 'PASSWORD=${{ secrets.NAME }}'
const refForName = computed(() => `\${{ secrets.${form.value.name || 'name'} }}`)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Secrets</h1>
        <div class="text-muted text-sm subtitle">Reference these from any app's env, e.g. <code>{{ refExample }}</code>
          Values are resolved at deploy time and never shown unless revealed.</div>
      </div>
      <div class="page-header-actions">
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate"><span class="mdi mdi-plus"></span> New
          secret</button>
      </div>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input v-model="search" class="form-input" type="search" placeholder="Search secrets by name or description…"
            aria-label="Search secrets" style="max-width: 320px" @input="onSearchInput" />
        </div>
        <span class="text-muted">{{ pageable.total_elements }} secret{{ pageable.total_elements === 1 ? '' : 's'
          }}</span>
      </div>

      <div v-if="loading && secrets.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="secrets.length === 0" class="empty-state">
        <span class="mdi mdi-key-variant" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No secrets {{ search.trim() ? 'found' : '' }}</h3>
        <p v-if="search.trim()">No secrets match your search.</p>
        <p v-else>Store a secret once and reference it from many apps. Rotating it updates every consumer on next
          deploy.</p>
        <button v-if="ws.canEdit && !search.trim()" class="btn btn-primary mt-4" @click="openCreate">New secret</button>
      </div>
      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Reference</th>
                <th>Description</th>
                <th>Version</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in secrets" :key="s.id" @click="openDetails(s)" class="cursor-pointer">
                <td class="cell-title" style="font-family: monospace">
                  {{ s.name }}
                  <span v-if="s.managed" class="badge badge-muted"
                    title="Auto-created and managed by a database; rotate or remove via that database"
                    style="margin-left: 6px"><span class="mdi mdi-database-outline"></span> managed</span>
                </td>
                <td class="cell-sub" style="font-family: monospace">
                  {{ reference(s) }}
                  <button class="btn-icon btn-icon-muted" title="Copy reference" aria-label="Copy reference"
                    @click.stop="copy(reference(s))"><span class="mdi mdi-content-copy"></span></button>
                </td>
                <td class="cell-sub">
                  <div class="table-desc">
                    {{ s.description || '—' }}
                  </div>
                </td>
                <td class="cell-sub">v{{ s.version }}</td>
                <td class="text-right">
                  <div class="table-actions">
                    <button type="button" class="btn-icon btn-icon-muted"
                      :title="revealed?.name === s.name ? 'View secret' : 'Reveal & view secret'"
                      :disabled="revealingId === s.id" @click.stop="reveal(s); openDetails(s)">
                      <span v-if="revealingId === s.id" class="mdi mdi-loading mdi-spin"></span>
                      <span v-else class="mdi"
                        :class="revealed?.name === s.name ? 'mdi-lock-open-outline' : 'mdi-lock-outline'"></span>
                    </button>
                    <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" title="Reveal value"
                      aria-label="Reveal value" @click.stop="openDetails(s)"><span
                        class="mdi mdi-eye-outline"></span></button>
                    <button v-if="ws.canEdit && !s.managed" class="btn-icon btn-icon-muted" title="Edit"
                      aria-label="Edit" @click.stop="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
                    <button v-if="ws.canEdit && !s.managed" class="btn-icon btn-icon-danger" title="Delete"
                      aria-label="Delete" @click.stop="toDelete = s"><span
                        class="mdi mdi-delete-outline"></span></button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>

    <Pagination :pageable="pageable" @page="goToPage" />

    <Teleport to="body">
      <!-- Create / edit -->
      <div v-if="showForm" class="modal-overlay" @click.self="showForm = false">
        <div class="modal">
          <div class="modal-header">
            <h3>{{ editingId ? 'Edit secret' : 'New secret' }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showForm = false"><span
                class="mdi mdi-close"></span></button>
          </div>
          <form @submit.prevent="save">
            <div class="modal-body">
              <div class="form-group">
                <label class="form-label">Name</label>
                <input v-model="form.name" class="form-input" placeholder="db_password" :disabled="!!editingId"
                  aria-label="Name" required autofocus style="font-family: monospace" />
                <p class="form-hint">Letters, digits, <code>_</code> or <code>-</code>. Referenced as <code>{{ refForName
                }}</code>.</p>
              </div>
              <div class="form-group">
                <label class="form-label">Value <span v-if="editingId" class="text-muted">(leave blank to keep
                    current)</span></label>
                <textarea v-model="form.value" class="form-input" rows="3" :required="!editingId"
                  placeholder="the secret value" aria-label="Value" style="font-family: monospace"></textarea>
              </div>
              <div class="form-group" style="margin-bottom: 0">
                <label class="form-label">Description <span class="text-muted">(optional)</span></label>
                <input v-model="form.description" class="form-input" placeholder="e.g. Postgres app password"
                  aria-label="Description" />
              </div>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-secondary" @click="showForm = false">Cancel</button>
              <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editingId ?
                'Save' :
                'Create') }}</button>
            </div>
          </form>
        </div>
      </div>

      <!-- Reveal -->
      <!--       <div v-if="revealed" class="modal-overlay" @click.self="revealed = null">
        <div class="modal" style="max-width: 560px; width: 100%">
          <div class="modal-header">
            <h3>{{ revealed.name }}</h3>
            <button class="btn-icon btn-icon-muted" aria-label="Close" @click="revealed = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="dns-field">
              <span class="dns-field-label">Value</span>
              <div class="dns-field-row">
                <span class="dns-field-value" style="word-break: break-all">{{ revealed.value }}</span>
                <button class="btn-icon btn-icon-muted" title="Copy" aria-label="Copy" @click="copy(revealed.value)"><span class="mdi mdi-content-copy"></span></button>
              </div>
            </div>
          </div>
        </div>
      </div> -->
      <div v-if="showDetails && selectedSecret" class="modal-overlay" @click.self="closeDetails">
        <div class="modal">
          <div class="modal-header">
            <h3>Secret details</h3>
            <button type="button" class="btn-icon btn-icon-muted" aria-label="Close" @click="closeDetails">
              <span class="mdi mdi-close"></span>
            </button>
          </div>

          <div class="modal-body">
            <div class="detail-group">
              <label class="form-label">Name</label>
              <div class="code-box">
                <code>{{ selectedSecret.name }}</code>
                <button type="button" class="btn-icon btn-icon-muted" title="Copy key" @click="copy(selectedSecret.name)">
                  <span class="mdi mdi-content-copy"></span>
                </button>
              </div>
            </div>

            <div class="detail-group">
              <label class="form-label">Reference</label>
              <div class="code-box">
                <code>{{ reference(selectedSecret) }}</code>
                <button type="button" class="btn-icon btn-icon-muted" title="Copy reference"
                  @click="copy(reference(selectedSecret))">
                  <span class="mdi mdi-content-copy"></span>
                </button>
              </div>
            </div>

            <div class="detail-group">
              <label class="form-label">Value</label>
              <div class="code-box">
                <code class="secret-text">
              {{ revealed?.name === selectedSecret.name ? revealed.value : '••••••••••••••••' }}
            </code>
                <div class="code-actions">
                  <button type="button" class="btn-icon btn-icon-muted" :disabled="revealingId === selectedSecret.id"
                    @click="reveal(selectedSecret)">
                    <span v-if="revealingId === selectedSecret.id" class="mdi mdi-loading mdi-spin"></span>
                    <span v-else class="mdi"
                      :class="revealed?.name === selectedSecret.name ? 'mdi-eye-off' : 'mdi-eye'"></span>
                  </button>

                  <button v-if="revealed?.name === selectedSecret.name" type="button" class="btn-icon btn-icon-muted"
                    title="Copy value" @click="copy(revealed.value)">
                    <span class="mdi mdi-content-copy"></span>
                  </button>
                </div>
              </div>
            </div>

            <div class="detail-group" style="margin-bottom: 0">
              <label class="form-label">Description</label>
              <p class="detail-text">{{ selectedSecret.description || 'No description provided.' }}</p>
            </div>
          </div>

          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="closeDetails">Close</button>
            <button type="button" class="btn btn-primary" @click="closeDetails(); openEdit(selectedSecret)">
              <span class="mdi mdi-pencil"></span> Edit
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <ConfirmDialog :open="!!toDelete" title="Delete secret"
      :message="`Delete secret &quot;${toDelete?.name}&quot;? Apps that reference it will fail to deploy until the reference is removed.`"
      confirm-label="Delete" variant="danger" :busy="deleting" @confirm="confirmDelete" @cancel="toDelete = null" />
  </div>
</template>

<style scoped>
.text-muted {
  color: var(--text-muted);
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.search {
  position: relative;
  flex: 1;
  max-width: 360px;
}

.search .mdi {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  pointer-events: none;
}

.search .form-input {
  padding-left: 32px;
}

code {
  background: var(--bg-tertiary);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
  font-family: monospace;
}

.form-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 4px;
}

.dns-field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.03em;
}

.dns-field-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 4px;
}

.dns-field-value {
  font-family: monospace;
  font-size: 13px;
}

.detail-group {
  margin-bottom: 16px;
}

.detail-text {
  font-size: 14px;
  color: var(--text-secondary);
  margin: 0;
}

.code-box {
  display: flex;
  align-items: center;
  justify-content: space-between;
  border: 1px solid var(--border-secondary);
  border-radius: var(--radius-sm);
  padding: 8px 12px;
  font-family: monospace;
  font-size: 13px;
}

.code-box code {
  color: var(--text-primary);
  word-break: break-all;
}

.code-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: 12px;
}
</style>
