<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import { configApi, type Config, type ConfigInput, type ConfigUsage } from '@/api/configs'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const configs = ref<Config[]>([])
const loading = ref(false)
const search = ref('')

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) { configs.value = []; return }
  loading.value = true
  try {
    const res = await configApi.list(id, search.value.trim(), page, pageable.value.size)
    configs.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) { notify.apiError(e) }
  finally { loading.value = false }
})

function reload() { goToPage(pageable.value.current_page) }
watch(currentWorkspaceId, () => goToPage(0))

let searchTimer: ReturnType<typeof setTimeout> | undefined
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => goToPage(0), 300)
}
onBeforeUnmount(() => { if (searchTimer) clearTimeout(searchTimer) })

function totalSize(c: Config): number {
  return Object.values(c.sizes || {}).reduce((a, b) => a + b, 0)
}
function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
// Rendered as data: a literal mustache cannot be written inline in a template.
const mustache = '{' + '{ }' + '}'

function fileLanguage(key: string): string {
  const ext = key.split('.').pop()?.toLowerCase() ?? ''
  return ({ yml: 'yaml', yaml: 'yaml', json: 'json', toml: 'toml', ini: 'ini', conf: 'conf' } as Record<string, string>)[ext] ?? 'text'
}

// --- Create / edit -------------------------------------------------------
// Files are edited as an ordered array so a key can be renamed without losing
// its position; it is converted back to the API's map on save.
type FileRow = { key: string; content: string }

const showForm = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const form = ref({ name: '', description: '', mode: '0644', sensitive: false, delimiters: '' })
const files = ref<FileRow[]>([{ key: '', content: '' }])
const activeFile = ref(0)
const affected = ref<ConfigUsage[]>([])

function openCreate() {
  editingId.value = null
  form.value = { name: '', description: '', mode: '0644', sensitive: false, delimiters: '' }
  files.value = [{ key: '', content: '' }]
  activeFile.value = 0
  affected.value = []
  showForm.value = true
}

async function openEdit(c: Config) {
  const id = currentWorkspaceId.value
  if (!id) return
  editingId.value = c.id
  form.value = {
    name: c.name,
    description: c.description || '',
    mode: c.mode || '0644',
    sensitive: c.sensitive,
    delimiters: (c.delimiters || []).join(','),
  }
  files.value = c.keys.map((k) => ({ key: k, content: '' }))
  activeFile.value = 0
  try {
    const data = (await configApi.reveal(id, c.id)).data.data?.data ?? {}
    files.value = Object.keys(data).sort().map((k) => ({ key: k, content: data[k] }))
    affected.value = (await configApi.usage(id, c.id)).data.data ?? []
    showForm.value = true
  } catch (e) {
    notify.apiError(e, 'Only admins can open a config for editing')
  }
}

function addFile() {
  files.value.push({ key: '', content: '' })
  activeFile.value = files.value.length - 1
}
function removeFile(i: number) {
  files.value.splice(i, 1)
  if (files.value.length === 0) files.value.push({ key: '', content: '' })
  activeFile.value = Math.min(activeFile.value, files.value.length - 1)
}

const formValid = computed(() =>
  (editingId.value !== null || /^[a-z0-9][a-z0-9-]*$/.test(form.value.name.trim())) &&
  files.value.length > 0 &&
  files.value.every((f) => f.key.trim() !== ''),
)

async function save() {
  const id = currentWorkspaceId.value
  if (!id || !formValid.value) return
  const data: Record<string, string> = {}
  for (const f of files.value) data[f.key.trim()] = f.content
  const delimiters = form.value.delimiters.trim()
    ? form.value.delimiters.split(',').map((s) => s.trim())
    : undefined

  saving.value = true
  const input: ConfigInput = {
    name: form.value.name.trim(),
    description: form.value.description,
    data,
    mode: form.value.mode.trim() || undefined,
    sensitive: form.value.sensitive,
    delimiters,
  }
  try {
    if (editingId.value) {
      await configApi.update(id, editingId.value, {
        data, description: input.description, mode: input.mode, delimiters,
      })
      notify.success(affected.value.length
        ? `Config updated — redeploying ${affected.value.length} app(s)`
        : 'Config updated')
      reload()
    } else {
      await configApi.create(id, input)
      notify.success('Config created')
      goToPage(0)
    }
    showForm.value = false
  } catch (e) { notify.apiError(e) }
  finally { saving.value = false }
}

