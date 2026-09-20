<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { storeToRefs } from 'pinia'
import { useWorkspaceStore } from '@/stores/workspace'
import { useNotificationStore } from '@/stores/notification'
import GeneratorPanel from '@/components/GeneratorPanel.vue'
import GenerateButton from '@/components/GenerateButton.vue'
import SaveAsSecret from '@/components/SaveAsSecret.vue'
import { secretApi, type SecretInput, type SecretOwnership } from '@/api/secrets'
import type { Secret } from '@/api/types'
import { usePagination } from '@/composables/usePagination'
import { copyText } from '@/utils/clipboard'
import Pagination from '@/components/Pagination.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import AppModal from '@/components/AppModal.vue'

const ws = useWorkspaceStore()
const { t } = useI18n()
const notify = useNotificationStore()
const { currentWorkspaceId } = storeToRefs(ws)

const secrets = ref<Secret[]>([])
const loading = ref(false)
const search = ref('')
// Ownership is filtered by the API, not in this page: the list is paged, so
// hiding rows here would leave the count and the page size disagreeing with
// what's on screen.
const ownership = ref<SecretOwnership>('all')
const ownershipFilters: Array<{ value: SecretOwnership; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'unmanaged', label: 'Not managed' },
  { value: 'managed', label: 'Managed' },
]

const { pageable, goToPage } = usePagination(async (page) => {
  const id = currentWorkspaceId.value
  if (!id) { secrets.value = []; return }
  loading.value = true
  try {
    const res = await secretApi.list(id, search.value.trim(), page, pageable.value.size, ownership.value)
    secrets.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) { notify.apiError(e) }
  finally { loading.value = false }
})

// Reload the current page (e.g. after a create/edit/delete).
function reload() { goToPage(pageable.value.current_page) }
// Switching workspaces resets to the first page.
watch(currentWorkspaceId, () => goToPage(0))
// Narrowing can leave the current page past the end of the result, so every
// filter change re-queries from the first page.
function setOwnership(v: SecretOwnership) {
  ownership.value = v
  goToPage(0)
}

// Whether the view is showing a subset — drives the empty state, which must not
// read as "the vault is empty" when it is really "nothing matches".
const narrowed = computed(() => !!search.value.trim() || ownership.value !== 'all')
function clearFilters() {
  search.value = ''
  setOwnership('all')
}

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
    notify.success(t(editingId.value ? 'notify.secrets.updated' : 'notify.secrets.created'))
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
    notify.success(t('notify.secrets.deleted'))
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
  if (await copyText(text)) notify.success(t('notify.common.copied'))
  else notify.error(t('notify.common.copyFailedSelectAndCopy'))
}
function reference(s: Secret) {
  return `\${{ secrets.${s.name} }}`
}
// Literal reference examples (built in script so the `}}` doesn't confuse the
// The generator modal: the deliberate "make me a value" path from the vault.
// It shares GeneratorPanel with the /generator page and the inline button, so
// the three cannot drift.
const showGenerator = ref(false)
const generatorSaving = ref(false)

async function saveGenerated(name: string, description: string, value: string): Promise<boolean> {
  const id = currentWorkspaceId.value
  if (!id) return false
  generatorSaving.value = true
  try {
    await secretApi.create(id, { name, value, description })
    notify.success(t('notify.secrets.namedCreated', { name }))
    showGenerator.value = false
    reload()
    return true
  } catch (e) {
    notify.apiError(e)
    return false
  } finally {
    generatorSaving.value = false
  }
}