// --- Delete --------------------------------------------------------------
const toDelete = ref<Config | null>(null)
const deleting = ref(false)

async function confirmDelete() {
  const id = currentWorkspaceId.value
  if (!id || !toDelete.value) return
  deleting.value = true
  try {
    await configApi.remove(id, toDelete.value.id)
    notify.success('Config deleted')
    toDelete.value = null
    reload()
  } catch (e) {
    notify.apiError(e, 'A config cannot be deleted while an application still mounts it')
  } finally { deleting.value = false }
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Configs</h1>
        <div class="text-muted text-sm subtitle">
          Configuration files mounted into applications as read-only files. Content is encrypted at rest.
        </div>
      </div>
      <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span> New config
      </button>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input v-model="search" class="form-input" type="search" placeholder="Search configs by name or description…"
            aria-label="Search configs" @input="onSearchInput" />
        </div>
        <span class="text-muted">{{ pageable.total_elements }} config{{ pageable.total_elements === 1 ? '' : 's' }}</span>
      </div>

      <div v-if="loading && configs.length === 0" class="card-body"><span class="spinner"></span></div>

      <div v-else-if="configs.length === 0" class="empty-state">
        <span class="mdi mdi-file-cog-outline" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No configs {{ search.trim() ? 'found' : '' }}</h3>
        <p v-if="search.trim()">Try a different search term.</p>
        <p v-else>
          Create a config to mount files like <code>prometheus.yml</code> or <code>redis.conf</code> into an app.
        </p>
        <button v-if="ws.canEdit && !search.trim()" class="btn btn-primary mt-4" @click="openCreate">New config</button>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Name</th>
              <th>Files</th>
              <th>Size</th>
              <th>Version</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in configs" :key="c.id">
              <td>
                <div class="cell-title">
                  {{ c.name }}
                  <span v-if="c.sensitive" class="badge badge-warning">sensitive</span>
                  <span v-if="c.managed" class="badge badge-neutral">managed</span>
                </div>
                <div v-if="c.description" class="cell-sub">{{ c.description }}</div>
              </td>
              <td>
                <span v-for="k in c.keys.slice(0, 3)" :key="k" class="badge badge-neutral file-chip">{{ k }}</span>
                <span v-if="c.keys.length > 3" class="cell-sub">+{{ c.keys.length - 3 }}</span>
              </td>
              <td>{{ humanBytes(totalSize(c)) }}</td>
              <td>v{{ c.version }}</td>
              <td class="text-right">
                <button v-if="ws.canEdit" class="btn btn-sm btn-secondary" @click="openEdit(c)">
                  <span class="mdi mdi-pencil"></span> Edit
                </button>
                <button v-if="ws.canEdit" class="btn btn-sm btn-danger" @click="toDelete = c">
                  <span class="mdi mdi-delete-outline"></span>
                </button>
              </td>
            </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @change="goToPage" />
      </template>
    </div>

    <!-- Create / edit -->
    <AppModal v-if="showForm" max-width="860px" @close="showForm = false">
      <div class="modal-header">
        <h3>{{ editingId ? 'Edit config' : 'New config' }}</h3>
        <button class="btn-icon btn-icon-muted" aria-label="Close" @click="showForm = false"><span class="mdi mdi-close"></span></button>
      </div>
      <div class="modal-body">
        <div class="form-row">
          <div class="form-group">
            <label class="form-label">Name</label>
            <input v-model="form.name" class="form-input" :disabled="editingId !== null" placeholder="prometheus-conf" />
            <p class="form-hint">Lowercase letters, digits and dashes.</p>
          </div>
          <div class="form-group">
            <label class="form-label">Default file mode</label>
            <input v-model="form.mode" class="form-input" placeholder="0644" />
          </div>
        </div>

        <div class="form-group">
          <label class="form-label">Description</label>
          <input v-model="form.description" class="form-input" placeholder="Prometheus scrape configuration" />
        </div>

        <div class="form-row">
          <div class="form-group">
            <label class="form-label">Delimiters</label>
            <input v-model="form.delimiters" class="form-input" placeholder="<<,>>" />
            <p class="form-hint">
              Set these when the file's own syntax uses <code>{{ mustache }}</code>, so it is not interpolated.
            </p>
          </div>
          <div class="form-group">
            <label class="form-label">Sensitive</label>
            <label class="checkbox">
              <input v-model="form.sensitive" type="checkbox" :disabled="editingId !== null" />
              <span>Content carries credentials</span>
            </label>
            <p class="form-hint">Redacted in responses and diffed by digest only.</p>
          </div>
        </div>

        <div v-if="editingId && affected.length" class="app-banner app-banner--warning">
          <span class="mdi mdi-restart"></span>
          <span>Saving redeploys {{ affected.length }} app(s): {{ affected.map((a) => a.name).join(', ') }}</span>
        </div>

        <div class="files">
          <div class="files-tabs">
            <button
              v-for="(f, i) in files"
              :key="i"
              class="file-tab"
              :class="{ active: i === activeFile }"
              @click="activeFile = i"
            >
              {{ f.key || 'untitled' }}
            </button>
            <button class="file-tab add" @click="addFile"><span class="mdi mdi-plus"></span></button>
          </div>

          <div v-if="files[activeFile]" class="file-editor">
            <div class="file-editor-head">
              <input
                v-model="files[activeFile].key"
                class="form-input"
                placeholder="prometheus.yml or rules/alerts.yml"
              />
              <span class="badge badge-neutral">{{ fileLanguage(files[activeFile].key) }}</span>
              <button class="btn btn-sm btn-danger" :disabled="files.length === 1" @click="removeFile(activeFile)">
                <span class="mdi mdi-delete-outline"></span>
              </button>
            </div>
            <textarea
              v-model="files[activeFile].content"
              class="form-input code-area"
              spellcheck="false"
              rows="16"
              placeholder="File content…"
            ></textarea>
            <p class="form-hint">
              Reference a workspace secret with <code v-pre>${{ secrets.NAME }}</code> or the mounting app's
              environment with <code v-pre>${{ env.NAME }}</code>. Values are substituted when the app deploys,
              so rotating a secret updates the file. A secret wins over an app variable of the same name, and a
              reference that resolves to nothing fails the deploy.
            </p>
          </div>
        </div>
      </div>
      <div class="modal-footer">
        <button class="btn btn-secondary" @click="showForm = false">Cancel</button>
        <button class="btn btn-primary" :disabled="!formValid || saving" @click="save">
          <span v-if="saving" class="spinner spinner-sm"></span>
          {{ editingId ? 'Save changes' : 'Create config' }}
        </button>
      </div>
    </AppModal>

    <ConfirmDialog
      :open="!!toDelete"
      title="Delete config"
      :message="`Delete config &quot;${toDelete?.name}&quot; and its ${toDelete?.keys.length ?? 0} file(s)? An application still mounting it blocks the delete.`"
      confirm-label="Delete"
      variant="danger"
      :busy="deleting"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </div>
</template>

<style scoped>
.text-muted { color: var(--text-muted); }

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.search { position: relative; flex: 1; max-width: 360px; }
.search .mdi {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-muted);
  pointer-events: none;
}
.search .form-input { padding-left: 32px; }

/* No shared two-column form class exists; each view defines its own. */
.form-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 16px;
}

.file-chip { margin-right: 4px; }

.files { margin-top: 16px; }
.files-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  border-bottom: 1px solid var(--border-primary);
  margin-bottom: 12px;
}
.file-tab {
  padding: 6px 12px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
}
.file-tab.active {
  color: var(--primary-600);
  border-bottom-color: var(--primary-600);
  font-weight: 500;
}
.file-tab.add { color: var(--text-muted); }

.file-editor-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.code-area {
  font-family: 'SF Mono', ui-monospace, Menlo, Consolas, monospace;
  font-size: 13px;
  line-height: 1.5;
  white-space: pre;
  overflow-x: auto;
}
</style>