// Vue template's mustache parser).
const refExample = 'PASSWORD=${{ secrets.NAME }}'
const refForName = computed(() => `\${{ secrets.${form.value.name || 'name'} }}`)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>{{ $t('secrets.secrets') }}</h1>
        <i18n-t keypath="secrets.subtitle" tag="div" class="text-muted text-sm subtitle"><template #ref><code>{{ refExample }}</code></template></i18n-t>
      </div>
      <div class="header-actions">
        <button v-if="ws.canEdit" class="btn btn-secondary" @click="showGenerator = true"><span
            class="mdi mdi-auto-fix"></span>{{ $t('secrets.generator') }}</button>
        <button v-if="ws.canEdit" class="btn btn-primary" @click="openCreate"><span class="mdi mdi-plus"></span>{{ $t('secrets.newSecret') }}</button>
      </div>
    </div>

    <div class="card">
      <div class="card-body toolbar">
        <div class="search">
          <span class="mdi mdi-magnify"></span>
          <input v-model="search" class="form-input" type="search" :placeholder="$t('secrets.searchPlaceholder')"
            :aria-label="$t('secrets.searchSecrets')" style="max-width: 320px" @input="onSearchInput" />
        </div>
        <div class="filters" role="group" :aria-label="$t('secrets.filterByOwnership')">
          <button v-for="f in ownershipFilters" :key="f.value" type="button" class="chip"
            :class="{ active: ownership === f.value }" :aria-pressed="ownership === f.value"
            @click="setOwnership(f.value)">{{ f.label }}</button>
        </div>
        <span class="text-muted">{{ pageable.total_elements }} secret{{ pageable.total_elements === 1 ? '' : 's'
          }}</span>
      </div>

      <div v-if="loading && secrets.length === 0" class="card-body"><span class="spinner"></span></div>
      <div v-else-if="secrets.length === 0" class="empty-state">
        <span class="mdi mdi-key-variant" style="font-size: 44px; color: var(--text-muted)"></span>
        <h3>No secrets {{ narrowed ? 'found' : '' }}</h3>
        <!-- An empty result under a filter is not an empty vault: offering
             "New secret" here would answer a question the user didn't ask. -->
        <p v-if="narrowed">{{ $t('secrets.noMatches') }}</p>
        <p v-else>{{ $t('secrets.emptyHint') }}</p>
        <button v-if="narrowed" class="btn btn-secondary mt-4" @click="clearFilters">{{ $t('secrets.clearFilters') }}</button>
        <button v-else-if="ws.canEdit" class="btn btn-primary mt-4" @click="openCreate">{{ $t('secrets.newSecret') }}</button>
      </div>
      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>{{ $t('apps.form.name') }}</th>
                <th>{{ $t('secrets.reference') }}</th>
                <th>{{ $t('secrets.version') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in secrets" :key="s.id" @click="openDetails(s)" class="cursor-pointer">
                <td class="cell-title" style="font-family: monospace">
                  {{ s.name }}
                  <span v-if="s.managed" class="badge badge-muted"
                    :title="$t('secrets.managedHint')"
                    style="margin-left: 6px"><span class="mdi mdi-database-outline"></span>{{ $t('secrets.managed') }}</span>
                </td>
                <td class="cell-sub" style="font-family: monospace">
                  {{ reference(s) }}
                  <button class="btn-icon btn-icon-muted" :title="$t('secrets.copyReference')" :aria-label="$t('secrets.copyReference')"
                    @click.stop="copy(reference(s))"><span class="mdi mdi-content-copy"></span></button>
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
                    <button v-if="ws.isWorkspaceAdmin" class="btn-icon btn-icon-muted" :title="$t('secrets.revealValue')"
                      :aria-label="$t('secrets.revealValue')" @click.stop="openDetails(s)"><span
                        class="mdi mdi-eye-outline"></span></button>
                    <button v-if="ws.canEdit && !s.managed" class="btn-icon btn-icon-muted" :title="$t('action.edit')"
                      :aria-label="$t('action.edit')" @click.stop="openEdit(s)"><span class="mdi mdi-pencil-outline"></span></button>
                    <button v-if="ws.canEdit && !s.managed" class="btn-icon btn-icon-danger" :title="$t('action.delete')"
                      :aria-label="$t('action.delete')" @click.stop="toDelete = s"><span
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
      <!-- Generator -->
      <AppModal v-if="showGenerator" @close="showGenerator = false">
        <div class="modal-header">
          <h3>{{ $t('secrets.generateAValue') }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showGenerator = false"><span
              class="mdi mdi-close"></span></button>
        </div>
        <div class="modal-body">
          <GeneratorPanel>
            <template #actions="{ value }">
              <SaveAsSecret :value="value" :saving="generatorSaving" :save="saveGenerated" />
            </template>
          </GeneratorPanel>
        </div>
      </AppModal>

      <!-- Create / edit -->
      <AppModal v-if="showForm" @close="showForm = false">
        <div class="modal-header">
          <h3>{{ editingId ? 'Edit secret' : 'New secret' }}</h3>
          <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="showForm = false"><span
              class="mdi mdi-close"></span></button>
        </div>
        <form @submit.prevent="save">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">{{ $t('apps.form.name') }}</label>
              <input v-model="form.name" class="form-input" placeholder="db_password" :disabled="!!editingId"
                :aria-label="$t('apps.form.name')" required autofocus style="font-family: monospace" />
              <i18n-t keypath="secrets.nameHint" tag="p" class="form-hint">
                <template #underscore><code>_</code></template>
                <template #hyphen><code>-</code></template>
                <template #ref><code>{{ refForName }}</code></template>
              </i18n-t>
            </div>
            <div class="form-group">
              <label class="form-label">{{ $t('secrets.value') }} <span v-if="editingId" class="text-muted">{{ $t('secrets.keepCurrent') }}</span>
                <GenerateButton v-if="ws.canEdit" :label="$t('secrets.generateValueLabel')"
                  @generated="form.value = $event" />
              </label>
              <textarea v-model="form.value" class="form-input" rows="8" :required="!editingId"
                :placeholder="$t('secrets.valuePlaceholder')"
                :aria-label="$t('secrets.value')" style="font-family: monospace; white-space: pre"></textarea>
              <p class="form-hint">{{ $t('secrets.valueHint') }}</p>
            </div>
            <div class="form-group" style="margin-bottom: 0">
              <label class="form-label">{{ $t('plans.description') }}<span class="text-muted">{{ $t('secrets.optional') }}</span></label>
              <input v-model="form.description" class="form-input" :placeholder="$t('secrets.descriptionPlaceholder')"
                :aria-label="$t('plans.description')" />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showForm = false">{{ $t('action.cancel') }}</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">{{ saving ? 'Saving…' : (editingId ?
              'Save' :
              'Create') }}</button>
          </div>
        </form>
      </AppModal>

      <!-- Reveal -->
      <!--       <div v-if="revealed" class="modal-overlay">
        <div class="modal" style="max-width: 560px; width: 100%">
          <div class="modal-header">
            <h3>{{ revealed.name }}</h3>
            <button class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="revealed = null"><span class="mdi mdi-close"></span></button>
          </div>
          <div class="modal-body">
            <div class="dns-field">
              <span class="dns-field-label">{{ $t('secrets.value') }}</span>
              <div class="dns-field-row">
                <span class="dns-field-value secret-text">{{ revealed.value }}</span>
                <button class="btn-icon btn-icon-muted" :title="$t('secrets.copy')" :aria-label="$t('secrets.copy')" @click="copy(revealed.value)"><span class="mdi mdi-content-copy"></span></button>
              </div>
            </div>
          </div>
        </div>
      </div> -->
      <AppModal v-if="showDetails && selectedSecret" @close="closeDetails">
        <div class="modal-header">
          <h3>{{ $t('secrets.secretDetails') }}</h3>
          <button type="button" class="btn-icon btn-icon-muted" :aria-label="$t('shell.close')" @click="closeDetails">
            <span class="mdi mdi-close"></span>
          </button>
        </div>

        <div class="modal-body">
          <div class="detail-group">
            <label class="form-label">{{ $t('apps.form.name') }}</label>
            <div class="code-box">
              <code>{{ selectedSecret.name }}</code>
              <button type="button" class="btn-icon btn-icon-muted" :title="$t('secrets.copyKey')" @click="copy(selectedSecret.name)">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
          </div>

          <div class="detail-group">
            <label class="form-label">{{ $t('secrets.reference') }}</label>
            <div class="code-box">
              <code>{{ reference(selectedSecret) }}</code>
              <button type="button" class="btn-icon btn-icon-muted" :title="$t('secrets.copyReference')"
                @click="copy(reference(selectedSecret))">
                <span class="mdi mdi-content-copy"></span>
              </button>
            </div>
          </div>

          <div class="detail-group">
            <label class="form-label">{{ $t('secrets.value') }}</label>
            <div class="code-box code-box-block">
              <code class="secret-text">{{ revealed?.name === selectedSecret.name ? revealed.value : '••••••••••••••••' }}</code>
              <div class="code-actions">
                <button type="button" class="btn-icon btn-icon-muted" :disabled="revealingId === selectedSecret.id"
                  @click="reveal(selectedSecret)">
                  <span v-if="revealingId === selectedSecret.id" class="mdi mdi-loading mdi-spin"></span>
                  <span v-else class="mdi"
                    :class="revealed?.name === selectedSecret.name ? 'mdi-eye-off' : 'mdi-eye'"></span>
                </button>

                <button v-if="revealed?.name === selectedSecret.name" type="button" class="btn-icon btn-icon-muted"
                  :title="$t('routes.copyValue')" @click="copy(revealed.value)">
                  <span class="mdi mdi-content-copy"></span>
                </button>
              </div>
            </div>
          </div>

          <div class="detail-group" style="margin-bottom: 0">
            <label class="form-label">{{ $t('plans.description') }}</label>
            <p class="detail-text">{{ selectedSecret.description || 'No description provided.' }}</p>
          </div>
        </div>

        <div class="modal-footer">
          <button type="button" class="btn btn-secondary" @click="closeDetails">{{ $t('shell.close') }}</button>
          <button type="button" class="btn btn-primary" @click="closeDetails(); openEdit(selectedSecret)">
            <span class="mdi mdi-pencil"></span>{{ $t('action.edit') }}</button>
        </div>
      </AppModal>
    </Teleport>

    <ConfirmDialog :open="!!toDelete" :title="$t('confirm.title.deleteSecret')"
      :message="$t('confirm.message.secrets.deleteSecretNameApps', { name: toDelete?.name })"
      :confirm-label="$t('action.delete')" variant="danger" :busy="deleting" @confirm="confirmDelete" @cancel="toDelete = null" />
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

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

.filters {
  display: flex;
  gap: 6px;
}

.chip {
  padding: 4px 12px;
  font-size: 12px;
  border-radius: 999px;
  cursor: pointer;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text-muted);
  white-space: nowrap;
}

.chip.active {
  border-color: var(--primary);
  color: var(--primary);
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

/* A secret is stored byte for byte, so it has to be shown that way: a
   certificate or private key is multi-line, and collapsing the line breaks —
   which is what HTML does by default — makes an intact value look mangled. */
.code-box-block {
  align-items: flex-start;
  gap: 8px;
}

.secret-text {
  flex: 1;
  min-width: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: normal;
  max-height: 260px;
  overflow-y: auto;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  font-size: 12px;
  line-height: 1.5;
}

.code-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: 12px;
}
</style>
